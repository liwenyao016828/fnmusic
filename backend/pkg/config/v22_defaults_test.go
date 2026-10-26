package config

import "testing"

// v2.22 新键在「老配置文件」上的行为：缺键时读出来的必须是安全默认值，
// 且不能因为「保存了别的设置」而把它们悄悄改掉。
func TestV22KeysDefaultsFromOldFile(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatal(err)
	}
	c := cm.Get()
	// ⚠️ nil 本身是「从没配过」的合法表示（见 MusicDLEffectiveSources 的注释），
	// 前端要拿的是**生效值**，所以这里断言 accessor 而不是字段。
	if got := c.MusicDLActiveSources(); len(got) == 0 {
		t.Fatal("没配过时生效源该是默认的一批（前端据此画勾选态）")
	}
	if c.AutoDownloadQuality == "" {
		t.Fatal("自动下载档位该有默认值")
	}
	if got := c.DownloadChannels(); len(got) == 0 {
		t.Fatal("渠道该有默认值")
	}
	// 全新配置：收藏自动下载默认关 → 补强也跟着关（升级前就是这关系，不能变）。
	if c.FavAutoDownloadOn() {
		t.Fatal("前提变了：收藏自动下载的默认值被改成开了")
	}
	if c.UpgradeSweepOn() {
		t.Fatal("收藏自动下载没开时，补强该跟着关（与升级前一致）")
	}
	best, sweepOn, favOn := c.AutoDownloadQuality, c.UpgradeSweepOn(), c.FavAutoDownloadOn()
	// 只改主题：其它一律不动
	if err := cm.Update(AppConfig{Theme: "light"}); err != nil {
		t.Fatal(err)
	}
	c = cm.Get()
	if c.AutoDownloadQuality != best || c.UpgradeSweepOn() != sweepOn || c.FavAutoDownloadOn() != favOn || len(c.DownloadChannels()) == 0 {
		t.Fatal("保存无关设置不该动 v2.22 的这些键")
	}
}
