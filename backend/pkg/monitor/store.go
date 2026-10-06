// Package monitor 提供歌单/榜单/收藏夹的定时监控与增量下载决策。
//
// 思路移植自 music-monitor 的编排层（pipeline.py / scheduler.py），
// 关键取舍：
//   - 只移植「决策层」，接入层复用本项目已有的音源能力（lx 脚本 / AI 解析桥）；
//   - 曲目唯一键为 (source, songID)，与 music-monitor 一致；
//   - **补上 music-monitor 缺失的「首轮基线」**：首次监控一个歌单时默认只登记
//     曲目、不批量下载，避免几百首歌单瞬间灌满下载队列。
package monitor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind 监控类型
type Kind string

const (
	KindChart     Kind = "chart"     // 榜单
	KindPlaylist  Kind = "playlist"  // 指定歌单
	KindFavorites Kind = "favorites" // 个人收藏夹 / 我喜欢的音乐
)

// Quality 目标音质档位
type Quality string

const (
	QualityStandard Quality = "standard" // ≈128k
	QualityHigh     Quality = "high"     // ≈320k
	QualityLossless Quality = "lossless" // FLAC
	QualityHiRes    Quality = "hires"    // Hi-Res
)

// Fallback 未达目标音质时的策略
type Fallback string

const (
	FallbackBestEffort Fallback = "best_effort" // 尽力而为，取最高可用
	FallbackSkip       Fallback = "skip"        // 未达标则跳过
)

// MinKbps 各档位的最低码率要求
func (q Quality) MinKbps() int {
	switch q {
	case QualityStandard:
		return 96
	case QualityHigh:
		return 256
	case QualityLossless:
		return 700
	case QualityHiRes:
		return 1400
	}
	return 700
}

// NeedsLossless 该档位是否要求无损容器
func (q Quality) NeedsLossless() bool {
	return q == QualityLossless || q == QualityHiRes
}

// Label 中文标签（用于日志与界面）
func (q Quality) Label() string {
	switch q {
	case QualityStandard:
		return "标准 (≈128 kbps)"
	case QualityHigh:
		return "较高 (≈320 kbps)"
	case QualityLossless:
		return "无损 (FLAC)"
	case QualityHiRes:
		return "Hi-Res (≥24bit/96kHz)"
	}
	return string(q)
}

// NormalizeQuality 归一化音质档位，未知值回退到无损
func NormalizeQuality(s string) Quality {
	switch Quality(strings.ToLower(strings.TrimSpace(s))) {
	case QualityStandard:
		return QualityStandard
	case QualityHigh:
		return QualityHigh
	case QualityLossless:
		return QualityLossless
	case QualityHiRes:
		return QualityHiRes
	}
	return QualityLossless
}

// Target 监控目标（按 kind 使用不同字段）
type Target struct {
	Charts      []ChartRef    `json:"charts,omitempty"`       // kind=chart
	Playlists   []PlaylistRef `json:"playlists,omitempty"`    // kind=playlist
	PlaylistIDs []string      `json:"playlist_ids,omitempty"` // kind=favorites，空表示全部
}

// ChartRef 榜单引用
type ChartRef struct {
	Key      string `json:"key,omitempty"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform,omitempty"`
	ID       string `json:"id,omitempty"`
	Link     string `json:"link,omitempty"`
}

// PlaylistRef 歌单引用
type PlaylistRef struct {
	Name   string `json:"name,omitempty"`
	Link   string `json:"link,omitempty"`
	ID     string `json:"id,omitempty"`
	Source string `json:"source,omitempty"`
}

