package fnos

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type recorded struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   map[string]any
}

// fakeMusicServer 起一个假的飞牛音乐接口服务，按路径返回预设响应。
func fakeMusicServer(t *testing.T, replies map[string]any) *[]recorded {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "music.sock")

	var mu sync.Mutex
	got := &[]recorded{}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)

		mu.Lock()
		*got = append(*got, recorded{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Auth:   r.Header.Get("Authorization"),
			Body:   parsed,
		})
		mu.Unlock()

		reply, ok := replies[r.URL.Path]
		if !ok {
			reply = map[string]any{"code": 0, "msg": "", "data": map[string]any{}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	})

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("无法创建 Unix socket，跳过: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	origSock, origBase := musicSocket, musicBase
	t.Cleanup(func() { musicSocket, musicBase = origSock, origBase })
	musicSocket = sock
	musicBase = "http://localhost/music/api/v1"
	return got
}

func ok(data any) map[string]any {
	return map[string]any{"code": 0, "msg": "", "data": data}
}

// 飞牛音乐接口用裸令牌，不带 Bearer 前缀（与官方开放 API 不同）。
func TestMusicUsesRawTokenNotBearer(t *testing.T) {
	got := fakeMusicServer(t, map[string]any{
		"/music/api/v1/user/me": ok(map[string]any{"guid": "g1", "name": "tester"}),
	})

	m := NewMusic("raw-token-123")
	if _, err := m.Me(); err != nil {
		t.Fatalf("Me 失败: %v", err)
	}

	reqs := *got
	if len(reqs) != 1 {
		t.Fatalf("应收到 1 次请求，实际 %d", len(reqs))
	}
	if reqs[0].Auth != "raw-token-123" {
		t.Fatalf("Authorization = %q，应为裸令牌（不带 Bearer）", reqs[0].Auth)
	}
}

func TestMusicNoTokenFails(t *testing.T) {
	t.Setenv("FNOS_TOKEN", "")
	orig := musicDBPath
	t.Cleanup(func() { musicDBPath = orig })
	musicDBPath = filepath.Join(t.TempDir(), "none.db")

	m := NewMusic("")
	if _, err := m.Me(); err == nil {
		t.Fatal("无令牌时应报错")
	}
}

func TestMusicMeParsesAccount(t *testing.T) {
	fakeMusicServer(t, map[string]any{
		"/music/api/v1/user/me": ok(map[string]any{"guid": "acc-1", "name": "liwenyao"}),
	})
	account, err := NewMusic("tok").Me()
	if err != nil {
		t.Fatalf("Me 失败: %v", err)
	}
	if account["guid"] != "acc-1" || account["name"] != "liwenyao" {
		t.Fatalf("account = %v", account)
	}
}

func TestMusicPlaylistsHandlesBothShapes(t *testing.T) {
	// 直接给数组
	fakeMusicServer(t, map[string]any{
		"/music/api/v1/playlist/list": ok([]map[string]any{{"guid": "p1", "name": "A"}}),
	})
	list, err := NewMusic("tok").Playlists()
	if err != nil || len(list) != 1 || list[0]["guid"] != "p1" {
		t.Fatalf("直接数组形态解析失败: %v %v", list, err)
	}

	// 包一层 list
	fakeMusicServer(t, map[string]any{
		"/music/api/v1/playlist/list": ok(map[string]any{"list": []map[string]any{{"guid": "p2", "name": "B"}}}),
	})
	list, err = NewMusic("tok").Playlists()
	if err != nil || len(list) != 1 || list[0]["guid"] != "p2" {
		t.Fatalf("嵌套 list 形态解析失败: %v %v", list, err)
	}
}

func TestMusicSearchTracksEscapesKeyword(t *testing.T) {
	got := fakeMusicServer(t, map[string]any{
		"/music/api/v1/search/track": ok([]map[string]any{{"guid": "t1", "title": "晴天"}}),
	})
	if _, err := NewMusic("tok").SearchTracks("周杰伦 晴天"); err != nil {
		t.Fatalf("SearchTracks 失败: %v", err)
	}
	reqs := *got
	if !strings.Contains(reqs[0].Query, "q=") {
		t.Fatalf("查询串缺少 q 参数: %q", reqs[0].Query)
	}
	if strings.Contains(reqs[0].Query, " ") {
		t.Fatalf("关键词未转义，查询串含空格: %q", reqs[0].Query)
	}
}

