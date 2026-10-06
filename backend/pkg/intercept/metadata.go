package intercept

import (
	"context"
	"net/http"
	"time"

	"fn-lx-player/pkg/online"
)

// metadataResolveBudget 是元数据端点解析直链的等待上限。
//
// 为什么要在这里解析：官方客户端取元数据之后紧接着就取流，两次请求之间
// 只隔几十毫秒。先在元数据阶段解析一次（结果进 Pool 缓存），取流阶段就能
// 命中缓存直接开播；否则「点一下等一次网络往返」。给 4 秒是因为这是**加分项**
// ——超时就退回未知规格，客户端按 mp3/320k 显示，不影响能不能播。
const metadataResolveBudget = 4 * time.Second

// handleMetadata 接管 `GET /music/api/v1/track/metadata` 与
// `GET /music/api/v1/track/audio-info`。
//
// 两个端点共用同一份数据，官方前端在「曲目详情」和「播放前准备」两处分别调，
// 所以必须都拦 —— 只拦一个会出现「详情页显示在线曲目、播放时却说找不到」。
func (i *Interceptor) handleMetadata(w http.ResponseWriter, r *http.Request) bool {
	sub := subPathAfter(r.URL.Path, apiPrefix+"/track/metadata")
	if sub == "" {
		sub = subPathAfter(r.URL.Path, apiPrefix+"/track/audio-info")
	}
	track, fake, ok := i.lookupTrack(r, sub)
	if !ok {
		i.passThrough(w, r)
		return true
	}

	playback := online.Playback{}
	ctx, cancel := context.WithTimeout(r.Context(), metadataResolveBudget)
	if res, err := i.pool.Resolve(ctx, track.Platform, track.PlatformID); err == nil {
		playback = playbackFrom(res)
	}
	cancel()

	favorites := i.store.FavoriteGUIDs(i.userKey(r))
	hasLyric := i.lyricFor(fake) != ""

	writeJSON(w, http.StatusOK, online.MetadataVO(track, playback, favorites[fake], hasLyric))
	return true
}
