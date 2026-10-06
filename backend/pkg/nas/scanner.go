package nas

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/dirwalk"
	"fn-lx-player/pkg/tags"
)

type LocalSong struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	Duration  int    `json:"duration"` // in seconds
	Interval  int    `json:"interval"`
	Size      int64  `json:"size"`
	Format    string `json:"format"`
	CoverURL  string `json:"cover_url"`
	Cover     string `json:"cover"`
	HasLyric  bool   `json:"has_lyric"`
	UpdatedAt int64  `json:"updated_at"`
}

type FolderItem struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	HasSubdirs bool   `json:"has_subdirs"`
	AudioCount int    `json:"audio_count"`
}

// FileItem 目录里的**普通文件**（只在 `files=1` 时才下发，见 BrowseDirectory）。
//
// 为什么不默认下发：音乐浏览页只需要目录 + 音频计数，把目录里成百上千个文件也塞进响应
// 会白白放大 JSON。只有「从 NAS 选脚本」这类场景才要文件列表。
type FileItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type BrowseData struct {
	Current    string       `json:"current"`
	Parent     string       `json:"parent"`
	Volumes    []string     `json:"volumes"`
	Folders    []FolderItem `json:"folders"`
	Files      []FileItem   `json:"files,omitempty"`
	AudioCount int          `json:"audio_count"`
}

func getAvailableVolumes() []string {
	vols := make([]string, 0)
	for i := 1; i <= 16; i++ {
		p := fmt.Sprintf("/vol%d", i)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			vols = append(vols, p)
		}
	}
	for _, p := range []string{"/media", "/mnt", "/home"} {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			vols = append(vols, p)
		}
	}
	if len(vols) == 0 {
		vols = []string{"/vol1", "/vol2", "/vol3"}
	}
	return vols
}

func BrowseDirectory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	volumes := AllowedRoots()
	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath == "" {
		if len(volumes) == 0 {
			writePathError(w, ErrNoAllowedRoots)
			return
		}
		rawPath = volumes[0]
	}

	realPath, err := ResolveSafePath(rawPath)
	if err != nil {
		writePathError(w, err)
		return
	}

	cleanPath := filepath.ToSlash(realPath)
	parent := ""
	if candidate := filepath.Dir(realPath); candidate != realPath {
		// 父目录必须同样位于允许根目录内，否则不下发（避免泄露 / 等越权目录）。
		if _, perr := ResolveSafePath(candidate); perr == nil {
			parent = filepath.ToSlash(candidate)
		}
	}

	// files=1 时额外列出普通文件；ext 逗号分隔（如 ext=js,mjs），留空表示不限
	wantFiles := strings.TrimSpace(r.URL.Query().Get("files")) == "1"
	extFilter := map[string]bool{}
	for _, e := range strings.Split(r.URL.Query().Get("ext"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			if !strings.HasPrefix(e, ".") {
				e = "." + e
			}
			extFilter[e] = true
		}
	}

	folders := make([]FolderItem, 0)
	files := make([]FileItem, 0)
	audioCount := 0

	entries, err := os.ReadDir(realPath)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") || strings.HasPrefix(name, "@") {
				continue
			}
			fullPath := filepath.ToSlash(filepath.Join(realPath, name))
			if e.IsDir() {
				hasSub := false
				subAudio := 0
				if subEntries, sErr := os.ReadDir(filepath.Join(realPath, name)); sErr == nil {
					for _, se := range subEntries {
						sName := se.Name()
						if strings.HasPrefix(sName, ".") {
							continue
						}
						if se.IsDir() {
							hasSub = true
						} else {
							ext := strings.ToLower(filepath.Ext(sName))
							if audioext.Exts[ext] {
								subAudio++
							}
						}
					}
				}
				folders = append(folders, FolderItem{
					Name:       name,
					Path:       fullPath,
					HasSubdirs: hasSub,
					AudioCount: subAudio,
				})
			} else {
				ext := strings.ToLower(filepath.Ext(name))
				if audioext.Exts[ext] {
					audioCount++
				}
				if wantFiles && (len(extFilter) == 0 || extFilter[ext]) {
					var size int64
					if fi, iErr := e.Info(); iErr == nil {
						size = fi.Size()
					}
					files = append(files, FileItem{Name: name, Path: fullPath, Size: size})
				}
			}
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"message": "ok",
		"data": BrowseData{
			Current:    cleanPath,
			Parent:     parent,
			Volumes:    volumes,
			Folders:    folders,
			Files:      files,
			AudioCount: audioCount,
		},
	})
}

