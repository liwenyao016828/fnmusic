package ai

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// 每日推荐里的「大模型推荐层」：拿用户的收听 / 收藏当种子，让模型给出候选歌名。
//
// # 这里只负责「问」与「解析」，不负责「信」
//
// 模型给回来的**只有歌名 / 歌手这些文字**，没有任何平台 id。它可能编造不存在的歌，
// 也可能把同一首歌写成别的平台的写法。所以这一层的输出**不是**播放列表，只是
// 「候选词」—— 必须再走一遍搜索 + 匹配 + 可播过滤才能变成能播的歌
// （见 `pkg/intercept/vdaily_llm.go`）。这条分工是刻意的：模型不可信，搜索层可信。
//
// # 参考实现
//
// 行为对齐 `/tmp/fnme-z/proxy/recommend.py` 的 `build_llm_prompt`(:727) /
// `call_llm`(:824) / `parse_llm_recommendations`(:769)：种子按用户取、提示词里
// 明确禁止推荐已有的「歌名+歌手」、要求只回 JSON 数组、解析时容忍代码块与夹带文字。
// 差异见各处的「与参考实现不同」注释。

// 提示词与解析的规模上限。
const (
	// RecommendMaxRecent 是提示词里最多带几条「最近收听」。
	//
	// 与参考实现的 `SEED_LIMIT`(:43, 20) 一致。
	RecommendMaxRecent = 20
	// RecommendMaxFavorites 是提示词里最多带几条「收藏」。
	//
	// ⚠️ 与参考实现不同：它给 40（`recommend.py:1728` 的 `fav_seeds[:40]`），
	// 这里砍到 20。收藏与最近收听高度重叠（收藏过的往往刚听过），40 条换来的
	// 口味信号增量很小，却把**发出去的收听数据翻了一倍** —— 这一层发的是用户的
	// 收听历史，能少发就少发。20 条足够模型看出语种 / 曲风偏好。
	RecommendMaxFavorites = 20
	// RecommendDefaultCount 是调用方没指定条数时问模型要几条。
	RecommendDefaultCount = 10
	// RecommendMaxCount 是要模型最多给几条。
	RecommendMaxCount = 20
)

// RecommendSeed 是一条种子（用户听过或收藏过的一首歌）。
//
// ⚠️ **这三个字段就是发给模型的全部内容**（字段级说明见 `RecommendRequest`）。
type RecommendSeed struct {
	Title  string
	Artist string
	Album  string
}

// RecommendCandidate 是模型给出的一条候选。
//
// 全是**不可信的文字**：没有 id、没有时长、没有平台。调用方必须把它当搜索关键词，
// 不能当曲目。
type RecommendCandidate struct {
	Title  string
	Artist string
	Album  string
	Reason string
}

// RecommendRequest 是一次大模型推荐的输入。
//
// # 发给模型的确切内容（字段级）
//
// 只有下面这些，**没有**其它东西：
//
//	最近收听（≤ RecommendMaxRecent 条）：每条的 Title / Artist / Album
//	收藏    （≤ RecommendMaxFavorites 条）：每条的 Title / Artist / Album
//	以及 Count（拼进提示词里的一句「正好 N 首」）
//
// **不发**：曲目虚拟 id、平台内 id、播放时间戳（`PlayedAt`）、播放次数、平台名、
// 文件路径、用户标识（`userKey` / guid 后缀）、收藏时间、任何密钥或令牌。
//
// 也就是说：发出去的是「歌名 + 歌手 + 专辑名」这组公开元数据，**不是**「他什么时候
// 听的、听了多少次、在哪个平台听」。后者才是行为数据，前者是唱片封底上就有的东西。
type RecommendRequest struct {
	Recent    []RecommendSeed
	Favorites []RecommendSeed
	Count     int
}

// Recommend 让大模型按用户口味出候选。**任何异常都返回 nil，不返回 error。**
//
// 静默降级（与 `pkg/ai` 其它方法同一条规矩，见包注释）：未配置 → nil。
//
// ⚠️ 「解析不了也返回 nil」是**刻意**的，不是吞错。用户那台网关每次随机路由到
// 不同的免费模型，**偶尔返回空 content（非故障）**、并且忽略 `stream:false`
// 始终按 SSE 返回（见 HANDOVER §4.2 坑 B）。对调用方来说「这次没给东西」与
// 「给的东西没法用」是同一件事：这一层跳过，每日推荐照常交现有的两层结果。
// 所以这里没有 error 可以传 —— 有的话调用方也只能做同一件事。
//
// 用量照记（`Chat` 内部按 kind=`recommend` 记账）：空回复与失败也记一笔，
// 「没回明细就当没花钱」是明令禁止的。
func (c *Client) Recommend(ctx context.Context, req RecommendRequest) []RecommendCandidate {
	system := recommendSystemPrompt()
	user := buildRecommendPrompt(req)

	raw, _, err := c.Chat(ctx, "recommend", system, user)
	if err != nil {
		// 未配置 / 超时 / 空 content / 网关报错 —— 全都走这里。
		return nil
	}
	return parseRecommendCandidates(raw)
}

