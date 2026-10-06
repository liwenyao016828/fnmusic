// 用户自定义的命名解析规则。
//
// ── 为什么要有这个 ──
//
// 内置启发式只能覆盖常见命名（"歌手 - 歌名"）。遇到某个用户特有的命名习惯
// （"【无损】Jay_Chou_晴天_2003"），要么拆错、要么每首歌都去问 AI。
//
// 参考实现 music-tidy v1.14.0 的 `analyze_name_rule` 给了更好的解法：
// **让 AI 生成一条正则** —— 用户给一个样例 + 一句解释，AI 产出规则，
// 之后整库按这条规则解析、**不再调 AI**。成本从「每首歌一次」降到「每种命名习惯一次」，
// 而且产出物（正则）是透明的、可检查的、可手改的。
//
// ── 安全关键：AI 产出的正则**必须回测** ──
//
// 正则来自模型，可能语法错误、可能匹配不上样例、可能提取不出字段，
// 甚至可能匹配到一堆无关文件上。所以 `ValidateRule` 是硬门槛：
// 编译不过 / 匹配不上样例 / 字段为空 / **提取结果不在样例里**，一律拒绝保存。
// 参考实现在 UI 上也是这么做的（「AI 生成的规则未通过校验，请在下方修正正则」）。
package nameparse

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Rule 一条用户定义的命名解析规则
type Rule struct {
	ID     string   `json:"id"`
	Name   string   `json:"name,omitempty"` // 规则名（展示用，可空）
	Regex  string   `json:"regex"`
	Fields []string `json:"fields,omitempty"` // 需要提取的字段，见 AllowedFields
	Note   string   `json:"note,omitempty"`
	Sample string   `json:"sample,omitempty"` // 生成时的样例，用于回测与展示
	Source string   `json:"source,omitempty"` // ai | manual
	// Disabled 停用这条规则。
	//
	// ⚠️ 用「反向」字段（Disabled 而不是 Enabled）是为了让**零值 = 启用** ——
	// 否则「AI 生成 → 保存」出来的规则默认不生效，用户会以为功能坏了。
	// （这个坑真踩到了：第一版用 Enabled，测试里保存完规则不生效。）
	Disabled  bool   `json:"disabled,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// AllowedFields 允许在正则里出现的命名分组。
//
// 白名单而不是「任意组名」：组名是给下游用的（写标签、拼搜索关键词），
// 冒出个拼错的组名不会报错，只会静默少一个字段。
var AllowedFields = []string{"artist", "title", "album", "track"}

// compiledRule 编译好的规则
type compiledRule struct {
	rule Rule
	re   *regexp.Regexp
}

// Store 规则存储（JSON 文件持久化）
type Store struct {
	path string

	mu       sync.RWMutex
	rules    []Rule
	compiled []compiledRule
}

// NewStore 创建存储。path 为空时只在内存里用（测试友好）。
func NewStore(path string) *Store {
	s := &Store{path: path}
	if path != "" {
		s.load()
	}
	return s
}

func (s *Store) load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return // 文件不存在是正常的首次运行
	}
	var rules []Rule
	if err := json.Unmarshal(b, &rules); err != nil {
		// 损坏就当空的，不要让整个功能挂掉
		return
	}
	s.rules = rules
	s.recompile()
}

// recompile 重建编译缓存。**编译不过的规则直接跳过** ——
// 文件可能被手工改坏，不能因为一条坏规则让整个解析挂掉。
func (s *Store) recompile() {
	out := make([]compiledRule, 0, len(s.rules))
	for _, r := range s.rules {
		if r.Disabled {
			continue
		}
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			continue
		}
		out = append(out, compiledRule{rule: r, re: re})
	}
	s.compiled = out
}

// Save 落盘
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.RLock()
	b, err := json.MarshalIndent(s.rules, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}

// List 返回全部规则（含未启用的）
func (s *Store) List() []Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Rule, len(s.rules))
	copy(out, s.rules)
	return out
}

// Upsert 新增或更新一条规则。**会先校验**，不通过直接报错不保存。
func (s *Store) Upsert(r Rule) (Rule, error) {
	if err := ValidateRule(r); err != nil {
		return Rule{}, err
	}
	if r.ID == "" {
		r.ID = fmt.Sprintf("rule_%d", time.Now().UnixNano())
	}
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().Format(time.RFC3339)
	}
	if r.Source == "" {
		r.Source = "manual"
	}

	s.mu.Lock()
	replaced := false
	for i := range s.rules {
		if s.rules[i].ID == r.ID {
			s.rules[i] = r
			replaced = true
			break
		}
	}
	if !replaced {
		s.rules = append(s.rules, r)
	}
	s.recompile()
	s.mu.Unlock()

	if err := s.Save(); err != nil {
		return r, err
	}
	return r, nil
}

// Remove 删除一条规则
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	out := s.rules[:0]
	for _, r := range s.rules {
		if r.ID != id {
			out = append(out, r)
		}
	}
	s.rules = out
	s.recompile()
	s.mu.Unlock()
	return s.Save()
}

// Parse 先试用户规则，命中就用；没命中回落到内置启发式。
//
// 用户规则优先于内置启发式 —— 它是**用户显式定义的意图**，
// 而启发式只是我们猜的。
func (s *Store) Parse(name string) Result {
	if s != nil {
		s.mu.RLock()
		compiled := s.compiled
		s.mu.RUnlock()

		base := filepath.Base(strings.TrimSpace(name))
		for _, c := range compiled {
			if r, ok := applyRule(c, base); ok {
				return r
			}
		}
	}
	return Parse(name)
}

// applyRule 用一条规则解析；没匹配上返回 ok=false。
func applyRule(c compiledRule, name string) (Result, bool) {
	m := c.re.FindStringSubmatch(name)
	if m == nil {
		return Result{}, false
	}
	names := c.re.SubexpNames()
	r := Result{}
	for i, n := range names {
		if i == 0 || i >= len(m) {
			continue
		}
		v := strings.TrimSpace(m[i])
		switch n {
		case "artist":
			r.Artist = v
		case "title":
			r.Title = v
		case "album":
			r.Album = v
		case "track":
			r.Track = strings.TrimLeft(v, "0")
		}
	}
	if r.Title == "" && r.Artist == "" {
		return Result{}, false
	}
	// 用户规则命中 = 用户明确告诉过我们这类名字怎么读，不再标可疑
	r.Candidates = append(r.Candidates, Candidate{Artist: r.Artist, Title: r.Title, Source: "rule"})
	return r, true
}

// ValidateRule 校验一条规则能不能用。**这是 AI 产出正则的硬门槛。**
//
// 五道检查，任何一道不过都拒绝保存：
//  1. 正则语法正确（能编译）
//  2. 组名在白名单里 —— 拼错的组名不会报错，只会静默丢字段
//  3. 至少含 artist 或 title 之一 —— 否则解析出来没用
//  4. 能匹配上样例（用 search 语义）
//  5. 提取出的字段**必须出现在样例里** —— 同 JudgeName 的防幻觉思路，
//     挡住「正则从别处变出内容」这种荒唐结果
func ValidateRule(r Rule) error {
	if strings.TrimSpace(r.Regex) == "" {
		return fmt.Errorf("正则不能为空")
	}
	re, err := regexp.Compile(r.Regex)
	if err != nil {
		return fmt.Errorf("正则语法错误：%v", err)
	}

	names := re.SubexpNames()
	hasArtist, hasTitle := false, false
	for _, n := range names {
		if n == "" {
			continue
		}
		if !containsStr(AllowedFields, n) {
			return fmt.Errorf("不支持的字段名「%s」，只能用：%s",
				n, strings.Join(AllowedFields, " / "))
		}
		if n == "artist" {
			hasArtist = true
		}
		if n == "title" {
			hasTitle = true
		}
	}
	if !hasArtist && !hasTitle {
		return fmt.Errorf("至少要有 artist(歌手) 或 title(歌名) 之一")
	}

	sample := strings.TrimSpace(r.Sample)
	if sample == "" {
		return nil // 没给样例就只做静态检查
	}

	m := re.FindStringSubmatch(sample)
	if m == nil {
		return fmt.Errorf("这条正则匹配不上样例，请检查")
	}
	for i, n := range names {
		if i == 0 || i >= len(m) {
			continue
		}
		v := strings.TrimSpace(m[i])
		if v == "" {
			continue
		}
		// 防幻觉：提取出来的内容必须出自样例本身
		if !strings.Contains(sample, v) {
			return fmt.Errorf("字段「%s」提取出的「%s」不在样例里", n, v)
		}
	}
	got := Result{}
	for i, n := range names {
		if i == 0 || i >= len(m) {
			continue
		}
		switch n {
		case "artist":
			got.Artist = strings.TrimSpace(m[i])
		case "title":
			got.Title = strings.TrimSpace(m[i])
		}
	}
	if got.Artist == "" && got.Title == "" {
		return fmt.Errorf("这条正则没提取出歌手或歌名，等于没用")
	}
	return nil
}

// TestRule 用一条规则解析一个文件名，返回结果（供 UI 预览）
func TestRule(regexStr, sample string) (Result, error) {
	re, err := regexp.Compile(regexStr)
	if err != nil {
		return Result{}, fmt.Errorf("正则语法错误：%v", err)
	}
	r, ok := applyRule(compiledRule{re: re}, strings.TrimSpace(sample))
	if !ok {
		return Result{}, fmt.Errorf("没匹配上，或没提取出歌手/歌名")
	}
	return r, nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
