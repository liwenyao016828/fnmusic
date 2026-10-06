package sources

// 导入内容的形状校验。
//
// 为什么需要：`addSource` 原先接受**任意**内容 —— 用户把网页地址、git 地址、
// 甚至任意文本当成音源脚本导进来，都会「导入成功」。
// 之后运行时执行它，从里面解析出奇怪的 platform 键（实测出现过 `git`），
// 前端照着渲染成一个平台按钮，用户一点就是后端 400 —— 而错误信息里
// 只有一句「不支持的平台 "git"」，完全指不到「脚本本身是假的」。
// 所以要在入口挡住明显不是脚本的东西。
//
// ⚠️ **但这里刻意不再要求出现 `lx.on(` 之类的字面量。**
//
// 曾经的做法是扫源码文本，命中 `lx.on(` / `lx.request` / `globalThis.lx` 等
// 任一标记才算通过。这个判据**误杀了真实脚本**：大量音源经过 VM 混淆，
// 标识符是用 `\u006c\u0078`（= "lx"）这类转义拼出来的，源码里根本不出现明文
// `lx.on(`。实测用户提供的 `lx-music-source-v6 (修复).js` 七个标记**全部不命中**，
// 于是被拒在导入之外，报「没有找到音源脚本的注册标识」—— 而它在别的播放器里完全能用。
//
// **正确做法：交给运行时执行结果判定。**
// 脚本在沙箱里跑起来之后，看它有没有 `lx.send('inited')`、有没有
// `lx.on('request')` 注册 handler —— 这才是「是不是合格音源」的可靠判据。
// 参考实现觅音（miyin）就是这么做的（`loadLxSource`：执行 → 等 inited →
// 查 `handlers.length`），它整个运行时里**没有任何基于源码文本的音源识别逻辑**。
//
// 所以本函数只做粗筛，挡掉明显不是脚本的东西：
//   - 空白
//   - HTML（从网页地址抓回来的内容一定是这个形状）
//   - 过短（错误页 / 占位文本）
//   - 过大（> 2 MB，与觅音的上限一致）
//   - 整段就是一条网址
//   - 完全没有 JavaScript 语法特征（纯说明文字）

import "strings"

// minScriptBytes 体积下限：真正的脚本都有几 KB，几百字节的多半是错误页或占位文本。
// （体积上限用 manager.go 里已有的 maxScriptBytes，不重复定义。）
const minScriptBytes = 200

// jsSyntaxHints JavaScript 的粗特征。命中任一即认为「像代码」——
// 宁可漏放也不要误杀，真正的把关在运行时。
var jsSyntaxHints = []string{
	"function", "=>", "var ", "let ", "const ", "return", "if(", "if (", "for(", "for (", "{",
}

// ValidateScriptContent 判断内容是否**像一段 JavaScript 脚本**。
//
// 返回 (是否通过, 不通过的原因)。原因会原样回给调用方展示给用户，
// 所以要说人话、并给出下一步怎么做。
//
// 注意它**不判断**「是不是合格的音源脚本」—— 那要靠运行时执行（见文件头注释）。
func ValidateScriptContent(content string) (bool, string) {
	trimmed := strings.TrimSpace(content)

	if trimmed == "" {
		return false, "内容为空"
	}

	// HTML 判定放在长度判定**之前**：抓网页失败时拿到的往往是一段很短的错误页，
	// 若先判长度，用户看到的会是「内容过短」，而不是更有用的「你填的是网页地址」。
	head := strings.ToLower(trimmed[:min(len(trimmed), 512)])
	if strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html") ||
		strings.Contains(head, "<head>") || strings.Contains(head, "<body") {
		return false, "拿到的是网页而不是脚本 —— 请填脚本文件的 raw 直链（例如 GitHub 的 raw.githubusercontent.com 地址），不要填仓库或网页地址"
	}

	// 整段就是一条网址（没有空白分隔）—— 常见于把仓库地址粘进来。
	// ⚠️ 必须排在长度判定**之前**：一条网址往往只有几十字符，
	// 先判长度的话用户看到的是「内容过短」，而不是更有用的「你填的是网址」。
	if !strings.ContainsAny(trimmed, " \t\n\r") &&
		(strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")) {
		return false, "这是一条网址而不是脚本内容 —— 请填脚本文件的 raw 直链（例如 GitHub 的 raw.githubusercontent.com 地址），不要填仓库或网页地址"
	}

	if len(trimmed) < minScriptBytes {
		return false, "内容过短，不像音源脚本（可能抓到了错误页或占位内容）"
	}

	// 上限在 API 层（batch.go / manager.go）已经拦过一道，这里再兜一次底，
	// 保证 ValidateScriptContent 单独被调用时也不会放超大内容进去。
	if int64(len(trimmed)) > maxScriptBytes {
		return false, "音源脚本过大（超过 2 MB），拒绝导入"
	}

	// 落雪「薄壳」脚本：声明 API_URL 后把请求转发给自建服务。这类脚本本身很短但确实有效。
	if strings.Contains(content, "API_URL") {
		return true, ""
	}

	for _, h := range jsSyntaxHints {
		if strings.Contains(content, h) {
			return true, ""
		}
	}

	return false, "内容里没有任何 JavaScript 语法特征，不像音源脚本（可能是说明文字、仓库地址或抓取到的页面片段）。" +
		"提示：请填脚本文件的 raw 直链；脚本能否真正用起来，导入后会自动实测，以实测结果为准"
}