// recommendSystemPrompt 是系统提示词：只讲**输出契约**，不讲内容。
//
// 与 `BuildSystemPrompt` 分开写是因为契约不同（那边是 JSON 对象，这里是数组），
// 而 `BuildSystemPrompt` 的措辞是围绕「音乐库整理助手」来的，用在推荐上会串味。
func recommendSystemPrompt() string {
	return "你是音乐推荐引擎。你只输出一个合法的 JSON 数组，不要 markdown 代码块，" +
		"不要任何解释文字。"
}

// buildRecommendPrompt 拼用户提示词。
//
// 与参考实现（`recommend.py:727`）的差异：
//   - 不要求 `dimension` / `language` / `genre` / `type` 四个字段。参考实现用
//     `dimension` 强制多样性，但曲率这边**没有地方放这些字段**（`online.Track`
//     没有 genre，VO 也没有对应项）—— 要了也只能丢掉，纯属白花 token。
//     多样性的收益改用提示词里的一句话约束拿（见下第 3 条）。
//   - 不写 `来源:{source}`（参考实现会写 favorite/netease…）。那会顺带把「这首歌
//     是在哪个平台听的」也发出去，而它对推荐质量没有帮助。
//   - 明确要求「必须真实存在、能搜到」—— 参考实现也有这条（约束 2），
//     它同时是在替下游的搜索匹配省事。
func buildRecommendPrompt(req RecommendRequest) string {
	count := req.Count
	if count <= 0 {
		count = RecommendDefaultCount
	}
	if count > RecommendMaxCount {
		count = RecommendMaxCount
	}

	var b strings.Builder
	b.WriteString("你是音乐推荐引擎。根据下面这个用户的收听与收藏口味，给出 ")
	b.WriteString(strconv.Itoa(count))
	b.WriteString(" 首「他大概率会喜欢、但还没听过」的歌。\n\n")
	b.WriteString("要求：\n")
	b.WriteString("1. 每首歌必须**真实存在**，且是能在公开音乐平台上搜到的正式发行曲目 —— " +
		"不要编造歌名，不要给只在某个私人歌单里才有的冷门曲目。\n")
	b.WriteString("2. 不要推荐下面两个清单里已经出现过的「歌名 + 歌手」组合。\n")
	b.WriteString("3. 同一歌手最多 1 首；整体要覆盖不同的语种 / 曲风 / 年代，不要全是同一类。\n")
	b.WriteString("4. 正好 ")
	b.WriteString(strconv.Itoa(count))
	b.WriteString(" 条。\n")
	b.WriteString("5. 只输出 JSON 数组，不要 markdown 代码块，不要任何解释文字。\n")
	b.WriteString("6. 每一项的字段（全部是字符串）：\n")
	b.WriteString("   title  歌名（不要带书名号，不要带歌手名）\n")
	b.WriteString("   artist 歌手（只写一个主要歌手）\n")
	b.WriteString("   album  专辑名（不确定就留空串）\n")
	b.WriteString("   reason 一句话说明为什么推荐给他（不超过 30 字）\n\n")

	b.WriteString("最近收听：\n")
	b.WriteString(recommendSeedBlock(req.Recent, RecommendMaxRecent, "（暂无最近播放）"))
	b.WriteString("\n\n收藏：\n")
	b.WriteString(recommendSeedBlock(req.Favorites, RecommendMaxFavorites, "（暂无收藏）"))
	b.WriteString("\n")
	return b.String()
}

// recommendSeedBlock 把一个种子清单拼成编号列表。只写歌名 / 歌手 / 专辑三个字段。
func recommendSeedBlock(seeds []RecommendSeed, limit int, empty string) string {
	lines := make([]string, 0, limit)
	for _, s := range seeds {
		if len(lines) >= limit {
			break
		}
		title := strings.TrimSpace(s.Title)
		if title == "" {
			continue
		}
		artist := strings.TrimSpace(s.Artist)
		if artist == "" {
			artist = "未知"
		}
		album := strings.TrimSpace(s.Album)
		if album == "" {
			album = "-"
		}
		lines = append(lines, strconv.Itoa(len(lines)+1)+". 《"+title+"》 / "+artist+" / 专辑:"+album)
	}
	if len(lines) == 0 {
		return empty
	}
	return strings.Join(lines, "\n")
}

// ── 解析 ────────────────────────────────────────────────────────────────

// recommendFenceRe / recommendArrayRe 是两层兜底：模型经常在 JSON 外面套代码块
// 或者加一句「好的，这是推荐：」。与 `ParseJSONObject` 的三层兜底同一条思路。
var (
	recommendFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")
	recommendArrayRe = regexp.MustCompile(`(?s)\[.*\]`)
)

// recommendListKeys 是「模型把数组包在对象里」时要往下钻的键名。
//
// 与参考实现 `parse_llm_recommendations`(:792) 的键名一致，另加 `list`。
var recommendListKeys = []string{"recommendations", "songs", "tracks", "items", "data", "list"}