func TestMusicPlaylistTracksPaginates(t *testing.T) {
	// 第一页满 50 条，第二页 3 条 → 应请求 2 次并合并
	full := make([]map[string]any, playlistPageSize)
	for i := range full {
		full[i] = map[string]any{"guid": "t"}
	}
	var calls int
	var mu sync.Mutex

	dir := t.TempDir()
	sock := filepath.Join(dir, "m.sock")
	mux := http.NewServeMux()
	mux.HandleFunc("/music/api/v1/track/playlist-detail/list", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		data := any(full)
		if n > 1 {
			data = []map[string]any{{"guid": "a"}, {"guid": "b"}, {"guid": "c"}}
		}
		_ = json.NewEncoder(w).Encode(ok(data))
	})
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("跳过: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	origSock, origBase := musicSocket, musicBase
	t.Cleanup(func() { musicSocket, musicBase = origSock, origBase })
	musicSocket, musicBase = sock, "http://localhost/music/api/v1"

	tracks, err := NewMusic("tok").PlaylistTracks("p1")
	if err != nil {
		t.Fatalf("PlaylistTracks 失败: %v", err)
	}
	if len(tracks) != playlistPageSize+3 {
		t.Fatalf("分页合并后应有 %d 条，实际 %d", playlistPageSize+3, len(tracks))
	}
}

func TestMusicCreatePlaylistReturnsGUID(t *testing.T) {
	got := fakeMusicServer(t, map[string]any{
		"/music/api/v1/playlist/create": ok(map[string]any{"guid": "new-guid"}),
	})
	guid, err := NewMusic("tok").CreatePlaylist("我的歌单", "描述")
	if err != nil {
		t.Fatalf("CreatePlaylist 失败: %v", err)
	}
	if guid != "new-guid" {
		t.Fatalf("guid = %q", guid)
	}
	body := (*got)[0].Body
	if body["name"] != "我的歌单" || body["description"] != "描述" {
		t.Fatalf("请求体 = %v", body)
	}
	if body["visibility"] != float64(1) {
		t.Fatalf("visibility 应为 1，实际 %v", body["visibility"])
	}
}

func TestMusicFindOrCreatePlaylist(t *testing.T) {
	// 已存在同名 → 复用，不创建
	got := fakeMusicServer(t, map[string]any{
		"/music/api/v1/playlist/list": ok([]map[string]any{{"guid": "exist-1", "name": "目标"}}),
	})
	guid, created, err := NewMusic("tok").FindOrCreatePlaylist("目标", "")
	if err != nil || guid != "exist-1" || created {
		t.Fatalf("复用分支失败: guid=%q created=%v err=%v", guid, created, err)
	}
	for _, r := range *got {
		if strings.HasSuffix(r.Path, "/playlist/create") {
			t.Fatal("已存在同名歌单时不应调用创建")
		}
	}

	// 不存在 → 创建
	got2 := fakeMusicServer(t, map[string]any{
		"/music/api/v1/playlist/list":   ok([]map[string]any{}),
		"/music/api/v1/playlist/create": ok(map[string]any{"guid": "made-1"}),
	})
	guid, created, err = NewMusic("tok").FindOrCreatePlaylist("新目标", "")
	if err != nil || guid != "made-1" || !created {
		t.Fatalf("创建分支失败: guid=%q created=%v err=%v", guid, created, err)
	}
	_ = got2

	// 同名多个 → 报错，避免误推
	fakeMusicServer(t, map[string]any{
		"/music/api/v1/playlist/list": ok([]map[string]any{
			{"guid": "d1", "name": "重名"}, {"guid": "d2", "name": "重名"},
		}),
	})
	if _, _, err := NewMusic("tok").FindOrCreatePlaylist("重名", ""); err == nil {
		t.Fatal("存在多个同名歌单时应报错")
	}
}

