package online

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 这一组盯的是「外挂进程形态的解析器」：musicdl sidecar。
//
// 三件事必须成立：
//   1. 对 `Pool` 来说它和原生解析器**没有区别**（同一套接口、同一套缓存与负缓存）；
//   2. 它需要的关键词（歌名/歌手）能一路传下去 —— 这是 musicdl 唯一能拿到直链的路；
//   3. sidecar 掉线 / 报错时**降级正确**：少一个平台，而不是整批失败、更不能把
//      「连不上」写成「这首歌不能听」（那会让负缓存把曲目锁死五分钟）。

// fakeSidecar 起一个假的 sidecar，返回指向它的客户端。
func fakeSidecar(t *testing.T, handler http.HandlerFunc) *MusicDL {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewMusicDL(srv.URL, t.Logf)
}

// sidecarJSON 按 body 里有没有 title 决定回什么，便于同时测「带关键词」与「只有 id」两条路。
func sidecarJSON(t *testing.T, resolveBody *[]byte) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"items": []map[string]any{{
					"id": "mg:42", "source": "mg", "platform_id": "42",
					"title": "海屿你", "artist": "马也_Crabbit",
					"download_url": "https://cdn.invalid/mg42.flac", "ext": "flac",
					"file_size": 12345,
				}},
				"errors": map[string]any{"bq": map[string]any{"kind": "unavailable", "message": "超时"}},
			})
		case "/resolve":
			if resolveBody != nil {
				*resolveBody = body
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"items": []map[string]any{{
					"id": "mg:42", "source": "mg",
					"url": "https://cdn.invalid/mg42.flac", "ext": "flac", "file_size": 12345,
				}},
				"errors": map[string]any{},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// ── 契约：id 的拼法必须与 sidecar 一致 ────────────────────────────────

func TestMusicDLSongIDMatchesSidecar(t *testing.T) {
	// 这个 id 是**跨进程的键**：Go 这边拼错，sidecar 那边就对不上，表现为
	// 「所有平台都解析失败」而且看不出为什么。钉死它。
	if got := musicdlSongID("mg", "42"); got != "mg:42" {
		t.Fatalf("id 拼法必须与 sidecar 的 core.song_id 一致，得到 %q", got)
	}
}

// ── 带关键词的那条路（正路）──────────────────────────────────────────

func TestResolveTracksSendsKeywordAndReturnsURL(t *testing.T) {
	var sent []byte
	m := fakeSidecar(t, sidecarJSON(t, &sent))
	pool := NewPool(m.Resolver("mg"))

	tracks := []Track{{
		Platform: "mg", PlatformID: "42", Title: "海屿你", Artists: []string{"马也_Crabbit"},
	}}
	got := pool.ResolveTracks(context.Background(), "mg", tracks)

	res, ok := got["42"]
	if !ok {
		t.Fatalf("应解析出 42，得到 %+v", got)
	}
	if res.URL != "https://cdn.invalid/mg42.flac" || res.Format != "flac" || res.Size != 12345 {
		t.Fatalf("解析结果不对：%+v", res)
	}

	// 关键词必须真的发过去了 —— 这是 musicdl 唯一能拿到直链的方式
	var payload struct {
		Items []MusicDLQuery `json:"items"`
	}
	if err := json.Unmarshal(sent, &payload); err != nil {
		t.Fatalf("发给 sidecar 的不是 JSON：%v / %s", err, sent)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("应发一条查询，得到 %d", len(payload.Items))
	}
	q := payload.Items[0]
	if q.ID != "mg:42" || q.Source != "mg" || q.Title != "海屿你" || q.Artist != "马也_Crabbit" {
		t.Fatalf("查询内容不对：%+v", q)
	}
}

func TestResolveManyWithoutQueriesSendsOnlyID(t *testing.T) {
	// ResolveMany 手里只有 id（既有调用点就是这样）。这条路是**尽力而为** ——
	// sidecar 只能靠自己的进程内缓存，拿不到就失败。这里钉的是「它确实没带关键词」，
	// 免得以后有人以为 ResolveMany 也能可靠解析外挂平台。
	var sent []byte
	m := fakeSidecar(t, sidecarJSON(t, &sent))
	pool := NewPool(m.Resolver("mg"))

	_ = pool.ResolveMany(context.Background(), "mg", []string{"42"})

	var payload struct {
		Items []MusicDLQuery `json:"items"`
	}
	if err := json.Unmarshal(sent, &payload); err != nil {
		t.Fatalf("不是 JSON：%v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Title != "" || payload.Items[0].Artist != "" {
		t.Fatalf("ResolveMany 不该带关键词：%+v", payload.Items)
	}
}

// ── Pool 的分派：有 QueryResolver 就走它，没有就走 ResolveBatch ────────

// queryResolver 是「实现了可选扩展」的假解析器。
type queryResolver struct {
	fakeResolver
	queries []Query
}

func (q *queryResolver) ResolveQueries(_ context.Context, qs []Query) (map[string]*Resolved, error) {
	q.queries = append(q.queries, qs...)
	out := map[string]*Resolved{}
	for _, one := range qs {
		out[one.ID] = &Resolved{URL: "https://cdn.invalid/" + one.ID + ".mp3"}
	}
	return out, nil
}

func TestPoolPrefersQueryResolverWhenKeywordsAvailable(t *testing.T) {
	qr := &queryResolver{fakeResolver: fakeResolver{platform: "mg", answers: map[string]*Resolved{}}}
	plain := &fakeResolver{platform: "kg", answers: map[string]*Resolved{}}
	pool := NewPool(qr, plain)

	tracks := []Track{
		{Platform: "mg", PlatformID: "1", Title: "甲", Artists: []string{"A"}},
		{Platform: "kg", PlatformID: "2", Title: "乙", Artists: []string{"B"}},
	}
	got := pool.ResolveTracks(context.Background(), "mg", tracks[:1])
	if len(got) != 1 {
		t.Fatalf("应解析出 1 条，得到 %+v", got)
	}
	if len(qr.queries) != 1 || qr.queries[0].Title != "甲" || qr.queries[0].Artist != "A" {
		t.Fatalf("QueryResolver 应收到关键词：%+v", qr.queries)
	}

	// 没实现 QueryResolver 的平台：照样能用，走 ResolveBatch（不带关键词）
	plain.answers["2"] = &Resolved{URL: "https://cdn.invalid/2.mp3"}
	got2 := pool.ResolveTracks(context.Background(), "kg", tracks[1:])
	if got2["2"] == nil {
		t.Fatalf("没实现 QueryResolver 的平台也该能解析：%+v", got2)
	}
	if qr.calls != 0 && len(qr.queries) != 1 {
		t.Fatal("kg 的解析不该跑到 mg 的 QueryResolver 上")
	}
}

// ── 降级：sidecar 掉线 ────────────────────────────────────────────────

func TestSidecarDownDegradesWithoutPoisoningCache(t *testing.T) {
	// 连接直接失败（端口没人听）
	m := NewMusicDL("http://127.0.0.1:1", t.Logf)
	pool := NewPool(m.Resolver("mg"), &fakeResolver{
		platform: "wy", answers: map[string]*Resolved{"9": {URL: "https://cdn.invalid/9.mp3"}},
	})

	tracks := []Track{{Platform: "mg", PlatformID: "42", Title: "海屿你", Artists: []string{"A"}}}
	got := pool.ResolveTracks(context.Background(), "mg", tracks)
	if len(got) != 0 {
		t.Fatalf("sidecar 掉线时应返回空，得到 %+v", got)
	}

	// ⚠️ 关键：不能把「连不上」写成「这首歌不能听」。
	// 写了负缓存 = 这首曲目在接下来五分钟里被锁死为不可播，sidecar 恢复了也没用。
	if _, hit, _ := pool.fromCache("mg\x0042"); hit {
		t.Fatal("网络故障不该进负缓存")
	}

	// 别的平台不受影响
	if r := pool.ResolveMany(context.Background(), "wy", []string{"9"}); r["9"] == nil {
		t.Fatal("sidecar 掉线不该影响原生平台")
	}
}

func TestSidecarErrorStatusDoesNotPoisonCache(t *testing.T) {
	m := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	pool := NewPool(m.Resolver("mg"))

	tracks := []Track{{Platform: "mg", PlatformID: "42", Title: "甲", Artists: []string{"A"}}}
	if got := pool.ResolveTracks(context.Background(), "mg", tracks); len(got) != 0 {
		t.Fatalf("5xx 时应返回空，得到 %+v", got)
	}
	if _, hit, _ := pool.fromCache("mg\x0042"); hit {
		t.Fatal("5xx 不该进负缓存（可能只是 sidecar 在重启）")
	}
}

func TestSidecarNotPlayableIsNegativeCached(t *testing.T) {
	// 与上一条相对：sidecar **明确**说这条曲目没有直链 → 这是稳定结论，可以负缓存。
	m := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "items": []any{},
			"errors": map[string]any{"mg:42": map[string]any{"kind": "unavailable", "message": "平台受限"}},
		})
	})
	pool := NewPool(m.Resolver("mg"))

	tracks := []Track{{Platform: "mg", PlatformID: "42", Title: "甲", Artists: []string{"A"}}}
	if got := pool.ResolveTracks(context.Background(), "mg", tracks); len(got) != 0 {
		t.Fatalf("应返回空，得到 %+v", got)
	}
	res, hit, miss := pool.fromCache("mg\x0042")
	if !hit || res != nil || !errors.Is(miss, ErrNotPlayable) {
		t.Fatalf("「sidecar 明确说不可播」应进负缓存，得到 hit=%v res=%v miss=%v", hit, res, miss)
	}
}

