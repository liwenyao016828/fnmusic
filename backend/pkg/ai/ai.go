// Package ai 提供 OpenAI 兼容的大模型接入能力。
//
// 移植自 music-tidy 的 ai_llm.py 思路，并保留其最有价值的几条工程约束：
//   - **未配置 Key 时静默降级**：所有方法首行返回空结果，主流程零感知；
//   - **强制 JSON 输出 + 三层防御性解析**：剥代码块 → json.Unmarshal → 正则抓第一个 {...}；
//   - **区分「用户提示词」与「输出契约」**：用户提示词可以覆盖内容，
//     但不能破坏内置的格式要求，否则下游 JSON/LRC 解析会全挂；
//   - **用量记账**：接口不返回 usage 时也单独记一笔，避免「没回明细就当没花钱」。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/security"
)

// Config 大模型配置
type Config struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
	Timeout int    `json:"timeout"` // 秒
}

// DefaultConfig 默认配置（未启用、无 Key，主流程静默降级）
func DefaultConfig() Config {
	return Config{
		Enabled: false,
		Timeout: 20,
		Model:   "doubao-seed-1-6-250615",
	}
}

// Available 是否具备调用条件
func (c Config) Available() bool {
	return c.Enabled && strings.TrimSpace(c.BaseURL) != "" && strings.TrimSpace(c.APIKey) != ""
}

// MaskKey 密钥脱敏展示（读配置时使用）
func MaskKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 5 {
		return "****"
	}
	return key[:3] + "****" + key[len(key)-2:]
}

// MergeSecret 写入配置时的密钥保持语义：
// 传入空串或含 **** 表示「沿用旧值」，避免前端整体回传配置时把 Key 抹掉。
func MergeSecret(incoming, existing string) string {
	incoming = strings.TrimSpace(incoming)
	if incoming == "" || strings.Contains(incoming, "****") {
		return existing
	}
	return incoming
}

// Usage 一次调用的用量
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// Missing 表示接口未返回 usage 明细（仍应计入调用次数，避免漏记）
	Missing bool `json:"usage_missing"`
}

// Client 大模型客户端
type Client struct {
	mu     sync.RWMutex
	cfg    Config
	client *http.Client

	// 用量累计
	usageMu   sync.Mutex
	usageLog  []UsageRecord
	totalCall int
	totalMiss int
}

// UsageRecord 一条用量记录
type UsageRecord struct {
	At      string `json:"at"`
	Kind    string `json:"kind"` // 用途标签，如 name_parse / analyze
	Prompt  int    `json:"prompt_tokens"`
	Output  int    `json:"completion_tokens"`
	Total   int    `json:"total_tokens"`
	Missing bool   `json:"usage_missing"`
	OK      bool   `json:"ok"`
}

// NewClient 创建客户端。
//
// 安全取舍：AI 服务地址是**用户显式配置**的可信目标，且常见于局域网自建
// （Ollama / one-api / LM Studio / new-api 等）。
// 因此这里允许访问内网地址——目标来自用户配置而非请求参数，不存在 SSRF 面。
// 同时 proxyFunc 会让内网目标直连，避免「代理本身是内网地址被自己拦下」的死锁。
func NewClient(cfg Config) *Client {
	opts := security.DefaultOptions()
	opts.AllowPrivateNet = true
	return &Client{
		cfg:    cfg,
		client: security.NewSafeClient(opts, 0),
	}
}

// Config 返回当前配置（密钥已脱敏）
func (c *Client) Config() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return map[string]interface{}{
		"enabled":    c.cfg.Enabled,
		"base_url":   c.cfg.BaseURL,
		"model":      c.cfg.Model,
		"timeout":    c.cfg.Timeout,
		"api_key":    MaskKey(c.cfg.APIKey),
		"available":  c.cfg.Available(),
		"configured": strings.TrimSpace(c.cfg.APIKey) != "",
	}
}

// UpdateConfig 更新配置（密钥遵循「脱敏保持」语义）
func (c *Client) UpdateConfig(patch Config) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if patch.BaseURL != "" {
		c.cfg.BaseURL = strings.TrimRight(strings.TrimSpace(patch.BaseURL), "/")
	}
	if patch.Model != "" {
		c.cfg.Model = strings.TrimSpace(patch.Model)
	}
	if patch.Timeout > 0 {
		c.cfg.Timeout = patch.Timeout
	}
	c.cfg.APIKey = MergeSecret(patch.APIKey, c.cfg.APIKey)
	// Enabled 用显式传参：无法用零值区分「未传」与「要关掉」，
	// 因此约定前端必须显式传 enabled。
	c.cfg.Enabled = patch.Enabled
}

