package nas

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"fn-lx-player/pkg/namefmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/dirwalk"
	"fn-lx-player/pkg/fnos"
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
	// HiddenCount / HiddenDirs：本目录里因为「没开 hidden」被折叠掉的**子目录**数量与前几个名字。
	//
	// 为什么要有：只给一个 hidden=1 开关不够 —— 用户 2026-10-25 两次报障
	// 「曲库在 music-v2/.dev/music，中间的 .dev 在目录选择器里看不到」，
	// 开关就摆在界面上但他没注意到。有了这两个字段，前端可以直接在列表底部写
	// 「还有 2 个隐藏目录（.dev、.tools）· 显示」，不用指望用户自己发现开关。
	HiddenCount int      `json:"hidden_count"`
	HiddenDirs  []string `json:"hidden_dirs,omitempty"`
}

// getAvailableVolumes 没有显式授权时的兜底：挂载点探测。
//
// ⚠️ 2026-10-29：以前这里把 `/home` 也当成「卷」—— 在飞牛上 `/home` 里放的是各用户
// 家目录（`/home/liwenyao`），它作为允许根会把**整个家目录**纳入可删范围，
// 并且让回收站计算把 `.tidy_trash` 落到用户没权限写的 `/home/.tidy_trash`
// （用户报「本地曲库删歌…实际没有删除」的直接原因）。
//
// 现在 `/home` **无条件不收**，不是「有条件地收」：任何环境（包括家目录自己就是挂载点的
// 老式发行版）都别指望这个兜底把你家目录放出来。理由是这里的返回值直接当**删除/浏览的
// 安全边界**用，猜错一次的代价是整个家目录可被删；而真心需要家目录里的曲库时，
// 有两条明确的路可走 —— `FN_ALLOWED_ROOTS` 环境变量，或飞牛管理员授权目录
// （见 AllowedRoots 的 ①/② 优先级）。这类授权是显式的，出问题也能一眼看出是谁配的。
func getAvailableVolumes() []string {
	vols := make([]string, 0)
	for i := 1; i <= 16; i++ {
		p := fmt.Sprintf("/vol%d", i)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			vols = append(vols, p)
		}
	}
	for _, p := range []string{"/media", "/mnt"} {
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
	// hidden=1 时连 `.` `$` `@` 开头的隐藏/系统目录一起列出来。
	// 默认仍然藏起来（`.tidy_trash`、`@eaDir`、`$RECYCLE.BIN` 这类不该被选成曲库），
	// 但「曲库正好放在 .dev/music 这种点目录里」时必须有办法看见 —— 由前端给显式开关。
	wantHidden := strings.TrimSpace(r.URL.Query().Get("hidden")) == "1"
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
	hiddenCount := 0
	hiddenDirs := make([]string, 0, 4)

	entries, err := os.ReadDir(realPath)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if !wantHidden && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") || strings.HasPrefix(name, "@")) {
				// 记下被折叠的子目录：前端用它渲染「还有 N 个隐藏目录 · 显示」。
				// 只统计目录 —— 列表里本来就只有目录，隐藏的普通文件说了也没用。
				if e.IsDir() {
					hiddenCount++
					if len(hiddenDirs) < 12 {
						hiddenDirs = append(hiddenDirs, name)
					}
				}
				continue
			}
			fullPath := filepath.ToSlash(filepath.Join(realPath, name))
			if e.IsDir() {
				hasSub := false
				subAudio := 0
				if subEntries, sErr := os.ReadDir(filepath.Join(realPath, name)); sErr == nil {
					for _, se := range subEntries {
						sName := se.Name()
						if !wantHidden && strings.HasPrefix(sName, ".") {
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
			Current:     cleanPath,
			Parent:      parent,
			Volumes:     volumes,
			Folders:     folders,
			Files:       files,
			AudioCount:  audioCount,
			HiddenCount: hiddenCount,
			HiddenDirs:  hiddenDirs,
		},
	})
}

