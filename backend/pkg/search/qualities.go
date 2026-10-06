package search

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ============================================================================
//  GET /api/music/qualities —— 查询某首歌在指定平台真实可用的音质档位
//
//  为什么用「搜索 + 按 ID 匹配」而不是各平台的详情接口？
//  实测四个平台的详情接口普遍不可用：
//    · 网易 song/detail 不返回音质字段
//    · QQ 详情缺无损档
//    · 酷狗返回空壳
//    · 酷我需要额外 token
//
//  而**搜索结果里已经带了真实音质**（由 quality.go 从各平台码率/文件尺寸
//  字段推导）。所以这里用「曲名+歌手 搜索 → 按 ID 精确匹配」取同一首歌的音质，
//  数据来源与 /api/search 完全一致，不会出现两处口径不一。
//
//  绝不臆造：匹配不到就返回空列表 + matched_by="none"，由调用方决定怎么处理。
// ============================================================================

// officialPlatforms 官方协议覆盖的平台（与 pkg/protocol.OfficialPlatforms 保持一致）。
var officialPlatforms = map[string]bool{
	"wy": true,
	"tx": true,
	"kg": true,
	"kw": true,
}

// searchByPlatform 按平台分发到对应的搜索实现。
// 这里**不调用 filterAndRankSongs** —— 去噪会剔除部分结果，
// 而本接口的目标是"尽量找到那一首"，召回优先于排序。
func searchByPlatform(platform, keyword string, page, limit int) []UnifiedSong {
	switch platform {
	case "wy":
		return searchNetEase(keyword, page, limit)
	case "tx":
		return searchQQ(keyword, page, limit)
	case "kg":
		return searchKuGou(keyword, page, limit)
	case "kw":
		return searchKuWo(keyword, page, limit)
	}
	return nil
}

// normalizeForMatch 归一化用于匹配的字符串：
// 转小写、去掉括号及其内容、去掉空白与常见分隔符。
func normalizeForMatch(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))

	// 去掉成对括号及其内容（中英文 + 方括号 + 书名号）
	for _, pair := range [][2]string{
		{"(", ")"}, {"（", "）"}, {"[", "]"}, {"【", "】"},
	} {
		for {
			i := strings.Index(s, pair[0])
			if i < 0 {
				break
			}
			rest := s[i+len(pair[0]):]
			j := strings.Index(rest, pair[1])
			if j < 0 {
				break
			}
			s = s[:i] + rest[j+len(pair[1]):]
		}
	}

	r := strings.NewReplacer(
		" ", "", "\t", "", "-", "", "_", "",
		"·", "", "・", "", ".", "", "．", "",
		"，", "", ",", "", "、", "", "&", "", "/", "",
	)
	return r.Replace(s)
}

// matchByID 按 id / songmid / hash 精确匹配。
//
// 各平台填的字段不同（见 aggregator.go）：
//   - 网易：Songmid = 数字 ID
//   - QQ  ：ID = "tx_"+songmid，Songmid = songmid
//   - 酷狗：ID = "kg_"+FileHash，Songmid = Hash = FileHash
//   - 酷我：Songmid = rid
//
// 所以依次比对 Songmid、完整 ID、以及 "前缀_ID" 形式的后半段。
func matchByID(songs []UnifiedSong, id, hash string) (UnifiedSong, bool) {
	if id == "" && hash == "" {
		return UnifiedSong{}, false
	}
	for _, s := range songs {
		if id != "" {
			if s.Songmid == id || s.ID == id {
				return s, true
			}
			// 形如 "tx_0039MnYb0qxYhV"，传入的是后半段
			if i := strings.Index(s.ID, "_"); i >= 0 && s.ID[i+1:] == id {
				return s, true
			}
		}
		if hash != "" && s.Hash == hash {
			return s, true
		}
	}
	return UnifiedSong{}, false
}

