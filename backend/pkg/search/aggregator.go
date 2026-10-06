package search

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type UnifiedSong struct {
	ID       string   `json:"id"`
	Songmid  string   `json:"songmid"`
	Hash     string   `json:"hash"`
	Name     string   `json:"name"`
	Singer   string   `json:"singer"`
	Album    string   `json:"album"`
	Cover    string   `json:"cover"`
	Source   string   `json:"source"` // "wy", "tx", "kg", "kw", "mg"
	Duration int      `json:"duration"`
	Interval int      `json:"interval"`
	Quality  string   `json:"quality"`
	Qualitys []string `json:"qualitys"`
	// FileSize 是平台声明的文件体积（字节），0 = 平台没给。
	//
	// ⚠️ 它只用于**同档位之间**比大小（「无损优先，同档比体积」——同一首歌、同一档位，
	// 文件更大 = 码率更高）。**不要**拿它跨档位比较，也**不要**把 0 当成「很小」：
	// 0 是「没有信息」，不是「体积为零」。外挂音源里缺这个字段是常态。
	FileSize  int64  `json:"file_size,omitempty"`
	RawSource string `json:"raw_source"`

	// ── 补全用的元数据（2026-09-22 新增）──
	//
	// ⚠️ 这几个值**全部来自平台搜索响应本身**，不是推断出来的：
	// 网易 `publishTime`(ms) / `no` / `cd`，QQ `pubtime`(s) / `cdIdx` / `belongCD`。
	// 以前反序列化时把它们丢了，所以「补年份/曲序」无从下手。
	// **不要**用 AI 猜这些值（用户明确要求「用搜索的，而不是 AI 瞎猜」）。
	Year  int `json:"year,omitempty"`  // 发行年份，0 = 平台没给
	Track int `json:"track,omitempty"` // 碟内曲序，0 = 平台没给
	Disc  int `json:"disc,omitempty"`  // 光盘序号，0 = 平台没给
	// AlbumMID 只有 QQ 有：取「风格」得再调一次专辑详情接口（四家的**搜索**响应都不含 genre）。
	AlbumMID string `json:"album_mid,omitempty"`
}

// msToYear 毫秒时间戳 → 年份（<=0 或异常值返回 0）
func msToYear(ms int64) int {
	if ms <= 0 {
		return 0
	}
	return time.Unix(ms/1000, 0).UTC().Year()
}

// secToYear 秒时间戳 → 年份
func secToYear(sec int64) int {
	if sec <= 0 {
		return 0
	}
	return time.Unix(sec, 0).UTC().Year()
}