func ListFolders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	presetPaths := []string{
		"/vol1/Music",
		"/vol1/music",
		"/vol1/1000/Music",
		"/vol2/Music",
		"/vol2/music",
		"/vol3/Music",
		"/vol1",
		"/vol2",
		"/vol3",
		"/vol4",
	}

	folders := make([]string, 0)
	for _, p := range presetPaths {
		// 仅返回确实存在且位于允许根目录内的目录，避免泄露根目录之外的路径。
		real, err := ResolveSafePath(p)
		if err != nil {
			continue
		}
		folders = append(folders, filepath.ToSlash(real))
	}

	if len(folders) == 0 {
		folders = AllowedRoots()
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"message": "ok",
		"data":    folders,
		"folders": folders,
	})
}

// scanSongsMaxAudio 浏览接口单次枚举的音频上限。
//
// 上限本身有用（有人把 /vol1 当目录扫时不能把整个 NAS 拖进来），
// 但它以前是硬编码的、而且超限后**什么都不说**：界面看起来只是「这个目录有 1000 首」。
// 现在走到上限会在响应里带 truncated:true + warnings，测试也能把它调小来验证这条路径。
var scanSongsMaxAudio = 1000

func ScanSongs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	if dir == "" {
		roots := AllowedRoots()
		if len(roots) == 0 {
			writePathError(w, ErrNoAllowedRoots)
			return
		}
		// 兼容旧默认值 /vol1/Music，不存在时退回到第一个允许根目录。
		if preferred := filepath.Join(roots[0], "Music"); isResolvableDir(preferred) {
			dir = preferred
		} else {
			dir = roots[0]
		}
	}

	realDir, err := ResolveSafePath(dir)
	if err != nil {
		writePathError(w, err)
		return
	}
	dir = filepath.ToSlash(realDir)

	// 文件缓存：非强制刷新时先命中缓存，避免每次打开页面都全盘重扫。
	// 缓存按目录落盘在数据目录 scan_cache.json，重新打开/刷新浏览器仍在。
	if r.URL.Query().Get("refresh") != "1" {
		if cached := loadScanCache(dir); cached != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code":      200,
				"message":   "ok",
				"dir":       dir,
				"songs":     cached.Songs,
				"total":     cached.Total,
				"cached":    true,
				"truncated": cached.Truncated,
				"warnings":  orEmpty(cached.Warnings),
				"data": map[string]interface{}{
					"dir":       dir,
					"songs":     cached.Songs,
					"total":     cached.Total,
					"cached":    true,
					"truncated": cached.Truncated,
					"warnings":  orEmpty(cached.Warnings),
				},
			})
			return
		}
	}

	// 遍历走 pkg/dirwalk：与曲库索引同一套规则（排除目录表、深度上限、
	// 软链接环剪枝、协作式取消）。以前这里是自己一份 filepath.Walk：
	// 只跳隐藏目录、没有环保护、到 1000 首就默默截断、读不了的目录不留痕
	// —— 于是「浏览器里少了几首」永远查不出来。
	maxCount := scanSongsMaxAudio
	files, stat := dirwalk.Walk([]string{realDir}, dirwalk.Options{
		MaxAudio: maxCount,
		// 指向允许根目录之外的软链接文件不暴露给前端（越权），
		// 但仍计入 stat.Skipped —— 跳过多少条是看得见的。
		AcceptFile: func(path string, mode fs.FileMode) bool {
			if mode&os.ModeSymlink != 0 && !pathAllowedAfterResolve(path) {
				return false
			}
			return true
		},
	})

	songs := make([]LocalSong, 0, len(files))
	for i, f := range files {
		name := filepath.Base(f.Path)
		title, artist, album := parseBasicMeta(f.Path, name)
		coverURL := fmt.Sprintf("/api/nas/cover?path=%s", f.Path)
		songs = append(songs, LocalSong{
			ID:        fmt.Sprintf("nas_%d", i+1),
			Path:      f.Path,
			Filename:  name,
			Title:     title,
			Artist:    artist,
			Album:     album,
			Size:      f.Size,
			Format:    f.Format,
			CoverURL:  coverURL,
			Cover:     coverURL,
			HasLyric:  checkHasLyric(f.Path),
			UpdatedAt: f.MTime,
		})
	}

	// 扫描完成落盘缓存（含全量扫描结果）
	saveScanCache(dir, songs, stat)

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":      200,
		"message":   "ok",
		"dir":       dir,
		"songs":     songs,
		"total":     len(songs),
		"cached":    false,
		"truncated": stat.LimitHit,
		"warnings":  orEmpty(stat.Warnings),
		"audit":     stat,
		"data": map[string]interface{}{
			"dir":       dir,
			"songs":     songs,
			"total":     len(songs),
			"cached":    false,
			"truncated": stat.LimitHit,
			"warnings":  orEmpty(stat.Warnings),
			"audit":     stat,
		},
	})
}

