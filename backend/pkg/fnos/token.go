package fnos

import (
	"database/sql"
	"os"
	"strings"
	"sync/atomic"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，无需 CGO，可交叉编译
)

// musicDBPathDefault 为飞牛音乐库的**真实位置**。
//
// ⚠️ 这个值是从官方安装包（`trim.music-1.0.1.fpk`）的二进制里挖出来的，
// 不是猜的：`app/trim-music` 里 `music.db` 只出现一次，就是这个路径。
// 它与官方 `cmd/common` 的 `check_dir()` 引用的 `${TRIM_PKGVAR}/db` 一致
// （`TRIM_PKGVAR` = `/var/apps/trim.music/var`）。
//
// **踩过的坑**：这里以前写的是 `/var/lib/fnos-music-db/music.db`（来自
// docs/飞牛音乐API.md 的记载，该记载是错的）。真机上那个路径根本不存在 →
// 取不到令牌 → `Available()` 返回 false → `POST /api/fnos/check` 回 503
// 「飞牛音乐连接不上」。**这就是 503 的根因。**
const musicDBPathDefault = "/var/apps/trim.music/var/db/music.db"

// musicDBPath 为飞牛音乐应用的数据库；声明为变量以便测试注入。
var musicDBPath = musicDBPathDefault

// musicDBFallbackPaths 是历史上见过 / 文档提过的其它位置，作为兜底再试一遍。
// 实测 1.0.1 用的是 musicDBPathDefault；这里保留旧路径只为兼容不同版本，
// 命中不了也不影响（只是多一次 os.Stat）。
//
// ⚠️ 只在 musicDBPath 仍是默认值时才尝试回退，避免绕过测试注入的路径。
var musicDBFallbackPaths = []string{
	"/var/lib/fnos-music-db/music.db",
}

// manualToken 手工指定的令牌（故障排查用）。
// 由 API 层在启动时从配置注入，用户也可通过接口改。
// 用 atomic 是因为它可能被接口调用改、同时被下载/推送等并发读取。
var manualToken atomic.Value // string

// SetManualToken 设置手工令牌；传空串表示清除（回到自动读取）。
//
// 优先级：**手工 > 环境变量 FNOS_TOKEN > 飞牛数据库**。
// 手工排在最前，因为它的用途就是「自动读不到时先顶上」。
func SetManualToken(t string) {
	manualToken.Store(strings.TrimSpace(t))
}

func currentManualToken() string {
	if v, ok := manualToken.Load().(string); ok {
		return v
	}
	return ""
}

// MusicToken 返回飞牛音乐应用的可用登录令牌，取不到时返回空串。
//
// 顺序：
//  1. 手工指定（接口保存的，排障用）
//  2. 环境变量 FNOS_TOKEN（部署时覆盖）
//  3. 只读打开飞牛音乐库，取 user_token 表最新的非空令牌
//
// ⚠️ **第 3 条是「每次调用都现读」**，不是缓存 —— 所以飞牛自己刷新令牌后
// 我们下一轮就拿到新的，**天然就是自动续期**，不需要额外的续期机制。
//
// 该令牌仅在本进程内用于调用飞牛音乐本地接口，不对外发送、不写入日志。
func MusicToken() string {
	if v := currentManualToken(); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("FNOS_TOKEN")); v != "" {
		return v
	}
	return tokenFromMusicDB()
}

// TokenSource 报告当前令牌来自哪里，供「飞牛同步管理」页展示。
//
// 返回值：manual / env / db / none
func TokenSource() string {
	if currentManualToken() != "" {
		return "manual"
	}
	if strings.TrimSpace(os.Getenv("FNOS_TOKEN")) != "" {
		return "env"
	}
	if tokenFromMusicDB() != "" {
		return "db"
	}
	return "none"
}

// tokenFromMusicDB 以只读方式读取令牌。任何异常都退化为返回空串，
// 由调用方决定降级行为——绝不因读库失败影响应用其他功能。
func tokenFromMusicDB() string {
	return firstTokenFrom(MusicDBPaths())
}