func ListFolders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// 飞牛环境：直接回**已授权目录**（AllowedRoots() 在飞牛里只含官方授权目录）。
	// 以前这里先试一堆预设路径（/vol1/Music、/vol1、/vol2…），在飞牛里虽然会被
	// ResolveSafePath 过滤掉未授权项，但一旦管理员恰好授权了 /vol1，预设里的
	// `/vol1` 就会被当成「快速切换」显示出来 —— 用户以为曲率能看见整块盘。
	// 官方模型是「没授权 = 看不见」，所以这里干脆不做猜测，只回真实授权列表。
	if fnos.Detected() {
		folders := AllowedRoots()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    200,
			"message": "ok",
			"data":    folders,
			"folders": folders,
		})
		return
	}

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
		// 封面地址必须**转义**：路径里带空格、`#`、`?`、`&`、`+` 时，
		// 原样拼进查询串会被截断或改写（`#` 之后整段被当锚点丢掉），
		// 表现为「这首歌有封面但界面一直是 💿」。
		// 2026-11-01（用户：「本地曲库还是看不到封面」）。
		coverURL := "/api/nas/cover?path=" + url.QueryEscape(f.Path)
		// has_cover：这一首**真的能取到图**才给地址。以前不管有没有都给，
		// 界面上就是一批 <img> 各吃一个 404 再回落成 💿 —— 多花的请求还是次要的，
		// 更糟的是列表里到处闪一下空盒子。这里按同一套判定先问一次（只读元数据，
		// 不解码音频），没图的直接给空串，界面立刻画占位。
		if !hasEmbeddedCover(f.Path) {
			coverURL = ""
		}
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
	// 文件名反解走**用户配的命名格式**（`歌手 - 歌名` 还是 `歌名 - 歌手`），见 pkg/namefmt。
	// 下载写入与这里的读取必须是同一份约定，否则新下的文件歌手/歌名会读反。
	// 顺带修正一处老毛病：只按**第一个**分隔符切，「甲 - 乙 - 丙」得 artist=甲、
	// title=乙 - 丙（改造前用 Split 会把「- 丙」直接丢掉）。
	artist, title = namefmt.Parse(base)
	if title == "" {
		title = base
	}
	if artist == "" {
		artist = "本地歌手"
	}
	album = "NAS 音乐"

	// 内嵌标签优先。改造前这里**只对 .mp3** 试 ID3v2，FLAC 的元数据完全靠文件名 ——
	// 所以换命名格式对 FLAC 曲库的影响是直接的（歌名歌手直接对调）。
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp3":
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
	case ".flac":
		if meta, err := tags.Read(filePath); err == nil {
			if isCleanMeta(meta.Title) {
				title = meta.Title
			}
			if isCleanMeta(meta.Artist) {
				artist = meta.Artist
			}
			if isCleanMeta(meta.Album) {
				album = meta.Album
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
		// 用内嵌字节算 ETag：文件没换就 304，换了立刻出新图。
		// 以前固定 max-age=86400，用户补了封面后最长要等一天才看得到，
		// 而且中间一直被旧缓存盖住（2026-11-01「看不到封面」的次因）。
		etag := fmt.Sprintf(`W/"%x-%x"`, len(data), fnv32(data))
		w.Header().Set("ETag", etag)
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(data)
		return
	}

	http.NotFound(w, r)
}

