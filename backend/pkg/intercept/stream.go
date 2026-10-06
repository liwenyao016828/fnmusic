package intercept

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"fn-lx-player/pkg/online"
)

// mediaClient 是取第三方 CDN 音频用的客户端。
//
// 与解析用的 sharedClient 分开：媒体是**流式长响应**，超时语义完全不同 ——
// 解析是「12 秒内拿到一小段 JSON」，取流是「建立连接后可能持续几分钟」。
// 用一个客户端就得为两边各让一步，结果两边都不合适。
var mediaClient = &http.Client{
	// 不设 Timeout：那是「整个响应读完」的上限，一首歌要几分钟。连接与
	// 首字节超时交给 Transport 管。
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
	},
}

// handleStream 接管官方客户端的取流请求。
//
// 官方客户端的 `<audio>` 指向 `/music/api/v1/track/stream?guid=…`。本地曲目
// 这条请求会透传给官方后端（它能直接读磁盘），在线曲目则由我们解析出第三方
// 直链再代理过来。
//
// # 为什么是「代理」而不是 302 跳转
//
// 直链是第三方 CDN 的地址，客户端能不能直连不由我们决定（官方 App 可能跑在
// 外网、可能有 DNS 或地区限制）。更重要的是 **Range 请求**：进度条拖动需要
// 服务端支持字节范围，而 CDN 直链的 Range 行为各家不一。在这里统一代理，
// 客户端看到的始终是一个稳定的同源地址。
func (i *Interceptor) handleStream(w http.ResponseWriter, r *http.Request) bool {
	sub := subPathAfter(r.URL.Path, apiPrefix+"/track/stream")
	track, fake, ok := i.lookupTrack(r, sub)
	if !ok {
		i.passThrough(w, r)
		return true
	}

	// 「收藏自动绑定本地」的落地点：这个虚拟 guid 已经下到本地了就直接服务本地
	// 文件（带 Range），不再代理在线流 —— 收藏条目继续可用，但已经在听硬盘那份。
	if i.serveDownloadedIfAny(w, r, fake) {
		return true
	}

	// 「边听边存」**关着**时留下的滚动试听缓存：命中就完全不出网（见 tee.go）。
	// 刻意排在「已进曲库」之后 —— 永久那一份更权威，缓存只是临时的。
	if i.serveRollingIfAny(w, r, fake) {
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	res, err := i.pool.Resolve(ctx, track.Platform, track.PlatformID)
	if err != nil {
		i.logf("[INTERCEPT] 在线取流失败 %s（%s/%s）：%v",
			fake, track.Platform, track.PlatformID, err)
		// 404 + 固定文案：官方客户端对非 2xx 的处理就是「这首歌播不了」，
		// 给一个它认识的形状，别让它去解析我们自定义的错误体。
		writeJSON(w, http.StatusNotFound, map[string]any{
			"code": 404, "msg": "online source unavailable", "data": nil,
		})
		return true
	}

	i.proxyMedia(w, r, track, fake, res)
	return true
}

// proxyMedia 把第三方直链的内容转发给客户端，保留 Range 语义。
func (i *Interceptor) proxyMedia(w http.ResponseWriter, r *http.Request, t online.Track, fake string, res *online.Resolved) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, res.URL, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"code": 502, "msg": "bad media url", "data": nil,
		})
		return
	}
	applyMediaHeaders(req, t, r.Header.Get("Range"))
	resp, err := mediaClient.Do(req)
	if err != nil {
		i.logf("[INTERCEPT] 拉取媒体失败 %s：%v", t.RealID(), err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"code": 502, "msg": "media fetch failed", "data": nil,
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// 只回传对客户端有意义的头。逐跳头、以及 CDN 自己的 cookie/set-cookie
	// 都不该泄给官方客户端。
	for _, name := range []string{
		"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
		"Last-Modified", "ETag", "Cache-Control",
	} {
		for _, v := range resp.Header.Values(name) {
			w.Header().Add(name, v)
		}
	}
	if w.Header().Get("Accept-Ranges") == "" {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	// 边听边下：把同一条流转手写一份到磁盘。开关决定归宿 —— 开着落曲库，
	// 关着只留滚动试听缓存（见 tee.go）。已经在库里 / 客户端要的是分片 /
	// 上游没给总长 → beginTee 返回 nil，走原来的纯转发。
	tt := i.beginTee(fake, t, resp, r)
	if tt == nil {
		// io.Copy 会流式转发，不缓冲整首歌。
		_, _ = io.Copy(w, resp.Body)
		return
	}
	// 读侧单独包一层：io.Copy 的错误分不清「客户端不写了」还是「上游断了」，
	// 而这两件事在收尾时完全不同 —— 前者可以把半截交接给后台续传，后者不能
	// （见 tee.go 的 teeInterrupt）。
	rd := &teeReadErr{r: resp.Body}
	_, _ = io.Copy(&teeWriter{w: w, t: tt}, rd)
	i.finishTeeAfterStream(tt, fake, t, resp.ContentLength, res.Format, teeInterrupt{
		readErr: rd.err,
		// net/http 在客户端断开（切歌 / 关页面 / 手机切网）时会取消请求上下文 ——
		// 这是「客户端走了」最直接的证据，也是唯一需要判的东西（见 teeInterrupt）。
		clientGone: r.Context().Err() != nil,
	})
}

// applyMediaHeaders 给取媒体（第三方 CDN）的请求装上这一套头。
//
// 两个调用方必须完全一致，所以只有这一份：播放那条流（proxyMedia，Range 原样
// 转发）与后台续传（tee.go 的 resumeInto，Range 是要补的那一段）。少一个头就
// 会被 CDN 的防盗链挡在外面，而那种失败看起来像「源站偶尔抽风」。
func applyMediaHeaders(req *http.Request, t online.Track, rng string) {
	req.Header.Set("User-Agent", online.DesktopUA)
	req.Header.Set("Accept", "*/*")
	// 部分 CDN 会按 Referer 做防盗链，带上来源站的地址。
	if ref := mediaReferer(t.Platform); ref != "" {
		req.Header.Set("Referer", ref)
	}
	// **必须**带 Range：进度条拖动、边听边存的中断续传都靠它。
	if rng = strings.TrimSpace(rng); rng != "" {
		req.Header.Set("Range", rng)
	}
	// 不要压缩：音频已经是压缩格式，再套一层 gzip 只会让 Range 偏移对不上。
	req.Header.Set("Accept-Encoding", "identity")
}

// mediaReferer 返回各平台 CDN 期望的 Referer。
func mediaReferer(platform string) string {
	switch platform {
	case "wy":
		return "https://music.163.com/"
	case "tx":
		return "https://y.qq.com/"
	case "kg":
		return "https://www.kugou.com/"
	case "kw":
		return "https://www.kuwo.cn/"
	}
	return ""
}

// playbackFrom 把解析结果转成 VO 需要的播放信息。
//
// 有了真实的大小与格式，客户端的时长/码率显示与进度条才准确；解析失败时
// 调用方传零值 Playback，客户端会按 mp3/320k 的默认值显示。
func playbackFrom(res *online.Resolved) online.Playback {
	if res == nil {
		return online.Playback{}
	}
	return online.Playback{
		Format:  strings.ToLower(res.Format),
		Size:    res.Size,
		Bitrate: res.Bitrate,
	}
}
