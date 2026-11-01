package nas

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/fnos"
)

// allowedRootsEnv 允许通过环境变量自定义可访问根目录（冒号分隔）。
// 设置后**完全覆盖**内置根目录；如需在默认根目录之外追加，请把内置根一并写入该变量。
const allowedRootsEnv = "FN_ALLOWED_ROOTS"

// 路径校验错误。HTTP 层据此映射 400（请求不合法）或 403（越权/未配置根目录）。
var (
	// ErrPathEmpty 路径参数为空
	ErrPathEmpty = errors.New("path 参数必填")
	// ErrPathNotAbsolute 必须是绝对路径
	ErrPathNotAbsolute = errors.New("必须提供绝对路径")
	// ErrPathInvalid 路径包含非法字符
	ErrPathInvalid = errors.New("路径包含非法字符")
	// ErrPathForbidden 路径不在允许访问的根目录范围内
	ErrPathForbidden = errors.New("路径不在允许访问的目录范围内，已拒绝访问")
	// ErrNoAllowedRoots 没有可用根目录（fail closed）
	//
	// 文案面向用户：飞牛环境下「没有根目录」不是配置错误，而是**还没授权**，
	// 所以直接说清下一步该做什么，别只甩一句「未配置可访问的根目录」。
	ErrNoAllowedRoots = errors.New("还没有被授权访问任何目录，请先在「应用设置 → 授权目录」里授权一个音乐文件夹")
	// ErrPathNotFound 路径不存在或不可访问（含软链接悬空）
	ErrPathNotFound = errors.New("路径不存在或不可访问")
	// ErrPathNotAudio 请求的文件不是受支持的音频类型
	ErrPathNotAudio = errors.New("仅支持音频文件，已拒绝访问")
)

// fnosDetectedSource 供测试替换，默认就是 fnos.Detected()。
//
// 单独抽出来是因为 AllowedRoots 的**分支取决于「是不是飞牛环境」**：
// 在飞牛里只认官方授权目录，在开发机/纯 Linux 上才退回卷探测。测试要能钉住
// 这两种分支，而 fnos.Detected() 依赖真实部署目录，测试里造不出来。
//
// ⚠️ 2026-10-31 这里从 fnos.Available() 换成了 fnos.Detected()：前者要求
// TRIM_API_TOKEN 非空，而真机上系统可能没注入 token（app center 降级），于是
// 「明明在飞牛」被判成「不在飞牛」，AllowedRoots 转而并入 /vol1../vol3 探测卷，
// 造成「用户没授权任何目录，曲率却能列出整卷」。详见 fnos.Detected() 的注释。
var fnosDetectedSource = fnos.Detected

