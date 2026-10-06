package nas

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/search"
	"fn-lx-player/pkg/security"
	"fn-lx-player/pkg/tags"
)

// tagHTTPClient 抓取封面用；已配合 security.NewSafeTransport 做 SSRF 防护
var tagHTTPClient = security.NewSafeClient(security.DefaultOptions(), 20*time.Second)

// EmbedTagsRequest 标签写入请求
type EmbedTagsRequest struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Genre    string `json:"genre"` // 风格：飞牛「风格」页读的就是这个内嵌字段
	Lyric    string `json:"lyric"`
	Cover    string `json:"cover"`
	WriteLrc bool   `json:"write_lrc"` // 同时写出同名 .lrc
	Fetch    bool   `json:"fetch"`     // 未直接给 lyric 时联网抓取
	Source   string `json:"source"`
	Songmid  string `json:"songmid"`
	Hash     string `json:"hash"`
	Duration int    `json:"duration"`
}

// safeAudioPath 校验并规范化待写入的音频路径。
// 统一走 ResolveSafePath：绝对路径 + 位于 AllowedRoots() 内 + 解析软链接后仍未逃逸，
// 再校验扩展名，避免通过链接写入任意文件。
func safeAudioPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrPathEmpty
	}
	if !audioext.Writable[strings.ToLower(filepath.Ext(raw))] {
		return "", errors.New("仅支持 MP3 / FLAC 文件的标签写入")
	}

	real, err := ResolveSafePath(raw)
	if err != nil {
		return "", err
	}
	if !audioext.Writable[strings.ToLower(filepath.Ext(real))] {
		return "", errors.New("软链接指向了非音频文件，已拒绝写入")
	}
	fi, err := os.Stat(real)
	if err != nil || fi.IsDir() {
		return "", errors.New("目标不是可写入的文件")
	}
	return real, nil
}

// guardWriteTarget 校验待新建/覆盖的伴生文件（如同名 .lrc）不会经由软链接写到
// 允许根目录之外。目标不存在时允许（就在同目录内新建）。
func guardWriteTarget(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	_, gerr := ResolveSafePath(p)
	return gerr
}

// HandleEmbedTags 将歌词 / 封面 / 元数据写入 NAS 上已有的音频文件。
// POST /api/nas/tags
func HandleEmbedTags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 405, "message": "Method not allowed"})
		return
	}

	var req EmbedTagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": "invalid payload"})
		return
	}

	real, err := safeAudioPath(req.Path)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": err.Error()})
		return
	}

	// 歌词：优先使用请求携带的文本，其次按需联网抓取
	lyric := strings.TrimSpace(req.Lyric)
	lyricFetched := false
	if lyric == "" && req.Fetch {
		source := strings.ToLower(strings.TrimSpace(req.Source))
		songmid := strings.TrimSpace(req.Songmid)
		lrc, _ := search.FetchLyric(source, songmid, req.Title, req.Artist, req.Duration, req.Hash)
		if strings.TrimSpace(lrc) != "" {
			lyric = lrc
			lyricFetched = true
		}
	}

	meta := tags.Metadata{
		Title:  strings.TrimSpace(req.Title),
		Artist: strings.TrimSpace(req.Artist),
		Album:  strings.TrimSpace(req.Album),
		// 风格：以前请求结构体里压根没有 genre，手动写标签永远改不了风格
		// （只有下载路径会写），飞牛「风格」页就一直归不了类。
		Genre: strings.TrimSpace(req.Genre),
		Lyric: lyric,
	}

	coverEmbedded := false
	coverError := ""
	if strings.TrimSpace(req.Cover) != "" {
		data, mime, cerr := fetchTagCover(req.Cover)
		if cerr != nil {
			// 封面失败**不阻塞**标签写入（歌词/标题照样写），但要把原因如实回报 ——
			// 以前这里是静默的：用户选了张封面没生效，界面没有任何解释
			// （2026-09-22 反馈「飞牛里歌曲不显示封面」时很难查的原因之一）。
			coverError = cerr.Error()
		} else {
			meta.Cover = data
			meta.CoverMime = mime
			coverEmbedded = true
		}
	}

	if meta.IsEmpty() {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    400,
			"message": "没有可写入的内容（歌词/标题/歌手/专辑/封面至少提供一项）",
		})
		return
	}

	// 外挂同名 .lrc
	wroteLrc := false
	if req.WriteLrc && strings.TrimSpace(lyric) != "" {
		lrcPath := strings.TrimSuffix(real, filepath.Ext(real)) + ".lrc"
		if guardWriteTarget(lrcPath) == nil {
			if werr := os.WriteFile(lrcPath, []byte(lyric), 0644); werr == nil {
				wroteLrc = true
			}
		}
	}

	format, err := tags.Write(real, meta)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    500,
			"message": "写入标签失败: " + err.Error(),
		})
		return
	}

	// 文件内容变了 → 让 NAS 扫描缓存失效，否则「本地」页的封面/标签还是旧的
	InvalidateScanCacheFile(real)

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"message": "ok",
		"data": map[string]interface{}{
			"path":           filepath.ToSlash(real),
			"format":         string(format),
			"embedded_lyric": strings.TrimSpace(meta.Lyric) != "",
			"lyric_fetched":  lyricFetched,
			"wrote_lrc":      wroteLrc,
			"cover_embedded": coverEmbedded,
			"cover_error":    coverError,
		},
	})
}

