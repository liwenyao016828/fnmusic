package sources

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fn-lx-player/pkg/config"
)

const sampleScript = `/*!
 * @name 我的音源
 * @description 仅供个人学习研究
 * @version 2.1.0
 * @author 某开发者
 */
const x = 1
`

// scriptsBody 用给定内容构造 import_batch 的请求体。
// 用 json.Marshal 而不是手写字符串 —— 脚本内容里有换行，手写容易转义出错。
func scriptsBody(items map[string]string) string {
	scripts := make([]map[string]string, 0, len(items))
	for name, content := range items {
		scripts = append(scripts, map[string]string{"filename": name, "content": content})
	}
	raw, _ := json.Marshal(map[string]any{"scripts": scripts})
	return string(raw)
}

// fakeLXScript 造一份「形状正确」的假 LX 音源脚本。
//
// 导入会做形状校验（见 validate.go），测试用的脚本必须像真的 JS 且长度过下限，
// 否则会被挡在门外。
//
// ⚠️ 注意：校验**不再要求出现 `lx.on(` 这类明文标记** ——
// 那会误杀混淆过的真脚本。所以这里保留 lx.on 只是为了让样本更贴近真实输入，
// 不是因为校验需要它（「是不是合格音源」由运行时执行结果判定）。
func fakeLXScript(name string) string {
	pad := strings.Repeat("// 占位内容，用来模拟真实脚本的体积\n", 12)
	return "/*!\n * @name " + name + "\n * @version 1.0.0\n * @author 测试\n */\n" +
		pad +
		"lx.on('request', (eventName, payload) => {\n" +
		"  if (eventName !== 'request') return\n" +
		"  lx.send('inited', { status: true, sources: { kw: { qualitys: ['320k'] } } })\n" +
		"})\n"
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	cfgMgr, err := config.NewConfigManager(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("创建配置管理器失败: %v", err)
	}
	return NewManager(cfgMgr, nil, "")
}

