package protocol

import (
	"strings"
	"testing"
)

func TestListHasOfficialAndLx(t *testing.T) {
	list := List()
	if len(list) != 2 {
		t.Fatalf("期望 2 个协议，实际 %d 个", len(list))
	}
	if list[0].ID != IDOfficial {
		t.Errorf("第一个协议应为 official，实际 %q", list[0].ID)
	}
	if list[1].ID != IDLx {
		t.Errorf("第二个协议应为 lx，实际 %q", list[1].ID)
	}
}

// 官方协议必须是「内置 + 免脚本」，否则前端解耦就无从谈起。
func TestOfficialProtocolIsBuiltinAndNeedsNoSource(t *testing.T) {
	p, ok := Get(IDOfficial)
	if !ok {
		t.Fatal("找不到 official 协议")
	}
	if !p.Builtin {
		t.Error("official 应为内置协议")
	}
	if p.RequiresSource {
		t.Error("official 不应要求用户先导入音源")
	}
	if p.Location != LocBackend {
		t.Errorf("official 的执行位置应为 backend，实际 %q", p.Location)
	}
}

// 官方协议不得宣称能取直链 —— 这是 lx 协议的职责，混淆会误导前端与外部 AI。
func TestOfficialProtocolDoesNotClaimMusicURL(t *testing.T) {
	if HasCapability(IDOfficial, CapMusicURL) {
		t.Error("official 不应提供 music_url 能力")
	}
}

// lx 协议必须留在浏览器侧 —— 后端不执行第三方 JS，这是合规硬约束。
func TestLxProtocolStaysInBrowser(t *testing.T) {
	p, ok := Get(IDLx)
	if !ok {
		t.Fatal("找不到 lx 协议")
	}
	if p.Location != LocBrowser {
		t.Errorf("lx 的执行位置必须是 browser，实际 %q（后端不得执行第三方 JS）", p.Location)
	}
	if !p.RequiresSource {
		t.Error("lx 应要求用户先导入音源")
	}
	if !HasCapability(IDLx, CapMusicURL) {
		t.Error("lx 必须提供 music_url 能力（取直链）")
	}
}

// 搜索/榜单/歌单/歌词这四项是官方协议的解耦依据，缺一不可。
func TestOfficialCoversDecouplingCapabilities(t *testing.T) {
	for _, c := range []Capability{CapSearch, CapCharts, CapPlaylist, CapLyric} {
		if !HasCapability(IDOfficial, c) {
			t.Errorf("official 缺少能力 %q —— 前端解耦依赖它", c)
		}
	}
}

func TestOfficialPlatformsMatchImplementation(t *testing.T) {
	p, _ := Get(IDOfficial)
	want := []string{"wy", "tx", "kg", "kw"}
	if len(p.Platforms) != len(want) {
		t.Fatalf("official 平台数应为 %d，实际 %d", len(want), len(p.Platforms))
	}
	for i, id := range want {
		if p.Platforms[i] != id {
			t.Errorf("第 %d 个平台应为 %q，实际 %q", i, id, p.Platforms[i])
		}
	}
}

// Get 返回的切片必须与内部状态隔离，避免调用方改坏全局描述。
func TestListReturnsCopy(t *testing.T) {
	a := List()
	a[0].Platforms[0] = "MUTATED"
	b := List()
	if b[0].Platforms[0] == "MUTATED" {
		t.Error("List 返回的平台切片被外部修改后污染了后续调用")
	}
}

func TestGetUnknown(t *testing.T) {
	if _, ok := Get("nope"); ok {
		t.Error("未知协议 ID 应返回 false")
	}
	if CapabilitiesOf("nope") != nil {
		t.Error("未知协议的能力集合应为 nil")
	}
	if HasCapability("nope", CapSearch) {
		t.Error("未知协议不应有任何能力")
	}
}

// 协议描述里不应出现空字段，否则前端与 AI 会拿到无意义的数据。
func TestProtocolsAreFullyDescribed(t *testing.T) {
	for _, p := range List() {
		if strings.TrimSpace(p.Name) == "" {
			t.Errorf("%s: Name 为空", p.ID)
		}
		if strings.TrimSpace(p.Description) == "" {
			t.Errorf("%s: Description 为空", p.ID)
		}
		if len(p.Platforms) == 0 {
			t.Errorf("%s: Platforms 为空", p.ID)
		}
		if len(p.Capabilities) == 0 {
			t.Errorf("%s: Capabilities 为空", p.ID)
		}
	}
}
