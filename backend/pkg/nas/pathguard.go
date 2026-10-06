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
	ErrNoAllowedRoots = errors.New("未配置可访问的根目录，已拒绝访问")
	// ErrPathNotFound 路径不存在或不可访问（含软链接悬空）
	ErrPathNotFound = errors.New("路径不存在或不可访问")
	// ErrPathNotAudio 请求的文件不是受支持的音频类型
	ErrPathNotAudio = errors.New("仅支持音频文件，已拒绝访问")
)

// AllowedRoots 返回允许访问的根目录列表（均为已解析软链接、确实存在的目录）。
//
// 顺序：
//  1. 飞牛开放 API 返回的管理员授权目录（fnOS 正规权限模型，优先）；
//  2. 环境变量 FN_ALLOWED_ROOTS（冒号分隔）设置了（即使为空串）时，完全覆盖内置列表；
//  3. 否则使用 getAvailableVolumes()（/vol1..16、/media、/mnt、/home）。
//
// 不存在的条目会被忽略；返回空切片表示不允许访问任何路径（fail closed）。
// 每次调用都重新探测，以便 NAS 卷在运行时挂载/卸载后立即生效（代价仅为少量 stat）；
// 飞牛授权目录需走 socket，故单独做 30 秒缓存。
func AllowedRoots() []string {
	if roots := fnosSharedRoots(); len(roots) > 0 {
		return normalizeRoots(roots)
	}
	if envRaw, envSet := os.LookupEnv(allowedRootsEnv); envSet {
		return normalizeRoots(strings.Split(envRaw, string(os.PathListSeparator)))
	}
	return normalizeRoots(getAvailableVolumes())
}

// fnosSharedRoots 查询飞牛管理员授权给本应用的目录。
// 非飞牛环境或调用失败时返回 nil，调用方自然回退到环境变量/卷探测。
var fnosRootsCache struct {
	sync.Mutex
	at    time.Time
	roots []string
}

// fnosRootsSource 供测试替换。默认语义：飞牛不可用（没令牌/没 socket）就当作没有共享目录
// —— 这是**正常**情况，不记日志；真去拉了但失败才是要说的那件事。
var fnosRootsSource = func() ([]string, error) {
	if !fnos.Available() {
		return nil, nil
	}
	return fnos.SharedFolders()
}

func fnosSharedRoots() []string {
	fnosRootsCache.Lock()
	defer fnosRootsCache.Unlock()
	if time.Since(fnosRootsCache.at) < 30*time.Second {
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