// endpoint 兼容三种 base 形式（与 music-tidy 的 _endpoint 一致）
func (c *Client) endpoint() string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	// 以 /v1、/v3 之类结尾时直接追加
	if regexp.MustCompile(`/v\d+$`).MatchString(base) {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// chatRequest OpenAI 兼容请求体
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	// Stream 显式声明非流式。部分自建网关（one-api / new-api 等）即使不传也默认流式返回，
	// 因此客户端必须同时具备解析 SSE 的能力，不能只依赖这个字段。
	Stream bool `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatMessageOut 响应里的 message。
//
// ⚠️ `ReasoningContent` / `Reasoning` 是**推理模型**（DeepSeek-R1 / QwQ 等）的字段：
// 它们把思维链放这里，**content 可能是空的**。不接这两个字段的话，
// 配了推理模型会「静默无结果」—— 上层只看到空串，完全不知道原因。
type chatMessageOut struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	Reasoning        string `json:"reasoning"`
}

// chatDelta 流式响应里的 delta
type chatDelta struct {
	Content          string `json:"content"`
	Role             string `json:"role"`
	ReasoningContent string `json:"reasoning_content"`
	Reasoning        string `json:"reasoning"`
}

// chatChoice 一个 choice
type chatChoice struct {
	Message      chatMessageOut `json:"message"`
	FinishReason *string        `json:"finish_reason"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	// 兼容两种错误格式：OpenAI 的 {"error":{"message":"..."}}
	// 与部分自建网关的 {"error":"..."}（字符串）
	Error json.RawMessage `json:"error"`
}

// Chat 发起一次对话调用。未配置时返回空串且不报错（静默降级）。
func (c *Client) Chat(ctx context.Context, kind, system, user string) (string, Usage, error) {
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()

	if !cfg.Available() {
		return "", Usage{}, nil // 静默降级：未配置不是错误
	}

	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	msgs := make([]chatMessage, 0, 2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: user})

	payload, err := json.Marshal(chatRequest{
		Model:       cfg.Model,
		Messages:    msgs,
		Temperature: 0.2,
		Stream:      false,
	})
	if err != nil {
		return "", Usage{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		c.record(kind, Usage{Missing: true}, false)
		return "", Usage{}, fmt.Errorf("调用大模型失败: %w", err)
	}
	defer resp.Body.Close()

	// 限制响应体大小，避免异常响应打爆内存
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		c.record(kind, Usage{Missing: true}, false)
		return "", Usage{}, fmt.Errorf("读取大模型响应失败: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.record(kind, Usage{Missing: true}, false)
		return "", Usage{}, fmt.Errorf("大模型返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	parsed, err := parseChatResponse(body)
	if err != nil {
		c.record(kind, Usage{Missing: true}, false)
		return "", Usage{}, fmt.Errorf("解析大模型响应失败: %w", err)
	}
	if msg := extractErrorMessage(parsed.Error); msg != "" {
		c.record(kind, Usage{Missing: true}, false)
		return "", Usage{}, fmt.Errorf("大模型报错: %s", msg)
	}
	if len(parsed.Choices) == 0 {
		c.record(kind, Usage{Missing: true}, false)
		return "", Usage{}, fmt.Errorf("大模型未返回任何结果")
	}

	u := Usage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
	}
	// 接口不返回 usage 时也记一笔：不能把「没回明细」当成「没花钱」
	if u.TotalTokens == 0 && u.PromptTokens == 0 && u.CompletionTokens == 0 {
		u.Missing = true
	}
	c.record(kind, u, true)

	// 取正文。推理模型（DeepSeek-R1 / QwQ 等）会把思维链放在 reasoning_content，
	// **content 可能是空的** —— 这时如果直接返回空串，上层只会看到「没结果」，
	// 完全不知道是自己配了推理模型。所以：
	//   ① content 空时回退到 reasoning（个别网关把正文塞在那里）
	//   ② 两者都空且 finish_reason=length → 给出**明确可行动**的错误
	msg := parsed.Choices[0].Message
	content := msg.Content
	if strings.TrimSpace(content) == "" {
		content = firstNonEmptyStr(msg.ReasoningContent, msg.Reasoning)
	}
	if strings.TrimSpace(content) == "" {
		fr := ""
		if parsed.Choices[0].FinishReason != nil {
			fr = *parsed.Choices[0].FinishReason
		}
		if fr == "length" {
			return "", u, fmt.Errorf("大模型输出被 token 上限截断（finish_reason=length）——" +
				"推理模型常把额度耗在思维链上，请调大 max_tokens 或换非推理模型")
		}
		return "", u, fmt.Errorf("大模型返回成功但正文为空（finish_reason=%s）——"+
			"若用的是推理模型，请确认网关是否把正文放在 reasoning_content", orNone(fr))
	}
	return content, u, nil
}

func orNone(s string) string {
	if s == "" {
		return "无"
	}
	return s
}

// ── JSON 防御性解析 ──

var jsonBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")
var jsonObjectRe = regexp.MustCompile(`(?s)\{.*\}`)

// ParseJSONObject 三层兜底解析：
//  1. 直接 Unmarshal
//  2. 剥掉 ```json ``` 代码块后再试
//  3. 正则抓第一个 {...} 再试
//
// 模型经常在 JSON 前后加解释文字，这三层能覆盖绝大多数情况。
func ParseJSONObject(raw string, out interface{}) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("模型返回为空")
	}

	if err := json.Unmarshal([]byte(raw), out); err == nil {
		return nil
	}

	if m := jsonBlockRe.FindStringSubmatch(raw); len(m) > 1 {
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), out); err == nil {
			return nil
		}
	}

	if m := jsonObjectRe.FindString(raw); m != "" {
		if err := json.Unmarshal([]byte(m), out); err == nil {
			return nil
		}
	}

	return fmt.Errorf("模型返回的不是合法 JSON")
}

