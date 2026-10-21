package lxnode

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SyncScripts 把音源脚本正文落到宿主要读的目录（`Dir/*.js`）。
//
// # 为什么必须有这一步
//
// 服务端宿主是一个 **Node 子进程**，它只会 `readFileSync(<Dir>/*.js)` —— 用户的脚本
// 正文却存在**配置**里（`config.CustomSource.Script`，因为前端要拿它去浏览器里执行）。
// 两边之间原先**没有任何桥**：`Config.Dir` 指着一个永远为空的目录。
//
// ⚠️ 2026-10-08 实测确认：在这之前没有任何代码往那个目录写过东西，于是
// 「服务端洛雪宿主」就算被打开也是**零脚本** —— 搜索恒空、洛雪候选永远进不了
// 下载源池。用户报的「我 lx 音乐怎么没有参与到下载」的最后一环就在这里。
//
// # 语义是「同步」而不是「追加」
//
// 先清掉目录里**我们管的** `*.js`，再按当前名单写一遍。不这么做的话，
// 用户删掉/改名的脚本会一直留在目录里继续被搜索 —— 那种「已经删了的源还在出结果」
// 只能靠人发现。
//
// 只清 `*.js`、不递归子目录、不碰其它后缀：这个目录是本应用自己的，
// 但「只删自己认得的」这条底线还是要守。
//
// scripts 的键是脚本名（用它拼文件名），值是正文。空正文会被跳过。
func SyncScripts(dir string, scripts map[string]string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("空的脚本目录")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// 1. 清掉旧的 *.js（只顶层、只这个后缀）
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".js") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}

	// 2. 按当前名单写一遍。排序只为让「同名冲突时谁覆盖谁」是确定的。
	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := scripts[name]
		if strings.TrimSpace(body) == "" {
			continue
		}
		file := filepath.Join(dir, safeScriptName(name))
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ScriptFileName 是脚本名 → 落盘文件名的**唯一**口径（对外导出）。
//
// 为什么必须对外导出：取链时要按名字告诉宿主「只用这几个脚本」，
// 而宿主只认落盘后的文件名。两边各写一遍转换规则，迟早不一致 ——
// 真机就撞上过：配置里叫 `全豆要[聚合音源]`，盘上是 `全豆要_聚合音源_.js`，
// 拼出来的 `全豆要[聚合音源].js` 在宿主里根本不存在。
func ScriptFileName(name string) string {
	return safeScriptName(name)
}

// safeScriptName 把脚本名变成安全的文件名。
//
// 脚本名是用户可编辑的（导入时能从脚本元信息里取到），里面可能有 `/`、`..`、
// 全角标点 —— 直接拼路径就会写到目录外面去（这是路径穿越，不是洁癖）。
// 认不出的字符统一换成 `_`，再兜一个长度上限。
func safeScriptName(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		s = "source"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r > 0x7f: // 中文等：保留（文件名合法，只是非 ASCII）
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		out = "source"
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out + ".js"
}
