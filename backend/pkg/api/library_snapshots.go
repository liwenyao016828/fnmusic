package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"fn-lx-player/pkg/config"
)

// 数据快照 —— 现在指的是「曲库元数据快照」，实现见 meta_snapshot.go。
//
// 这个文件只剩定时任务那三个开关的读写（开关 / 间隔 / 保留份数）。
//
// # 为什么删掉了老的整文件快照
//
// 老实现（2026-10-24 第一版）在拍快照时把每个音频文件整份复制一遍，
// 目的是「文件级回滚」。用户 2026-10-27 明确纠正：
// 「数据快照是不是做错了，我要快照的是歌曲的元数据。目的是为了，保留我的曲库中数据被误操作。」
// 误操作改的只有标签，音频本体从来没被改过；整文件快照既慢（一库几十 GB）
// 又让用户不敢开定时。所以老入口 /api/library/snapshots 直接下线（404），
// 新入口是 /api/library/meta-snapshots。

// handleSnapshotSchedule POST /api/library/snapshots  只保留 op=schedule
//
// 保留旧路径只为兼容老前端缓存里的请求；新代码一律走 meta-snapshots。
func (s *Server) handleSnapshotSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Op            string `json:"op"`
		ID            string `json:"id"`
		Enabled       *bool  `json:"enabled"`
		IntervalHours *int   `json:"interval_hours"`
		Keep          *int   `json:"keep"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(req.Op) != "schedule" {
		errJSON(w, http.StatusBadRequest,
			"数据快照已经改成「曲库元数据快照」，请用 /api/library/meta-snapshots（op=create / restore / delete）")
		return
	}
	s.saveSnapshotSchedule(w, req)
}

// saveSnapshotSchedule 保存定时快照的三个开关。
func (s *Server) saveSnapshotSchedule(w http.ResponseWriter, req struct {
	Op            string `json:"op"`
	ID            string `json:"id"`
	Enabled       *bool  `json:"enabled"`
	IntervalHours *int   `json:"interval_hours"`
	Keep          *int   `json:"keep"`
}) {
	var patch config.AppConfig
	if req.Enabled != nil {
		v := *req.Enabled
		patch.SnapshotScheduleEnabled = &v
	}
	if req.IntervalHours != nil {
		patch.SnapshotIntervalHours = *req.IntervalHours
	}
	if req.Keep != nil {
		patch.SnapshotKeep = *req.Keep
	}
	if err := s.cfgMgr.Update(patch); err != nil {
		errJSON(w, http.StatusInternalServerError, "保存失败："+err.Error())
		return
	}
	// 打开开关时立刻拍一份：否则用户勾上「每天快照」之后要等一整天才有第一份，
	// 期间会一直怀疑是不是没生效。已经有一份「刚刚的」就跳过（防连点）。
	if req.Enabled != nil && *req.Enabled && s.snapSched != nil {
		if time.Since(s.snapSched.lastRun()) > time.Minute {
			s.snapSched.markRun(time.Now())
			go func() {
				snap, saved, err := s.scheduledMetaSnapshot()
				if err != nil {
					// 「内容没变」不是失败，不进错误日志
					return
				}
				if saved {
					_ = snap
				}
				s.snapSched.prune(s.cfgMgr.Get().SnapshotRetention())
			}()
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": s.snapshotScheduleStatus(),
	})
}

// metaSnapshotKeepDays 手动快照的自然保留天数（定时快照按份数保留，见 prune）
func (s *Server) metaSnapshotKeepDays() int { return 90 }

// metaSnapshotScheduleEnabled 定时快照是否开着（给列表接口回显）
func (s *Server) metaSnapshotScheduleEnabled() bool {
	return s.cfgMgr.Get().SnapshotsScheduled()
}