// BuildSystemPrompt 构建系统提示词，明确输出契约。
//
// 关键：用户提示词可以影响内容，但**不能破坏格式契约**——
// 否则下游的 JSON / LRC 解析会全部失败（这是 music-tidy 明确踩过的坑）。
func BuildSystemPrompt(task, contract string) string {
	var b strings.Builder
	b.WriteString("你是音乐库整理助手。\n")
	if task != "" {
		b.WriteString(task)
		b.WriteString("\n")
	}
	if contract != "" {
		b.WriteString("输出要求（必须严格遵守，不得更改格式）：\n")
		b.WriteString(contract)
		b.WriteString("\n")
	}
	b.WriteString("只输出要求的内容，不要输出任何额外的解释文字。")
	return b.String()
}

// WithUserPrompt 把用户自定义提示词包装成「内容优先但保留契约」的形式
func WithUserPrompt(userPrompt, contract string) string {
	userPrompt = strings.TrimSpace(userPrompt)
	if userPrompt == "" {
		return contract
	}
	return "以下是用户的额外要求（内容上具有最高优先级，但不得改变输出格式）：\n" +
		userPrompt + "\n\n" + contract
}

// ── 用法：文件名语义识别 ──

// NameParseResult 文件名解析结果
type NameParseResult struct {
	Artist string `json:"artist"`
	Title  string `json:"title"`
	Album  string `json:"album"`
	Style  string `json:"style"`
}

// ParseName 用大模型识别杂乱文件名（未配置时返回空结果，不报错）
func (c *Client) ParseName(ctx context.Context, filename string) NameParseResult {
	var out NameParseResult

	c.mu.RLock()
	available := c.cfg.Available()
	c.mu.RUnlock()
	if !available {
		return out
	}

	system := BuildSystemPrompt(
		"从音乐文件名中推断歌曲信息。",
		`只输出一个 JSON 对象，字段为 {"artist":"歌手","title":"歌名","album":"专辑","style":"风格"}。`+
			`无法确定的字段填空字符串。示例：{"artist":"周杰伦","title":"晴天","album":"叶惠美","style":"流行"}`)

	raw, _, err := c.Chat(ctx, "name_parse", system, "文件名："+filename)
	if err != nil {
		return out
	}
	if err := ParseJSONObject(raw, &out); err != nil {
		return NameParseResult{}
	}
	return out
}

// ── 用法：文件名候选消歧 ──

// NameCandidate 一种「歌手 + 歌名」的解释，由规则层（pkg/nameparse）给出。
type NameCandidate struct {
	Artist string `json:"artist"`
	Title  string `json:"title"`
}

// JudgeName 从规则层给出的候选里挑出正确的一组。
//
// ── 为什么不是「让 AI 直接认」──
//
// 参考实现 music-tidy v1.14.0 的 judge_name 注释写得很直白：
// 「模型的作用是『挑对/纠正』而不是重新创作」。把候选递过去，
// AI 的输出范围就被限制成**有限集合**，幻觉无处可逃 ——
// 这比让它对着一个乱名字凭空猜准得多。
//
// ── 防幻觉是硬约束，不是提示词的事 ──
//
// 光靠 prompt 说「不许编」是不够的，返回前必须**自己校验**：
// artist / title 都得能在原始文件名里找到（允许忽略空格与标点）。
// 校验不过一律返回 ok=false，调用方沿用规则层的结果。
// 宁可漏补（少写一个标签），也不能把模型的胡言写进用户的文件。
//
// 未配置 AI 时返回 ok=false，不报错。
func (c *Client) JudgeName(ctx context.Context, filename string, candidates []NameCandidate) (NameCandidate, bool) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return NameCandidate{}, false
	}

	c.mu.RLock()
	available := c.cfg.Available()
	c.mu.RUnlock()
	if !available {
		return NameCandidate{}, false
	}

	var b strings.Builder
	if len(candidates) > 0 {
		b.WriteString("下面是程序按分隔符切出来的候选，可能切错了位置：\n")
		for i, cd := range candidates {
			fmt.Fprintf(&b, "候选%d：歌手=「%s」 歌名=「%s」\n", i+1, cd.Artist, cd.Title)
		}
	}

	system := BuildSystemPrompt(
		"判定音乐文件名里到底谁是歌手、谁是歌名。",
		`只输出一个 JSON 对象，字段为 {"artist":"歌手","title":"歌名"}。`+
			`歌手与歌名都只能是文件名里出现过的文字（可以再剔掉其中的噪声词），`+
			`绝对不许换成另一首歌或另一个歌手；判不出来就把对应字段填空字符串。`+
			`「好听的歌曲推荐/热门/经典老歌/抖音热歌/车载音乐/合集/歌单/榜单/无损」`+
			`这类歌单名、推荐语、音质标记既不是歌手也不是歌名。`+
			`候选都不对时，从文件名里重新给出正确的歌手与歌名。`+
			`示例：{"artist":"周杰伦","title":"晴天"}`)

	user := b.String() + "文件名：" + filename
	raw, _, err := c.Chat(ctx, "name_judge", system, user)
	if err != nil {
		return NameCandidate{}, false
	}

	var out NameCandidate
	if err := ParseJSONObject(raw, &out); err != nil {
		return NameCandidate{}, false
	}
	out.Artist = strings.TrimSpace(out.Artist)
	out.Title = strings.TrimSpace(out.Title)

	// 两边都空 = 模型判不出来，等于没结果
	if out.Artist == "" && out.Title == "" {
		return NameCandidate{}, false
	}
	// ⚠️ 防幻觉校验：结果必须能在文件名里找到
	if !nameAppearsIn(filename, out.Artist) || !nameAppearsIn(filename, out.Title) {
		return NameCandidate{}, false
	}
	return out, true
}