// ── 搜索：单源出错不影响别的源 ────────────────────────────────────────

func TestSearchReturnsItemsWhenOneSourceFails(t *testing.T) {
	m := fakeSidecar(t, sidecarJSON(t, nil))
	items, err := m.Search(context.Background(), "海屿你", []string{"mg", "bq"}, 15)
	if err != nil {
		t.Fatalf("有源失败但仍有结果时不该报错：%v", err)
	}
	if len(items) != 1 || items[0].ID != "mg:42" || items[0].DownloadURL == "" {
		t.Fatalf("应返回能播的那条：%+v", items)
	}
}

func TestSearchFailsOnlyWhenEverySourceFails(t *testing.T) {
	m := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "items": []any{},
			"errors": map[string]any{
				"mg": map[string]any{"kind": "unavailable", "message": "超时"},
				"bq": map[string]any{"kind": "unavailable", "message": "超时"},
			},
		})
	})
	if _, err := m.Search(context.Background(), "海屿你", []string{"mg", "bq"}, 15); err == nil {
		t.Fatal("所有源都失败时应返回错误（让调用方能如实报告「这次没搜到」）")
	}
}

func TestSearchEmptyKeywordIsNoop(t *testing.T) {
	m := NewMusicDL("http://127.0.0.1:1", t.Logf)
	items, err := m.Search(context.Background(), "   ", nil, 0)
	if err != nil || items != nil {
		t.Fatalf("空关键词不该发请求：items=%v err=%v", items, err)
	}
}

