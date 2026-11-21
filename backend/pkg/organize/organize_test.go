package organize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败 %s：%v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败 %s：%v", path, err)
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// 用户要的那种样式：歌手 / 专辑 / 曲目，同一曲目的 flac + jpg + lrc
// 同名同目录地一起搬过去。
func TestPlanGroupsByArtistAlbumTrack(t *testing.T) {
	root := t.TempDir()
	const base = "周杰伦 - 双截棍"
	audio := filepath.Join(root, base+".flac")
	cover := filepath.Join(root, base+".jpg")
	lyric := filepath.Join(root, base+".lrc")
	writeTestFile(t, audio, "fake-flac")
	writeTestFile(t, cover, "fake-jpg")
	writeTestFile(t, lyric, "fake-lrc")

	lookup := func(p string) (Meta, bool) {
		if p == audio {
			return Meta{Title: "双截棍", Artist: "周杰伦", Album: "范特西", Track: 1}, true
		}
		return Meta{}, false
	}

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate(), Lookup: lookup})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	if plan.Stats.Audio != 1 || plan.Stats.Move != 1 || plan.Stats.Conflict != 0 {
		t.Fatalf("统计不对：%+v", plan.Stats)
	}
	if plan.Stats.Sidecars != 2 {
		t.Errorf("应当带上 2 个附属文件，得到 %d", plan.Stats.Sidecars)
	}

	albumDir := filepath.Join(root, "周杰伦", "范特西")
	wantAudio := filepath.Join(albumDir, "01 双截棍.flac")
	if got := plan.Items[0].Target; got != wantAudio {
		t.Fatalf("目标路径不对\n 期望 %s\n 实际 %s", wantAudio, got)
	}

	res, err := Apply(plan)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if res.Moved != 1 || res.Failed != 0 {
		t.Fatalf("执行结果不对：%+v", res)
	}
	if res.Sidecars != 2 {
		t.Errorf("应当搬了 2 个附属文件，得到 %d", res.Sidecars)
	}

	// 三件套必须同目录同名 —— 这正是「别让歌词和封面跟丢」的全部意义。
	for _, ext := range []string{".flac", ".jpg", ".lrc"} {
		p := filepath.Join(albumDir, "01 双截棍"+ext)
		if !pathExists(p) {
			t.Errorf("应当存在 %s", p)
		}
	}
	if pathExists(audio) || pathExists(cover) || pathExists(lyric) {
		t.Error("原位置不该再留文件")
	}
}

// 没有曲库索引、也没有标签时，至少要靠「歌手 - 歌名」的文件名把歌手认出来。
func TestPlanFallsBackToFileName(t *testing.T) {
	root := t.TempDir()
	audio := filepath.Join(root, "周杰伦 - 七里香.flac")
	writeTestFile(t, audio, "fake-flac")

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate()})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	want := filepath.Join(root, "周杰伦", "未知专辑", "七里香.flac")
	if got := plan.Items[0].Target; got != want {
		t.Errorf("目标路径不对\n 期望 %s\n 实际 %s", want, got)
	}
	if got := plan.Items[0].Meta.Source; !strings.Contains(got, "filename") {
		t.Errorf("元数据来源应当标出来自文件名，得到 %q", got)
	}
}

// 两首歌撞到同一个目标位置时，第二首不能覆盖第一首 —— 换名字（" (2)"）搬，
// 而不是整首跳过（合辑 / 双碟 / 两张专辑各有一首同名歌都会撞）。
func TestPlanDedupesSameTarget(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "甲", "周杰伦 - 双截棍.flac"), "a")
	writeTestFile(t, filepath.Join(root, "乙", "周杰伦 - 双截棍.flac"), "b")

	plan, err := PlanOf(Options{Root: root})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	if plan.Stats.Move != 2 || plan.Stats.Conflict != 0 {
		t.Fatalf("两首都该搬走（第二首换名），实际 %+v", plan.Stats)
	}
	targets := map[string]string{}
	for _, item := range plan.Items {
		targets[item.Target] = item.Source
		if item.Status == "move" && strings.Contains(item.Target, " (2)") {
			if !strings.Contains(item.Reason, "重名") {
				t.Errorf("换名原因没写清楚：%q", item.Reason)
			}
		}
	}
	if len(targets) != 2 {
		t.Fatalf("两首的目标路径不该相同：%v", targets)
	}
}

