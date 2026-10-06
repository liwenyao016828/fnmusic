package downloader

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/nas"
	"fn-lx-player/pkg/search"
	"fn-lx-player/pkg/security"
	"fn-lx-player/pkg/tags"
)

var illegalChars = regexp.MustCompile(`[\\/:*?"<>|\r\n\t]`)

// maxFilenameBytes 文件名（不含扩展名）的字节上限，避免 ENAMETOOLONG
const maxFilenameBytes = 180

// audioExtensions 落盘时需要考虑的音频扩展名（用于查重与命名）
var audioExtensions = []string{"mp3", "flac", "m4a", "aac", "ogg", "wav", "ape", "dsf", "dff"}

func sanitizeFilename(s string) string {
	s = illegalChars.ReplaceAllString(s, "_")
	s = strings.TrimSpace(s)
	s = strings.Trim(s, ". ")
	if s == "" {
		s = "未知"
	}
	if len(s) > maxFilenameBytes {
		// 按字节截断，避免切断多字节 UTF-8 字符
		cut := maxFilenameBytes
		for cut > 0 && !utf8Start(s[cut]) {
			cut--
		}
		s = strings.TrimSpace(s[:cut])
		if s == "" {
			s = "未知"
		}
	}
	return s
}

// utf8Start 判断字节是否为 UTF-8 字符起始字节
func utf8Start(b byte) bool {
	return b&0xC0 != 0x80
}

type SongPayload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Singer    string `json:"singer"`
	Album     string `json:"album"`
	Genre     string `json:"genre"` // 风格。写入内嵌标签供飞牛音乐「风格」页归类
	Cover     string `json:"cover"`
	Source    string `json:"source"`
	Songmid   string `json:"songmid"`
	URL       string `json:"url"`
	StreamURL string `json:"streamUrl"`
	Referer   string `json:"referer"`
	Hash      string `json:"hash"`
	Duration  int    `json:"duration"`
	Interval  int    `json:"interval"`

	// ── 发行信息（2026-09-26 新增）──
	//
	// 直接透传搜索结果的 year/track/disc（网易 publishTime/no/cd、QQ pubtime/cdIdx/belongCD）。
	// 前端是**整个对象展开**着传的（downloadManager.js 里 `{...song}`），所以这三个 JSON
	// 字段名必须与 search.UnifiedSong 保持一致，否则值会在下载落盘时被静默丢掉。
	//
	// 0 = 平台没给：tags 的写入器对 <=0 的字段一律跳过，不会往文件里塞假值。
	Year       int    `json:"year"`
	Track      int    `json:"track"`
	Disc       int    `json:"disc"`
	Quality    string `json:"quality"`     // 前端实际解析到的音质（flac / 320k / 128k ...）
	EmbedLyric bool   `json:"embed_lyric"` // 是否将歌词内嵌进音频文件标签
	WriteLrc   bool   `json:"write_lrc"`   // 是否同时写出同名 .lrc 外挂歌词
	// EmbedCover 是否把封面内嵌进标签。**nil = 是** —— 老调用方与前端都不传这个
	// 字段（前端是 `{...song}` 展开），而「下载下来的歌没封面」是明显的退步，
	// 所以缺省必须是开。要关它的只有曲率的设置项。
	EmbedCover *bool `json:"embed_cover"`
}

// GetStreamReferer determines the appropriate Referer header for music streaming and downloading
func GetStreamReferer(rawURL, customReferer string) string {
	if customReferer != "" {
		return customReferer
	}
	lower := strings.ToLower(rawURL)
	if strings.Contains(lower, "qq.com") {
		return "https://y.qq.com/"
	}
	if strings.Contains(lower, "126.net") || strings.Contains(lower, "163.com") {
		return "https://music.163.com/"
	}
	if strings.Contains(lower, "kuwo.cn") {
		return "http://www.kuwo.cn/"
	}
	if strings.Contains(lower, "kugou.com") {
		return "https://www.kugou.com/"
	}
	if strings.Contains(lower, "migu.cn") {
		return "https://music.migu.cn/"
	}
	return ""
}