// nameAppearsIn 判断 s 是否出自 filename（忽略大小写、空格与常见标点）。
//
// 之所以要归一化：文件名可能是 "Jay Chou - 晴天"，模型回 "JayChou" ——
// 字面不等但确实是文件里的内容。空串视为通过（表示「这一项判不出来」）。
func nameAppearsIn(filename, s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	return strings.Contains(normalizeNameText(filename), normalizeNameText(s))
}

var nameSepRe = regexp.MustCompile(`[\s\-_.,，。、·・\[\]【】()（）!！?？"“”'‘’]+`)

func normalizeNameText(s string) string {
	return nameSepRe.ReplaceAllString(strings.ToLower(s), "")
}

// ── 用法：生成命名解析正则 ──

// NameRegexSuggestion AI 给出的命名解析规则建议
type NameRegexSuggestion struct {
	Regex  string   `json:"regex"`
	Fields []string `json:"fields"`
	Note   string   `json:"note"`
}

// SuggestNameRegex 把「文件名样例 + 一句解释」转成一条命名解析正则。
//
// ── 为什么让 AI 产出「规则」而不是「结果」──
//
// 参考实现 music-tidy v1.14.0 的 `analyze_name_rule` 是这个思路里最好的一个：
// 用户给一个样例 + 一句解释 → AI 产出一条正则 → 之后**整库按这条正则解析、不再调 AI**。
// 成本从「每首歌一次调用」降到「每种命名习惯一次」，而且产出物（正则）是透明的、
// 用户能看懂、能自己改。
//
// ── ⚠️ 生成出来的东西**必须回测才能用** ──
//
// 模型可能给出：语法错误的、匹配不上样例的、提取不出字段的、
// 甚至用了 Go/RE2 不支持的语法（lookahead / 反向引用）的正则。
// 所以本函数**只负责生成**，校验是调用方的责任（见 `nameparse.ValidateRule`）。
// 这也是它不返回 error 的原因：生成失败和校验失败对调用方是同一件事 —— 都没法用。
//
// 未配置 AI 时返回 ok=false。
func (c *Client) SuggestNameRegex(ctx context.Context, sample, desc string) (NameRegexSuggestion, bool) {
	sample = strings.TrimSpace(sample)
	if sample == "" {
		return NameRegexSuggestion{}, false
	}

	c.mu.RLock()
	available := c.cfg.Available()
	c.mu.RUnlock()
	if !available {
		return NameRegexSuggestion{}, false
	}

	system := BuildSystemPrompt(
		"把音乐文件的命名方式转换成解析用的正则。",
		`只输出一个 JSON 对象，字段为 {"regex":"...","fields":["artist","title"],"note":"一句话中文说明"}。`+
			`要求：`+
			`1. 用命名分组 (?P<名字>...)，组名只能取 artist(歌手)、title(歌名)、album(专辑)、track(序号)，`+
			`至少包含 artist 或 title 之一；不需要提取的部分用非捕获分组或通配。`+
			`2. 正则要能匹配给出的样例文件名本身，对扩展名要兼容（结尾用 (?:\.[^.]+)?$ 或不锚定结尾）。`+
			`3. 样例里的 . [ ] ( ) 等字符若要按字面匹配必须转义。`+
			`4. 只能用 Go/RE2 兼容语法：**不支持** lookahead (?=)、lookbehind (?<=)、反向引用 \1。`+
			`5. regex 里不要加前后斜杠，不要用 markdown 代码块包裹。`)

	user := fmt.Sprintf("文件名样例：%q\n用户解释：%q", sample,
		firstNonEmptyStr(strings.TrimSpace(desc), "（无，请按样例与音乐命名习惯推断）"))

	raw, _, err := c.Chat(ctx, "name_rule", system, user)
	if err != nil {
		return NameRegexSuggestion{}, false
	}

	var out NameRegexSuggestion
	if err := ParseJSONObject(raw, &out); err != nil {
		return NameRegexSuggestion{}, false
	}
	out.Regex = strings.Trim(strings.TrimSpace(out.Regex), "/") // 有人会手滑加前后斜杠
	out.Note = strings.TrimSpace(out.Note)
	if out.Regex == "" {
		return NameRegexSuggestion{}, false
	}
	return out, true
}