// 目标位置已经有同名附属文件（别的曲子带的歌词 / 封面）时，不能覆盖它。
func TestPlanKeepExistingSidecar(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "散放", "周杰伦 - 双截棍.flac"), "a")
	writeTestFile(t, filepath.Join(root, "散放", "周杰伦 - 双截棍.lrc"), "old lyric")
	// 目标目录里已经放着一份同名歌词（别的曲子带的）
	writeTestFile(t, filepath.Join(root, "周杰伦", "未知专辑", "双截棍.lrc"), "someone else")

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate(), Lookup: func(string) (Meta, bool) {
		return Meta{Title: "双截棍", Artist: "周杰伦", Album: "未知专辑"}, true
	}})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	var audio *Item
	for i := range plan.Items {
		if strings.HasSuffix(plan.Items[i].Source, ".flac") {
			audio = &plan.Items[i]
		}
	}
	if audio == nil {
		t.Fatal("没找到音频条目")
	}
	for _, mv := range audio.Moves {
		if seenHasSuffix(mv.To, ".lrc") {
			t.Fatalf("不该把歌词覆盖到已存在的同名文件上：%s", mv.To)
		}
	}
	found := false
	for _, w := range plan.Warnings {
		if strings.Contains(w, "保留原样") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应当有「保留原样」的警告，实际 %v", plan.Warnings)
	}
}

func seenHasSuffix(s, suffix string) bool { return strings.HasSuffix(s, suffix) }

// 已经在目标位置上的，不该被判成「要搬」，也不该报冲突。
func TestPlanRecognizesAlreadyInPlace(t *testing.T) {
	root := t.TempDir()
	const rel = "周杰伦/范特西/01 双截棍.flac"
	audio := filepath.Join(root, filepath.FromSlash(rel))
	writeTestFile(t, audio, "fake-flac")

	lookup := func(string) (Meta, bool) {
		return Meta{Title: "双截棍", Artist: "周杰伦", Album: "范特西", Track: 1}, true
	}
	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate(), Lookup: lookup})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	if plan.Stats.Same != 1 || plan.Stats.Move != 0 || plan.Stats.Conflict != 0 {
		t.Fatalf("应当判成「已就位」，实际 %+v", plan.Stats)
	}
	if len(plan.Items[0].Moves) != 0 {
		t.Error("已就位的条目不该带移动动作")
	}
}

// 整理可撤回：撤回后原路径必须回来，新路径必须消失。
func TestApplyThenUndoRestoresOriginals(t *testing.T) {
	root := t.TempDir()
	audio := filepath.Join(root, "周杰伦 - 双截棍.flac")
	lyric := filepath.Join(root, "周杰伦 - 双截棍.lrc")
	writeTestFile(t, audio, "fake-flac")
	writeTestFile(t, lyric, "fake-lrc")

	plan, err := PlanOf(Options{Root: root})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	res, err := Apply(plan)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if pathExists(audio) || pathExists(lyric) {
		t.Fatal("执行后原位置不该还留着文件")
	}
	moved := plan.Items[0].Target
	if !pathExists(moved) {
		t.Fatalf("目标不存在：%s", moved)
	}

	undo, err := Undo(res.Journal, root)
	if err != nil {
		t.Fatalf("撤回失败：%v", err)
	}
	if undo.Failed != 0 {
		t.Fatalf("撤回有失败项：%v", undo.Errors)
	}
	if !pathExists(audio) || !pathExists(lyric) {
		t.Error("撤回后原文件应当回来")
	}
	if pathExists(moved) {
		t.Errorf("撤回后目标位置不该还留着 %s", moved)
	}
}

// 模板里写 ../ 之类的越界写法时，绝对不能搬到整理目录外面去。
func TestPlanKeepsTargetsInsideRoot(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "周杰伦 - 双截棍.flac"), "fake-flac")

	plan, err := PlanOf(Options{Root: root, Template: Template{Dir: "../外面", File: "{title}"}})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	target := plan.Items[0].Target
	if !withinRoot(root, target) {
		t.Fatalf("目标跑出整理目录了：%s", target)
	}
	// `../` 那一段被丢掉，「外面」当成普通目录名用。
	want := filepath.Join(root, "外面", "双截棍.flac")
	if target != want {
		t.Errorf("目标路径不对\n 期望 %s\n 实际 %s", want, target)
	}
	if plan.Stats.Skip != 0 {
		t.Errorf("不该有跳过项，实际 %+v", plan.Stats)
	}
}