// fnv32 给封面算一个廉价的指纹（只要变一点就不同即可，不要密码学强度）。
func fnv32(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// imageSignatures 是封面里**真实存在**的图片格式与它们的魔数。
//
// 为什么不能只认 JPEG/PNG（2026-11-01 用户报「本地曲库的封面怎么看不到了」）：
// 现在不少音源（含本项目的下载链路）内嵌的是 **WebP** 封面。旧实现只扫
// FF D8 FF / 89 50 4E 47 两个魔数，遇到 WebP 一律当成「没有封面」→ 接口 404，
// 界面整列掉回 💿 占位符。GIF 同理。
var imageSignatures = []struct {
	magic []byte
	mime  string
}{
	{[]byte{0xFF, 0xD8, 0xFF}, "image/jpeg"},
	{[]byte{0x89, 0x50, 0x4E, 0x47}, "image/png"},
	// RIFF....WEBP：只匹配 "RIFF" 太宽（wav/avi 也用它），这里连 WEBP 一起看
	{[]byte("RIFF"), "image/webp"},
	{[]byte("GIF8"), "image/gif"},
	{[]byte{0x42, 0x4D}, "image/bmp"},
}

// sliceImage 从 APIC 帧体（含 mime 文本、描述串等前缀）里切出真正的图片字节。
//
// 取**最靠前**的那个魔数：帧体开头是 `image/webp\x00\x03\x00` 这类头，
// 真正的 RIFF 紧跟其后；用「谁先出现」判断比按格式逐个试更稳，
// 也不会因为描述里恰好含有某个字节串就切错（描述串在图片之前，切出来的第一段
// 必然是图片本身的起点）。
func sliceImage(frameData []byte) ([]byte, string) {
	best := -1
	bestMime := ""
	for _, sig := range imageSignatures {
		idx := bytes.Index(frameData, sig.magic)
		if idx == -1 {
			continue
		}
		// WebP 还要确认后面跟的是 "WEBP"（RIFF 容器也可能装 wav）
		if sig.mime == "image/webp" {
			// RIFF 头之后紧跟 4 字节文件长度，再往后才是 "WEBP"
			at := idx + 8
			if at+4 > len(frameData) || string(frameData[at:at+4]) != "WEBP" {
				continue
			}
		}
		if best == -1 || idx < best {
			best = idx
			bestMime = sig.mime
		}
	}
	if best == -1 {
		return nil, ""
	}
	return frameData[best:], bestMime
}

// hasEmbeddedCover 判断这一首有没有可用的封面 —— 与 extractCoverBytes 同一套
// 候选口径（内嵌 APIC/PICTURE + 同目录同名图 / cover / folder / front），
// 但**只读元数据不解码**，扫描一首的代价只有几个字节的头部读取。
//
// 用途：把「一定取不到图」的曲目提前标成没有封面，界面直接画占位，
// 不再让列表里一排 <img> 各打一个 404（2026-11-01「本地曲库看不到封面」）。
// 判断失败时一律返回 true —— 宁可多试一次请求，也不要漏掉本来有的图。
func hasEmbeddedCover(filePath string) bool {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".mp3":
		f, err := os.Open(filePath)
		if err != nil {
			return true
		}
		defer f.Close()
		header := make([]byte, 10)
		if _, err := io.ReadFull(f, header); err != nil || string(header[:3]) != "ID3" {
			// 无 ID3 标签：封面只可能来自同目录图，交给下面的兜底判定
			return folderCoverExists(filePath) || fileHasTrailingCover(filePath)
		}
		tagSize := synchsafeInt(header[6:10])
		if tagSize <= 0 || tagSize > 25*1024*1024 {
			return folderCoverExists(filePath)
		}
		body := make([]byte, tagSize)
		if _, err := io.ReadFull(f, body); err != nil {
			return true
		}
		return bytes.Contains(body, []byte("APIC")) || folderCoverExists(filePath)
	case ".flac":
		if meta, err := tags.Read(filePath); err == nil && len(meta.Cover) > 0 {
			return true
		}
		return folderCoverExists(filePath)
	}
	// 其它格式（wav/m4a/aac/ogg…）没有内嵌封面解析，只有同目录图这一条路
	return folderCoverExists(filePath)
}

// 同目录同名图 / cover / folder / front（与 extractCoverBytes 的候选保持一份口径）
func folderCoverExists(filePath string) bool {
	dir := filepath.Dir(filePath)
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	for _, name := range []string{
		base + ".jpg", base + ".jpeg", base + ".png",
		"cover.jpg", "cover.png", "folder.jpg", "folder.png", "front.jpg",
	} {
		cp := filepath.Join(dir, name)
		if _, gerr := ResolveSafePath(cp); gerr != nil {
			continue
		}
		if st, err := os.Stat(cp); err == nil && st.Size() > 0 {
			return true
		}
	}
	return false
}

// 有些 mp3 把图片追加在音频尾部（非标准但常见）：看到图片魔数就当有封面。
func fileHasTrailingCover(filePath string) bool {
	f, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	n, _ := f.Read(buf)
	for _, sig := range [][]byte{
		{0xFF, 0xD8, 0xFF},    // JPEG
		{0x89, 'P', 'N', 'G'}, // PNG
	} {
		if bytes.Contains(buf[:n], sig) {
			return true
		}
	}
	return false
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
								if data, mime := sliceImage(frameData); len(data) > 0 {
									return data, mime
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
	// 「歌名.lrc」这个候选要按用户配的命名格式反解出歌名（见 pkg/namefmt）：
	// 写死「歌手在前」的话，命名格式一换，这条例外的歌词文件就再也找不到了。
	if _, title := namefmt.Parse(base); title != "" && title != base {
		candidates = append(candidates,
			filepath.Join(dir, title+".lrc"),
			filepath.Join(dir, title+".LRC"),
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
	// 「歌名.lrc」这个候选要按用户配的命名格式反解出歌名（见 pkg/namefmt）：
	// 写死「歌手在前」的话，命名格式一换，这条例外的歌词文件就再也找不到了。
	if _, title := namefmt.Parse(base); title != "" && title != base {
		candidates = append(candidates,
			filepath.Join(dir, title+".lrc"),
			filepath.Join(dir, title+".LRC"),
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
