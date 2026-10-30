package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fn-lx-player/pkg/complete"
	"fn-lx-player/pkg/library"
)

// 2026-10-29 用户报「本地曲库删除歌曲…列表删除了，实际没有删除」的接口层回归：
//
//  ① 一个文件都没删掉（res.Failed > 0 且 moved+deleted == 0）时，Handler 以前照样
//     writeJSON(200) —— 前端只看 code 就当成成功，把行从列表里摘了。
//     现在必须回 500，让调用方没法把它当成功。
//  ② mode=trash 且回收站目录建不出来（权限/只读盘）时，必须**动手之前**就中止，
//     并说清楚是哪个目录、以及「没有删除任何文件」。
//
// 这里不改干净的 joinTrash：测试直接替换包的 trashDirFunc 来模拟「回收站不可写」。

// deleteTestServer 造一个足够 HandleNASDelete 跑起来的 Server：数据目录隔离在 t.TempDir，
// ai_backup 根也在里面（否则会写到真实的 <dataDir>/ai_backup）。
func deleteTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	return &Server{
		aiBackupRoot: filepath.Join(dir, "ai_backup"),
		libIndex:     library.NewIndex(dir),
	}
}

// allowedRootForTest 把 FN_ALLOWED_ROOTS 设成 dir，让 dir 下的临时文件过得了路径守卫。
// 注意 t.Setenv 不能和 t.Parallel 同用，这些用例都不并行。
func allowedRootForTest(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("FN_ALLOWED_ROOTS", dir)
}

// postDelete 发一次真实请求，返回 recorder 与解出来的 JSON 体。
func postDelete(t *testing.T, s *Server, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/nas/delete", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.HandleNASDelete(rec, req)
	var out map[string]any
	if b := rec.Body.Bytes(); len(b) > 0 {
		_ = json.Unmarshal(b, &out) // 非 JSON 就让下面按空体判定
	}
	return rec, out
}

// 全失败 → 500（而不是 200 + failed 计数）。
//
// 手法：trash 模式的回收站目录固定指向一个「父级是普通文件」的路径，
// MkdirAll 必然失败 → 走「回收站不可写，已中止」分支。
func TestNASDelete_AllFailedReturns500(t *testing.T) {
	s := deleteTestServer(t)
	root := t.TempDir()
	allowedRootForTest(t, root)

	song := filepath.Join(root, "a.mp3")
	if err := os.WriteFile(song, []byte("ID3testpayload"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 用一个普通文件当「父目录」，让 MkdirAll 一定失败
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := trashDirFunc
	trashDirFunc = func(string) string { return filepath.Join(blocker, ".tidy_trash") }
	t.Cleanup(func() { trashDirFunc = orig })

	rec, out := postDelete(t, s, map[string]any{"paths": []string{song}})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("全失败必须回 500，实得 %d，体：%s", rec.Code, rec.Body.String())
	}
	msg, _ := out["message"].(string)
	if !strings.Contains(msg, "没有删除任何文件") {
		t.Errorf("500 的提示要说清楚「没有删除任何文件」，实得：%q", msg)
	}
	// 文件必须原样还在 —— 「报错」和「没删」要同时成立
	if _, err := os.Stat(song); err != nil {
		t.Fatalf("中止时不该动文件，但 %s 不见了：%v", song, err)
	}
}

// 回收站建不出来时，提示里要带上那个目录 —— 否则用户只知道「失败」不知道去哪看。
func TestNASDelete_TrashDirReportedInError(t *testing.T) {
	s := deleteTestServer(t)
	root := t.TempDir()
	allowedRootForTest(t, root)

	song := filepath.Join(root, "b.mp3")
	if err := os.WriteFile(song, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(root, "blocker2")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(blocker, ".tidy_trash")
	orig := trashDirFunc
	trashDirFunc = func(string) string { return want }
	t.Cleanup(func() { trashDirFunc = orig })

	_, out := postDelete(t, s, map[string]any{"paths": []string{song}})
	msg, _ := out["message"].(string)
	if !strings.Contains(msg, want) {
		t.Errorf("提示里应包含回收站目录 %q，实得：%q", want, msg)
	}
}

// 正常路径：回收站可写 → 200，moved=1，文件真的从原位置消失（进了 .tidy_trash）。
// 这条是上面两条的对照，防止「为了报错把成功也砍了」。
func TestNASDelete_SuccessMovesFileAndReturns200(t *testing.T) {
	s := deleteTestServer(t)
	root := t.TempDir()
	allowedRootForTest(t, root)

	song := filepath.Join(root, "c.mp3")
	if err := os.WriteFile(song, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec, out := postDelete(t, s, map[string]any{"paths": []string{song}})
	if rec.Code != http.StatusOK {
		t.Fatalf("成功应回 200，实得 %d，体：%s", rec.Code, rec.Body.String())
	}
	data, _ := out["data"].(map[string]any)
	if data == nil {
		t.Fatalf("响应缺少 data：%s", rec.Body.String())
	}
	if moved, _ := data["moved"].(float64); moved != 1 {
		t.Errorf("moved 应为 1，实得 %v（体：%s）", data["moved"], rec.Body.String())
	}
	if failed, _ := data["failed"].(float64); failed != 0 {
		t.Errorf("failed 应为 0，实得 %v", data["failed"])
	}
	if _, err := os.Stat(song); !os.IsNotExist(err) {
		t.Fatalf("文件应已离开原位置，stat err = %v", err)
	}
	// 落在回收站里（trash_dir 下面），文件名保留
	trashDir, _ := data["trash_dir"].(string)
	if trashDir == "" {
		t.Fatalf("响应缺少 trash_dir：%s", rec.Body.String())
	}
	var found string
	_ = filepath.Walk(trashDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && info.Name() == "c.mp3" {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatalf("回收站 %s 里找不到 c.mp3", trashDir)
	}
}

// 顺带钉住：部分失败时，message 里不能再说「可在快照里恢复」（没删的文件本来就在）。
func TestRecoveryNoteWording(t *testing.T) {
	cases := []struct {
		mode   string
		failed int
		want   string
	}{
		{"trash", 0, "（可在「补全整理 → 数据快照」里恢复）"},
		{"delete", 0, "（备份在数据快照里）"},
		{"trash", 2, "（失败的文件没有任何改动）"},
		{"delete", 1, "（失败的文件没有任何改动）"},
	}
	for _, c := range cases {
		if got := recoveryNote(c.mode, c.failed); got != c.want {
			t.Errorf("recoveryNote(%q, %d) = %q，期望 %q", c.mode, c.failed, got, c.want)
		}
	}
}

// errTail 最多串 3 条，避免响应体变成日志。
func TestErrTailCapsAtThree(t *testing.T) {
	if got := errTail(nil); got != "" {
		t.Errorf("空错误列表应返回空串，实得 %q", got)
	}
	if got := errTail([]string{"a"}); !strings.Contains(got, "a") {
		t.Errorf("单条错误应被带出来，实得 %q", got)
	}
	got := errTail([]string{"a", "b", "c", "d", "e"})
	if strings.Contains(got, "d") || strings.Contains(got, "e") {
		t.Errorf("最多只带 3 条，实得 %q", got)
	}
	for _, want := range []string{"a", "b", "c"} {
		if !strings.Contains(got, want) {
			t.Errorf("前 3 条都该出现，缺 %q（实得 %q）", want, got)
		}
	}
}

// 编译期确认 complete 包仍被引用（备份设施是删除回滚的唯一手段，别哪天被悄悄摘掉）。
var _ = complete.NewBackup