// orEmpty 让 JSON 里始终是数组而不是 null：调用方直接 len() 就行，不用判空。
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func isCleanMeta(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "\ufffd") {
		return false
	}
	printableCount := 0
	for _, r := range s {
		if r >= 32 && r != 127 && r != 0xfffd {
			printableCount++
		}
	}
	return printableCount > 0
}

func parseBasicMeta(filePath, filename string) (title, artist, album string) {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	parts := strings.Split(base, " - ")
	if len(parts) >= 2 {
		artist = strings.TrimSpace(parts[0])
		title = strings.TrimSpace(parts[1])
	} else {
		title = base
		artist = "本地歌手"
	}
	album = "NAS 音乐"

	// Try extracting ID3v2 if mp3
	if strings.ToLower(filepath.Ext(filePath)) == ".mp3" {
		if t, a, al, ok := readID3v2Tags(filePath); ok {
			if isCleanMeta(t) {
				title = t
			}
			if isCleanMeta(a) {
				artist = a
			}
			if isCleanMeta(al) {
				album = al
			}
		}
	}
	return
}

func readID3v2Tags(filePath string) (title, artist, album string, ok bool) {
	f, err := os.Open(filePath)
	if err != nil {
		return
	}
	defer f.Close()

	header := make([]byte, 10)
	if _, err := io.ReadFull(f, header); err != nil {
		return
	}
	if string(header[:3]) != "ID3" {
		return
	}

	tagSize := synchsafeInt(header[6:10])
	if tagSize <= 0 || tagSize > 10*1024*1024 {
		return
	}

	body := make([]byte, tagSize)
	if _, err := io.ReadFull(f, body); err != nil {
		return
	}

	reader := bytes.NewReader(body)
	for reader.Len() > 10 {
		var frameHeader [10]byte
		if _, err := reader.Read(frameHeader[:]); err != nil {
			break
		}
		frameID := string(frameHeader[:4])
		frameSize := id3FrameSize(frameHeader[4:8], header[3])
		if frameSize <= 0 || frameSize > int64(reader.Len()) {
			break
		}

		frameData := make([]byte, frameSize)
		if _, err := reader.Read(frameData); err != nil {
			break
		}

		text := decodeID3Text(frameData)
		switch frameID {
		case "TIT2":
			title = text
		case "TPE1":
			artist = text
		case "TALB":
			album = text
		}
	}
	ok = true
	return
}