// AllowedRoots 返回允许访问的根目录列表（均为已解析软链接、确实存在的目录）。
//
// 优先级（三条互斥分支，2026-10-31 按官方权限模型重排）：
//
//  1. **环境变量 FN_ALLOWED_ROOTS 设置了（即使空串）→ 显式覆盖**：
//     飞牛授权目录 + 该变量的值。这是给「开发机 / 非飞牛部署 / 高级用户」的
//     手动阀门，写了就按写的来。
//
//  2. **在飞牛环境里（见 fnos.Detected()：api-scope socket 或 /var/apps/<appname> 存在）→ 只认飞牛授权目录**：
//     a) 管理员共享授权（trim.file.getSharedAccessibleFolders）；
//     b) 系统注入的 TRIM_DATA_ACCESSIBLE_PATHS / TRIM_DATA_SHARE_PATHS。
//     两路是**并集**，任何一路失败不影响另一路。
//     这里**不再**兜底 `/vol1..16`、`/media`、`/mnt`：官方模型是「没授权 = 看不见」，
//     自动把整个存储卷放进来等于绕过用户在应用设置里的授权动作，既不符合规范，
//     也让「我只授权了一个子目录」实际变成「整个卷可读」。没有授权时返回空切片，
//     由前端提示条引导用户去授权（见 /api/fnos/auth-status）。
//
//     ⚠️ 判据不看 TRIM_API_TOKEN：真机上系统可能没注入 token（app center 降级），
//     那时 Available() 为 false 但机器就是飞牛。用 token 当判据会把这种情况误判成
//     「不在飞牛」，转而并入探测卷 —— 这正是「没授权却能看到整卷」的根因。
//
//     ⚠️ 2026-10-31 用户明确「这个应用只给管理员自己用」，所以**不再查询当前用户的
//     个人授权**（trim.file.getUserAccessibleFolders / pickUserFile）。个人授权的目录
//     绑定在登录账号上，换个账号或 uid 变化就要重新授权，对单管理员场景只会多一个
//     会失效的来源。授权入口统一为「管理员共享授权」（pickSharedFile）。
//
//  3. **不在飞牛环境（开发机 / 非飞牛 Linux）→ 用 getAvailableVolumes()**：
//     探到的 `/vol1..16`、`/media`、`/mnt`（**不含 /home**，见那边的注释）。
//     这条分支的存在只是为了让本地开发和「绿色版」还能用，飞牛里永远走上面第 2 条。
//
// 不存在的条目会被忽略；返回空切片表示不允许访问任何路径（fail closed）。
// 每次调用都重新探测，以便 NAS 卷在运行时挂载/卸载后立即生效（代价仅为少量 stat）；
// 飞牛授权目录需走 socket，故单独做 30 秒缓存。
func AllowedRoots() []string {
	fnosRoots := fnosAuthorizedRoots()

	// ① 显式环境变量：飞牛授权 + 变量值，一起生效（保持历史语义）。
	if envRaw, envSet := os.LookupEnv(allowedRootsEnv); envSet {
		roots := append(fnosRoots, strings.Split(envRaw, string(os.PathListSeparator))...)
		return normalizeRoots(roots)
	}

	// ② 飞牛环境：只认官方授权目录，没有就是空（fail closed），绝不补探测卷。
	if fnosDetectedSource() {
		return normalizeRoots(fnosRoots)
	}

	// ③ 非飞牛环境：退回卷探测，保证开发机还能选目录。
	return normalizeRoots(append(fnosRoots, getAvailableVolumes()...))
}

// fnosSharedRoots 查询飞牛管理员授权给本应用的目录。
// 非飞牛环境或调用失败时返回 nil，调用方自然回退到环境变量/卷探测。
var fnosRootsCache struct {
	sync.Mutex
	at    time.Time
	roots []string
}

// fnosRootsSource 供测试替换。默认语义：开放 API 现在调不通（没令牌 / 没 socket）
// 就当作没有共享目录 —— 这是**正常**降级（真机上系统没注入 token 时就是如此，
// 见 fnos.Detected() 的注释），不记日志；真去拉了但失败才是要说的那件事。
//
// 2026-10-31（授权缓存刷新）：`fnosRootsStale()` 为真时跳过 30 秒缓存，直接问飞牛。
// 它只在「刚授权完」这一小段时间内为真 —— 原因见 noteRootsStale 的注释。
var fnosRootsSource = func() ([]string, error) {
	if !fnos.Available() {
		return nil, nil
	}
	return fnos.SharedFolders()
}

// 授权刚发生后的「缓存不信任窗口」。
//
// 坑（2026-10-31 用户口径「选完目录直接可用，不要再来一步管理员授权」）：
// 授权目录原来只有 30 秒 TTL 缓存，用户刚在宿主选择器里点完目录，后端可能仍在
// 窗口期内 —— 界面于是显示「还是没授权」，用户只能再点一次「我已授权，刷新」。
//
// 为什么不能用 `InvalidateFnOSRootsCache()` 直接解决（它已经在用了）：
// 前端调刷新时，飞牛可能还没把新授权落库（宿主侧是异步的），这一刷拿回的仍是
// 旧列表，而且**紧接着又被缓存 30 秒**，比不刷还慢。所以改成「窗口期内不读缓存」：
// 授权后的几秒里，所有问「能访问哪些目录」的调用都真去问一次飞牛，等宿主落库
// 自然就对了。窗口过后恢复正常缓存，不会每次浏览目录都走一遍 Unix socket。
const (
	rootsStaleWindow = 20 * time.Second
	// 窗口期内真去问飞牛的节流间隔：目录树一次浏览可能连打好几个请求，
	// 不节流就变成「刷新授权 ≈ 每个请求一次 socket」。
	rootsStaleMinInterval = 1500 * time.Millisecond
)

