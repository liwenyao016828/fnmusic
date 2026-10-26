package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"fn-lx-player/pkg/config"
)

// 后台「音质补强」的设置面（v2.22）。
//
// # 为什么会有这个文件
//
// 用户 2026-10-25 原话（m00286）：
//
//	「音质补强是不是有个自动补强？下载-自动记录里有个记录，但是我没法控制呢？
//	 增加相应的定时按钮，补强音质来源，音质。将这个自动下载功能设置增加到设置。」
//
// 在那之前，「后台补强」在界面上**只留下一条事后流水**：触发列写着「音质补强」，
// 用户既看不到它在跑、关不掉、也调不了节奏。它的四个维度全是写死在
// pkg/intercept/upgrade_sweeper.go 里的常量。
//
// 这个文件把那个记录的「另一面」补齐 —— 让那行流水背后有个能看能调的控制台：
//
//	开关    upgrade_sweep_enabled        独立于「收藏自动下载」
//	节奏    upgrade_sweep_interval_minutes
//	批量    upgrade_sweep_batch          一轮最多派几首
//	来源    auto_download_channels       与「收藏自动下载」共用一套取音渠道
//	档位    auto_download_quality        与「收藏自动下载」共用一档
//
// # 为什么读的是「整套自动下载设置」而不只是补强那四个键
//
// 界面上的控制台要一次把「开关 / 节奏 / 来源 / 档位」四排都画出来，来源与档位本来就是
// 自动下载那套的键（补强就是自动下载的自动版，用两套会让「下载源」在界面上分出两个
// 互不同步的版本）。所以 GET 一次把两边都读回来，POST 一次把改过的键合并进去 ——
// 这也是 /api/config 现有的口径（选择性合并，见 config.Update）。

// handleQualityAutoUpgrade 读 / 改后台补强设置。
//
// GET  data：`{enabled, running, interval_minutes, batch, ignore_cooldown, pending,
//
//	cooling, channels, quality, last_run_at, last_dispatched, next_run_at}`
//
// POST body：上面任意子集（给了才改；两个数值 0 = 回到默认）。
func (s *Server) handleQualityAutoUpgrade(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.upgradeStatus == nil {
			qualityUnavailable(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": s.upgradeStatus()})
	case http.MethodPost:
		s.saveQualityAutoUpgrade(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": 405, "message": "只支持 GET / POST"})
	}
}

// saveQualityAutoUpgrade 处理 POST：把改过的键合并进配置，然后让轮子按新值重排。
func (s *Server) saveQualityAutoUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.cfgMgr == nil {
		qualityUnavailable(w)
		return
	}
	var body struct {
		// 指针 = 三态：nil 表示「这次没改这个键」。
		//
		// 这是本接口最容易写错的地方：用 bool 的话「改成 false」与「没传」分不开，
		// 而这两个开关的关掉正是用户最想做的动作。
		Enabled         *bool `json:"enabled"`
		IgnoreCooldown  *bool `json:"ignore_cooldown"`
		IntervalMinutes int   `json:"interval_minutes"`
		Batch           int   `json:"batch"`
		// 来源与档位复用自动下载那套键（见文件头注释）。
		Channels *[]string `json:"channels"`
		Quality  *string   `json:"quality"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<15)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "msg": "请求体不是合法 JSON"})
			return
		}
	}
	patch := config.AppConfig{
		UpgradeSweepEnabled:         body.Enabled,
		UpgradeSweepIgnoreCooldown:  body.IgnoreCooldown,
		UpgradeSweepIntervalMinutes: body.IntervalMinutes,
		UpgradeSweepBatch:           body.Batch,
	}
	if body.Channels != nil {
		// 归一化与 /api/config 同一条路（见 config.Update）：空列表 = 三档全开。
		// ⚠️ 这里必须给归一后的值：AutoDownloadChannels 是非指针切片，nil 会被
		// config.Update 判成「没改这个键」，界面上清空勾选就永远存不下去。
		patch.AutoDownloadChannels = config.NormalizeDownloadChannels(*body.Channels)
	}
	if body.Quality != nil {
		q := strings.ToLower(strings.TrimSpace(*body.Quality))
		if q == "" {
			q = "highest"
		}
		patch.AutoDownloadQuality = q
	}
	if err := s.cfgMgr.Update(patch); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "msg": err.Error()})
		return
	}
	// 内存改了不等于轮子知道：间隔/开关靠的是**起停循环**，必须显式重排一次。
	// （来源与档位不用，它们是每轮现取的，见 pkg/intercept/upgrade_sweeper.go。）
	if s.upgradeReload != nil {
		s.upgradeReload()
	}
	if s.upgradeStatus != nil {
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": s.upgradeStatus()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": map[string]any{"saved": true}})
}

// handleQualityAutoUpgradeRun 「立即补一轮」。
//
// body：`{"now":true}` —— true 时先清掉冷却位（用户的意思就是「我知道刚失败过，
// 再试一次」）。默认 false：只补那些「本来就该补」的，不额外外呼。
//
// 同步返回派出去的条数与欠账数：派活本身只是起 goroutine，几十毫秒就回来了；
// 真正的下载在后台跑，「替换音质」页的记录表会如实长出条目。
func (s *Server) handleQualityAutoUpgradeRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": 405, "message": "只支持 POST"})
		return
	}
	if s.upgradeRun == nil {
		qualityUnavailable(w)
		return
	}
	var body struct {
		Now bool `json:"now"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body)
	}
	n, pending, msg := s.upgradeRun(body.Now)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "msg": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 200,
		"data": map[string]any{"dispatched": n, "pending": pending},
	})
}