// HandleReadTags 读取 NAS 音频文件已有的标签，用于前端展示「是否已内嵌歌词」。
// GET /api/nas/tags?path=...
func HandleReadTags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	real, err := safeAudioPath(r.URL.Query().Get("path"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": err.Error()})
		return
	}

	meta, err := tags.Read(real)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 500, "message": err.Error()})
		return
	}

	format, _ := tags.SniffFormat(real)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"message": "ok",
		"data": map[string]interface{}{
			"path":   filepath.ToSlash(real),
			"format": string(format),
			"title":  meta.Title,
			"artist": meta.Artist,
			"album":  meta.Album,
			// 风格/年份/曲序/光盘：v2.1.26 补上 —— 曲库管家的「补全结果卡片」要展示
			// **文件里真实的**这几栏。以前这个接口只回 title/artist/album，
			// 于是卡片只能显示"打算写成什么"而不是"写成了什么"，二者可能不一致。
			"genre":        meta.Genre,
			"year":         meta.Year,
			"track":        meta.Track,
			"disc":         meta.Disc,
			"has_lyric":    strings.TrimSpace(meta.Lyric) != "",
			"has_cover":    len(meta.Cover) > 0,
			"lyric_length": len([]rune(meta.Lyric)),
		},
	})
}

// fetchTagCover 下载封面图并识别类型。第三个返回值是**失败原因**（成功时为 nil）。
//
// ⚠️ 与 `pkg/downloader` 的 `fetchCoverBytes` 保持同一口径：
// 认 jpeg/png/gif/**webp**，并且失败要给出原因。
// 两处曾经不一致 —— 同一张 webp 封面「补全能写、下载写不进去」，用户看到的就是封面时有时无。
func fetchTagCover(rawURL string) ([]byte, string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, "http") {
		return nil, "", errors.New("封面地址不是 http(s)")
	}

	// SSRF 防护：拒绝内网 / 回环 / 链路本地 / 云元数据地址
	opts := security.Options{MaxBodyBytes: 12 << 20}
	if _, err := security.ValidateURL(rawURL, opts); err != nil {
		return nil, "", fmt.Errorf("封面地址被安全校验拒绝：%v", err)
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("构造封面请求失败：%v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://music.163.com/")

	resp, err := tagHTTPClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("封面下载失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("封面下载返回 HTTP %d", resp.StatusCode)
	}

	const maxCover = 12 << 20
	data, err := security.LimitedRead(resp.Body, security.Options{MaxBodyBytes: maxCover})
	if err != nil {
		return nil, "", fmt.Errorf("读取封面数据失败：%v", err)
	}
	if len(data) < 100 {
		return nil, "", fmt.Errorf("封面数据过小（%d 字节），疑似不是图片", len(data))
	}

	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return data, "image/jpeg", nil
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return data, "image/png", nil
	case len(data) >= 6 && strings.HasPrefix(string(data), "GIF8"):
		return data, "image/gif", nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return data, "image/webp", nil
	}
	return nil, "", errors.New("封面不是图片，或格式不在支持范围内（jpeg/png/gif/webp）")
}
