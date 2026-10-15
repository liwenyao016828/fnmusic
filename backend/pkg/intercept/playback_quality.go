package intercept

import (
	"context"
	"strings"
	"time"

	"fn-lx-player/pkg/online"
)

// 「试听音质」：偏好、阶梯、以及「这首歌这次实际用了哪条源、哪一档」。
//
// # 用户口径（2026-10-07 指令）
//
// 「飞牛音乐页面在线播放的时候，播放音质用的哪个源，可以有切换的按钮吗？」
// 「增加音质阶梯：试听音质按 flac → 320k → 128k 逐级递减，能上无损就上无损。」
//
// 于是这一层要回答两个问题，缺一不可：
//
//  1. **按什么档位取音** —— 阶梯在 `pkg/online` 的 `PlaybackLadder`，这里只决定
//     用哪一条阶梯（偏好）。
//  2. **这次实际用的是什么** —— 必须能**如实**报给用户。档位是解析时**回读**
//     出来的（平台会静默降级：网易要 lossless 给 exhigh，不报错、不给 null），
//     源也可能是下载源池换过的。页面自己猜不出来，猜出来的结果就是骗人。
//
// 改这一层之前先读 `pkg/online/tier.go` 顶部那段：那里写着真机实测的
// 「未登录到底能拿到什么」，很多看起来像 bug 的现象（QQ 恒无 flac）是平台能力。

const (
	// PlaybackQualityAuto 是默认：本平台内阶梯 flac → 320k → 128k。
	// 能上无损就上无损，拿不到就降档 —— 快，不跨源搜索。
	PlaybackQualityAuto = "auto"
	// PlaybackQualityLossless 是无损优先：本平台拿不到无损时，再去**下载源池**
	// 找同名的无损版（咪咕/酷狗那边常常有 flac，而网易免登录只有 320k mp3）。
	//
	// 代价是首次播放要多等一轮跨源搜索，所以**不设为默认**：试听的首字节延迟
	// 是用户直接感受得到的，为了一份可能存在的无损每次都多等一轮不划算。
	// 要真无损就明确切到这一档 —— 这是一个用户可以自己权衡的选择，不该由我们代选。
	PlaybackQualityLossless = "lossless"
)

// playbackQualityOptions 是界面上可选的档位（顺序即展示顺序）。
var playbackQualityOptions = []string{
	PlaybackQualityAuto, PlaybackQualityLossless, online.Tier320k, online.Tier128k,
}

// validPlaybackQuality 判断一个档位是不是我们能存的值。
func validPlaybackQuality(q string) bool {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case PlaybackQualityAuto, PlaybackQualityLossless, online.Tier320k, online.Tier192k, online.Tier128k:
		return true
	}
	return false
}

// playbackQuality 读当前偏好并归一化；未知 / 空 / nil 一律当 auto。
//
// 归一化放在这里而不是存的时候：配置文件可能被人手改过、也可能是老版本写的，
// 读的时候兜一道，界面与解析就都不会拿到一个认不出的值。
func (i *Interceptor) playbackQuality() string {
	raw := ""
	if i.cfg.PlaybackQuality != nil {
		raw = strings.ToLower(strings.TrimSpace(i.cfg.PlaybackQuality()))
	}
	if validPlaybackQuality(raw) {
		return raw
	}
	return PlaybackQualityAuto
}

// playbackTiers 把偏好翻成「要按哪几档依次试」。
//
//	auto     → flac → 320k → 128k（能上无损就上无损，逐级递减）
//	lossless → 只试无损；拿不到时由 resolvePlayback 去池里找，再兜回 auto 的阶梯
//	320k/192k/128k → **固定一档，不降级**
//
// 固定档位为什么不降级：用户点名要 128k（省流量 / 老设备），给他一个 320k
// 是没听话；点名要 320k 却降到 128k 更是。拿不到就明确失败，让他自己改。
func (i *Interceptor) playbackTiers() []string {
	switch i.playbackQuality() {
	case online.Tier320k:
		return []string{online.Tier320k}
	case online.Tier192k:
		return []string{online.Tier192k}
	case online.Tier128k:
		return []string{online.Tier128k}
	case PlaybackQualityLossless:
		return []string{online.TierLossless}
	}
	return online.PlaybackLadder
}

// nowPlaying 是一次试听实际用到的「源 + 档位」。
type nowPlaying struct {
	Platform string `json:"platform"`
	Quality  string `json:"quality"`
	At       int64  `json:"at"`
}

// nowPlayLimit 是这张表的上限。
//
// 它只是「当前状态」，不需要留历史：超了就把最旧的一条丢掉 —— 列表页连着点歌
// 时它不会无限涨（一首歌一个 key，一晚上能点几百首）。
const nowPlayLimit = 64