// atoiLoose 解析可能带前导零/空白的数字字符串（"01" → 1；解析不了返回 0）
func atoiLoose(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// discOfQQ QQ 的 belongCD 实测常为 0（单碟专辑也返回 0），0 一律当「平台没给」；
// 而 cdIdx 有值说明这首确实在专辑里 → 补一个 1（第一碟）。
// 宁可不写，也别写个错的。
//
// ⚠️ 非测试代码里现在没有调用方：搜索接口换成 search_for_qq_cp 之后不再返回
// belongCD/cdIdx（见 qqSearchEndpoint 注释），QQ 的曲序/光盘得靠专辑详情补。
// 留着是因为补曲序那天要用这个判据，meta_test.go 也仍在测它。
func discOfQQ(belongCD, cdIdx int) int {
	if belongCD > 0 {
		return belongCD
	}
	if cdIdx > 0 {
		return 1
	}
	return 0
}

var httpClient = &http.Client{
	Timeout: 8 * time.Second,
}

func isNoisySearchItem(keyword, title, singer, album string, durationSec int) bool {
	kwLower := strings.ToLower(strings.TrimSpace(keyword))
	tLower := strings.ToLower(strings.TrimSpace(title))
	sLower := strings.ToLower(strings.TrimSpace(singer))
	aLower := strings.ToLower(strings.TrimSpace(album))

	if durationSec > 600 || (durationSec > 0 && durationSec < 50 && !strings.Contains(kwLower, "铃声") && !strings.Contains(kwLower, "片段")) {
		return true
	}

	noiseKeywords := []string{
		"伴奏", "ktv", "cover", "翻唱", "片段", "铃声", "剪辑", "纯音乐",
		"喊麦", "慢摇", "串烧", "变速", "降调", "升调", "高潮版", "副歌",
		"dj", "remix", "伴唱", "男声版", "女声版", "烟嗓", "热播", "抖音", "快手",
		"加长版", "慢速", "变调", "弹唱", "电音版", "电音", "dj版", "remix版", "深情版", "dj串烧",
	}
	for _, nk := range noiseKeywords {
		if !strings.Contains(kwLower, nk) {
			if strings.Contains(tLower, nk) || strings.Contains(sLower, nk) {
				return true
			}
		}
	}

	singerNoise := []string{
		"电台", "故事会", "讲故事", "音乐盒", "放映室", "恋人", "解说", "广播", "有声",
	}
	for _, sn := range singerNoise {
		if !strings.Contains(kwLower, sn) && strings.Contains(sLower, sn) {
			return true
		}
	}

	albumNoise := []string{
		"翻唱", "cover", "伴奏", "轻音乐", "钢琴", "吉他", "小提琴", "古筝",
		"纯音乐", "睡眠", "助眠", "减压", "冥想", "胎教", "瑜伽", "白噪音",
		"广播剧", "有声书", "评书", "相声", "小说", "讲故事", "故事", "解说", "睡眠曲",
		"for流浪", "抖音", "快手", "深情版", "经典好歌", "流行歌曲", "大全集", "合集", "合辑",
		"畅听", "网络流行", "音乐驿站", "精选集", "纪录片", "大型纪录片", "麦克阿瑟", "短剧",
	}
	for _, an := range albumNoise {
		if !strings.Contains(kwLower, an) && strings.Contains(aLower, an) {
			return true
		}
	}

	return false
}

type scoredSong struct {
	song  UnifiedSong
	score int
}

func filterAndRankSongs(keyword string, songs []UnifiedSong) []UnifiedSong {
	kwLower := strings.ToLower(strings.TrimSpace(keyword))
	kwTokens := strings.Fields(kwLower)

	type pairKey struct {
		title  string
		singer string
	}
	consensusMap := make(map[pairKey]int)
	for _, s := range songs {
		t := strings.ToLower(strings.TrimSpace(s.Name))
		if idx := strings.Index(t, "("); idx > 0 {
			t = strings.TrimSpace(t[:idx])
		}
		if idx := strings.Index(t, "（"); idx > 0 {
			t = strings.TrimSpace(t[:idx])
		}
		sName := strings.ToLower(strings.TrimSpace(s.Singer))
		sName = strings.ReplaceAll(sName, "g.e.m.", "")
		sName = strings.TrimSpace(sName)
		k := pairKey{title: t, singer: sName}
		consensusMap[k]++
	}

	scored := make([]scoredSong, 0, len(songs))
	noisy := make([]UnifiedSong, 0)

	for idx, s := range songs {
		if isNoisySearchItem(kwLower, s.Name, s.Singer, s.Album, s.Duration) {
			noisy = append(noisy, s)
			continue
		}

		score := 0
		sNameLower := strings.ToLower(strings.TrimSpace(s.Name))
		sSingerLower := strings.ToLower(strings.TrimSpace(s.Singer))
		sAlbumLower := strings.ToLower(strings.TrimSpace(s.Album))

		normTitle := sNameLower
		if i := strings.Index(normTitle, "("); i > 0 {
			normTitle = strings.TrimSpace(normTitle[:i])
		}
		if i := strings.Index(normTitle, "（"); i > 0 {
			normTitle = strings.TrimSpace(normTitle[:i])
		}

		// 1. Multi-token or Single-token matching
		if len(kwTokens) > 1 {
			titleMatched := false
			singerMatched := false
			for _, tok := range kwTokens {
				if normTitle == tok {
					score += 3000
					titleMatched = true
				} else if strings.Contains(normTitle, tok) {
					score += 1500
					titleMatched = true
				}
				if strings.Contains(sSingerLower, tok) {
					score += 3000
					singerMatched = true
				}
			}
			if titleMatched && singerMatched {
				score += 6000 // Golden combo! Both title and singer matched!
			} else if !titleMatched {
				score -= 4000 // Only singer matched but wrong song!
			}
		} else {
			if sNameLower == kwLower {
				score += 3000
			} else if normTitle == kwLower {
				score += 2400
			} else if strings.HasPrefix(sNameLower, kwLower) {
				score += 1000
			} else if strings.Contains(sNameLower, kwLower) {
				score += 400
			} else {
				score -= 2000
			}

			if sSingerLower == kwLower {
				score += 2500
			}
		}

		// 2. Platform authoritative ranking bonus (KuGou & QQ rank popular originals top)
		rawRank := idx % 16
		if s.Source == "kg" || s.Source == "tx" {
			if rawRank == 0 {
				score += 1000
			} else if rawRank == 1 {
				score += 700
			} else if rawRank == 2 {
				score += 450
			}
		} else {
			if rawRank == 0 {
				score += 400
			}
		}

		// 3. Cross-platform consensus bonus
		normSinger := strings.ReplaceAll(sSingerLower, "g.e.m.", "")
		normSinger = strings.TrimSpace(normSinger)
		k := pairKey{title: normTitle, singer: normSinger}
		if c := consensusMap[k]; c >= 2 {
			score += 1500 * c
		}

		// 4. Studio Album Authority
		if sAlbumLower != "" {
			if sAlbumLower == normTitle {
				// Eponymous title track album (e.g. 七里香 in album 七里香)
				score += 600
			} else if !strings.Contains(sAlbumLower, "（") && !strings.Contains(sAlbumLower, "(") {
				score += 400
			}
		}

		// 5. Normal Duration sweet spot (170s - 340s)
		if s.Duration >= 170 && s.Duration <= 340 {
			score += 250
		} else if s.Duration > 0 && s.Duration < 120 {
			score -= 500
		}

		// 6. Quality bonus
		if strings.ToUpper(s.Quality) == "FLAC" {
			score += 150
		}

		scored = append(scored, scoredSong{song: s, score: score})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	clean := make([]UnifiedSong, len(scored))
	for i, sc := range scored {
		clean[i] = sc.song
	}

	if len(clean) >= 6 {
		return clean
	}
	return append(clean, noisy...)
}

// Search 按关键词搜**单个**平台，返回已过滤排序的候选。
//
// 为什么要有这个薄包装：`HandleSearch` 是 HTTP 层（解析 query、拼统一响应），
// 真正的检索在下面几个小写函数里。整理补全这类**包内调用方**需要「拿一批候选挑最匹配的」，
// 走一遍 HTTP 既没必要也会丢掉错误上下文。所以这里开一个直接调用的入口。
//
// platform 只接受 wy / tx / kg / kw（官方协议覆盖的平台）；其它值返回空 ——
// 聚合（all）不在这里做，补全场景下按平台逐个试比一次拿四家更容易定位匹配质量。
func Search(keyword, platform string, page, pageSize int) []UnifiedSong {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 20
	}
	plat := strings.ToLower(strings.TrimSpace(platform))

	// 外挂搜索器优先（见 provider.go）。
	//
	// 为什么「先问外挂」而不是「原生优先」：**一个平台的搜索与解析必须共用同一个
	// id 空间** —— 搜索给出 Songmid，解析就按这个 id 去要直链。musicdl 覆盖的平台
	// （咪咕 / 千千 / B站，以及用户可选的酷狗酷我）的 id 只有它自己认识，所以这些
	// 平台必须整体走它。
	//
	// 外挂没结果（sidecar 掉线 / 该平台抽风）就退回原生实现（如果这个平台有）。
	// 退回去的结果可能解析不出直链 —— 那正是「外挂不可用」时的既有行为，
	// 不假装它能播（下游的 playableOnly 会如实把它挡在可播列表之外）。
	if s := lookupSearcher(plat); s != nil {
		if out := s.Search(context.Background(), keyword, page, pageSize*2); len(out) > 0 {
			return filterAndRankSongs(keyword, out)
		}
	}

	switch plat {
	case "wy":
		return filterAndRankSongs(keyword, searchNetEase(keyword, page, pageSize*2))
	case "tx":
		return filterAndRankSongs(keyword, searchQQ(keyword, page, pageSize*2))
	case "kg":
		return filterAndRankSongs(keyword, searchKuGou(keyword, page, pageSize*2))
	case "kw":
		return filterAndRankSongs(keyword, searchKuWo(keyword, page, pageSize*2))
	}
	return nil
}

func HandleSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	q := r.URL.Query()
	keyword := strings.TrimSpace(q.Get("q"))
	if keyword == "" {
		keyword = strings.TrimSpace(q.Get("keyword"))
	}
	if keyword == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    200,
			"data":    map[string]interface{}{"list": []UnifiedSong{}, "total": 0},
			"list":    []UnifiedSong{},
			"total":   0,
			"message": "empty keyword",
		})
		return
	}

	platform := strings.ToLower(q.Get("source"))
	if platform == "" {
		platform = strings.ToLower(q.Get("platform"))
	}
	if platform == "" {
		platform = "all"
	}

	page, _ := strconv.Atoi(q.Get("page"))
	if page <= 0 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 20
	}

	results := make([]UnifiedSong, 0)
	var wg sync.WaitGroup
	var mu sync.Mutex

	appendResults := func(songs []UnifiedSong) {
		mu.Lock()
		defer mu.Unlock()
		results = append(results, songs...)
	}

	// 单个平台：**走 `Search()`**，而不是在这里再抄一份 switch。
	//
	// 抄一份的代价真出现过：`Search()` 已经会「先问外挂搜索器、没结果退回原生」，
	// 而这里自己 switch 的话，外挂平台（咪咕 / 千千 / B站）请求进来会落到 default
	// 分支 —— 用户要咪咕，拿到的是网易 / QQ / 酷狗 / 酷我的结果，而且看不出为什么。
	switch platform {
	case "", "all":
		// 全部平台：原生四个 + 当前注册的外挂平台一起并跑。
		// 外挂平台从注册表**现取**（sidecar 关着时它就不在，不会白等一轮超时）。
		plats := append([]string{"wy", "tx", "kg", "kw"}, ExternalSearcherPlatforms()...)
		wg.Add(len(plats))
		for _, p := range plats {
			go func(p string) {
				defer wg.Done()
				appendResults(Search(keyword, p, page, 16))
			}(p)
		}
		wg.Wait()
		results = filterAndRankSongs(keyword, results)
	default:
		// 认不出的平台**返回空**，而不是偷偷回退到「全部平台」：
		// 用户点名要咪咕，就不该拿到网易的歌（那种「搜到了但不是这个平台的」
		// 比「没搜到」更难解释）。
		results = filterAndRankSongs(keyword, Search(keyword, platform, page, pageSize*2))
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":     200,
		"message":  "ok",
		"keyword":  keyword,
		"platform": platform,
		"data": map[string]interface{}{
			"list":     results,
			"total":    len(results),
			"keyword":  keyword,
			"platform": platform,
		},
		"list":  results,
		"total": len(results),
	})
}