// Monitor 一个监控任务
type Monitor struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Kind            Kind     `json:"kind"`
	Enabled         bool     `json:"enabled"`
	Sources         []string `json:"sources"` // 参与的平台
	Target          Target   `json:"target"`
	Quality         Quality  `json:"quality"`
	Fallback        Fallback `json:"fallback"`
	AutoDownload    bool     `json:"auto_download"`
	Embed           bool     `json:"embed"`
	IntervalMinutes int      `json:"interval_minutes"`
	MaxDownloads    int      `json:"max_downloads"`
	IncludeKW       string   `json:"include_kw"`
	ExcludeKW       string   `json:"exclude_kw"`
	// BaselineDone 首轮基线是否已完成。
	// 未完成时首轮只登记曲目、不下载，避免几百首歌单瞬间灌满队列。
	BaselineDone bool `json:"baseline_done"`

	LastRunAt string `json:"last_run_at,omitempty"`
	NextRunAt string `json:"next_run_at,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Track 曲目记录
type Track struct {
	MonitorID     string `json:"monitor_id"`
	Source        string `json:"source"`
	SongID        string `json:"song_id"`
	Name          string `json:"name"`
	Artist        string `json:"artist"`
	Album         string `json:"album"`
	Duration      int    `json:"duration"`
	Cover         string `json:"cover"`
	Status        string `json:"status"` // pending|downloaded|skipped|failed|missing
	QualityActual string `json:"quality_actual,omitempty"`
	Bitrate       string `json:"bitrate,omitempty"`
	FilePath      string `json:"file_path,omitempty"`
	Error         string `json:"error,omitempty"`
	HitCount      int    `json:"hit_count"`
	FirstSeen     string `json:"first_seen"`
	LastSeen      string `json:"last_seen"`
}

// TrackKey 曲目唯一键
func TrackKey(source, songID string) string {
	return source + ":" + songID
}

// 曲目状态
const (
	StatusPending    = "pending"
	StatusDownloaded = "downloaded"
	StatusSkipped    = "skipped"
	StatusFailed     = "failed"
	StatusMissing    = "missing"
)

// Run 一次运行记录
type Run struct {
	ID         string `json:"id"`
	MonitorID  string `json:"monitor_id"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Status     string `json:"status"` // running|ok|partial|error
	Found      int    `json:"found"`
	NewItems   int    `json:"new_items"`
	Downloaded int    `json:"downloaded"`
	Skipped    int    `json:"skipped"`
	Failed     int    `json:"failed"`
	// ResolveFailed 后端取链失败数。**不计入 Failed**（音源偶发失败不该把整轮标红），
	// 这些曲目登记为 pending，下一轮监控自动重试。
	ResolveFailed int    `json:"resolve_failed,omitempty"`
	Message       string `json:"message,omitempty"`
	Log           string `json:"log,omitempty"`
	Baseline      bool   `json:"baseline,omitempty"` // 是否为基线轮（只登记不下载）
}

// 默认值
const (
	DefaultIntervalMinutes = 360
	DefaultMaxDownloads    = 30
	// MaxDownloadsPerRun 「每次下载数量」的上限。
	//
	// ⚠️ 它**不是节流阀，只是防呆**：拦「手输了一个荒唐的大数」（比如 999999）
	// 把一轮 run 拖成几小时、监控一直挂在「运行中」。
	// 真正的节流靠音源服务自身 + 下载队列的并发/间隔。
	//
	// 原值 50 是照抄 music-monitor 的；但用户在界面上是**自由输入**的
	// （2026-09-22：「手动选择，或者手动输入」），50 太小、会让人以为填了不生效。
	MaxDownloadsPerRun = 300
	// DefaultMaxTracksPerSource 每个源抓取的曲目上限
	DefaultMaxTracksPerSource = 500
)

// Store 监控数据的持久化（JSON 文件，零第三方依赖）
type Store struct {
	mu       sync.RWMutex
	filePath string
	data     storeData
}

type storeData struct {
	Version  int               `json:"version"`
	Monitors []*Monitor        `json:"monitors"`
	Tracks   map[string]*Track `json:"tracks"` // key = monitorID|source|songID
	Runs     []*Run            `json:"runs"`
	Settings map[string]string `json:"settings,omitempty"`
}

const storeVersion = 1

func nowISO() string {
	return time.Now().Format("2006-01-02T15:04:05")
}

func trackStoreKey(monitorID, source, songID string) string {
	return monitorID + "|" + source + "|" + songID
}

// NewStore 加载（或创建）监控存储
func NewStore(dataDir string) *Store {
	s := &Store{
		filePath: filepath.Join(dataDir, "monitors.json"),
		data: storeData{
			Version: storeVersion,
			Tracks:  make(map[string]*Track),
		},
	}
	s.load()
	return s
}

func (s *Store) load() {
	raw, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}
	var d storeData
	if err := json.Unmarshal(raw, &d); err != nil {
		return // 损坏时从空开始，不影响主流程
	}
	if d.Tracks == nil {
		d.Tracks = make(map[string]*Track)
	}
	s.data = d
}

// Save 原子写盘
func (s *Store) Save() error {
	s.mu.RLock()
	payload, err := json.MarshalIndent(storeData{
		Version:  storeVersion,
		Monitors: s.data.Monitors,
		Tracks:   s.data.Tracks,
		Runs:     s.data.Runs,
		Settings: s.data.Settings,
	}, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".monitors-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.Write(payload); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, s.filePath)
}

// ── 监控 CRUD ──

// CreateMonitor 创建监控。首轮会被标记为基线轮（只登记不下载）。
func (s *Store) CreateMonitor(m *Monitor) (*Monitor, error) {
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("监控名称不能为空")
	}
	switch m.Kind {
	case KindChart, KindPlaylist, KindFavorites:
	default:
		return nil, fmt.Errorf("不支持的监控类型: %s（可选 chart / playlist / favorites）", m.Kind)
	}
	if m.Quality == "" {
		m.Quality = QualityLossless
	}
	if m.Fallback == "" {
		m.Fallback = FallbackBestEffort
	}
	if m.IntervalMinutes <= 0 {
		m.IntervalMinutes = DefaultIntervalMinutes
	}
	if m.MaxDownloads <= 0 {
		m.MaxDownloads = DefaultMaxDownloads
	}

	now := time.Now()
	if m.ID == "" {
		m.ID = fmt.Sprintf("mon_%d", now.UnixNano())
	}
	m.CreatedAt = nowISO()
	m.UpdatedAt = m.CreatedAt
	m.BaselineDone = false
	// 创建后一个 tick 内即跑第一轮（与 music-monitor 的 set_next_run(immediately) 一致）
	m.NextRunAt = nowISO()

	s.mu.Lock()
	s.data.Monitors = append(s.data.Monitors, m)
	s.mu.Unlock()

	if err := s.Save(); err != nil {
		return nil, err
	}
	return m, nil
}