var fnosRootsStale struct {
	sync.Mutex
	until time.Time
	last  time.Time
}

// NoteFnOSRootsStale 声明「授权可能刚变过，接下来一段时间别再信缓存」。
//
// 谁来调：前端在宿主选择器 / 授权动作返回后立刻打 POST /api/fnos/auth-refresh，
// 那个接口调它。**不要把它的窗口放大到常态**：窗口越长，目录浏览越多地落到
// 每次都要问 socket 的路径上。
func NoteFnOSRootsStale() {
	fnosRootsStale.Lock()
	fnosRootsStale.until = time.Now().Add(rootsStaleWindow)
	fnosRootsStale.last = time.Time{}
	fnosRootsStale.Unlock()
}

// fnosRootsStaleNow 报告现在是否处在「缓存不信任窗口」内，并按 1.5 秒节流。
func fnosRootsStaleNow() bool {
	fnosRootsStale.Lock()
	defer fnosRootsStale.Unlock()
	if fnosRootsStale.until.IsZero() || time.Now().After(fnosRootsStale.until) {
		return false
	}
	if time.Since(fnosRootsStale.last) < rootsStaleMinInterval {
		return false
	}
	fnosRootsStale.last = time.Now()
	return true
}

// fnosEnvRootsSource 读取系统注入的授权路径环境变量，供测试替换。
var fnosEnvRootsSource = func() []string {
	out := fnos.DataAccessiblePaths()
	out = append(out, fnos.DataSharePaths()...)
	return out
}

// fnosDataShareSource 读 config/resource 里 data-share 声明的路径，供测试替换。
// 这些是应用**自己**的数据目录，不算「用户授权了音乐目录」（见 FnOSGrantedRoots）。
var fnosDataShareSource = func() []string {
	return fnos.DataSharePaths()
}

// fnosAuthorizedRoots 汇总飞牛侧的授权目录（管理员共享 + 系统注入环境变量）。
//
// 这一层的取舍：**任何一路失败都不影响另一路**。用户最常见的抱怨是「我在应用设置
// 里授权了目录，曲率还是看不到」—— 那通常是因为某一路失败后整份列表被清空。
// 这里改为逐路收集，失败只记日志。
//
// 2026-10-31：去掉了「当前用户个人授权」这一路（用户明确本应用只给管理员自己用，
// 授权入口统一为管理员共享授权）。少一路查询也让「到底有没有授权」的判断更简单。
func fnosAuthorizedRoots() []string {
	roots := make([]string, 0, 8)
	roots = append(roots, fnosSharedRoots()...)
	roots = append(roots, fnosEnvRootsSource()...)
	return roots
}

// fnosGrantRoots 只收集「用户授权」，剔除 data-share 自用目录（见 FnOSGrantedRoots）。
//
// data-share 路径是应用自己的数据目录，系统把它注入进 TRIM_DATA_SHARE_PATHS
// 只是为了让应用能写自己的配置，不代表用户把音乐放在哪儿告诉了应用。
func fnosGrantRoots() []string {
	share := make(map[string]bool)
	for _, p := range fnosDataShareSource() {
		if p = strings.TrimSpace(p); p != "" {
			share[p] = true
		}
	}
	roots := make([]string, 0, 8)
	roots = append(roots, fnosSharedRoots()...)
	for _, p := range fnosEnvRootsSource() {
		if p = strings.TrimSpace(p); p != "" && !share[p] {
			roots = append(roots, p)
		}
	}
	return roots
}

