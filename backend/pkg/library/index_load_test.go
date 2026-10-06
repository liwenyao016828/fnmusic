package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fn-lx-player/pkg/applog"
)

// 索引文件坏掉/读不了时，以前是静默从空库开始：用户只看到「曲库一首歌都没有」，
// 真因（损坏、没权限）一个字都没有，而且下一次 Save 会把坏文件覆盖掉、事后无从查证。
// 见 HANDOVER §11 ㊼ 块九。

func loggedContaining(needle string) bool {
	for _, e := range applog.Default().Recent(100) {
		if strings.Contains(e.Message, needle) {
			return true
		}
	}
	return false
}

func TestCorruptIndexIsLoggedAndArchived(t *testing.T) {
	dir := t.TempDir()
	applog.Default().Clear()
	path := filepath.Join(dir, "library_index.json")
	if err := os.WriteFile(path, []byte("{这不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := NewIndex(dir)
	if idx.Len() != 0 {
		t.Fatalf("损坏的索引应当回退成空库，实际 %d 条", idx.Len())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("坏索引应被改名留档，但原文件还在（err=%v）", err)
	}
	matches, err := filepath.Glob(path + ".bad-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("期望留档 1 份 .bad-*，实际 %d 份：%v", len(matches), matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{这不是 JSON" {
		t.Fatalf("留档内容必须原样，实际 %q", string(data))
	}
	if !loggedContaining("索引文件损坏") {
		t.Fatalf("损坏没留日志：%+v", applog.Default().Recent(5))
	}
	if !loggedContaining(filepath.Base(matches[0])) {
		t.Fatal("日志里应写明留档文件名，否则事后找不到证据")
	}
}

func TestMissingIndexIsSilent(t *testing.T) {
	applog.Default().Clear()
	NewIndex(t.TempDir()) // 首启：文件不存在是正常情况
	if loggedContaining("索引") {
		t.Fatalf("首启不该记日志：%+v", applog.Default().Recent(5))
	}
}

// 索引能正常读回来时，既不留档也不报警（防止把「读成功」也当成异常）。
func TestHealthyIndexLoadsWithoutNoise(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "library_index.json"),
		[]byte(`{"version":1,"updated_at":0,"entries":{"/m/a.mp3":{"path":"/m/a.mp3","size":1}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	applog.Default().Clear()
	idx := NewIndex(dir)
	if idx.Len() != 1 {
		t.Fatalf("应读回 1 条，实际 %d 条", idx.Len())
	}
	if loggedContaining("索引") {
		t.Fatalf("正常加载不该有日志：%+v", applog.Default().Recent(5))
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "library_index.json.bad-*")); len(matches) != 0 {
		t.Fatalf("正常文件不该被留档：%v", matches)
	}
}
