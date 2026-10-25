package appupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 下载/安装状态机的取值。前端按这些字符串切换界面，**不要**改成中文。
const (
	StatusIdle        = "idle"
	StatusDownloading = "downloading"
	StatusReady       = "ready"
	StatusInstalling  = "installing"
	StatusError       = "error"
)

const (
	// defaultCacheTTL 是检查结果的缓存时长。GitHub 未鉴权限速 60 次/小时/IP，
	// 用户反复打开「关于」页时不该每次都打一次 API。
	defaultCacheTTL = 10 * time.Minute
	// defaultScriptDir 必须**在应用目录之外**：安装脚本会 uninstall 掉应用，
	// 放在应用目录里等于脚本把自己删了（knock 的 apply-update.sh 也是这个原因放 /tmp）。
	defaultScriptDir = "/tmp/yinshu-update"
	scriptName       = "apply-update.sh"
	envName          = "install.env"
	logName          = "install.log"
	// 下载超时：fpk 通常几十 MB，弱网给足 30 分钟；超时后任务可重新发起。
	downloadTimeout = 30 * time.Minute
	// downloadRetries 是下载请求的尝试次数（含第一次）。GitHub 经代理回源偶发
	// 「响应头超时」，一次抖动就整个失败太可惜（2026-10-24 用户截图）。
	downloadRetries = 2
)

// downloadRetryWait 是两次尝试之间的间隔。做成变量而不是常量：测试要能把它调小，
// 否则每个重试用例都得真等 3 秒。
var downloadRetryWait = 3 * time.Second

// CheckResult 是 GET /api/app/update/check 返回的数据。
type CheckResult struct {
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	HasUpdate   bool   `json:"has_update"`
	Notes       string `json:"notes"`
	PublishedAt string `json:"published_at"`
	AssetName   string `json:"asset_name"`
	AssetURL    string `json:"asset_url"`
	Size        int64  `json:"size"`
	HTMLURL     string `json:"html_url"`
	CheckedAt   string `json:"checked_at"`
	Error       string `json:"error"`
}

// DownloadState 是 GET /api/app/update/state 返回的数据。
type DownloadState struct {
	Status   string  `json:"status"`
	Progress float64 `json:"progress"`
	Path     string  `json:"path"`
	Size     int64   `json:"size"`
	Error    string  `json:"error"`
	Version  string  `json:"version,omitempty"`
}

// InstallResult 是 POST /api/app/update/install 返回的数据。
type InstallResult struct {
	Started bool   `json:"started"`
	Log     string `json:"log"`
}

// Options 是 Manager 的构造参数。
//
// 所有「会碰系统」的动作（执行 shell、判断 root）都做成可替换的字段，
// 测试里能把整个安装流程跑完而不真的动 /var/apps —— 否则这个包只有真机才测得了，
// 而真机测试意味着每跑一次测试就卸载一次应用。
type Options struct {
	DataDir        string
	AppName        string // 包名（/var/apps/<appname>）；为空时用 DefaultAppName
	CurrentVersion string
	Repo           string // owner/repo
	Token          string // 可选 GitHub token
	APIURL         string // 测试/镜像用；空 = 官方 releases/latest
	Client         *http.Client
	// RateKBPS 是下载安装包的限速（KB/s）。0 = 不限速。
	//
	// 用户 2026-10-25：「版本更新新增限制速率，避免被限速」——共享代理后面
	// 裸速下载会把整条出口吃满，镜像/代理侧也更容易 429。限速在**读 body** 时做
	// （见 download），不影响检查更新这类小请求。
	RateKBPS int
	// TokenFunc / RateFunc 是**每次下载前重新读一次**的取值函数（可选）。
	//
	// 为什么要函数而不是只在构造时读一次：这两个值在「系统 → 设置」里可改，
	// 用户改完不希望「要点重启才生效」；而 Manager 是进程级单例，构造时就固定了。
	// 有函数时以函数为准（函数返回的 0 表示不限速、空串表示不用 token）。
	TokenFunc     func() string
	RateFunc      func() int
	Logf          func(string, ...any)
	Now           func() time.Time
	CacheTTL      time.Duration
	ScriptDir     string
	Launch        func(script, logPath string) error
	IsRoot        func() bool
	SudoAvailable func() bool
}

