package applog

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// disableSink 关掉落盘并注册清理，保证用例之间互不影响（sink 是全局单例）。
func disableSink(t *testing.T) {
	t.Helper()
	if err := EnableFile(""); err != nil {
		t.Fatalf("关闭落盘失败：%v", err)
	}
	t.Cleanup(func() { _ = EnableFile("") })
}

// 落盘行必须带日期与时区偏移：只写 "2006-01-02 15:04:05" 的 UTC 墙钟，
// 用户在中国看会差 8 小时，且跨天的记录分不清是哪一天。
func TestSinkWritesRFC3339(t *testing.T) {
	disableSink(t)
	dir := t.TempDir()
	if err := EnableFile(dir); err != nil {
		t.Fatalf("启用落盘失败：%v", err)
	}

	ts := time.Date(2026, 11, 3, 13, 30, 0, 0, time.UTC)
	sink.write(ts, "error", "backend", "写入失败：磁盘满")

	body := readFile(t, filepath.Join(dir, "qulv.log"))
	first := strings.SplitN(body, "\n", 2)[0]
	if !strings.HasPrefix(first, "2026-11-03T13:30:00Z ") {
		t.Errorf("落盘行应以 RFC3339 时间开头，实际 %q", first)
	}
	if !strings.Contains(first, "[error][backend]") {
		t.Errorf("落盘行应含 [级别][来源]，实际 %q", first)
	}
	// 时间必须能被无歧义解析回原时刻（带 offset 才做得到）。
	stamp := strings.Fields(first)[0]
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("时间列应为 RFC3339，实际 %q：%v", stamp, err)
	}
	if !parsed.Equal(ts) {
		t.Errorf("解析回的绝对时刻不一致：%s != %s", parsed, ts)
	}
}

