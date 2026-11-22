package intercept

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/audioprobe"
	"fn-lx-player/pkg/nas"
	"fn-lx-player/pkg/online"
)

// 歌曲修复（替换）—— v2.35.0。
//
// 与「替换音质」的区别，是这两个功能容易混的地方：
//   - 替换音质：文件本身没坏，只是**档位不够高**。判据是「能不能换成更高的档位」，
//     所以探测完一路要比档位、比完才决定换不换。
//   - 歌曲修复：文件本身**不对**（扩展名写 flac 其实是 mp3、时长只有 30 秒、
//     无损小得离谱）。档位高低不是重点 —— 同样是 320k，坏的那份也必须换掉。
//
// 所以这里**不做档位比较，也不看 Upgradeable**：用户点名要换这个候选，就换。
// 唯一的守卫是「别把文件换成更小的垃圾」：落盘后读一次盘上事实，读不出来或者
// 时长归零就当失败，把旧的留着。

// TrackReplaceRequest 一次指向候选曲目的替换请求。
type TrackReplaceRequest struct {
	// Path 要修的**盘上那份文件**（原地覆盖，扩展名变了会删旧的）。
	Path string `json:"path"`
	// Platform / PlatformID 用户挑中的候选（平台短码 + 平台内 id）。
	Platform   string `json:"platform"`
	PlatformID string `json:"platform_id"`
	// Title/Artist/Album/Duration 写进新文件标签用。缺了不会失败，只是标签不全。
	Title    string   `json:"title,omitempty"`
	Artist   string   `json:"artist,omitempty"`
	Album    string   `json:"album,omitempty"`
	Duration int      `json:"duration,omitempty"`
	Artists  []string `json:"artists,omitempty"`
	CoverURL string   `json:"cover_url,omitempty"`
	// Quality 候选声明的档位（flac / 320k…），只影响取链的起点，不影响成败判定。
	Quality string `json:"quality,omitempty"`
}

// TrackReplaceResult 替换结果。Before/After 是盘上事实，供界面如实显示。
type TrackReplaceResult struct {
	Path string `json:"path"`
	OK   bool   `json:"ok"`
	// Note 一句话说明结果（成功也说清换成了什么，失败说清为什么）。
	Note string `json:"note,omitempty"`
	// Before/After 是替换前后从文件本身读到的容器与时长。
	Before AudioInfo `json:"before"`
	After  AudioInfo `json:"after"`
	// Tier 落盘后实际档位（audioext 口径）。
	Tier string `json:"tier,omitempty"`
}

// AudioInfo 是对外的「文件身体」快照（不含内部字段）。
type AudioInfo struct {
	Container   string  `json:"container,omitempty"`
	DurationSec float64 `json:"duration_sec,omitempty"`
	Size        int64   `json:"size,omitempty"`
	Kbps        int     `json:"kbps,omitempty"`
}

func AudioInfoOf(p string) AudioInfo {
	info, err := audioprobe.Probe(p)
	if err != nil {
		return AudioInfo{}
	}
	return AudioInfo{
		Container: info.Container, DurationSec: info.DurationSec,
		Size: info.Size, Kbps: info.NominalKbps,
	}
}

// ReplaceTrackFromCandidate 用指定候选曲目覆盖盘上那份坏文件。
func (i *Interceptor) ReplaceTrackFromCandidate(ctx context.Context, req TrackReplaceRequest) (TrackReplaceResult, error) {
	// 路径守卫：只允许曲库根目录内的文件（与整理/删除/替换音质同一道闸）。
	path, err := nas.ResolveSafePath(strings.TrimSpace(req.Path))
	if err != nil {
		return TrackReplaceResult{}, fmt.Errorf("路径不在曲库内：%w", err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return TrackReplaceResult{}, fmt.Errorf("文件不存在或是目录")
	}
	platform := strings.TrimSpace(req.Platform)
	platformID := strings.TrimSpace(req.PlatformID)
	if platform == "" || platformID == "" {
		return TrackReplaceResult{}, fmt.Errorf("没有指定候选曲目（platform / platform_id 为空）")
	}

	res, err := i.pool.ResolveLadder(ctx, platform, platformID, i.downloadTiers())
	if err != nil || res == nil || strings.TrimSpace(res.URL) == "" {
		if err == nil {
			err = fmt.Errorf("这个候选没解析出直链")
		}
		return TrackReplaceResult{}, fmt.Errorf("这个版本取不到直链（%s/%s）：%w", platform, platformID, err)
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	artists := req.Artists
	if len(artists) == 0 {
		if a := strings.TrimSpace(req.Artist); a != "" {
			artists = splitArtists(a)
		}
	}
	track := online.Track{
		Platform:   platform,
		PlatformID: platformID,
		Title:      title,
		Artists:    artists,
		Album:      strings.TrimSpace(req.Album),
		Duration:   req.Duration,
		CoverURL:   strings.TrimSpace(req.CoverURL),
		Quality:    firstNonEmpty(strings.TrimSpace(req.Quality), res.Tier),
	}
	track = track.Normalized()

	// 落盘前后各读一次文件身体 —— 结论以磁盘事实为准，不信任何一侧的应答。
	before := AudioInfoOf(path)

	fake := "repair:" + fmt.Sprint(time.Now().UnixNano())
	// ⚠️ 走**强制替换**那条：坏文件的档位常常与候选一样（都是 320k），而且小到
	// 不被下载器认成音频（<32KB）。按普通的「升级音质」口径走，结果是旁边多出一份
	// `xxx (2).mp3`、坏的还躺在原地 —— v2.35.0 真机实测就是这个形状。
	item, err := i.postDownloadForceReplace(ctx, fake, track, track, res.URL, res.Tier, path)
	if err != nil {
		return TrackReplaceResult{Path: path, Before: before, Note: "下载没成功：" + err.Error()}, err
	}
	target := strings.TrimSpace(item.Path)
	if target == "" {
		target = path
	}
	after := AudioInfoOf(target)
	tier := audioTierOf(target)

	out := TrackReplaceResult{Path: target, Before: before, After: after, Tier: tier}
	switch {
	case after.Size <= 0:
		out.Note = "新文件是空的，旧的保留"
		return out, fmt.Errorf("新文件没有落盘成功")
	case after.DurationSec > 0 && after.DurationSec < 5:
		out.Note = fmt.Sprintf("新文件只有 %.0f 秒，看着还是坏的，旧的保留", after.DurationSec)
		return out, fmt.Errorf("新文件时长异常")
	}
	out.OK = true
	if before.Container != "" && after.Container != "" && before.Container != after.Container {
		out.Note = fmt.Sprintf("已替换：%s → %s", before.Container, after.Container)
	} else if after.DurationSec > 0 {
		out.Note = fmt.Sprintf("已替换：%.0f 秒 / %s", after.DurationSec, shortSize(after.Size))
	} else {
		out.Note = "已替换"
	}
	i.logf("[INTERCEPT] 歌曲修复：%s ← %s/%s（%s，%s）",
		filepath.Base(target), platform, platformID, res.Tier, out.Note)
	return out, nil
}

// audioTierOf 读盘上档位；读不出来给空串（不拿空串冒充档位）。
func audioTierOf(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return audioext.TierOfFile(p)
}

// shortSize 人类可读的体积，只用于日志与提示。
func shortSize(n int64) string {
	switch {
	case n <= 0:
		return "0 B"
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
}
