package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 应用内更新代理（用户 2026-10-25）。
//
// 背景：NAS 上「下载安装包」失败于 `dial tcp 20.205.243.166:443: i/o timeout`
// —— release 资产在 objects.githubusercontent.com，那条网络直连出不去；而应用由
// 飞牛拉起，读不到 shell 的 HTTP_PROXY。所以代理要能**存进配置**。
//
// 这组用例专钉本项目踩过好几次的那个坑：AppConfig 的读写是**两处独立的选择性合并**
// （load 与 Update），新字段只登记一处就会出现「改完能用、重启变回默认」或
// 「写进去了读不回来」。之前的 APISources、PreferredPlatforms 都栽在这上面。

func TestNormalizeUpdateProxy(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://192.168.1.3:7890", "http://192.168.1.3:7890"},
		{"  socks5://192.168.1.3:7891  ", "socks5://192.168.1.3:7891"},
		// 界面上最容易漏的就是 scheme：只填 host:port 也要能用。
		{"192.168.1.3:7890", "http://192.168.1.3:7890"},
		// 不支持的协议 / 空值 / 不是地址 → 当没配（返回空串），不报错。
		{"ftp://192.168.1.3:21", ""},
		{"", ""},
		{"   ", ""},
		{"http://", ""},
	}
	for _, c := range cases {
		if got := NormalizeUpdateProxy(c.in); got != c.want {
			t.Errorf("NormalizeUpdateProxy(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestUpdateProxyRoundTripThroughUpdateAndLoad(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatal(err)
	}
	if got := cm.Get().UpdateProxyURL(); got != "" {
		t.Fatalf("默认不该有代理，得到 %q", got)
	}

	// Update 侧：存进去要归一（补 scheme）。
	if err := cm.Update(AppConfig{UpdateProxy: "192.168.1.3:7890"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if got := cm.Get().UpdateProxyURL(); got != "http://192.168.1.3:7890" {
		t.Fatalf("Update 后代理 = %q，期望归一成带 scheme 的地址", got)
	}

	// 空串 = 不动（omitempty 分不出「没传」），不能把已有值抹掉。
	if err := cm.Update(AppConfig{Theme: "dark"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if got := cm.Get().UpdateProxyURL(); got != "http://192.168.1.3:7890" {
		t.Fatalf("不相关的保存不该动代理，得到 %q", got)
	}

	// 清空走显式标记（UpdateClearProxy）。
	if err := cm.Update(AppConfig{UpdateClearProxy: true}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if got := cm.Get().UpdateProxyURL(); got != "" {
		t.Fatalf("显式清空后应回到空，得到 %q", got)
	}

	// load 侧：重新从一个进程读同一份文件 —— 这条就是「重启后自己变回默认」的回归。
	if err := cm.Update(AppConfig{UpdateProxy: "socks5://192.168.1.3:7891"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("配置文件没落盘: %v", err)
	}
	cm2, err := NewConfigManager(dir, 8899)
	if err != nil {
		t.Fatal(err)
	}
	if got := cm2.Get().UpdateProxyURL(); got != "socks5://192.168.1.3:7891" {
		t.Fatalf("重启（重新 load）后代理 = %q，期望 socks5://192.168.1.3:7891"+
			"（load 漏登记就是「重启变回默认」）", got)
	}
}

// 代理不是密钥，但也不该被当成 token 那样抹掉：界面要能回显、能编辑。
func TestUpdateProxyNotRedactedByConfig(t *testing.T) {
	cm, err := NewConfigManager(t.TempDir(), 8899)
	if err != nil {
		t.Fatal(err)
	}
	if err := cm.Update(AppConfig{UpdateProxy: "http://192.168.1.3:7890"}); err != nil {
		t.Fatal(err)
	}
	got := cm.Get().UpdateProxy
	if !strings.Contains(got, "192.168.1.3:7890") {
		t.Fatalf("代理值应当可读回用于界面回显，得到 %q", got)
	}
}
