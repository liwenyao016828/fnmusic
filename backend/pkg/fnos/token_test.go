package fnos

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeMusicDB 造一个与飞牛音乐库同构的最小 SQLite 库（含 user_token 表）。
func makeMusicDB(t *testing.T, tokens []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "music.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`create table user_token (id integer primary key autoincrement, token text)`,
		`insert into user_token (token) values (null)`,
		`insert into user_token (token) values ('')`,
	}
	for _, tok := range tokens {
		stmts = append(stmts, `insert into user_token (token) values ('`+tok+`')`)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("执行 %q 失败: %v", s, err)
		}
	}
	return path
}

func TestMusicTokenFromEnvWins(t *testing.T) {
	origPath := musicDBPath
	t.Cleanup(func() { musicDBPath = origPath })

	// 即便库里有令牌，环境变量也应优先
	musicDBPath = makeMusicDB(t, []string{"from-db"})
	t.Setenv("FNOS_TOKEN", "from-env")

	if got := MusicToken(); got != "from-env" {
		t.Fatalf("MusicToken() = %q, want from-env", got)
	}
}

func TestMusicTokenFromDBTakesLatestNonEmpty(t *testing.T) {
	origPath := musicDBPath
	t.Cleanup(func() { musicDBPath = origPath })

	// 表里含 null、空串、以及多个令牌；应取 id 最大者
	musicDBPath = makeMusicDB(t, []string{"older-token", "newest-token"})
	t.Setenv("FNOS_TOKEN", "")

	if got := MusicToken(); got != "newest-token" {
		t.Fatalf("MusicToken() = %q, want newest-token", got)
	}
}

func TestMusicTokenTrimsWhitespace(t *testing.T) {
	origPath := musicDBPath
	t.Cleanup(func() { musicDBPath = origPath })

	musicDBPath = makeMusicDB(t, []string{"  padded-token  "})
	t.Setenv("FNOS_TOKEN", "")

	if got := MusicToken(); got != "padded-token" {
		t.Fatalf("MusicToken() = %q, want padded-token", got)
	}
}

func TestMusicTokenMissingDBReturnsEmpty(t *testing.T) {
	origPath := musicDBPath
	t.Cleanup(func() { musicDBPath = origPath })

	musicDBPath = filepath.Join(t.TempDir(), "not-exist.db")
	t.Setenv("FNOS_TOKEN", "")

	if got := MusicToken(); got != "" {
		t.Fatalf("库不存在时应返回空串，实际 %q", got)
	}
}

func TestMusicTokenNoUsableRowsReturnsEmpty(t *testing.T) {
	origPath := musicDBPath
	t.Cleanup(func() { musicDBPath = origPath })

	musicDBPath = makeMusicDB(t, nil) // 只有 null 与空串
	t.Setenv("FNOS_TOKEN", "")

	if got := MusicToken(); got != "" {
		t.Fatalf("无可用令牌时应返回空串，实际 %q", got)
	}
}

func TestMusicTokenCorruptDBReturnsEmpty(t *testing.T) {
	origPath := musicDBPath
	t.Cleanup(func() { musicDBPath = origPath })

	// 写一个非 SQLite 内容，验证不会 panic、只是优雅返回空
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	musicDBPath = path
	t.Setenv("FNOS_TOKEN", "")

	if got := MusicToken(); got != "" {
		t.Fatalf("损坏库应返回空串，实际 %q", got)
	}
}

// ── 库路径回退 ──────────────────────────────────────────────
//
// 判据（为什么要有这些测试）：飞牛音乐库的位置**不止一个**，
// docs/飞牛音乐API.md 里就写了两个。代码曾经只试第一个，
// 结果在库位于第二个位置的机器上「取不到令牌」→ POST /api/fnos/check 回 503，
// 而界面上只显示「Request failed with status code 503」，完全没法定位。