// 样式是可以自己写的：占位符按需拼，空占位符不留多余空格。
func TestCustomTemplate(t *testing.T) {
	root := t.TempDir()
	audio := filepath.Join(root, "周杰伦 - 双截棍.flac")
	writeTestFile(t, audio, "fake-flac")

	// 没有曲序时 {track:02d} 应当整段消失，而不是变成「00」或者留个空格。
	plan, err := PlanOf(Options{Root: root, Template: Template{Dir: "{artist}", File: "{track:02d} {title}"}})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	want := filepath.Join(root, "周杰伦", "双截棍.flac")
	if got := plan.Items[0].Target; got != want {
		t.Errorf("目标路径不对\n 期望 %s\n 实际 %s", want, got)
	}
}

// 平铺样式（目录模板为空）也是合法取值 —— 它就是「把散在子目录里的歌收成一层」。
func TestFlatTemplate(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "旧目录", "周杰伦 - 双截棍.flac"), "fake-flac")

	plan, err := PlanOf(Options{Root: root, Template: Template{Dir: "", File: "{artist} - {title}"}})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	want := filepath.Join(root, "周杰伦 - 双截棍.flac")
	if got := plan.Items[0].Target; got != want {
		t.Errorf("目标路径不对\n 期望 %s\n 实际 %s", want, got)
	}
}

// 内置样式是给界面下拉框用的，id 必须唯一、名字必须能看懂。
func TestPresets(t *testing.T) {
	presets := Presets()
	if len(presets) < 5 {
		t.Fatalf("内置样式至少 5 个，得到 %d", len(presets))
	}
	seen := map[string]bool{}
	for _, p := range presets {
		if p.ID == "" || p.Name == "" {
			t.Errorf("样式缺少 id 或名称：%+v", p)
		}
		if seen[p.ID] {
			t.Errorf("样式 id 重复：%s", p.ID)
		}
		seen[p.ID] = true
		if strings.TrimSpace(p.File) == "" {
			t.Errorf("样式 %s 的文件名模板为空", p.ID)
		}
	}

	// 空文件名模板要回落到默认，而不是留空把歌名弄丢；
	// 空目录模板则**是合法取值**（平铺），不能当成「没写」。
	if got := NormalizeTemplate(Template{Dir: "", File: ""}); got.Dir != "" || got.File != DefaultTemplate().File {
		t.Errorf("空文件名模板应回落默认、空目录模板应保留为空，得到 %+v", got)
	}
	if got := NormalizeTemplate(Template{Dir: "  {artist}  ", File: " {title} "}); got.Dir != "{artist}" || got.File != "{title}" {
		t.Errorf("模板两端的空格应当被去掉，得到 %+v", got)
	}
}

// 文件名里本来就带序号、又没有标签可读时（wav / ape 这类），序号要被摘出来
// 当曲序用，不能既当曲序又留在歌名里 —— 否则会渲染成 `05 05 探针 支持吗.flac`。
func TestFilenameTrackNumberNotDuplicated(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "05 探针 支持吗.wav"), "fake-wav")

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate()})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	if plan.Stats.Move != 1 {
		t.Fatalf("应当有 1 个待搬文件，统计：%+v", plan.Stats)
	}
	want := filepath.Join(root, UnknownArtist, UnknownAlbum, "05 探针 支持吗.wav")
	if got := plan.Items[0].Target; got != want {
		t.Fatalf("目标路径不对\n 期望 %s\n 实际 %s", want, got)
	}
	if tr := plan.Items[0].Meta.Track; tr != 5 {
		t.Errorf("曲序应当是 5，得到 %d", tr)
	}
}

// 歌名里的数字不能当序号剥：「2001年的第一场雪」后面没有分隔符。
func TestFilenameYearIsNotTrackNumber(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "2001年的第一场雪.flac"), "fake-flac")

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate()})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	if tr := plan.Items[0].Meta.Track; tr != 0 {
		t.Errorf("不该把年份当曲序，得到 %d", tr)
	}
	if title := plan.Items[0].Meta.Title; title != "2001年的第一场雪" {
		t.Errorf("歌名不该被截，得到 %q", title)
	}
}

