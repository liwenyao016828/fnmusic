package downloadqueue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func raw(t *testing.T, s string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(s)) {
		t.Fatalf("测试数据不是合法 JSON：%s", s)
	}
	return json.RawMessage(s)
}

// sameJSON 语义比较。
//
// ⚠️ 别用字节比较：落盘走的是 `json.MarshalIndent`，会重新缩进，
// 字节必然不等 —— 而我们要守的是「字段和值原样保留」，不是空格长什么样。
func sameJSON(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("解析实际值失败：%v（原始：%s）", err, got)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("解析期望值失败：%v", err)
	}
	return reflect.DeepEqual(a, b)
}

// 核心判据：写进去、**重开一个 Store** 还能读出来。
// 这就是「换浏览器也能看到下载记录」的全部意义 —— 只测同进程内读回会漏掉落盘环节。
func TestReplacePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	first := `{"id":"t1","status":"success","song":{"name":"夜曲"}}`
	second := `{"id":"t2","status":"pending","song":{"name":"晴天"}}`
	if err := s.Replace([]json.RawMessage{raw(t, first), raw(t, second)}); err != nil {
		t.Fatalf("Replace 失败：%v", err)
	}

	got := NewStore(dir).All()
	if len(got) != 2 {
		t.Fatalf("重开后应有 2 条，实际 %d", len(got))
	}
	if !sameJSON(t, got[0], first) {
		t.Fatalf("第 1 条不一致：%s", got[0])
	}
	if !sameJSON(t, got[1], second) {
		t.Fatalf("第 2 条不一致：%s", got[1])
	}
}

// 后端**不解释**字段：前端加的新字段必须原样活下来。
// 这条守的是「以后前端改任务结构不用同时改后端」。
func TestUnknownFieldsSurvive(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	weird := `{"id":"t1","someFutureField":{"nested":[1,2,3]},"status":"paused"}`
	if err := s.Replace([]json.RawMessage{raw(t, weird)}); err != nil {
		t.Fatalf("Replace 失败：%v", err)
	}

	got := NewStore(dir).All()
	if len(got) != 1 || !sameJSON(t, got[0], weird) {
		t.Fatalf("未知字段没被原样保留：%s", got)
	}
}

// 没有文件时是空队列，不是错误。
func TestEmptyWhenNoFile(t *testing.T) {
	s := NewStore(t.TempDir())
	if got := s.All(); len(got) != 0 {
		t.Fatalf("应为空，实际 %d 条", len(got))
	}
}

// 文件损坏时**不能 panic、也不能让应用起不来** —— 队列丢了只是列表空。
func TestCorruptFileDegradesToEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "download_queue.json"), []byte("{不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if got := s.All(); len(got) != 0 {
		t.Fatalf("损坏文件应退化成空队列，实际 %d 条", len(got))
	}
	// 还能正常写入（覆盖掉坏文件）
	if err := s.Replace([]json.RawMessage{raw(t, `{"id":"t1"}`)}); err != nil {
		t.Fatalf("损坏文件后应仍可写入：%v", err)
	}
	if got := NewStore(dir).All(); len(got) != 1 {
		t.Fatalf("写入后应有 1 条，实际 %d", len(got))
	}
}

// All() 返回副本：调用方改了不该影响存储（否则前端一次误改就把内存里的队列改了）。
func TestAllReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Replace([]json.RawMessage{raw(t, `{"id":"t1"}`)}); err != nil {
		t.Fatal(err)
	}

	got := s.All()
	got[0] = raw(t, `{"id":"被改了"}`)

	if !sameJSON(t, s.All()[0], `{"id":"t1"}`) {
		t.Fatalf("All() 返回的切片被改动后影响了存储：%s", s.All()[0])
	}
}

// Replace(nil) 要写成空数组，不能写出 "tasks": null ——
// 前端拿到 null 会让 for...of 直接抛（`buildSubscriptions(null)` 就踩过这个坑）。
func TestReplaceNilWritesEmptyArray(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Replace(nil); err != nil {
		t.Fatalf("Replace(nil) 失败：%v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "download_queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if p.Tasks == nil {
		t.Fatalf("应落成空数组而不是 null：%s", data)
	}
	if len(p.Tasks) != 0 {
		t.Fatalf("应为空，实际 %d", len(p.Tasks))
	}
}

// 整体替换语义：第二次写必须**完全顶掉**第一次，不能残留。
func TestReplaceIsNotAppend(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	if err := s.Replace([]json.RawMessage{raw(t, `{"id":"a"}`), raw(t, `{"id":"b"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace([]json.RawMessage{raw(t, `{"id":"c"}`)}); err != nil {
		t.Fatal(err)
	}

	got := NewStore(dir).All()
	if len(got) != 1 || !sameJSON(t, got[0], `{"id":"c"}`) {
		t.Fatalf("整体替换应只剩 1 条 c，实际：%s", got)
	}
}
