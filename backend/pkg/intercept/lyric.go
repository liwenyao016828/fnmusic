package intercept

import (
	"net/http"
	"strings"
	"time"

	"fn-lx-player/pkg/online"
	"fn-lx-player/pkg/search"
)

// 歌词取回与缓存。
const (
	// lyricCacheTTL 是歌词缓存时长。歌词基本不变，可以缓存久一点。
	lyricCacheTTL = 6 * time.Hour
	// lyricFetchBudget 是单次取歌词的等待上限。
	//
	// 歌词是播放的**附属品**：拿不到就安静地不给，绝不能因为第三方歌词接口
	// 卡住而让客户端的歌词面板一直转圈。
	lyricFetchBudget = 6 * time.Second
)

// lyricEntry 缓存一首歌的歌词正文。
type lyricEntry struct {
	text string
	ts   time.Time
}

// handleLyricList 接管 `GET /music/api/v1/lyric/list`。
//
// 官方客户端先问「这首歌有哪些歌词」，拿到 preferred 之后再取正文。
// 在线曲目没有官方歌词记录，所以这里直接构造一份。
func (i *Interceptor) handleLyricList(w http.ResponseWriter, r *http.Request) bool {
	sub := subPathAfter(r.URL.Path, apiPrefix+"/lyric/list")
	_, fake, ok := i.lookupTrack(r, sub)
	if !ok {
		i.passThrough(w, r)
		return true
	}
	text := i.lyricFor(fake)
	writeJSON(w, http.StatusOK, online.LyricListVO(fake, text, time.Now().Unix()))
	return true
}

// handleLyricText 接管 `GET /music/api/v1/track/lyrics` 与
// `GET /music/api/v1/detail/lyrics/{subpath}`。
//
// 两个端点的响应形状不同（前者 `{guid, lyric}`、后者同形），所以共用一个
// handler；路径靠前缀区分，认不出前缀时 subpath 为空、走 query 参数。
func (i *Interceptor) handleLyricText(w http.ResponseWriter, r *http.Request) bool {
	sub := subPathAfter(r.URL.Path, apiPrefix+"/track/lyrics")
	if sub == "" {
		sub = subPathAfter(r.URL.Path, apiPrefix+"/detail/lyrics")
	}
	_, fake, ok := i.lookupTrack(r, sub)
	if !ok {
		i.passThrough(w, r)
		return true
	}
	text := i.lyricFor(fake)
	if strings.TrimSpace(text) == "" {
		// 没歌词时回 `data:{}`，**不是** `{guid, lyric:""}`。
		// 后者会让客户端认为「有歌词但内容为空」，渲染出一个空白面板。
		writeJSON(w, http.StatusOK, online.EmptyOK())
		return true
	}
	writeJSON(w, http.StatusOK, online.LyricTextVO(fake, text))
	return true
}

// defaultLyricFetcher 是生产环境的歌词实现。
//
// 复用曲率既有的 `search.FetchLyric`：它内部已经实现了「网易按 id 直取 →
// 网易 cloudsearch 兜底 → 酷狗严格校验兜底」的三级链路，并且自带进程内缓存。
func defaultLyricFetcher(t online.Track) string {
	hash := ""
	if t.Platform == "kg" {
		// 酷狗的歌词接口要 file hash，平台内 id 正好就是它。
		hash = t.PlatformID
	}
	lrc, _ := search.FetchLyric(t.Platform, t.PlatformID, t.Title, t.Artist(), t.Duration, hash)
	return lrc
}

// lyricFor 取一首在线曲目的歌词正文（带缓存）。
//
// 复用曲率既有的 `search.FetchLyric`：它内部已经实现了「网易按 id 直取 →
// 网易 cloudsearch 兜底 → 酷狗严格校验兜底」的三级链路，并且自带进程内缓存。
// 这里再包一层缓存是因为我们拿到的入参是虚拟 id，需要先换回真实曲目描述符。
func (i *Interceptor) lyricFor(fakeID string) string {
	i.lyricMu.Lock()
	if e, ok := i.lyricCache[fakeID]; ok && time.Since(e.ts) < lyricCacheTTL {
		i.lyricMu.Unlock()
		return e.text
	}
	i.lyricMu.Unlock()

	track, ok := i.registry.Lookup(fakeID)
	if !ok {
		return ""
	}

	// 曲率的 FetchLyric 是同步阻塞的，且内部没有可传入的 context。
	// 用「有缓冲的 channel + 超时」把它围起来：超时后我们立刻返回空歌词，
	// 那个 goroutine 自己跑完就退（channel 有缓冲，不会阻塞在发送上）。
	ch := make(chan string, 1)
	go func() { ch <- strings.TrimSpace(i.lyricFetch(track)) }()

	text := ""
	select {
	case text = <-ch:
	case <-time.After(lyricFetchBudget):
		i.logf("[INTERCEPT] 取歌词超时，本次不给歌词：%s", track.RealID())
	}

	i.lyricMu.Lock()
	if len(i.lyricCache) > 512 {
		i.lyricCache = make(map[string]lyricEntry, 64)
	}
	// 取到空也缓存：一次失败后短时间内别再把同一个第三方接口打一遍。
	i.lyricCache[fakeID] = lyricEntry{text: text, ts: time.Now()}
	i.lyricMu.Unlock()

	return text
}
