package nas

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"fn-lx-player/pkg/library"
)

// 这一组用例盯的是「两套目录 walker 合并」这件事的**外部可见后果**：
// 浏览接口和曲库索引对同一棵树必须给出同一个答案，且「结果不完整」必须说出来。

func mkAudio(t *testing.T, path string, size int) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

type scanResponse struct {
	Code      int             `json:"code"`
	Dir       string          `json:"dir"`
	Cached    bool            `json:"cached"`
	Truncated bool            `json:"truncated"`
	Warnings  []string        `json:"warnings"`
	Total     int             `json:"total"`
	Songs     []LocalSong     `json:"songs"`
	Audit     json.RawMessage `json:"audit"`
}

func scanSongs(t *testing.T, dir string, refresh bool) scanResponse {
	t.Helper()
	target := "/api/nas/scan?dir=" + url.QueryEscape(dir)
	if refresh {
		target += "&refresh=1"
	}
	rec := httptest.NewRecorder()
	ScanSongs(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ScanSongs 状态 %d，响应：%s", rec.Code, rec.Body.String())
	}
	var out scanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败：%v（%s）", err, rec.Body.String())
	}
	return out
}

func songPaths(songs []LocalSong) []string {
	out := make([]string, 0, len(songs))
	for _, s := range songs {
		out = append(out, s.Path)
	}
	sort.Strings(out)
	return out
}

func warningsHave(list []string, needle string) bool {
	for _, w := range list {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}

// 浏览器接口必须跟索引一样跳排除目录、一样吃深度上限 —— 以前它两样都不做：
// 隐藏目录跳了，@eaDir/.git 这些照进，深度完全不管，而且读不了的目录不留痕。
func TestScanSongsUsesSharedEnumerationRules(t *testing.T) {
	root := realPath(t, t.TempDir())
	setAllowedRoots(t, root)

	mkAudio(t, filepath.Join(root, "ok.mp3"), 4)
	mkAudio(t, filepath.Join(root, "@eaDir", "thumb.mp3"), 4)
	mkAudio(t, filepath.Join(root, ".git", "blob.mp3"), 4)
	mkAudio(t, filepath.Join(root, ".hidden", "hidden.mp3"), 4)

	// 13 层嵌套：默认深度上限 12，底下的歌必须够不着，并且要说出来
	deep := root
	for i := 0; i < 14; i++ {
		deep = filepath.Join(deep, "d")
	}
	mkAudio(t, filepath.Join(deep, "deep.mp3"), 4)

	resp := scanSongs(t, root, true)

	if got := songPaths(resp.Songs); len(got) != 1 || !strings.HasSuffix(got[0], "ok.mp3") {
		t.Fatalf("只应枚举到 ok.mp3（排除目录/隐藏目录/超深目录都不该进），实际：%v", got)
	}
	if resp.Truncated {
		t.Error("没到数量上限时 truncated 应为 false（深度截断是另一回事）")
	}
	if !warningsHave(resp.Warnings, "目录层级超过上限") {
		t.Errorf("深度截断必须出现在 warnings 里：%v", resp.Warnings)
	}
	if resp.Total != len(resp.Songs) {
		t.Errorf("total = %d, 应为 %d", resp.Total, len(resp.Songs))
	}
	if len(resp.Audit) == 0 {
		t.Error("首次枚举应带 audit 对账信息")
	}
}

func TestScanSongsFlagsTruncationAndRemembersItInCache(t *testing.T) {
	root := realPath(t, t.TempDir())
	setAllowedRoots(t, root)

	for _, n := range []string{"a.mp3", "b.mp3", "c.mp3", "d.mp3", "e.mp3"} {
		mkAudio(t, filepath.Join(root, n), 2)
	}

	old := scanSongsMaxAudio
	scanSongsMaxAudio = 3
	t.Cleanup(func() { scanSongsMaxAudio = old })

	InitScanCache(t.TempDir())
	t.Cleanup(func() { InitScanCache("") })

	fresh := scanSongs(t, root, true)
	if len(fresh.Songs) != 3 || !fresh.Truncated {
		t.Fatalf("上限 3 时应返回 3 首且 truncated=true，实际 %d 首 truncated=%v", len(fresh.Songs), fresh.Truncated)
	}
	if !warningsHave(fresh.Warnings, "达到本次上限") {
		t.Errorf("命中上限必须有告警：%v", fresh.Warnings)
	}

	// 命中缓存时也得如实说「这份是截断的」，不能把 1000 首伪装成全部
	cached := scanSongs(t, root, false)
	if !cached.Cached {
		t.Fatal("第二次不带 refresh 应命中缓存")
	}
	if !cached.Truncated {
		t.Error("缓存里必须记住 truncated（否则缓存把截断洗成完整）")
	}
	if !warningsHave(cached.Warnings, "达到本次上限") {
		t.Errorf("缓存命中也要带上告警：%v", cached.Warnings)
	}
	if len(cached.Songs) != 3 {
		t.Errorf("缓存歌曲数 = %d, 期望 3", len(cached.Songs))
	}
}

// 合并的意义：同一棵树，索引和浏览器必须数出同一批文件。
func TestScanSongsAndLibraryIndexAgree(t *testing.T) {
	root := realPath(t, t.TempDir())
	setAllowedRoots(t, root)

	mkAudio(t, filepath.Join(root, "a.mp3"), 3)
	mkAudio(t, filepath.Join(root, "专辑", "b.flac"), 3)
	mkAudio(t, filepath.Join(root, "专辑", "lrc", "c.m4a"), 3)
	mkAudio(t, filepath.Join(root, "@eaDir", "skip.mp3"), 3)
	mkAudio(t, filepath.Join(root, "node_modules", "skip.mp3"), 3)
	mkAudio(t, filepath.Join(root, ".hidden", "skip.mp3"), 3)
	mkAudio(t, filepath.Join(root, "专辑", "cover.jpg"), 3)

	// 软链接环：两条 walker 都不能绕着它转
	if err := os.Symlink(filepath.Join(root, "专辑"), filepath.Join(root, "专辑", "self")); err != nil {
		t.Skipf("环境不支持软链接：%v", err)
	}

	indexed, audit := library.Scan([]string{root}, library.ScanOptions{})
	indexPaths := make([]string, 0, len(indexed))
	for p := range indexed {
		indexPaths = append(indexPaths, p)
	}
	sort.Strings(indexPaths)

	resp := scanSongs(t, root, true)
	browsePaths := songPaths(resp.Songs)

	if len(indexPaths) != 3 {
		t.Fatalf("索引应认到 3 首，实际 %d：%v", len(indexPaths), indexPaths)
	}
	if strings.Join(indexPaths, "|") != strings.Join(browsePaths, "|") {
		t.Fatalf("同目录两者枚举结果必须一致\n索引：%v\n浏览：%v", indexPaths, browsePaths)
	}
	if !audit.Complete {
		t.Errorf("这棵树是干净的，索引侧不该判不完整：%v", audit.Warnings)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("浏览侧也不该有告警：%v", resp.Warnings)
	}
}