func decodeID3Text(data []byte) string {
	if len(data) <= 1 {
		return ""
	}
	encoding := data[0]
	raw := data[1:]

	var decoded string

	switch encoding {
	case 1, 2: // UTF-16 (with or without BOM)
		if len(raw) >= 2 {
			var endian binary.ByteOrder = binary.LittleEndian
			start := 0
			// Check BOM
			if raw[0] == 0xFE && raw[1] == 0xFF {
				endian = binary.BigEndian
				start = 2
			} else if raw[0] == 0xFF && raw[1] == 0xFE {
				endian = binary.LittleEndian
				start = 2
			} else if encoding == 2 {
				endian = binary.BigEndian
			}

			u16s := make([]uint16, 0, (len(raw)-start)/2)
			for i := start; i+1 < len(raw); i += 2 {
				val := endian.Uint16(raw[i : i+2])
				if val == 0 { // null terminator
					break
				}
				u16s = append(u16s, val)
			}
			runes := utf16.Decode(u16s)
			decoded = string(runes)
		}
	case 3: // UTF-8
		decoded = string(raw)
	case 0: // ISO-8859-1 or GBK/UTF-8
		if utf8.Valid(raw) {
			decoded = string(raw)
		}
	default:
		if utf8.Valid(raw) {
			decoded = string(raw)
		}
	}

	decoded = strings.Trim(decoded, "\x00\r\n\t ")
	decoded = strings.ReplaceAll(decoded, "\ufffd", "")
	decoded = strings.ReplaceAll(decoded, "\ufeff", "")
	return strings.TrimSpace(decoded)
}

func synchsafeInt(b []byte) int64 {
	return int64(b[0]&0x7F)<<21 | int64(b[1]&0x7F)<<14 | int64(b[2]&0x7F)<<7 | int64(b[3]&0x7F)
}

// id3FrameSize 解析 ID3v2 帧长度。
// ID3v2.4 起帧长度使用 syncsafe 编码（每字节仅低 7 位有效），
// ID3v2.2 / v2.3 则是普通大端 32 位。若不区分版本，
// ffmpeg 等工具写出的 ID3v2.4 文件里较大的帧（如 APIC 封面、USLT 歌词）
// 长度会被算成 2 倍以上，随后越界判断直接中断解析，导致封面/歌词读不出来。
func id3FrameSize(raw []byte, majorVersion byte) int64 {
	if majorVersion >= 4 {
		return synchsafeInt(raw)
	}
	return int64(binary.BigEndian.Uint32(raw))
}

