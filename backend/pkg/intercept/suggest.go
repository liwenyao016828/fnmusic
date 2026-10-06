package intercept

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

// 搜索联想（官方 `GET /music/api/v1/search/suggest`）的在线词合并。
//
// # 为什么值得认领这条路
//
// 官方 App 的搜索框**每次按键**都会打这条接口，而它只认官方库。用户想找
// 「飞牛本地没有、但在线源里有」的歌时，打字过程中不会有任何提示，只能把
// 关键词打全了再去搜索页看 —— 而搜索页的第一屏还有解析直链的等待。
// 把在线源的前几条歌名并进官方结果，输入框下拉里就能直接出现在线歌名。
//
// # 与官方「搜索合并」的预算不能混（见 handleSearch）
//
// `handleSearch` 是用户**点一下**在等的操作，预算 8 秒（onlineSearchBudget）。
// 这条是**每次按键**都要发的请求 —— 多等 100ms 就写在手感上。所以这里的策略是：
//
//	官方那一份照旧先拿到先返回；在线那一份只并入「预算内已经回来的」，
//	超时 / 失败一律当作「这次没有」，**既不拖慢响应，更不会变成错误或空白**。
//
// # 响应形状（本文件最关键的前提）
//
// 官方 suggest 的 `data` 是**字符串数组**（联想词本身），合并 = 往数组尾部追加。
// 这个形状取自参考实现 fnme-z 的用例（`proxy/tests/test_merge.py` 的
// `test_search_suggest_merge`：`{"code":0,"msg":"ok","data":["本地周杰伦"]}`），
// 那是同一个上游（proxied 的 `trim_music` unix socket）的代理实现所依赖的形状。
//
// ⚠️ **本机没有拿到真机抓包**：官方页面只在 NAS 上可达（开发机能连的只有
// 曲率的 8898 / 旧版 8899 / AI 网关 20128），所以这个形状是**二手**的。
// 正因为可能不对，这里**不按猜的形状去改任何响应**：
//
//   - 只有「HTTP 状态码可接受 + 是 JSON 对象 + `code == 0` + `data` 恰好是
//     全是字符串的数组」才合并；
//   - 其余一切情况（非 JSON / `code != 0` / `data` 是对象 / `data` 里混了
//     非字符串）**逐字节原样透传**；
//   - 形状不认识时打一条**一次性**诊断日志，把实际类型（以及数组元素如果是
//     对象，它的键名）记下来 —— 下次照着实测结果改，而不是继续猜。
//
// 这样最坏情况是「在线词没进去」，而不是「官方联想被我们搞坏了」。

// suggestOnlineBudget 是「等在线那一路」的上限。
//
// 它是**变量**不是常量：用例要把它调小（否则一条超时用例就要真等 0.8 秒，
// 而且还得让假搜索器一起等）—— 与 onlineSearchBudget 同一个理由。
//
// 为什么是 800ms：在线搜索要过全局出站节流（pkg/search 的默认最小间隔
// 300ms）再打第三方接口，快的平台几百毫秒能回来；再长就会让「边打字边等」
// 变成明显卡顿。宁可少几个在线词，也不许拖慢输入框。
var suggestOnlineBudget = 800 * time.Millisecond

// suggestOnlineMax 是最多并入几条在线词。
//
// 取参考实现的 5：下拉本来就只显示几条，塞更多只会把官方词挤下去。
const suggestOnlineMax = 5

// suggestPerSource 是每个平台问几条候选（只用来取歌名，越多越慢）。
const suggestPerSource = 8