func TestSplitLeadingTrack(t *testing.T) {
	cases := []struct {
		in   string
		num  int
		rest string
	}{
		{"01 双截棍", 1, "双截棍"},
		{"05 探针 支持吗", 5, "探针 支持吗"},
		{"07-稻香", 7, "稻香"},
		{"08.晴天", 8, "晴天"},
		{"09、夜曲", 9, "夜曲"},
		{"04", 0, "04"},
		{"2001年的第一场雪", 0, "2001年的第一场雪"},
		{"双截棍", 0, "双截棍"},
		{"03 ", 0, "03 "},
	}
	for _, c := range cases {
		num, rest := splitLeadingTrack(c.in)
		if num != c.num || rest != c.rest {
			t.Errorf("splitLeadingTrack(%q) = (%d, %q)，期望 (%d, %q)", c.in, num, rest, c.num, c.rest)
		}
	}
}

// 撤销之后整理时新建的「歌手/专辑」目录不该留着空壳。
func TestUndoCleansEmptyDirs(t *testing.T) {
	root := t.TempDir()
	const base = "周杰伦 - 双截棍"
	audio := filepath.Join(root, base+".flac")
	writeTestFile(t, audio, "fake-flac")

	lookup := func(p string) (Meta, bool) {
		return Meta{Title: "双截棍", Artist: "周杰伦", Album: "范特西", Track: 1}, p == audio
	}
	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate(), Lookup: lookup})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	res, err := Apply(plan)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	albumDir := filepath.Join(root, "周杰伦", "范特西")
	if !pathExists(albumDir) {
		t.Fatalf("整理后专辑目录应当存在：%s", albumDir)
	}

	undoRes, err := Undo(res.Journal, root)
	if err != nil {
		t.Fatalf("撤销失败：%v", err)
	}
	if undoRes.Moved != 1 {
		t.Errorf("应当撤回 1 首，得到 %d", undoRes.Moved)
	}
	if !pathExists(audio) {
		t.Error("文件应当回到原位")
	}
	if pathExists(filepath.Join(root, "周杰伦")) {
		t.Error("撤销后歌手目录应当被清掉，不该留下空壳")
	}
}

// 整理过一遍之后再预演，必须全部落在「已就位」上。
//
// 没有标签可读的文件（wav / ape 之类）整理后文件名里只剩歌名，如果歌手只能从
// 文件名里拆，那第二遍就会把它们又判成「未知歌手」重新搬一次 —— 每点一次
// 预演都像还有活干，用户根本判断不了到底整理完了没有。
func TestPlanIsIdempotentAfterApply(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "周杰伦 - 双截棍.flac"), "fake-flac")

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate()})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	if plan.Stats.Move != 1 {
		t.Fatalf("第一次预演应当有 1 首要搬，统计：%+v", plan.Stats)
	}
	if _, err := Apply(plan); err != nil {
		t.Fatalf("执行失败：%v", err)
	}

	again, err := PlanOf(Options{Root: root, Template: DefaultTemplate()})
	if err != nil {
		t.Fatalf("第二次预演失败：%v", err)
	}
	if again.Stats.Move != 0 {
		var lines []string
		for _, it := range again.Items {
			lines = append(lines, it.Source+" -> "+it.Target)
		}
		t.Fatalf("第二遍不该再有要搬的，实际 move=%d：%s", again.Stats.Move, strings.Join(lines, "; "))
	}
	if again.Stats.Same != 1 {
		t.Errorf("第二遍应当认成已就位，统计：%+v", again.Stats)
	}
}

// 只有一层目录（用户自己的分类习惯）不能被当成歌手名。
func TestSingleLevelDirIsNotArtist(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "散放")
	writeTestFile(t, filepath.Join(dir, "双截棍.flac"), "fake-flac")

	plan, err := PlanOf(Options{Root: root, Template: DefaultTemplate()})
	if err != nil {
		t.Fatalf("出方案失败：%v", err)
	}
	if got := plan.Items[0].Meta.Artist; got != UnknownArtist {
		t.Errorf("单层目录名不该被当成歌手，得到 %q", got)
	}
}