func HandleLyric(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	q := r.URL.Query()
	source := strings.ToLower(q.Get("source"))
	songmid := strings.TrimSpace(q.Get("songmid"))
	if songmid == "" {
		songmid = strings.TrimSpace(q.Get("id"))
	}
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		title = strings.TrimSpace(q.Get("name"))
	}
	singer := strings.TrimSpace(q.Get("singer"))
	if singer == "" {
		singer = strings.TrimSpace(q.Get("artist"))
	}

	durSec, _ := strconv.Atoi(q.Get("duration"))
	if durSec <= 0 {
		durSec, _ = strconv.Atoi(q.Get("interval"))
	}
	hash := strings.TrimSpace(q.Get("hash"))

	lrc, tlrc := fetchLyric(source, songmid, title, singer, durSec, hash)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200,
		"data": map[string]string{
			"lyric":  lrc,
			"tlyric": tlrc,
		},
		"lyric": lrc,
	})
}

func normalizeLyricTitle(s string) string {
	s = strings.ToLower(cleanTitle(s))
	for _, pair := range [][]string{{"(", ")"}, {"（", "）"}, {"[", "]"}, {"【", "】"}} {
		for {
			start := strings.Index(s, pair[0])
			if start == -1 {
				break
			}
			end := strings.Index(s[start:], pair[1])
			if end == -1 {
				break
			}
			s = s[:start] + s[start+end+len(pair[1]):]
		}
	}
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	return strings.TrimSpace(s)
}

