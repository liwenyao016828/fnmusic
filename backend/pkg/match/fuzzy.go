package match

import (
	"strings"
)

// 模糊匹配（2026-10-29 用户：「一个个搜全名感觉很慢，搞模糊搜索」）
//
// 严格档 `search.MatchByNameSinger` 要求**曲名归一化后完全相等**，它挡住的是
// 「张冠李戴」（把别的歌的年份/曲序写进你的文件），这条底线不能松。
// 但真机上另一类失败很常见：歌名只差一个字（错字 / 简繁 / 少个标点 / 平台写的是
// 「晴天(原唱)」而文件名是「晴天」）。这类候**本来是同一首歌**，却因为「不完全相等」
// 被整批判掉，于是用户看到的是「搜到了但配不上」。
//
// 所以这里加一个**独立的模糊档**，只在严格档全部落空之后才启用，命中结果必须
// 在预览里标出来（`complete` 包会把它写进 Note），让用户在看词/预览时核一眼。
//
// 为什么不直接放宽 `normalizeForMatch`：那个函数同时被 /api/music/qualities
// 的音质对比、歌名修复、订阅匹配共用，改它等于把所有链路一起放宽 —— 一处改动、
// 四处风险。模糊只影响「补全」这一条路，所以单开一个判据。

// 模糊档的两个门槛。歌名比歌手严：歌名错了就是别的歌，歌手标注则常有
// 「周杰伦」vs「周杰伦/杨瑞代」这种多歌手差异。
const (
	// FuzzyArtistMin 歌手相似度下限。
	// 复用 ArtistScore（一方没标注时给中性 0.5），所以门槛必须低于 0.5
	// 才能允许「候选没标歌手」——那种情况无法判断，交给预览让用户核。
	FuzzyArtistMin = 0.45
)

// tradToSimp 常见繁简 / 异体字归一。
//
// 只用一张**小表**而不是完整转换库：完整转换需要 ~7000 条映射（或引入
// golang.org/x/text 依赖），而歌名里真正反复出现的繁体字就这些。
// 覆盖不到的字不会因此错配 —— 它们照常走 LCS 相似度，只是少了一次等价机会。
var tradToSimp = strings.NewReplacer(
	"臺", "台", "檯", "台", "颱", "台", "後", "后",
	"裡", "里", "裏", "里", "於", "于", "妳", "你",
	"祂", "他", "牠", "它", "與", "与", "來", "来",
	"個", "个", "們", "们", "這", "这", "說", "说",
	"話", "话", "時", "时", "間", "间", "開", "开",
	"關", "关", "門", "门", "愛", "爱", "夢", "梦",
	"風", "风", "雲", "云", "淚", "泪", "國", "国",
	"語", "语", "詞", "词", "聲", "声", "讀", "读",
	"聽", "听", "見", "见", "覺", "觉", "讓", "让",
	"給", "给", "對", "对", "錯", "错", "麼", "么",
	"嗎", "吗", "會", "会", "還", "还", "東", "东",
	"車", "车", "長", "长", "過", "过", "遠", "远",
	"邊", "边", "際", "际", "隨", "随", "陽", "阳",
	"陰", "阴", "隊", "队", "陳", "陈", "張", "张",
	"劉", "刘", "黃", "黄", "葉", "叶", "蘇", "苏",
	"許", "许", "鄭", "郑", "謝", "谢", "鄧", "邓",
	"馮", "冯", "羅", "罗", "趙", "赵", "吳", "吴",
	"楊", "杨", "蔣", "蒋", "韓", "韩", "鍾", "钟",
	"鐘", "钟", "蕭", "萧", "龍", "龙", "馬", "马",
	"鳥", "鸟", "魚", "鱼", "麗", "丽", "歡", "欢",
	"樂", "乐", "舊", "旧", "萬", "万", "億", "亿",
	"靈", "灵", "點", "点", "燈", "灯", "燒", "烧",
	"熱", "热", "無", "无", "為", "为", "經", "经",
	"釋", "释", "髮", "发", "發", "发", "變", "变",
	"態", "态", "樣", "样", "種", "种", "類", "类",
	"數", "数", "學", "学", "氣", "气", "運", "运",
	"轉", "转", "繞", "绕", "從", "从", "歷", "历",
	"曆", "历", "練", "练", "習", "习", "紅", "红",
	"緣", "缘", "線", "线", "結", "结", "終", "终",
	"彈", "弹", "牆", "墙", "壞", "坏", "願", "愿",
	"寫", "写", "據", "据", "檔", "档", "灑", "洒",
	"歸", "归", "傷", "伤", "憶", "忆", "戲", "戏",
	"廣", "广", "廠", "厂", "動", "动", "務", "务",
	"勞", "劳", "勢", "势", "團", "团", "園", "园",
	"圍", "围", "圖", "图", "塵", "尘", "夥", "伙",
	"實", "实", "寧", "宁", "寬", "宽", "審", "审",
	"層", "层", "島", "岛", "歲", "岁", "師", "师",
	"帶", "带", "幣", "币", "幫", "帮", "莊", "庄",
	"機", "机", "隻", "只", "處", "处", "誰", "谁",
	"請", "请", "錢", "钱", "鐵", "铁", "銀", "银",
	"藍", "蓝", "綠", "绿", "純", "纯", "網", "网",
	"絕", "绝", "繼", "继", "續", "续", "難", "难",
	"雙", "双", "雞", "鸡", "麵", "面", "齊", "齐",
	"準", "准", "蓋", "盖", "蘭", "兰", "塵", "尘",
)