// Manager 持有检查缓存、下载状态和安装动作。
//
// 为什么是单例（每个进程一个）而不是每次请求新建：下载进度和「正在安装」是
// **进程级**状态，两次请求各建一个对象会让 progress 永远停在 0，而且可能并发
// 下载两份 fpk。Manager 内部用一把锁保护全部状态，API 层无脑转调即可。
type Manager struct {
	dataDir string
	appName string
	current string
	repo    string
	token   string
	apiURL  string
	ua      string
	dir     string // 安装脚本目录
	// tokenFunc 见 Options.TokenFunc。
	tokenFunc func() string

	client *http.Client
	logf   func(string, ...any)
	now    func() time.Time
	ttl    time.Duration

	launch        func(script, logPath string) error
	isRoot        func() bool
	sudoAvailable func() bool

	mu       sync.Mutex
	cache    CheckResult
	cachedAt time.Time
	hasCache bool

	dl           DownloadState
	assetURL     string
	assetName    string
	assetVersion string
	cancel       context.CancelFunc
}

// New 构造 Manager。返回的实例可以立刻使用；检查/下载都是显式调用才发生。
func New(opts Options) *Manager {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ttl := opts.CacheTTL
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	dir := strings.TrimSpace(opts.ScriptDir)
	if dir == "" {
		dir = defaultScriptDir
	}
	appName := strings.TrimSpace(opts.AppName)
	if appName == "" {
		appName = DefaultAppName
	}
	repo := strings.TrimSpace(opts.Repo)
	if repo == "" {
		repo = DefaultRepo
	}
	if r := strings.TrimSpace(os.Getenv("FN_UPDATE_REPO")); r != "" {
		repo = r
	}
	dataDir := strings.TrimSpace(opts.DataDir)
	if dataDir != "" {
		// 必须是绝对路径：安装脚本以 / 为工作目录执行，相对路径会让
		// appcenter-cli install-fpk 找不到刚下好的 fpk。
		if abs, err := filepath.Abs(dataDir); err == nil {
			dataDir = abs
		}
	}
	m := &Manager{
		dataDir:       dataDir,
		appName:       appName,
		current:       strings.TrimSpace(opts.CurrentVersion),
		repo:          repo,
		token:         strings.TrimSpace(opts.Token),
		tokenFunc:     opts.TokenFunc,
		apiURL:        strings.TrimSpace(opts.APIURL),
		dir:           dir,
		client:        opts.Client,
		logf:          logf,
		now:           now,
		ttl:           ttl,
		launch:        opts.Launch,
		isRoot:        opts.IsRoot,
		sudoAvailable: opts.SudoAvailable,
	}
	if m.client == nil {
		m.client = newUpdateClient()
	}
	if m.launch == nil {
		m.launch = launchDetached
	}
	if m.isRoot == nil {
		m.isRoot = func() bool { return os.Geteuid() == 0 }
	}
	if m.sudoAvailable == nil {
		m.sudoAvailable = detectSudo
	}
	m.ua = "yinshu-ai-updater/" + m.current
	// 零值 DownloadState 的 Status 是空串，前端按枚举判断会显示空白；
	// 初始状态必须是明确的 idle。
	m.dl.Status = StatusIdle
	return m
}

// Current 返回应用当前版本（供 /api/app/version 之外的地方复用）。
func (m *Manager) Current() string { return m.current }

// AppName 返回解析出的包名。
func (m *Manager) AppName() string { return m.appName }