// MatchByNameSinger 按「曲名 + 歌手」在候选里挑一条（供整理补全等包内调用方使用）。
//
// 判据与 /api/music/qualities 完全一致：曲名归一化后必须相等；
// 歌手在双方都非空时要求有交集，否则只看曲名。
// 复用同一套逻辑，避免「音质查询认得出、补全认不出」这种前后不一致。
func MatchByNameSinger(songs []UnifiedSong, name, singer string) (UnifiedSong, bool) {
	return matchByNameSinger(songs, name, singer)
}

// matchByNameSinger 按曲名 + 歌手匹配（ID 因平台改版失效时的兜底）。
// 曲名必须归一化后完全相等；歌手在双方都非空时要求有交集，否则只看曲名。
func matchByNameSinger(songs []UnifiedSong, name, singer string) (UnifiedSong, bool) {
	wantName := normalizeForMatch(name)
	if wantName == "" {
		return UnifiedSong{}, false
	}
	wantSinger := normalizeForMatch(singer)

	for _, s := range songs {
		if normalizeForMatch(s.Name) != wantName {
			continue
		}
		if wantSinger == "" {
			return s, true
		}
		got := normalizeForMatch(s.Singer)
		// 歌手可能是 "周杰伦" 或 "蔡依林, 周杰伦"，做双向包含判定
		if got == wantSinger || strings.Contains(got, wantSinger) || strings.Contains(wantSinger, got) {
			return s, true
		}
	}
	return UnifiedSong{}, false
}

// writeQualities 统一输出。qualitys 为 nil 时返回空数组（不臆造档位）。
func writeQualities(w http.ResponseWriter, source, id string, qs []string, matchedBy string, checked int) {
	sorted := sortQualitysDesc(qs)
	if sorted == nil {
		sorted = []string{}
	}
	best := bestQuality(sorted)

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"message": "ok",
		"data": map[string]interface{}{
			"source":             source,
			"id":                 id,
			"qualitys":           sorted,
			"best":               best,
			"matched_by":         matchedBy, // id | name_singer | none
			"candidates_checked": checked,
		},
		// 同时给扁平字段，方便外部程序少解一层
		"qualitys":   sorted,
		"best":       best,
		"matched_by": matchedBy,
	})
}

// HandleQualities 处理 GET /api/music/qualities
//
// 参数：
//
//	source  必填，wy / tx / kg / kw
//	id      必填（或 songmid / hash），平台内的歌曲标识
//	name    建议填，缺失时无法搜索，将直接返回空音质列表
//	singer  可选，用于提高匹配准确度
//	hash    可选，酷狗等以 hash 为标识的平台可只传它
func HandleQualities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	q := r.URL.Query()
	source := strings.ToLower(strings.TrimSpace(q.Get("source")))
	if source == "" {
		source = strings.ToLower(strings.TrimSpace(q.Get("platform")))
	}
	id := strings.TrimSpace(q.Get("id"))
	if id == "" {
		id = strings.TrimSpace(q.Get("songmid"))
	}
	hash := strings.TrimSpace(q.Get("hash"))
	name := strings.TrimSpace(q.Get("name"))
	singer := strings.TrimSpace(q.Get("singer"))

	if !officialPlatforms[source] {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    400,
			"message": "不支持的平台，仅支持 wy / tx / kg / kw",
		})
		return
	}
	if id == "" && hash == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    400,
			"message": "需要 id（或 songmid / hash）",
		})
		return
	}

	// 没有曲名就搜不了 —— 直接返回空，不猜
	keyword := strings.TrimSpace(name + " " + singer)
	if keyword == "" {
		writeQualities(w, source, id, nil, "none", 0)
		return
	}

	songs := searchByPlatform(source, keyword, 1, 40)

	// 1. 优先按 ID 精确匹配（最可靠）
	if s, ok := matchByID(songs, id, hash); ok {
		writeQualities(w, source, id, s.Qualitys, "id", len(songs))
		return
	}

	// 2. 退一步按 曲名+歌手 匹配（ID 可能因平台改版而失效）
	if s, ok := matchByNameSinger(songs, name, singer); ok {
		writeQualities(w, source, id, s.Qualitys, "name_singer", len(songs))
		return
	}

	writeQualities(w, source, id, nil, "none", len(songs))
}