// ── 用法：搜索结果消歧 ──

// SongQuery 本地曲目的已知信息（用于判断候选是不是同一首歌）
type SongQuery struct {
	Title    string
	Artist   string
	Album    string
	Duration int
}

// SongCandidate 一条搜索候选。
//
// 注意**没有 Index 字段** —— 候选在切片里的位置就是它的编号，
// 显示给模型时从 0 开始。多个 index 来源（切片位置 vs 结构体字段）迟早会不一致。
type SongCandidate struct {
	Name     string `json:"name"`
	Singer   string `json:"singer"`
	Album    string `json:"album,omitempty"`
	Duration int    `json:"duration,omitempty"`
	Source   string `json:"source,omitempty"`
}

// PickSongMatch 从搜索结果里挑出与目标曲目最匹配的一条，返回**候选序号**。
//
// ── 为什么需要它 ──
//
// `search.MatchByNameSinger` 要求曲名归一化后**完全相等**，只能处理「同名同歌手」。
// 而平台返回的标题常带后缀（`晴天 (Live)`、`晴天 (原唱 周杰伦)`、`晴天（R&B版）`），
// 或者同名不同版本（原版 / Live / 翻唱 / remix / 钢琴版）—— 规则匹配全都不认，
// 结果就是「搜到了但配不上，整首歌补不了」。
//
// ── AI 只做「从候选里挑」──
//
// 候选由搜索给出，AI 只输出**序号**或 `null`，不能引入候选外的任何内容。
// 跟 `JudgeName` 是同一套模式：把输出范围限制成有限集合，幻觉无处可逃。
//
// 拿不准时返回 ok=false，调用方沿用「没匹配上」——
// **宁可漏配，也不能配错**（配错会把别的歌的歌词封面写进用户文件）。
func (c *Client) PickSongMatch(ctx context.Context, want SongQuery, candidates []SongCandidate) (int, bool) {
	if len(candidates) == 0 || strings.TrimSpace(want.Title) == "" {
		return 0, false
	}

	c.mu.RLock()
	available := c.cfg.Available()
	c.mu.RUnlock()
	if !available {
		return 0, false
	}

	var b strings.Builder
	for i, cd := range candidates {
		// ⚠️ 编号**从 0 开始显示**（参考实现也这么做）。
		// 显示 1 起会让「模型回 0」产生歧义 —— 差一位就是挑错歌，
		// 而挑错歌会把别的歌的歌词封面写进用户文件。0 起则没有这个歧义。
		fmt.Fprintf(&b, "编号%d：%s | %s", i, cd.Name, cd.Singer)
		if cd.Album != "" {
			fmt.Fprintf(&b, " | 专辑:%s", cd.Album)
		}
		if cd.Duration > 0 {
			fmt.Fprintf(&b, " | %d秒", cd.Duration)
		}
		if cd.Source != "" {
			fmt.Fprintf(&b, " | 平台:%s", cd.Source)
		}
		b.WriteString("\n")
	}

	target := fmt.Sprintf("本地曲目：歌名=%q 歌手=%q", want.Title, want.Artist)
	if want.Album != "" {
		target += fmt.Sprintf(" 专辑=%q", want.Album)
	}
	if want.Duration > 0 {
		target += fmt.Sprintf(" 时长=%d秒", want.Duration)
	}

	system := BuildSystemPrompt(
		"判断搜索结果里哪一条就是目标曲目。",
		`只输出一个 JSON 对象：{"pick": <候选编号>} 或 {"pick": null}。`+
			`规则：`+
			`1. 候选编号**从 0 开始**（编号0 是第一条）。pick 只能是候选列表里出现过的编号，`+
			`不能自己编，也不能输出候选外的歌名歌手。`+
			`2. **优先选标题与目标完全一致的那条**；标题带后缀（Live/翻唱/remix/钢琴版/女声版）的，`+
			`只有在没有更匹配的选项时才选。`+
			`3. 时长相近的优先（差 15 秒内算相近）。`+
			`4. 同名不同歌（不同歌手各自的同名曲）要按歌手判断，**歌手对不上就不要选**。`+
			`5. **拿不准就返回 null** —— 宁可漏配，也不要配错。`+
			`示例：{"pick": 0}`)

	user := target + "\n候选：\n" + b.String()
	raw, _, err := c.Chat(ctx, "song_match", system, user)
	if err != nil {
		return 0, false
	}

	var out struct {
		Pick *int `json:"pick"`
	}
	if err := ParseJSONObject(raw, &out); err != nil {
		return 0, false
	}
	if out.Pick == nil {
		return 0, false // 模型明确表示「拿不准」
	}
	// 越界一律判失败 —— 宁可漏配也不能挑错
	if *out.Pick < 0 || *out.Pick >= len(candidates) {
		return 0, false
	}
	return *out.Pick, true
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// ── 用法：歌词核验 ──

// LyricVerdict 歌词核验结果
type LyricVerdict struct {
	Match      bool    `json:"match"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	// Actual 判定不匹配时，歌词实际出自的「歌手 - 歌名」（判不出来或匹配时为空）
	Actual string `json:"actual,omitempty"`
}

// lyricTimecodeRe 去掉 LRC 时间码，只留文本 —— 核验看的是歌词内容，不是时间轴
var lyricTimecodeRe = regexp.MustCompile(`\[\d{1,2}:\d{2}(?:[.:]\d{1,3})?\]`)

// maxLyricVerifyRunes 送进去核验的歌词正文上限。
// 整首歌词动辄上千字，全塞进去 prompt 又长又贵；前 1500 字足够判断是不是同一首。
const maxLyricVerifyRunes = 1500

// VerifyLyric 判断给定歌词是否就是「歌手 + 歌名」这首歌的。
//
// ── 这是**验证**用途，不是生成 ──
//
// 参考实现 music-tidy v1.14.0 的 `analyze_song` 就是这个定位：
// 「只判断歌词与歌曲是否匹配，不要提出修改歌手或歌名」。
//
// ── 为什么需要 ──
//
// `search.MatchByNameSinger` 只能保证**曲名歌手对得上**，
// 不能保证平台返回的歌词文件本身是对的（平台数据错标、同名前缀串味）。
// 而**写错歌词比不写更糟** —— 用户会以为是真歌词，还失去了「缺歌词」这个可行动的信号。
//
// 返回 ok=false 表示核验本身没做成（未配置 / 调用失败 / 返回不可解析），
// 此时调用方应当**按「没核验」处理**，而不是当成「不匹配」——
// 把「没查成」当成「有问题」会把好歌词全丢掉。
func (c *Client) VerifyLyric(ctx context.Context, artist, title, lyric string) (LyricVerdict, bool) {
	title = strings.TrimSpace(title)
	body := strings.TrimSpace(lyricTimecodeRe.ReplaceAllString(lyric, ""))
	if title == "" || body == "" {
		return LyricVerdict{}, false
	}

	c.mu.RLock()
	available := c.cfg.Available()
	c.mu.RUnlock()
	if !available {
		return LyricVerdict{}, false
	}

	if r := []rune(body); len(r) > maxLyricVerifyRunes {
		body = string(r[:maxLyricVerifyRunes])
	}

	system := BuildSystemPrompt(
		"核实歌词是否属于指定的歌曲。",
		`只输出一个 JSON 对象，字段为 `+
			`{"match":true/false,"confidence":0~1的小数,"reason":"一句话中文说明","actual":"歌词实际出自的歌手 - 歌名"}`+
			`。要求：`+
			`1. **只判断歌词与歌曲是否匹配，不要提出修改歌手或歌名。**`+
			`2. 歌词来自该歌曲的官方版本 / 现场 / 翻唱都算匹配，只要**内容主体一致**。`+
			`3. 判定不匹配时，在 actual 里写出歌词实际出自哪首歌；判不出来或判定匹配时填空字符串。`+
			`4. **不确定就如实说不确定**（match 给 false、confidence 给低值），不要猜。`+
			`示例：{"match":true,"confidence":0.9,"reason":"副歌与《晴天》一致","actual":""}`)

	user := fmt.Sprintf("歌手：%s\n歌名：%s\n歌词（已去时间码，可能截断）：\n%s",
		firstNonEmptyStr(artist, "未知"), title, body)

	rawJSON, _, err := c.Chat(ctx, "lyric_verify", system, user)
	if err != nil {
		return LyricVerdict{}, false
	}

	// ⚠️ `Match` 用指针接：模型没给这个字段时（比如只回了个 `{}`），
	// 用 bool 会取零值 false —— 那就变成「判定不匹配」，**把好歌词全丢掉**。
	// 必须区分「判定为不匹配」和「根本没判定」。参考实现也是这么防的（`if "match" not in obj: return {}`）。
	var raw struct {
		Match      *bool   `json:"match"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
		Actual     string  `json:"actual"`
	}
	if err := ParseJSONObject(rawJSON, &raw); err != nil {
		return LyricVerdict{}, false
	}
	if raw.Match == nil {
		return LyricVerdict{}, false // 没给判定 = 核验没做成，调用方按「没核验」处理
	}

	out := LyricVerdict{Match: *raw.Match, Confidence: raw.Confidence}
	// 置信度夹到 [0,1] —— 模型偶尔会给出 1.5 这种值
	if out.Confidence < 0 {
		out.Confidence = 0
	}
	if out.Confidence > 1 {
		out.Confidence = 1
	}
	out.Reason = strings.TrimSpace(raw.Reason)
	out.Actual = strings.TrimSpace(raw.Actual)
	return out, true
}