// handleSuggest 接管官方搜索联想：官方词在前、在线词补在后。
//
// 返回 false 的情况**没有**：路径一旦命中就一定会写出响应（最坏是原样透传）。
func (i *Interceptor) handleSuggest(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	keyword := strings.TrimSpace(firstNonEmpty(q.Get("keyword"), q.Get("q"), q.Get("query")))

	// 在线那一路与官方转发**并发**跑：串行的话在线源再快也要在官方往返之后
	// 才开始，白白吃掉预算（参考实现也是先起任务再转发）。
	//
	// 没有可用源（或没有关键词）时**一个请求都不发** —— 见 suggestSources。
	var titlesCh chan []string
	if keyword != "" {
		if plats := i.suggestSources(); len(plats) > 0 {
			titlesCh = make(chan []string, 1)
			deadline := time.Now().Add(suggestOnlineBudget)
			go func() {
				// 缓冲 1：预算用完之后这个 goroutine 仍会被调用方丢弃，
				// 但结果能写进 channel 而不会永久阻塞（不会漏 goroutine）。
				titlesCh <- i.suggestOnline(keyword, plats, deadline)
			}()
		}
	}

	obj, raw, status, ct, ok := i.upstreamJSON(r, nil)
	if !ok {
		// 上游返回非 JSON / 业务码非 0 / 转发失败 —— 全部原样透传。
		// 这正是官方自己说的「登录失效」，改一个字都是错的。
		writeRaw(w, raw, status, ct)
		return true
	}

	official, isStringList := asStringList(obj["data"])
	if !isStringList {
		i.noteSuggestShape(obj["data"])
		writeRaw(w, raw, status, ct)
		return true
	}

	extra := i.waitSuggest(titlesCh)
	if len(extra) == 0 {
		// 没有可加的在线词 → 连序列化都不做，**逐字节原样**返回官方那份。
		// 官方联想是用户已经在依赖的东西，能不动就不动。
		writeRaw(w, raw, status, ct)
		return true
	}

	obj["data"] = mergeSuggestWords(official, extra)
	// 状态码按官方成功的形状归一成 200（与 handleSearch 一致）：走到这里说明
	// 上游是「HTTP 成功 + code == 0」的信封，没有别的语义要保留。
	writeJSON(w, http.StatusOK, obj)
	return true
}

// suggestSources 返回「这次联想可以去问的在线源」。
//
// ⚠️ 与 `platforms()`（搜索合并用）**故意不同**：那边在没有配置时会回落到
// 内置默认（网易 + QQ），因为它服务的是用户明确点下的搜索，多几个源是好事；
// 这条是每次按键都要发的请求，**配置明确说了「没有源」就不该再回落去发请求**。
// 所以：PlatformsFunc 存在时以它的返回值为准（空就是空），不存在时才用静态列表。
func (i *Interceptor) suggestSources() []string {
	if i.cfg.PlatformsFunc != nil {
		return i.cfg.PlatformsFunc()
	}
	if len(i.platformsStatic) > 0 {
		return i.platformsStatic
	}
	return nil
}

// waitSuggest 等在线那一路，超时返回 nil。
//
// 超时不是错误：官方结果照常返回，只是这次没有在线词。
func (i *Interceptor) waitSuggest(ch chan []string) []string {
	if ch == nil {
		return nil
	}
	select {
	case titles := <-ch:
		return titles
	case <-time.After(suggestOnlineBudget):
		i.logf("[INTERCEPT] 搜索联想：在线源超过 %s 没回来，这次只回官方结果", suggestOnlineBudget)
		return nil
	}
}

// suggestOnline 取在线源的歌名，按平台顺序累积，到点立刻停。
//
// 顺序而不是并发，是**故意的**：搜索层有全局出站节流（pkg/search 的
// `throttle`，默认最小间隔 300ms），并发打 N 个平台 = 每个按键都往节流队列里
// 塞 N 个名额，会把「用户真正点下去的搜索」挤到后面。顺序跑最多只有一个在途请求。
//
// `i.searcher` 是同步的、没有 ctx（与 collectOnline 同一处限制），所以超时只
// 表现为「这次不用它的结果」，那个调用仍会跑完 —— 用 deadline 在两次平台之间
// 检查，把它们限制在预算内。
func (i *Interceptor) suggestOnline(keyword string, plats []string, deadline time.Time) []string {
	out := make([]string, 0, suggestOnlineMax)
	seen := make(map[string]bool, suggestOnlineMax)

	for _, platform := range plats {
		if time.Now().After(deadline) {
			break
		}
		for _, s := range i.searcher(keyword, platform, 1, suggestPerSource) {
			title := strings.TrimSpace(s.Name)
			if title == "" {
				continue
			}
			key := suggestWordKey(title)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, title)
			if len(out) >= suggestOnlineMax {
				return out
			}
		}
	}
	return out
}