// recommendTitleCleaner 剥掉模型给歌名时爱加的装饰。
//
// 提示词里种子是写成 《歌名》 的，模型很可能照抄书名号回来。
var recommendTitleCleaner = strings.NewReplacer(
	"《", "", "》", "", "「", "", "」", "", `"`, "", `“`, "", `”`, "", `'`, "", "`", "",
)

// parseRecommendCandidates 宽容地把模型回复解析成候选清单。
//
// **宽容 ≠ 信任**：这里只保证「能读出来的都读出来」，读出来的东西一律还是
// 不可信的文字，调用方必须过搜索匹配那一套。
//
// 覆盖的形态：
//   - 空回复                     → nil
//   - ```json [...] ``` 代码块    → 剥出来再解
//   - 前后夹带解释文字            → 正则抓第一个 `[...]`
//   - 包在对象里（`{"songs":[]}`）→ 按 recommendListKeys 往下钻
//   - 元素是字符串而不是对象       → 当成只有歌名的候选
//   - 字段名换了（name/singer…）   → 按别名取
//   - 歌名带书名号 / 引号          → 剥掉
//   - 重复的「歌名+歌手」          → 去重
//   - 条数超了                    → 截到 RecommendMaxCount
//
// 一条都读不出来时返回 nil（不是空切片），让调用方一眼看出「这次没有」。
func parseRecommendCandidates(raw string) []RecommendCandidate {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	data := decodeRecommendPayload(raw)
	items, _ := data.([]any)
	if items == nil {
		return nil
	}

	out := make([]RecommendCandidate, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if len(out) >= RecommendMaxCount {
			break
		}
		c, ok := recommendItem(item)
		if !ok {
			continue
		}
		key := recommendKey(c.Title, c.Artist)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// decodeRecommendPayload 把回复解成 `any`，解不出返回 nil。
func decodeRecommendPayload(raw string) any {
	tries := make([]string, 0, 3)
	if m := recommendFenceRe.FindStringSubmatch(raw); len(m) > 1 {
		tries = append(tries, strings.TrimSpace(m[1]))
	}
	if m := recommendArrayRe.FindString(raw); m != "" {
		tries = append(tries, m)
	}
	tries = append(tries, raw)

	for _, t := range tries {
		if t == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(t), &v); err != nil {
			continue
		}
		// 模型把数组包在对象里时往下钻一层。
		if obj, ok := v.(map[string]any); ok {
			for _, k := range recommendListKeys {
				if list, ok := obj[k].([]any); ok {
					return list
				}
			}
			continue
		}
		return v
	}
	return nil
}

// recommendItem 把数组里的一项转成候选。
func recommendItem(item any) (RecommendCandidate, bool) {
	switch v := item.(type) {
	case string:
		// 模型只回了歌名（`["晴天", ...]`）。接受，但没有歌手可用来收窄搜索。
		title := cleanRecommendText(v)
		if title == "" {
			return RecommendCandidate{}, false
		}
		return RecommendCandidate{Title: title}, true
	case map[string]any:
		title := cleanRecommendText(firstNonEmptyStr(
			recommendStr(v, "title"), recommendStr(v, "name"),
			recommendStr(v, "song"), recommendStr(v, "songname"),
		))
		if title == "" {
			return RecommendCandidate{}, false
		}
		return RecommendCandidate{
			Title:  title,
			Artist: cleanRecommendText(recommendArtist(v)),
			Album:  cleanRecommendText(recommendStr(v, "album")),
			Reason: cleanRecommendText(recommendStr(v, "reason")),
		}, true
	default:
		return RecommendCandidate{}, false
	}
}

// recommendStr 取一个字符串字段（容忍数字 / 其它标量被写成字符串以外的东西）。
func recommendStr(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return ""
	}
}

// recommendArtist 取歌手：兼容 `artist` / `singer` / `singers` 三个字段名，
// 且字段本身可能是字符串或字符串数组（参考实现 `:794` 也是这么兼容的）。
func recommendArtist(m map[string]any) string {
	for _, key := range []string{"artist", "singer", "singers", "artists"} {
		switch v := m[key].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case []any:
			names := make([]string, 0, len(v))
			for _, a := range v {
				if s, ok := a.(string); ok {
					if s = strings.TrimSpace(s); s != "" {
						names = append(names, s)
					}
				}
			}
			if len(names) > 0 {
				return strings.Join(names, "/")
			}
		}
	}
	return ""
}

// cleanRecommendText 剥掉装饰、压平空白、并丢掉「模型用来表示空」的占位词。
func cleanRecommendText(s string) string {
	s = recommendTitleCleaner.Replace(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	switch strings.ToLower(s) {
	case "", "-", "无", "未知", "unknown", "n/a", "none", "null", "暂无":
		return ""
	}
	return s
}

// recommendKey 是候选的身份键（歌名+歌手，小写、去空白），只用于本层去重。
func recommendKey(title, artist string) string {
	t := strings.ToLower(strings.Join(strings.Fields(title), ""))
	if t == "" {
		return ""
	}
	return t + "\x1f" + strings.ToLower(strings.Join(strings.Fields(artist), ""))
}
