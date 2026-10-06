package sources

import (
	"strings"
	"testing"
)

// 形状校验的用意：导入时挡掉「根本不是脚本」的内容。
//
// 背景：addSource 原先接受任意内容 —— 用户把网页 / git 地址导进来也「成功」，
// 运行时执行后解析出奇怪的 platform 键（实测出现过 'git'），
// 界面上表现为一个点了必然 400 的平台按钮。所以要在入口挡住。

func TestValidateScriptAcceptsLXCustomSource(t *testing.T) {
	ok, reason := ValidateScriptContent(fakeLXScript("正常音源"))
	if !ok {
		t.Fatalf("标准 LX 自定义源脚本应通过，实际被拒：%s", reason)
	}
}

// 落雪「薄壳」脚本：只声明 API_URL 转发给自建服务。这类脚本很短但确实有效，必须放行
func TestValidateScriptAcceptsThinShellScript(t *testing.T) {
	script := "/*! @name 薄壳音源 */\nconst API_URL = 'http://192.168.1.10:8080'\n" +
		strings.Repeat("// 填充\n", 40)
	ok, reason := ValidateScriptContent(script)
	if !ok {
		t.Fatalf("声明 API_URL 的薄壳脚本应通过，实际被拒：%s", reason)
	}
}

func TestValidateScriptAcceptsVariantMarkers(t *testing.T) {
	pad := strings.Repeat("// 填充内容\n", 40)
	for _, marker := range []string{
		"lx.on(", "lx.on (", "lx.EVENT_NAMES", "lx.request", "globalThis.lx", "window.lx",
	} {
		script := pad + marker + "'request', () => {})\n"
		if ok, reason := ValidateScriptContent(script); !ok {
			t.Errorf("含标识 %q 应通过，实际被拒：%s", marker, reason)
		}
	}
}

func TestValidateScriptRejectsHTML(t *testing.T) {
	cases := []struct{ name, content string }{
		{"doctype", "<!DOCTYPE html><html><head><title>x</title></head><body>hi</body></html>"},
		{"html 标签", "<html lang=\"zh\"><body>not a script</body></html>"},
	}
	for _, c := range cases {
		ok, reason := ValidateScriptContent(c.content + strings.Repeat(" ", 300))
		if ok {
			t.Errorf("%s: HTML 应被拒绝", c.name)
			continue
		}
		// 提示要说清「该填什么」，而不是只说「不对」
		if !strings.Contains(reason, "raw") {
			t.Errorf("%s: 应提示填 raw 直链，实际：%s", c.name, reason)
		}
	}
}

func TestValidateScriptRejectsTooShort(t *testing.T) {
	ok, reason := ValidateScriptContent("const a=1")
	if ok {
		t.Fatal("过短内容应被拒绝")
	}
	if !strings.Contains(reason, "过短") {
		t.Errorf("应说明过短，实际：%s", reason)
	}
}

func TestValidateScriptRejectsArbitraryText(t *testing.T) {
	// 够长，但没有任何 JavaScript 语法特征 —— 例如抓回来的 README 或纯文本
	content := strings.Repeat("这是一段普通的说明文字，不是脚本。\n", 40)
	ok, reason := ValidateScriptContent(content)
	if ok {
		t.Fatal("无 JavaScript 语法特征的内容应被拒绝")
	}
	if !strings.Contains(reason, "JavaScript") {
		t.Errorf("应说明缺少 JavaScript 语法特征，实际：%s", reason)
	}
}

// ⚠️ 回归测试：**混淆过的脚本源码里没有 `lx.on(` 这类明文**，必须放行。
//
// 判据（为什么要有这条）：曾经的做法是扫源码文本找 `lx.on(` / `lx.request` /
// `globalThis.lx` 等字面量，命中才通过。而真实音源大量经过 VM 混淆，
// 标识符是用 `\u006c\u0078`（"lx"）这种转义拼出来的 ——
// 用户提供的 `lx-music-source-v6 (修复).js` 七个标记**全部不命中**，
// 于是在导入环节就被拒，报「没有找到音源脚本的注册标识」，而它在别的播放器完全能用。
//
// 「是不是合格音源」应当由**运行时执行结果**判定（有没有 send('inited')、
// 有没有注册 request handler），参考实现觅音就是这么做的。
func TestValidateScriptAcceptsObfuscatedScript(t *testing.T) {
	// 构造一份「无任何明文标记」的最小混淆脚本：
	// 标识符靠字符串拼接 + 转义拼出来，运行时才成立
	script := `/*! @name 混淆音源 @version 6 */` + "\n" +
		`;(function(){var _a='\u006c\u0078';` + "\n" +
		`var _b='\u006f\u006e';` + "\n" +
		`var _c='\u0072\u0065\u0071\u0075\u0065\u0073\u0074';` + "\n" +
		`globalThis[_a][_b](_c,function(p){return Promise.resolve('https://x/'+p.source)});` + "\n" +
		`globalThis[_a].send('\u0069\u006e\u0069\u0074\u0065\u0064',{sources:{wy:{qualitys:['320k']}}});` + "\n" +
		strings.Repeat("// 混淆填充\n", 40) +
		`})();` + "\n"

	// 前提校验：这份脚本确实不含任何明文标记，否则测试就失去意义
	for _, marker := range []string{"lx.on(", "lx.request", "globalThis.lx", "window.lx", "lx.EVENT_NAMES"} {
		if strings.Contains(script, marker) {
			t.Fatalf("测试样本本身含有明文标记 %q，构造失败", marker)
		}
	}

	ok, reason := ValidateScriptContent(script)
	if !ok {
		t.Fatalf("混淆脚本（无明文标记）应通过导入校验，实际被拒：%s", reason)
	}
}

// 反过来：真脚本的判据不能松到「什么都放」。纯文本、网址、HTML 仍要挡住。
func TestValidateScriptRejectsBareURL(t *testing.T) {
	content := "https://github.com/lxmusics/lx-music-api-server/blob/main/README.md"
	ok, reason := ValidateScriptContent(content)
	if ok {
		t.Fatal("整段就是网址的内容应被拒绝")
	}
	if !strings.Contains(reason, "raw") {
		t.Errorf("应提示填 raw 直链，实际：%s", reason)
	}
}

func TestValidateScriptRejectsOversize(t *testing.T) {
	content := "function f(){return 1}" + strings.Repeat("// 填充\n", 300000)
	ok, reason := ValidateScriptContent(content)
	if ok {
		t.Fatal("超过 2 MB 的脚本应被拒绝")
	}
	if !strings.Contains(reason, "过大") {
		t.Errorf("应说明体积过大，实际：%s", reason)
	}
}

func TestValidateScriptRejectsEmpty(t *testing.T) {
	for _, s := range []string{"", "   ", "\n\n"} {
		if ok, _ := ValidateScriptContent(s); ok {
			t.Errorf("空白内容应被拒绝（%q）", s)
		}
	}
}

// git 地址这类内容会被当成普通文本拒绝 —— 正是用户反馈过的场景
func TestValidateScriptRejectsGitAddress(t *testing.T) {
	content := "git@github.com:someone/some-repo.git\n" + strings.Repeat("x", 300)
	if ok, _ := ValidateScriptContent(content); ok {
		t.Error("git 地址内容应被拒绝")
	}
}
