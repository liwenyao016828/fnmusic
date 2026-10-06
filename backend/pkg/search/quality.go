package search

import (
	"sort"
	"strconv"
	"strings"
)

// ============================================================================
//  音质档位与解析
//
//  设计要点：
//   1. 档位常量必须与前端 frontend/src/engine/quality.js 的 QUALITY_LADDER 严格对齐。
//      前端 buildQualityTiers 只按阶梯排序，非阶梯值会被追加到末尾兜底——
//      若此处产生非阶梯值，会导致"最高音质"排序错乱。
//   2. 搜索结果里的 Qualitys 是**事实**（该曲在该平台真实存在哪些档位），
//      由各平台返回的码率/文件尺寸字段推导，不再硬编码。
//   3. 所有解析函数对缺失/异常数据都退化为"不产生该档位"，绝不臆造。
// ============================================================================

// 标准音质档位（与前端 QUALITY_LADDER 一致）
const (
	Quality128k   = "128k"
	Quality192k   = "192k"
	Quality320k   = "320k"
	QualityFlac   = "flac"
	QualityFlac24 = "flac24bit"
)

// normalizeQualityKey 归一化音质键：去空白 + 转小写。
// 音源脚本约定小写，搜索结果里可能出现 "320K"/"FLAC"，必须统一。
func normalizeQualityKey(q string) string {
	return strings.ToLower(strings.TrimSpace(q))
}

// qualityRank 返回档位高低（越大越高）；非阶梯值返回 -1。
func qualityRank(q string) int {
	switch normalizeQualityKey(q) {
	case QualityFlac24:
		return 5
	case QualityFlac:
		return 4
	case Quality320k:
		return 3
	case Quality192k:
		return 2
	case Quality128k:
		return 1
	}
	return -1
}

// sortQualitysDesc 去重并按档位从高到低排序。
// 非阶梯值（音源自定义档位）统一排在标准档位之后，内部按字典序，保证输出确定性。
func sortQualitysDesc(qs []string) []string {
	seen := make(map[string]bool, len(qs))
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		n := normalizeQualityKey(q)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := qualityRank(out[i]), qualityRank(out[j])
		switch {
		case ri < 0 && rj < 0:
			return out[i] < out[j]
		case ri < 0:
			return false // 非阶梯值排后
		case rj < 0:
			return true
		default:
			return ri > rj
		}
	})
	return out
}

// bestQuality 返回最高可用档位；无可用档位时返回空串。
func bestQuality(qs []string) string {
	sorted := sortQualitysDesc(qs)
	if len(sorted) == 0 {
		return ""
	}
	return sorted[0]
}

// qualityFromBitrateKbps 把码率（kbps）映射到标准档位。
// 低于 128k 的档位（如 aac48 / ogg96）不在阶梯内，返回空串表示忽略。
//
// 无损阈值取 500：有损编码上限为 320k，而实测无损档普遍在 900kbps 以上
// （酷狗 SQ=937、Res=1642），取 500 可稳妥区分两者。
func qualityFromBitrateKbps(kbps int) string {
	switch {
	case kbps >= 500:
		return QualityFlac
	case kbps >= 256:
		return Quality320k
	case kbps >= 160:
		return Quality192k
	case kbps >= 112:
		return Quality128k
	}
	return ""
}

// ---------------------------------------------------------------------------
// 网易云
// ---------------------------------------------------------------------------

// neteaseLevelInfo 网易为每档音质提供的对象；该档不可用时字段为 null。
type neteaseLevelInfo struct {
	Br int `json:"br"` // 码率 bps
}

// neteaseQualitys 依据 l/m/h/sq/hr 是否存在推导可用档位。
// 实测：l=128000 m=192000 h=320000 sq≈478k~1.6M（无损） hr≈1.39M~4.95M（Hi-Res）。
// 注意 privilege.maxbr 实测恒为 999000，无法区分无损与 Hi-Res，故不作为判据。
func neteaseQualitys(l, m, h, sq, hr *neteaseLevelInfo) []string {
	qs := make([]string, 0, 5)
	if l != nil && l.Br > 0 {
		qs = append(qs, Quality128k)
	}
	if m != nil && m.Br > 0 {
		qs = append(qs, Quality192k)
	}
	if h != nil && h.Br > 0 {
		qs = append(qs, Quality320k)
	}
	if sq != nil && sq.Br > 0 {
		qs = append(qs, QualityFlac)
	}
	if hr != nil && hr.Br > 0 {
		qs = append(qs, QualityFlac24)
	}
	return sortQualitysDesc(qs)
}

// ---------------------------------------------------------------------------
// QQ 音乐
// ---------------------------------------------------------------------------

// qqQualitys 依据各档文件尺寸推导可用档位（尺寸为 0 表示该档不存在）。
// 实测 client_search_cp 接口返回 size128/size320/sizeflac/sizeape，
// 不返回 sizehires；但仍保留该字段解析，以便接口版本变化时自动生效。
func qqQualitys(size128, size320, sizeflac, sizehires, sizeape int64) []string {
	qs := make([]string, 0, 5)
	if size128 > 0 {
		qs = append(qs, Quality128k)
	}
	if size320 > 0 {
		qs = append(qs, Quality320k)
	}
	if sizeflac > 0 {
		qs = append(qs, QualityFlac)
	}
	if sizehires > 0 {
		qs = append(qs, QualityFlac24)
	}
	// APE 为无损的一种封装，归入 flac 档（阶梯中无独立 ape 档）
	if sizeape > 0 && sizeflac <= 0 {
		qs = append(qs, QualityFlac)
	}
	return sortQualitysDesc(qs)
}