func StreamAudio(w http.ResponseWriter, r *http.Request) {
	filePath, err := ResolveSafePath(r.URL.Query().Get("path"))
	if err != nil {
		writePathError(w, err)
		return
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	if !audioext.Exts[ext] {
		writePathError(w, ErrPathNotAudio)
		return
	}

	fi, err := os.Stat(filePath)
	if err != nil || fi.IsDir() {
		http.Error(w, "Audio file not found", http.StatusNotFound)
		return
	}

	f, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "Failed to open audio: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	contentType := "application/octet-stream"
	switch ext {
	case ".mp3":
		contentType = "audio/mpeg"
	case ".flac":
		contentType = "audio/flac"
	case ".wav":
		contentType = "audio/wav"
	case ".m4a", ".aac":
		contentType = "audio/mp4"
	case ".ogg":
		contentType = "audio/ogg"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

func ExtractCover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	filePath, err := ResolveSafePath(r.URL.Query().Get("path"))
	if err != nil {
		writePathError(w, err)
		return
	}

	data, mimeType := extractCoverBytes(filePath)
	if len(data) > 0 {
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(data)
		return
	}

	http.NotFound(w, r)
}

func extractCoverBytes(filePath string) ([]byte, string) {
	// 1. Try audio file tags
	if f, err := os.Open(filePath); err == nil {
		defer f.Close()
		ext := strings.ToLower(filepath.Ext(filePath))

		if ext == ".mp3" {
			header := make([]byte, 10)
			if _, err := io.ReadFull(f, header); err == nil && string(header[:3]) == "ID3" {
				tagSize := synchsafeInt(header[6:10])
				if tagSize > 0 && tagSize <= 25*1024*1024 {
					body := make([]byte, tagSize)
					if _, err := io.ReadFull(f, body); err == nil {
						reader := bytes.NewReader(body)
						for reader.Len() > 10 {
							var frameHeader [10]byte
							if _, err := reader.Read(frameHeader[:]); err != nil {
								break
							}
							frameID := string(frameHeader[:4])
							frameSize := id3FrameSize(frameHeader[4:8], header[3])
							if frameSize <= 0 || frameSize > int64(reader.Len()) {
								break
							}
							frameData := make([]byte, frameSize)
							if _, err := reader.Read(frameData); err != nil {
								break
							}
							if frameID == "APIC" && len(frameData) > 10 {
								jpegIdx := bytes.Index(frameData, []byte{0xFF, 0xD8, 0xFF})
								pngIdx := bytes.Index(frameData, []byte{0x89, 0x50, 0x4E, 0x47})
								if jpegIdx != -1 && (pngIdx == -1 || jpegIdx < pngIdx) {
									return frameData[jpegIdx:], "image/jpeg"
								} else if pngIdx != -1 {
									return frameData[pngIdx:], "image/png"
								}
							}
						}
					}
				}
			}
		} else if ext == ".flac" {
			// 复用 tags 包里经过单测的 FLAC PICTURE 块解析。
			// 旧实现把从 JPEG 魔数起「直到文件末尾」的全部字节都返回，
			// 会把后续的元数据与音频帧一并当作封面吐出去（实测 300KB+ 响应），
			// 既浪费带宽也让封面接口返回非图片数据。
			if meta, err := tags.Read(filePath); err == nil && len(meta.Cover) > 0 {
				mime := meta.CoverMime
				if mime == "" {
					mime = "image/jpeg"
				}
				return meta.Cover, mime
			}
		}
	}

	// 2. Folder cover fallback
	dir := filepath.Dir(filePath)
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	candidates := []string{
		filepath.Join(dir, base+".jpg"),
		filepath.Join(dir, base+".jpeg"),
		filepath.Join(dir, base+".png"),
		filepath.Join(dir, "cover.jpg"),
		filepath.Join(dir, "cover.png"),
		filepath.Join(dir, "folder.jpg"),
		filepath.Join(dir, "folder.png"),
		filepath.Join(dir, "front.jpg"),
	}
	for _, cp := range candidates {
		// 同目录封面候选同样要过安全校验：防止有人放一个指向根目录外的软链接。
		if _, gerr := ResolveSafePath(cp); gerr != nil {
			continue
		}
		if data, err := os.ReadFile(cp); err == nil && len(data) > 0 {
			if strings.HasSuffix(strings.ToLower(cp), ".png") {
				return data, "image/png"
			}
			return data, "image/jpeg"
		}
	}

	return nil, ""
}

// ── NAS 本地歌词同步扫描与读取 ──

func checkHasLyric(filePath string) bool {
	dir := filepath.Dir(filePath)
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	candidates := []string{
		filepath.Join(dir, base+".lrc"),
		filepath.Join(dir, base+".LRC"),
		filepath.Join(dir, base+".txt"),
	}
	parts := strings.Split(base, " - ")
	if len(parts) == 2 {
		candidates = append(candidates,
			filepath.Join(dir, strings.TrimSpace(parts[1])+".lrc"),
			filepath.Join(dir, strings.TrimSpace(parts[1])+".LRC"),
		)
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return true
		}
	}
	return false
}

func HandleNasLyric(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	filePath, err := ResolveSafePath(r.URL.Query().Get("path"))
	if err != nil {
		writePathError(w, err)
		return
	}

	lrcText, source, err := ExtractLyric(filePath)
	if err != nil || strings.TrimSpace(lrcText) == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    404,
			"message": "Lyric not found",
			"data":    nil,
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200,
		"data": map[string]interface{}{
			"lyric":  lrcText,
			"source": source,
		},
	})
}