func titleMatches(t1, t2 string) bool {
	rawTarget := strings.ToLower(cleanTitle(t1))
	rawCand := strings.ToLower(cleanTitle(t2))

	noisyKeywords := []string{"伴奏", "ktv", "cover", "翻唱", "片段", "铃声", "剪辑", "纯音乐", "激情", "弹唱"}
	for _, kw := range noisyKeywords {
		if !strings.Contains(rawTarget, kw) && strings.Contains(rawCand, kw) {
			return false
		}
	}

	n1 := normalizeLyricTitle(t1)
	n2 := normalizeLyricTitle(t2)
	if n1 == "" || n2 == "" {
		return true
	}
	return n1 == n2 || strings.Contains(n1, n2) || strings.Contains(n2, n1)
}

func singerMatches(s1, s2 string) bool {
	s1 = strings.ToLower(cleanTitle(s1))
	s2 = strings.ToLower(cleanTitle(s2))
	if s1 == "" || s2 == "" {
		return true
	}
	s1 = strings.ReplaceAll(s1, " ", "")
	s2 = strings.ReplaceAll(s2, " ", "")
	return strings.Contains(s1, s2) || strings.Contains(s2, s1)
}

func fetchNetEaseLyricByID(songmid string) (string, string) {
	url := fmt.Sprintf("https://music.163.com/api/song/lyric?id=%s&lv=1&kv=1&tv=-1", songmid)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://music.163.com/")
	resp, err := doHTTP(req)
	if err == nil && resp.StatusCode == 200 {
		defer resp.Body.Close()
		var res struct {
			Lrc struct {
				Lyric string `json:"lyric"`
			} `json:"lrc"`
			Tlyric struct {
				Lyric string `json:"lyric"`
			} `json:"tlyric"`
		}
		body, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(body, &res)
		if res.Lrc.Lyric != "" {
			return res.Lrc.Lyric, res.Tlyric.Lyric
		}
	}
	return "", ""
}