// 一条日志里的换行不能把文件撑成多行，否则 grep/按行统计全乱。
func TestSinkFlattensNewlines(t *testing.T) {
	disableSink(t)
	dir := t.TempDir()
	if err := EnableFile(dir); err != nil {
		t.Fatalf("启用落盘失败：%v", err)
	}
	sink.write(time.Now(), "error", "backend", "第一行\n第二行")
	lines := strings.Split(strings.TrimRight(readFile(t, filepath.Join(dir, "qulv.log")), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("带换行的消息应压成 1 行，实际 %d 行：%q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "第一行 ⏎ 第二行") {
		t.Errorf("换行应替换成可见符号，实际 %q", lines[0])
	}
}

// 写盘失败不能静默：Status().LastErr 必须带上原因，
// 否则用户以为日志一直在记，等要导出时才发现文件根本没更新。
func TestStatusReportsWriteError(t *testing.T) {
	disableSink(t)
	dir := t.TempDir()
	if err := EnableFile(dir); err != nil {
		t.Fatalf("启用落盘失败：%v", err)
	}
	if st := Status(); st.LastErr != "" || !st.Enabled {
		t.Fatalf("刚启用时不该有错误，实际 %+v", st)
	}

	// 抽掉句柄下的文件：句柄仍是我们持有的，但写入会失败（或写到已 unlink 的 inode）。
	// 更稳妥的构造是把底层 fd 关掉，模拟「磁盘/文件句柄出问题」。
	sink.mu.Lock()
	_ = sink.f.Close()
	sink.mu.Unlock()

	sink.write(time.Now(), "info", "backend", "x")
	st := Status()
	if st.LastErr == "" {
		t.Fatal("写失败后 Status().LastErr 应带原因，实际为空（错误被吞掉了）")
	}
}

// 一次成功写入后，历史错误不应继续挂着 —— 否则界面会一直报错。
func TestStatusClearsErrorAfterSuccess(t *testing.T) {
	disableSink(t)
	dir := t.TempDir()
	if err := EnableFile(dir); err != nil {
		t.Fatalf("启用落盘失败：%v", err)
	}
	sink.mu.Lock()
	sink.lastErr = "历史错误"
	sink.mu.Unlock()

	sink.write(time.Now(), "info", "backend", "ok")
	if st := Status(); st.LastErr != "" {
		t.Errorf("写入成功后应清除历史错误，实际 %q", st.LastErr)
	}
}

// 换目录成功后，旧目录的错误不该继续显示。
func TestEnableFileResetsLastError(t *testing.T) {
	disableSink(t)
	sink.mu.Lock()
	sink.lastErr = "旧目录的错误"
	sink.mu.Unlock()
	if err := EnableFile(t.TempDir()); err != nil {
		t.Fatalf("启用落盘失败：%v", err)
	}
	if st := Status(); st.LastErr != "" {
		t.Errorf("成功启用后应清除历史错误，实际 %q", st.LastErr)
	}
}

// 关闭落盘后 Status 应如实说不启用，而不是还报一个路径。
func TestEnableFileEmptyDisables(t *testing.T) {
	disableSink(t)
	dir := t.TempDir()
	if err := EnableFile(dir); err != nil {
		t.Fatalf("启用落盘失败：%v", err)
	}
	if err := EnableFile(""); err != nil {
		t.Fatalf("关闭落盘失败：%v", err)
	}
	if st := Status(); st.Enabled || st.Path != "" {
		t.Errorf("关闭后应 Enabled=false 且 Path 为空，实际 %+v", st)
	}
	if got := DiskFiles(); got != nil {
		t.Errorf("关闭后不该返回落盘文件，实际 %v", got)
	}
}

// 导出要带上当前与上一代两个文件，顺序固定为 [当前, 上一代]。
func TestDiskFilesIncludesRotated(t *testing.T) {
	disableSink(t)
	dir := t.TempDir()
	if err := EnableFile(dir); err != nil {
		t.Fatalf("启用落盘失败：%v", err)
	}
	sink.write(time.Now(), "info", "backend", "当前")
	cur := filepath.Join(dir, "qulv.log")
	if err := os.WriteFile(cur+".1", []byte("上一代\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := DiskFiles()
	if len(got) != 2 || got[0] != cur || got[1] != cur+".1" {
		t.Fatalf("应返回 [当前, 上一代]，实际 %v", got)
	}
}

// AddDiskFile 的内容必须完整进包，且不吞掉读失败。
func TestAddDiskFile(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "qulv.log")
	body := "2026-11-03T13:30:00Z [info][backend] 第一条\n"
	if err := os.WriteFile(cur, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	n, truncated, err := AddDiskFile(zw, cur, "qulv.log")
	if err != nil {
		t.Fatalf("AddDiskFile 失败：%v", err)
	}
	if truncated {
		t.Error("小文件不该被标记截断")
	}
	if n != int64(len(body)) {
		t.Errorf("应写出 %d 字节，实际 %d", len(body), n)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "qulv.log" {
		t.Fatalf("包内应恰有一个 qulv.log，实际 %+v", zr.File)
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("包内内容不一致：%q", got)
	}
}

// 文件不存在时必须报错（由调用方写进 info.txt），而不是静默生成空条目。
func TestAddDiskFileMissing(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	defer func() { _ = zw.Close() }()
	if _, _, err := AddDiskFile(zw, filepath.Join(t.TempDir(), "nope.log"), "qulv.log"); err == nil {
		t.Error("文件不存在时应返回错误")
	}
}

// 超过单文件上限的文件只带前 maxFileBytes：防止外部塞进来的巨型日志把 NAS 内存吃光。
func TestReadDiskFileTruncatesHuge(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	// 稀疏文件：写 1 字节到 maxFileBytes+1 处，不真的占 20MB 磁盘。
	if _, err := f.WriteAt([]byte("x"), maxFileBytes); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	r, truncated, err := readDiskFile(p)
	if err != nil {
		t.Fatalf("readDiskFile 失败：%v", err)
	}
	defer func() { _ = r.Close() }()
	if !truncated {
		t.Error("超过上限的文件应标记 truncated")
	}
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatal(err)
	}
	if n > maxFileBytes {
		t.Errorf("最多读出 %d 字节，实际 %d", maxFileBytes, n)
	}
}

// 目录不是普通文件（比如有人把 qulv.log 做成了目录）时应报错而不是把整个卷读爆。
func TestReadDiskFileRejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := readDiskFile(dir); err == nil {
		t.Error("目录应被拒绝")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", p, err)
	}
	return string(b)
}