func TestMusicAddTracksBatches(t *testing.T) {
	got := fakeMusicServer(t, map[string]any{
		"/music/api/v1/playlist/add-track": ok(nil),
	})

	guids := make([]string, 120) // 120 条 → 50 + 50 + 20，共 3 批
	for i := range guids {
		guids[i] = "g"
	}
	if err := NewMusic("tok").AddTracks("p1", guids); err != nil {
		t.Fatalf("AddTracks 失败: %v", err)
	}

	reqs := *got
	if len(reqs) != 3 {
		t.Fatalf("120 条应分 3 批，实际 %d 批", len(reqs))
	}
	wantSizes := []int{50, 50, 20}
	for i, r := range reqs {
		items, _ := r.Body["trackGUIDs"].([]any)
		if len(items) != wantSizes[i] {
			t.Errorf("第 %d 批应有 %d 条，实际 %d", i+1, wantSizes[i], len(items))
		}
		if r.Body["guid"] != "p1" {
			t.Errorf("第 %d 批 guid = %v", i+1, r.Body["guid"])
		}
	}
}

func TestMusicSetPlaylistCoverUploadsThenEdits(t *testing.T) {
	got := fakeMusicServer(t, map[string]any{
		"/music/api/v1/static/cover/playlist": ok(map[string]any{"coverId": "cov-9"}),
		"/music/api/v1/playlist/edit":         ok(nil),
	})

	err := NewMusic("tok").SetPlaylistCover("p1", "歌单名", []byte("fake-jpeg-bytes"), "cover.jpg")
	if err != nil {
		t.Fatalf("SetPlaylistCover 失败: %v", err)
	}

	reqs := *got
	if len(reqs) != 2 {
		t.Fatalf("应上传封面 + 写回歌单，共 2 次请求，实际 %d", len(reqs))
	}
	if !strings.HasSuffix(reqs[0].Path, "/static/cover/playlist") {
		t.Errorf("第一次应为封面上传，实际 %s", reqs[0].Path)
	}
	if !strings.HasSuffix(reqs[1].Path, "/playlist/edit") {
		t.Errorf("第二次应为歌单编辑，实际 %s", reqs[1].Path)
	}
	if reqs[1].Body["coverId"] != "cov-9" {
		t.Errorf("edit 未带上 coverId: %v", reqs[1].Body)
	}
	if reqs[1].Body["name"] != "歌单名" {
		t.Errorf("edit 未带上名称: %v", reqs[1].Body)
	}
}

func TestMusicSetPlaylistCoverSkipsEmptyImage(t *testing.T) {
	got := fakeMusicServer(t, map[string]any{})
	if err := NewMusic("tok").SetPlaylistCover("p1", "x", nil, "c.jpg"); err != nil {
		t.Fatalf("空图片不应报错: %v", err)
	}
	if len(*got) != 0 {
		t.Fatalf("空图片不应发起任何请求，实际 %d 次", len(*got))
	}
}

func TestMusicNonZeroCodeBecomesError(t *testing.T) {
	fakeMusicServer(t, map[string]any{
		"/music/api/v1/playlist/list": map[string]any{"code": 401, "msg": "INVALID TOKEN", "data": nil},
	})
	_, err := NewMusic("bad").Playlists()
	if err == nil || !strings.Contains(err.Error(), "INVALID TOKEN") {
		t.Fatalf("应返回含服务端消息的错误，实际: %v", err)
	}
}

func TestMusicAvailableRequiresTokenAndSocket(t *testing.T) {
	orig := musicSocket
	t.Cleanup(func() { musicSocket = orig })

	musicSocket = filepath.Join(t.TempDir(), "nope.sock")
	if NewMusic("tok").Available() {
		t.Fatal("socket 不存在时 Available 应为 false")
	}

	fakeMusicServer(t, map[string]any{})
	if NewMusic("tok").Available() != true {
		t.Fatal("token + socket 就绪时 Available 应为 true")
	}
	if NewMusic("").Available() {
		t.Fatal("无令牌时 Available 应为 false")
	}
}