func fetchNetEaseLyricBySearch(query, targetName, targetSinger string, durationSec int) (string, string) {
	apiURL := fmt.Sprintf("https://music.163.com/api/cloudsearch/pc?s=%s&type=1&offset=0&limit=5", url.QueryEscape(query))
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://music.163.com/")
	resp, err := doHTTP(req)
	if err != nil || resp.StatusCode != 200 {
		return "", ""
	}
	defer resp.Body.Close()

	var raw struct {
		Result struct {
			Songs []struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"ar"`
				Duration int `json:"dt"`
			} `json:"songs"`
		} `json:"result"`
	}
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &raw)

	for _, s := range raw.Result.Songs {
		if isNoisySearchItem("", s.Name, "", "", 0) {
			continue
		}
		if !titleMatches(targetName, s.Name) {
			continue
		}
		artistNames := make([]string, 0)
		for _, a := range s.Artists {
			artistNames = append(artistNames, a.Name)
		}
		artistStr := strings.Join(artistNames, ", ")
		if targetSinger != "" && !singerMatches(targetSinger, artistStr) {
			continue
		}
		// If duration is provided, check tolerance
		if durationSec > 0 && s.Duration > 0 {
			diff := int(math.Abs(float64(s.Duration/1000 - durationSec)))
			if diff > 10 && durationSec > 60 {
				continue
			}
		}
		idStr := strconv.FormatInt(s.ID, 10)
		if lrc, tlrc := fetchNetEaseLyricByID(idStr); lrc != "" {
			return lrc, tlrc
		}
	}
	return "", ""
}

func fetchKuGouLyricStrict(searchKeyword, targetName, targetSinger string, durationSec int, hash string) string {
	durationMs := durationSec * 1000
	searchURL := fmt.Sprintf("http://lyrics.kugou.com/search?ver=1&man=yes&client=pc&keyword=%s&duration=%d&hash=%s",
		url.QueryEscape(searchKeyword), durationMs, hash)
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := doHTTP(req)
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()

	var kgRes struct {
		Candidates []struct {
			ID        string      `json:"id"`
			AccessKey string      `json:"accesskey"`
			Duration  int         `json:"duration"`
			Song      string      `json:"song"`
			Singer    string      `json:"singer"`
			Adjust    interface{} `json:"adjust"`
		} `json:"candidates"`
	}
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &kgRes)
	if len(kgRes.Candidates) == 0 {
		return ""
	}

	bestIdx := -1
	minDiff := 999999999

	for idx, cand := range kgRes.Candidates {
		if isNoisySearchItem("", cand.Song, cand.Singer, "", 0) {
			continue
		}
		if !titleMatches(targetName, cand.Song) {
			continue
		}
		if targetSinger != "" && !singerMatches(targetSinger, cand.Singer) {
			continue
		}

		if durationMs > 0 {
			diff := int(math.Abs(float64(cand.Duration - durationMs)))
			if cand.Duration > 50000 && diff < minDiff {
				minDiff = diff
				bestIdx = idx
			}
		} else if cand.Duration > 50000 {
			bestIdx = idx
			break
		}
	}

	if bestIdx == -1 {
		for idx, cand := range kgRes.Candidates {
			if !isNoisySearchItem("", cand.Song, cand.Singer, "", 0) && cand.Duration > 50000 {
				bestIdx = idx
				break
			}
		}
	}

	if bestIdx == -1 {
		return ""
	}

	c := kgRes.Candidates[bestIdx]
	dlURL := fmt.Sprintf("http://lyrics.kugou.com/download?ver=1&client=pc&id=%s&accesskey=%s&fmt=lrc&charset=utf8",
		c.ID, c.AccessKey)
	req2, _ := http.NewRequest("GET", dlURL, nil)
	req2.Header.Set("User-Agent", "Mozilla/5.0")
	resp2, err2 := doHTTP(req2)
	if err2 == nil && resp2.StatusCode == 200 {
		defer resp2.Body.Close()
		var dlRes struct {
			Content string `json:"content"`
		}
		body2, _ := io.ReadAll(resp2.Body)
		_ = json.Unmarshal(body2, &dlRes)
		if dlRes.Content != "" {
			decoded, decErr := base64.StdEncoding.DecodeString(dlRes.Content)
			if decErr == nil && len(decoded) > 0 {
				lrcStr := string(decoded)
				var adjMs int
				switch v := c.Adjust.(type) {
				case float64:
					adjMs = int(v)
				case string:
					adjMs, _ = strconv.Atoi(v)
				case int:
					adjMs = v
				}
				if adjMs != 0 && !strings.Contains(lrcStr, "[offset:") {
					lrcStr = fmt.Sprintf("[offset:%d]\n%s", adjMs, lrcStr)
				}
				return lrcStr
			}
		}
	}
	return ""
}

// FetchLyric 歌词获取的导出入口，供下载器等其它包复用。
// 返回 (原文歌词, 翻译歌词)。
// FetchLyric 取歌词，返回 (正文, 翻译)。
// 结果走进程内缓存 —— 见 cache.go 的 cachedLyric。
func FetchLyric(source, songmid, title, singer string, durationSec int, hash string) (string, string) {
	return cachedLyric(source, songmid, title, singer, durationSec, hash, func() (string, string) {
		return fetchLyric(source, songmid, title, singer, durationSec, hash)
	})
}

func fetchLyric(source, songmid, title, singer string, durationSec int, hash string) (string, string) {
	// 1. If NetEase ID available
	if (source == "wy" || strings.HasPrefix(source, "wy")) && songmid != "" {
		if lrc, tlrc := fetchNetEaseLyricByID(songmid); lrc != "" {
			return lrc, tlrc
		}
	}

	cleanT := cleanTitle(title)
	cleanS := cleanTitle(singer)

	// 2. High-precision Official NetEase CloudSearch (Works universally for QQ, KuGou, KuWo)
	if cleanT != "" {
		searchQ := cleanT
		if cleanS != "" {
			searchQ = cleanS + " " + cleanT
		}
		if lrc, tlrc := fetchNetEaseLyricBySearch(searchQ, cleanT, cleanS, durationSec); lrc != "" {
			return lrc, tlrc
		}
	}

	// 3. Universal Lyric Search via KuGou Lyric Engine (with strict candidate validation)
	if cleanT != "" {
		searchKeyword := cleanT
		if cleanS != "" {
			searchKeyword = cleanS + " - " + cleanT
		}
		if lrc := fetchKuGouLyricStrict(searchKeyword, cleanT, cleanS, durationSec, hash); lrc != "" {
			return lrc, ""
		}
	}

	return "", ""
}

// 1. NetEase (uses cloudsearch for HD cover al.picUrl)
// searchNetEaseRaw 网易云搜索的**真实请求**实现。
// 带缓存、带上限速的对外入口是同名的 searchNetEase（见 cache.go）。
func searchNetEaseRaw(keyword string, page, limit int) []UnifiedSong {
	apiURL := fmt.Sprintf("https://music.163.com/api/cloudsearch/pc?s=%s&type=1&offset=%d&limit=%d",
		url.QueryEscape(keyword), (page-1)*limit, limit)

	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	req.Header.Set("Referer", "https://music.163.com/")

	resp, err := doHTTP(req)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()

	var raw struct {
		Result struct {
			Songs []struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"ar"`
				Album struct {
					Name   string `json:"name"`
					PicURL string `json:"picUrl"`
				} `json:"al"`
				Duration int `json:"dt"`
				// 补全用（实测这三个键都在搜索响应里，以前没取）：
				//   publishTime = 发行时间毫秒戳；no = 碟内曲序；cd = 光盘序号（字符串 "01"）
				PublishTime int64  `json:"publishTime"`
				No          int    `json:"no"`
				Cd          string `json:"cd"`
				// 各档音质对象；该档不可用时为 null（实测字段名固定为 l/m/h/sq/hr）
				L  *neteaseLevelInfo `json:"l"`
				M  *neteaseLevelInfo `json:"m"`
				H  *neteaseLevelInfo `json:"h"`
				Sq *neteaseLevelInfo `json:"sq"`
				Hr *neteaseLevelInfo `json:"hr"`
			} `json:"songs"`
		} `json:"result"`
	}

	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &raw)

	list := make([]UnifiedSong, 0, len(raw.Result.Songs))
	for _, s := range raw.Result.Songs {
		artists := make([]string, 0)
		for _, a := range s.Artists {
			artists = append(artists, a.Name)
		}
		durationSec := s.Duration / 1000
		cover := s.Album.PicURL
		if cover != "" && strings.HasPrefix(cover, "http://") {
			cover = strings.Replace(cover, "http://", "https://", 1)
		}
		qs := neteaseQualitys(s.L, s.M, s.H, s.Sq, s.Hr)
		if len(qs) == 0 {
			// 平台未返回任何音质信息（接口异常/改版）时的保守兜底：
			// 320k 在四个平台实测均为普遍可用档位，且不虚报无损。
			qs = []string{Quality320k}
		}
		list = append(list, UnifiedSong{
			ID:        fmt.Sprintf("wy_%d", s.ID),
			Songmid:   fmt.Sprintf("%d", s.ID),
			Name:      s.Name,
			Singer:    strings.Join(artists, ", "),
			Album:     s.Album.Name,
			Cover:     cover,
			Source:    "wy",
			Duration:  durationSec,
			Interval:  durationSec,
			Quality:   bestQuality(qs),
			Qualitys:  qs,
			RawSource: "wy",
			Year:      msToYear(s.PublishTime),
			Track:     s.No,
			Disc:      atoiLoose(s.Cd),
		})
	}
	return list
}

