package monitor

import (
	"strings"
	"testing"
)

func planFixture() *Monitor {
	return &Monitor{
		ID: "mon_test", Kind: KindPlaylist, BaselineDone: true,
		AutoDownload: true, Quality: QualityLossless,
		MaxDownloads: 30,
	}
}

func trackerFor(tracks ...*Track) func(source, songID string) (*Track, bool) {
	m := map[string]*Track{}
	for _, t := range tracks {
		m[TrackKey(t.Source, t.SongID)] = t
	}
	return func(source, songID string) (*Track, bool) {
		t, ok := m[TrackKey(source, songID)]
		return t, ok
	}
}

// 后端自抓的曲目没有直链：即使开了自动下载，也只能登记为 pending（等网页端补下载），
// 且不能占用单轮下载预算。
func TestBuildPlanRegistersSongsWithoutURL(t *testing.T) {
	mon := planFixture()
	songs := []Song{
		{Source: "wy", ID: "1", Name: "无链接歌", Artist: "A"},
		{Source: "wy", ID: "2", Name: "有链接歌", Artist: "B", URL: "https://example.com/a.flac"},
	}
	existing := map[string]bool{"wy:1": true, "wy:2": true}
	noopGet := func(string, string) (*Track, bool) { return nil, false }

	plan := BuildPlan(mon, songs, existing, func(string) bool { return false }, noopGet, false)

	if plan.ToDownload != 1 || plan.Registered != 1 || plan.Skipped != 0 {
		t.Fatalf("期望 download=1 registered=1 skip=0，实际 %+v", plan)
	}
	var noURL *PlanItem
	for i := range plan.Items {
		if plan.Items[i].Song.ID == "1" {
			noURL = &plan.Items[i]
		}
	}
	if noURL == nil || noURL.Action != "register" || !strings.Contains(noURL.Reason, "补下载") {
		t.Fatalf("无直链曲目应登记为待补下载，实际 %+v", noURL)
	}
}

// 网页端补下载完成后标记 downloaded 但可能没有文件路径 → 后续轮次应信任标记并跳过，
// 不能反复把同一首歌登记回 pending。
func TestBuildPlanTrustsDownloadedMarkWithoutPath(t *testing.T) {
	mon := planFixture()
	songs := []Song{
		{Source: "wy", ID: "d1", Name: "已补下载", Artist: "A"},
		{Source: "wy", ID: "d2", Name: "带路径已下载", Artist: "B"},
	}
	get := trackerFor(
		&Track{MonitorID: mon.ID, Source: "wy", SongID: "d1", Status: StatusDownloaded, FilePath: ""},
		&Track{MonitorID: mon.ID, Source: "wy", SongID: "d2", Status: StatusDownloaded, FilePath: "/music/d2.flac"},
	)
	existing := map[string]bool{"wy:d1": true, "wy:d2": true}

	plan := BuildPlan(mon, songs, existing, func(p string) bool { return p == "/music/d2.flac" }, get, false)

	if plan.Skipped != 2 || plan.Registered != 0 || plan.ToDownload != 0 {
		t.Fatalf("两条 downloaded 记录都应跳过，实际 %+v", plan)
	}
}

// downloaded 且带路径但文件已被删 → 不能跳过，应重新走登记/下载决策
func TestBuildPlanRedownloadsMissingFileWithMark(t *testing.T) {
	mon := planFixture()
	songs := []Song{{Source: "wy", ID: "d1", Name: "文件丢了", Artist: "A"}}
	get := trackerFor(
		&Track{MonitorID: mon.ID, Source: "wy", SongID: "d1", Status: StatusDownloaded, FilePath: "/music/gone.flac"},
	)
	plan := BuildPlan(mon, songs, map[string]bool{"wy:d1": true}, func(string) bool { return false }, get, false)

	if plan.Registered != 1 || plan.Items[0].Action != "register" {
		t.Fatalf("文件缺失应重新登记待补下载，实际 %+v", plan)
	}
}