// FnOSAuthorizedRoots 返回「飞牛侧授权目录」的归一化结果（管理员共享 ∪ 系统注入）。
//
// 与 AllowedRoots() 的区别：这里**不含**任何探测卷（/vol1..16、/media、/mnt），
// 也不受 FN_ALLOWED_ROOTS 影响。api 层用它来回答「到底有没有人授权过目录」，
// 这样即便用户把整个 /vol1 授权了，也不会因为「/vol1 看起来像内置卷」被误判成没授权。
//
// ⚠️ 它包含 config/resource 里 data-share 声明的路径（应用自己的数据目录），
// 所以**不能**用它判断「用户是否授权了音乐目录」—— 那件事要看 FnOSGrantedRoots()。
func FnOSAuthorizedRoots() []string {
	return normalizeRoots(fnosAuthorizedRoots())
}

// FnOSGrantedRoots 返回「用户/管理员真的授权给本应用的业务目录」（管理员共享 ∪
// TRIM_DATA_ACCESSIBLE_PATHS），**排除** data-share 自己的数据目录。
//
// 为什么必须分开（2026-10-31 真机事故）：`config/resource` 里声明的
// `data-share: yinshu-ai/data` 会被系统注入成 TRIM_DATA_SHARE_PATHS，而
// `/vol1/@appshare/yinshu-ai/data` 是曲率**自己**的配置/缓存目录，不是用户给的
// 音乐目录。把它算进「已授权」的结果是：auth-status 永远回 authorized=true，
// 前端提示条永远不出现 —— 用户明明一个音乐目录都没授权，却看不到任何引导，
// 只能自己撞 403。所以「要不要提示用户去授权」只看这一份。
//
// 路径守卫（AllowedRoots）仍然要包含 data-share，否则曲率写不了自己的数据目录。
func FnOSGrantedRoots() []string {
	return normalizeRoots(fnosGrantRoots())
}

func fnosSharedRoots() []string {
	fnosRootsCache.Lock()
	defer fnosRootsCache.Unlock()
	if time.Since(fnosRootsCache.at) < 30*time.Second && !fnosRootsStaleNow() {
		return fnosRootsCache.roots
	}
	roots, err := fnosRootsSource()
	if err != nil {
		// 取失败 ≠ 没有共享目录：以前静默 roots = nil，之后所有飞牛目录都会被判成
		// 「不在允许访问的目录范围内」，用户看到的是越权提示，真因（飞牛接口不可用 /
		// 令牌失效）一个字都没有。这里被 30 秒缓存天然限流，每次真拉失败记一条。
		applog.Default().AddFrom("nas", "warn",
			fmt.Sprintf("[路径守卫] 读取飞牛共享目录失败，本次不把它们加入允许根目录：%v", err))
		roots = nil
	}
	fnosRootsCache.at, fnosRootsCache.roots = time.Now(), roots
	return roots
}

// InvalidateFnOSRootsCache 让下一次 AllowedRoots() 重新去飞牛取授权目录。
//
// 用途：用户刚在页面上点了「授权目录」，紧接着界面就会刷新目录树。不主动失效的话
// 最长要等 30 秒才看得到，而用户把这段等待理解成「授权没生效」。
//
// ⚠️ 只失效一次是不够的（见 NoteFnOSRootsStale）：宿主侧的授权可能是异步落库的，
// 失效后立刻重取经常拿到的还是旧列表，而且**又被缓存 30 秒**。所以这里顺手打开
// 「缓存不信任窗口」，让接下来十几秒的每次询问都落到飞牛本体上。
func InvalidateFnOSRootsCache() {
	fnosRootsCache.Lock()
	fnosRootsCache.at = time.Time{}
	fnosRootsCache.roots = nil
	fnosRootsCache.Unlock()
	NoteFnOSRootsStale()
}

// normalizeRoots 去空、取绝对路径、解析软链接、要求真实存在的目录，并按顺序去重。
func normalizeRoots(candidates []string) []string {
	roots := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" || !filepath.IsAbs(c) {
			continue
		}
		// root 本身也可能是软链接（如 /media -> /mnt/media），必须解析到真实路径，
		// 否则 ResolveSafePath 解析出的真实路径无法与 root 匹配。
		real, err := filepath.EvalSymlinks(filepath.Clean(c))
		if err != nil {
			continue // 不存在则忽略
		}
		real = filepath.Clean(real)
		if !filepath.IsAbs(real) || seen[real] {
			continue
		}
		fi, err := os.Stat(real)
		if err != nil || !fi.IsDir() {
			continue
		}
		seen[real] = true
		roots = append(roots, real)
	}
	return roots
}