// ---------------------------------------------------------------------------
// 酷狗
// ---------------------------------------------------------------------------

// kugouQualitys 依据各档 FileHash 是否为空推导可用档位。
// 实测对应关系：FileHash(128k) / HQFileHash(320k) / SQFileHash(≈937kbps 无损)
//
//	/ ResFileHash(≈1642kbps 24bit) / SuperFileHash(超品，同为高码率)
func kugouQualitys(fileHash, hqFileHash, sqFileHash, resFileHash, superFileHash string) []string {
	nonEmpty := func(s string) bool { return strings.TrimSpace(s) != "" }
	qs := make([]string, 0, 5)
	if nonEmpty(fileHash) {
		qs = append(qs, Quality128k)
	}
	if nonEmpty(hqFileHash) {
		qs = append(qs, Quality320k)
	}
	if nonEmpty(sqFileHash) {
		qs = append(qs, QualityFlac)
	}
	if nonEmpty(resFileHash) || nonEmpty(superFileHash) {
		qs = append(qs, QualityFlac24)
	}
	return sortQualitysDesc(qs)
}

// ---------------------------------------------------------------------------
// 酷我
// ---------------------------------------------------------------------------

// kuwoQualitys 解析酷我的 MINFO / N_MINFO 字段。
//
// 格式：level:<等级>,bitrate:<kbps>,format:<封装>,size:<体积>，多项以 ";" 分隔。
// 实测样例：
//
//	level:ff,bitrate:2000,format:flac,...   -> 无损
//	level:zp,bitrate:20000,format:zp,...    -> 臻品（Hi-Res，bitrate 为哨兵值）
//	level:p,bitrate:320,format:mp3,...      -> 320k
//	level:p,bitrate:192,format:ogg,...      -> 192k
//	level:h,bitrate:128,format:mp3,...      -> 128k
//	level:s,bitrate:48,format:aac,...       -> 低于阶梯下限，忽略
//
// 注意：zp 档的 bitrate=20000 是哨兵值而非真实码率，必须先按 format/level 判定，
//
//	再回退到码率映射，否则会被误判成无损。
func kuwoQualitys(minfo string) []string {
	qs := make([]string, 0, 4)
	for _, seg := range strings.Split(minfo, ";") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		var level, format string
		bitrate := 0
		for _, kv := range strings.Split(seg, ",") {
			kv = strings.TrimSpace(kv)
			idx := strings.Index(kv, ":")
			if idx <= 0 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(kv[:idx]))
			val := strings.TrimSpace(kv[idx+1:])
			switch key {
			case "level":
				level = strings.ToLower(val)
			case "format":
				format = strings.ToLower(val)
			case "bitrate":
				if n, err := strconv.Atoi(val); err == nil {
					bitrate = n
				}
			}
		}

		switch {
		case format == "flac":
			qs = append(qs, QualityFlac)
		case format == "zp" || level == "zp":
			// 臻品音质（Hi-Res）。阶梯中对应最高档 flac24bit；
			// 不使用 "hires" —— 它不在 QUALITY_LADDER 内，会被前端排到最后兜底。
			qs = append(qs, QualityFlac24)
		default:
			if q := qualityFromBitrateKbps(bitrate); q != "" {
				qs = append(qs, q)
			}
		}
	}
	return sortQualitysDesc(qs)
}

// kuwoQualitysFromFormats 解析酷我 FORMATS 字段（MINFO 缺失时的兜底）。
// 形如 "AAC48|MP3128|MP3H|WMA128|WMA96|ALFLAC|OGG192|OGG96"。
func kuwoQualitysFromFormats(formats string) []string {
	qs := make([]string, 0, 4)
	for _, tok := range strings.Split(formats, "|") {
		t := strings.ToUpper(strings.TrimSpace(tok))
		switch {
		case t == "ALFLAC" || t == "FLAC":
			qs = append(qs, QualityFlac)
		case t == "MP3H" || t == "OGGH":
			qs = append(qs, Quality320k)
		case strings.HasPrefix(t, "OGG"):
			if n, err := strconv.Atoi(strings.TrimPrefix(t, "OGG")); err == nil {
				if q := qualityFromBitrateKbps(n); q != "" {
					qs = append(qs, q)
				}
			}
		case strings.HasPrefix(t, "MP3"):
			if n, err := strconv.Atoi(strings.TrimPrefix(t, "MP3")); err == nil {
				if q := qualityFromBitrateKbps(n); q != "" {
					qs = append(qs, q)
				}
			}
		}
	}
	return sortQualitysDesc(qs)
}

// kuwoResolveQualitys 优先用 N_MINFO/MINFO，缺失时回退 FORMATS。
func kuwoResolveQualitys(nMinfo, minfo, formats string) []string {
	if qs := kuwoQualitys(nMinfo); len(qs) > 0 {
		return qs
	}
	if qs := kuwoQualitys(minfo); len(qs) > 0 {
		return qs
	}
	return kuwoQualitysFromFormats(formats)
}
