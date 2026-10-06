package search

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ── QQ 专辑详情：唯一能拿到「风格」的权威来源 ──
//
// 为什么单独写这个：**四家的搜索响应都不含 genre**（`pkg/complete/complete.go` 的注释、
// `docs/飞牛刮削适配调研.md` 都记着这条既有结论），而飞牛「歌曲信息」里有「风格」一栏。
// 用户明确要求「用搜索的，而不是 AI 瞎猜」，所以：
//   - 只认这种**平台返回的真实字段**（QQ 专辑详情有）；
//   - 拿不到就**留空**，绝不用推断顶上（`complete.inferGenre` 那个 AI 推断默认是关的）。
//
// 接口（2026-09-22 实测）：
//
//	GET https://c.y.qq.com/v8/fcg-bin/fcg_v8_album_info_cp.fcg?albummid=<mid>&g_tk=5381&format=json
//	→ data.genre = "Pop 流行"（中文字符串）
//	  data.aDate = "2003-07-31"（发行日期，可当「年份」的兜底来源）

// QQAlbumMeta QQ 专辑详情里与补全相关的那几项
type QQAlbumMeta struct {
	Genre string `json:"genre,omitempty"` // 风格，如 "Pop 流行"；空 = 没拿到
	Date  string `json:"date,omitempty"`  // 发行日期，如 "2003-07-31"
	Year  int    `json:"year,omitempty"`  // 从 Date 解析出的年份，0 = 没有
}

// FetchQQAlbumMeta 按 albummid 取 QQ 专辑的「风格 + 发行日期」。
// 第二个返回值为 false 表示没拿到（网络失败 / 字段为空）——调用方应原样留空，不要推断。
//
// 缓存是 cache.go 里的 albumMetaCache（进程内、带 TTL 与 LRU）：一张专辑会被
// 补全流水线里的很多首歌命中，别为同一张专辑反复打上游（也顺带避开上游风控）。
func FetchQQAlbumMeta(albumMID string) (QQAlbumMeta, bool) {
	albumMID = strings.TrimSpace(albumMID)
	if albumMID == "" {
		return QQAlbumMeta{}, false
	}

	// 只缓存**成功**结果：原来失败的空结果也会被记下来，等于把一次网络抖动
	// 固化到进程重启为止（所有调用方都跟着拿到「这首歌没风格」）。
	if hit, ok := albumMetaCache.Get(albumMID); ok {
		return hit, hit.Genre != "" || hit.Year > 0
	}

	meta, ok := fetchQQAlbumMeta(albumMID)
	if ok {
		albumMetaCache.Put(albumMID, meta)
	}
	return meta, ok
}

func fetchQQAlbumMeta(albumMID string) (QQAlbumMeta, bool) {
	apiURL := "https://c.y.qq.com/v8/fcg-bin/fcg_v8_album_info_cp.fcg?albummid=" +
		url.QueryEscape(albumMID) + "&g_tk=5381&format=json"

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return QQAlbumMeta{}, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://y.qq.com/")

	resp, err := doHTTP(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return QQAlbumMeta{}, false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QQAlbumMeta{}, false
	}

	var raw struct {
		Data struct {
			Genre string `json:"genre"`
			ADate string `json:"aDate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return QQAlbumMeta{}, false
	}

	meta := QQAlbumMeta{
		Genre: strings.TrimSpace(raw.Data.Genre),
		Date:  strings.TrimSpace(raw.Data.ADate),
	}
	meta.Year = yearFromDate(meta.Date)
	return meta, meta.Genre != "" || meta.Year > 0
}

// yearFromDate 从 "2003-07-31" / "2003-07" / "2003" 里取年份；取不到返回 0
func yearFromDate(date string) int {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0
	}
	y := atoiLoose(date[:4])
	if y < 1900 || y > 2100 {
		return 0
	}
	return y
}
