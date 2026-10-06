package nas

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupReadRoot 造一个允许根目录，返回 (根目录, 设置函数)
func setupReadRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	setAllowedRoots(t, root)
	return root
}

func readReq(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/nas/read?path="+path, nil)
	rec := httptest.NewRecorder()
	ReadTextFile(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestReadTextFileReturnsContent(t *testing.T) {
	root := setupReadRoot(t)
	body := "/*! @name 测试音源 */ const a = 1\n"
	f := filepath.Join(root, "source.js")
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	code, out := readReq(t, f)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d: %v", code, out)
	}
	data := out["data"].(map[string]any)
	if data["content"] != body {
		t.Errorf("content 不对: %q", data["content"])
	}
	if data["name"] != "source.js" {
		t.Errorf("name 不对: %v", data["name"])
	}
}

func TestReadTextFileRejectsNonTextExtension(t *testing.T) {
	root := setupReadRoot(t)
	// 配置/密钥这类文件就躺在允许根目录里 —— 白名单就是拦这个的
	for _, name := range []string{"config.json.bak", "secret.pem", "db.sqlite", "binary.bin"} {
		f := filepath.Join(root, name)
		_ = os.WriteFile(f, []byte("x"), 0o644)
		if code, _ := readReq(t, f); code != http.StatusBadRequest {
			t.Errorf("%s 应被扩展名白名单拒绝，得到 HTTP %d", name, code)
		}
	}
	// .json / .txt 属于白名单，放行
	for _, name := range []string{"a.json", "b.txt", "c.mjs", "d.cjs"} {
		f := filepath.Join(root, name)
		_ = os.WriteFile(f, []byte("{}"), 0o644)
		if code, _ := readReq(t, f); code != http.StatusOK {
			t.Errorf("%s 应放行，得到 HTTP %d", name, code)
		}
	}
}

func TestReadTextFileRejectsOutsideRoots(t *testing.T) {
	setupReadRoot(t)
	code, _ := readReq(t, "/etc/hostname")
	if code != http.StatusForbidden {
		t.Fatalf("根目录外应 403，得到 HTTP %d", code)
	}
}

func TestReadTextFileRejectsMissingAndDir(t *testing.T) {
	root := setupReadRoot(t)
	if code, _ := readReq(t, filepath.Join(root, "nope.js")); code != http.StatusNotFound {
		t.Errorf("不存在应 404，得到 %d", code)
	}
	// 目录：名字没有白名单扩展名 → 先被扩展名检查拦下（400 也是对的）
	if code, _ := readReq(t, root); code != http.StatusBadRequest {
		t.Errorf("无扩展名的目录应 400，得到 %d", code)
	}
	// 名字像脚本、其实是目录 → 走 IsDir 分支，404
	dirLike := filepath.Join(root, "looks-like.js")
	if err := os.Mkdir(dirLike, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if code, _ := readReq(t, dirLike); code != http.StatusNotFound {
		t.Errorf("目录（哪怕名字像 .js）应 404，得到 %d", code)
	}
}

func TestReadTextFileRejectsTooLarge(t *testing.T) {
	root := setupReadRoot(t)
	f := filepath.Join(root, "big.js")
	if err := os.WriteFile(f, make([]byte, maxTextBytes+1), 0o644); err != nil {
		t.Fatalf("写大文件失败: %v", err)
	}
	code, out := readReq(t, f)
	if code != http.StatusBadRequest {
		t.Fatalf("超大文件应 400，得到 %d", code)
	}
	if !strings.Contains(out["message"].(string), "2MB") {
		t.Errorf("错误信息应说明上限: %v", out["message"])
	}
}

func TestReadTextFileRejectsNonUTF8(t *testing.T) {
	root := setupReadRoot(t)
	f := filepath.Join(root, "bin.js")
	if err := os.WriteFile(f, []byte{0xff, 0xfe, 0x00, 0x01}, 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	if code, _ := readReq(t, f); code != http.StatusBadRequest {
		t.Fatalf("非 UTF-8 应 400，得到 %d", code)
	}
}