// Check 检查更新。refresh=false 时命中 10 分钟缓存。
//
// 为什么把网络错误塞进 CheckResult.Error 而不是返回 error：界面需要
// 「当前版本 + 检查失败原因」同时可见；返回 error 就只剩一个红字，用户看不到
// 自己正跑在哪个版本。API 层因此永远返回 code=200，error 字段非空即失败。
func (m *Manager) Check(ctx context.Context, refresh bool) CheckResult {
	m.mu.Lock()
	if !refresh && m.hasCache && m.now().Sub(m.cachedAt) < m.ttl {
		res := m.cache
		m.mu.Unlock()
		return res
	}
	m.mu.Unlock()

	res := CheckResult{Current: m.current, CheckedAt: m.now().Format(time.RFC3339)}

	// m.tokenNow() 而不是 m.token 字段：设置里刚改的 token 下一次检查就用上。
	rel, err := FetchLatestRelease(ctx, m.client, m.apiURL, m.repo, m.tokenNow(), m.ua)
	if err != nil {
		res.Error = err.Error()
		m.storeCheck(res)
		return res
	}
	res.Latest = strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	res.Latest = strings.TrimPrefix(res.Latest, "V")
	res.Notes = rel.Body
	res.PublishedAt = rel.PublishedAt
	res.HTMLURL = rel.HTMLURL
	res.HasUpdate = IsNewer(res.Latest, m.current)
	if asset := PickFPKAsset(rel.Assets); asset != nil {
		res.AssetName = asset.Name
		res.AssetURL = asset.DownloadURL
		res.Size = asset.Size
	} else {
		// 有新版却没有 fpk：这是发布侧问题，明确告诉用户，别让人反复点「更新」。
		res.Error = fmt.Sprintf("已找到新版本 %s，但该 release 里没有 .fpk 安装包", res.Latest)
	}
	if res.Latest == "" {
		res.Error = "更新源返回的版本号为空"
	}
	m.storeCheck(res)
	m.mu.Lock()
	m.assetURL = res.AssetURL
	m.assetName = res.AssetName
	m.assetVersion = res.Latest
	m.mu.Unlock()
	m.logf("[UPDATE] 检查更新：当前 %s，最新 %s，有更新 %v，error=%q", res.Current, res.Latest, res.HasUpdate, res.Error)
	return res
}

func (m *Manager) storeCheck(res CheckResult) {
	m.mu.Lock()
	m.cache = res
	m.cachedAt = m.now()
	m.hasCache = true
	m.mu.Unlock()
}

// State 返回当前下载/安装状态。
func (m *Manager) State() DownloadState {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.dl
	if st.Status == StatusReady && st.Path != "" {
		// 用户可能手动删了 updates 目录：状态要和磁盘一致，不能显示「已就绪」
		// 却点了安装才报错。
		if _, err := os.Stat(st.Path); err != nil {
			st.Status = StatusIdle
			st.Path = ""
			st.Size = 0
			st.Progress = 0
			m.dl = st
		}
	}
	return st
}

// StartDownload 启动后台下载并立即返回当前状态（不等下载完成）。
//
// 幂等：已 ready 直接回 ready；正在 downloading 回当前进度；只有空闲/失败才发起
// 新任务。这样前端连点两下「更新」不会产生两个下载协程抢同一个文件。
func (m *Manager) StartDownload(ctx context.Context) (DownloadState, error) {
	m.mu.Lock()
	switch m.dl.Status {
	case StatusDownloading:
		st := m.dl
		m.mu.Unlock()
		return st, nil
	case StatusReady:
		if m.dl.Path != "" {
			if _, err := os.Stat(m.dl.Path); err == nil {
				st := m.dl
				m.mu.Unlock()
				return st, nil
			}
		}
	case StatusInstalling:
		st := m.dl
		m.mu.Unlock()
		return st, fmt.Errorf("正在安装，无法再次下载")
	}
	url, name, version := m.assetURL, m.assetName, m.assetVersion
	m.mu.Unlock()

	if url == "" {
		// 自动补一次检查时也加超时：调用方可能传的是 HTTP 请求 ctx，
		// 用户关了页面才断，而 GitHub 卡住不该让下载请求一直挂着。
		checkCtx, cancel := context.WithTimeout(ctx, CheckTimeout)
		res := m.Check(checkCtx, false)
		cancel()
		if res.Error != "" {
			return DownloadState{Status: StatusError, Error: res.Error}, fmt.Errorf("%s", res.Error)
		}
		m.mu.Lock()
		url, name, version = m.assetURL, m.assetName, m.assetVersion
		m.mu.Unlock()
	}
	if url == "" || name == "" {
		err := fmt.Errorf("没有可下载的安装包，请先检查更新")
		return DownloadState{Status: StatusError, Error: err.Error()}, err
	}

	dlCtx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	m.mu.Lock()
	m.cancel = cancel
	m.dl = DownloadState{Status: StatusDownloading, Progress: 0, Version: version}
	state := m.dl
	m.mu.Unlock()

	go m.download(dlCtx, url, name)
	return state, nil
}