// ── 健康检查 ──────────────────────────────────────────────────────────

func TestHealthReportsSidecarState(t *testing.T) {
	m := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "version": "1.0.0", "musicdl": true, "sources": []string{"mg"},
		})
	})
	h, err := m.Health(context.Background())
	if err != nil {
		t.Fatalf("健康检查该成功：%v", err)
	}
	if !h.OK || !h.MusicDL || h.Version != "1.0.0" {
		t.Fatalf("健康信息不对：%+v", h)
	}

	// 没配地址 / 连不上都要给错误，而不是「当它是好的」
	if _, err := NewMusicDL("", nil).Health(context.Background()); err == nil {
		t.Fatal("没配地址时该报错")
	}
	if _, err := NewMusicDL("http://127.0.0.1:1", nil).Health(context.Background()); err == nil {
		t.Fatal("连不上时该报错")
	}
}

func TestHealthRejectsNonJSON(t *testing.T) {
	// 端口被别人占了（回了个 HTML）时，不能当成健康
	m := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not me</html>"))
	})
	if _, err := m.Health(context.Background()); err == nil {
		t.Fatal("响应不是 JSON 时该报错")
	}
}

// ── 错误归类 ──────────────────────────────────────────────────────────

func TestSideErrorDistinguishesInvalidFromUnavailable(t *testing.T) {
	// 「参数错了」是我们自己的 bug，必须能从日志里一眼看出来；
	// 「平台不可用」只是少一个平台。混在一起的话，一次风控会被误读成搜索坏了。
	if !(MusicDLSideError{Kind: "invalid", Message: "认不出平台"}).IsInvalid() {
		t.Fatal("kind=invalid 应被判为参数错")
	}
	if (MusicDLSideError{Kind: "unavailable", Message: "超时"}).IsInvalid() {
		t.Fatal("kind=unavailable 不该被判为参数错")
	}
	if got := (MusicDLSideError{Kind: "invalid", Message: "x"}).Error(); !strings.Contains(got, "invalid") {
		t.Fatalf("错误串该带上 kind：%q", got)
	}
}

