package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func eqChannels(a, b []string) bool { return reflect.DeepEqual(a, b) }

// NormalizeDownloadChannels 是配置边界的唯一清洗点：顺序要留住、认不出的要丢、
// 全空要回落默认。三条各测一次，别指望调用方兜。
func TestNormalizeDownloadChannels(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"空列表回落默认", nil, DefaultDownloadChannels},
		{"全是垃圾值也回落默认", []string{"", "  ", "kugou"}, DefaultDownloadChannels},
		{"认不出的值丢掉、认得出的保序", []string{"DL", " native ", "bogus", "lx"}, []string{"dl", "native", "lx"}},
		{"重复取第一次出现", []string{"lx", "dl", "lx"}, []string{"lx", "dl"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeDownloadChannels(c.in); !eqChannels(got, c.want) {
				t.Fatalf("NormalizeDownloadChannels(%q) = %q，想要 %q", c.in, got, c.want)
			}
		})
	}
}

func TestChannelRankAndEnabled(t *testing.T) {
	ch := []string{"dl", "lx"}
	if ChannelRank(ch, "dl") != 0 || ChannelRank(ch, "lx") != 1 {
		t.Fatalf("位次没按顺序给：%v", ch)
	}
	if got := ChannelRank(ch, "native"); got != -1 {
		t.Fatalf("没勾选的渠道该是 -1，得到 %d", got)
	}
	if ChannelEnabled(ch, "native") {
		t.Fatal("native 不在列表里，ChannelEnabled 不该说在")
	}
	if !ChannelEnabled(ch, "lx") {
		t.Fatal("lx 在列表里，ChannelEnabled 该说在")
	}
}

// 默认顺序必须洛雪最前 —— 这是 v2.17 的直接动机（免登录网易云只会静默给 mp3）。
// 有人改默认值时这条要响：默认顺序本身就是修复的一部分。
func TestDefaultDownloadChannelsPutsLxFirst(t *testing.T) {
	if got := (AppConfig{}).DownloadChannels(); !eqChannels(got, []string{"lx", "dl", "native"}) {
		t.Fatalf("默认渠道顺序 = %q", got)
	}
}

// AppConfig.DownloadChannels 走同一个清洗器，配置里写脏值不会漏到运行时。
func TestAppConfigDownloadChannelsCleansInput(t *testing.T) {
	a := AppConfig{AutoDownloadChannels: []string{"native", "NATIVE", " nope ", "lx"}}
	if got := a.DownloadChannels(); !eqChannels(got, []string{"native", "lx"}) {
		t.Fatalf("清洗后 = %q，想要 [native lx]", got)
	}
}

// Update 的合并逻辑在 config.go 里有**两份**（整档覆盖 + 增量 PATCH），
// 漏掉任何一份都会让界面保存一次就静默失效。这里覆盖增量 PATCH 那一份，
// 并钉住「落盘 → 重新读回来」这条真相链。
func TestUpdateStoresChannels(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatalf("NewConfigManager: %v", err)
	}
	if err := cm.Update(AppConfig{AutoDownloadChannels: []string{"native", "dl"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := cm.Get().DownloadChannels(); !eqChannels(got, []string{"native", "dl"}) {
		t.Fatalf("Update 后 = %q，想要 [native dl]", got)
	}

	// 落盘字段名是对外契约（前端 client.js 按它读写），改名要同步前端，所以直接断言 JSON。
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var dump map[string]any
	if err := json.Unmarshal(raw, &dump); err != nil {
		t.Fatalf("落盘的 JSON 解析不了: %v", err)
	}
	stored, _ := dump["auto_download_channels"].([]any)
	if len(stored) != 2 || stored[0] != "native" || stored[1] != "dl" {
		t.Fatalf("落盘字段 auto_download_channels = %v，想要 [native dl]", dump["auto_download_channels"])
	}

	cm2, err := NewConfigManager(dir, 0)
	if err != nil {
		t.Fatalf("重新 NewConfigManager: %v", err)
	}
	if got := cm2.Get().DownloadChannels(); !eqChannels(got, []string{"native", "dl"}) {
		t.Fatalf("读回来 = %q，想要 [native dl]", got)
	}
}
