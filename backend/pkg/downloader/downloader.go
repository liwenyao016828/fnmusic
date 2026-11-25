package downloader

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/audiok"
	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/filename"
	"fn-lx-player/pkg/flacok"
	"fn-lx-player/pkg/fnos"
	"fn-lx-player/pkg/fslock"
	"fn-lx-player/pkg/library"
	"fn-lx-player/pkg/namefmt"
	"fn-lx-player/pkg/nas"
	"fn-lx-player/pkg/search"
	"fn-lx-player/pkg/security"
	"fn-lx-player/pkg/tags"
	"fn-lx-player/pkg/useragent"
)

// maxFilenameBytes 与 pkg/filename 的上限保持一致（本包测试要引用这个常量）。
const maxFilenameBytes = 180

// audioExtensions 落盘时需要考虑的音频扩展名（用于查重与命名）
var audioExtensions = []string{"mp3", "flac", "m4a", "aac", "ogg", "wav", "ape", "dsf", "dff"}

// sanitizeFilename 是 pkg/filename.Sanitize 在本包的薄封装。
//
// 规则本身（非法字符 / 非法 UTF-8 / 控制字符 / Windows 保留名 / 按字符边界截断）
// 全在 filename 包里，因为库体检也要用同一套规则算出「这个名字该改成什么」——
// 两处各写一份必然漂移（2026-10-24 抽取）。
func sanitizeFilename(s string) string { return filename.Sanitize(s) }

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
	// UpgradeIfBetter 允许「用更高音质覆盖已有的低音质文件」。
	//
	// 默认**关**：`/api/download/song` 是开放接口，外部调用方传同一首歌时
	// 「已存在就复用」是它们预期的行为（重下一遍几十兆很浪费）。只有曲率自己的
	// 自动下载/注入页下载会打开它——因为那两条路的语义就是「按最高音质入库」。
	//
	// 打开后：命中已存在但**容器有损**（mp3/m4a/aac/ogg）而本次想要无损时，
	// 不再直接返回 already_exists，而是照常下载，成功后把旧的有损文件删掉。
	// 反过来（已有无损、本次 320k）永远不降级。
	UpgradeIfBetter bool `json:"upgrade_if_better,omitempty"`
	// DestDir / DestBase 指定**落盘位置**，给「替换音质」用。
	//
	// 为什么需要：替换的对象是用户盘上那份文件，而候选曲目的歌名常常与它不一样
	// （真机：本地叫《喜欢你》，咪咕给的叫《喜欢你 (电影《澳门风云3》插曲)》）。
	// 按候选名落盘就会在曲库根目录多出一份新文件、旧的 128k 还躺在原地 ——
	// 2026-10-21 真机实测就是这么「换」的。带上这两个字段后，下载器在**原目录、
	// 原文件名**上做覆盖（扩展名变了则先落新的、再删旧的）。
	//
	// 只有曲率自己的替换流程会传；外部调用方不传，行为不变。
	DestDir  string `json:"dest_dir,omitempty"`
	DestBase string `json:"dest_base,omitempty"`

	// ForceReplace = 「不管盘上那份是什么，都换成这次下到的」。给**歌曲修复**用。
	//
	// 为什么不能复用 UpgradeIfBetter：那条的判据是「本次档位严格更高」，而歌曲修复
	// 要换的是**坏文件**（扩展名写 flac 其实是 mp3、时长只有几秒、无损小得离谱）。
	// 坏文件的档位常常与候选一样（都是 320k），按档位比就永远换不掉 —— 用户点了
	// 「换这个版本」却什么都没发生。
	//
	// 打开后跳过两处判断：① shouldUpgradeOver 的档位比较；② findExistingAudio 的
	// 体积门槛（见 libraryFileIsAudio：<32KB 一律不算音频，而坏文件正好都在这一档，
	// 于是「找不到旧文件」→ 走 allocateUniquePath → 在旁边多出一份 `xxx (2).mp3`，
	// 坏的还躺在原地。v2.35.0 真机实测就是这个形状）。
	//
	// ⚠️ 只在调用方**明确指定了目标文件**（DestDir/DestBase）时才有意义；
	// 外部调用方不传，行为完全不变。
	ForceReplace bool `json:"force_replace,omitempty"`
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
	// ErrorCode 是给调用方**机器判别**用的失败分类（Error 是给人看的）。
	//
	// 目前只有一种："flac_corrupt" —— 下载到的 FLAC 是坏流（帧 CRC 对不上）。
	// 它值得单列，是因为这种失败可以**自动换档重下**（降到 320k），
	// 而不是把错误抛给用户。前端看到这个码就自动降档重试。
	ErrorCode string `json:"error_code,omitempty"`
}

