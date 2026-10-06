package monitor

import (
	"os"
	"path/filepath"
	"testing"
)

func testSongs() []Song {
	// 带 URL：BuildPlan 只把「有直链」的曲目排进下载（无直链的登记为待补下载，
	// 见 plan_backend_test.go），这些用例考察的是基线/预算/文件存在性逻辑。
	return []Song{
		{Source: "wy", ID: "1", Name: "夜曲", Artist: "周杰伦", Duration: 227, URL: "https://cdn.example/1.flac"},
		{Source: "wy", ID: "2", Name: "晴天", Artist: "周杰伦", Duration: 269, URL: "https://cdn.example/2.flac"},
		{Source: "wy", ID: "3", Name: "夜曲 (Live)", Artist: "周杰伦", Duration: 230, URL: "https://cdn.example/3.flac"},
		{Source: "wy", ID: "4", Name: "富士山下", Artist: "陈奕迅", Duration: 245, URL: "https://cdn.example/4.flac"},
	}
}

// ── 过滤 ──

func TestFilterSongRejectsVariantsByDefault(t *testing.T) {
	v := FilterSong(Song{Source: "wy", ID: "3", Name: "夜曲 (Live)"}, nil, nil, 0, false)
	if v.Keep {
		t.Error("默认应拒绝改编版（Live）")
	}
	if v.Reason == "" {
		t.Error("应给出拒绝原因")
	}
}

func TestFilterSongAllowsVariantWhenConfigured(t *testing.T) {
	v := FilterSong(Song{Source: "wy", ID: "3", Name: "夜曲 (Live)"}, nil, nil, 0, true)
	if !v.Keep {
		t.Errorf("allowVariant=true 时应放行，得到 %q", v.Reason)
	}
}

func TestFilterSongRejectsPreviewEvenWithVariantAllowed(t *testing.T) {
	v := FilterSong(Song{Source: "wy", ID: "5", Name: "夜曲 试听"}, nil, nil, 0, true)
	if v.Keep {
		t.Error("试听片段即使在允许改编版时也必须拒绝")
	}
}

func TestFilterSongRequiresID(t *testing.T) {
	if v := FilterSong(Song{Source: "wy", Name: "夜曲"}, nil, nil, 0, true); v.Keep {
		t.Error("缺少曲目 ID 应被拒绝")
	}
	if v := FilterSong(Song{Source: "wy", ID: "1", Name: "  "}, nil, nil, 0, true); v.Keep {
		t.Error("歌名为空应被拒绝")
	}
}

func TestMatchKeywords(t *testing.T) {
	s := Song{Name: "夜曲", Artist: "周杰伦", Album: "十一月的萧邦"}

	if v := MatchKeywords(s, []string{"周杰伦"}, nil); !v.Keep {
		t.Error("包含关键词命中应保留")
	}
	if v := MatchKeywords(s, []string{"陈奕迅"}, nil); v.Keep {
		t.Error("包含关键词未命中应剔除")
	}
	if v := MatchKeywords(s, nil, []string{"萧邦"}); v.Keep {
		t.Error("排除关键词命中应剔除")
	}
	if v := MatchKeywords(s, nil, []string{"陈奕迅"}); !v.Keep {
		t.Error("排除关键词未命中应保留")
	}
}

func TestSplitKeywords(t *testing.T) {
	got := SplitKeywords("周杰伦, 陈奕迅；邓紫棋 林俊杰")
	want := []string{"周杰伦", "陈奕迅", "邓紫棋", "林俊杰"}
	if len(got) != len(want) {
		t.Fatalf("切分结果 = %v, 期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项 = %q, 期望 %q", i, got[i], want[i])
		}
	}
}

func TestDedupeSongsBySourceAndID(t *testing.T) {
	songs := []Song{
		{Source: "wy", ID: "1", Name: "夜曲"},
		{Source: "wy", ID: "1", Name: "夜曲（重复）"},
		{Source: "wy", ID: "2", Name: "晴天"},
		{Source: "", ID: "9", Name: "无源"},
		{Source: "wy", ID: "", Name: "无ID"},
	}
	got := DedupeSongs(songs)
	if len(got) != 2 {
		t.Fatalf("去重后 = %d 首, 期望 2: %+v", len(got), got)
	}
	if got[0].Name != "夜曲" {
		t.Errorf("应保留首次出现的记录，得到 %q", got[0].Name)
	}
}