// FoldVariants 归一化到「可比较的形态」：
// 繁体→简体 → 小写 → 去括号注释/噪音词 → 只留字母数字与中日韩文字。
//
// 复用 NormTitle 的管线（括号剥离 + 噪音词 + keepAlnumCJK），只是多了一步繁简折叠，
// 这样「晴天 (Live)」「晴天（現場版）」这类后缀差异照旧被抹掉。
func FoldVariants(s string) string {
	if s == "" {
		return ""
	}
	return NormTitle(tradToSimp.Replace(s))
}

// FuzzyTitle 模糊歌名比较。
//
// 判据用的是**编辑距离**而不是相似度比值 —— 因为用户的心智是「歌名错了一个字」，
// 不是一个 0~1 的分数。比值在短歌名上会骗人：《晴天》vs《晴天娃娃》的 LCS 比值
// 高达 0.67，可它们是两首不同的歌；而《青花瓷》vs《青花磁》的比值同样是 0.67，
// 却明显该配。编辑距离能分开这两个例子（前者差 2 个字，后者差 1 个字）。
//
// 容错字数按歌名长度分档（宁可少配，不可错配）：
//
//	1~2 字 → 零容错（《晴天》vs《雨天》、《童话》vs《神话》都只差一个字，
//	          两字歌名给不出「像不像」的信息，猜就是错）
//	3~5 字 → 容 1 个字（《青花瓷》vs《青花磁》）
//	6~10 字 → 容 2 个字
//	>10 字 → 容 3 个字
func FuzzyTitle(want, got string) (score float64, kind string, ok bool) {
	a, b := FoldVariants(want), FoldVariants(got)
	if a == "" || b == "" {
		return 0, "", false
	}
	// 折叠后完全相同：先分辨「本来就一样」还是「繁简/标点不同」，原因要写给人看。
	if a == b {
		if NormTitle(want) == NormTitle(got) {
			return 1, "歌名一致", true
		}
		return 1, "繁简/标点差异", true
	}
	// 包含关系：平台标题爱加「原唱」「抖音热歌」等尾巴，剥括号剥不完。
	// ⚠️ 三个条件缺一不可，否则会把**同结构的不同歌**吞进来：
	//   · 较短的至少 3 个字（两字歌名太容易是别人的子串）；
	//   · 长度差 ≤ 2（只是多挂了个尾巴，不是「短句 vs 长句」）；
	//   · 方向任意（谁包含谁都算）。
	// 例：「告白气球」⊂「告白气球甜蜜」✅（差 2）；「晴天」⊂「晴天娃娃」❌（短的不够 3 字）。
	if (strings.Contains(a, b) || strings.Contains(b, a)) &&
		minRuneLen(a, b) >= 3 && runeDiff(a, b) <= 2 {
		return 0.85, "歌名包含", true
	}
	dist := editDistance([]rune(a), []rune(b))
	if dist > fuzzyTolerance(minRuneLen(a, b)) {
		return 1 - float64(dist)/float64(maxRuneLen(a, b)), "", false
	}
	// 分数按下式折算，仅供调用方排序（越大越像），不用来判定。
	score = 1 - float64(dist)/float64(maxRuneLen(a, b))
	return score, "歌名差 " + itoa(dist) + " 个字", true
}

