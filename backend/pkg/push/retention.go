package push

// 推送保留期策略。
//
// 背景：像「每日推荐」这种来源，内容每天在变。不做清理的话，飞牛歌单会一直膨胀
// （今天是这 30 首，明天再加 30 首，一个月就 900 首）。flow 有 retention_mode/expires_at
// 做这件事，我们这里补上。
//
// ⚠️ **安全前提：只删我们自己推送过的曲目。**
// 用户完全可能手动往同一个歌单里加歌 —— 如果按「曲目在歌单里的加入时间」盲删，
// 就会误伤用户手动加的那些。所以每个任务要持久化一份「我推送过哪些 guid、什么时候推的」
// 清单（`PushedTrack`），清理只在这个清单范围内进行。
//
// 因此这里**不使用**飞牛接口返回的 createdAt 作为判据：
// 那个时间只能说明「曲目何时进的歌单」，分不清是程序推的还是用户手动加的。

import (
	"fmt"
	"time"

	"fn-lx-player/pkg/fnos"
)

// 保留期模式
const (
	// RetentionKeep 永久保留（默认）—— 推送上去就不动它
	RetentionKeep = "keep"
	// RetentionDays 按天清理 —— 超过 RetentionDays 天的自动从歌单移除
	RetentionDays = "days"
)

// pruneBatch 单批移除数量，与飞牛接口约定一致（add-track 也建议 ≤50）
const pruneBatch = 50

// PushedTrack 记录一条「本任务推送过」的曲目及其加入时间。
type PushedTrack struct {
	GUID    string `json:"guid"`
	AddedAt int64  `json:"added_at"` // Unix 秒
	Title   string `json:"title,omitempty"`
}

// NormalizeRetentionMode 归一化保留期模式；非法值一律退回 keep（安全默认）。
func NormalizeRetentionMode(mode string) string {
	if mode == RetentionDays {
		return RetentionDays
	}
	return RetentionKeep
}

// PruneExpired 按保留期移除**本任务推送过**且已超期的曲目。
//
// 参数：
//
//	pushed 该任务此前推送过的全部曲目记录（持久化在任务里）
//	days   保留天数；<=0 或 mode=keep 时调用方不该走到这里
//
// 返回：
//
//	removed 本轮成功移除的 guid
//	kept    清理后仍应保留的记录（调用方覆盖回任务里）
//	err     移除失败的原因；**失败时已成功的批次仍计入 removed**，
//	        未成功的会留在 kept 里下轮重试 —— 不静默丢记录
//
// 只删 pushed 里的 guid，绝不去动用户手动加进歌单的曲目。
func PruneExpired(m *fnos.Music, playlistGUID string, pushed []PushedTrack, days int) (removed []string, kept []PushedTrack, err error) {
	if days <= 0 || len(pushed) == 0 {
		return nil, pushed, nil
	}

	cutoff := time.Now().AddDate(0, 0, -days).Unix()

	// 挑出超期的：只可能是我们自己推上去的
	expired := make([]PushedTrack, 0, len(pushed))
	kept = make([]PushedTrack, 0, len(pushed))
	for _, p := range pushed {
		// AddedAt 缺失（老数据）时按「不超期」处理，宁可留着也不误删
		if p.AddedAt > 0 && p.AddedAt < cutoff {
			expired = append(expired, p)
			continue
		}
		kept = append(kept, p)
	}
	if len(expired) == 0 {
		return nil, kept, nil
	}

	// 分批移除：某批失败时，该批及其后的记录全部保留待下轮重试
	for i := 0; i < len(expired); i += pruneBatch {
		end := i + pruneBatch
		if end > len(expired) {
			end = len(expired)
		}
		chunk := expired[i:end]
		guids := make([]string, len(chunk))
		for j, p := range chunk {
			guids[j] = p.GUID
		}

		if e := m.RemoveTracks(playlistGUID, guids); e != nil {
			kept = append(kept, expired[i:]...)
			return removed, kept, fmt.Errorf("移除超期曲目失败（本轮已移除 %d 首）: %w", len(removed), e)
		}
		removed = append(removed, guids...)
	}

	return removed, kept, nil
}

// MergePushed 把本轮新增的曲目并入推送记录（同 guid 去重，保留最新时间）。
//
// 用途：一轮推送成功后，把「这次加了哪些」追加进任务的推送清单，
// 供后续的保留期清理判断归属。
func MergePushed(existing []PushedTrack, added []PushedTrack) []PushedTrack {
	if len(added) == 0 {
		return existing
	}
	index := make(map[string]int, len(existing))
	for i, p := range existing {
		index[p.GUID] = i
	}
	out := make([]PushedTrack, len(existing))
	copy(out, existing)

	for _, p := range added {
		if p.GUID == "" {
			continue
		}
		if i, ok := index[p.GUID]; ok {
			// 已在清单里（重复推送同一首）→ 刷新时间，不重复追加
			out[i] = p
			continue
		}
		index[p.GUID] = len(out)
		out = append(out, p)
	}
	return out
}