type Downloader struct {
	cfgMgr     *config.ConfigManager
	httpClient *http.Client
	secOpts    security.Options
	mu         sync.RWMutex
	batchTasks map[string]*BatchTask

	// libIndex 是全库索引快照的读取函数（见 library_index.go / SetLibraryIndex）。
	// 单独一把锁：批量任务的状态读写很频繁，别让它们跟索引快照互相等。
	libMu    sync.RWMutex
	libIndex func() []library.Entry
}

func NewDownloader(cfgMgr *config.ConfigManager) *Downloader {
	opts := security.DefaultOptions()
	return &Downloader{
		cfgMgr: cfgMgr,
		// 下载是「长时间读一个响应体」的场景：超时必须按**整条响应体**算，
		// 不是按「等响应头」算。原来用 45s 的 NewSafeClient，http.Client.Timeout
		// 会把整个下载掐在 45 秒内 → 大文件必挂（实测「写入文件失败: context canceled」）。
		// 这里给 10 分钟，并保留代理尝试上限（代理不通仍 5 秒掉头直连）。
		httpClient: security.NewSafeStreamClient(opts, 10*time.Minute),
		secOpts:    opts,
		batchTasks: make(map[string]*BatchTask),
	}
}

// validateDownloadDir 校验下载目录是否位于允许的 NAS 存储根内。
//
// 注意：目标目录可能尚不存在，而 ResolveSafePath 要求路径已存在（需要解析软链接），
// 因此这里自底向上找到「最近的已存在祖先目录」再交给它校验。
func validateDownloadDir(dir string) error {
	return ValidateNasDir(dir, "下载目录")
}

// validateNasDir 是目录类设置的共同校验：绝对路径 + 落在 NAS 存储卷内。
//
// label 只影响报错文案（「下载目录」/「缓存目录」）—— 用户在设置页里同时能看到
// 两个输入框，报错不说是哪一个等于让他自己猜。
func ValidateNasDir(dir, label string) error {
	clean := filepath.Clean(strings.TrimSpace(dir))
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("%s必须是绝对路径", label)
	}

	target := clean
	for {
		if fi, err := os.Stat(target); err == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(target)
		if parent == target {
			return fmt.Errorf("无法定位%s的可用父目录", label)
		}
		target = parent
	}

	if _, err := nas.ResolveSafePath(target); err != nil {
		return fmt.Errorf("%s必须位于已授权的音乐目录内: %v", label, err)
	}
	return nil
}

// getDownloadDir 返回生效的落盘目录。
//
// 2026-10-31：兜底逻辑收敛到 config.AppConfig.LibraryDir()，不再在这里
// 各写一份 —— 那份写死 "/vol1/Music" 的兜底会让「用户还没授权任何目录」时
// 下载任务直接往一个无权访问的路径写。空串 = 还没授权，调用方必须据此报错/引导。
func (d *Downloader) getDownloadDir() string {
	cfg := d.cfgMgr.Get()
	return cfg.LibraryDir() // Get 返回的是值拷贝，取地址后再调指针方法
}

