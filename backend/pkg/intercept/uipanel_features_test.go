package intercept

import (
	"net/http"
	"testing"
)

// /api/info 的 panel_features：面板功能开关的「最后一公里」。
//
// 这组测试钉四件事：
//  1. Config 没接线（老调用方 / 最小 harness）→ 四键全开，面板行为与从前一样；
//  2. hook 是**现取**的 —— 两次请求之间翻转闭包里的开关，响应必须跟着变
//     （缓存一份旧 map，表现就是「改完配置面板不动」）；
//  3. hook 返回的 map 缺键 → 缺的那个按开算（配置层加键、调用方没升级时
//     宁可多显示，不能让功能凭空消失）；
//  4. hook 返回 nil → 全开（接线了但这次没值，与「没接线」同口径）。
//
// 显隐的**执行**在注入脚本（见 assets/ui.js 的 applyFeatures / featOnWith）；
// 这里只保证后端把状态如实、新鲜地交出去。

func panelFeaturesOf(t *testing.T, h *harness) map[string]any {
	t.Helper()
	w, handled := h.do(http.MethodGet, qulvAPIPrefix+"/info", "", nil)
	if !handled {
		t.Fatal("/info 应被拦截层处理")
	}
	d := dataOf(t, w)
	pf, ok := d["panel_features"].(map[string]any)
	if !ok {
		t.Fatalf("panel_features 应为对象，得到 %v", d["panel_features"])
	}
	return pf
}

func TestQulvInfoPanelFeaturesDefaultAllOn(t *testing.T) {
	h := newHarness(t) // newHarness 不接 UIPanelFeatures —— 就是「老调用方」
	pf := panelFeaturesOf(t, h)
	if len(pf) != 4 {
		t.Fatalf("应恰好 4 个键，得到 %d：%v", len(pf), pf)
	}
	for _, k := range []string{"quality", "assistant", "jump", "download"} {
		if pf[k] != true {
			t.Errorf("未接线时 %s 应默认开，得到 %v", k, pf[k])
		}
	}
}

// 动态 function hook：同一份 live map 被两次请求之间改写，第二次响应必须
// 反映新值。这条同时盖住了 main.go 的接线形态
// （UIPanelFeatures: func() map[string]bool { return cfgMgr.Get().UIPanelFeatures() }）
// —— 只要拦截层不缓存，配置一改，下一次打开面板就是新的。
func TestQulvInfoPanelFeaturesLiveGetter(t *testing.T) {
	h := newHarness(t)
	live := map[string]bool{"quality": true, "assistant": true, "jump": true, "download": true}
	h.it.panelFeats = func() map[string]bool { return live }

	pf := panelFeaturesOf(t, h)
	for k := range live {
		if pf[k] != true {
			t.Fatalf("翻转前 %s 应为开，得到 %v", k, pf[k])
		}
	}

	live["quality"] = false
	live["download"] = false

	pf = panelFeaturesOf(t, h)
	if pf["quality"] != false || pf["download"] != false {
		t.Errorf("翻转后 quality/download 应为关，得到 %v", pf)
	}
	if pf["assistant"] != true || pf["jump"] != true {
		t.Errorf("没动过的两个键应仍为开，得到 %v", pf)
	}
}

func TestQulvInfoPanelFeaturesPartialMapDefaultsOn(t *testing.T) {
	h := newHarness(t)
	h.it.panelFeats = func() map[string]bool {
		return map[string]bool{"quality": false, "jump": false} // 故意缺 assistant/download
	}
	pf := panelFeaturesOf(t, h)
	if pf["quality"] != false || pf["jump"] != false {
		t.Errorf("显式 false 的键应如实为关，得到 %v", pf)
	}
	if pf["assistant"] != true || pf["download"] != true {
		t.Errorf("缺键应按开算（宁可多显示，不能凭空消失），得到 %v", pf)
	}
}

func TestQulvInfoPanelFeaturesNilMapDefaultsOn(t *testing.T) {
	h := newHarness(t)
	h.it.panelFeats = func() map[string]bool { return nil }
	pf := panelFeaturesOf(t, h)
	for _, k := range []string{"quality", "assistant", "jump", "download"} {
		if pf[k] != true {
			t.Errorf("hook 给 nil 时 %s 应按开算，得到 %v", k, pf[k])
		}
	}
}
