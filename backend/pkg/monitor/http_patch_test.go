package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// patchMonitor 打一次 PATCH /api/monitors/{id}，返回更新后的监控。
func patchMonitor(t *testing.T, h *Handler, id, body string) *Monitor {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/monitors/"+id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleMonitorDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH %s 失败：%d %s", body, rec.Code, rec.Body.String())
	}
	var resp struct {
		Data Monitor `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	return &resp.Data
}

// 局部更新绝不能「顺手」改掉没传的字段。
//
// 这条固化一个真实事故。原实现写的是：
//
//	if patch.AutoDownload != m.AutoDownload { m.AutoDownload = patch.AutoDownload }
//
// 看着像「值变了才改」，实际是**没传也会改** —— 请求里没有 auto_download
// （零值 false）≠ 当前值 true → 被覆盖成 false。
//
// 后果极隐蔽：前端「停止更新」只发 `{enabled:false}`，就把「自动下载」一起关掉了；
// 「恢复更新」同样只发 `{enabled:true}`，**关掉的那个回不来**。
// 界面上一切正常（仍显示「已订阅」），只是这个订阅再也不会自动下载了 ——
// 正好破坏「不想要了点停止、想恢复再点一次订阅就行」这条需求。
//
// 修法：解一遍请求体里**出现过哪些键**，只有显式出现才写回。
func TestPatchMonitorKeepsUntouchedBoolFields(t *testing.T) {
	store := NewStore(t.TempDir())
	mon := &Monitor{
		ID: "m1", Name: "原创榜（订阅）", Kind: KindPlaylist,
		AutoDownload: true, Embed: true, Enabled: true,
		Quality: QualityLossless, IntervalMinutes: 360, MaxDownloads: 30,
	}
	if _, err := store.CreateMonitor(mon); err != nil {
		t.Fatalf("创建监控失败：%v", err)
	}
	h := NewHandler(store, nil)

	// ① 停止更新：只发 enabled
	got := patchMonitor(t, h, "m1", `{"enabled":false}`)
	if !got.AutoDownload || !got.Embed {
		t.Errorf("只发 {enabled:false} 不该动 auto_download/embed，得到 auto_download=%v embed=%v",
			got.AutoDownload, got.Embed)
	}
	if got.Enabled {
		t.Errorf("enabled 应被改成 false，得到 %v", got.Enabled)
	}

	// ② 恢复更新：只发 enabled —— ① 里保住的东西必须还在
	got = patchMonitor(t, h, "m1", `{"enabled":true}`)
	if !got.AutoDownload || !got.Embed {
		t.Errorf("恢复后 auto_download/embed 必须保留，得到 auto_download=%v embed=%v",
			got.AutoDownload, got.Embed)
	}
	if !got.Enabled {
		t.Error("enabled 应被改回 true")
	}

	// ③ 显式传了就要生效 —— 别把「只有显式才改」写成「永远不改」
	got = patchMonitor(t, h, "m1", `{"auto_download":false,"embed":false}`)
	if got.AutoDownload || got.Embed {
		t.Errorf("显式传 false 应生效，得到 auto_download=%v embed=%v", got.AutoDownload, got.Embed)
	}

	// ④ 不带 enabled 的局部更新不该把监控停掉（同一类错误）
	got = patchMonitor(t, h, "m1", `{"name":"改个名"}`)
	if !got.Enabled {
		t.Error("不带 enabled 的局部更新不该把监控停掉")
	}
	if got.Name != "改个名" {
		t.Errorf("name 应被更新，得到 %q", got.Name)
	}

	// ⑤ 落盘后再读一次，确认不是只在响应里对
	reloaded, ok := store.GetMonitor("m1")
	if !ok {
		t.Fatal("监控应仍在")
	}
	if reloaded.Name != "改个名" || !reloaded.Enabled {
		t.Errorf("持久化后应保持改后的状态，得到 name=%q enabled=%v", reloaded.Name, reloaded.Enabled)
	}
}