// 2. QQ Music (HD album cover via Albummid)

// qqSearchEndpoint QQ 搜索端点。
//
// 2026-09-26 实测：原桌面接口 `c.y.qq.com/soso/fcgi-bin/client_search_cp`
// 对任何参数组合（含完整 g_tk/loginUin/platform 参数、http 代理与 socks5 代理）
// 都稳定返回 HTTP 500 且 body 为空 —— QQ 侧已下线。
// 而这里原本是 `resp.StatusCode != 200 → return nil`，于是 QQ 搜索在界面上
// 表现为「搜到 0 条」：不报错、没日志，查起来像玄学。
//
// 改用同 host 的移动接口 search_for_qq_cp：照旧返回 pubtime（→年份）与各档体积，
// 但**没有 cdIdx/belongCD** → 搜索阶段拿不到曲序/光盘（要补只能走专辑详情，
// 见 album_qq.go）。
// 该接口唯一的硬要求是 Referer: https://y.qq.com/ —— 缺了会返回 HTTP 200 +
// {"subcode":-10001,"message":"禁止跨域访问"} 的空壳；UA 和 cookie 都不挑。
const qqSearchEndpoint = "https://c.y.qq.com/soso/fcgi-bin/search_for_qq_cp"

// qqMobileSearchURL 拼 QQ 搜索 URL。
// 单独抽出来是为了能测：端点选错是静默故障（HTTP 200 + 空结果），不值得靠肉眼守。
func qqMobileSearchURL(keyword string, page, limit int) string {
	return fmt.Sprintf("%s?w=%s&p=%d&n=%d&format=json",
		qqSearchEndpoint, url.QueryEscape(keyword), page, limit)
}