// 这条路径是**从官方安装包 trim.music-1.0.1.fpk 的二进制里挖出来的**，
// 不是文档抄来的 —— 而文档（docs/飞牛音乐API.md）当时记错了，
// 导致真机上「取不到令牌 → POST /api/fnos/check 回 503」。
// 用测试钉死它，避免又被改回 /var/lib/fnos-music-db/music.db。
func TestMusicDBDefaultPathIsTheOneFromOfficialPackage(t *testing.T) {
	const want = "/var/apps/trim.music/var/db/music.db"
	if musicDBPathDefault != want {
		t.Fatalf("飞牛音乐库默认路径被改动了：%q，应为 %q（取自官方 FPK 二进制）", musicDBPathDefault, want)
	}
}

// 排障信息必须区分「文件不存在」与「文件在但读不到令牌」——
// 这两种原因在真机上的处置方式完全不同。
func TestMusicDBDiagnosisDistinguishesMissingFromUnreadable(t *testing.T) {
	t.Setenv("FNOS_TOKEN", "")

	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	empty := makeMusicDB(t, nil) // 文件在，但只有 null / 空串

	got := strings.Join(diagnoseDBPaths([]string{missing, empty}), "\n")
	if !strings.Contains(got, "文件不存在") {
		t.Fatalf("应报告文件不存在，实际 %q", got)
	}
	if !strings.Contains(got, "文件存在但读不到令牌") {
		t.Fatalf("应报告文件在但读不到令牌，实际 %q", got)
	}
}

func TestMusicDBDiagnosisReportsTokenFound(t *testing.T) {
	t.Setenv("FNOS_TOKEN", "")

	got := strings.Join(diagnoseDBPaths([]string{makeMusicDB(t, []string{"tok"})}), "\n")
	if !strings.Contains(got, "已读到令牌") {
		t.Fatalf("读到令牌时应如实报告，实际 %q", got)
	}
}

func TestMusicDBPathsIncludesFallbackWhenDefault(t *testing.T) {
	origPath := musicDBPath
	t.Cleanup(func() { musicDBPath = origPath })
	musicDBPath = musicDBPathDefault

	paths := MusicDBPaths()
	if len(paths) < 2 {
		t.Fatalf("默认路径下应同时尝试备用路径，实际只有 %v", paths)
	}
	if paths[0] != musicDBPathDefault {
		t.Fatalf("主路径必须排在最前，实际 %v", paths)
	}
}

func TestMusicDBPathsSkipsFallbackWhenInjected(t *testing.T) {
	origPath := musicDBPath
	t.Cleanup(func() { musicDBPath = origPath })
	musicDBPath = filepath.Join(t.TempDir(), "injected.db")

	// 注入路径时不得再回退到系统路径 —— 否则测试会读到真实机器上的库
	if got := MusicDBPaths(); len(got) != 1 || got[0] != musicDBPath {
		t.Fatalf("注入路径时不应回退，实际 %v", got)
	}
}

func TestFirstTokenFromFallsBackToLaterPath(t *testing.T) {
	t.Setenv("FNOS_TOKEN", "")

	missing := filepath.Join(t.TempDir(), "nope.db")
	real := makeMusicDB(t, []string{"from-fallback"})

	if got := firstTokenFrom([]string{missing, real}); got != "from-fallback" {
		t.Fatalf("第一个路径不存在时应回退，实际 %q", got)
	}
}

func TestFirstTokenFromPrefersEarlierPath(t *testing.T) {
	t.Setenv("FNOS_TOKEN", "")

	first := makeMusicDB(t, []string{"from-first"})
	second := makeMusicDB(t, []string{"from-second"})

	if got := firstTokenFrom([]string{first, second}); got != "from-first" {
		t.Fatalf("应优先靠前的路径，实际 %q", got)
	}
}

// ── 不可用原因要说清楚缺什么 ──────────────────────────────
//
// 判据：以前只回一句「请确认应用已安装到飞牛 fnOS」，真机上等于没说。
// 现在必须**具体指出**是缺令牌还是缺 socket，否则排查只能靠猜。
//
// ⚠️ 2026-09-18 起拆成两层（用户要求「界面只留一句人话，技术细节进日志」）：
//   · UnavailableReason() → 显示在界面上：只说缺什么，不含路径
//   · UnavailableDetail() → 只进日志：逐条库路径 / socket 路径
// 所以「列出路径」的断言落在 Detail 上，Reason 反而**不应**出现路径。