type BatchTask struct {
	ID          string        `json:"id"`
	Total       int           `json:"total"`
	Completed   int           `json:"completed"`
	Failed      int           `json:"failed"`
	CurrentSong string        `json:"current_song"`
	Status      string        `json:"status"` // "running", "finished"
	Songs       []SongPayload `json:"-"`
	Results     []SongResult  `json:"results"`
}

type SongResult struct {
	SongName string `json:"song_name"`
	Singer   string `json:"singer"`
	Status   string `json:"status"` // "success", "already_exists", "failed"
	Path     string `json:"path"`
	Quality  string `json:"quality,omitempty"` // 前端**请求**的音质档位（flac24bit / flac / 320k ...）
	// ActualFormat 是按文件真实魔数嗅探出的容器格式（flac / mp3 / ogg / ape / dsf / dff / wav / m4a）。
	//
	// 为什么要单独一个字段：Quality 是「档位」，ActualFormat 是「容器」，两个轴不同
	// （320k 和 128k 都是 mp3）。而**有些音源请求无损时会返回有损直链** ——
	// 文件本身没错（扩展名按魔数给，内容与扩展名一致），但用户以为拿到的是无损。
	// 把真实格式回报出来，调用方才能发现这种「档位与容器不符」。
	ActualFormat string `json:"actual_format,omitempty"`
	Embedded     bool   `json:"embedded,omitempty"` // 是否成功内嵌歌词
	Error        string `json:"error,omitempty"`
}

type Downloader struct {
	cfgMgr     *config.ConfigManager
	httpClient *http.Client
	secOpts    security.Options
	mu         sync.RWMutex
	batchTasks map[string]*BatchTask
}

func NewDownloader(cfgMgr *config.ConfigManager) *Downloader {
	opts := security.DefaultOptions()
	return &Downloader{
		cfgMgr:     cfgMgr,
		httpClient: security.NewSafeClient(opts, 45*time.Second),
		secOpts:    opts,
		batchTasks: make(map[string]*BatchTask),
	}
}

// validateDownloadDir 校验下载目录是否位于允许的 NAS 存储根内。
//
// 注意：目标目录可能尚不存在，而 ResolveSafePath 要求路径已存在（需要解析软链接），
// 因此这里自底向上找到「最近的已存在祖先目录」再交给它校验。
func validateDownloadDir(dir string) error {
	clean := filepath.Clean(strings.TrimSpace(dir))
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("下载目录必须是绝对路径")
	}

	target := clean
	for {
		if fi, err := os.Stat(target); err == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(target)
		if parent == target {
			return fmt.Errorf("无法定位下载目录的可用父目录")
		}
		target = parent
	}

	if _, err := nas.ResolveSafePath(target); err != nil {
		return fmt.Errorf("下载目录必须位于 NAS 存储卷内（如 /vol1/Music）: %v", err)
	}
	return nil
}

func (d *Downloader) getDownloadDir() string {
	cfg := d.cfgMgr.Get()
	dir := cfg.DownloadDir
	if dir == "" {
		dir = cfg.DefaultNasDir
	}
	if dir == "" {
		dir = "/vol1/Music"
	}
	return filepath.Clean(dir)
}

func (d *Downloader) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	cfg := d.cfgMgr.Get()
	dir := d.getDownloadDir()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":            200,
		"download_dir":    dir,
		"default_nas_dir": cfg.DefaultNasDir,
	})
}