func postBatch(t *testing.T, m *Manager, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sources/import_batch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	m.HandleImportBatch(rec, req)

	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestParseScriptMeta(t *testing.T) {
	meta := parseScriptMeta(sampleScript)
	if meta.Name != "我的音源" {
		t.Errorf("Name = %q", meta.Name)
	}
	if meta.Version != "2.1.0" {
		t.Errorf("Version = %q", meta.Version)
	}
	if meta.Author != "某开发者" {
		t.Errorf("Author = %q", meta.Author)
	}
	if meta.Description != "仅供个人学习研究" {
		t.Errorf("Description = %q", meta.Description)
	}

	// 没有头部注释时各字段留空，不臆造
	empty := parseScriptMeta("const a = 1")
	if empty.Name != "" || empty.Version != "" || empty.Author != "" {
		t.Errorf("无元数据时应留空，实际 %+v", empty)
	}
}

func TestScriptIDStableAndDistinct(t *testing.T) {
	a := scriptID("abc")
	b := scriptID("abc")
	c := scriptID("abd")
	if a != b {
		t.Fatal("相同内容应得到相同 ID")
	}
	if a == c {
		t.Fatal("不同内容应得到不同 ID")
	}
	if !strings.HasPrefix(a, "custom_") {
		t.Fatalf("ID 前缀应为 custom_，实际 %q", a)
	}
}

func TestImportBatchLocalScripts(t *testing.T) {
	m := newTestManager(t)
	code, out := postBatch(t, m, scriptsBody(map[string]string{"mine.js": fakeLXScript("我的音源")}))
	if code != http.StatusOK {
		t.Fatalf("HTTP %d, body=%v", code, out)
	}
	data := out["data"].(map[string]any)
	if data["imported"].(float64) != 1 {
		t.Fatalf("应导入 1 条，实际 %v", data["imported"])
	}

	// 元数据被提取进结果
	results := data["results"].([]any)
	first := results[0].(map[string]any)
	if first["name"] != "我的音源" {
		t.Errorf("name = %v", first["name"])
	}
	if first["version"] != "1.0.0" {
		t.Errorf("version = %v", first["version"])
	}

	// 确认真的落盘
	cfg := m.cfgMgr.Get()
	if len(cfg.CustomSources) != 1 {
		t.Fatalf("配置中应有 1 个音源，实际 %d", len(cfg.CustomSources))
	}
	if cfg.CustomSources[0].Name != "我的音源" {
		t.Errorf("落盘名称 = %q", cfg.CustomSources[0].Name)
	}
}

// 批量导入同一批内容不会因时间戳相同而 ID 冲突。
func TestImportBatchNoIDCollisionWithinSameSecond(t *testing.T) {
	m := newTestManager(t)
	raw, _ := json.Marshal(map[string]any{
		"scripts": []map[string]any{
			{"filename": "a.js", "content": fakeLXScript("A")},
			{"filename": "b.js", "content": fakeLXScript("B")},
			{"filename": "c.js", "content": fakeLXScript("C")},
		},
	})
	_, out := postBatch(t, m, string(raw))
	data := out["data"].(map[string]any)
	if data["imported"].(float64) != 3 {
		t.Fatalf("应导入 3 条，实际 %v", data["imported"])
	}

	cfg := m.cfgMgr.Get()
	ids := make(map[string]bool)
	for _, cs := range cfg.CustomSources {
		if ids[cs.ID] {
			t.Fatalf("ID 重复: %s", cs.ID)
		}
		ids[cs.ID] = true
	}
	if len(ids) != 3 {
		t.Fatalf("应有 3 个不同 ID，实际 %d", len(ids))
	}
}

func TestImportBatchSkipsDuplicateContent(t *testing.T) {
	m := newTestManager(t)

	// 第一次导入
	_, out := postBatch(t, m, scriptsBody(map[string]string{"x.js": fakeLXScript("重复测试")}))
	if out["data"].(map[string]any)["imported"].(float64) != 1 {
		t.Fatal("首次应导入成功")
	}

	// 同样内容再来一次（换个文件名）→ 应跳过
	_, out = postBatch(t, m, scriptsBody(map[string]string{"x-copy.js": fakeLXScript("重复测试")}))
	data := out["data"].(map[string]any)
	if data["imported"].(float64) != 0 {
		t.Fatalf("重复内容不应再次导入，imported=%v", data["imported"])
	}
	if data["skipped"].(float64) != 1 {
		t.Fatalf("应计为 skipped，实际 %v", data["skipped"])
	}
	results := data["results"].([]any)
	if results[0].(map[string]any)["status"] != "skipped" {
		t.Errorf("status = %v", results[0].(map[string]any)["status"])
	}

	// 配置里仍然只有一条
	if n := len(m.cfgMgr.Get().CustomSources); n != 1 {
		t.Fatalf("配置中应仍为 1 条，实际 %d", n)
	}
}

func TestImportBatchMixedSources(t *testing.T) {
	m := newTestManager(t)
	raw, _ := json.Marshal(map[string]any{
		"scripts": []map[string]any{
			{"filename": "ok.js", "content": fakeLXScript("正常音源")},
			{"filename": "empty.js", "content": "   "},
		},
	})
	_, out := postBatch(t, m, string(raw))
	data := out["data"].(map[string]any)

	if data["imported"].(float64) != 1 {
		t.Errorf("imported = %v", data["imported"])
	}
	if data["failed"].(float64) != 1 {
		t.Errorf("failed = %v", data["failed"])
	}
	results := data["results"].([]any)
	second := results[1].(map[string]any)
	if second["status"] != "failed" || !strings.Contains(second["reason"].(string), "为空") {
		t.Errorf("空内容应报 failed 且说明原因，实际 %v", second)
	}
}

func TestImportBatchRequiresInput(t *testing.T) {
	m := newTestManager(t)
	code, out := postBatch(t, m, `{}`)
	if code != http.StatusBadRequest {
		t.Fatalf("无输入应 400，实际 %d", code)
	}
	if !strings.Contains(out["message"].(string), "urls") {
		t.Errorf("错误信息应说明缺什么，实际 %v", out["message"])
	}
}

func TestImportBatchRejectsTooMany(t *testing.T) {
	m := newTestManager(t)
	// 用 json.Marshal 拼装，别手工转义 —— 脚本内容里有换行和引号，手写必错
	items := make([]map[string]string, 0, maxBatchItems+1)
	for i := 0; i < maxBatchItems+1; i++ {
		items = append(items, map[string]string{"filename": "f.js", "content": fakeLXScript("F")})
	}
	raw, _ := json.Marshal(map[string]any{"scripts": items})

	code, out := postBatch(t, m, string(raw))
	if code != http.StatusBadRequest {
		t.Fatalf("超限应 400，实际 %d", code)
	}
	if !strings.Contains(out["message"].(string), "最多") {
		t.Errorf("错误信息应说明上限，实际 %v", out["message"])
	}
}

func TestImportBatchRejectsOversizedScript(t *testing.T) {
	m := newTestManager(t)
	big := strings.Repeat("a", int(maxScriptBytes)+100)
	body, _ := json.Marshal(map[string]any{
		"scripts": []map[string]string{{"filename": "big.js", "content": big}},
	})

	_, out := postBatch(t, m, string(body))
	data := out["data"].(map[string]any)
	if data["failed"].(float64) != 1 {
		t.Fatalf("超大脚本应 failed，实际 %v", data["failed"])
	}
	results := data["results"].([]any)
	if !strings.Contains(results[0].(map[string]any)["reason"].(string), "超限") {
		t.Errorf("应说明体积超限，实际 %v", results[0])
	}
}

// 本地导入无 @name 时，用文件名兜底。
func TestImportBatchFallsBackToFilename(t *testing.T) {
	m := newTestManager(t)
	_, out := postBatch(t, m, scriptsBody(map[string]string{"我的脚本.js": fakeLXScript("我的脚本")}))
	results := out["data"].(map[string]any)["results"].([]any)
	if name := results[0].(map[string]any)["name"]; name != "我的脚本" {
		t.Fatalf("应回退用文件名（去 .js），实际 %v", name)
	}
}