// mergeSuggestWords 把在线词并到官方词**后面**，并按归一化后的形式去重。
//
// 三条规矩（顺序即优先级）：
//  1. 官方词一条不少、顺序不动 —— 它是官方自己算出来的，我们只是往外加东西；
//  2. 在线词若与官方已有的词重合（忽略大小写与空白差异）就不重复出现；
//  3. 在线词之间同样去重（不同平台常给出同一个歌名）。
//
// 官方词**原样保留**（不 trim、不去重）：官方给什么就回什么，改它没有任何好处，
// 只会让「官方改了响应的表达方式」时我们的输出跟着变。
func mergeSuggestWords(official, extra []string) []string {
	seen := make(map[string]bool, len(official)+len(extra))
	out := make([]string, 0, len(official)+len(extra))
	for _, w := range official {
		out = append(out, w)
		if k := suggestWordKey(w); k != "" {
			seen[k] = true
		}
	}
	for _, w := range extra {
		title := strings.TrimSpace(w)
		key := suggestWordKey(title)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, title)
	}
	return out
}

// suggestWordKey 是去重用的键：忽略大小写、首尾与中间的空白差异。
//
// 只做这两种归一化：联想词是**歌名/歌手名**，把标点也抹掉会让
// 「海阔天空 (Live)」与「海阔天空(Live)」之外的东西也撞在一起 ——
// 与 `titleArtistKey` 的取舍不同，那个键要匹配「同一首歌」，这个只要不重复。
func suggestWordKey(w string) string {
	w = strings.ToLower(strings.TrimSpace(w))
	if w == "" {
		return ""
	}
	return strings.Join(strings.Fields(w), " ")
}

// asStringList 判断 data 是不是「全是字符串的数组」。
//
// 用 json.Number 语义的 decoder 解出来的数组元素是 any，所以逐个判类型。
// 空数组也算（官方没给联想词时就是它）—— 那种情况合并之后正好变成在线词。
func asStringList(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// noteSuggestShape 记下「官方 suggest 的 data 不是字符串数组」这件事，只记一次。
//
// 存在的理由：这个形状是**从参考实现抄来的**（见文件头）。真机上要是对不上，
// 唯一的线索就在这里 —— 有了它，下一次改动是「照实测形状适配」，
// 没有它就只能继续猜。**只打类型与键名，不打值**（联想词=用户正在输入的词）。
func (i *Interceptor) noteSuggestShape(v any) {
	i.suggestShapeOnce.Do(func() {
		kind := "null"
		detail := ""
		switch t := v.(type) {
		case nil:
			kind = "null"
		case []any:
			kind = "array"
			if len(t) > 0 {
				if m, ok := t[0].(map[string]any); ok {
					keys := make([]string, 0, len(m))
					for k := range m {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					detail = "；首元素是对象，键名 " + strings.Join(keys, "/")
				} else {
					detail = "；首元素类型 " + jsonTypeName(t[0])
				}
			}
		default:
			kind = jsonTypeName(t)
		}
		i.logf("[INTERCEPT] 搜索联想：官方 data 的形状是 %s%s —— "+
			"与「全字符串数组」不符，这次**不合并**、原样透传。"+
			"（形状取自参考实现的用例，没有真机抓包；按这条日志实测后再适配）", kind, detail)
	})
}

// jsonTypeName 把 JSON 值的类型说成人话（诊断日志用）。
func jsonTypeName(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}