// GetMonitor 查询单个监控
func (s *Store) GetMonitor(id string) (*Monitor, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.data.Monitors {
		if m.ID == id {
			return m, true
		}
	}
	return nil, false
}

// ListMonitors 列出全部监控
func (s *Store) ListMonitors() []*Monitor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Monitor, len(s.data.Monitors))
	copy(out, s.data.Monitors)
	return out
}

// UpdateMonitor 局部更新（传入的函数就地修改副本）
func (s *Store) UpdateMonitor(id string, mutate func(*Monitor)) (*Monitor, error) {
	s.mu.Lock()
	var target *Monitor
	for _, m := range s.data.Monitors {
		if m.ID == id {
			target = m
			break
		}
	}
	if target == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("监控不存在: %s", id)
	}
	oldInterval := target.IntervalMinutes
	mutate(target)
	target.UpdatedAt = nowISO()
	// 修改间隔后重设下次运行时间，避免沿用旧的排期
	if target.IntervalMinutes != oldInterval && target.IntervalMinutes > 0 {
		target.NextRunAt = time.Now().Add(time.Duration(target.IntervalMinutes) * time.Minute).Format("2006-01-02T15:04:05")
	}
	snapshot := *target
	s.mu.Unlock()

	if err := s.Save(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// DeleteMonitor 删除监控并级联删除其曲目与运行记录
func (s *Store) DeleteMonitor(id string) error {
	s.mu.Lock()
	kept := make([]*Monitor, 0, len(s.data.Monitors))
	for _, m := range s.data.Monitors {
		if m.ID != id {
			kept = append(kept, m)
		}
	}
	s.data.Monitors = kept
	for k := range s.data.Tracks {
		if strings.HasPrefix(k, id+"|") {
			delete(s.data.Tracks, k)
		}
	}
	runs := make([]*Run, 0, len(s.data.Runs))
	for _, r := range s.data.Runs {
		if r.MonitorID != id {
			runs = append(runs, r)
		}
	}
	s.data.Runs = runs
	s.mu.Unlock()
	return s.Save()
}

// DueMonitors 返回到点且启用中的监控（按下次运行时间升序）
func (s *Store) DueMonitors(limit int) []*Monitor {
	now := nowISO()
	s.mu.RLock()
	defer s.mu.RUnlock()

	due := make([]*Monitor, 0)
	for _, m := range s.data.Monitors {
		if !m.Enabled {
			continue
		}
		if m.NextRunAt == "" || m.NextRunAt <= now {
			due = append(due, m)
		}
	}
	sort.Slice(due, func(a, b int) bool { return due[a].NextRunAt < due[b].NextRunAt })
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due
}

// SetNextRun 重设下次运行时间
func (s *Store) SetNextRun(id string, minutes int) {
	if minutes <= 0 {
		minutes = DefaultIntervalMinutes
	}
	next := time.Now().Add(time.Duration(minutes) * time.Minute).Format("2006-01-02T15:04:05")
	s.mu.Lock()
	for _, m := range s.data.Monitors {
		if m.ID == id {
			m.NextRunAt = next
			m.LastRunAt = nowISO()
			break
		}
	}
	s.mu.Unlock()
}

// ── 曲目记录 ──

// ExistingKeys 返回某监控下已登记的曲目键集合
func (s *Store) ExistingKeys(monitorID string) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool)
	prefix := monitorID + "|"
	for k := range s.data.Tracks {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = true
		}
	}
	return out
}

