package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"fn-lx-player/pkg/apisource"
	"fn-lx-player/pkg/downloader"
	"fn-lx-player/pkg/monitor"
)

// monitorDispatcher 把监控的「发现」与「下载」接到本项目既有能力上。
//
// 设计要点：
//   - **发现**：优先消费浏览器上报（POST /api/monitors/{id}/discover，
//     酷狗/酷我等 lx 平台的来源只有网页端能抓）；没有上报时由后端用
//     已登录账号/公开接口自抓网易云、QQ 的歌单与收藏（见 monitor_backend_discover.go）。
//     音源直链仍只在浏览器解析 —— 后端抓到的曲目登记为 pending，等网页端「补下载」。
//   - **下载**：复用已有的 downloader（含断点续传、歌词内嵌、魔数校验），
//     仅对「带直链的上报曲目」生效。
type monitorDispatcher struct {
	srv *Server
}

// Discover 优先取「最近一次浏览器上报的曲目」；没有上报时走后端自抓
// （歌单/收藏类，见 monitor_backend_discover.go）。
//
// 保留浏览器上报优先是为了「同步曲目」按钮仍能指定任意 lx 平台（酷狗/酷我）的来源；
// 榜单类监控后端没有抓取通道，仍需要浏览器上报。
func (d *monitorDispatcher) Discover(mon *monitor.Monitor) ([]monitor.Song, []string, error) {
	cached, warnings, ok := d.srv.takeDiscovered(mon.ID)
	if ok {
		return cached, warnings, nil
	}

	songs, w2, handled, err := d.srv.backendDiscover(mon)
	if handled {
		return songs, append(warnings, w2...), err
	}
	return nil, warnings, fmt.Errorf(
		"尚未收到该监控的曲目上报（%s 类监控后端无法自动抓取）。请保持曲率 页面开着，并在「歌单监控」中点击「同步曲目」；"+
			"或由外部程序 POST /api/monitors/%s/discover 提交曲目列表", mon.Kind, mon.ID)
}

// PeekDiscover 只读地查看已上报曲目（供干跑预览使用，不消费数据）
func (d *monitorDispatcher) PeekDiscover(mon *monitor.Monitor) ([]monitor.Song, []string, bool) {
	return d.srv.peekDiscovered(mon.ID)
}

// Download 复用已有下载器落盘
func (d *monitorDispatcher) Download(song monitor.Song) (string, error) {
	if strings.TrimSpace(song.URL) == "" {
		return "", fmt.Errorf("缺少音频直链：请在提交曲目时一并给出 url")
	}

	res, err := d.srv.downloader.DownloadOne(monitorToDownloadPayload(song))
	if err != nil {
		return "", err
	}
	if res.Status == "failed" {
		return "", fmt.Errorf("%s", res.Error)
	}
	return res.Path, nil
}

// ── 后端取链：把「无直链曲目」交给已启用的服务型音源 ──
//
// 这是 v2.1.11 的接线点：v2.1.10 起后端能自己抓到歌单曲目，但拿不到直链，
// 只能登记 pending 等网页端点「补下载」。配上服务型音源后，后端可以自己取链、
// 自己落盘（落盘成功会自动触发飞牛扫库），监控才算真正无人值守。
//
// 合规边界不变：音源脚本仍只在浏览器执行；这里走的是用户自建/自配的
// **服务型音源**（pkg/apisource 的三协议），不是在后端执行第三方 JS。

// apiSourceResolve 放包级变量，便于离线测试替换（与 monitor_backend_discover.go 的 bd* 模式一致）
var apiSourceResolve = apisource.Resolve

// qualityChain 把监控音质档位映射成音源服务认的 quality 字符串序列。
//
//	standard → 128k，high → 320k，lossless → flac
//
// best_effort：目标档取不到就逐档降级（目标 → 320k → 128k）；
// skip：只试目标档，取不到就失败（交给下一轮或网页端）。
//
// ⚠️ hires 各服务的拼写未实测，先按 flac 请求（同属无损容器），待真机确认后再放开。
func qualityChain(q monitor.Quality, fb monitor.Fallback) []string {
	var chain []string
	switch q {
	case monitor.QualityStandard:
		chain = []string{"128k"}
	case monitor.QualityHigh:
		chain = []string{"320k", "128k"}
	default: // lossless / hires / 未知值一律按无损请求
		chain = []string{"flac", "320k", "128k"}
	}
	if fb == monitor.FallbackSkip {
		return chain[:1] // 只试目标档，取不到就交给下一轮
	}
	return chain
}

// CanResolve 当前是否有已启用的服务型音源。
//
// 返回 false 时监控不会产出取链动作 —— 没配音源的用户应该看到「待网页端补下载」，
// 而不是满屏「取链失败」。
func (d *monitorDispatcher) CanResolve() bool {
	if d.srv == nil || d.srv.cfgMgr == nil {
		return false
	}
	for _, src := range d.srv.cfgMgr.Get().APISources {
		if src.Enabled && strings.TrimSpace(src.BaseURL) != "" {
			return true
		}
	}
	return false
}