// fuzzyTolerance 这个长度的歌名最多容几个错字（见 FuzzyTitle 的说明）。
func fuzzyTolerance(minLen int) int {
	switch {
	case minLen <= 2:
		return 0
	case minLen <= 5:
		return 1
	case minLen <= 10:
		return 2
	default:
		return 3
	}
}

// editDistance Levenshtein 距离（按 rune，插入/删除/替换各算 1）。
// 歌名都短（<30 字），O(n*m) 完全够用，不必上滚动优化。
func editDistance(a, b []rune) int {
	n, m := len(a), len(b)
	if n == 0 {
		return m
	}
	if m == 0 {
		return n
	}
	prev := make([]int, m+1)
	cur := make([]int, m+1)
	for j := 0; j <= m; j++ {
		prev[j] = j
	}
	for i := 1; i <= n; i++ {
		cur[0] = i
		for j := 1; j <= m; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = minOf3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[m]
}

func minOf3(x, y, z int) int {
	m := x
	if y < m {
		m = y
	}
	if z < m {
		m = z
	}
	return m
}

// FuzzyAccept 判断「候选是不是这首歌」的模糊判据（歌名 + 歌手双门）。
//
// 歌名过了模糊门、歌手也得说得过去：歌手差距大（翻唱 / 同名不同人）直接否，
// 这条与 push 包的二次否决同源 —— 宁可这次不补，也不要写错人。
func FuzzyAccept(wantTitle, wantArtist, candTitle, candArtist string) (score float64, kind string, ok bool) {
	titleScore, kind, ok := FuzzyTitle(wantTitle, candTitle)
	if !ok {
		return 0, "", false
	}
	artistScore := ArtistScore(
		Catalog{Artist: wantArtist},
		Catalog{Artist: candArtist},
	)
	if artistScore < FuzzyArtistMin {
		return 0, "", false
	}
	// 歌手只是门槛，不进加权：模糊档的价值全在歌名容错，把歌手再折进总分会让
	// 「歌手完全一致但歌名错一个字」和「歌手勉强像但歌名完全一致」混为一谈。
	if artistScore >= 0.9 {
		kind += "·歌手一致"
	}
	return titleScore, kind, true
}

// minRuneLen 两个字符串里较短的那个的字数（按 rune 计，中文一个字算 1）。
func minRuneLen(a, b string) int {
	la, lb := len([]rune(a)), len([]rune(b))
	if la < lb {
		return la
	}
	return lb
}

// maxRuneLen 两个字符串里较长的那个的字数。
func maxRuneLen(a, b string) int {
	la, lb := len([]rune(a)), len([]rune(b))
	if la > lb {
		return la
	}
	return lb
}

// runeDiff 两个字符串的字数差（取绝对值）。
func runeDiff(a, b string) int {
	d := len([]rune(a)) - len([]rune(b))
	if d < 0 {
		return -d
	}
	return d
}