// ── 首轮基线（music-monitor 缺失的能力） ──

func TestBuildPlanFirstRunIsBaselineOnly(t *testing.T) {
	mon := &Monitor{
		ID: "m1", Name: "测试", Kind: KindPlaylist,
		AutoDownload: true, MaxDownloads: 30,
		BaselineDone: false, // 首次
	}

	plan := BuildPlan(mon, testSongs(), map[string]bool{}, nil, func(string, string) (*Track, bool) { return nil, false }, false)

	if !plan.Baseline {
		t.Error("首轮应标记为基线轮")
	}
	if plan.ToDownload != 0 {
		t.Errorf("基线轮不应下载任何曲目，却计划下载 %d 首", plan.ToDownload)
	}
	if plan.Registered != 4 {
		t.Errorf("基线轮应登记全部 4 首，实际 %d", plan.Registered)
	}
}

func TestBuildPlanSecondRunDownloads(t *testing.T) {
	mon := &Monitor{
		ID: "m1", Name: "测试", Kind: KindPlaylist,
		AutoDownload: true, MaxDownloads: 30,
		BaselineDone: true, // 基线已完成
	}

	plan := BuildPlan(mon, testSongs(), map[string]bool{}, nil, func(string, string) (*Track, bool) { return nil, false }, false)

	if plan.Baseline {
		t.Error("基线完成后不应再标记为基线轮")
	}
	// Live 版应被过滤，其余 3 首下载
	if plan.ToDownload != 3 {
		t.Errorf("计划下载 %d 首, 期望 3（Live 被过滤）", plan.ToDownload)
	}
	if plan.Skipped != 1 {
		t.Errorf("跳过 %d 首, 期望 1", plan.Skipped)
	}
}

func TestBuildPlanRespectsMaxDownloads(t *testing.T) {
	mon := &Monitor{
		ID: "m1", Name: "测试", Kind: KindPlaylist,
		AutoDownload: true, MaxDownloads: 2, BaselineDone: true,
	}

	plan := BuildPlan(mon, testSongs(), map[string]bool{}, nil, func(string, string) (*Track, bool) { return nil, false }, false)

	if plan.ToDownload != 2 {
		t.Errorf("应受单轮上限约束下载 2 首，实际 %d", plan.ToDownload)
	}
	found := false
	for _, it := range plan.Items {
		if it.Action == "skip" && it.Reason == "已达单轮下载上限" {
			found = true
		}
	}
	if !found {
		t.Error("超出上限的曲目应记录明确的跳过原因")
	}
}

func TestBuildPlanAutoDownloadOffOnlyRegisters(t *testing.T) {
	mon := &Monitor{
		ID: "m1", Name: "测试", Kind: KindPlaylist,
		AutoDownload: false, BaselineDone: true,
	}
	plan := BuildPlan(mon, testSongs(), map[string]bool{}, nil, func(string, string) (*Track, bool) { return nil, false }, false)

	if plan.ToDownload != 0 {
		t.Errorf("未开启自动下载时不应下载，实际 %d", plan.ToDownload)
	}
	if plan.Registered == 0 {
		t.Error("应登记曲目")
	}
}