// ResolveURL 实现 monitor.URLResolver：为无直链曲目向后端音源服务取下载地址。
//
// 依次尝试**已启用**的服务型音源（配置顺序即优先级），第一个成功即用 —— 简单的故障切换，
// 不引入健康检查状态机（YAGNI）。全部失败时返回错误，由监控把该曲目登记 pending，
// 下一轮自动重试（也仍可由网页端「补下载」兜底）。
func (d *monitorDispatcher) ResolveURL(mon *monitor.Monitor, song monitor.Song) (string, error) {
	urls, err := d.ResolveURLCandidates(mon, song, 1)
	if err != nil {
		return "", err
	}
	if len(urls) == 0 {
		return "", fmt.Errorf("音源服务未返回地址")
	}
	return urls[0], nil
}

// ResolveURLCandidates 实现 monitor.URLResolverCandidates：按「音源顺序 × 音质降级链」
// 给出最多 max 条**互不相同**的候选直链（配置顺序即优先级）。
//
// 与 ResolveURL 的区别只在「拿到第一条后不返回、继续往下取」——所以正常取链请用
// ResolveURL（只外呼到第一个成功为止），只有确定要换源时才要多条：
// **取链成功不等于下载成功**，音源可能返回已失效的 CDN 链接、非音频页面或试听片段。
func (d *monitorDispatcher) ResolveURLCandidates(mon *monitor.Monitor, song monitor.Song, max int) ([]string, error) {
	if max <= 0 {
		max = 1
	}
	if d.srv == nil || d.srv.cfgMgr == nil {
		return nil, fmt.Errorf("配置尚未就绪")
	}
	if strings.TrimSpace(song.Source) == "" || strings.TrimSpace(song.ID) == "" {
		return nil, fmt.Errorf("曲目缺少平台或 ID，无法取链")
	}

	sources := d.srv.cfgMgr.Get().APISources
	chain := qualityChain(mon.Quality, mon.Fallback)

	var (
		out      []string
		seen     = make(map[string]bool)
		lastErr  error
		attempts []string
	)
	for _, src := range sources {
		if !src.Enabled || strings.TrimSpace(src.BaseURL) == "" {
			continue
		}
		for _, q := range chain {
			url, err := apiSourceResolve(src.Protocol, src.BaseURL, src.Token, song.Source, song.ID, q)
			if err == nil && strings.TrimSpace(url) != "" {
				u := strings.TrimSpace(url)
				// 同一档位下多个源可能给出同一个 CDN 地址，去重后才有「换源」的意义
				if !seen[u] {
					seen[u] = true
					out = append(out, u)
					if len(out) >= max {
						return out, nil
					}
				}
				continue
			}
			if err != nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("音源服务未返回地址")
			}
			if len(attempts) < 6 {
				attempts = append(attempts, src.Name+"/"+q)
			}
		}
	}

	if len(out) > 0 {
		return out, nil
	}
	if lastErr == nil {
		return nil, fmt.Errorf("没有已启用的服务型音源：请在「音源管理 → 按服务地址接入」添加并启用")
	}
	if len(attempts) > 0 {
		return nil, fmt.Errorf("%v（已尝试：%s）", lastErr, strings.Join(attempts, "、"))
	}
	return nil, lastErr
}

