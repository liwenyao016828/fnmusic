// Package organize 把曲库里的音频文件按用户指定的结构重新组织成目录树。
//
// 目标形态（默认「歌手 / 专辑 / 曲目」三层）：
//
//	Music/
//	├── 周杰伦/
//	│   ├── 范特西/
//	│   │   ├── 01 双截棍.flac
//	│   │   ├── 01 双截棍.jpg
//	│   │   └── 01 双截棍.lrc
//	│   └── 七里香/
//
// 三条硬规矩（都是踩过坑才定下来的）：
//
//  1. **只搬不改**：整理只移动文件，不改标签、不转码、不删内容。
//     用户的曲库是心血，改内容必须走「补全 / 修复」那些显式功能。
//  2. **附属文件同命同目录**：同名 .jpg / .lrc / .txt 跟着音频一起改名、一起搬，
//     落在同一个专辑目录里 —— 飞牛音乐按「同名」认封面与歌词，改了名就得一起改，
//     否则整理完等于把封面歌词弄丢了。
//  3. **先预演后执行**：Plan 只算不搬，Apply 才动盘；执行过程记进日志，
//     搬错了能 Undo 回原位。
//
// 结构样式由用户自己填两个模板决定（目录模板 / 文件名模板），见 Template。
// 模板占位符：{artist} {album} {title} {track} {disc} {year} {genre} {ext}，
// 数字占位符支持零填充写法 {track:02d}。
package organize

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"fn-lx-player/pkg/dirwalk"
	"fn-lx-player/pkg/filename"
	"fn-lx-player/pkg/namefmt"
	"fn-lx-player/pkg/tags"
)

// 兜底名：元数据缺失时用它占位，而不是把文件丢进一个叫「」的空目录。
const (
	UnknownArtist = "未知歌手"
	UnknownAlbum  = "未知专辑"
	UnknownTitle  = "未知曲目"
)

// sidecarExts 是「与音频同名同目录」的附属文件扩展名（小写，带点）。
//
// 只认这些：它们是飞牛音乐真正会去找的东西（封面图 / 外挂歌词）。
// 目录级 cover.jpg **不认** —— 它是整张专辑共用的，搬走它没有意义（见 pkg/tidy 的说明）。
var sidecarExts = []string{".lrc", ".txt", ".jpg", ".jpeg", ".png", ".webp"}

// Meta 一首歌的元数据（整理时用来算目录与文件名）。
type Meta struct {
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	Album  string `json:"album,omitempty"`
	Track  int    `json:"track,omitempty"`
	Disc   int    `json:"disc,omitempty"`
	Year   int    `json:"year,omitempty"`
	Genre  string `json:"genre,omitempty"`
	// Source 说明这份元数据是从哪来的：tag（文件内嵌标签）/ library（曲库索引）/
	// filename（从文件名反解）/ mixed（几处拼起来的）。界面据此告诉用户
	// 「这一首的歌名是从文件名猜的」，而不是让人以为那就是真标签。
	Source string `json:"source,omitempty"`
}

// Template 描述目标结构：目录怎么分层、文件怎么命名。
//
// Dir 是**目录模板**（相对根目录），用 "/" 分层，如 "{artist}/{album}"；
// 为空 = 不建目录，所有文件平铺在根下。
// File 是**文件名模板**（不含扩展名），如 "{track:02d} {title}"。
type Template struct {
	Dir  string `json:"dir"`
	File string `json:"file"`
}

