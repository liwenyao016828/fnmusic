package api

import (
	"encoding/json"
	"net/http"

	"fn-lx-player/pkg/downloader"
	"fn-lx-player/pkg/library"
)

// 本文件负责把「曲库管家」维护的全库索引接到下载器上，并暴露一个只读查询接口。
// 两者的判据是**同一份代码**（downloader.MatchLibrary）：各写一份迟早会出现
// 「查询页说没有、下载器说有」的互相打脸。

// attachDownloadLibraryIndex 把全库索引接到下载器，让 /download/song 与
// /download/check 在下载目录之外也能认出「同一首歌」。
//
// 调用位置：NewServer 里 `s.libIndex = library.NewIndex(...)` 之后。下载器比
// libIndex 先构造，所以不能走 NewDownloader 的参数。
//
// 注入的是**读取函数**而不是一份切片：索引会随扫描整份替换，注入拷贝等于把
// 下载器的视野钉死在启动那一刻，用户刚扫完的新文件永远看不见。
// 重复调用是安全的（后者覆盖前者）；不调用也能正常工作，只是退化成
// 「只按下载目录判重」的旧行为。
func (s *Server) attachDownloadLibraryIndex() {
	if s == nil || s.downloader == nil {
		return
	}
	s.downloader.SetLibraryIndex(func() []library.Entry {
		idx := s.libIndex
		if idx == nil {
			return nil
		}
		return idx.List()
	})
}

// HandleDownloadLibraryLookup 批量查询「这些歌曲库里是不是已经有了」。
//
//	POST /api/download/library/lookup
//	{"songs":[{"id":"...","name":"晴天","singer":"周杰伦","album":"叶惠美","duration":269}]}
//
// 只读、无副作用：不改索引、不碰文件，也不判断会不会真的下载（那是
// shouldUpgradeOver 的事，取决于调用方有没有带 upgrade_if_better）。
// 每条结果带上 Hit（命中详情，含 path/score/tier/duration/upgradable），
// 没命中时 Hit 为 null —— 与 /download/check 的 exists 语义区分开：
// 这里回答「盘上有没有」，那里回答「要不要下载」。
func (s *Server) HandleDownloadLibraryLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Songs []downloader.SongPayload `json:"songs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	type item struct {
		ID  string                    `json:"id,omitempty"`
		Hit *downloader.LocalIndexHit `json:"hit"`
	}

	// 一次请求只取一份快照：几百首的批量查询不该把索引列表拷几百遍。
	var entries []library.Entry
	if s != nil && s.libIndex != nil {
		entries = s.libIndex.List()
	}
	out := make([]item, 0, len(req.Songs))
	for _, song := range req.Songs {
		it := item{ID: song.ID}
		if hit, ok := downloader.MatchLibrary(entries, song); ok {
			h := hit
			it.Hit = &h
		}
		out = append(out, it)
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"results": out,
	})
}