func TestUnavailableReasonNamesMissingSocket(t *testing.T) {
	origSock, origPath := musicSocket, musicDBPath
	t.Cleanup(func() { musicSocket, musicDBPath = origSock, origPath })

	musicSocket = filepath.Join(t.TempDir(), "nope.socket")
	musicDBPath = filepath.Join(t.TempDir(), "nope.db")
	t.Setenv("FNOS_TOKEN", "")
	SetManualToken("")
	t.Cleanup(func() { SetManualToken("") })

	reason := UnavailableReason()
	if !strings.Contains(reason, "服务") {
		t.Fatalf("原因里应指出飞牛音乐服务缺失，实际 %q", reason)
	}
	if !strings.Contains(reason, "令牌") {
		t.Fatalf("原因里应指出令牌缺失，实际 %q", reason)
	}
	// 界面上不能出现路径 —— 用户改不了 NAS 上的文件，写出来只是噪音
	if strings.Contains(reason, musicDBPath) || strings.Contains(reason, musicSocket) {
		t.Fatalf("界面文案不应包含路径，实际 %q", reason)
	}
	if len([]rune(reason)) > 40 {
		t.Fatalf("界面文案必须是一句话（<=40 字），实际 %d 字：%q", len([]rune(reason)), reason)
	}

	// 逐项诊断（含路径）走 Detail，进日志
	detail := UnavailableDetail()
	if !strings.Contains(detail, "socket") {
		t.Fatalf("详细诊断里应指出 socket 缺失，实际 %q", detail)
	}
	if !strings.Contains(detail, musicDBPath) {
		t.Fatalf("详细诊断里应列出尝试过的库路径，实际 %q", detail)
	}
}

func TestUnavailableDetailEmptyWhenEverythingPresent(t *testing.T) {
	origSock, origPath := musicSocket, musicDBPath
	t.Cleanup(func() { musicSocket, musicDBPath = origSock, origPath })

	sock := filepath.Join(t.TempDir(), "trim_music.socket")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	musicSocket = sock
	musicDBPath = makeMusicDB(t, []string{"tok"})
	t.Setenv("FNOS_TOKEN", "")
	SetManualToken("")
	t.Cleanup(func() { SetManualToken("") })

	if detail := UnavailableDetail(); detail != "" {
		t.Fatalf("令牌与 socket 都在时不应有诊断输出，实际 %q", detail)
	}
}

func TestUnavailableReasonEmptyWhenEverythingPresent(t *testing.T) {
	origSock, origPath := musicSocket, musicDBPath
	t.Cleanup(func() { musicSocket, musicDBPath = origSock, origPath })

	// 造一个真实存在的 socket 文件与库
	sock := filepath.Join(t.TempDir(), "trim_music.socket")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	musicSocket = sock
	musicDBPath = makeMusicDB(t, []string{"tok"})
	t.Setenv("FNOS_TOKEN", "")
	SetManualToken("")
	t.Cleanup(func() { SetManualToken("") })

	if reason := UnavailableReason(); strings.Contains(reason, "不可用") {
		t.Fatalf("令牌与 socket 都在时不应报不可用，实际 %q", reason)
	}
}

// 只读打开：读令牌不得改动源库（flow 同样以 mode=ro 打开）。
func TestMusicTokenDoesNotModifyDB(t *testing.T) {
	origPath := musicDBPath
	t.Cleanup(func() { musicDBPath = origPath })

	musicDBPath = makeMusicDB(t, []string{"tok"})
	t.Setenv("FNOS_TOKEN", "")

	before, err := os.ReadFile(musicDBPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = MusicToken()
	after, err := os.ReadFile(musicDBPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("读令牌改动了源库：%d -> %d 字节", len(before), len(after))
	}
}
