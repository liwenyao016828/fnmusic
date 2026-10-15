package online

import (
	"context"
	"errors"
	"strings"
)

// 音质档位与「试听阶梯」。
//
// # 为什么档位常量放在这里而不是散在各解析器里
//
// 档位必须与前端 `frontend/src/engine/quality.js` 的 `QUALITY_LADDER` 严格对齐：
// 那边按同一个阶梯决定「要试哪几档」，这边按同一个阶梯决定「怎么问平台」。
// 两边对不上不会报错，只会出现「界面写着无损、实际拿到 320k」这种只能靠人看出来的偏差
// （`pkg/search/quality.go` 顶部那段注释记的就是同一件事）。
//
// # 用户口径（2026-10-07 指令）
//
// 「试听音质按 flac → 320k → 128k 逐级递减，能上无损就上无损。」
//
// 即：**从最高档开始问，这一档拿不到就降下一档**；能拿到无损就用无损。
// 注意「拿不到」有两种，必须分开：
//
//   - 平台**明确拒绝**这一档（QQ 未登录问 flac 恒回 result=104003）→ 降档重试；
//   - 平台**静默降级**（网易问 lossless 回 exhigh/320k）→ 这不是失败，是一次
//     成功的解析，只是拿到的档位比要的低。照实记下实际档位，**不要**再降档重试
//     —— 那会白白多打两次请求，还拿到同一份东西。
const (
	// TierLossless 无损档。flac / flac24bit / ape / wav / dsd 都归这一档 ——
	// 阶梯里不为它们各开一格：对用户来说「是不是无损」才是那个决策点。
	TierLossless = "flac"
	Tier320k     = "320k"
	Tier192k     = "192k"
	Tier128k     = "128k"
)

// TierRank 返回档位高低（越大越高）；认不出的档位返回 -1。
//
// 与 `pkg/search.qualityRank` 同口径（那边管搜索结果里「这首歌有哪些档」，
// 这边管「我们该按哪一档去要」）—— 两处判据必须一致，所以档位取值也共用一套字符串。
func TierRank(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case TierLossless, "flac24bit", "hires", "ape", "wav", "dsd":
		return 4
	case Tier320k:
		return 3
	case Tier192k:
		return 2
	case Tier128k:
		return 1
	}
	return -1
}

// PlaybackLadder 是试听的默认降级阶梯：能上无损就上无损，逐级递减到 128k。
//
// 只到 128k 为止：再低的档位（aac48 / ogg96）不在阶梯内，问它们没有意义
// （`pkg/search/quality.go` 的 `qualityFromBitrateKbps` 也是同一个下限）。
var PlaybackLadder = []string{TierLossless, Tier320k, Tier128k}

// ErrTierUnavailable 表示「平台明确没有这一档」。
//
// 与 ErrNotPlayable 必须分开：前者是「换低一档还能听」，后者是「这首歌根本听不了」。
// 混成一个的表现是：一首只有 128k 的歌会直接报「播不了」，而不是降档播出来。
var ErrTierUnavailable = errors.New("该平台没有这一档音质")

// TierResolver 是 Resolver 的**可选扩展**：按指定档位解析。
//
// # 为什么要有它
//
// `Resolve()` 只能问「给我这首歌」，档位由平台自己决定 —— 而网易那条免登录接口
// 我们一直固定 `br=320000`，于是**永远拿不到无损**，哪怕这首歌明明有无损档。
// 要「能上无损就上无损」，就必须能指定档位。
//
// # 为什么是可选接口而不是改 Resolver 的签名
//
// 与 `QueryResolver` 同一个理由：改签名会把所有既有实现与它们的全部用例卷进来，
// 而 musicdl / 洛雪那几条路根本不需要它（它们的档位由外挂侧自己定）。
// 没实现这个接口的平台，`Pool.ResolveLadder` 会退化成「问一次 Resolve」。
type TierResolver interface {
	// ResolveAtTier 按指定档位解析单条。
	//
	// 平台明确没有这一档时返回 ErrTierUnavailable（调用方据此降档）；
	// 曲目本身不可播时返回 ErrNotPlayable（调用方应停止降档）。
	// 返回的 Resolved.Tier 必须是**实际**拿到的档位，不是请求的档位。
	ResolveAtTier(ctx context.Context, platformID string, tier string) (*Resolved, error)
}

// tierFromResolved 推断一次解析实际拿到的档位。
//
// 优先用平台自己回读出来的（`Resolved.Tier`），没有就按码率推，再没有就按容器推。
// 为什么要这么兜：解析器回读档位是**新**能力，老解析器（musicdl / 洛雪）不回读 ——
// 不兜的话它们的档位会显示成空，界面上就是「这首歌没写音质」。
func tierFromResolved(res *Resolved) string {
	if res == nil {
		return ""
	}
	if t := strings.ToLower(strings.TrimSpace(res.Tier)); t != "" {
		return t
	}
	if res.Bitrate > 0 {
		switch {
		case res.Bitrate >= 500_000:
			return TierLossless
		case res.Bitrate >= 256_000:
			return Tier320k
		case res.Bitrate >= 160_000:
			return Tier192k
		case res.Bitrate >= 112_000:
			return Tier128k
		}
	}
	switch strings.ToLower(strings.TrimSpace(res.Format)) {
	case "flac", "flac24bit", "ape", "wav", "dsf", "dff":
		return TierLossless
	case "mp3":
		return "" // mp3 不等于 320k：码率没给就别猜
	case "m4a", "aac":
		return ""
	}
	return ""
}