// setNowPlaying 记下「这个虚拟 guid 这次用了哪条源、哪一档」，返回是否**发生了变化**。
//
// 为什么要返回 changed：一次播放会打**很多**次取流请求（拖动进度条、边听边存的
// 续传、客户端预取都会各发一次 Range 请求）。每次都打一行日志的话，真机日志会被
// 「试听取音」刷满，真正的问题反而看不见。只在结论变化时说话。
func (i *Interceptor) setNowPlaying(fake, platform, tier string) (changed bool) {
	if fake == "" || (platform == "" && tier == "") {
		return false
	}
	i.nowMu.Lock()
	defer i.nowMu.Unlock()
	if i.nowPlay == nil {
		i.nowPlay = map[string]nowPlaying{}
	}
	if prev, ok := i.nowPlay[fake]; ok && prev.Platform == platform && prev.Quality == tier {
		return false
	}
	if len(i.nowPlay) >= nowPlayLimit {
		// 丢最旧的一条。O(n) 但 n 只有 64，而且只在**已满**时才跑。
		var oldestKey string
		var oldestAt int64
		for k, v := range i.nowPlay {
			if oldestKey == "" || v.At < oldestAt {
				oldestKey, oldestAt = k, v.At
			}
		}
		delete(i.nowPlay, oldestKey)
	}
	i.nowPlay[fake] = nowPlaying{Platform: platform, Quality: tier, At: time.Now().Unix()}
	return true
}

// nowPlayingOf 读「这个虚拟 guid 上次用了哪条源、哪一档」。
func (i *Interceptor) nowPlayingOf(fake string) (nowPlaying, bool) {
	if fake == "" {
		return nowPlaying{}, false
	}
	i.nowMu.Lock()
	defer i.nowMu.Unlock()
	v, ok := i.nowPlay[fake]
	return v, ok
}

// resolvePlayback 按「试听音质」偏好解析一条可播直链。
//
// 返回的第二个值 `used` 是**实际取音**的那条曲目：默认就是入参本身，只有
// 「无损优先」换源成功时才会不同。调用方要用它如实记录「从哪儿、什么音质」，
// 不能拿入参糊弄界面（与下载那条路的 resolveForDownload 同一个口径）。
func (i *Interceptor) resolvePlayback(ctx context.Context, t online.Track) (*online.Resolved, online.Track, error) {
	res, err := i.pool.ResolveLadder(ctx, t.Platform, t.PlatformID, i.playbackTiers())

	// 无损优先：本平台拿不到无损（或压根解析失败）时，去下载源池找同名无损版。
	//
	// 匹配判据与**下载**完全共用（bestQualityTrack → rankedSources），所以不会出现
	// 「下载换源了、试听没换」这种两套语义 —— 同一个池、同一份 sameTitle + 时长判据。
	if i.playbackQuality() == PlaybackQualityLossless {
		lossy := err != nil || res == nil ||
			online.TierRank(res.Tier) < online.TierRank(online.TierLossless)
		if lossy {
			if better, ok := i.bestQualityTrack(ctx, t.Title, t.Artist(), t.Duration); ok &&
				online.TierRank(better.Quality) >= online.TierRank(online.TierLossless) {
				bres, berr := i.pool.ResolveLadder(ctx, better.Platform, better.PlatformID,
					[]string{online.TierLossless})
				if berr == nil && bres != nil {
					i.logf("[INTERCEPT] 试听换源取无损：%s/%s → %s/%s",
						t.Platform, t.PlatformID, better.Platform, better.PlatformID)
					return bres, better, nil
				}
			}
			// 池里也没有无损 → 兜回 auto 阶梯。宁可让它**能播**（有损），
			// 也不要因为「拿不到无损」把一首本来能听的歌变成「播不了」。
			if res2, err2 := i.pool.ResolveLadder(ctx, t.Platform, t.PlatformID, online.PlaybackLadder); err2 == nil && res2 != nil {
				return res2, t, nil
			}
		}
	}

	if err == nil && res != nil {
		return res, t, nil
	}
	if err == nil {
		err = online.ErrTierUnavailable
	}
	return nil, t, err
}

// playbackTierOf 取一次解析结果的**展示用**档位。
//
// 优先用回读出来的 `res.Tier`；它为空时退回曲目自己声明的档位（`t.Quality`）——
// 有些外挂源不回报档位，显示成空会让用户以为「这首歌没写音质」。
func playbackTierOf(res *online.Resolved, t online.Track) string {
	if res != nil {
		if tier := strings.ToLower(strings.TrimSpace(res.Tier)); tier != "" {
			return tier
		}
	}
	return strings.ToLower(strings.TrimSpace(t.Quality))
}
