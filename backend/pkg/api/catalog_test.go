package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// 接口目录的契约测试。
//
// 为什么必须有：catalog.go 是接口文档的**单一数据源**，外部 AI 靠
// GET /api/catalog 决定怎么调用；但它和真实路由之间原来没有任何机制保证一致。
// 结果是「加了接口忘了登记」（AI 发现不了）和「改了路径忘了同步」（AI 照文档调出 404），
// 而调用方完全看不出是文档错 —— 只会以为应用坏了。
//
// 目录与真实路由的**两边对账**由 catalog_routes_test.go 负责（路由表在 routes.go）；
// 本文件只保证目录自身自洽。

// maxNotesRunes 单条 notes 的篇幅上限。
//
// 目录是给 AI 读的操作面，不是交接文档：踩坑经验写进 HANDOVER，
// 一条上千字的 notes 会挤掉别的接口在上下文里的位置，而且几乎必然过期。
const maxNotesRunes = 200

func catalogByKey(list []Endpoint) map[string]Endpoint {
	m := make(map[string]Endpoint, len(list))
	for _, e := range list {
		m[e.Method+" "+e.Path] = e
	}
	return m
}

func TestEveryEndpointHasAValidRole(t *testing.T) {
	list := APICatalog("http://example.com")
	if len(list) == 0 {
		t.Fatal("目录为空")
	}
	seen := map[string]bool{}
	for _, e := range list {
		key := e.Method + " " + e.Path
		if seen[key] {
			t.Errorf("目录里有重复条目：%s", key)
		}
		seen[key] = true

		if e.Role != roleCore && e.Role != roleInternal {
			t.Errorf("%s 的 role = %q，必须是 %q 或 %q", key, e.Role, roleCore, roleInternal)
		}
		// internal 与 hidden 必须同步：抽屉靠 hidden 折叠，AI 靠 role 分流，
		// 两者一旦不一致，界面看到的和程序读到的就不是同一份目录。
		if want := e.Role == roleInternal; e.Hidden != want {
			t.Errorf("%s role=%s 但 hidden=%v（应一致）", key, e.Role, e.Hidden)
		}
		if n := utf8.RuneCountInString(e.Notes); n > maxNotesRunes {
			t.Errorf("%s 的 notes %d 字，超过上限 %d —— 经验该放 HANDOVER，不是目录", key, n, maxNotesRunes)
		}
		if strings.TrimSpace(e.Summary) == "" {
			t.Errorf("%s 没有 summary", key)
		}
	}
}

// internal 名单是白名单：路径写错、或接口删了没同步，这条就会红。
func TestInternalAllowlistPointsAtRealEntries(t *testing.T) {
	index := catalogByKey(APICatalog("http://example.com"))
	for key := range internalEndpoints {
		if _, ok := index[key]; !ok {
			t.Errorf("internal 名单里有 %q，但目录中没有这条接口（写错或已删除，请同步）", key)
		}
	}
}

// 角色只能由名单推导，不许在条目里散着写。
func TestRolesDeriveFromAllowlist(t *testing.T) {
	for _, e := range APICatalog("http://example.com") {
		key := e.Method + " " + e.Path
		if (e.Role == roleInternal) != internalEndpoints[key] {
			t.Errorf("%s role=%q 与 internal 名单不一致（名单里 %v）", key, e.Role, internalEndpoints[key])
		}
	}
}

// 食谱引用的接口必须真的在目录里，否则 AI 照着做会撞 404。
func TestWorkflowsReferenceCataloguedEndpoints(t *testing.T) {
	index := catalogByKey(APICatalog("http://example.com"))
	g := CatalogGuide()
	if len(g.Workflows) == 0 {
		t.Fatal("guide.workflows 为空：外部 AI 拿不到「怎么做」")
	}
	for _, wf := range g.Workflows {
		if strings.TrimSpace(wf.Task) == "" || len(wf.Steps) == 0 {
			t.Error("有条食谱缺 task 或 steps")
			continue
		}
		if len(wf.Endpoints) == 0 {
			t.Errorf("食谱「%s」没列出用到的接口", wf.Task)
		}
		for _, ref := range wf.Endpoints {
			if _, ok := index[ref]; !ok {
				t.Errorf("食谱「%s」引用了 %q，但目录里没有这条（写法漂移或接口已撤）", wf.Task, ref)
			}
		}
	}
}

func TestGuideSectionsAreFilled(t *testing.T) {
	g := CatalogGuide()
	// prereq 必须写明「取直链要有人开着页面」—— 这是外部 AI 最容易撞的墙。
	if !strings.Contains(strings.Join(g.Prereq, "\n"), "页面") {
		t.Error("guide.prereq 没说明「哪些操作必须有开着的应用页面」")
	}
	if len(g.What) == 0 || len(g.Rules) == 0 {
		t.Error("guide 的 what / rules 不能为空")
	}
}

// usage 由 guide 生成：手写两份必然不同步（原来那 6 条 usage 就漏了补全等新功能）。
func TestUsageDerivedFromGuide(t *testing.T) {
	g := CatalogGuide()
	usage := usageFromGuide(g)
	if len(usage) != len(g.Workflows)+len(g.Rules) {
		t.Fatalf("usage 条数 %d，应为 workflows(%d)+rules(%d)", len(usage), len(g.Workflows), len(g.Rules))
	}
	for i, wf := range g.Workflows {
		if !strings.Contains(usage[i], wf.Task) {
			t.Errorf("usage 第 %d 条不含任务名 %q", i, wf.Task)
		}
	}
}

func TestCatalogResponseShape(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).HandleCatalog(rec, httptest.NewRequest(http.MethodGet, "/api/catalog", nil))

	var resp CatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	// endpoints 在**顶层**、不在 data 里 —— 这条结构约定有外部调用方依赖，别改。
	if len(resp.Endpoints) == 0 {
		t.Fatal("顶层 endpoints 为空")
	}
	if len(resp.Guide.Workflows) == 0 || len(resp.Guide.What) == 0 {
		t.Fatal("顶层 guide 为空")
	}
	if len(resp.Usage) == 0 {
		t.Fatal("顶层 usage 为空")
	}
}