// download 是后台协程：流式落盘 + 逐块更新进度。
func (m *Manager) download(ctx context.Context, rawURL, assetName string) {
	defer func() {
		m.mu.Lock()
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.mu.Unlock()
	}()

	fail := func(err error) {
		m.logf("[UPDATE] 下载失败：%v", err)
		m.mu.Lock()
		m.dl.Status = StatusError
		m.dl.Error = err.Error()
		m.dl.Path = ""
		m.mu.Unlock()
	}

	name := safeAssetName(assetName)
	if name == "" {
		fail(fmt.Errorf("安装包文件名非法：%q", assetName))
		return
	}
	dir := filepath.Join(m.dataDir, "updates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(fmt.Errorf("创建更新目录失败：%w", err))
		return
	}
	finalPath := filepath.Join(dir, name)
	tmpPath := finalPath + ".part"

	// 一次网络抖动不该报废整个更新：GitHub 经代理回源偶发首字节超时，重试一次再报错。
	// 只重试「请求没发出去/响应头没回来」这一类传输层错误；HTTP 状态码不对不重
	// （404 的资产重试一百次也不会出现）。
	var resp *http.Response
	for attempt := 1; ; attempt++ {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if rerr != nil {
			fail(rerr)
			return
		}
		req.Header.Set("User-Agent", m.ua)
		req.Header.Set("Accept", "application/octet-stream")
		resp, rerr = m.client.Do(req)
		if rerr == nil {
			break
		}
		if attempt >= downloadRetries || ctx.Err() != nil {
			fail(fmt.Errorf("下载请求失败：%w", rerr))
			return
		}
		m.logf("[UPDATE] 下载第 %d 次失败（%v），%s 后重试", attempt, rerr, downloadRetryWait)
		m.mu.Lock()
		m.dl.Error = fmt.Sprintf("网络不稳定，正在重试（%v）", rerr)
		m.mu.Unlock()
		select {
		case <-time.After(downloadRetryWait):
		case <-ctx.Done():
			fail(ctx.Err())
			return
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fail(fmt.Errorf("下载失败：HTTP %d", resp.StatusCode))
		return
	}
	m.mu.Lock()
	m.dl.Error = "" // 清掉重试期间写下的「正在重试」
	m.mu.Unlock()

	// 有 Content-Length 就按它显示进度并最终校验；没有则显示已下载字节（progress=0）。
	total := resp.ContentLength
	if total <= 0 {
		m.mu.Lock()
		total = m.dl.Size
		m.mu.Unlock()
	}

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		fail(fmt.Errorf("创建临时文件失败：%w", err))
		return
	}
	var written int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				_ = f.Close()
				_ = os.Remove(tmpPath)
				fail(fmt.Errorf("写入安装包失败：%w", werr))
				return
			}
			written += int64(n)
			m.mu.Lock()
			m.dl.Progress = percent(written, total)
			m.dl.Size = written
			m.mu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			fail(fmt.Errorf("下载中断：%w", rerr))
			return
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		fail(fmt.Errorf("关闭安装包失败：%w", err))
		return
	}
	// 校验长度：CDN/代理截断时 io.Copy 可能「正常」EOF，落盘的是半个包，
	// 装上去会把应用弄坏 —— 这个检查是唯一能提前发现的地方。
	if total > 0 && written != total {
		_ = os.Remove(tmpPath)
		fail(fmt.Errorf("安装包不完整：期望 %d 字节，实际 %d 字节", total, written))
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		fail(fmt.Errorf("保存安装包失败：%w", err))
		return
	}

	m.mu.Lock()
	m.dl.Status = StatusReady
	m.dl.Path = finalPath
	m.dl.Size = written
	m.dl.Progress = 100
	m.dl.Error = ""
	m.mu.Unlock()
	m.logf("[UPDATE] 安装包已就绪：%s（%d 字节）", finalPath, written)
}