func (d *Downloader) HandleSetConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		DownloadDir string `json:"download_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	targetDir := strings.TrimSpace(req.DownloadDir)
	if targetDir == "" {
		http.Error(w, "download_dir required", http.StatusBadRequest)
		return
	}

	// 目录越权防护：只允许写入 NAS 存储卷内的目录
	if err := validateDownloadDir(targetDir); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": err.Error()})
		return
	}
	targetDir = filepath.Clean(targetDir)

	// Try create dir if not exists
	_ = os.MkdirAll(targetDir, 0755)

	cfg := d.cfgMgr.Get()
	cfg.DownloadDir = targetDir
	if err := d.cfgMgr.Update(cfg); err != nil {
		http.Error(w, "failed to update config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":         200,
		"message":      "ok",
		"download_dir": targetDir,
	})
}

func (d *Downloader) HandleDownloadSong(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var song SongPayload
	if err := json.NewDecoder(r.Body).Decode(&song); err != nil {
		http.Error(w, "invalid song payload", http.StatusBadRequest)
		return
	}

	res, err := d.downloadSingleSong(song)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    500,
			"message": err.Error(),
			"result":  res,
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"message": "success",
		"data":    res,
		"result":  res,
	})
}

// maxDownloadBytes 单曲下载体积上限，防止异常大流写满磁盘
const maxDownloadBytes = 300 << 20 // 300MB

// DownloadOne 下载单曲的公开入口，供其它包（如监控调度）复用。
//
// 与 HTTP 处理器不同，它直接返回结果，便于调用方自行记录与重试。
func (d *Downloader) DownloadOne(song SongPayload) (*SongResult, error) {
	return d.downloadSingleSong(song)
}

// GetDownloadDir 返回当前生效的下载目录（供其它包判断文件位置）
func (d *Downloader) GetDownloadDir() string {
	return d.getDownloadDir()
}

func (d *Downloader) downloadSingleSong(song SongPayload) (*SongResult, error) {
	destDir := d.getDownloadDir()
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return &SongResult{
			SongName: song.Name,
			Singer:   song.Singer,
			Status:   "failed",
			Error:    "创建下载目录失败: " + err.Error(),
		}, err
	}

	cleanSinger := sanitizeFilename(song.Singer)
	cleanName := sanitizeFilename(song.Name)
	base := fmt.Sprintf("%s - %s", cleanSinger, cleanName)

	// 已存在（任意受支持扩展名且体积达标）→ 直接复用，避免重复下载
	if existing, ok := findExistingAudio(destDir, base); ok {
		return &SongResult{
			SongName:     song.Name,
			Singer:       song.Singer,
			Status:       "already_exists",
			Path:         filepath.ToSlash(existing),
			Quality:      song.Quality,
			ActualFormat: strings.TrimPrefix(strings.ToLower(filepath.Ext(existing)), "."),
		}, nil
	}

	// Resolve download audio URL
	audioURL := song.URL
	if audioURL == "" || strings.Contains(audioURL, "notice") || strings.Contains(audioURL, "panspace") {
		if song.StreamURL != "" && strings.HasPrefix(song.StreamURL, "http") {
			audioURL = song.StreamURL
		}
	}

	if audioURL == "" || strings.Contains(audioURL, "notice") || strings.Contains(audioURL, "panspace") {
		return &SongResult{
			SongName: song.Name,
			Singer:   song.Singer,
			Status:   "failed",
			Error:    "未提供有效音频流地址，请先通过第三方音源解析后再发起下载",
		}, fmt.Errorf("未提供有效音频流地址")
	}

	// SSRF 防护：拒绝内网 / 回环 / 链路本地 / 云元数据地址
	if _, err := security.ValidateURL(audioURL, d.secOpts); err != nil {
		return &SongResult{
			SongName: song.Name,
			Singer:   song.Singer,
			Status:   "failed",
			Error:    "拒绝下载该地址: " + err.Error(),
		}, fmt.Errorf("拒绝下载该地址: %w", err)
	}

	// ── 下载到临时文件（支持断点续传） ──
	//
	// 临时文件命名基于「目标基名」，而不是随机名：这样同一个任务重试时
	// 能找回上次的 .part 继续下载，避免大文件每次断线都从头再来。
	tmpPath := partialPath(destDir, base, song)
	if err := os.MkdirAll(filepath.Dir(tmpPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建下载目录失败: %w", err)
	}

	have := int64(0)
	if fi, statErr := os.Stat(tmpPath); statErr == nil && fi.Size() > 0 {
		have = fi.Size()
	}

	req, err := http.NewRequest("GET", audioURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	if ref := GetStreamReferer(audioURL, song.Referer); ref != "" {
		req.Header.Set("Referer", ref)
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		// 网络中断：保留 .part 供下次续传
		return nil, fmt.Errorf("下载请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 服务端不支持 Range（返回 200 而非 206）时，从头重下
	if have > 0 && resp.StatusCode == http.StatusOK {
		have = 0
		_ = os.Remove(tmpPath)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP 错误: %d", resp.StatusCode)
	}

	// 预检 Content-Length：明显超限就直接拒绝，避免白下载一遍
	total := resp.ContentLength
	if resp.StatusCode == http.StatusPartialContent {
		// 206 的 Content-Length 是本次剩余部分，需要加上已下载的字节
		total += have
	}
	if total > maxDownloadBytes {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("对端声明的文件过大（%d MB），超过上限 %d MB",
			total>>20, maxDownloadBytes>>20)
	}

	// 先探读开头若干字节，用于识别「根本不是音频」的响应。
	// 防盗链 / 需登录 / 被限流时，CDN 常返回 200 + 一段 HTML 或 JSON；
	// 不校验就会把错误页存成 .mp3 并计为「下载成功」，
	// 用户看到的现象是「下载成功但播不了 / 时长不对」。
	//
	// 仅在第 0 字节开始时才检查：续传时开头是上次已写好的内容，
	// 本次响应的开头是文件的中间片段，魔数检查不适用。
	// head 仅在「从第 0 字节开始下载」时用于魔数校验。
	// 续传时响应体的开头是文件的中间片段，既不能用于魔数校验，
	// 也不能被写进文件（否则会插入一段垃圾数据）。
	var head []byte
	if have == 0 {
		buf := make([]byte, 512)
		n, readErr := io.ReadFull(resp.Body, buf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("读取响应失败: %w", readErr)
		}
		head = buf[:n]

		if isErrorPayload(head, resp.Header.Get("Content-Type")) {
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("对端返回的不是音频（可能需登录、被限流或链接已失效）")
		}

		// 魔数是唯一权威依据：认不出来就拒绝。
		// 不再回退到「按 URL 扩展名猜」——HTML 错误页正是从那个兜底溜进来的。
		if ext := detectAudioExtByMagic(head); ext == "" {
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("对端返回的内容不是可识别的音频格式")
		}
	}

	flags := os.O_CREATE | os.O_WRONLY
	if have > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	tmp, err := os.OpenFile(tmpPath, flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开临时文件失败: %w", err)
	}

	discardTmp := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	// 本轮从响应体新写入的字节数。
	//
	// 注意：head 是「为了做魔数校验而从 resp.Body 预读出来的部分」，
	// 它不是额外的数据，必须算进 reply 的写入量，否则会重复计入导致
	// 续传后文件比原文件大。
	var written int64
	body := io.Reader(resp.Body)
	if len(head) > 0 {
		body = io.MultiReader(bytes.NewReader(head), resp.Body)
	}

	// 多读 1 字节以便识别「超过上限被截断」
	written, copyErr := io.Copy(tmp, io.LimitReader(body, maxDownloadBytes+1))
	if copyErr != nil {
		// 对端提前断流（unexpected EOF）不算写入失败：
		// 已写入的数据要保留下来供下次 Range 续传，因此这里不删 .part。
		if !isTruncatedBody(copyErr) {
			discardTmp()
			return nil, fmt.Errorf("写入文件失败: %w", copyErr)
		}
	}

	totalWritten := have + written
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("关闭临时文件失败: %w", err)
	}

	if totalWritten > maxDownloadBytes {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("下载文件超过体积上限 (%d MB)", maxDownloadBytes>>20)
	}

	// 完全不完整（没拿到任何新数据）说明请求本身有问题，清掉断点重来
	if written == 0 {
		_ = os.Remove(tmpPath)
		if copyErr != nil {
			return nil, fmt.Errorf("下载失败: %w", copyErr)
		}
		return nil, fmt.Errorf("对端未返回任何数据")
	}

	// 对端提前断流时 io.Copy 会提前结束。旧实现当作下载完成，
	// 把截断文件改名入库，表现为「下载成功但播不了 / 时长不对」。
	// 这里保留 .part 供下次按 Range 续传，而不是直接丢弃。
	if total > 0 && totalWritten < total {
		return nil, fmt.Errorf("下载不完整（%d/%d 字节），已保留断点，下次将续传",
			totalWritten, total)
	}
	if copyErr != nil {
		return nil, fmt.Errorf("下载中断（%d 字节），已保留断点，下次将续传", totalWritten)
	}

	if totalWritten < 300*1024 {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("下载文件过小 (可能为提示音频或无效流)")
	}

	// 依据文件真实魔数确定最终扩展名（此时 tmpPath 可能不含其实我们已知的 head）
	ext := detectAudioExtByMagic(head)
	if ext == "" {
		// 续传场景下没有 head，从文件开头重新嗅探
		ext = sniffExtFromFile(tmpPath)
	}
	if ext == "" {
		ext = "mp3"
	}
	finalPath := allocateUniquePath(destDir, base, ext)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("保存最终文件失败: %w", err)
	}

	// 下载成功 → 使所在目录的扫描缓存失效，下次 NAS 页打开时自动重扫拿到新文件。
	nas.InvalidateScanCacheFile(finalPath)

	// 通知飞牛音乐重扫曲库 —— 它不会自己发现新文件，不通知的话
	// 用户「下载完了但飞牛里看不到」。调度器会合并+节流，这里是立即返回的。
	fnos.ScheduleLibraryRescan()

	// 元数据 / 封面 / 歌词写入（best-effort，失败不影响下载结果）
	embedded := d.applyTags(finalPath, song)

	return &SongResult{
		SongName:     song.Name,
		Singer:       song.Singer,
		Status:       "success",
		Path:         filepath.ToSlash(finalPath),
		Quality:      song.Quality,
		ActualFormat: ext,
		Embedded:     embedded,
	}, nil
}

// CoverWanted 报告要不要内嵌封面（缺省要，见 EmbedCover 字段的说明）。
func (s SongPayload) CoverWanted() bool { return s.EmbedCover == nil || *s.EmbedCover }

// coverForTags 决定要为这首歌抓哪个封面（空 = 不抓）。
//
// 单独抽出来是为了**可测**：判断逻辑埋在 applyTags 里的话，只能靠造一个真音频
// 文件去间接覆盖它，那条路很容易就没人维护了。
func coverForTags(song SongPayload) string {
	if !song.CoverWanted() {
		return ""
	}
	return strings.TrimSpace(song.Cover)
}

// applyTags 写入标题/歌手/专辑/风格、封面与歌词，返回是否成功内嵌歌词
func (d *Downloader) applyTags(path string, song SongPayload) bool {
	lrcText := ""
	if song.EmbedLyric || song.WriteLrc {
		lrcText = d.fetchLyricText(song)
	}

	// 外挂同名 .lrc（UTF-8 无 BOM）
	wroteSidecar := false
	if song.WriteLrc && strings.TrimSpace(lrcText) != "" {
		lrcPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".lrc"
		if err := os.WriteFile(lrcPath, []byte(lrcText), 0644); err == nil {
			wroteSidecar = true
		}
	}

	meta := tags.Metadata{
		Title:  strings.TrimSpace(song.Name),
		Artist: strings.TrimSpace(song.Singer),
		Album:  strings.TrimSpace(song.Album),
		Genre:  strings.TrimSpace(song.Genre),

		// 发行年份 / 曲序 / 光盘序号。以前这里**只有上面四项**，所以下载落盘的歌
		// 在飞牛里年份/曲序/光盘永远是空的（2026-09-26 实测：库里 41 首测试曲目
		// 41 首都缺），只能事后跑 /api/library/complete 补或手工编辑。
		// 值来自搜索响应，不经 AI 推断；<=0 由 tags 的写入器自动跳过。
		Year:  song.Year,
		Track: song.Track,
		Disc:  song.Disc,
	}
	if song.EmbedLyric {
		meta.Lyric = lrcText
	}
	if u := coverForTags(song); u != "" {
		data, mime, cerr := d.fetchCoverBytes(u)
		if cerr != nil {
			// 以前这里是**静默跳过**：封面抓不到就什么都不写，用户只看到「飞牛里没封面」。
			// 现在留一条日志（可在「日志」页看到），并**只带域名** ——
			// 很多 CDN 的封面链接带签名参数，整条打进日志不合适。
			applog.Default().Add(fmt.Sprintf("[下载] 封面抓取失败，将不内嵌封面：%s - %s（%s：%v）",
				song.Name, song.Singer, coverHost(u), cerr))
		} else {
			meta.Cover = data
			meta.CoverMime = mime
		}
	}

	embedded := false
	if !meta.IsEmpty() {
		if _, err := tags.Write(path, meta); err == nil {
			embedded = true
		} else if !errors.Is(err, tags.ErrUnsupportedFormat) {
			// 「容器不支持内嵌标签」是已知限制（ogg/ape/wav/m4a…），不必每首都刷一条；
			// 但其它写失败（权限、只读、磁盘）以前也是静默的，得留痕。
			applog.Default().Add(fmt.Sprintf("[下载] 写内嵌标签失败：%s - %s（%v）",
				song.Name, song.Singer, err))
		}
	}

	// 只要动过文件（内嵌标签或外挂歌词），就把音频与附属文件的时间戳推到当下 ——
	// 飞牛按「路径 + mtime/size」做增量指纹，不推它就不会重新读取
	// （表现是「改了歌词飞牛里还是旧的」，见 pkg/tags/rescan.go）。
	// 注意：只写 .lrc 时音频内容没变，这里正是唯一能让飞牛察觉的地方。
	if embedded || wroteSidecar {
		tags.TouchForRescan(path)
	}

	return strings.TrimSpace(meta.Lyric) != ""
}

// fetchLyricText 通过服务端歌词聚合能力获取 LRC 文本
func (d *Downloader) fetchLyricText(song SongPayload) string {
	source := strings.ToLower(strings.TrimSpace(song.Source))
	songmid := strings.TrimSpace(song.Songmid)
	if songmid == "" {
		songmid = strings.TrimSpace(song.ID)
	}
	dur := song.Duration
	if dur <= 0 {
		dur = song.Interval
	}
	lrc, _ := search.FetchLyric(source, songmid, song.Name, song.Singer, dur, song.Hash)
	return lrc
}

// fetchCoverBytes 下载并校验封面图片，返回原始字节与 MIME。
//
// 第三个返回值是**失败原因**（成功时为 nil）。
// ⚠️ 以前失败一律静默返回 `nil, ""`：用户只看到「飞牛里没封面」，
// 分不清是「这首本来就没图」还是「抓取失败了」（2026-09-22 反馈）。
// 调用方会把原因写进「日志」页，排查时不用再猜。
func (d *Downloader) fetchCoverBytes(coverURL string) ([]byte, string, error) {
	coverURL = strings.TrimSpace(coverURL)
	if !strings.HasPrefix(coverURL, "http") {
		return nil, "", fmt.Errorf("封面地址不是 http(s)")
	}
	if _, err := security.ValidateURL(coverURL, d.secOpts); err != nil {
		return nil, "", fmt.Errorf("封面地址被安全校验拒绝：%v", err)
	}
	req, err := http.NewRequest("GET", coverURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("构造封面请求失败：%v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://music.163.com/")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("封面下载失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("封面下载返回 HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, "", fmt.Errorf("读取封面数据失败：%v", err)
	}
	if len(data) < 100 {
		return nil, "", fmt.Errorf("封面数据过小（%d 字节），疑似不是图片", len(data))
	}
	if len(data) > 12<<20 {
		return nil, "", fmt.Errorf("封面数据超过 12MB")
	}
	mime := detectImageMime(data)
	if mime == "" {
		return nil, "", fmt.Errorf("封面不是图片，或格式不在支持范围内（jpeg/png/gif/webp）")
	}
	return data, mime, nil
}

// coverHost 取封面链接的域名。日志里**不整条打链接** ——
// 很多 CDN 的封面地址带签名参数，属于敏感信息。
func coverHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "未知来源"
	}
	return u.Host
}

// detectImageMime 通过魔数识别图片类型，非图片返回空串。
//
// ⚠️ webp 必须认：`security/image.go` 的 FetchImage 是认 webp 的（补全那条链在用），
// 这里以前只认 jpeg/png/gif —— 同一张封面「补全能写、下载写不进去」，
// 用户看到的就是「封面时有时无」。
func detectImageMime(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) >= 6 && strings.HasPrefix(string(data), "GIF8"):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// isTruncatedBody 判断错误是否为「对端提前断流」。
// 这类错误应保留已下载数据供续传，而不是当作本地写入失败。
func isTruncatedBody(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe")
}

// partialPath 生成断点续传用的临时文件路径。
//
// 命名基于目标基名而非随机名，这样同一任务重试时能找回上次的 .part
// 继续下载，而不是从头再来。
func partialPath(destDir, base string, song SongPayload) string {
	// 用平台 + 曲目 ID 参与命名，避免同名不同曲互相污染断点
	tag := strings.TrimSpace(song.Source + "_" + song.Songmid)
	if tag == "_" || tag == "" {
		tag = strings.TrimSpace(song.ID)
	}
	tag = sanitizeFilename(tag)
	if tag == "未知" {
		tag = "default"
	}
	if len(tag) > 40 {
		tag = tag[:40]
	}
	// 目标目录可能与下载目录不同（allocateUniquePath 会落到 destDir），统一放 destDir
	return filepath.Join(destDir, fmt.Sprintf(".%s.%s.part", base, tag))
}

// sniffExtFromFile 从已落盘文件的开头重新嗅探容器格式
func sniffExtFromFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	if n <= 0 {
		return ""
	}
	return detectAudioExtByMagic(head[:n])
}

// detectAudioExtByMagic 仅依据文件头魔数判断音频容器，认不出则返回空串。
// 刻意不做「按 URL 扩展名 / 音质猜」的兜底：那种兜底会让防盗链返回的
// HTML 错误页被当成 .mp3 存下来，并计为「下载成功」。
func detectAudioExtByMagic(head []byte) string {
	if len(head) < 4 {
		return ""
	}
	switch {
	case bytes.HasPrefix(head, []byte("fLaC")):
		return "flac"
	case bytes.HasPrefix(head, []byte("ID3")):
		return "mp3"
	// MPEG 帧同步字：11 位全 1
	case head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return "mp3"
	case bytes.HasPrefix(head, []byte("OggS")):
		return "ogg"
	case bytes.HasPrefix(head, []byte("MAC ")):
		return "ape"
	case bytes.HasPrefix(head, []byte("DSD ")):
		return "dsf"
	case bytes.HasPrefix(head, []byte("FRM8")):
		return "dff"
	case len(head) >= 12 && bytes.HasPrefix(head, []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WAVE")):
		return "wav"
	case len(head) >= 8 && bytes.Equal(head[4:8], []byte("ftyp")):
		return "m4a"
	}
	return ""
}

// isErrorPayload 判断响应体是否明显不是音频（HTML / JSON / XML 错误页）。
//
// 刻意不使用「可打印字符占比」这类启发式：带大段文本标签的合法 MP3
// 很容易被它误杀。
func isErrorPayload(head []byte, contentType string) bool {
	ct := strings.ToLower(contentType)
	for _, bad := range []string{
		"text/html", "text/plain", "text/xml", "application/xml", "application/json",
	} {
		if strings.Contains(ct, bad) {
			return true
		}
	}

	trimmed := bytes.TrimLeft(head, " \t\r\n\xef\xbb\xbf")
	for _, prefix := range [][]byte{
		[]byte("<html"), []byte("<!DOCTYPE"), []byte("<!doctype"),
		[]byte("<?xml"), []byte("{"), []byte("["),
	} {
		if bytes.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// findExistingAudio 查找同名且体积达标的既有音频文件
func findExistingAudio(destDir, base string) (string, bool) {
	for _, ext := range audioExtensions {
		p := filepath.Join(destDir, base+"."+ext)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 500*1024 {
			return p, true
		}
	}
	return "", false
}

// allocateUniquePath 生成不覆盖既有文件的路径（同名依次追加 " (2)"、" (3)" ...）
func allocateUniquePath(destDir, base, ext string) string {
	first := filepath.Join(destDir, base+"."+ext)
	if !fileExists(first) {
		return first
	}
	for i := 2; i < 1000; i++ {
		cand := filepath.Join(destDir, fmt.Sprintf("%s (%d).%s", base, i, ext))
		if !fileExists(cand) {
			return cand
		}
	}
	return filepath.Join(destDir, fmt.Sprintf("%s (%d).%s", base, time.Now().UnixNano(), ext))
}

func (d *Downloader) HandleCheckDownloaded(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Songs []SongPayload `json:"songs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	destDir := d.getDownloadDir()
	existsMap := make(map[string]bool)

	for _, s := range req.Songs {
		cleanSinger := sanitizeFilename(s.Singer)
		cleanName := sanitizeFilename(s.Name)
		filename := fmt.Sprintf("%s - %s.mp3", cleanSinger, cleanName)
		fullPath := filepath.Join(destDir, filename)
		key := fmt.Sprintf("%s - %s", s.Singer, s.Name)
		if fi, err := os.Stat(fullPath); err == nil && fi.Size() > 300*1024 {
			existsMap[key] = true
			if s.ID != "" {
				existsMap[s.ID] = true
			}
		} else {
			existsMap[key] = false
			if s.ID != "" {
				existsMap[s.ID] = false
			}
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":   200,
		"exists": existsMap,
	})
}