// ResolveSafePath 规范化并校验请求路径，返回解析软链接后的真实绝对路径。
//
// 校验步骤（任一步失败都返回错误，绝不返回可用于访问文件系统的路径）：
//  1. 非空绝对路径，且不含 NUL；
//  2. 先对 Clean 后的路径做**词法**归属判断（不存在的路径也会被拦下，不泄露是否存在）；
//  3. 用 EvalSymlinks 解析软链接，防止通过软链逃逸出根目录；
//  4. 对真实路径再次做归属判断。
//
// AllowedRoots() 为空时拒绝一切（fail closed）。
func ResolveSafePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrPathEmpty
	}
	if strings.ContainsRune(raw, 0) {
		return "", ErrPathInvalid
	}
	if !filepath.IsAbs(raw) {
		return "", ErrPathNotAbsolute
	}

	clean := filepath.Clean(raw)
	if !filepath.IsAbs(clean) {
		return "", ErrPathNotAbsolute
	}

	roots := AllowedRoots()
	if len(roots) == 0 {
		return "", ErrNoAllowedRoots
	}
	// 词法预检：/vol1x 不会命中 root /vol1（按路径分段比较）。
	if !withinAnyRoot(clean, roots) {
		return "", fmt.Errorf("%w: %s", ErrPathForbidden, clean)
	}

	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrPathNotFound, clean)
	}
	real = filepath.Clean(real)
	if !filepath.IsAbs(real) {
		return "", ErrPathInvalid
	}
	if !withinAnyRoot(real, roots) {
		return "", fmt.Errorf("%w: %s", ErrPathForbidden, clean)
	}
	return real, nil
}

// withinAnyRoot 判断 target 是否等于某个 root 或位于其下。
func withinAnyRoot(target string, roots []string) bool {
	for _, root := range roots {
		if isWithinRoot(target, root) {
			return true
		}
	}
	return false
}

// isWithinRoot 用 filepath.Rel 做按段比较：/vol10 不会被 root /vol1 匹配。
func isWithinRoot(target, root string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	switch {
	case rel == ".":
		return true
	case rel == "..":
		return false
	case strings.HasPrefix(rel, ".."+string(filepath.Separator)):
		return false
	case filepath.IsAbs(rel):
		return false
	default:
		return true
	}
}

// isResolvableDir 判断路径是否为位于允许根目录内的已存在目录。
func isResolvableDir(p string) bool {
	real, err := ResolveSafePath(p)
	if err != nil {
		return false
	}
	fi, err := os.Stat(real)
	return err == nil && fi.IsDir()
}

// pathAllowedAfterResolve 判断已存在路径解析软链接后是否仍位于允许根目录内。
// 用于扫描时跳过指向根目录之外的软链接条目。
func pathAllowedAfterResolve(p string) bool {
	roots := AllowedRoots()
	if len(roots) == 0 {
		return false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	return withinAnyRoot(filepath.Clean(real), roots)
}

// writePathError 按现有响应风格输出路径校验错误：{"code":..., "message":...}。
// 路径参数本身不合法（空/相对/非法字符）返回 400，越权或未配置根目录返回 403，
// 路径不存在返回 404。
func writePathError(w http.ResponseWriter, err error) {
	status, code := http.StatusForbidden, 403
	switch {
	case errors.Is(err, ErrPathEmpty), errors.Is(err, ErrPathNotAbsolute), errors.Is(err, ErrPathInvalid):
		status, code = http.StatusBadRequest, 400
	case errors.Is(err, ErrPathNotFound):
		status, code = http.StatusNotFound, 404
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    code,
		"message": err.Error(),
	})
}