// ── 用量统计 ──

func (c *Client) record(kind string, u Usage, ok bool) {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()

	if u.Missing {
		c.totalMiss++
	}
	c.totalCall++
	c.usageLog = append(c.usageLog, UsageRecord{
		At:      time.Now().Format("2006-01-02T15:04:05"),
		Kind:    kind,
		Prompt:  u.PromptTokens,
		Output:  u.CompletionTokens,
		Total:   u.TotalTokens,
		Missing: u.Missing,
		OK:      ok,
	})
	// 只保留最近 1000 条，避免无限增长
	if len(c.usageLog) > 1000 {
		c.usageLog = c.usageLog[len(c.usageLog)-1000:]
	}
}

// UsageSummary 用量汇总
type UsageSummary struct {
	TotalCalls   int                 `json:"total_calls"`
	MissingUsage int                 `json:"missing_usage"`
	PromptTokens int                 `json:"prompt_tokens"`
	OutputTokens int                 `json:"completion_tokens"`
	TotalTokens  int                 `json:"total_tokens"`
	ByKind       map[string]KindStat `json:"by_kind"`
	Recent       []UsageRecord       `json:"recent,omitempty"`
}

// KindStat 按用途统计
type KindStat struct {
	Calls  int `json:"calls"`
	Prompt int `json:"prompt_tokens"`
	Output int `json:"completion_tokens"`
	Total  int `json:"total_tokens"`
}