// Install 写安装脚本并脱离当前进程启动它。
//
// 为什么不在本进程里直接调 appcenter-cli：uninstall 会杀掉本进程，
// 后面的 install-fpk 就没人执行了，应用会**消失**。脚本必须由独立会话
// （setsid）持有，且脚本自身放在应用目录之外（/tmp）。
func (m *Manager) Install() (InstallResult, error) {
	st := m.State()
	if st.Status == StatusInstalling {
		return InstallResult{Started: true, Log: m.logPath()}, nil
	}
	if st.Status != StatusReady || st.Path == "" {
		return InstallResult{}, fmt.Errorf("还没有下载好的安装包，请先下载更新")
	}
	if _, err := os.Stat(st.Path); err != nil {
		return InstallResult{}, fmt.Errorf("安装包不存在（可能已被清理）：%w", err)
	}

	// 权限检查放在写脚本之前：让用户在点「立即安装」的当下就看到可操作提示，
	// 而不是等 10 秒后看安装日志里的一行 permission denied。
	if !m.isRoot() {
		if !m.sudoAvailable() {
			return InstallResult{}, fmt.Errorf("当前应用没有安装权限，请到飞牛应用中心手动安装新版 fpk")
		}
	}

	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return InstallResult{}, fmt.Errorf("创建更新脚本目录失败：%w", err)
	}
	envPath := filepath.Join(m.dir, envName)
	if err := os.WriteFile(envPath, []byte(collectInstallEnv(os.Environ())), 0o600); err != nil {
		return InstallResult{}, fmt.Errorf("写入安装环境变量失败：%w", err)
	}
	scriptPath := filepath.Join(m.dir, scriptName)
	body := BuildApplyScript(ScriptConfig{
		Pkg:     m.appName,
		Fpk:     st.Path,
		EnvFile: envPath,
		Stage:   m.dir,
		Log:     m.logPath(),
	})
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		return InstallResult{}, fmt.Errorf("写入安装脚本失败：%w", err)
	}
	logPath := m.logPath()
	m.mu.Lock()
	m.dl.Status = StatusInstalling
	m.mu.Unlock()

	// 延后 800ms 再拉脚本：HTTP 响应要先写完（连接可能被 uninstall 直接切断），
	// 用户在界面上看到「开始安装」才合得上后面的失联。
	go func() {
		time.Sleep(800 * time.Millisecond)
		m.logf("[UPDATE] 启动安装脚本：%s（日志 %s）", scriptPath, logPath)
		if err := m.launch(scriptPath, logPath); err != nil {
			m.logf("[UPDATE] 启动安装脚本失败：%v", err)
			m.mu.Lock()
			m.dl.Status = StatusError
			m.dl.Error = "启动安装脚本失败：" + err.Error()
			m.mu.Unlock()
		}
	}()
	return InstallResult{Started: true, Log: logPath}, nil
}

func (m *Manager) logPath() string { return filepath.Join(m.dir, logName) }

// percent 把已写字节换算成 0~100；总大小未知时返回 0（界面显示「已下载 N MB」）。
func percent(written, total int64) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(written) * 100 / float64(total)
	if p > 100 {
		p = 100
	}
	return float64(int64(p*10)) / 10
}

// safeAssetName 只保留文件名部分，防 `../../` 之类把 fpk 写到数据目录之外。
func safeAssetName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	base := filepath.Base(name)
	if base == "." || base == string(filepath.Separator) || strings.ContainsAny(base, `/\`) {
		return ""
	}
	return base
}
