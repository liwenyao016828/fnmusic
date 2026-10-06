package nas

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── helpers ──

func setAllowedRoots(t *testing.T, roots ...string) {
	t.Helper()
	t.Setenv(allowedRootsEnv, strings.Join(roots, string(os.PathListSeparator)))
}

// realPath 返回解析软链接后的路径，避免 macOS 上 /tmp -> /private/tmp 之类的差异。
func realPath(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return real
}

func mustWrite(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", p, err)
	}
}

// ── AllowedRoots ──

func TestAllowedRootsEnvOverride(t *testing.T) {
	base := t.TempDir()
	rootA := filepath.Join(base, "rootA")
	rootB := filepath.Join(base, "rootB")
	for _, d := range []string{rootA, rootB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(base, "does-not-exist")

	setAllowedRoots(t, rootA, missing, rootB, rootA) // 不存在项与重复项都要被处理

	got := AllowedRoots()
	want := []string{realPath(t, rootA), realPath(t, rootB)}
	if len(got) != len(want) {
		t.Fatalf("AllowedRoots() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllowedRoots()[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}

	// 环境变量覆盖内置列表：内置的 /vol1 等不应再出现。
	for _, r := range got {
		if r == "/vol1" || r == "/vol2" {
			t.Fatalf("AllowedRoots() 泄漏了内置根目录: %v", got)
		}
	}
}

func TestAllowedRootsIgnoresFiles(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "not-a-dir")
	mustWrite(t, file, []byte("x"))
	setAllowedRoots(t, file)
	if got := AllowedRoots(); len(got) != 0 {
		t.Fatalf("文件不应作为 root 被接受，got %v", got)
	}
}

// ── ResolveSafePath ──

func TestResolveSafePathValid(t *testing.T) {
	root := t.TempDir()
	song := filepath.Join(root, "Music", "a.mp3")
	mustWrite(t, song, []byte("MP3"))
	setAllowedRoots(t, root)

	got, err := ResolveSafePath(song)
	if err != nil {
		t.Fatalf("ResolveSafePath(%q) 出错: %v", song, err)
	}
	if got != realPath(t, song) {
		t.Fatalf("ResolveSafePath(%q) = %q, want %q", song, got, realPath(t, song))
	}

	// root 自身
	if got, err := ResolveSafePath(root); err != nil || got != realPath(t, root) {
		t.Fatalf("root 自身应通过: got=%q err=%v", got, err)
	}
	// 含冗余分量的合法路径
	dirty := filepath.Join(root, "Music", "..", "Music", "a.mp3")
	if got, err := ResolveSafePath(dirty); err != nil || got != realPath(t, song) {
		t.Fatalf("规范化后的合法路径应通过: got=%q err=%v", got, err)
	}
}

func TestResolveSafePathRejectsDotDotEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	mustWrite(t, filepath.Join(root, "Music", "a.mp3"), []byte("MP3"))
	secret := filepath.Join(base, "secret.txt")
	mustWrite(t, secret, []byte("SECRET"))
	setAllowedRoots(t, root)

	cases := []string{
		filepath.Join(root, "..", "secret.txt"),
		filepath.Join(root, "Music", "..", "..", "secret.txt"),
		root + "/../../etc/passwd",
		root + "/./../../" + filepath.Base(secret),
	}
	for _, c := range cases {
		if got, err := ResolveSafePath(c); err == nil {
			t.Errorf("ResolveSafePath(%q) 应被拒绝，却返回 %q", c, got)
		} else if !errors.Is(err, ErrPathForbidden) {
			t.Errorf("ResolveSafePath(%q) 错误类型 = %v, want ErrPathForbidden", c, err)
		}
	}
}

func TestResolveSafePathRejectsPrefixConfusion(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "vol1") // 模拟 root = /vol1
	mustWrite(t, filepath.Join(root, "ok.mp3"), []byte("MP3"))
	sibling := filepath.Join(base, "vol10") // 模拟 /vol10，不应被 /vol1 匹配
	secret := filepath.Join(sibling, "secret.txt")
	mustWrite(t, secret, []byte("SECRET"))
	setAllowedRoots(t, root)

	if got, err := ResolveSafePath(secret); err == nil {
		t.Fatalf("前缀混淆路径应被拒绝，却返回 %q", got)
	} else if !errors.Is(err, ErrPathForbidden) {
		t.Fatalf("错误类型 = %v, want ErrPathForbidden", err)
	}
	if got, err := ResolveSafePath(filepath.Join(root, "ok.mp3")); err != nil {
		t.Fatalf("真实位于 root 内的文件应通过: %v (got %q)", err, got)
	}

	// isWithinRoot 按路径分段比较
	pairs := []struct {
		target, root string
		want         bool
	}{
		{"/vol1", "/vol1", true},
		{"/vol1/Music/a.mp3", "/vol1", true},
		{"/vol10", "/vol1", false},
		{"/vol10/Music", "/vol1", false},
		{"/vol1x", "/vol1", false},
		{"/vol1/../vol2", "/vol1", false},
		{"/etc/passwd", "/vol1", false},
		{"/vol1", "/vol1/Music", false},
	}
	for _, p := range pairs {
		if got := isWithinRoot(p.target, p.root); got != p.want {
			t.Errorf("isWithinRoot(%q, %q) = %v, want %v", p.target, p.root, got, p.want)
		}
	}
}

func TestResolveSafePathRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	mustWrite(t, secret, []byte("SECRET"))
	setAllowedRoots(t, root)

	// 软链接文件指向 root 之外
	linkFile := filepath.Join(root, "passwd.mp3")
	if err := os.Symlink(secret, linkFile); err != nil {
		t.Skipf("无法创建软链接，跳过: %v", err)
	}
	if got, err := ResolveSafePath(linkFile); err == nil {
		t.Fatalf("指向 root 外的软链接应被拒绝，却返回 %q", got)
	} else if !errors.Is(err, ErrPathForbidden) {
		t.Fatalf("软链接错误类型 = %v, want ErrPathForbidden", err)
	}

	// 软链接目录：词法上在 root 内，解析后逃逸
	linkDir := filepath.Join(root, "evil")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Skipf("无法创建软链接目录，跳过: %v", err)
	}
	if got, err := ResolveSafePath(filepath.Join(linkDir, "secret.txt")); err == nil {
		t.Fatalf("经软链接目录逃逸应被拒绝，却返回 %q", got)
	} else if !errors.Is(err, ErrPathForbidden) {
		t.Fatalf("软链接目录错误类型 = %v, want ErrPathForbidden", err)
	}

	// root 内的真实软链接（指向 root 内）应通过
	inside := filepath.Join(root, "Music", "real.mp3")
	mustWrite(t, inside, []byte("MP3"))
	insideLink := filepath.Join(root, "alias.mp3")
	if err := os.Symlink(inside, insideLink); err != nil {
		t.Skipf("无法创建软链接，跳过: %v", err)
	}
	if got, err := ResolveSafePath(insideLink); err != nil || got != realPath(t, inside) {
		t.Fatalf("root 内软链接应解析为真实路径: got=%q err=%v", got, err)
	}
}

func TestResolveSafePathRejectsRelativeAndEmpty(t *testing.T) {
	root := t.TempDir()
	setAllowedRoots(t, root)

	// 相对路径
	for _, p := range []string{"etc/passwd", "Music/a.mp3", "./a.mp3", "../a.mp3"} {
		if _, err := ResolveSafePath(p); !errors.Is(err, ErrPathNotAbsolute) {
			t.Errorf("ResolveSafePath(%q) 应返回 ErrPathNotAbsolute, got %v", p, err)
		}
	}

	// 空 / 纯空白
	for _, p := range []string{"", "   ", "\t"} {
		if _, err := ResolveSafePath(p); !errors.Is(err, ErrPathEmpty) {
			t.Errorf("ResolveSafePath(%q) 应返回 ErrPathEmpty, got %v", p, err)
		}
	}

	// NUL 注入
	if _, err := ResolveSafePath(root + "/a\x00b.mp3"); !errors.Is(err, ErrPathInvalid) {
		t.Errorf("含 NUL 的路径应返回 ErrPathInvalid")
	}
}

func TestResolveSafePathEmptyAllowedRootsFailClosed(t *testing.T) {
	root := t.TempDir()
	song := filepath.Join(root, "a.mp3")
	mustWrite(t, song, []byte("MP3"))

	setAllowedRoots(t, root)
	if _, err := ResolveSafePath(song); err != nil {
		t.Fatalf("前置条件失败，root 内路径本应通过: %v", err)
	}

	// 根目录列表为空 -> 拒绝一切（fail closed），而不是放行
	setAllowedRoots(t, "")
	if got := AllowedRoots(); len(got) != 0 {
		t.Fatalf("AllowedRoots() = %v, want empty", got)
	}
	for _, p := range []string{song, root, "/etc/passwd", "/"} {
		if got, err := ResolveSafePath(p); !errors.Is(err, ErrNoAllowedRoots) {
			t.Errorf("空 AllowedRoots 时 ResolveSafePath(%q) 应返回 ErrNoAllowedRoots, got %q / %v", p, got, err)
		}
	}
}

func TestResolveSafePathRejectsOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "a.mp3")
	mustWrite(t, outsideFile, []byte("MP3"))
	setAllowedRoots(t, root)

	if got, err := ResolveSafePath(outsideFile); err == nil {
		t.Fatalf("root 之外的绝对路径应被拒绝，却返回 %q", got)
	} else if !errors.Is(err, ErrPathForbidden) {
		t.Fatalf("错误类型 = %v, want ErrPathForbidden", err)
	}

	// 越权判断发生在存在性检查之前，不泄露文件是否存在
	if _, err := ResolveSafePath(outsideDir + "/nope-does-not-exist"); !errors.Is(err, ErrPathForbidden) {
		t.Errorf("root 外不存在的路径也应返回 ErrPathForbidden（不泄露存在性），got %v", err)
	}

	// root 内不存在的路径 -> NotFound（不是越权）
	if _, err := ResolveSafePath(filepath.Join(root, "missing.mp3")); !errors.Is(err, ErrPathNotFound) {
		t.Errorf("root 内不存在路径应返回 ErrPathNotFound, got %v", err)
	}
}

// ── HTTP handlers ──

func TestStreamAudioGuard(t *testing.T) {
	root := t.TempDir()
	audio := filepath.Join(root, "a.mp3")
	mustWrite(t, audio, []byte("ID3FAKEAUDIO"))
	note := filepath.Join(root, "notes.txt")
	mustWrite(t, note, []byte("hello"))
	outside := filepath.Join(t.TempDir(), "b.mp3")
	mustWrite(t, outside, []byte("OUTSIDE"))
	setAllowedRoots(t, root)

	decode := func(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
		t.Helper()
		var body map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是 JSON: %v (%q)", err, rec.Body.String())
		}
		return body
	}

	t.Run("拒绝 root 外文件", func(t *testing.T) {
		rec := httptest.NewRecorder()
		StreamAudio(rec, httptest.NewRequest(http.MethodGet, "/api/nas/stream?path="+outside, nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		if body := decode(t, rec); body["code"] != float64(403) {
			t.Fatalf("body = %v", body)
		}
	})

	t.Run("拒绝目录穿越", func(t *testing.T) {
		rec := httptest.NewRecorder()
		StreamAudio(rec, httptest.NewRequest(http.MethodGet, "/api/nas/stream?path="+root+"/../../etc/passwd", nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("拒绝空 path", func(t *testing.T) {
		rec := httptest.NewRecorder()
		StreamAudio(rec, httptest.NewRequest(http.MethodGet, "/api/nas/stream", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("拒绝相对路径", func(t *testing.T) {
		rec := httptest.NewRecorder()
		StreamAudio(rec, httptest.NewRequest(http.MethodGet, "/api/nas/stream?path=etc/passwd", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("拒绝非音频扩展名", func(t *testing.T) {
		rec := httptest.NewRecorder()
		StreamAudio(rec, httptest.NewRequest(http.MethodGet, "/api/nas/stream?path="+note, nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		if body := decode(t, rec); body["code"] != float64(403) {
			t.Fatalf("body = %v", body)
		}
	})

	t.Run("放行 root 内音频", func(t *testing.T) {
		rec := httptest.NewRecorder()
		StreamAudio(rec, httptest.NewRequest(http.MethodGet, "/api/nas/stream?path="+audio, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "audio/mpeg") {
			t.Fatalf("Content-Type = %q, want audio/mpeg", ct)
		}
		if rec.Body.String() != "ID3FAKEAUDIO" {
			t.Fatalf("body = %q", rec.Body.String())
		}
	})
}

func TestBrowseDirectoryGuardAndVolumes(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Music", "a.mp3"), []byte("MP3"))
	setAllowedRoots(t, root)

	t.Run("默认进入第一个 root 且 volumes 来自 AllowedRoots", func(t *testing.T) {
		rec := httptest.NewRecorder()
		BrowseDirectory(rec, httptest.NewRequest(http.MethodGet, "/api/nas/browse", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Code int        `json:"code"`
			Data BrowseData `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v (%q)", err, rec.Body.String())
		}
		if resp.Data.Current != realPath(t, root) {
			t.Fatalf("current = %q, want %q", resp.Data.Current, realPath(t, root))
		}
		if len(resp.Data.Volumes) != 1 || resp.Data.Volumes[0] != realPath(t, root) {
			t.Fatalf("volumes = %v, want [%q]", resp.Data.Volumes, realPath(t, root))
		}
		if resp.Data.Parent != "" {
			t.Fatalf("root 的 parent 应为空（/ 不在允许范围内），got %q", resp.Data.Parent)
		}
		if len(resp.Data.Folders) != 1 || resp.Data.Folders[0].Name != "Music" {
			t.Fatalf("folders = %+v", resp.Data.Folders)
		}
	})

	t.Run("拒绝列出根目录", func(t *testing.T) {
		rec := httptest.NewRecorder()
		BrowseDirectory(rec, httptest.NewRequest(http.MethodGet, "/api/nas/browse?path=/", nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
		}
	})

	t.Run("拒绝列出 root 外目录", func(t *testing.T) {
		rec := httptest.NewRecorder()
		BrowseDirectory(rec, httptest.NewRequest(http.MethodGet, "/api/nas/browse?path=/etc", nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
		}
	})
}

func TestScanSongsGuard(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Music", "a.mp3"), []byte("MP3"))
	setAllowedRoots(t, root)

	rec := httptest.NewRecorder()
	ScanSongs(rec, httptest.NewRequest(http.MethodGet, "/api/nas/scan?dir=/etc", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dir=/etc status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	ScanSongs(rec, httptest.NewRequest(http.MethodGet, "/api/nas/scan?dir="+filepath.Join(root, "Music"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("root 内目录 status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Total int         `json:"total"`
		Songs []LocalSong `json:"songs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || resp.Songs[0].Filename != "a.mp3" {
		t.Fatalf("songs = %+v", resp.Songs)
	}
}

func TestExtractCoverAndLyricGuard(t *testing.T) {
	root := t.TempDir()
	song := filepath.Join(root, "s.mp3")
	mustWrite(t, song, []byte("MP3"))
	mustWrite(t, filepath.Join(root, "s.lrc"), []byte("[00:01]hi"))
	setAllowedRoots(t, root)

	secret := filepath.Join(t.TempDir(), "secret.txt")
	mustWrite(t, secret, []byte("SECRET"))

	// cover: root 外 -> 403
	rec := httptest.NewRecorder()
	ExtractCover(rec, httptest.NewRequest(http.MethodGet, "/api/nas/cover?path="+secret, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cover status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
	}

	// lyric: root 外 -> 403 JSON
	rec = httptest.NewRecorder()
	HandleNasLyric(rec, httptest.NewRequest(http.MethodGet, "/api/nas/lyric?path="+secret, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("lyric status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("lyric 响应不是 JSON: %v", err)
	}
	if body["code"] != float64(403) {
		t.Fatalf("lyric body = %v", body)
	}

	// lyric: root 内同名 .lrc -> 200
	rec = httptest.NewRecorder()
	HandleNasLyric(rec, httptest.NewRequest(http.MethodGet, "/api/nas/lyric?path="+song, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("合法 lyric status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "[00:01]hi") {
		t.Fatalf("lyric body = %q", rec.Body.String())
	}
}

func TestSafeAudioPathUnifiedGuard(t *testing.T) {
	root := t.TempDir()
	mp3 := filepath.Join(root, "a.mp3")
	mustWrite(t, mp3, []byte("MP3"))
	setAllowedRoots(t, root)

	if got, err := safeAudioPath(mp3); err != nil || got != realPath(t, mp3) {
		t.Fatalf("safeAudioPath(root 内 mp3) = %q, %v", got, err)
	}

	outside := filepath.Join(t.TempDir(), "b.mp3")
	mustWrite(t, outside, []byte("MP3"))
	if _, err := safeAudioPath(outside); !errors.Is(err, ErrPathForbidden) {
		t.Fatalf("safeAudioPath(root 外) err = %v, want ErrPathForbidden", err)
	}

	flacOutside := filepath.Join(t.TempDir(), "c.flac")
	mustWrite(t, flacOutside, []byte("fLaC"))
	link := filepath.Join(root, "link.flac")
	if err := os.Symlink(flacOutside, link); err != nil {
		t.Skipf("无法创建软链接，跳过: %v", err)
	}
	if _, err := safeAudioPath(link); !errors.Is(err, ErrPathForbidden) {
		t.Fatalf("safeAudioPath(软链接逃逸) err = %v, want ErrPathForbidden", err)
	}

	if _, err := safeAudioPath("a.mp3"); !errors.Is(err, ErrPathNotAbsolute) {
		t.Fatalf("safeAudioPath(相对路径) err = %v, want ErrPathNotAbsolute", err)
	}
	if _, err := safeAudioPath(""); !errors.Is(err, ErrPathEmpty) {
		t.Fatalf("safeAudioPath(空) err = %v, want ErrPathEmpty", err)
	}
	if _, err := safeAudioPath(filepath.Join(root, "a.txt")); err == nil {
		t.Fatal("非音频扩展名应被拒绝")
	}
}