// Usage 返回用量汇总
func (c *Client) Usage(limitRecent int) UsageSummary {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()

	s := UsageSummary{
		TotalCalls:   c.totalCall,
		MissingUsage: c.totalMiss,
		ByKind:       make(map[string]KindStat),
	}
	for _, r := range c.usageLog {
		s.PromptTokens += r.Prompt
		s.OutputTokens += r.Output
		s.TotalTokens += r.Total

		st := s.ByKind[r.Kind]
		st.Calls++
		st.Prompt += r.Prompt
		st.Output += r.Output
		st.Total += r.Total
		s.ByKind[r.Kind] = st
	}

	if limitRecent <= 0 || limitRecent > 200 {
		limitRecent = 20
	}
	n := len(c.usageLog)
	start := n - limitRecent
	if start < 0 {
		start = 0
	}
	s.Recent = append([]UsageRecord(nil), c.usageLog[start:]...)
	return s
}

// ResetUsage 清空用量统计
func (c *Client) ResetUsage() {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	c.usageLog = nil
	c.totalCall = 0
	c.totalMiss = 0
}

// Test 连通性测试
func (c *Client) Test(ctx context.Context) (string, error) {
	c.mu.RLock()
	available := c.cfg.Available()
	c.mu.RUnlock()
	if !available {
		return "", fmt.Errorf("尚未配置大模型：请先填写 Base URL 与 API Key 并启用")
	}
	raw, _, err := c.Chat(ctx, "test", "你是一个测试助手。", "请只回复两个字：正常")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(raw), nil
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// RawConfig 返回未脱敏的原始配置（仅供持久化使用，不要直接下发给前端）
func (c *Client) RawConfig() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

// doWithFallback 发起请求，并在「直连」与「走代理」之间自动重试。
//
// 背景：内网自建 AI 服务存在两种相反的环境需求——
//   - 多数情况：内网直连最快，走代理反而被 SSRF 规则拦下；
//   - 少数情况：服务监听在 NAS 自己身上，用局域网 IP 访问时因 hairpin/路由策略不通，
//     必须经代理才能到达（典型报错是 i/o timeout，而代理能通）。
//
// 与其让用户去猜该配哪个环境变量，这里直接两条路都试：
// 先按当前 Transport 的策略发一次，失败再换另一种。
// 对用户来说「填上地址就能用」，不需要理解代理细节。
func (c *Client) doWithFallback(ctx context.Context, req *http.Request, payload []byte) (*http.Response, error) {
	// 第一次：按默认策略（内网直连 / 公网走代理）
	resp, err := c.client.Do(req)
	if err == nil {
		return resp, nil
	}
	firstErr := err

	// 仅在「连接层面失败」时才有必要换路径；HTTP 状态码错误不重试
	if !isConnectError(err) {
		return nil, err
	}

	// 第二次：反转代理策略
	alt := security.Options{AllowPrivateNet: true}
	if security.IsLocalTarget(req.URL.Hostname()) {
		// 目标是内网：第一次是直连，这次强制走代理
		alt.ForceProxyForLocal = true
	} else {
		// 目标是公网：第一次走了代理，这次直连
		alt.DisableProxy = true
	}

	client := security.NewSafeClient(alt, 0)
	req2 := req.Clone(ctx)
	req2.Body = io.NopCloser(bytes.NewReader(payload))

	resp2, err2 := client.Do(req2)
	if err2 == nil {
		return resp2, nil
	}
	// 两条路都不通时，返回更有信息量的那个错误
	return nil, fmt.Errorf("%v（已尝试直连与代理两种方式）", firstErr)
}

// isConnectError 判断是否为连接层失败（值得换代理路径重试）
func isConnectError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{
		"timeout", "timed out", "connection refused", "no route to host",
		"network is unreachable", "dial tcp", "proxyconnect", "connection reset",
		"i/o timeout", "tls handshake timeout", "eof",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// extractErrorMessage 从 error 字段提取可读信息。
// 兼容 {"error":"文本"} 与 {"error":{"message":"文本"}} 两种形态。
func extractErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// 字符串形态
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	// 对象形态
	var obj struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.Message != "" {
			return obj.Message
		}
		return obj.Type
	}
	return strings.TrimSpace(string(raw))
}

