package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUIPrefsMergeAndPersist(t *testing.T) {
	dir := t.TempDir()
	m := NewUIPrefsManager(dir)

	if len(m.All()) != 0 {
		t.Fatalf("新目录应为空记忆，实际 %d 个", len(m.All()))
	}

	if err := m.Merge(map[string]json.RawMessage{
		"discover_tab":    json.RawMessage(`"charts"`),
		"search_platform": json.RawMessage(`"all"`),
	}); err != nil {
		t.Fatalf("Merge 失败: %v", err)
	}

	// 重新加载：应从文件恢复
	m2 := NewUIPrefsManager(dir)
	all := m2.All()
	if string(all["discover_tab"]) != `"charts"` {
		t.Fatalf("discover_tab 恢复失败: %v", all)
	}
	if string(all["search_platform"]) != `"all"` {
		t.Fatalf("search_platform 恢复失败: %v", all)
	}

	// 落盘文件本身应是合法 JSON，且可读
	data, err := os.ReadFile(filepath.Join(dir, UiPrefsFileName))
	if err != nil {
		t.Fatalf("读文件失败: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("落盘不是合法 JSON: %v", err)
	}
}

func TestUIPrefsRejectBadKey(t *testing.T) {
	dir := t.TempDir()
	m := NewUIPrefsManager(dir)

	bad := map[string]json.RawMessage{
		"../../evil":     json.RawMessage(`"x"`),
		"key with space": json.RawMessage(`"x"`),
		"":               json.RawMessage(`"x"`),
	}
	for k := range bad {
		if err := m.Merge(map[string]json.RawMessage{k: bad[k]}); err == nil {
			t.Fatalf("非法 key %q 应被拒绝", k)
		}
	}
	if len(m.All()) != 0 {
		t.Fatalf("非法写入不应污染内存: %v", m.All())
	}
	if _, err := os.Stat(filepath.Join(dir, UiPrefsFileName)); !os.IsNotExist(err) {
		t.Fatalf("非法写入不应产生文件")
	}
}

func TestUIPrefsCorruptFileIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, UiPrefsFileName), []byte("{broken"), 0644); err != nil {
		t.Fatal(err)
	}
	m := NewUIPrefsManager(dir)
	if len(m.All()) != 0 {
		t.Fatalf("损坏的文件应被忽略，实际: %v", m.All())
	}
}

func TestUIPrefsOversizeRejected(t *testing.T) {
	dir := t.TempDir()
	m := NewUIPrefsManager(dir)

	big := make([]byte, MaxUIPrefsBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	bigJSON, _ := json.Marshal(string(big))
	if err := m.Merge(map[string]json.RawMessage{"huge": bigJSON}); err == nil {
		t.Fatalf("超大记忆应被拒绝")
	}
	if _, err := os.Stat(filepath.Join(dir, UiPrefsFileName)); !os.IsNotExist(err) {
		t.Fatalf("超大写入不应产生文件")
	}
}
