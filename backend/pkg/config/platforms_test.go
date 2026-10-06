package config

import (
	"reflect"
	"testing"
)

func TestPlatformPriorityNormalization(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"没配置走默认", nil, []string{"wy", "tx"}},
		// ⚠️ 反例必须是**真的不认识**的平台。这里原来写的是 `mg` —— 那时它确实
		// 没被支持；v2.1.83 起咪咕由 musicdl sidecar 提供搜索与解析，它已经在
		// 白名单里了（见 knownPlatforms）。用已经支持的平台当反例，这条断言
		// 会变成「测了个寂寞」。
		{"全是要素外的值也回默认", []string{"nope", "qq"}, []string{"wy", "tx"}},
		{"去重", []string{"tx", "tx", "wy"}, []string{"tx", "wy"}},
		{"去空白与大小写", []string{" TX ", "", "kw"}, []string{"tx", "kw"}},
		{"保留用户给的顺序", []string{"kw", "tx"}, []string{"kw", "tx"}},
	}
	for _, c := range cases {
		got := AppConfig{PreferredPlatforms: c.in}.PlatformPriority()
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: PlatformPriority() = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// 主力在前、其余殿后，但**一个都不丢** ——
// 用户要的是「QQ/网易都没有时才用 kg/kw」，不是「永远不用 kg/kw」。
func TestOrderedPlatformsKeepsEveryone(t *testing.T) {
	got := OrderedPlatforms([]string{"tx", "wy"}, []string{"wy", "tx", "kg", "kw"})
	want := []string{"tx", "wy", "kg", "kw"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OrderedPlatforms = %v, 期望 %v", got, want)
	}

	// 没配主力时顺序原样保留，不会凭空重排
	same := OrderedPlatforms(nil, []string{"wy", "tx", "kg", "kw"})
	if !reflect.DeepEqual(same, []string{"wy", "tx", "kg", "kw"}) {
		t.Errorf("无主力时不该改动顺序，实际 %v", same)
	}
}

// 返回的默认切片必须是副本：调用方排序时 append 会把全局默认改掉。
func TestPlatformPriorityReturnsCopy(t *testing.T) {
	got := AppConfig{}.PlatformPriority()
	got = append(got, "zzz")
	_ = got
	again := AppConfig{}.PlatformPriority()
	if len(again) != 2 {
		t.Errorf("默认值被调用方改坏了，现在是 %v", again)
	}
}