// endpointFor 基于指定 base 构造 completions 地址
func endpointFor(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if regexp.MustCompile(`/v\d+$`).MatchString(base) {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// parseChatResponse 解析响应，同时支持普通 JSON 与 SSE 流式格式。
//
// 必要性：不少自建网关（one-api / new-api / 各类中转）即使请求里写 stream:false，
// 仍然按 SSE 返回 `data: {...}\n\n`，直接按 JSON 解析会报
// `invalid character 'd' after top-level value`——这正是用户遇到的现象。
// 因此这里自动识别两种格式，把 SSE 的所有增量片段拼成完整回复。
func parseChatResponse(body []byte) (*chatResponse, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("响应为空")
	}

	// 普通 JSON
	if trimmed[0] == '{' {
		var out chatResponse
		if err := json.Unmarshal(trimmed, &out); err == nil {
			return &out, nil
		}
	}

	// SSE：逐行取 data: 负载
	if bytes.Contains(trimmed, []byte("data:")) {
		return parseSSE(trimmed)
	}

	var out chatResponse
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// parseSSE 解析 SSE 流，把 delta.content 拼接为完整回复
func parseSSE(body []byte) (*chatResponse, error) {
	text := string(body)

	// \r\n 与 \n 都要兼容
	text = strings.ReplaceAll(text, "\r\n", "\n")

	out := &chatResponse{}
	var content strings.Builder
	// 推理模型单独攒思维链：正文一直没来就回退到它
	var reasoning strings.Builder
	// 记最后一次 finish_reason，用于给出可行动的错误（length = 被 token 上限截断）
	finishReason := ""
	got := false

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var chunk struct {
			Choices []struct {
				Delta        chatDelta      `json:"delta"`
				Message      chatMessageOut `json:"message"`
				FinishReason *string        `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // 跳过无法解析的片段，不影响整体
		}
		if msg := extractErrorMessage(chunk.Error); msg != "" {
			return nil, fmt.Errorf("大模型报错: %s", msg)
		}

		for _, ch := range chunk.Choices {
			// 流式用 delta.content，个别网关在最后一片用 message.content
			if ch.Delta.Content != "" {
				content.WriteString(ch.Delta.Content)
				got = true
			} else if ch.Message.Content != "" {
				content.WriteString(ch.Message.Content)
				got = true
			} else {
				// 推理模型：思维链在 reasoning_content 里，content 全程为空。
				// 单独攒起来 —— 正文一直没来就回退到它（见函数末尾）。
				if s := firstNonEmptyStr(ch.Delta.ReasoningContent, ch.Delta.Reasoning,
					ch.Message.ReasoningContent, ch.Message.Reasoning); s != "" {
					reasoning.WriteString(s)
				}
			}
			if ch.FinishReason != nil {
				finishReason = *ch.FinishReason
			}
		}
		if chunk.Usage.TotalTokens > 0 {
			out.Usage.PromptTokens = chunk.Usage.PromptTokens
			out.Usage.CompletionTokens = chunk.Usage.CompletionTokens
			out.Usage.TotalTokens = chunk.Usage.TotalTokens
		}
	}

	if !got && content.Len() == 0 {
		// 正文没来但有思维链 → 回退（个别网关把正文塞在 reasoning 里）
		if reasoning.Len() > 0 {
			content = reasoning
			got = true
		} else if finishReason == "length" {
			return nil, fmt.Errorf("大模型输出被 token 上限截断（finish_reason=length）——" +
				"推理模型常把额度耗在思维链上，请调大 max_tokens 或换非推理模型")
		} else {
			return nil, fmt.Errorf("流式响应中没有解析到任何内容（finish_reason=%s）", orNone(finishReason))
		}
	}

	out.Choices = []chatChoice{{Message: chatMessageOut{Content: content.String()}}}

	return out, nil
}