// TestBuildPlanRedownloadsWhenFileDeleted 关键：文件被删后必须重新下载，不能僵死
func TestBuildPlanRedownloadsWhenFileDeleted(t *testing.T) {
	mon := &Monitor{
		ID: "m1", Name: "测试", Kind: KindPlaylist,
		AutoDownload: true, MaxDownloads: 30, BaselineDone: true,
	}
	existing := map[string]bool{"wy:1": true, "wy:2": true}

	// 记录显示已下载，但文件实际已不存在
	getTrack := func(source, songID string) (*Track, bool) {
		return &Track{
			MonitorID: "m1", Source: source, SongID: songID,
			Status: StatusDownloaded, FilePath: "/nonexistent/" + songID + ".mp3",
		}, true
	}
	exists := func(string) bool { return false } // 文件都没了

	plan := BuildPlan(mon, testSongs(), existing, exists, getTrack, false)

	// 已登记的两首（1=夜曲、2=晴天）应重新进入下载；3=Live 被过滤；4=富士山下新曲
	if plan.ToDownload != 3 {
		t.Errorf("文件丢失后应重新下载 3 首（1、2、4），实际 %d", plan.ToDownload)
	}
}

func TestBuildPlanSkipsWhenFileStillExists(t *testing.T) {
	mon := &Monitor{
		ID: "m1", Name: "测试", Kind: KindPlaylist,
		AutoDownload: true, MaxDownloads: 30, BaselineDone: true,
	}
	existing := map[string]bool{"wy:1": true, "wy:2": true}

	getTrack := func(source, songID string) (*Track, bool) {
		return &Track{
			MonitorID: "m1", Source: source, SongID: songID,
			Status: StatusDownloaded, FilePath: "/music/" + songID + ".mp3",
		}, true
	}
	exists := func(string) bool { return true } // 文件都在

	plan := BuildPlan(mon, testSongs(), existing, exists, getTrack, false)

	// 跳过 3 首：已下载且存在的 2 首 + 被过滤的 Live 版；只下载新增的「富士山下」
	if plan.Skipped != 3 {
		t.Errorf("应跳过 3 首（2 首已存在 + 1 首 Live 被过滤），实际 %d", plan.Skipped)
	}
	if plan.ToDownload != 1 {
		t.Errorf("只应下载新增的 1 首（富士山下），实际 %d", plan.ToDownload)
	}
}

// ── 干跑 ──

func TestPreviewDoesNotPlanDownloads(t *testing.T) {
	mon := &Monitor{ID: "m1", Kind: KindPlaylist, BaselineDone: true}
	res := Preview(mon, testSongs())

	if res.Total != 4 {
		t.Errorf("总数 = %d, 期望 4", res.Total)
	}
	if res.Filtered != 3 {
		t.Errorf("过滤后 = %d, 期望 3（Live 被剔除）", res.Filtered)
	}
	if len(res.Rejected) != 1 {
		t.Errorf("应记录 1 条拒绝原因，实际 %d", len(res.Rejected))
	}
	if res.Rejected[0].Reason == "" {
		t.Error("拒绝原因不应为空（便于用户与 AI 理解）")
	}
}

func TestPreviewWarnsOnBaseline(t *testing.T) {
	mon := &Monitor{ID: "m1", Kind: KindPlaylist, BaselineDone: false}
	res := Preview(mon, testSongs())
	if !res.Baseline {
		t.Error("应标记为基线轮")
	}
	if len(res.Warnings) == 0 {
		t.Error("基线轮应给出提示")
	}
}

func TestPreviewWarnsOnEmptyDiscovery(t *testing.T) {
	mon := &Monitor{ID: "m1", Kind: KindPlaylist}
	res := Preview(mon, nil)
	if len(res.Warnings) == 0 {
		t.Error("没发现曲目时应给出提示（歌单可能失效）")
	}
}

