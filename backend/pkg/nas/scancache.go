package nas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/dirwalk"
)

// 目录扫描结果的文件缓存。
//
// 背景：NAS 浏览页每次打开都要全盘 Walk 一遍（读标签判断歌词），
// 大曲库要等好几秒；此前缓存只在浏览器内存里，刷新即丢。
// 这里把「目录 → 歌曲列表」落盘到数据目录 scan_cache.json，
// 重新打开页面直接命中缓存，只有显式刷新或文件变化才重扫。

const (
	scanCacheFileName = "scan_cache.json"
	scanCacheMaxDirs  = 30             // 最多缓存 30 个目录
	scanCacheMaxSongs = 5000           // 单目录最多缓存歌曲数（枚举单次上限 1000，这里留足余量）
	scanCacheTTL      = 72 * time.Hour // 超过 3 天视为过期，重扫
	scanCacheMaxBytes = 32 << 20       // 整个缓存文件上限 32MB
)

type scanCacheEntry struct {
	SavedAt int64       `json:"saved_at"`
	Dir     string      `json:"dir"`
	Songs   []LocalSong `json:"songs"`
	Total   int         `json:"total"`
	// Truncated / Warnings 跟着缓存一起存：命中缓存时也要如实告诉调用方
	// 「这份列表是被上限截断的」——否则缓存会把一次截断结果伪装成完整结果。
	Truncated bool     `json:"truncated,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

type scanCacheFile struct {
	Version int                        `json:"version"`
	Dirs    map[string]*scanCacheEntry `json:"dirs"`
}

var (
	scanCacheMu  sync.Mutex
	scanCache    map[string]*scanCacheEntry
	scanCacheDir string
)

// InitScanCache 从数据目录加载扫描缓存。应在服务启动时调用一次。
func InitScanCache(dataDir string) {
	scanCacheMu.Lock()
	defer scanCacheMu.Unlock()
	scanCacheDir = dataDir
	scanCache = make(map[string]*scanCacheEntry)
	if dataDir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(dataDir, scanCacheFileName))
	if err != nil || len(data) > scanCacheMaxBytes {
		return
	}
	var f scanCacheFile
	if json.Unmarshal(data, &f) != nil || f.Dirs == nil {
		return
	}
	for k, v := range f.Dirs {
		if v == nil || v.Dir != k || len(v.Songs) > scanCacheMaxSongs {
			continue
		}
		scanCache[k] = v
	}
}

func scanCachePath() string {
	if scanCacheDir == "" {
		return ""
	}
	return filepath.Join(scanCacheDir, scanCacheFileName)
}

// loadScanCache 返回 dir 的未过期缓存；不存在/过期/未初始化返回 nil。
func loadScanCache(dir string) *scanCacheEntry {
	scanCacheMu.Lock()
	defer scanCacheMu.Unlock()
	if scanCache == nil {
		return nil
	}
	e := scanCache[filepath.ToSlash(dir)]
	if e == nil {
		return nil
	}
	if time.Since(time.Unix(e.SavedAt, 0)) > scanCacheTTL {
		return nil
	}
	// 返回拷贝，避免调用方修改污染缓存
	cp := *e
	cp.Songs = append([]LocalSong(nil), e.Songs...)
	return &cp
}

// saveScanCache 写入目录扫描结果（异步落盘，失败静默——缓存丢了下次重扫即可）。
//
// st 是本次枚举的对账信息：只取「是否被上限截断」与告警文案一起存，
// 命中缓存时才能把「被截断」如实还原，不把 1000 首当成全部。
func saveScanCache(dir string, songs []LocalSong, st dirwalk.Stats) {
	scanCacheMu.Lock()
	if scanCache == nil {
		scanCacheMu.Unlock()
		return
	}
	if len(songs) > scanCacheMaxSongs {
		scanCacheMu.Unlock()
		return
	}
	dirSlash := filepath.ToSlash(dir)
	scanCache[dirSlash] = &scanCacheEntry{
		SavedAt:   time.Now().Unix(),
		Dir:       dirSlash,
		Songs:     append([]LocalSong(nil), songs...),
		Total:     len(songs),
		Truncated: st.LimitHit,
		Warnings:  append([]string(nil), st.Warnings...),
	}
	// 容量控制：按保存时间淘汰最老的目录
	if len(scanCache) > scanCacheMaxDirs {
		type kv struct {
			k string
			v int64
		}
		all := make([]kv, 0, len(scanCache))
		for k, v := range scanCache {
			all = append(all, kv{k, v.SavedAt})
		}
		sort.Slice(all, func(a, b int) bool { return all[a].v < all[b].v })
		for _, old := range all[:len(all)-scanCacheMaxDirs] {
			delete(scanCache, old.k)
		}
	}
	snapshot := make(map[string]*scanCacheEntry, len(scanCache))
	for k, v := range scanCache {
		snapshot[k] = v
	}
	scanCacheMu.Unlock()

	go flushScanCache(snapshot)
}

// InvalidateScanCache 使某个目录（含其子目录）的缓存失效。
// 文件被改动后调用（下载/整理/移动/删除），保证下次读取重扫。
func InvalidateScanCache(dir string) {
	scanCacheMu.Lock()
	if scanCache == nil {
		scanCacheMu.Unlock()
		return
	}
	dirSlash := strings.TrimSuffix(filepath.ToSlash(dir), "/")
	for k := range scanCache {
		if k == dirSlash || strings.HasPrefix(k, dirSlash+"/") || strings.HasPrefix(dirSlash, k+"/") {
			delete(scanCache, k)
		}
	}
	snapshot := make(map[string]*scanCacheEntry, len(scanCache))
	for k, v := range scanCache {
		snapshot[k] = v
	}
	scanCacheMu.Unlock()
	go flushScanCache(snapshot)
}

// InvalidateScanCacheFile 使包含该文件路径的目录缓存失效。
func InvalidateScanCacheFile(filePath string) {
	InvalidateScanCache(filepath.Dir(filePath))
}

func flushScanCache(snapshot map[string]*scanCacheEntry) {
	path := scanCachePath()
	if path == "" {
		return
	}
	data, err := json.Marshal(scanCacheFile{Version: 1, Dirs: snapshot})
	if err != nil || len(data) > scanCacheMaxBytes {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".scancache-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
	}
}
