package config

import "testing"

// 四个开关都要**活过重启**：设值 → 保存 → 重新加载 → 读回一致。
//
// 这一类的 bug 已经出过一次：`musicdl_sources` 带 `omitempty`，用户在音源页把源
// 全关掉，序列化时被省成缺字段，重启后读回来是 nil → 又变回默认源。用户看到的是
// 「我设的东西自己跑回去了」—— 而设置页显示的还会是他关掉之前的状态，两头对不上。
//
// 四个开关各有自己的缺省语义（`tee_enabled`/`fav_auto_download` 缺省**关**，
// `lyric_auto_download`/`cover_embed` 缺省**开**），任何一种在往返中丢失都会出这种事。
func TestSwitchesSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	c := cm.Get()
	c.TeeEnabled = &on         // 缺省关 → 显式开
	c.FavAutoDownload = &on    // 缺省关 → 显式开
	c.LyricAutoDownload = &off // 缺省**开** → 显式关（最容易丢的那个方向）
	c.LXServerEnabled = &on    // 缺省关 → 显式开
	c.LXServerPort = 18925
	c.CoverEmbed = &off // 缺省**开** → 显式关
	if err := cm.Update(c); err != nil {
		t.Fatal(err)
	}

	// 「重启」：新建一个 manager 读同一份配置。
	cm2, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := cm2.Get()
	if !g.TeeOn() {
		t.Fatal("tee_enabled 该活过重启（显式开的那个方向）")
	}
	if !g.FavAutoDownloadOn() {
		t.Fatal("fav_auto_download 该活过重启（显式开的那个方向）")
	}
	if g.LyricAutoDownloadOn() {
		t.Fatal("⚠️ lyric_auto_download 被显式关掉了，重启后又变回「开」")
	}
	if g.CoverEmbedOn() {
		t.Fatal("⚠️ cover_embed 被显式关掉了，重启后又变回「开」")
	}

	// 反过来：显式打开那两个缺省**开**的，以及显式关掉那两个缺省**关**的。
	cm3, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	c3 := cm3.Get()
	c3.TeeEnabled = &off
	c3.FavAutoDownload = &off
	c3.LyricAutoDownload = &on
	c3.LXServerEnabled = &off
	c3.LXServerPort = 0
	c3.CoverEmbed = &on
	if err := cm3.Update(c3); err != nil {
		t.Fatal(err)
	}
	cm4, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	g4 := cm4.Get()
	if g4.TeeOn() || g4.FavAutoDownloadOn() {
		t.Fatal("显式关掉的那两个该活过重启")
	}
	if !g4.LyricAutoDownloadOn() || !g4.CoverEmbedOn() {
		t.Fatal("显式打开的那两个该活过重启")
	}
}

// 「从没配过」与「显式关掉」是两种意图，四个开关都必须区分。
//
// 缺省**开**的两个：nil → 开；显式 false → 关。
// 缺省**关**的两个：nil → 关；显式 true → 开。
func TestSwitchDefaultsDistinguishUnsetFromExplicit(t *testing.T) {
	var fresh AppConfig // 老配置 / 新装：四个字段全是 nil
	if fresh.TeeOn() || fresh.FavAutoDownloadOn() {
		t.Fatal("缺省关的两个：nil 该是关")
	}
	if !fresh.LyricAutoDownloadOn() || !fresh.CoverEmbedOn() {
		t.Fatal("缺省开的两个：nil 该是开（老配置里没这两个键，nil 当关会让老用户静默掉档次）")
	}
	on, off := true, false
	explicit := AppConfig{TeeEnabled: &on, FavAutoDownload: &on, LyricAutoDownload: &off, CoverEmbed: &off}
	if !explicit.TeeOn() || !explicit.FavAutoDownloadOn() {
		t.Fatal("显式开的两个该是开")
	}
	if explicit.LyricAutoDownloadOn() || explicit.CoverEmbedOn() {
		t.Fatal("显式关的两个该是关")
	}
}

// 服务端洛雪宿主的两个键：**独立**验，不塞进别的用例里。
//
// 为什么单独写：上一版我图省事往已有用例里插断言，锚在 `if …{` 这种条件行之后，
// 断言被塞进花括号内部 —— 条件为假时永远不执行，测试照样绿。
// **变异校验把它抓出来了**（把缺省改成「开」，用例竟然还是过）。假绿比没有测试更糟。
func TestLXServerKeysAreOffByDefaultAndSurviveRestart(t *testing.T) {
	// ① 缺省必须是关：打开它 = 第三方音源脚本跑在服务端，
	//    破掉「后端不执行第三方 JS」这条边界，必须用户显式打开。
	var fresh AppConfig
	if fresh.LXServerOn() {
		t.Fatal("⚠️ lx_server_enabled 缺省必须是关")
	}

	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	c := cm.Get()
	c.LXServerEnabled = &on
	c.LXServerPort = 18925
	if err := cm.Update(c); err != nil {
		t.Fatal(err)
	}

	// ② 显式打开 + 端口都要活过重启。
	cm2, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := cm2.Get()
	if !g.LXServerOn() {
		t.Fatal("lx_server_enabled 该活过重启（显式开的那个方向）")
	}
	if g.LXServerPort != 18925 {
		t.Fatalf("lx_server_port 该活过重启，得到 %d", g.LXServerPort)
	}

	// ③ 显式关掉也要活过重启。
	off := false
	c3 := cm2.Get()
	c3.LXServerEnabled = &off
	c3.LXServerPort = 0
	if err := cm2.Update(c3); err != nil {
		t.Fatal(err)
	}
	cm3, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	g3 := cm3.Get()
	if g3.LXServerOn() {
		t.Fatal("显式关掉的 lx_server_enabled 该活过重启")
	}
	if g3.LXServerPort != 18925 {
		t.Fatal("端口传 0 该表示「不改动」，应保持 18925")
	}
}