func TestSummarizePlan(t *testing.T) {
	s := SummarizePlan(Plan{Baseline: true, Found: 10, ToDownload: 0, Registered: 10, Skipped: 0})
	if s == "" {
		t.Fatal("摘要不应为空")
	}
	if !contains(s, "首次检查") || !contains(s, "发现 10 首") {
		t.Errorf("摘要内容不完整: %q", s)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// ── 存储 ──

func TestStoreMonitorCRUD(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	m, err := s.CreateMonitor(&Monitor{
		Name: "我的歌单", Kind: KindPlaylist, AutoDownload: true,
		Target: Target{Playlists: []PlaylistRef{{Link: "https://music.163.com/playlist?id=1"}}},
	})
	if err != nil {
		t.Fatalf("创建监控失败: %v", err)
	}
	if m.BaselineDone {
		t.Error("新建监控应处于未完成基线的状态")
	}
	if m.NextRunAt == "" {
		t.Error("新建监控应设置下次运行时间（一个 tick 内即跑首轮）")
	}

	got, ok := s.GetMonitor(m.ID)
	if !ok {
		t.Fatal("应能查到刚创建的监控")
	}
	if got.Name != "我的歌单" {
		t.Errorf("名称 = %q", got.Name)
	}

	if _, err := s.UpdateMonitor(m.ID, func(mm *Monitor) { mm.Name = "改名了" }); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	got, _ = s.GetMonitor(m.ID)
	if got.Name != "改名了" {
		t.Errorf("更新后名称 = %q", got.Name)
	}

	if err := s.DeleteMonitor(m.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, ok := s.GetMonitor(m.ID); ok {
		t.Error("删除后不应还能查到")
	}
}

func TestStoreCreateMonitorValidation(t *testing.T) {
	s := NewStore(t.TempDir())

	if _, err := s.CreateMonitor(&Monitor{Name: "", Kind: KindPlaylist}); err == nil {
		t.Error("空名称应报错")
	}
	if _, err := s.CreateMonitor(&Monitor{Name: "x", Kind: Kind("bogus")}); err == nil {
		t.Error("未知监控类型应报错")
	}
}

func TestStorePersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	m, _ := s.CreateMonitor(&Monitor{Name: "持久化测试", Kind: KindChart, AutoDownload: true})
	s.UpsertTrack(&Track{
		MonitorID: m.ID, Source: "wy", SongID: "1",
		Name: "夜曲", Artist: "周杰伦", Status: StatusDownloaded, FilePath: "/music/1.mp3",
	})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded := NewStore(dir)
	got, ok := reloaded.GetMonitor(m.ID)
	if !ok {
		t.Fatal("重新加载后应能查到监控")
	}
	if got.Name != "持久化测试" {
		t.Errorf("名称 = %q", got.Name)
	}
	tr, ok := reloaded.GetTrack(m.ID, "wy", "1")
	if !ok {
		t.Fatal("重新加载后应能查到曲目")
	}
	if tr.FilePath != "/music/1.mp3" {
		t.Errorf("文件路径 = %q", tr.FilePath)
	}
}

func TestStoreUpsertTrackKeepsFilePathOnRetryFailure(t *testing.T) {
	s := NewStore(t.TempDir())
	m, _ := s.CreateMonitor(&Monitor{Name: "x", Kind: KindPlaylist})

	s.UpsertTrack(&Track{MonitorID: m.ID, Source: "wy", SongID: "1", Status: StatusDownloaded, FilePath: "/music/1.mp3"})
	// 重试失败时不应把已有路径清空
	s.UpsertTrack(&Track{MonitorID: m.ID, Source: "wy", SongID: "1", Status: StatusFailed, Error: "网络错误"})

	tr, _ := s.GetTrack(m.ID, "wy", "1")
	if tr.FilePath != "/music/1.mp3" {
		t.Errorf("失败重试不应清空文件路径，得到 %q", tr.FilePath)
	}
	if tr.Status != StatusFailed {
		t.Errorf("状态应为 failed，得到 %q", tr.Status)
	}
	if tr.HitCount != 2 {
		t.Errorf("命中计数 = %d, 期望 2", tr.HitCount)
	}
}

func TestStoreDueMonitors(t *testing.T) {
	s := NewStore(t.TempDir())
	due, _ := s.CreateMonitor(&Monitor{Name: "到点", Kind: KindPlaylist, Enabled: true})
	future, _ := s.CreateMonitor(&Monitor{Name: "未到点", Kind: KindPlaylist, Enabled: true})

	s.mu.Lock()
	s.data.Monitors[0].NextRunAt = "2000-01-01T00:00:00" // due
	s.data.Monitors[1].NextRunAt = "2999-01-01T00:00:00" // future
	s.mu.Unlock()

	got := s.DueMonitors(10)
	if len(got) != 1 {
		t.Fatalf("到点监控数 = %d, 期望 1", len(got))
	}
	if got[0].ID != due.ID {
		t.Errorf("到点的应是 %s", due.ID)
	}
	_ = future
}

func TestStoreDueMonitorsSkipsDisabled(t *testing.T) {
	s := NewStore(t.TempDir())
	m, _ := s.CreateMonitor(&Monitor{Name: "已停用", Kind: KindPlaylist, Enabled: false})
	s.mu.Lock()
	for _, mm := range s.data.Monitors {
		if mm.ID == m.ID {
			mm.NextRunAt = "2000-01-01T00:00:00"
		}
	}
	s.mu.Unlock()

	if got := s.DueMonitors(10); len(got) != 0 {
		t.Errorf("停用的监控不应被调度，得到 %d 个", len(got))
	}
}

func TestStoreRecoverRunningRuns(t *testing.T) {
	s := NewStore(t.TempDir())
	m, _ := s.CreateMonitor(&Monitor{Name: "x", Kind: KindPlaylist})
	r := s.StartRun(m.ID, false)

	if r.Status != "running" {
		t.Fatalf("新运行记录状态 = %q", r.Status)
	}
	n := s.RecoverRunningRuns()
	if n != 1 {
		t.Errorf("应恢复 1 条 running，实际 %d", n)
	}
	runs := s.ListRuns(m.ID, 10)
	if len(runs) != 1 || runs[0].Status != "error" {
		t.Errorf("残留运行应被收尾为 error，得到 %+v", runs)
	}
}

func TestStoreDeleteMonitorCascades(t *testing.T) {
	s := NewStore(t.TempDir())
	m, _ := s.CreateMonitor(&Monitor{Name: "x", Kind: KindPlaylist})
	s.UpsertTrack(&Track{MonitorID: m.ID, Source: "wy", SongID: "1", Status: StatusPending})
	s.StartRun(m.ID, false)

	if err := s.DeleteMonitor(m.ID); err != nil {
		t.Fatal(err)
	}
	if tracks, total := s.ListTracks(m.ID, "", 100, 0); total != 0 || len(tracks) != 0 {
		t.Errorf("删除监控应级联删除曲目，仍有 %d 条", total)
	}
	if runs := s.ListRuns(m.ID, 10); len(runs) != 0 {
		t.Errorf("删除监控应级联删除运行记录，仍有 %d 条", len(runs))
	}
}

func TestStoreTrackStats(t *testing.T) {
	s := NewStore(t.TempDir())
	m, _ := s.CreateMonitor(&Monitor{Name: "x", Kind: KindPlaylist})
	s.UpsertTrack(&Track{MonitorID: m.ID, Source: "wy", SongID: "1", Status: StatusDownloaded})
	s.UpsertTrack(&Track{MonitorID: m.ID, Source: "wy", SongID: "2", Status: StatusFailed})
	s.UpsertTrack(&Track{MonitorID: m.ID, Source: "wy", SongID: "3", Status: StatusSkipped})

	stats := s.TrackStats(m.ID)
	if stats[StatusDownloaded] != 1 || stats[StatusFailed] != 1 || stats[StatusSkipped] != 1 {
		t.Errorf("统计不正确: %+v", stats)
	}
}

func TestStoreCorruptedFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "monitors.json"), []byte("{{{ broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if len(s.ListMonitors()) != 0 {
		t.Error("存储损坏时应从空开始")
	}
}

func TestQualityNormalization(t *testing.T) {
	if NormalizeQuality("") != QualityLossless {
		t.Error("空值应回退到无损")
	}
	if NormalizeQuality("BOGUS") != QualityLossless {
		t.Error("未知值应回退到无损")
	}
	if NormalizeQuality("HiRes") != QualityHiRes {
		t.Error("应大小写不敏感")
	}
	if QualityLossless.MinKbps() != 700 || QualityHiRes.MinKbps() != 1400 {
		t.Error("码率门槛不正确")
	}
	if !QualityLossless.NeedsLossless() || QualityHigh.NeedsLossless() {
		t.Error("无损判定不正确")
	}
}