func (d *Downloader) HandleDownloadBatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Songs []SongPayload `json:"songs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Songs) == 0 {
		http.Error(w, "empty songs list", http.StatusBadRequest)
		return
	}

	taskID := fmt.Sprintf("task_%d", time.Now().UnixNano())
	task := &BatchTask{
		ID:        taskID,
		Total:     len(req.Songs),
		Completed: 0,
		Failed:    0,
		Status:    "running",
		Songs:     req.Songs,
		Results:   make([]SongResult, 0, len(req.Songs)),
	}

	d.mu.Lock()
	d.batchTasks[taskID] = task
	d.mu.Unlock()

	go d.processBatchTask(task)

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"task_id": taskID,
		"message": "Batch download started",
		"total":   len(req.Songs),
	})
}

func (d *Downloader) processBatchTask(task *BatchTask) {
	sem := make(chan struct{}, 3) // Concurrency 3
	var wg sync.WaitGroup

	for _, song := range task.Songs {
		sem <- struct{}{}
		wg.Add(1)

		d.mu.Lock()
		task.CurrentSong = fmt.Sprintf("%s - %s", song.Singer, song.Name)
		d.mu.Unlock()

		go func(s SongPayload) {
			defer func() {
				<-sem
				wg.Done()
			}()

			res, err := d.downloadSingleSong(s)
			d.mu.Lock()
			defer d.mu.Unlock()
			if err != nil {
				task.Failed++
				if res != nil {
					task.Results = append(task.Results, *res)
				}
			} else {
				task.Completed++
				if res != nil {
					task.Results = append(task.Results, *res)
				}
			}
		}(song)
	}

	wg.Wait()

	d.mu.Lock()
	task.Status = "finished"
	task.CurrentSong = "全部下载完成"
	d.mu.Unlock()
}

func (d *Downloader) HandleBatchStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		http.Error(w, "task_id required", http.StatusBadRequest)
		return
	}

	d.mu.RLock()
	task, exists := d.batchTasks[taskID]
	d.mu.RUnlock()

	if !exists {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200,
		"data": task,
	})
}

// EnrichExisting 给**已经存在的**音频文件补标签（标题/歌手/专辑/封面/歌词），
// 不重新下载音频。
//
// 这是「边听边下」需要的：tee 直接复制流字节，落盘的文件**没有任何标签** ——
// 歌能听、能绑定，但在飞牛里元数据是空的。重新下载一遍会把 tee 省下的带宽又花回去，
// 所以这里只补标签。
//
// 返回是否成功内嵌歌词。
func (d *Downloader) EnrichExisting(song SongPayload, path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, errors.New("empty path")
	}
	// ⚠️ 这个接口会**写盘**，而路径来自调用方 —— 只允许改下载目录里的文件。
	// 不做这个检查的话它就是一个「往任意路径写标签」的入口。
	dir, err := filepath.Abs(d.getDownloadDir())
	if err != nil {
		return false, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, fmt.Errorf("path outside download dir: %s", path)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return false, err
	}
	if fi.IsDir() || fi.Size() == 0 {
		return false, fmt.Errorf("not an audio file: %s", path)
	}
	return d.applyTags(abs, song), nil
}

// HandleEnrichExisting 是 POST /api/download/enrich 的处理器：给已落盘的文件补标签。
func (d *Downloader) HandleEnrichExisting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SongPayload
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	embedded, err := d.EnrichExisting(req.SongPayload, req.Path)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 500, "message": err.Error(), "data": nil,
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200, "message": "success",
		"data": map[string]interface{}{"path": req.Path, "embedded": embedded},
	})
}
