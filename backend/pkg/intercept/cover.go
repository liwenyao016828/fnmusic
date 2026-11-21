package intercept

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fn-lx-player/pkg/online"
)

// 封面处理的常量。
const (
	// coverFetchBudget 是拉取一张封面的等待上限。
	coverFetchBudget = 6 * time.Second
	// coverMaxBytes 是单张封面的体积上限。超过就当抓取失败 —— 正常封面
	// 都在几百 KB 以内，几 MB 的响应几乎一定是错误页或反爬页面。
	coverMaxBytes = 6 << 20
)

// placeholderPNG 是一张 1×1 的透明 PNG。
//
// 官方的封面位要求同源且路径必须是 `/static/cover`，所以不能把第三方封面
// 地址直接交给客户端 —— 必须由我们在服务端取回来再转出去。取不到时也得给
// **一张图**：返回 404 会让客户端在列表里留下一排破图占位。
var placeholderPNG = func() []byte {
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	return b
}()

// handleCover 接管 `GET,HEAD /music/api/v1/static/cover`。
//
// 官方前端的封面加载器会**校验 URL**：必须与页面同源、pathname 以
// `/static/cover` 结尾、且带 `coverId` 参数，否则直接 return null 不加载。
// 也就是说第三方封面地址永远不可能直接显示 —— 必须经过这个端点代理。
func (i *Interceptor) handleCover(w http.ResponseWriter, r *http.Request) bool {
	sub := subPathAfter(r.URL.Path, apiPrefix+"/static/cover")
	track, fake, ok := i.lookupTrack(r, sub)
	if !ok {
		// v2.33.0：曲目表里没有不一定是「不是我们的」—— 歌手头像与纯专辑锚点
		// 都不属于曲目，但它们的 coverId 也是我们下发的虚拟 id。两级兜底都试一遍，
		// 都不中就原样透传（= 官方封面行为）。
		if data, ctype, hit := i.entityCover(r, sub); hit {
			i.writeCover(w, r, data, ctype)
			return true
		}
		i.passThrough(w, r)
		return true
	}

	data, ctype := i.coverBytes(r.Context(), track, fake)
	i.writeCover(w, r, data, ctype)
	return true
}

// writeCover 把封面字节写出去（含占位图与缓存头策略）。
func (i *Interceptor) writeCover(w http.ResponseWriter, r *http.Request, data []byte, ctype string) {
	// placeholder 为 true 表示这次没取到真图，发的是兜底图。
	placeholder := len(data) == 0
	if placeholder {
		data, ctype = placeholderPNG, "image/png"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", itoa(len(data)))
	if placeholder {
		// 兜底图**绝不能**被缓存。
		//
		// 封面 URL 是「平台 + 平台内 id」的确定性哈希，重启后还是同一个 URL，
		// 所以这里一旦给了长缓存，一次瞬时失败（取链抖动、风控窗口、网络超时）
		// 就会把破图钉在客户端七天，而且 URL 不变 → 永远等不到自愈。
		// no-store 让下一次请求重新来取，取到了就立刻换成真图。
		w.Header().Set("Cache-Control", "no-store")
	} else {
		// 真图不变，让客户端放心长缓存。
		w.Header().Set("Cache-Control", "public, max-age=604800")
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// coverBytes 取封面字节，优先用本地磁盘缓存。
//
// 缓存落在数据目录下的 `covers/` 里，键就是虚拟 id —— 虚拟 id 由「平台 +
// 平台内 id」确定性哈希而来，所以同一个封面永远落到同一个文件名，重启后
// 缓存依然有效。
func (i *Interceptor) coverBytes(ctx context.Context, t online.Track, fake string) ([]byte, string) {
	if strings.TrimSpace(t.CoverURL) == "" {
		return nil, ""
	}
	return i.coverURLBytes(ctx, t.CoverURL, fake, t.RealID())
}

// entityCover 按「歌手 / 纯专辑锚点」的虚拟 id 取封面。
//
// hit 表示「这个 id 是我们下发的实体」—— 此时即便取图失败也要发占位图（与曲目同
// 一条约定）；false 表示不是我们的 id，调用方应原样透传。
func (i *Interceptor) entityCover(r *http.Request, sub string) ([]byte, string, bool) {
	coverURL, cacheKey, logID, ok := i.entityCoverTarget(r, sub)
	if !ok {
		return nil, "", false
	}
	data, ctype := i.coverURLBytes(r.Context(), coverURL, cacheKey, logID)
	return data, ctype, true
}

// entityCoverTarget 把请求里的 id 反解成「该抓哪个封面地址」。
//
// 与 entityCover 拆开是为了可测：真抓一次要打外网，而「反解对了没有」这件事
// 只看这三个返回值就够了（cacheKey 是磁盘缓存文件名、logID 只进日志）。
func (i *Interceptor) entityCoverTarget(r *http.Request, sub string) (coverURL, cacheKey, logID string, ok bool) {
	for _, c := range albumRequestIDs(r, sub) {
		form := strings.TrimSpace(c)
		form = strings.TrimSuffix(form, ":"+online.SubKindArtist)
		form = strings.TrimSuffix(form, ":"+online.SubKindAlbum)
		if form == "" {
			continue
		}
		// 歌手头像（搜索结果 / 歌手页 / 收藏里点歌手都走这一条）
		if a, hit := i.artists.Lookup(form); hit {
			return a.CoverURL, form, a.RealID(), true
		}
		// 纯专辑锚点（歌手页的专辑卡片没有曲目，只能靠这张表）
		if ref, hit := i.albumRefs.Lookup(form); hit {
			return ref.CoverURL, form, ref.RealID(), true
		}
	}
	return "", "", "", false
}

// coverURLBytes 按 URL 取封面字节（磁盘缓存键为 cacheKey）。
func (i *Interceptor) coverURLBytes(ctx context.Context, coverURL, cacheKey, logID string) ([]byte, string) {
	coverURL = strings.TrimSpace(coverURL)
	if coverURL == "" {
		return nil, ""
	}
	cachePath := filepath.Join(i.cfg.DataDir, "covers", cacheKey)
	if data, err := os.ReadFile(cachePath); err == nil && len(data) > 0 {
		return data, sniffImageType(data)
	}

	ctx, cancel := context.WithTimeout(ctx, coverFetchBudget)
	defer cancel()

	req, err := online.NewRequest(ctx, http.MethodGet, coverURL, "")
	if err != nil {
		return nil, ""
	}
	resp, err := online.SharedClient().Do(req)
	if err != nil {
		i.logf("[INTERCEPT] 拉取封面失败 %s：%v", logID, err)
		return nil, ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, coverMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > coverMaxBytes {
		return nil, ""
	}
	// 只认真正的图片：第三方站点的错误页常常是 200 + HTML。
	ctype := sniffImageType(data)
	if ctype == "" {
		return nil, ""
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
		tmp := cachePath + ".part"
		if err := os.WriteFile(tmp, data, 0o644); err == nil {
			_ = os.Rename(tmp, cachePath)
		}
	}
	return data, ctype
}

// sniffImageType 按魔数判断图片类型，认不出返回空串。
//
// 不信任 Content-Type：第三方 CDN 经常把图片标成 application/octet-stream，
// 而反爬页面又会把自己标成 image/jpeg。只看字节。
func sniffImageType(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[0:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[0:6]) == "GIF87a" || string(data[0:6]) == "GIF89a"):
		return "image/gif"
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) >= 2 && data[0] == 'B' && data[1] == 'M':
		return "image/bmp"
	}
	return ""
}

// itoa 是无依赖的十进制转换（避免为一个数字引入 strconv 到本文件）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