// ── 源清单与单源检测（v2.1.89：musicdl 真机上有 56 个源）────────────────

func TestMusicDLSourcesParsesCatalog(t *testing.T) {
	m := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sources" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "musicdl": true, "registered": 56,
			"enabled": []string{"mg"},
			"sources": []map[string]any{
				{"id": "mg", "label": "咪咕音乐", "client": "MiguMusicClient", "enabled": true, "native": false},
				{"id": "kg", "label": "酷狗音乐", "client": "KuGouMusicClient", "enabled": false, "native": true},
			},
		})
	})
	got, err := m.Sources(context.Background())
	if err != nil {
		t.Fatalf("该成功：%v", err)
	}
	if got.Registered != 56 {
		t.Fatalf("registered 该原样带出来（界面要显示「56 个源」）：%d", got.Registered)
	}
	if len(got.Sources) != 2 {
		t.Fatalf("该解出 2 个源：%+v", got.Sources)
	}
	if got.Sources[0].ID != "mg" || got.Sources[0].Label != "咪咕音乐" || got.Sources[0].Native {
		t.Fatalf("字段映射不对：%+v", got.Sources[0])
	}
	// ⚠️ native 决定界面要不要提示「会换掉原生实现」—— 传丢了用户就不知道风险
	if !got.Sources[1].Native {
		t.Fatalf("kg 与原生重叠，native 必须为真：%+v", got.Sources[1])
	}
}

func TestMusicDLProbeSendsRequestedSources(t *testing.T) {
	var sent []byte
	m := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "keyword": "海阔天空",
			"results": []map[string]any{
				{"id": "mg", "ms": 2073, "items": 5, "ok": true},
				{"id": "bq", "ms": 12003, "items": 0, "ok": false,
					"error": map[string]any{"kind": "unavailable", "message": "源 bq 超过 12s 预算"}},
			},
		})
	})
	got, err := m.Probe(context.Background(), []string{"mg", "bq"}, "海阔天空", 3)
	if err != nil {
		t.Fatalf("该成功：%v", err)
	}
	if len(got) != 2 || got[0].ID != "mg" || got[0].MS != 2073 || got[0].Items != 5 || !got[0].OK {
		t.Fatalf("成功那条解错了：%+v", got)
	}
	if got[1].OK || got[1].Error == nil || got[1].Error.Kind != "unavailable" {
		t.Fatalf("失败那条该带上 kind（界面据此说清是平台抽风还是我们写错）：%+v", got[1])
	}

	var payload struct {
		Sources []string `json:"sources"`
		Keyword string   `json:"keyword"`
		Limit   int      `json:"limit"`
	}
	if err := json.Unmarshal(sent, &payload); err != nil {
		t.Fatalf("发给 sidecar 的不是 JSON：%v / %s", err, sent)
	}
	if len(payload.Sources) != 2 || payload.Keyword != "海阔天空" || payload.Limit != 3 {
		t.Fatalf("请求内容不对：%+v", payload)
	}
}

func TestMusicDLProbeOmitsEmptySources(t *testing.T) {
	// 不传 sources 时**不要**塞一个空数组进去：sidecar 的语义是「空/缺省 = 用当前
	// 启用的那些」，塞空数组会让它一个源都不测（界面上表现为「检测了但没结果」）。
	var sent map[string]any
	m := fakeSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "results": []any{}})
	})
	if _, err := m.Probe(context.Background(), nil, "", 0); err != nil {
		t.Fatalf("该成功：%v", err)
	}
	if _, has := sent["sources"]; has {
		t.Fatalf("不传 sources 时不该带这个键：%+v", sent)
	}
}

func TestMusicDLSourcesFailsWhenSidecarDown(t *testing.T) {
	// 连不上要**报错**（而不是返回空清单）：handler 据此回 code 502 + 一句能看懂的
	// 话。返回空清单的话，界面会显示「56 个源」但一个都点不动。
	m := NewMusicDL("http://127.0.0.1:1", t.Logf)
	if _, err := m.Sources(context.Background()); err == nil {
		t.Fatal("sidecar 连不上时该报错")
	}
	if _, err := m.Probe(context.Background(), []string{"mg"}, "", 0); err == nil {
		t.Fatal("sidecar 连不上时检测也该报错")
	}
}