func ExtractLyric(filePath string) (string, string, error) {
	dir := filepath.Dir(filePath)
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))

	// 1. 同名或歌曲名 .lrc 文件
	candidates := []string{
		filepath.Join(dir, base+".lrc"),
		filepath.Join(dir, base+".LRC"),
		filepath.Join(dir, base+".txt"),
	}
	parts := strings.Split(base, " - ")
	if len(parts) == 2 {
		candidates = append(candidates,
			filepath.Join(dir, strings.TrimSpace(parts[1])+".lrc"),
			filepath.Join(dir, strings.TrimSpace(parts[1])+".LRC"),
		)
	}

	for _, c := range candidates {
		// 外挂歌词候选也要过安全校验：防止软链接把 /etc/passwd 之类读成歌词。
		if _, gerr := ResolveSafePath(c); gerr != nil {
			continue
		}
		if b, err := os.ReadFile(c); err == nil && len(b) > 0 {
			text := decodeLrcText(b)
			if strings.TrimSpace(text) != "" {
				return text, "local_file", nil
			}
		}
	}

	// 2. 音频文件内嵌 ID3v2 USLT 歌词标签
	if strings.ToLower(filepath.Ext(filePath)) == ".mp3" {
		if lrc := extractEmbeddedID3Lyric(filePath); lrc != "" {
			return lrc, "embedded_id3", nil
		}
	}

	return "", "", fmt.Errorf("no lyric found")
}

func decodeLrcText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	// UTF-8 BOM
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return string(b[3:])
	}
	// UTF-16 LE BOM
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		u16 := make([]uint16, 0, (len(b)-2)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u16 = append(u16, binary.LittleEndian.Uint16(b[i:i+2]))
		}
		return string(utf16.Decode(u16))
	}
	// UTF-16 BE BOM
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		u16 := make([]uint16, 0, (len(b)-2)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u16 = append(u16, binary.BigEndian.Uint16(b[i:i+2]))
		}
		return string(utf16.Decode(u16))
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return string(b)
}

func extractEmbeddedID3Lyric(filePath string) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	header := make([]byte, 10)
	if _, err := io.ReadFull(f, header); err != nil || string(header[:3]) != "ID3" {
		return ""
	}

	tagSize := synchsafeInt(header[6:10])
	if tagSize <= 0 || tagSize > 25*1024*1024 {
		return ""
	}

	body := make([]byte, tagSize)
	if _, err := io.ReadFull(f, body); err != nil {
		return ""
	}

	reader := bytes.NewReader(body)
	for reader.Len() > 10 {
		var frameHeader [10]byte
		if _, err := reader.Read(frameHeader[:]); err != nil {
			break
		}
		frameID := string(frameHeader[:4])
		frameSize := id3FrameSize(frameHeader[4:8], header[3])
		if frameSize <= 0 || frameSize > int64(reader.Len()) {
			break
		}

		frameData := make([]byte, frameSize)
		if _, err := reader.Read(frameData); err != nil {
			break
		}

		if (frameID == "USLT" || frameID == "SYLT") && len(frameData) > 5 {
			encoding := frameData[0]
			content := frameData[4:] // skip language 3 bytes
			descEnd := -1
			if encoding == 1 || encoding == 2 {
				for i := 0; i+1 < len(content); i += 2 {
					if content[i] == 0 && content[i+1] == 0 {
						descEnd = i + 2
						break
					}
				}
			} else {
				descEnd = bytes.IndexByte(content, 0)
				if descEnd != -1 {
					descEnd += 1
				}
			}
			var lyricBytes []byte
			if descEnd != -1 && descEnd < len(content) {
				lyricBytes = content[descEnd:]
			} else {
				lyricBytes = content
			}
			toDecode := append([]byte{encoding}, lyricBytes...)
			return decodeID3Text(toDecode)
		}
	}
	return ""
}