// firstTokenFrom 依次尝试给定库文件，返回第一个读到的非空令牌。
func firstTokenFrom(paths []string) string {
	for _, p := range paths {
		if t := tokenFromDBFile(p); t != "" {
			return t
		}
	}
	return ""
}

// tokenFromDBFile 从单个库文件里读最新令牌；读不到返回空串。
func tokenFromDBFile(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	// mode=ro：只读打开，绝不改动飞牛音乐的数据
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return ""
	}
	defer db.Close()

	var token string
	err = db.QueryRow(
		`select token from user_token where token is not null and token <> '' order by id desc limit 1`,
	).Scan(&token)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(token)
}

// MusicDBPaths 返回**实际会尝试**的库路径列表，供排障信息展示。
func MusicDBPaths() []string {
	paths := []string{musicDBPath}
	if musicDBPath == musicDBPathDefault {
		paths = append(paths, musicDBFallbackPaths...)
	}
	return paths
}

// musicDBDiagnosis 逐条报告库路径的读取情况，供排障信息使用。
//
// 为什么值得单独写：真机上「拿不到令牌」有两种完全不同的原因 ——
// **库路径不对**（文件不存在）与**库在但读不出令牌**（没登录过 / 无读权限 /
// WAL 模式只读打开失败）。两者的处置方式完全不同，笼统报一句「没令牌」等于没说。
func musicDBDiagnosis() []string {
	return diagnoseDBPaths(MusicDBPaths())
}

func diagnoseDBPaths(paths []string) []string {
	lines := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			lines = append(lines, p+" = 文件不存在")
			continue
		}
		if tokenFromDBFile(p) != "" {
			lines = append(lines, p+" = 已读到令牌")
		} else {
			lines = append(lines, p+" = 文件存在但读不到令牌（未登录过 / 无读权限 / 不是预期结构）")
		}
	}
	return lines
}

// socketExists 飞牛音乐的服务 socket 是否存在。
func socketExists() bool {
	_, err := os.Stat(musicSocket)
	return err == nil
}

// UnavailableReason 用**一句话**说明飞牛音乐接口为什么不可用。
//
// ⚠️ 这句话会**原样显示在界面上**（账号连接 → 飞牛音乐连接），所以只写
// 「缺什么」这一层，**不写**库路径、socket 路径、环境变量名、接口地址 ——
// 那些用户改不了、也看不懂。
//
// 逐项诊断结果见 UnavailableDetail()，它只进日志：需要排查时到「日志」页看。
//
// 一切正常时返回空串（调用方自己决定兜底文案）—— 便于测试断言。
func UnavailableReason() string {
	noToken := MusicToken() == ""
	noSocket := !socketExists()
	switch {
	case noToken && noSocket:
		return "没有读到登录令牌，也找不到飞牛音乐服务"
	case noToken:
		return "没有读到登录令牌（飞牛音乐可能没登录过）"
	case noSocket:
		return "找不到飞牛音乐服务"
	}
	return ""
}

// UnavailableDetail 返回**逐项检查结果**，供日志使用（不要直接显示给用户）。
//
// 存在的意义：真机上「连不上」有好几种成因（socket 不对 / 库路径没读到令牌 /
// 文件存在但结构不符），笼统报一句等于没说。这里把每一项都写出来，
// 一次点击就能在日志里定位。
func UnavailableDetail() string {
	var missing []string

	if MusicToken() == "" {
		missing = append(missing,
			"没有可用的登录令牌（已依次尝试：手工令牌 / 环境变量 FNOS_TOKEN / 飞牛音乐库）"+
				"；逐条库路径检查结果："+strings.Join(musicDBDiagnosis(), "；"))
	}

	if !socketExists() {
		missing = append(missing, "飞牛音乐 socket 不存在："+musicSocket)
	}

	if len(missing) == 0 {
		return ""
	}
	return "飞牛音乐接口不可用：" + strings.Join(missing, "；")
}
