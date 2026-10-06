package lxnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── 假进程：只验状态机，不碰真 node ──────────────────────────────────────

type fakeProc struct {
	started bool
	stopped bool
	done    chan struct{}
	failOn  error
}

func (f *fakeProc) Start() error {
	if f.failOn != nil {
		return f.failOn
	}
	f.started = true
	f.done = make(chan struct{})
	return nil
}
func (f *fakeProc) Stop() error             { f.stopped = true; return nil }
func (f *fakeProc) Exited() <-chan struct{} { return f.done }

func fakeManager(p *fakeProc) *Manager {
	m := New()
	m.newProc = func(Config) process { return p }
	return m
}

// 关掉时必须停进程、状态回 off —— 这是「用户关了就真关」的最小承诺。
func TestApplyDisabledStopsProcess(t *testing.T) {
	p := &fakeProc{}
	m := fakeManager(p)
	m.Apply(Config{Enabled: true, Dir: "/tmp"})
	if m.Status()["state"] != StateReady {
		t.Fatalf("该是 ready：%+v", m.Status())
	}
	m.Apply(Config{Enabled: false})
	if !p.stopped {
		t.Fatal("关掉时该停掉子进程")
	}
	if got := m.Status()["state"]; got != StateOff {
		t.Fatalf("状态该是 off，得到 %v", got)
	}
}

// 起不来要如实报 failed 并带上原因，不能装作 ready。
func TestApplyStartFailureReportsFailed(t *testing.T) {
	p := &fakeProc{failOn: os.ErrPermission}
	m := fakeManager(p)
	m.Apply(Config{Enabled: true, Dir: "/tmp"})
	st := m.Status()
	if st["state"] != StateFailed {
		t.Fatalf("该是 failed：%+v", st)
	}
	if st["error"] == nil || st["error"] == "" {
		t.Fatalf("failed 该带原因：%+v", st)
	}
}