// searchQQRaw QQ 音乐搜索的**真实请求**实现。
// 带缓存、带上限速的对外入口是同名的 searchQQ（见 cache.go）。
func searchQQRaw(keyword string, page, limit int) []UnifiedSong {
	req, _ := http.NewRequest("GET", qqMobileSearchURL(keyword, page, limit), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://y.qq.com/")

	resp, err := doHTTP(req)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return parseQQMobile(body)
}

// parseQQMobile 解析 search_for_qq_cp 的响应。
//
// 形状（实测）：{code, subcode, data:{song:{list:[…], totalnum, curpage}}}
// 与旧桌面接口的差别：没有 cdIdx/belongCD，所以 Track/Disc 一律留 0。
// 另外还有 pay{payplay} 与 preview{trybegin,tryend,trysize} 两组键 —— 平台自报
// 「付费试听」，比按 ≤75 秒猜准，但 UnifiedSong 现在没有承载字段，先不取。
func parseQQMobile(body []byte) []UnifiedSong {
	var raw struct {
		Code    int    `json:"code"`
		Subcode int    `json:"subcode"`
		Message string `json:"message"`
		Data    struct {
			Song struct {
				List []struct {
					Songmid  string `json:"songmid"`
					Songname string `json:"songname"`
					Singer   []struct {
						Name string `json:"name"`
					} `json:"singer"`
					Albumname string `json:"albumname"`
					Albummid  string `json:"albummid"`
					Interval  int    `json:"interval"`
					// 发行时间秒戳。桌面接口下线后，这是 QQ 唯一的年份来源。
					Pubtime int64 `json:"pubtime"`
					// 各档文件体积（字节）；为 0 表示该档不存在。
					// 实测返回 128/320/flac/ape/ogg，不返回 sizehires，但仍保留解析。
					Size128   int64 `json:"size128"`
					Size320   int64 `json:"size320"`
					SizeFlac  int64 `json:"sizeflac"`
					SizeHires int64 `json:"sizehires"`
					SizeApe   int64 `json:"sizeape"`
				} `json:"list"`
			} `json:"song"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	// subcode != 0 = 被拦截的空壳（Referer 被剥时 -10001「禁止跨域访问」）。
	// 那不算「搜到 0 条」，当失败返回，免得上层把平台故障读成「这首歌不存在」。
	if raw.Subcode != 0 {
		return nil
	}

	list := make([]UnifiedSong, 0, len(raw.Data.Song.List))
	for _, s := range raw.Data.Song.List {
		singers := make([]string, 0)
		for _, a := range s.Singer {
			singers = append(singers, a.Name)
		}
		cover := ""
		if s.Albummid != "" {
			cover = fmt.Sprintf("https://y.gtimg.cn/music/photo_new/T002R300x300M000%s.jpg", s.Albummid)
		}
		qs := qqQualitys(s.Size128, s.Size320, s.SizeFlac, s.SizeHires, s.SizeApe)
		if len(qs) == 0 {
			qs = []string{Quality320k}
		}
		list = append(list, UnifiedSong{
			ID:        "tx_" + s.Songmid,
			Songmid:   s.Songmid,
			Name:      s.Songname,
			Singer:    strings.Join(singers, ", "),
			Album:     s.Albumname,
			Cover:     cover,
			Source:    "tx",
			Duration:  s.Interval,
			Interval:  s.Interval,
			Quality:   bestQuality(qs),
			Qualitys:  qs,
			RawSource: "tx",
			Year:      secToYear(s.Pubtime),
			AlbumMID:  s.Albummid,
			// Track/Disc：本接口不返回 cdIdx/belongCD，留 0（0 = 平台没给，写入时跳过）。
		})
	}
	return list
}

// 3. KuGou (HD album cover via Image field)
// searchKuGouRaw 酷狗搜索的**真实请求**实现。
// 带缓存、带上限速的对外入口是同名的 searchKuGou（见 cache.go）。
func searchKuGouRaw(keyword string, page, limit int) []UnifiedSong {
	apiURL := fmt.Sprintf("https://songsearch.kugou.com/song_search_v2?keyword=%s&page=%d&pagesize=%d&platform=WebFilter",
		url.QueryEscape(keyword), page, limit)

	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := doHTTP(req)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()

	var raw struct {
		Data struct {
			Lists []struct {
				FileHash   string `json:"FileHash"`
				SongName   string `json:"SongName"`
				SingerName string `json:"SingerName"`
				AlbumName  string `json:"AlbumName"`
				Duration   int    `json:"Duration"`
				Image      string `json:"Image"`
				// 各档 FileHash；为空表示该档不存在。
				// 实测对应 128k / 320k / 无损 / 24bit 无损 / 超品。
				HQFileHash    string `json:"HQFileHash"`
				SQFileHash    string `json:"SQFileHash"`
				ResFileHash   string `json:"ResFileHash"`
				SuperFileHash string `json:"SuperFileHash"`
			} `json:"lists"`
		} `json:"data"`
	}

	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &raw)

	list := make([]UnifiedSong, 0, len(raw.Data.Lists))
	for _, s := range raw.Data.Lists {
		name := strings.ReplaceAll(strings.ReplaceAll(s.SongName, "<em>", ""), "</em>", "")
		singer := strings.ReplaceAll(strings.ReplaceAll(s.SingerName, "<em>", ""), "</em>", "")
		cover := ""
		if s.Image != "" {
			cover = strings.ReplaceAll(s.Image, "{size}", "400")
			if strings.HasPrefix(cover, "http://") {
				cover = strings.Replace(cover, "http://", "https://", 1)
			}
		}
		qs := kugouQualitys(s.FileHash, s.HQFileHash, s.SQFileHash, s.ResFileHash, s.SuperFileHash)
		if len(qs) == 0 {
			qs = []string{Quality320k}
		}
		list = append(list, UnifiedSong{
			ID:        "kg_" + s.FileHash,
			Songmid:   s.FileHash,
			Hash:      s.FileHash,
			Name:      name,
			Singer:    singer,
			Album:     s.AlbumName,
			Cover:     cover,
			Source:    "kg",
			Duration:  s.Duration,
			Interval:  s.Duration,
			Quality:   bestQuality(qs),
			Qualitys:  qs,
			RawSource: "kg",
		})
	}
	return list
}

// 4. KuWo (HD album cover via web_albumpic_short)
// searchKuWoRaw 酷我搜索的**真实请求**实现。
// 带缓存、带上限速的对外入口是同名的 searchKuWo（见 cache.go）。
func searchKuWoRaw(keyword string, page, limit int) []UnifiedSong {
	apiURL := fmt.Sprintf("https://search.kuwo.cn/r.s?all=%s&ft=music&itemset=web_2013&client=kt&pn=%d&rn=%d&rformat=json&encoding=utf8",
		url.QueryEscape(keyword), page-1, limit)

	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := doHTTP(req)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()

	var raw struct {
		Abslist []struct {
			MUSICRID          string      `json:"MUSICRID"`
			SONGNAME          string      `json:"SONGNAME"`
			ARTIST            string      `json:"ARTIST"`
			ALBUM             string      `json:"ALBUM"`
			DURATION          interface{} `json:"DURATION"`
			WebAlbumpicShort  string      `json:"web_albumpic_short"`
			WebArtistpicShort string      `json:"web_artistpic_short"`
			// 音质描述字段。N_MINFO 为新格式，MINFO 为旧格式，FORMATS 为兜底。
			// 形如 level:p,bitrate:320,format:mp3,size:10.3Mb，多项以 ";" 分隔。
			NMinfo  string `json:"N_MINFO"`
			Minfo   string `json:"MINFO"`
			Formats string `json:"FORMATS"`
		} `json:"abslist"`
	}

	body, _ := io.ReadAll(resp.Body)
	cleanBody := strings.ReplaceAll(string(body), "'", "\"")
	_ = json.Unmarshal([]byte(cleanBody), &raw)

	list := make([]UnifiedSong, 0, len(raw.Abslist))
	for _, s := range raw.Abslist {
		rid := strings.TrimPrefix(s.MUSICRID, "MUSIC_")
		durSec := 0
		switch v := s.DURATION.(type) {
		case float64:
			durSec = int(v)
		case string:
			durSec, _ = strconv.Atoi(v)
		case int:
			durSec = v
		}

		cover := ""
		if s.WebAlbumpicShort != "" {
			hdPath := strings.Replace(s.WebAlbumpicShort, "120/", "500/", 1)
			cover = "https://img1.kuwo.cn/star/albumcover/" + hdPath
		} else if s.WebArtistpicShort != "" {
			cover = "https://img1.kuwo.cn/star/starheads/" + s.WebArtistpicShort
		}

		qs := kuwoResolveQualitys(s.NMinfo, s.Minfo, s.Formats)
		if len(qs) == 0 {
			qs = []string{Quality320k}
		}
		list = append(list, UnifiedSong{
			ID:        "kw_" + rid,
			Songmid:   rid,
			Name:      cleanTitle(s.SONGNAME),
			Singer:    cleanTitle(s.ARTIST),
			Album:     cleanTitle(s.ALBUM),
			Cover:     cover,
			Source:    "kw",
			Duration:  durSec,
			Interval:  durSec,
			Quality:   bestQuality(qs),
			Qualitys:  qs,
			RawSource: "kw",
		})
	}
	return list
}

func cleanTitle(t string) string {
	t = strings.ReplaceAll(t, "&nbsp;", " ")
	t = strings.ReplaceAll(t, "&amp;", "&")
	t = strings.ReplaceAll(t, "&quot;", "\"")
	t = strings.ReplaceAll(t, "&apos;", "'")
	return strings.TrimSpace(t)
}