// HandleMonitorPreview 干跑：只做发现 + 过滤，不下载不写库、也不消费上报数据。
//
// POST /api/monitor/preview  body: {"monitor_id":"..."} 或 {"monitor":{...}}
func (s *Server) HandleMonitorPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var body struct {
		MonitorID string           `json:"monitor_id"`
		Monitor   *monitor.Monitor `json:"monitor"`
		Limit     int              `json:"limit"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	var mon *monitor.Monitor
	if body.MonitorID != "" {
		existing, ok := s.monitorStore.GetMonitor(body.MonitorID)
		if !ok {
			errJSON(w, http.StatusNotFound, "监控不存在")
			return
		}
		mon = existing
	} else if body.Monitor != nil {
		mon = body.Monitor
		if mon.Quality == "" {
			mon.Quality = monitor.QualityLossless
		}
	} else {
		errJSON(w, http.StatusBadRequest, "请提供 monitor_id 或 monitor 配置")
		return
	}

	disp, _ := s.monitorAPI.Dispatcher().(*monitorDispatcher)
	if disp == nil {
		errJSON(w, http.StatusServiceUnavailable, "曲目发现通道未就绪")
		return
	}

	var songs []monitor.Song
	var warnings []string
	var reported bool
	if body.MonitorID != "" {
		// 已存监控：读取该监控的上报数据（不消费）
		songs, warnings, reported = disp.PeekDiscover(mon)
	} else {
		// 草稿配置：允许直接在请求体里带 songs 做规则试算
		songs, warnings, reported = disp.PeekDiscover(&monitor.Monitor{ID: "__draft__"})
	}

	// 没有浏览器上报 → 用后端自抓通道干跑（歌单/收藏类支持，chart 类维持原提示）
	if !reported {
		bs, bw, handled, berr := s.backendDiscover(mon)
		if handled && berr == nil {
			songs, warnings = bs, append(warnings, bw...)
		}
	}

	if songs == nil {
		errJSON(w, http.StatusServiceUnavailable,
			"尚未收到曲目上报。请保持曲率 页面开着并点击「同步曲目」，"+
				"或先 POST /api/monitors/{id}/discover 提交曲目列表")
		return
	}

	// 干跑始终按「非基线」展示真实的过滤结果
	previewMon := *mon
	previewMon.BaselineDone = true

	res := monitor.Preview(&previewMon, songs)
	res.Warnings = append(res.Warnings, warnings...)

	limit := body.Limit
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	if len(res.Songs) > limit {
		res.Songs = res.Songs[:limit]
	}
	if len(res.Rejected) > limit {
		res.Rejected = res.Rejected[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": res})
}

// monitorToDownloadPayload 把监控发现的曲目转成下载载荷。
//
// Year/Track/Disc **在这里填不了**，留在 0：monitor.Song 没有这三个字段，
// 而榜单/歌单曲目接口本身也不返回发行年份与曲序（2026-09-26 确认）—— 它们只在
// 搜索响应里有。所以走监控自动下载的歌，年份仍要靠 `/api/library/complete` 事后补。
func monitorToDownloadPayload(song monitor.Song) downloader.SongPayload {
	return downloader.SongPayload{
		ID:       song.ID,
		Name:     song.Name,
		Singer:   song.Artist,
		Album:    song.Album,
		Cover:    song.Cover,
		Source:   song.Source,
		Songmid:  song.ID,
		URL:      song.URL,
		Duration: song.Duration,
		Interval: song.Duration,
	}
}

// ── 曲目上报的暂存 ──

type discoveredPayload struct {
	Songs    []monitor.Song `json:"songs"`
	Warnings []string       `json:"warnings"`
}

// HandleDiscover 接收浏览器/外部程序上报的曲目列表。
//
// POST /api/monitors/{id}/discover
// body: {"songs":[{source,id,name,artist,album,duration,cover,url}...],"warnings":[...]}
func (s *Server) HandleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	id := monitorIDFromPath(r.URL.Path, "discover")
	if id == "" {
		errJSON(w, http.StatusBadRequest, "缺少监控 ID")
		return
	}
	if _, ok := s.monitorStore.GetMonitor(id); !ok {
		errJSON(w, http.StatusNotFound, "监控不存在")
		return
	}

	var payload discoveredPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&payload); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	s.putDiscovered(id, payload.Songs, payload.Warnings)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "已接收曲目上报",
		"data": map[string]interface{}{
			"monitor_id": id,
			"count":      len(payload.Songs),
			"hint":       "现在可以 POST /api/monitors/" + id + "/run 执行一轮监控",
		},
	})
}

func (s *Server) putDiscovered(id string, songs []monitor.Song, warnings []string) {
	s.discoveredMu.Lock()
	defer s.discoveredMu.Unlock()
	if s.discovered == nil {
		s.discovered = make(map[string]*discoveredPayload)
	}
	s.discovered[id] = &discoveredPayload{Songs: songs, Warnings: warnings}
}

// peekDiscovered 只读地取上报曲目，不消费。
//
// 干跑预览必须用 peek：否则预览会把曲目「吃掉」，随后的正式运行就发现不了任何东西。
func (s *Server) peekDiscovered(id string) ([]monitor.Song, []string, bool) {
	s.discoveredMu.Lock()
	defer s.discoveredMu.Unlock()
	p, ok := s.discovered[id]
	if !ok {
		return nil, nil, false
	}
	return p.Songs, p.Warnings, true
}

// takeDiscovered 取出并**消费**上报的曲目，避免下一轮重复使用旧数据
func (s *Server) takeDiscovered(id string) ([]monitor.Song, []string, bool) {
	s.discoveredMu.Lock()
	defer s.discoveredMu.Unlock()
	p, ok := s.discovered[id]
	if !ok {
		return nil, nil, false
	}
	delete(s.discovered, id)
	return p.Songs, p.Warnings, true
}

// monitorIDFromPath 从 /api/monitors/{id}/{action} 中取 ID
func monitorIDFromPath(path, action string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if p == "monitors" && i+1 < len(parts) {
			if i+2 < len(parts) && parts[i+2] == action {
				return parts[i+1]
			}
			if i+2 >= len(parts) {
				return parts[i+1]
			}
		}
	}
	return ""
}

// ── 本包内的 JSON 辅助（与 monitor 包同名函数区分，避免跨包冲突） ──

func writeJSON(w http.ResponseWriter, status int, body map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(body)
}

func errJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]interface{}{"code": status, "message": msg})
}