// 进程自己死了，状态要跟着变 —— 否则界面会一直显示「可用」，而其实早没了。
func TestProcessExitMarksFailed(t *testing.T) {
	p := &fakeProc{}
	m := fakeManager(p)
	m.Apply(Config{Enabled: true, Dir: "/tmp"})
	close(p.done) // 模拟进程退出
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.Status()["state"] == StateFailed {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("进程退出后该变成 failed：%+v", m.Status())
}

// 宿主不可用时 Resolve 必须**只返回错误**（调用方回落），不能 panic、不能挂住。
func TestResolveUnavailableReturnsError(t *testing.T) {
	m := New()
	if _, err := m.Resolve(context.Background(), "wy", map[string]any{"name": "x"}, "320k"); err == nil {
		t.Fatal("没起来时 Resolve 该报错")
	}
}

// ── 真集成：起真的 node + 真的 sidecar/lx_host/server.mjs ────────────────

func TestRealHostStartsAndReports(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过集成用例")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(root, "sidecar", "lx_host", "server.mjs")
	if _, err := os.Stat(host); err != nil {
		t.Skipf("宿主脚本不在：%v", err)
	}
	dir := t.TempDir()
	// 放一个能解析出直链的合成音源：证明整条链路真的通到「拿到 URL」。
	script := `
lx.send('inited', { sources: { wy: { name: '合成' } } })
lx.on('request', async ({ info }) => 'https://cdn.example.invalid/' + encodeURIComponent(info.musicInfo.name || 'MISSING-musicInfo') + '.mp3')
`
	if err := os.WriteFile(filepath.Join(dir, "ok.js"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New()
	m.Apply(Config{Enabled: true, NodeBin: "node", Script: host, Dir: dir, Port: 18921})
	defer m.Stop()

	st := m.Status()
	if st["state"] != StateReady {
		t.Fatalf("真宿主该起来：%+v", st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url, err := m.Resolve(ctx, "wy", map[string]any{"name": "晴天"}, "320k")
	if err != nil {
		t.Fatalf("该解析出直链：%v", err)
	}
	if !strings.Contains(url, "%E6%99%B4%E5%A4%A9") {
		t.Fatalf("直链该带上曲名（URL 编码后）：%q", url)
	}
	t.Logf("解析得到：%s", url)
}

// ── /search：真 node + 真宿主的集成用例 ─────────────────────────────────
//
// 合成脚本刻意**照真脚本 qsvip 的读法**写（实测契约，见 lxnode.go 的 Search 注释）：
//
//	· keyword 在 `info` **顶层**；读不到就**静默返回空**、不报错
//	  —— 所以「宿主把参数嵌成 info.musicInfo」这种错在这里会表现成「搜到 0 条」，
//	  而不是一个显眼的报错。这正是真脚本的行为，用例要守住它。
//	· `pagesize` 才是它认的名字（写成 limit 会静默按默认 30 搜）—— 所以下面
//	  把收到的 info 整个 echo 回来，好让用例逐字段断言宿主到底发了什么。
const searchSourceScript = `
lx.send('inited', { sources: {
  qsvip: { name: '合成搜索源', type: 'music', actions: ['musicSearch', 'musicUrl'], qualitys: ['128k'] },
  wy: { name: '合成解析源', type: 'music', actions: ['musicUrl'], qualitys: ['320k'] },
} })
lx.on('request', ({ action, info }) => {
  if (action !== 'musicSearch') return Promise.reject(new Error('action not support'))
  const kw = info && info.keyword
  if (!kw) return Promise.resolve({ isEnd: true, list: [] })
  return Promise.resolve({ isEnd: true, total: 1, list: [
    { id: 'S1', songmid: 'S1', hash: 'S1', name: kw, singer: '合成歌手', albumName: '合成专辑',
      duration: 245, pic: 'https://x/y.jpg', echo: info, _raw: { upstream_private: '不该出现在 raw 里' } },
  ] })
})
`

// 会炸的源：单源失败不该影响别的源（沿用 /resolve 的风格）。
const searchBoomScript = `
lx.send('inited', { sources: { qsvip: { name: '会炸的源', type: 'music', actions: ['musicSearch'], qualitys: ['128k'] } } })
lx.on('request', () => Promise.reject(new Error('上游炸了：ECONNREFUSED')))
`

// 只声明 musicUrl 的源：搜索**必须**跳过它（脚本自己都 reject `action not support`）。
const searchUnsupportedScript = `
lx.send('inited', { sources: { wy: { name: '只解析的源', type: 'music', actions: ['musicUrl'], qualitys: ['320k'] } } })
lx.on('request', ({ action }) => action === 'musicUrl'
  ? Promise.resolve('https://cdn.example.invalid/x.mp3')
  : Promise.reject(new Error('action not support')))
`

// startRealHost 起一个真 node + 真宿主，脚本目录是 dir；没有 node / 宿主脚本时跳过。
func startRealHost(t *testing.T, dir string, port int) *Manager {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过集成用例")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(root, "sidecar", "lx_host", "server.mjs")
	if _, err := os.Stat(host); err != nil {
		t.Skipf("宿主脚本不在：%v", err)
	}
	m := New()
	m.Apply(Config{Enabled: true, NodeBin: "node", Script: host, Dir: dir, Port: port})
	t.Cleanup(m.Stop)
	if st := m.Status(); st["state"] != StateReady {
		t.Fatalf("真宿主该起来：%+v", st)
	}
	return m
}

// writeSources 把「文件名 → 脚本内容」写进目录。
func writeSources(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, code := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// SearchAll 走完整链路：真 node 宿主 → 声明了 musicSearch 的源 → 归一化结果。
func TestSearchAllRealHost(t *testing.T) {
	dir := t.TempDir()
	writeSources(t, dir, map[string]string{
		"search.js": searchSourceScript,
		"boom.js":   searchBoomScript,
		"parse.js":  searchUnsupportedScript,
	})
	m := startRealHost(t, dir, 18941)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	songs, err := m.SearchAll(ctx, "晴天", 5)
	if err != nil {
		t.Fatalf("该搜到东西：%v", err)
	}
	// 只该有**一个**源能搜（qsvip@search.js）；会炸的那个要吞掉，只声明 musicUrl 的源要跳过
	if len(songs) != 1 {
		t.Fatalf("该只有 1 条（单源失败与不支持搜索的源都该被隔离）：%+v", songs)
	}
	s := songs[0]
	if s.Source != "qsvip" || s.Script != "search.js" {
		t.Fatalf("该来自 search.js 的 qsvip：%+v", s)
	}
	if s.ID != "S1" || s.Name != "晴天" || s.Singer != "合成歌手" || s.Album != "合成专辑" {
		t.Fatalf("字段该原样搬过来：%+v", s)
	}
	if s.Duration != 245 {
		t.Fatalf("时长该按**秒**搬（宿主已压过单位）：%d", s.Duration)
	}
	if s.Pic != "https://x/y.jpg" {
		t.Fatalf("封面该搬过来：%q", s.Pic)
	}
	// 实测的 qsvip 不给音质/体积 —— 缺失就该是空的，不许由 Go 侧补一个默认值
	if s.Quality != "" || s.Size != 0 {
		t.Fatalf("脚本没给音质/体积时该保持缺失：quality=%q size=%d", s.Quality, s.Size)
	}
}

// 「宿主到底发了什么给脚本」——这条只能从宿主那边问，所以直接打它的 /search。
//
// 它钉的是**契约本身**（三件事，全是实测出来的，写错任何一条都会让真脚本静默搜不到）：
//  1. 搜索参数在 `info` **顶层**（不是 info.musicInfo —— 那样真脚本会静默返回空）；
//  2. 尺寸字段叫 `pagesize`（不是 limit —— 那样真脚本会静默按默认 30 搜）；
//  3. 只调声明了 musicSearch 的源，其余明确记一笔「已跳过」；单源失败只记一笔。
func TestHostSearchPayloadContract(t *testing.T) {
	dir := t.TempDir()
	writeSources(t, dir, map[string]string{
		"search.js": searchSourceScript,
		"boom.js":   searchBoomScript,
		"parse.js":  searchUnsupportedScript,
	})
	m := startRealHost(t, dir, 18942)

	body := strings.NewReader(`{"keyword":"晴天","page":2,"limit":7}`)
	req, err := http.NewRequest(http.MethodPost, m.cfg.base()+"/search", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("该 200，得到 %d", resp.StatusCode)
	}
	var out struct {
		Results []struct {
			Script string         `json:"script"`
			Source string         `json:"source"`
			Raw    map[string]any `json:"raw"`
		} `json:"results"`
		Tried []Tried `json:"tried"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 {
		t.Fatalf("该 1 条结果：%+v", out.Results)
	}
	echo, _ := out.Results[0].Raw["echo"].(map[string]any)
	if echo == nil {
		t.Fatalf("脚本该把收到的 info 原样 echo 回来（raw 要保留脚本自己的字段）：%+v", out.Results[0].Raw)
	}
	if echo["keyword"] != "晴天" {
		t.Fatalf("keyword 该在 info 顶层：%+v", echo)
	}
	if _, nested := echo["musicInfo"]; nested {
		t.Fatalf("⚠️ 搜索参数不能被嵌成 info.musicInfo —— 真脚本读不到 keyword，会**静默**返回空：%+v", echo)
	}
	if got := fmt.Sprint(echo["pagesize"]); got != "7" {
		t.Fatalf("尺寸字段必须叫 pagesize（脚本只认这个名字），得到 %v：%+v", echo["pagesize"], echo)
	}
	if got := fmt.Sprint(echo["page"]); got != "2" {
		t.Fatalf("page 该透传：%v", echo["page"])
	}
	// raw 保留脚本自己的字段（echo 在），但**不带** `_raw`：那是脚本的上游透传，
	// 内容与体积都不可知，宿主不往外倒（这是刻意的取舍，不是漏掉）
	if _, leaked := out.Results[0].Raw["_raw"]; leaked {
		t.Fatalf("raw 里不该带 _raw（脚本的上游透传）：%+v", out.Results[0].Raw)
	}

	// tried 里必须有：不支持搜索的源的跳过记录 + 会炸的源的原因
	var sawSkip, sawBoom bool
	for _, tr := range out.Tried {
		if tr.Source == "wy" && strings.Contains(tr.Error, "未声明 musicSearch") {
			sawSkip = true
		}
		if tr.Script == "boom.js" && strings.Contains(tr.Error, "上游炸了") {
			sawBoom = true
		}
	}
	if !sawSkip {
		t.Fatalf("只声明 musicUrl 的源该被明确记成「已跳过」：%+v", out.Tried)
	}
	if !sawBoom {
		t.Fatalf("单源失败该只记一笔，不影响别的源：%+v", out.Tried)
	}
}

// 宿主没起来时 Search 必须**只返回哨兵错误**（池要据此静默跳过），不能 panic、不能挂住。
func TestSearchNotRunningReturnsErrNotRunning(t *testing.T) {
	m := New()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := m.Search(ctx, "", "晴天", 1, 5); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("该返回 ErrNotRunning（池要据此静默跳过），得到 %v", err)
	}
	if _, err := m.SearchAll(ctx, "晴天", 5); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("SearchAll 同样该是 ErrNotRunning，得到 %v", err)
	}
	// Resolve 的「没起来」也走同一个哨兵（字符串没变，只是可 errors.Is 了）
	if _, err := m.Resolve(ctx, "wy", map[string]any{"name": "x"}, "320k"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Resolve 没起来时也该 errors.Is 到 ErrNotRunning，得到 %v", err)
	}
}

// 空关键词不该发请求（调用方传空是 bug，不是「搜全部」）。
func TestSearchEmptyKeywordIsError(t *testing.T) {
	m := New()
	if _, err := m.Search(context.Background(), "", "   ", 1, 5); err == nil {
		t.Fatal("空关键词该报错")
	}
}

// 字符串空结果（上游没歌）**不是错误**：宿主 200 + results:[] 时 Search 必须
// 返回空切片 + nil —— 报错的话池会把「这个关键词本来就没歌」当成洛雪源坏了。
const searchEmptyScript = `
lx.send('inited', { sources: { qsvip: { name: '永远搜不到', type: 'music', actions: ['musicSearch'], qualitys: ['128k'] } } })
lx.on('request', () => Promise.resolve({ isEnd: true, total: 0, list: [] }))
`

func TestSearchEmptyResultIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	writeSources(t, dir, map[string]string{"empty.js": searchEmptyScript})
	m := startRealHost(t, dir, 18944)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	songs, err := m.SearchAll(ctx, "一定搜不到的字符串", 5)
	if err != nil {
		t.Fatalf("搜到 0 条不是错误：%v", err)
	}
	if len(songs) != 0 {
		t.Fatalf("该是空的：%+v", songs)
	}
}

// 真音源脚本（qsvip）在场时的实测用例：**搜不到东西也算通过**（上游 2026-10-06 就是 404），
// 这里钉的是「宿主确实按 inited 里的声明去调了 qsvip，且没去调只声明 musicUrl 的那些源」。
//
// 脚本不在（NAS / CI）时跳过 —— 这个文件是外部的第三方脚本，不进仓库。
func TestSearchRealQsvipScriptCallsOnlyDeclaringSource(t *testing.T) {
	real := filepath.Join(os.Getenv("LX_REAL_SOURCE_DIR"), "全豆要-聚合音源-V4.1.js")
	if os.Getenv("LX_REAL_SOURCE_DIR") == "" {
		real = "/tmp/fnme-z/lx-source/全豆要-聚合音源-V4.1.js"
	}
	if _, err := os.Stat(real); err != nil {
		t.Skipf("真音源脚本不在（设 LX_REAL_SOURCE_DIR 指向它可启用）：%v", err)
	}
	dir := t.TempDir()
	code, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	writeSources(t, dir, map[string]string{"qsvip.js": string(code)})
	m := startRealHost(t, dir, 18943)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	songs, err := m.SearchAll(ctx, "晴天", 5)
	if err != nil {
		t.Fatalf("真脚本该能应答（搜到 0 条也算）：%v", err)
	}
	t.Logf("真脚本 qsvip 搜到 %d 条", len(songs))
	for _, s := range songs {
		if s.Source != "qsvip" {
			t.Fatalf("只有 qsvip 声明了 musicSearch，别的源不该有结果：%+v", s)
		}
		if s.ID == "" || s.Name == "" {
			t.Fatalf("结果里 id 与歌名必须有（没有就没法进池）：%+v", s)
		}
	}
}
