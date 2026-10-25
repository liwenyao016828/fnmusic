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
	// RemovedAt 用户从歌单里**手动删掉**了这首的时间（Unix 秒），0 = 未删。
	//
	// 用户 2026-10-24：「歌单里我手动删掉的歌，下次同步不要再加回来」。
	// 记录**不删除**（删了下一轮 toAdd = matched - current 又把它加回去），
	// 而是打上时间戳：之后的推送直接跳过它。用户在网页端重新加回来
	// （曲目又出现在歌单里了）就清掉时间戳，让它恢复成正常曲目。
	//
	// 0 值不落 JSON：老数据与新数据在文件里长得一模一样。
	RemovedAt int64 `json:"removed_at,omitempty"`
}

// ManualRemovalGraceSec 判定「用户手动删掉的」所用的宽限期（秒）。
//
// 为什么要宽限：我们刚 AddTracks 成功、用户正好在这一刻删了它，
// 飞牛那边可能读到一个还没有这首歌的快照 —— 这种竞态不该被记成「用户不要了」。
// 6 小时足够覆盖任何一次读取延迟，又不至于让「刚推上去就删」的记录久等。
const ManualRemovalGraceSec = 6 * 3600

// DetectManualRemovals 从「歌单现在有什么」里找出**用户手动删掉的**曲目。
//
// 判据（三条同时满足才算）：
//  1. 在我们推送过的清单里（绝不碰用户自己加的歌）；
//  2. 现在还不在歌单里（current 里没有）；
//  3. 加进歌单之后已过宽限期。
//
// 返回的 PushedTrack 已带上 RemovedAt；调用方把它们并回任务清单（并集，不删记录）。
func DetectManualRemovals(pushed []PushedTrack, current map[string]bool, now time.Time) []PushedTrack {
	if len(pushed) == 0 {
		return nil
	}
	cutoff := now.Unix() - ManualRemovalGraceSec
	out := make([]PushedTrack, 0)
	for _, p := range pushed {
		if p.GUID == "" || p.RemovedAt > 0 {
			continue
		}
		if current[p.GUID] {
			continue // 还在歌单里
		}
		if p.AddedAt > 0 && p.AddedAt > cutoff {
			continue // 可能只是读取快照还没跟上
		}
		p.RemovedAt = now.Unix()
		out = append(out, p)
	}
	return out
}

// ManualRemovals 取出「用户手动删掉过」的 guid 集合（给 PushTracks 过滤用）。
func ManualRemovals(pushed []PushedTrack) map[string]bool {
	var out map[string]bool
	for _, p := range pushed {
		if p.GUID != "" && p.RemovedAt > 0 {
			if out == nil {
				out = make(map[string]bool, 8)
			}
			out[p.GUID] = true
		}
	}
	return out
}

// RevivedManuallyAdded 找出「曾经被用户删掉、后来又自己加回来」的 guid。
//
// 用户在网页端把删掉的歌重新拖回歌单，说明他改主意了 —— 这时必须清掉
// RemovedAt，否则这首会被永久黑名单，比「删了又被加回」更让人火大。
func RevivedManuallyAdded(pushed []PushedTrack, current map[string]bool) map[string]bool {
	var out map[string]bool
	for _, p := range pushed {
		if p.GUID != "" && p.RemovedAt > 0 && current[p.GUID] {
			if out == nil {
				out = make(map[string]bool, 4)
			}
			out[p.GUID] = true
		}
	}
	return out
}

// MergePushed 把本轮新增的曲目并入推送记录（同 guid 去重，保留最新时间）。
//
// 用途：一轮推送成功后，把「这次加了哪些」追加进任务的推送清单，
// 供后续的保留期清理判断归属。
// NormalizeRetentionMode 把任意输入规整成两种合法模式之一（默认 keep）。
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

// PushedIndex 把推送记录建成 guid → 下标 的索引。
//
// 给调用方做「这首歌我们推过吗 / 记录在第几条」的 O(1) 判断，
// 不必自己绕一圈线性扫。空 guid 不进索引（与 MergePushed 口径一致）。
func PushedIndex(pushed []PushedTrack) map[string]int {
	if len(pushed) == 0 {
		return nil
	}
	idx := make(map[string]int, len(pushed))
	for i, p := range pushed {
		if p.GUID == "" {
			continue
		}
		idx[p.GUID] = i
	}
	return idx
}

// ClearRemovals 清掉指定 guid 上的「手动删除」时间戳，返回一份新切片。
//
// 用途：用户把删掉的歌自己加回歌单时（RevivedManuallyAdded），那几条记录
// 必须恢复成正常曲目 —— 否则会被永久黑名单，比「删了又被加回」更让人火大。
// **不改入参**（调用方可能还持有旧切片）；没命中任何一条时原样返回，不做无谓拷贝。
func ClearRemovals(pushed []PushedTrack, guids map[string]bool) []PushedTrack {
	if len(pushed) == 0 || len(guids) == 0 {
		return pushed
	}
	hit := false
	for _, p := range pushed {
		if p.RemovedAt > 0 && guids[p.GUID] {
			hit = true
			break
		}
	}
	if !hit {
		return pushed
	}
	out := make([]PushedTrack, len(pushed))
	copy(out, pushed)
	for i := range out {
		if out[i].RemovedAt > 0 && guids[out[i].GUID] {
			out[i].RemovedAt = 0
		}
	}
	return out
}