func (d *Downloader) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	cfg := d.cfgMgr.Get()
	dir := d.getDownloadDir()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":         200,
		"download_dir": dir,
		// 空串 = 用程序推导的默认位置（暂存在下载目录下，试听缓存在数据目录下）。
		// 界面要显示的就是这个「已设置 / 未设置」的区别。
		"cache_dir":       cfg.CacheDirPath(),
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
		// CacheDir 是**指针**，因为要区分三件事（与 AppConfig.CacheDir 同一套三态）：
		//   没传这个键 → 不改（旧前端只传 download_dir，必须继续能用）
		//   传了空串   → 显式回到默认位置（界面的「恢复默认」）
		//   传了路径   → 用它
		CacheDir *string `json:"cache_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	targetDir := strings.TrimSpace(req.DownloadDir)
	if targetDir == "" && req.CacheDir == nil {
		http.Error(w, "download_dir required", http.StatusBadRequest)
		return
	}

	cfg := d.cfgMgr.Get()

	if targetDir != "" {
		// 目录越权防护：只允许写入 NAS 存储卷内的目录
		if err := ValidateNasDir(targetDir, "下载目录"); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": err.Error()})
			return
		}
		targetDir = filepath.Clean(targetDir)
		// Try create dir if not exists
		_ = os.MkdirAll(targetDir, 0755)
		cfg.DownloadDir = targetDir
	}

	if req.CacheDir != nil {
		cacheDir := strings.TrimSpace(*req.CacheDir)
		if cacheDir != "" {
			if err := ValidateNasDir(cacheDir, "缓存目录"); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": err.Error()})
				return
			}
			cacheDir = filepath.Clean(cacheDir)
			// 缓存目录是**真要写字节**的地方，建不出来现在就报，别等第一次试听
			// 才发现写不进去（那时用户只会看到「缓存失败」而猜不到是权限问题）。
			if err := os.MkdirAll(cacheDir, 0755); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code":    400,
					"message": "缓存目录无法创建（检查权限）: " + err.Error(),
				})
				return
			}
		}
		cfg.CacheDir = &cacheDir
	}

	// 校验要用**合并后**的这一对：界面可能只提交了其中一个，
	// 只看请求体会把「新下载目录撞上已有缓存目录」这种漏掉。
	if msg := cfg.ValidateDirs(); msg != "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 400, "message": msg})
		return
	}

	if err := d.cfgMgr.Update(cfg); err != nil {
		http.Error(w, "failed to update config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":         200,
		"message":      "ok",
		"download_dir": d.getDownloadDir(),
		"cache_dir":    cfg.CacheDirPath(),
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

// ErrCorruptLossless 标记「下载到的无损文件是坏流」这类失败（FLAC 及其它容器）。
//
// 上层（pkg/backenddl）看到它就知道：换条同档直链重试没意义（同一首的无损
// 直链本身是坏的），但**换档**有救 —— 320K 是另一条编码链路，通常完好。
var ErrCorruptLossless = errors.New("下载到的无损文件是坏流")

// ErrCorruptFLAC 标记「下载到的 FLAC 是坏流」这类失败。
//
// 单独留一个哨兵错误，是因为调用方需要能认出它并**自动换档重下**
// （降到 320k），而不是把错误原文抛给用户。配套的机器可读字段是
// SongResult.ErrorCode = "flac_corrupt"。
var ErrCorruptFLAC = errors.New("下载到的无损文件是坏流")

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

// ErrNoDownloadDir 表示「还没被授权任何目录」—— getDownloadDir 现在会回空串。
//
// 这不是内部错误，是用户可见的状态：界面要提示去「应用设置 → 授权目录」授权，
// 而不是把空路径当成「写到根目录」或凭空造一个 /vol1/Music。
var ErrNoDownloadDir = errors.New("还没有被授权访问任何目录，请先在「应用设置 → 授权目录」里授权一个音乐目录")

// downloadSingleSong 是落盘主流程的**带日志外壳**：真正干活的是 downloadInner。
//
// 为什么要包一层而不是在内部每个 return 前手动打日志：这条流程有十几条退出路径
// （无目录 / 目录建不出来 / 地址无效 / SSRF 拒绝 / HTTP 非 2xx / 体积超限 / 嗅探
// 不是音频 / 无损校验不过 / rename 失败…），逐条手打必漏；漏掉的那条恰好就是
// 用户来问的那条。外壳只在一个地方收口，起手记「要下什么」，收手记「结果如何」。
func (d *Downloader) downloadSingleSong(song SongPayload) (*SongResult, error) {
	startedAt := time.Now()
	// 曲库闸门的**共享**一侧：多个下载可以并行，但整理（排他）期间要等它收工，
	// 避免「整理刚确认目标名是空的，下载正好在这一刻写进去」这种撞车（审计 M8）。
	releaseLib := fslock.Shared()
	defer releaseLib()
	// 直链只记域名：音源直链带 token/签名，整条写进日志等于把下载凭据落到 qulv.log
	//（2026-11-01 用户口径）。域名够用来分辨「是哪家 CDN 在 403」。
	url := song.URL
	if url == "" || strings.Contains(url, "notice") || strings.Contains(url, "panspace") {
		url = song.StreamURL
	}
	log.Printf("[DOWNLOAD] 开始：%s - %s（目标档位 %s，来源 %s，域名 %s）",
		song.Singer, song.Name, dashIfEmpty(song.Quality), dashIfEmpty(song.Source), applog.HostOf(url))

	res, err := d.downloadInner(song)

	elapsed := time.Since(startedAt)
	switch {
	case err != nil:
		// 失败必须带原因：err 里的人话已经写清是卡在哪一步（见 downloadInner 的
		// 各处 fmt.Errorf），这里补上曲目身份与耗时，便于「是这首还是整批都慢」。
		log.Printf("[DOWNLOAD] 失败：%s - %s（耗时 %s）：%v", song.Singer, song.Name, briefElapsed(elapsed), err)
	case res != nil && res.Status == "success":
		// 成功日志要能回答三件事：文件落在哪、实际是什么档位、多大。
		// 「下到的怎么是 mp3」「文件到底在不在」都靠这一行，不再需要翻盘。
		size := int64(0)
		if fi, statErr := os.Stat(res.Path); statErr == nil {
			size = fi.Size()
		}
		log.Printf("[DOWNLOAD] 完成：%s - %s → %s（实际档位 %s，格式 %s，%s，来源 %s，域名 %s，耗时 %s）",
			song.Singer, song.Name, res.Path, dashIfEmpty(res.Quality), dashIfEmpty(res.ActualFormat),
			briefBytes(size), dashIfEmpty(song.Source), applog.HostOf(url), briefElapsed(elapsed))
	case res != nil && res.Status == "already_exists":
		// 复用已有文件是「成功但没干活」，必须与真下载区分开：否则用户看到
		// 「下载很快完成」会以为是网络变好了，其实是文件早就在盘上。
		log.Printf("[DOWNLOAD] 已存在，跳过下载：%s - %s → %s（%s，耗时 %s）",
			song.Singer, song.Name, res.Path, dashIfEmpty(res.ActualFormat), briefElapsed(elapsed))
	}
	return res, err
}

// dashIfEmpty 日志里的空值统一显示「未指定」。
func dashIfEmpty(v string) string {
	if strings.TrimSpace(v) == "" {
		return "未指定"
	}
	return v
}

// briefElapsed 把耗时压成「12s」「1m23s」。
func briefElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// briefBytes 把字节数写成日志易读形式。
func briefBytes(n int64) string {
	switch {
	case n <= 0:
		return "大小未知"
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%dKB", n/1024)
	}
}

// downloadInner 是实际落盘流程（见上面的 downloadSingleSong 注释）。
func (d *Downloader) downloadInner(song SongPayload) (*SongResult, error) {
	destDir := d.getDownloadDir()
	if destDir == "" {
		return &SongResult{
			SongName: song.Name,
			Singer:   song.Singer,
			Status:   "failed",
			Error:    ErrNoDownloadDir.Error(),
		}, ErrNoDownloadDir
	}
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
	// 文件名方向由 pkg/namefmt 统一决定（默认「歌手 - 歌名」，可配成「歌名 - 歌手」）。
	// 千万不要在这里自己拼分隔符：读取侧（扫描/去重/找歌词）按同一份约定反解，
	// 两边写死两份必然有一天读反（不报错，只是歌手歌名对调）。
	base := namefmt.Build(cleanSinger, cleanName)
	// 替换音质：落到调用方指定的目录与文件名（就是盘上那份文件本身）。
	if d := strings.TrimSpace(song.DestBase); d != "" {
		base = d
	}
	// 主干总长兜底（见 pkg/namefmt.MaxBaseBytes）：DestBase 来自调用方，
	// 也可能带回过长的名字，所以覆盖完之后再夹一次。
	base = namefmt.Clamp(base, namefmt.MaxBaseBytes)
	if dd := strings.TrimSpace(song.DestDir); dd != "" {
		destDir = dd
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return &SongResult{
				SongName: song.Name,
				Singer:   song.Singer,
				Status:   "failed",
				Error:    "创建目标目录失败: " + err.Error(),
			}, err
		}
	}

	// 已存在（任意受支持扩展名且体积达标）→ 直接复用，避免重复下载。
	// 例外：调用方明确要「升级」且现有文件是有损、本次要无损（见 UpgradeIfBetter）。
	var replacePath string
	if existing, ok := findExistingAudio(destDir, base); ok {
		if !song.shouldUpgradeOver(existing) && !song.forceReplaceHere() {
			return &SongResult{
				SongName:     song.Name,
				Singer:       song.Singer,
				Status:       "already_exists",
				Path:         filepath.ToSlash(existing),
				Quality:      song.Quality,
				ActualFormat: strings.TrimPrefix(strings.ToLower(filepath.Ext(existing)), "."),
			}, nil
		}
		replacePath = existing
	}

	// 歌曲修复：目标文件可能**小到不被认成音频**（坏文件常只有几 KB），上面那一步
	// 就会漏掉它。这里按「调用方点名的那份」再找一次 —— 只看存不存在，不看体积、
	// 不嗅魔数：用户已经明确说要换这一份，判据不该再由体积来否决。
	if replacePath == "" && song.forceReplaceHere() {
		if p, ok := findAnyFileWithBase(destDir, base); ok {
			replacePath = p
		}
	}

	// 下载目录里没有同名文件时，再查一次全库索引（曲库管家扫出来的所有目录）。
	//
	// 为什么必须放在同名检查之后：同名文件就在本次目标目录里，能直接原地升级；
	// 而索引命中可能在别的目录，语义不同（下面分别处理）。
	//
	// 为什么要跳过 DestDir/DestBase：那是调用方明确指定的「替换这一份」，
	// 候选歌名与本地文件名不一样时（见 SongPayload.DestDir 注释），不能被
	// 库里另一份同名版本提前挡回去，否则替换永远落不到目标文件上。
	if replacePath == "" && strings.TrimSpace(song.DestDir) == "" && strings.TrimSpace(song.DestBase) == "" {
		if hit, ok := d.findInLibrary(song); ok {
			if !song.shouldUpgradeOver(hit.Path) {
				return &SongResult{
					SongName:     song.Name,
					Singer:       song.Singer,
					Status:       "already_exists",
					Path:         filepath.ToSlash(hit.Path),
					Quality:      song.Quality,
					ActualFormat: strings.TrimPrefix(strings.ToLower(filepath.Ext(hit.Path)), "."),
				}, nil
			}
			// 库里那份档位更低、本次要升级：继续下载。
			//
			// 只有命中文件正好在下载目录里时，才设为 replacePath（原地换掉、不留两份）；
			// 在别的目录就不动它 —— 下载器没有权利去别处删用户的文件，落点仍按
			// 本次请求的目录与文件名（与「替换指定文件」的 DestDir/DestBase 语义分开）。
			if sameDirAs(hit.Path, destDir) {
				replacePath = hit.Path
			}
		}
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
	// 同一个 .part 同时只能有一个写入者：并发写同一份字节流会交错，
	// FLAC 有 CRC 能挡、坏 MP3 会被当成好文件入库（见 install.go 的说明）。
	unlockPart := lockPart(tmpPath)
	defer unlockPart()
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
	req.Header.Set("User-Agent", useragent.Desktop)
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
		// 魔数认不出时先问 Content-Type，最后才退回 mp3。
		// 直接猜 mp3 会把 AAC（ADTS）之类存成 .mp3，播放器按错容器解就是「下载
		// 成功但播不了」；而现在 ADTS / WMA / AIFF 都进了魔数表，剩下的未知容器
		// 极罕见，退回 mp3 只是不让这种边缘情况把下载彻底卡死。
		ext = extFromContentType(resp.Header.Get("Content-Type"))
	}
	if ext == "" {
		log.Printf("[DOWNLOAD] 认不出音频容器（Content-Type=%q），按 mp3 落盘：%s - %s",
			resp.Header.Get("Content-Type"), song.Singer, song.Name)
		ext = "mp3"
	}
	// ── 入库前的最后校验 ──
	//
	// 魔数对不代表内容完好：有些音源会返回「文件头正常、正文含坏帧」的流
	// （文件头合法、扩展名 .flac、体积也正常，但播到一半就坏）。这种文件以前
	// 会被照常改名入库，表现是「下载成功但播不了 / 转码下载失败（errno 68157441）」。
	//
	// 逐帧走一遍结构，用帧头 CRC-8 与整帧 CRC-16 逐帧验签（见 pkg/flacok）。
	// CRC 对不上就一定是坏的（误判概率 1/65536），所以直接拒绝并让上层换档重下，
	// 不留「可能有点小问题」的中间态。
	if ext == "flac" {
		if verr := flacok.ValidateFile(tmpPath); verr != nil {
			_ = os.Remove(tmpPath)
			return &SongResult{
				SongName:  song.Name,
				Singer:    song.Singer,
				Status:    "failed",
				Quality:   song.Quality,
				Error:     "下载到的无损文件是坏流（" + verr.Error() + "），可降到 320K 重试",
				ErrorCode: "flac_corrupt",
			}, fmt.Errorf("%w: %v", ErrCorruptFLAC, verr)
		}
	}
	// 其它无损容器没有解码器可逐帧验，但**结构**能验：WAV 的 data 块长度、
	// M4A 的 atom 链、WavPack 的块链、APE 的头部长度。截断流与自相矛盾的
	// 文件在这里被挡住，不会带着「下载成功但播不了」的假象进库。
	// 失败同样按「坏流」处理：上层（pkg/backenddl）会降档重试。
	if ext != "flac" {
		if verr := audiok.Validate(tmpPath, ext); verr != nil {
			_ = os.Remove(tmpPath)
			return &SongResult{
				SongName:  song.Name,
				Singer:    song.Singer,
				Status:    "failed",
				Quality:   song.Quality,
				Error:     "下载到的无损文件是坏流（" + verr.Error() + "），可降到 320K 重试",
				ErrorCode: "lossless_corrupt",
			}, fmt.Errorf("%w: %v", ErrCorruptLossless, verr)
		}
	}

	finalPath := allocateUniquePath(destDir, base, ext)
	// 替换场景要「接管」目标名，而不是在旁边生成 `xxx (1).flac`：
	//   - 扩展名没变（128k mp3 → 320k mp3）→ 直接覆盖原文件（rename 原子替换）；
	//   - 扩展名变了（mp3 → flac）→ 用同名换扩展名，随后删掉旧的（下面 replacePath 那段）；
	//   - 目标名被别人占着（极少）→ 退回 allocateUniquePath 的结果，宁可有影子文件也不覆盖别人的。
	overwriteOK := false
	if replacePath != "" {
		want := filepath.Join(destDir, base+"."+ext)
		if strings.EqualFold(filepath.ToSlash(want), filepath.ToSlash(replacePath)) {
			finalPath = want
			overwriteOK = true // 覆盖这份旧文件是用户点的「升级音质」，属于意图之内
		} else if _, serr := os.Stat(want); serr != nil {
			finalPath = want
		}
	}
	if overwriteOK {
		if err := os.Rename(tmpPath, finalPath); err != nil {
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("保存最终文件失败: %w", err)
		}
	} else {
		// 独占安装：预演与落地之间目标名被别人占了就换个名字，绝不覆盖。
		for attempt := 0; ; attempt++ {
			ierr := installFileExclusive(tmpPath, finalPath)
			if ierr == nil {
				_ = os.Remove(tmpPath)
				break
			}
			if !errors.Is(ierr, os.ErrExist) {
				_ = os.Remove(tmpPath)
				return nil, fmt.Errorf("保存最终文件失败: %w", ierr)
			}
			if attempt >= 8 {
				_ = os.Remove(tmpPath)
				return nil, fmt.Errorf("保存最终文件失败: 目标名一直被占用（%s）", filepath.Base(finalPath))
			}
			finalPath = allocateUniquePath(destDir, base, ext)
		}
	}

	// 升级路径：新文件已就位，删掉那份旧的有损文件。
	//
	// 放在 rename **之后**：新文件没落稳就把旧的删了，一次网络抖动就能让用户
	// 既没无损也没 320k。删除失败也不该让整次下载失败（新文件已经在），只记日志。
	if replacePath != "" && !strings.EqualFold(filepath.ToSlash(replacePath), filepath.ToSlash(finalPath)) {
		if rerr := os.Remove(replacePath); rerr != nil {
			log.Printf("[DOWNLOAD] 升级后删除旧文件失败（新的已就位）：%s %v", replacePath, rerr)
		} else {
			log.Printf("[DOWNLOAD] 音质升级：%s → %s", filepath.ToSlash(replacePath), filepath.ToSlash(finalPath))
			nas.InvalidateScanCacheFile(replacePath)
			// 扩展名变了（mp3 → flac）时，旧的同名 .lrc 会留在原地变成孤儿：
			// 新文件的歌词按新主干另写一份，旧的那份谁也不再读它。顺手清掉。
			removeOrphanLrc(replacePath, finalPath)
		}
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
	req.Header.Set("User-Agent", useragent.Desktop)
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
	// ADTS AAC：0xFFF 同步字 + layer 位为 00（MP3 的 layer 位必非 0）。
	// 必须先于下面的「MPEG 帧同步」判断，否则 AAC 会被认成 mp3。
	case head[0] == 0xFF && head[1]&0xF6 == 0xF0:
		return "aac"
	// MPEG 帧同步字：11 位全 1
	case head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return "mp3"
	// ASF/WMA 的 GUID 头
	case head[0] == 0x30 && head[1] == 0x26 && head[2] == 0xB2 && head[3] == 0x75:
		return "wma"
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

// extFromContentType 把 Content-Type 映射成扩展名（魔数认不出时的第二道判断）。
func extFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "audio/mpeg", "audio/mp3", "audio/x-mpeg":
		return "mp3"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/aac", "audio/aacp", "audio/x-aac":
		return "aac"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "m4a"
	case "audio/ogg", "application/ogg":
		return "ogg"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/x-ms-wma", "audio/wma":
		return "wma"
	case "audio/x-ape", "audio/ape":
		return "ape"
	}
	return ""
}

// MimeForAudioHead 依据文件头魔数给出该音频容器的标准 MIME（认不出返回空串）。
//
// 为什么需要：第三方直链的 Content-Type 经常是错的。实测网易云把 FLAC 直链
// 标成 audio/mpeg，播放中继原样透传，浏览器就拿到「声明是 mp3、内容是 FLAC」
// 的响应 —— 桌面 Chrome 靠内容嗅探还能放，但 canPlayType/MSE 判定与部分
// 移动端 WebView 会按声明类型走，表现为「明明有链接却不出声」。
// 这里与下载落地、播放预检共用同一套魔数判据，避免三处各说各话。
func MimeForAudioHead(head []byte) string {
	switch detectAudioExtByMagic(head) {
	case "flac":
		return "audio/flac"
	case "mp3":
		return "audio/mpeg"
	case "aac":
		return "audio/aac"
	case "m4a":
		return "audio/mp4"
	case "ogg":
		return "audio/ogg"
	case "wav":
		return "audio/wav"
	case "wma":
		return "audio/x-ms-wma"
	}
	return ""
}

// SniffAudioExt 把魔数判断暴露给其它包（播放前预检用，见 api.handlePlayerCheck）。
//
// 播放前预检和下载落地必须用**同一套**判据，否则会出现「预检说能播、真播失败」
// 或者反过来的假阴性 —— 两套规则各改一半是最难查的那类 bug。
func SniffAudioExt(head []byte) string { return detectAudioExtByMagic(head) }

// LooksLikeErrorPage 暴露错误页判断（同上：与落地共用一套判据）。
func LooksLikeErrorPage(head []byte, contentType string) bool {
	return isErrorPayload(head, contentType)
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
// isLosslessContainer 判断扩展名是不是无损容器（只按容器，不管码率）。
func isLosslessContainer(ext string) bool {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "flac", "ape", "wav", "dsf", "dff":
		return true
	}
	return false
}

// isLosslessQuality 判断**请求的档位**是不是无损（档位字符串来自平台/前端，写法很杂）。
func isLosslessQuality(q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	switch q {
	case "flac", "flac24bit", "hires", "hi-res", "ape", "wav", "dsd", "dsf", "dff", "lossless", "sq":
		return true
	}
	// 中文「无损」不能小写化，单独判。
	return strings.Contains(q, "无损")
}

// shouldUpgradeOver 判断「已有文件 existing 该不该被本次请求覆盖」。
//
// 判据只有一条：**本次请求的档位严格高于盘上那份**（用同一套实际档位判据，
// 见 pkg/audioext.TierOfFile）。于是：
//
//   - 已有无损 → 永远不覆盖（没有比它更高的档位，也就绝不会降级）；
//   - 已有 128k mp3、这次 320k → **覆盖**（v2.17.2 起；此前只认「有损→无损」，
//     结果是界面说这条能升、下载器回一句「已存在」——用户真正需要升的那批换不了）；
//   - 同档位 → 不覆盖（换个来源的同档文件没有意义，还要重下一遍）；
//   - 档位认不出来 → 不覆盖（「不知道」不能当成「更高」）。
func (s SongPayload) shouldUpgradeOver(existing string) bool {
	if !s.UpgradeIfBetter {
		return false
	}
	cur := audioext.TierOfFile(existing)
	want := audioext.TierRank(s.Quality)
	if want < 0 {
		return false
	}
	if want > audioext.TierRank(cur) {
		return true
	}
	// 同档位、**不同格式**也要覆盖（2026-10-23）。
	//
	// 用户报的原话：「选择下载音质最高，下载队列显示音质与实际音质不符」。
	// 其中一种成因在盘上而不是在界面：这条曲目以前按渠道推出来的「320k」
	// 下成了一个 mp3，而这次 `highest` 实际要的是 flac —— 两个请求的 `Quality`
	// 字符串都被推成 `320k`（渠道推出来的档位就是它），档位判据于是说「同档，
	// 不用换」，而用户放耳朵里听到的、在文件管理器里看到的都是 mp3。
	//
	// 判据第二条：**要的容器与盘上的容器不同**，就当作「这次能拿到不一样的」去下。
	// 这不会造成降级 —— 真下回来的档位更低时，落盘后的收尾仍按实际档位比对
	// （见 downloadSingleSong 里 replacePath 的清理路径），无损永远不会被 mp3 顶掉。
	if strings.EqualFold(filepath.Ext(existing), "."+strings.TrimPrefix(strings.ToLower(s.Quality), ".")) {
		return false
	}
	return sameTierDifferentContainer(s.Quality, existing)
}

func findExistingAudio(destDir, base string) (string, bool) {
	for _, ext := range audioExtensions {
		p := filepath.Join(destDir, base+"."+ext)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && libraryFileIsAudio(p, fi.Size()) {
			return p, true
		}
	}
	return "", false
}

// forceReplaceHere 判断这次请求是不是「点名要换掉盘上那一份」。
//
// 两个条件都要：ForceReplace 打开 **且** 调用方给了目标位置。只开开关不给位置时
// 落点是按候选歌名算出来的，那不是「用户点的那份文件」，不能拿它去覆盖别人的东西。
func (s SongPayload) forceReplaceHere() bool {
	return s.ForceReplace && strings.TrimSpace(s.DestBase) != ""
}

// findAnyFileWithBase 找同主干的音频文件，**不看体积、不嗅魔数**。
//
// 只给 forceReplaceHere 用。为什么需要单独一个：findExistingAudio 依赖
// libraryFileIsAudio，而它对 <32KB 的文件一律返回 false（那是为了别把封面图、
// 歌词碎片当成歌）。可歌曲修复要换的坏文件恰好都在这一档 —— 真机那份
// `01 夜曲.mp3` 只有 4127 字节（ID3 标签占了 3601，音频只剩 516 字节）。
// 于是「找不到旧文件」→ allocateUniquePath 生成 `01 夜曲 (2).mp3` → 坏的还在。
func findAnyFileWithBase(destDir, base string) (string, bool) {
	for _, ext := range audioExtensions {
		p := filepath.Join(destDir, base+"."+ext)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}
	return "", false
}

// removeOrphanLrc 清掉「换扩展名后留下的孤儿歌词」。
//
// 只在**主干没变、只有扩展名变了**时动手（mp3 → flac）：那种情况下新旧两份的
// .lrc 路径完全相同，新歌词已经写在那个位置上了，不存在要删的东西。真正会留孤儿
// 的是主干也变了的情形（调用方给的 DestBase 与旧文件名不同），此时旧 .lrc 谁也不读。
// 删不掉不算错（可能用户手工放过一份），静默跳过。
func removeOrphanLrc(oldPath, newPath string) {
	oldLrc := strings.TrimSuffix(oldPath, filepath.Ext(oldPath)) + ".lrc"
	newLrc := strings.TrimSuffix(newPath, filepath.Ext(newPath)) + ".lrc"
	if strings.EqualFold(filepath.ToSlash(oldLrc), filepath.ToSlash(newLrc)) {
		return
	}
	if fi, err := os.Stat(oldLrc); err == nil && !fi.IsDir() {
		_ = os.Remove(oldLrc)
	}
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

	// 没授权目录（destDir 为空）时不可能有任何「已下载」文件：下面按 destDir 拼
	// 路径会得到相对路径，命中不了任何条目，自然全部回 exists=false —— 这正是
	// 期望结果，界面照常能画，引导提示交给 /api/fnos/auth-status 的 hint。
	destDir := d.getDownloadDir()
	existsMap := make(map[string]bool)
	// paths / upgradable 是**新增**的附加字段（老前端只读 exists，不受影响）：
	//   paths[key]      = 命中的那份文件在哪 —— 「已下载」时告诉用户东西在哪，
	//                     「可升级」时告诉他会被替换掉的是哪一份；
	//   upgradable[key] = 盘上那份档位更低、还能升，界面可以显示「可升级」。
	pathsMap := make(map[string]string)
	upgradableMap := make(map[string]bool)
	// 一次请求只取一份全库快照：批量探测几百首时，每首重建一遍索引列表
	// （几万条记录的拷贝）纯属浪费，而且同一批里条目本来就不该变。
	entries := d.librarySnapshot()

	for _, s := range req.Songs {
		cleanSinger := sanitizeFilename(s.Singer)
		cleanName := sanitizeFilename(s.Name)
		base := namefmt.Build(cleanSinger, cleanName)
		key := fmt.Sprintf("%s - %s", s.Singer, s.Name)
		// ⚠️ 查重必须扫**全部音频扩展名**，不能只看 `.mp3`。
		//
		// 落盘扩展名由魔数嗅探决定（见 detectAudioExtByMagic），下载现在会优先取无损，
		// 所以「已下载」的那份很可能是 .flac。硬编码 .mp3 会让已经下过的无损查不到 ——
		// 界面照样显示「未下载」，用户再点一次就是白白重下一遍（一张专辑几百 MB）。
		hitPath, found := findExistingAudio(destDir, base)
		// 下载目录里没有同名文件时查全库索引（跨目录、不同文件名的同一首）。
		// 与 /download/song 复用同一套判据（MatchLibrary），否则界面显示的「已有」
		// 和实际会不会下载会对不上。
		if !found {
			if hit, ok := MatchLibrary(entries, s); ok {
				hitPath, found = hit.Path, true
			}
		}

		upgradable := false
		if found {
			// 盘上这份档位低于**本次请求**的音质 → 不算「已下载」，但要标出可升级。
			//
			// 判据用的是 libraryUpgradable（内部走 shouldUpgradeOver），而不是
			// 「有没有更新版」这种模糊说法：只有调用方明确带了足够高的 quality
			// 才算可升级，否则界面会对每首歌都喊「可升级」。
			if up, _ := libraryUpgradable(s, hitPath); up {
				found, upgradable = false, true
			}
		}

		existsMap[key] = found
		if s.ID != "" {
			existsMap[s.ID] = found
		}
		if hitPath != "" {
			pathsMap[key] = filepath.ToSlash(hitPath)
			if s.ID != "" {
				pathsMap[s.ID] = filepath.ToSlash(hitPath)
			}
		}
		if upgradable {
			upgradableMap[key] = true
			if s.ID != "" {
				upgradableMap[s.ID] = true
			}
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":       200,
		"exists":     existsMap,
		"paths":      pathsMap,
		"upgradable": upgradableMap,
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
	//
	// 没有下载目录（= 还没授权任何目录）时直接拒绝：filepath.Abs("") 会返回
	// **当前工作目录**，那样这道检查就形同虚设。
	if d.getDownloadDir() == "" {
		return false, ErrNoDownloadDir
	}
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

// sameTierDifferentContainer 判断「这次请求的档位与盘上那份文件**同档，但容器该更好**」。
//
// 只处理一种情况：盘上是**有损**容器（mp3/m4a/...），而这次请求的档位是无损
// （flac / flac24bit / hires）。这种组合下 `TierRank` 本应已经判出高低，
// 但历史上有些记录把「渠道推出来的 320k」与「实际无损」写成同一个字符串，
// 单看字符串会漏掉。用**文件扩展名**这个盘上事实来兜底，比再引入一个档位字段可靠。
//
// 反方向（盘上无损、这次 320k）永远返回 false —— 绝不降级。
func sameTierDifferentContainer(want, existing string) bool {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(existing)), ".")
	if ext == "" {
		return false
	}
	if !isLossyExt(ext) {
		return false // 盘上已经无损
	}
	return isLosslessTierName(want)
}

// isLossyExt 是有损容器扩展名（与 pkg/audioext 的口径保持一致的一组最小集）。
func isLossyExt(ext string) bool {
	switch ext {
	case "mp3", "m4a", "aac", "ogg", "wma":
		return true
	}
	return false
}

// isLosslessTierName 判断音质档位名是否属于无损。
func isLosslessTierName(q string) bool {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "flac", "flac24bit", "hires":
		return true
	}
	return false
}