// GetTrack 查询单条曲目记录
func (s *Store) GetTrack(monitorID, source, songID string) (*Track, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.data.Tracks[trackStoreKey(monitorID, source, songID)]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

// UpsertTrack 写入/更新曲目记录
func (s *Store) UpsertTrack(t *Track) {
	key := trackStoreKey(t.MonitorID, t.Source, t.SongID)
	now := nowISO()

	s.mu.Lock()
	defer s.mu.Unlock()

	if old, ok := s.data.Tracks[key]; ok {
		old.Name = t.Name
		old.Artist = t.Artist
		old.Album = t.Album
		old.Duration = t.Duration
		old.Cover = t.Cover
		old.Status = t.Status
		if t.QualityActual != "" {
			old.QualityActual = t.QualityActual
		}
		if t.Bitrate != "" {
			old.Bitrate = t.Bitrate
		}
		// 仅在非空时覆盖文件路径，避免重试失败把已有路径清空
		if t.FilePath != "" {
			old.FilePath = t.FilePath
		}
		old.Error = t.Error
		old.HitCount++
		old.LastSeen = now
		return
	}

	t.HitCount = 1
	t.FirstSeen = now
	t.LastSeen = now
	cp := *t
	s.data.Tracks[key] = &cp
}

// TouchTrack 仅刷新命中计数与最后发现时间，不改变状态
func (s *Store) TouchTrack(monitorID, source, songID string) {
	key := trackStoreKey(monitorID, source, songID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.data.Tracks[key]; ok {
		t.HitCount++
		t.LastSeen = nowISO()
	}
}

// MarkTrack 由网页端「补下载」完成后回写曲目状态。
//
// filePath 可为空（下载器落盘路径不一定回传得全）—— BuildPlan 对
// 「downloaded 且无路径」按信任处理，不再重复登记。
func (s *Store) MarkTrack(monitorID, source, songID, status, filePath string) bool {
	key := trackStoreKey(monitorID, source, songID)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Tracks[key]
	if !ok {
		return false
	}
	t.Status = status
	t.Error = ""
	t.LastSeen = nowISO()
	if strings.TrimSpace(filePath) != "" {
		t.FilePath = filePath
	}
	return true
}

// ListTracks 列出曲目，可按状态过滤
func (s *Store) ListTracks(monitorID, status string, limit, offset int) ([]*Track, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	all := make([]*Track, 0)
	for _, t := range s.data.Tracks {
		if monitorID != "" && t.MonitorID != monitorID {
			continue
		}
		if status != "" && t.Status != status {
			continue
		}
		cp := *t
		all = append(all, &cp)
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].LastSeen != all[b].LastSeen {
			return all[a].LastSeen > all[b].LastSeen
		}
		return all[a].Name < all[b].Name
	})

	total := len(all)
	if offset > 0 {
		if offset >= len(all) {
			return []*Track{}, total
		}
		all = all[offset:]
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, total
}

// TrackStats 曲目状态统计
func (s *Store) TrackStats(monitorID string) map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := map[string]int{
		StatusPending: 0, StatusDownloaded: 0, StatusSkipped: 0,
		StatusFailed: 0, StatusMissing: 0,
	}
	for _, t := range s.data.Tracks {
		if monitorID != "" && t.MonitorID != monitorID {
			continue
		}
		out[t.Status]++
	}
	return out
}

// ── 运行记录 ──

// StartRun 开启一次运行记录
func (s *Store) StartRun(monitorID string, baseline bool) *Run {
	r := &Run{
		ID:        fmt.Sprintf("run_%d", time.Now().UnixNano()),
		MonitorID: monitorID,
		StartedAt: nowISO(),
		Status:    "running",
		Baseline:  baseline,
	}
	s.mu.Lock()
	s.data.Runs = append(s.data.Runs, r)
	// 只保留最近 200 条，避免文件无限增长
	if len(s.data.Runs) > 200 {
		s.data.Runs = s.data.Runs[len(s.data.Runs)-200:]
	}
	s.mu.Unlock()
	return r
}

// FinishRun 结束运行记录
func (s *Store) FinishRun(r *Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.data.Runs {
		if existing.ID == r.ID {
			existing.FinishedAt = nowISO()
			existing.Status = r.Status
			existing.Found = r.Found
			existing.NewItems = r.NewItems
			existing.Downloaded = r.Downloaded
			existing.Skipped = r.Skipped
			existing.Failed = r.Failed
			existing.Message = truncate(r.Message, 500)
			existing.Log = truncate(r.Log, 20000)
			return
		}
	}
}

// ListRuns 列出运行记录
func (s *Store) ListRuns(monitorID string, limit int) []*Run {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Run, 0)
	for i := len(s.data.Runs) - 1; i >= 0; i-- {
		r := s.data.Runs[i]
		if monitorID != "" && r.MonitorID != monitorID {
			continue
		}
		cp := *r
		out = append(out, &cp)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// RecoverRunningRuns 服务启动时收尾残留的 running 记录，
// 否则界面会永久显示「运行中」。
func (s *Store) RecoverRunningRuns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.data.Runs {
		if r.Status == "running" {
			r.Status = "error"
			r.FinishedAt = nowISO()
			r.Message = "服务重启，任务中断"
			n++
		}
	}
	return n
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