// Preset 一个内置结构样式。
type Preset struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"desc"`
	Dir  string `json:"dir"`
	File string `json:"file"`
}

// Presets 返回内置的结构样式。
//
// 为什么给「预设」而不是只给一个模板输入框：用户脑子里是「我想按歌手和专辑分」，
// 不是「我想填 {artist}/{album}」。预设把这句话直接变成模板，自定义留给他改细节。
func Presets() []Preset {
	return []Preset{
		{
			ID: "artist-album-track", Name: "歌手 / 专辑 / 曲目",
			Desc: "最常用的一层：歌手目录下是专辑，专辑目录里是曲目（同名封面与歌词一起跟上）",
			Dir:  "{artist}/{album}", File: "{track:02d} {title}",
		},
		{
			ID: "artist-album", Name: "歌手 / 专辑（文件名带歌手）",
			Desc: "目录同上，但文件名保留「歌手 - 歌名」，兼容按文件名找歌的老习惯",
			Dir:  "{artist}/{album}", File: "{artist} - {title}",
		},
		{
			ID: "artist", Name: "歌手 / 曲目",
			Desc: "只按歌手分一层，文件名带专辑",
			Dir:  "{artist}", File: "{album} - {title}",
		},
		{
			ID: "album", Name: "专辑 / 曲目",
			Desc: "只按专辑分一层（合辑、专辑精选这类场景）",
			Dir:  "{album}", File: "{artist} - {title}",
		},
		{
			ID: "flat", Name: "平铺",
			Desc: "不建目录，只把文件名统一成「歌手 - 歌名」",
			Dir:  "", File: "{artist} - {title}",
		},
	}
}

// SidecarExts 返回「与音频同名同目录」的附属文件扩展名（小写、带点）。
//
// 给界面解释用（「这些文件会跟着音频一起改名、一起搬」）—— 让前端自己抄一份
// 清单，等这边加了格式就两边不一致了。
func SidecarExts() []string {
	out := make([]string, len(sidecarExts))
	copy(out, sidecarExts)
	return out
}

// Placeholder 一个模板占位符的说明。
type Placeholder struct {
	Key     string `json:"key"`
	Desc    string `json:"desc"`
	Example string `json:"example"`
}

// Placeholders 返回模板占位符清单（界面拿它渲染提示，别在前端另抄一份）。
//
// 顺序按「用户最常拼的顺序」排，不是字典序。
func Placeholders() []Placeholder {
	return []Placeholder{
		{Key: "{artist}", Desc: "歌手", Example: "周杰伦"},
		{Key: "{album}", Desc: "专辑", Example: "范特西"},
		{Key: "{title}", Desc: "歌名", Example: "双截棍"},
		{Key: "{track}", Desc: "曲序（数字），写 {track:02d} 会自动补零成 01", Example: "01"},
		{Key: "{disc}", Desc: "碟号（数字）", Example: "1"},
		{Key: "{year}", Desc: "年份", Example: "2001"},
		{Key: "{genre}", Desc: "风格", Example: "流行"},
		{Key: "{ext}", Desc: "扩展名（一般不用写，整理时会自动补上）", Example: "flac"},
	}
}

// DefaultTemplate 返回默认结构样式（歌手 / 专辑 / 曲目）。
func DefaultTemplate() Template {
	return Template{Dir: "{artist}/{album}", File: "{track:02d} {title}"}
}

// SampleMeta 是界面上「这一段搬完长什么样」的示例元数据。
//
// 为什么放在后端：用户在界面上要看的是**成品路径**（"周杰伦/范特西/01 双截棍.flac"），
// 不是模板串（"{artist}/{album}"）。成品怎么拼只有渲染器说了算，前端自己拼一份
// 迟早会跟这边不一致（比如 {track:02d} 的补零、空目录的平铺）。
func SampleMeta() Meta {
	return Meta{
		Title:  "双截棍",
		Artist: "周杰伦",
		Album:  "范特西",
		Track:  1,
		Disc:   1,
		Year:   2001,
		Genre:  "流行",
		Source: "sample",
	}
}

// Render 把模板套到一条元数据上，返回「相对目录（斜杠分隔，可为空）」与「文件名（不含扩展名）」。
//
// 只给界面做示例渲染用；真正的整理走 PlanOf（还要查冲突、查越界）。
func Render(t Template, m Meta, ext string) (string, string) {
	t = NormalizeTemplate(t)
	ext = strings.TrimPrefix(strings.TrimSpace(ext), ".")
	if ext == "" {
		ext = "flac"
	}
	return renderPath(t.Dir, m, ext), renderFile(t.File, m, ext)
}

// NormalizeTemplate 把外部传入的模板补全成合法模板。
//
// 两个方向都要兜底：目录模板可以合法地为空（平铺），但**不能**是「有内容却
// 全是占位符错误」；文件名模板为空则没得可算，回落默认。
func NormalizeTemplate(t Template) Template {
	out := Template{
		Dir:  strings.TrimSpace(t.Dir),
		File: strings.TrimSpace(t.File),
	}
	if out.File == "" {
		out.File = DefaultTemplate().File
	}
	return out
}

// Options 整理参数。
type Options struct {
	// Root 整理的根目录（曲库目录或它的某个子目录）。
	Root string
	// Template 目标结构。
	Template Template
	// Lookup 是曲库索引的兜底查询：按路径问一句「这首的歌手/专辑/曲序是什么」。
	// 没有它就只靠标签与文件名 —— 那样标签缺失的歌会大量落到「未知歌手」。
	Lookup func(path string) (Meta, bool)
	// MaxDepth 扫描深度（<=0 用 dirwalk 的默认）。
	MaxDepth int
	// NumberUnknown 给**没有曲序**的歌按同专辑内出现顺序补一个序号。
	// 默认关：乱编的序号比没有序号更误导。
	NumberUnknown bool
}

// Move 一条待执行的移动。
type Move struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Kind：audio（音频本体）/ lyric（歌词）/ cover（封面）/ other（其它附属文件）。
	Kind string `json:"kind"`
}

// Item 一首曲目的整理结果。
type Item struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Meta   Meta   `json:"meta"`
	// Moves 这首歌涉及的全部移动（音频本体 + 附属文件）。
	Moves []Move `json:"moves,omitempty"`
	// Status：move（要搬）/ same（已经在该去的位置）/ conflict（目标已占用）/ skip（跳过）。
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Stats 一次整理的对账数字。
type Stats struct {
	Audio    int `json:"audio"`
	Move     int `json:"move"`
	Same     int `json:"same"`
	Conflict int `json:"conflict"`
	Skip     int `json:"skip"`
	Sidecars int `json:"sidecars"`
}

// Plan 整理方案（预演结果）。
type Plan struct {
	Root     string   `json:"root"`
	Template Template `json:"template"`
	Items    []Item   `json:"items"`
	Stats    Stats    `json:"stats"`
	Warnings []string `json:"warnings,omitempty"`
}

// Result 一次实际整理的结果。
type Result struct {
	Root      string   `json:"root"`
	Moved     int      `json:"moved"`
	Sidecars  int      `json:"sidecars"`
	Failed    int      `json:"failed"`
	DirsClean int      `json:"dirs_cleaned"`
	Errors    []string `json:"errors,omitempty"`
	// Journal 这次移动的反向记录（Undo 用）。为空表示没有可撤销的动作。
	Journal []Move `json:"journal,omitempty"`
}

// PlanOf 计算整理方案：**只读，不动盘**。
//
// 为什么要分两步：移动是不可逆的（虽然有 Undo，但中间崩了就对不上），
// 而用户在点「执行」之前需要先看清「到底会搬哪些文件、哪些会撞车」。
func PlanOf(opts Options) (Plan, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return Plan{}, fmt.Errorf("没有指定整理目录")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Plan{}, fmt.Errorf("目录路径不合法: %v", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Plan{}, fmt.Errorf("目录不可访问: %v", err)
	}
	if !info.IsDir() {
		return Plan{}, fmt.Errorf("不是一个目录: %s", abs)
	}
	tpl := NormalizeTemplate(opts.Template)

	files, walkStats := dirwalk.Walk([]string{abs}, dirwalk.Options{MaxDepth: opts.MaxDepth})
	plan := Plan{Root: abs, Template: tpl, Items: make([]Item, 0, len(files))}
	for _, w := range walkStats.Warnings {
		plan.Warnings = append(plan.Warnings, w)
	}

	// 附属文件要按目录找：同一个目录只读一次。
	dirCache := map[string][]os.DirEntry{}
	sidecars := map[string][]string{}  // 音频路径 → 同名附属文件
	dirCovers := map[string][]string{} // 目录 → 该目录里的「专辑封面」文件名

	for _, f := range files {
		plan.Stats.Audio++
		dir := filepath.Dir(f.Path)
		entries, ok := dirCache[dir]
		if !ok {
			entries = readDirEntries(dir)
			dirCache[dir] = entries
		}
		base := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if !isSidecarExt(ext) {
				continue
			}
			if strings.TrimSuffix(name, filepath.Ext(name)) != base {
				continue
			}
			sidecars[f.Path] = append(sidecars[f.Path], filepath.Join(dir, name))
		}
		// 目录级的专辑封面（cover.jpg / folder.png）：它们不属于某一首歌，
		// 属于**整个目录**，所以单独收着，等下按「这个目录的歌是不是同一个专辑」
		// 决定要不要跟着搬（见 associateDirCovers）。
		for _, e := range entries {
			if e.IsDir() || !isDirCoverName(e.Name()) {
				continue
			}
			dirCovers[dir] = append(dirCovers[dir], filepath.Join(dir, e.Name()))
		}
	}

	// 先把每首歌的元数据定下来，再算目标路径 ——
	// 「同专辑内按出现顺序补序号」需要先知道谁和谁同专辑。
	// Root 换成绝对路径后再交下去：resolveMeta 要用它算「这个文件在根目录的第几层」。
	metaOpts := opts
	metaOpts.Root = abs
	metas := make([]Meta, len(files))
	for i, f := range files {
		metas[i] = resolveMeta(f.Path, metaOpts)
	}
	assignFallbackTracks(files, metas, opts.NumberUnknown)

	// 目标路径占用表：两首歌撞到同一个目标 = 冲突（不是「后者覆盖前者」）。
	occupied := map[string]string{}

	for i, f := range files {
		item := Item{Source: f.Path, Meta: metas[i]}
		// 符号链接一律不搬：链接本身是「指向别处」，搬走链接会让相对链接直接断掉
		// （目标文件还在原处，但链接按新位置解析就找不到了），用户看到的是「整理
		// 之后歌不见了」。上游也是跳过（organize.py 的 symlink 检查）。
		if f.Symlink {
			item.Status = "skip"
			item.Reason = "是符号链接，不参与整理（搬走会让链接失效）"
			plan.Stats.Skip++
			plan.Items = append(plan.Items, item)
			continue
		}
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(f.Path)), ".")
		m := metas[i]

		dirRel := renderPath(tpl.Dir, m, ext)
		base := renderFile(tpl.File, m, ext)
		if base == "" {
			base = filename.Sanitize(m.Title)
		}
		target := filepath.Join(abs, filepath.FromSlash(dirRel), base+"."+ext)

		// 目标必须还在根目录里（模板里写了 ../ 之类的越界写法时兜住）。
		if !withinRoot(abs, target) {
			item.Status = "skip"
			item.Reason = "目标路径超出整理目录，已跳过"
			plan.Stats.Skip++
			plan.Items = append(plan.Items, item)
			continue
		}

		// 同名不同曲（合辑、双碟里的第二首、两张专辑各有一首同名歌）不该整首
		// 跳过 —— 换个名字（「歌名 (2).flac」）搬过去，才是用户在文件管理器里
		// 的习惯做法（上游 organize.py 也是依次追加 " (2)"、" (3)"）。
		deduped := false
		if !samePath(f.Path, target) && targetTaken(occupied, target) {
			orig := base
			for n := 2; n <= 99; n++ {
				cand := fmt.Sprintf("%s (%d)", orig, n)
				ct := filepath.Join(filepath.Dir(target), cand+"."+ext)
				if targetTaken(occupied, ct) {
					continue
				}
				base, target = cand, ct
				deduped = true
				break
			}
		}

		// 附属文件跟着定稿后的 base 一起改名，并且**不覆盖**目标位置已有的同名
		// 歌词 / 封面（别的曲子带的，或者用户自己放的）：覆盖别人的封面是静默
		// 的数据损失，宁可让这一首继续用原来的附属文件。
		var sidecarMoves []Move
		for _, sc := range sidecars[f.Path] {
			scExt := strings.TrimPrefix(strings.ToLower(filepath.Ext(sc)), ".")
			scTarget := filepath.Join(filepath.Dir(target), base+"."+scExt)
			if !samePath(sc, scTarget) && fileExists(scTarget) {
				plan.Warnings = append(plan.Warnings,
					"已有同名附属文件，保留原样："+filepath.Base(scTarget))
				continue
			}
			sidecarMoves = append(sidecarMoves, Move{From: sc, To: scTarget, Kind: kindOf(scExt)})
		}

		item.Target = target
		switch {
		case samePath(f.Path, target):
			item.Status = "same"
			item.Moves = nil
			plan.Stats.Same++
		case occupied[target] != "":
			item.Status = "conflict"
			item.Reason = "目标已被另一首歌占用：" + occupied[target]
			plan.Stats.Conflict++
			occupied[target] = f.Path
		case fileExists(target):
			item.Status = "conflict"
			item.Reason = "目标位置已有同名文件"
			plan.Stats.Conflict++
		default:
			item.Status = "move"
			item.Moves = append([]Move{{From: f.Path, To: target, Kind: "audio"}}, sidecarMoves...)
			plan.Stats.Move++
			plan.Stats.Sidecars += len(sidecarMoves)
			occupied[target] = f.Path
			if deduped {
				item.Reason = "目标重名，改名为「" + filepath.Base(target) + "」"
			}
		}
		plan.Items = append(plan.Items, item)
	}

	// 专辑封面（目录里的 cover.jpg）跟着专辑目录一起搬。
	associateDirCovers(&plan, dirCovers)

	return plan, nil
}

// isDirCoverName 判断这个文件名是不是「专辑封面」。
//
// 只认 cover / folder 两个名字 + 常见图片格式 —— 这是音乐目录里的通用约定，
// 上游也是这个口径（organize.py 的 DIR_COVER_RE）。
func isDirCoverName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp":
	default:
		return false
	}
	switch strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))) {
	case "cover", "folder":
		return true
	}
	return false
}

// associateDirCovers 把「目录里的专辑封面」挂到对应的搬移动作上。
//
// 规则（照上游 organize.py 的封面阶段）：
//   - 这个目录里的歌**必须全部**都被搬走；有跳过的就说不清这张封面归谁；
//   - 这些歌必须**属于同一个专辑**（目录里就一首歌时也算清楚）；
//   - 满足上面两条，才把封面复制到目标专辑目录，**保持原文件名**
//     （cover.jpg 还是 cover.jpg，不改成歌名 —— 一个专辑一张封面）。
//
// 目标位置已经有同名文件时：内容一样就当已经搬过了（跳过）；内容不一样就
// 保留原来的、并在方案里说明 —— 覆盖用户自己的图片是静默的数据损失。
func associateDirCovers(plan *Plan, dirCovers map[string][]string) {
	if len(dirCovers) == 0 {
		return
	}
	// 先按源目录把条目归堆。
	byDir := map[string][]int{}
	for i, item := range plan.Items {
		byDir[filepath.Dir(item.Source)] = append(byDir[filepath.Dir(item.Source)], i)
	}
	// 已经挂在某一首上的封面移动，别重复挂。
	used := map[string]bool{}

	for dir, covers := range dirCovers {
		idxs := byDir[dir]
		if len(idxs) == 0 {
			continue
		}
		// 这些歌是不是同一个专辑？
		albums := map[string]bool{}
		albumEmpty := false
		organized := 0
		for _, i := range idxs {
			item := plan.Items[i]
			album := strings.TrimSpace(item.Meta.Album)
			if album == "" {
				albumEmpty = true
			} else {
				albums[album] = true
			}
			if item.Status == "move" {
				organized++
			}
		}
		associated := len(idxs) == 1 || (len(albums) == 1 && !albumEmpty)
		if organized != len(idxs) || !associated {
			for _, c := range covers {
				plan.Warnings = append(plan.Warnings,
					"封面归属不明，保留原样："+filepath.Base(c))
			}
			continue
		}
		// 目标专辑目录（同一专辑的歌会落到同一个目录）。
		dests := map[string]bool{}
		for _, i := range idxs {
			item := plan.Items[i]
			if item.Status != "move" {
				continue
			}
			dests[filepath.Dir(item.Target)] = true
		}
		for _, c := range covers {
			if used[c] {
				continue
			}
			for dest := range dests {
				target := filepath.Join(dest, filepath.Base(c))
				if samePath(c, target) {
					continue
				}
				if fileExists(target) {
					if sameFileBytes(c, target) {
						continue // 内容一样，当作已经搬过
					}
					plan.Warnings = append(plan.Warnings,
						"目标已有不同的封面，保留原图："+filepath.Base(target))
					continue
				}
				// 挂在第一首身上：搬歌时顺手把封面带过去。
				for _, i := range idxs {
					item := &plan.Items[i]
					if item.Status != "move" {
						continue
					}
					// Kind 用 "albumcover" 而不是 "cover"：track 自带的封面
					// 也是 "cover"，两种要分得开（撤销与统计都看这个字段）。
					item.Moves = append(item.Moves, Move{From: c, To: target, Kind: "albumcover"})
					plan.Stats.Sidecars++
					used[c] = true
					break
				}
			}
		}
	}
}

// sameFileBytes 判断两个文件内容是否一致（先比大小，再比摘要）。
func sameFileBytes(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	if fa.Size() != fb.Size() {
		return false
	}
	da, err := sha256File(a)
	if err != nil {
		return false
	}
	db, err := sha256File(b)
	if err != nil {
		return false
	}
	return da == db
}

// sha256File 算一个文件的 SHA-256 摘要（十六进制字符串）。
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Apply 执行整理方案。
//
// 只搬 Status == "move" 的条目；冲突与跳过的原样不动。
// 每搬一个文件就往 Journal 里记一条**反向**记录，Undo 靠它搬回去。
func Apply(plan Plan) (Result, error) {
	res := Result{Root: plan.Root}
	if plan.Root == "" {
		return res, fmt.Errorf("整理方案里没有根目录")
	}
	root := plan.Root
	// 源目录集合：搬完要清理搬空了的目录。
	touchedDirs := map[string]bool{}

	for _, item := range plan.Items {
		if item.Status != "move" {
			continue
		}
		for _, mv := range item.Moves {
			if err := moveFile(mv.From, mv.To); err != nil {
				res.Failed++
				if len(res.Errors) < 50 {
					res.Errors = append(res.Errors, filepath.Base(mv.From)+": "+err.Error())
				}
				continue
			}
			res.Journal = append(res.Journal, Move{From: mv.To, To: mv.From, Kind: mv.Kind})
			if mv.Kind == "audio" {
				res.Moved++
			} else {
				res.Sidecars++
			}
			touchedDirs[filepath.Dir(mv.From)] = true
		}
	}

	// 清理搬空了的目录（只清根目录以下、且确实空了的）。
	for dir := range touchedDirs {
		cleaned := removeEmptyDirs(dir, root)
		res.DirsClean += cleaned
	}
	return res, nil
}

// Undo 把一次整理搬回原位（按 Journal 逆序）。
//
// 逆序是必须的：先搬回去的是「后来才被搬走的」，把它放回原目录时，
// 那个目录可能正是另一首歌刚被搬空、正在清理中的目录 —— 正序会把它清掉。
func Undo(journal []Move, root string) (Result, error) {
	res := Result{Root: root}
	vacated := map[string]bool{}
	for i := len(journal) - 1; i >= 0; i-- {
		mv := journal[i]
		if err := moveFile(mv.From, mv.To); err != nil {
			res.Failed++
			if len(res.Errors) < 50 {
				res.Errors = append(res.Errors, filepath.Base(mv.From)+": "+err.Error())
			}
			continue
		}
		if mv.Kind == "audio" {
			res.Moved++
		} else {
			res.Sidecars++
		}
		// 歌搬回去了，整理时新建的「歌手/专辑」目录就空了 —— 顺手收掉，
		// 否则撤销之后目录里留下一串空文件夹，看着像没撤干净。
		vacated[filepath.Dir(mv.From)] = true
	}
	for _, dir := range deepestFirst(vacated) {
		res.DirsClean += removeEmptyDirs(dir, root)
	}
	return res, nil
}

// ── 元数据 ──

// resolveMeta 按「标签 → 曲库索引 → 文件名」的顺序拼出一首歌的元数据。
//
// 顺序有讲究：标签最权威（那是音乐文件自己带的），曲库索引次之（扫描时读过的），
// 文件名最后（只是「歌手 - 歌名」这个约定）。任何一步缺字段就往下补，
// 而不是整份替换 —— 标签里有歌手没有专辑时，专辑该由文件名/索引补。
func resolveMeta(path string, opts Options) Meta {
	m := Meta{}
	sources := map[string]bool{}

	if opts.Lookup != nil {
		if v, ok := opts.Lookup(path); ok {
			m = v
			sources["library"] = true
		}
	}
	if t, err := tags.Read(path); err == nil {
		if v := strings.TrimSpace(t.Title); v != "" {
			m.Title = v
			sources["tag"] = true
		}
		if v := strings.TrimSpace(t.Artist); v != "" {
			m.Artist = v
			sources["tag"] = true
		}
		if v := strings.TrimSpace(t.Album); v != "" {
			m.Album = v
			sources["tag"] = true
		}
		if v := strings.TrimSpace(t.Genre); v != "" && m.Genre == "" {
			m.Genre = v
			sources["tag"] = true
		}
		if t.Track > 0 && m.Track == 0 {
			m.Track = t.Track
			sources["tag"] = true
		}
		if t.Disc > 0 && m.Disc == 0 {
			m.Disc = t.Disc
			sources["tag"] = true
		}
		if t.Year > 0 && m.Year == 0 {
			m.Year = t.Year
			sources["tag"] = true
		}
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	fnArtist, fnTitle := namefmt.Parse(base)
	if m.Artist == "" || m.Title == "" {
		if m.Artist == "" && fnArtist != "" {
			m.Artist = fnArtist
			sources["filename"] = true
		}
		if m.Title == "" && fnTitle != "" {
			m.Title = fnTitle
			sources["filename"] = true
		}
	}
	// 文件名开头带序号（`01 双截棍`）时，把它当曲序用 —— 这比完全没有序号强。
	// 取出来之后要从歌名里摘掉：`05 探针 支持吗.flac` 留着序号就会渲染成
	// `05 05 探针 支持吗.flac`。只在歌名本身也来自文件名时才摘，标签里的真歌名不动。
	if m.Track == 0 {
		if n, rest := splitLeadingTrack(base); n > 0 {
			m.Track = n
			sources["filename"] = true
			if strings.TrimSpace(m.Title) == strings.TrimSpace(fnTitle) {
				m.Title = rest
			}
		}
	}

	if strings.TrimSpace(m.Artist) == "" {
		// 文件名里没有「歌手 - 歌名」这种写法时，先看这个文件是不是已经在
		// 「歌手/专辑/歌名」两层目录里 —— 整理过一遍之后再预演，才不会因为
		// 新文件名里没有歌手，把整库又搬进「未知歌手 / 未知专辑」。
		if artist, album, ok := dirMeta(opts.Root, path); ok {
			m.Artist = artist
			sources["dir"] = true
			if strings.TrimSpace(m.Album) == "" {
				m.Album = album
				sources["dir"] = true
			}
		}
	}
	if strings.TrimSpace(m.Artist) == "" {
		m.Artist = UnknownArtist
	}
	if strings.TrimSpace(m.Album) == "" {
		m.Album = UnknownAlbum
	}
	if strings.TrimSpace(m.Title) == "" {
		m.Title = UnknownTitle
	}
	m.Source = joinSources(sources)
	return m
}

// assignFallbackTracks 给没有曲序的歌按「同专辑内出现顺序」补序号。
//
// 同一专辑里按歌名排序后依次编号（不是按扫描顺序）—— 扫描顺序取决于文件系统，
// 每次跑都可能不一样；按歌名排至少是稳定的。
func assignFallbackTracks(files []dirwalk.File, metas []Meta, enable bool) {
	if !enable {
		return
	}
	groups := map[string][]int{}
	for i, m := range metas {
		groups[m.Artist+"\x00"+m.Album] = append(groups[m.Artist+"\x00"+m.Album], i)
	}
	for _, idxs := range groups {
		sort.Slice(idxs, func(a, b int) bool {
			return strings.ToLower(metas[idxs[a]].Title) < strings.ToLower(metas[idxs[b]].Title)
		})
		n := 0
		for _, i := range idxs {
			if metas[i].Track > 0 {
				continue
			}
			n++
			metas[i].Track = n
		}
	}
}

// ── 模板渲染 ──

// renderPath 按目录模板算出相对路径（用 "/" 分层）。
func renderPath(tpl string, m Meta, ext string) string {
	if strings.TrimSpace(tpl) == "" {
		return ""
	}
	segs := strings.FieldsFunc(tpl, func(r rune) bool { return r == '/' || r == '\\' })
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		// 「往上跳一层」在整理场景里没有意义：模板写 `../外面` 时
		// 一律当没写（而不是真的把文件搬到整理目录外面去）。
		if s == "." || s == ".." {
			continue
		}
		v := filename.Sanitize(render(s, m, ext))
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return strings.Join(out, "/")
}

// renderFile 按文件名模板算出文件名主干（不含扩展名）。
func renderFile(tpl string, m Meta, ext string) string {
	v := filename.Sanitize(render(tpl, m, ext))
	return v
}

// render 是模板展开的核心：把 {key} / {key:0Nd} 换成实际值。
func render(tpl string, m Meta, ext string) string {
	if tpl == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(tpl); {
		c := tpl[i]
		if c != '{' {
			b.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(tpl[i:], '}')
		if end < 0 {
			// 没闭合的 '{'：原样输出（用户多半正在输入半截模板）。
			b.WriteString(tpl[i:])
			break
		}
		spec := tpl[i+1 : i+end]
		i += end + 1
		key, width := spec, 0
		if j := strings.IndexByte(spec, ':'); j >= 0 {
			key = spec[:j]
			width = parseWidth(spec[j+1:])
		}
		val := placeholderValue(key, m, ext)
		if width > 0 && val != "" {
			if n, err := strconv.Atoi(val); err == nil {
				val = fmt.Sprintf("%0*d", width, n)
			}
		}
		b.WriteString(val)
	}
	// 折叠空白：占位符为空时会留下多余空格（「  双截棍」）。
	out := strings.Join(strings.Fields(b.String()), " ")
	return strings.TrimSpace(out)
}

// placeholderValue 取占位符对应的值。
func placeholderValue(key string, m Meta, ext string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "artist":
		return m.Artist
	case "album":
		return m.Album
	case "title":
		return m.Title
	case "track":
		if m.Track > 0 {
			return strconv.Itoa(m.Track)
		}
		return ""
	case "disc":
		if m.Disc > 0 {
			return strconv.Itoa(m.Disc)
		}
		return ""
	case "year":
		if m.Year > 0 {
			return strconv.Itoa(m.Year)
		}
		return ""
	case "genre":
		return m.Genre
	case "ext":
		return ext
	default:
		return ""
	}
}

// parseWidth 解析 "02d" / "2" 这样的宽度说明，返回补零宽度（0 = 不补）。
func parseWidth(spec string) int {
	spec = strings.TrimSpace(spec)
	spec = strings.TrimSuffix(spec, "d")
	spec = strings.TrimPrefix(spec, "0")
	n, err := strconv.Atoi(spec)
	if err != nil || n <= 0 || n > 9 {
		return 0
	}
	return n
}

// ── 文件操作 ──

// moveFile 把文件搬到新位置，绝不覆盖已存在的目标。
//
// 为什么不用 os.Rename 一条路：rename 在同设备上是原子的，但**目标已存在时
// 会静默覆盖**。预演（PlanOf）到执行（Apply）之间隔着用户点「确认」的那几秒，
// 期间完全可能有人往同一个名字里放了东西；覆盖掉就是丢别人的歌。
// link(2) 在目标已存在时原子失败，正好是我们需要的语义。
//
// 跨设备（NAS 上 /vol1 与 /vol3 是两个挂载点，rename 直接 EXDEV）以及不支持
// 硬链接的文件系统，退回「流式复制 + 校验 + 排他安装」——不能像原来那样
// ReadFile 整个文件进内存：一张 200MB 的 wav 会把进程内存打爆。
func moveFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := os.Link(from, to); err == nil {
		// 同设备：新建一个硬链接（原子、排他），再把旧名字摘掉。
		return os.Remove(from)
	} else if errors.Is(err, os.ErrExist) {
		return err
	}
	if err := copyFileExclusive(from, to); err != nil {
		return err
	}
	return os.Remove(from)
}

// copyFileExclusive 流式复制 + 大小 & SHA-256 校验 + 排他安装，成功后调用方删源。
//
// 临时文件建在**目标目录**里（不是系统临时目录）：这样最后的安装只是一次
// 同设备 link，既不会跨设备失败，也不会在目标盘上多留一份 .part。
func copyFileExclusive(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(to), ".organize-copy-*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	digest := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, digest), in)
	if copyErr == nil {
		copyErr = tmp.Sync()
	}
	if cerr := tmp.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		cleanup()
		return copyErr
	}
	if n != fi.Size() {
		cleanup()
		return fmt.Errorf("复制不完整：写入了 %d 字节，源文件 %d 字节", n, fi.Size())
	}
	if err := verifyDigest(tmpName, digest.Sum(nil)); err != nil {
		cleanup()
		return err
	}
	if err := installExclusive(tmpName, to); err != nil {
		cleanup()
		return err
	}
	return os.Remove(tmpName)
}

// installExclusive 把已校验的临时文件排他地装到目标路径。
func installExclusive(tmpName, to string) error {
	if err := os.Link(tmpName, to); err == nil {
		return nil
	} else if errors.Is(err, os.ErrExist) {
		return err
	}
	// 目标目录不支持硬链接时退化：先探存在、再 rename（窗口很小）。
	if fileExists(to) {
		return os.ErrExist
	}
	return os.Rename(tmpName, to)
}

// verifyDigest 重新读一遍刚落盘的文件并比对摘要。
//
// 为什么要读回：写错误、NAS 上的静默截断都不会让 Write 报错，只有读回才能发现。
// 文件刚写完还在页缓存里，这次读基本不碰盘。
func verifyDigest(path string, want []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("复制校验失败：落盘内容与源文件不一致")
	}
	return nil
}

// removeEmptyDirs 从 dir 往上删空目录，直到 root（不含 root 本身）。
// deepestFirst 把一组目录按层级从深到浅排好（去空目录要从最里层往外删，
// 否则外层目录还含着一个空目录，就永远删不掉）。
func deepestFirst(dirs map[string]bool) []string {
	out := make([]string, 0, len(dirs))
	for d := range dirs {
		out = append(out, d)
	}
	sort.Slice(out, func(a, b int) bool {
		da := strings.Count(out[a], string(filepath.Separator))
		db := strings.Count(out[b], string(filepath.Separator))
		if da != db {
			return da > db
		}
		return out[a] < out[b]
	})
	return out
}

func removeEmptyDirs(dir, root string) int {
	n := 0
	cur := filepath.Clean(dir)
	root = filepath.Clean(root)
	for cur != root && withinRoot(root, cur) {
		entries, err := os.ReadDir(cur)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(cur); err != nil {
			break
		}
		n++
		cur = filepath.Dir(cur)
	}
	return n
}

// ── 小工具 ──

func readDirEntries(dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return entries
}

func isSidecarExt(ext string) bool {
	for _, e := range sidecarExts {
		if e == ext {
			return true
		}
	}
	return false
}

func kindOf(ext string) string {
	switch ext {
	case "lrc", "txt":
		return "lyric"
	case "jpg", "jpeg", "png", "webp":
		return "cover"
	default:
		return "other"
	}
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// targetTaken 报告目标路径是否已被占用：本方案里另一首歌已经定过这个位置，
// 或者盘上已经有同名文件（用户手放的、上次整理失败留下的）。
func targetTaken(occupied map[string]string, target string) bool {
	if occupied[target] != "" {
		return true
	}
	return fileExists(target)
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// withinRoot 判断 path 是否在 root 之内（root 自身算在内）。
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..")
}

// splitLeadingTrack 取文件名开头的序号，并返回摘掉序号后的正文
// （`01 双截棍` → 1, `双截棍`；`04` → 0, `04`）。
//
// 为什么要一起把正文返回：序号本来就在文件名里，取出来当曲序之后如果还留在
// 歌名里，渲染模板时就变成 `01 01 双截棍.flac` —— 序号翻倍。
func splitLeadingTrack(base string) (int, string) {
	i := 0
	for i < len(base) && base[i] >= '0' && base[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, base
	}
	// 纯数字（没有分隔符）不当序号用 —— 那更可能是年份或别的编号。
	if i == len(base) {
		return 0, base
	}
	// 数字后面必须紧跟分隔符才认：`2001年的第一场雪` 不该被当成第 2001 首。
	trimmed := strings.TrimLeftFunc(base[i:], func(r rune) bool {
		switch r {
		case ' ', '\t', '-', '.', '_', '、', '．', '·':
			return true
		}
		return false
	})
	if trimmed == base[i:] || strings.TrimSpace(trimmed) == "" {
		return 0, base
	}
	n, err := strconv.Atoi(base[:i])
	if err != nil || n <= 0 || n > 999 {
		return 0, base
	}
	return n, strings.TrimSpace(trimmed)
}

// dirMeta 从「已经躺在 歌手/专辑/歌名 两层目录里」的文件身上取回歌手与专辑。
//
// 只在文件名给不出歌手时才用（整理之后文件名里通常只剩歌名）。要求至少两层
// 目录：只有一层（`散放/歌名.flac`）更像是用户自己的分类习惯，不是歌手名，
// 拿来当歌手会把「散放」写进目录结构中。
func dirMeta(root, path string) (string, string, bool) {
	root = filepath.Clean(root)
	if root == "" || root == "." {
		return "", "", false
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", "", false
	}
	segs := strings.Split(filepath.ToSlash(rel), "/")
	if len(segs) < 3 {
		return "", "", false
	}
	artist := strings.TrimSpace(segs[0])
	album := strings.TrimSpace(segs[1])
	if artist == "" {
		return "", "", false
	}
	return artist, album, true
}

// joinSources 把元数据来源列表拼成一句话。
func joinSources(sources map[string]bool) string {
	if len(sources) == 0 {
		return "unknown"
	}
	order := []string{"tag", "library", "filename", "dir"}
	out := make([]string, 0, len(order))
	for _, s := range order {
		if sources[s] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return strings.Join(out, "+")
}