// 后端具备取链能力（已配置服务型音源）时，无直链曲目应产出 resolve 动作，
// 并**占用单轮下载预算**（否则一次几百首会打爆音源服务）。
func TestBuildPlanResolvesSongsWhenBackendCanResolve(t *testing.T) {
	mon := planFixture()
	mon.MaxDownloads = 2
	songs := []Song{
		{Source: "wy", ID: "r1", Name: "取链一", Artist: "A"},
		{Source: "wy", ID: "r2", Name: "取链二", Artist: "B"},
		{Source: "wy", ID: "r3", Name: "超预算", Artist: "C"},
	}
	noopGet := func(string, string) (*Track, bool) { return nil, false }

	plan := BuildPlan(mon, songs, map[string]bool{}, func(string) bool { return false }, noopGet, true)

	if plan.ToResolve != 2 || plan.Registered != 0 {
		t.Fatalf("期望 resolve=2 registered=0，实际 %+v", plan)
	}
	actions := map[string]string{}
	for _, it := range plan.Items {
		actions[it.Song.ID] = it.Action
	}
	if actions["r1"] != "resolve" || actions["r2"] != "resolve" {
		t.Fatalf("预算内的无直链曲目应为 resolve，实际 %+v", actions)
	}
	if actions["r3"] != "skip" {
		t.Fatalf("超出预算的曲目应跳过，实际 %s", actions["r3"])
	}
	// 取链成功后同样会下载，因此计入 to_download
	if plan.ToDownload != 2 {
		t.Fatalf("resolve 应计入 to_download，实际 %d", plan.ToDownload)
	}
}

// ⚠️ 基线轮绝不能调音源服务：即使后端具备取链能力，首轮也只登记。
func TestBuildPlanBaselineNeverResolves(t *testing.T) {
	mon := planFixture()
	mon.BaselineDone = false
	songs := []Song{{Source: "wy", ID: "b1", Name: "基线歌", Artist: "A"}}

	plan := BuildPlan(mon, songs, map[string]bool{}, func(string) bool { return false },
		func(string, string) (*Track, bool) { return nil, false }, true)

	if !plan.Baseline {
		t.Fatal("应识别为基线轮")
	}
	if plan.ToResolve != 0 || plan.Registered != 1 {
		t.Fatalf("基线轮只能登记、不得取链，实际 %+v", plan)
	}
	if plan.Items[0].Action != "register" {
		t.Fatalf("基线轮动作应为 register，实际 %s", plan.Items[0].Action)
	}
}

// canResolve=false 时行为必须与 v2.1.10 完全一致（回归保护）。
func TestBuildPlanWithoutResolverKeepsLegacyBehavior(t *testing.T) {
	mon := planFixture()
	songs := []Song{{Source: "wy", ID: "n1", Name: "无通道", Artist: "A"}}

	plan := BuildPlan(mon, songs, map[string]bool{}, func(string) bool { return false },
		func(string, string) (*Track, bool) { return nil, false }, false)

	if plan.ToResolve != 0 || plan.Registered != 1 || plan.ToDownload != 0 {
		t.Fatalf("无取链能力时应维持登记 pending 的旧行为，实际 %+v", plan)
	}
}

func TestMarkTrack(t *testing.T) {
	s := NewStore(t.TempDir())
	s.UpsertTrack(&Track{MonitorID: "m1", Source: "wy", SongID: "s1", Name: "x", Status: StatusPending, Error: "待网页端一键补下载"})

	if !s.MarkTrack("m1", "wy", "s1", StatusDownloaded, "/music/s1.flac") {
		t.Fatal("MarkTrack 应命中已登记曲目")
	}
	tr, _ := s.GetTrack("m1", "wy", "s1")
	if tr.Status != StatusDownloaded || tr.FilePath != "/music/s1.flac" || tr.Error != "" {
		t.Fatalf("标记后状态不符：%+v", tr)
	}

	// 空路径不覆盖已有路径
	s.MarkTrack("m1", "wy", "s1", StatusDownloaded, "")
	if tr, _ = s.GetTrack("m1", "wy", "s1"); tr.FilePath != "/music/s1.flac" {
		t.Fatalf("空 filePath 不应清空已有路径：%+v", tr)
	}
	if s.MarkTrack("m1", "wy", "missing", StatusDownloaded, "") {
		t.Fatal("未登记的曲目不应被 MarkTrack 创建")
	}
}
