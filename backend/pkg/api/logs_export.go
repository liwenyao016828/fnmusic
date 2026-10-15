package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/namefmt"
)

// handleLogsExport 把「运行日志 + 环境信息 + 脱敏后的配置」打成一个 zip 下载。
//
// 为什么要有它：日志**只存在内存里**（见 pkg/applog 的设计），重启即清空；用户来报
// 问题时我们能拿到的往往只有他手抄的一行。有了这个包，一次点击就能把「这次失败前后
// 发生了什么」整包带走，而且环境信息与配置一起进去 —— 少了配置，很多问题从日志上
// 根本看不出成因（例如音质档位、命名格式、缓存目录）。
//
// 这个功能是照着参考实现（fnmusic-ext WebUI 的「一键导出运行日志」）做的：
// 那一条 release 里最不起眼、但对「用户愿意报问题」影响最大的就是它。
func (s *Server) handleLogsExport(w http.ResponseWriter, r *http.Request) {
	// 先全部在内存里拼好再写响应头：拼到一半失败的话，已经发出去的 200 会让
	// 浏览器存下一个半截 zip（下不下来、也看不出错）。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	if err := writeZipText(zw, "app.log", s.buildLogText()); err != nil {
		zipFail(w, err)
		return
	}
	if err := writeZipText(zw, "info.txt", s.buildInfoText()); err != nil {
		zipFail(w, err)
		return
	}
	if err := writeZipText(zw, "config.json", s.buildRedactedConfigJSON()); err != nil {
		zipFail(w, err)
		return
	}
	if err := zw.Close(); err != nil {
		zipFail(w, err)
		return
	}

	name := exportFileName(time.Now())
	w.Header().Set("Content-Type", "application/zip")
	// filename* 用 RFC 5987 写法：文件名是纯 ASCII，但这么写能避免中文环境下
	// 某些客户端把参数截断（参考实现那一条 500;http error 就是栽在这类编码问题上）。
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"; filename*=UTF-8''`+name)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

func zipFail(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    500,
		"message": "打包日志失败：" + err.Error(),
	})
}

func writeZipText(zw *zip.Writer, name, body string) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(body))
	return err
}

// buildLogText 把内存环形缓冲渲染成一行一条的文本。
//
// 用**文本**而不是原样的 JSON：用户会自己打开看一眼再发给我们，纯文本能读；
// 机器要解析的话这个格式也够（时间 级别 来源 消息）。
func (s *Server) buildLogText() string {
	entries := applog.Default().Recent(2000)
	var b strings.Builder
	fmt.Fprintf(&b, "# 曲率运行日志（内存缓冲，共 %d 条；重启应用即清空）\n", applog.Default().Len())
	fmt.Fprintf(&b, "# 导出时间：%s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "# 本次导出：%d 条\n\n", len(entries))
	if len(entries) == 0 {
		b.WriteString("（缓冲里没有日志）\n")
		return b.String()
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\t%-5s\t%-8s\t%s\n", e.At, e.Level, e.Source, e.Message)
	}
	return b.String()
}

// buildInfoText 是本机环境快照。刻意**只放排障真正用得到的那些**：
// 版本 / 运行时 / 目录 / 鉴权模式；不放机器名、内网 IP、序列号之类与问题无关的东西。
func (s *Server) buildInfoText() string {
	cfg := s.cfgMgr.Get()
	var b strings.Builder
	fmt.Fprintf(&b, "应用：曲率 qulv (yinshu-ai)\n")
	fmt.Fprintf(&b, "版本：%s\n", CurrentVersion)
	fmt.Fprintf(&b, "Go：%s\n", runtime.Version())
	fmt.Fprintf(&b, "平台：%s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "启动时长：%s\n", time.Since(s.startedAt).Round(time.Second))
	fmt.Fprintf(&b, "导出时间：%s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "\n数据目录：%s\n", s.cfgMgr.DataDir())
	fmt.Fprintf(&b, "下载目录：%s\n", cfg.DownloadDir)
	if d := cfg.CacheDirPath(); d != "" {
		fmt.Fprintf(&b, "缓存目录：%s（已自定义）\n", d)
	} else {
		fmt.Fprintf(&b, "缓存目录：（默认位置）\n")
	}
	fmt.Fprintf(&b, "文件名命名格式：%s（实际生效：%s）\n", cfg.FilenameFormat, namefmt.Current())
	fmt.Fprintf(&b, "鉴权模式：%s\n", s.effectiveAuthMode())
	fmt.Fprintf(&b, "\n配置文件：%s\n", filepath.Join(s.cfgMgr.DataDir(), "config.json"))
	return b.String()
}

// sensitiveKeyParts 是导出配置时**抹掉值**的键名规则（大小写不敏感的子串匹配）。
//
// 用键名规则而不是逐个写字段：以后新增一个带凭据的设置项，这里不用改 ——
// 漏抹一个令牌比少导出一样设置严重得多。`script` 也在列表里，因为 LX 音源脚本
// 里常常内嵌着用户自己的音乐账号 cookie。
var sensitiveKeyParts = []string{
	"token", "secret", "password", "passwd", "cookie",
	"credential", "authorization", "apikey", "api_key", "script",
}

const redactedPlaceholder = "*** 已抹除（导出日志时不含凭据） ***"

func isSensitiveKey(k string) bool {
	low := strings.ToLower(k)
	for _, p := range sensitiveKeyParts {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// redactValue 递归把敏感键的值换成占位符。
func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if isSensitiveKey(k) {
				if s, ok := val.(string); ok && s == "" {
					out[k] = ""
					continue
				}
				out[k] = redactedPlaceholder
				continue
			}
			out[k] = redactValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = redactValue(t[i])
		}
		return out
	default:
		return v
	}
}

// buildRedactedConfigJSON 导出**脱敏后**的配置。
//
// 为什么不是直接贴 config.json：里面有飞牛令牌、音源服务 token、用户音源脚本
// （常带账号 cookie）。这些东西一旦进了聊天窗口就收不回来了 —— 而排障真正需要的
// 只是「档位、目录、开关」这些非敏感字段。
func (s *Server) buildRedactedConfigJSON() string {
	raw, err := json.Marshal(s.cfgMgr.Get())
	if err != nil {
		return `{"error":"配置序列化失败：` + err.Error() + `"}`
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return string(raw)
	}
	out := redactValue(generic)
	pretty, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return string(raw)
	}
	header := "// 凭据类字段（" + strings.Join(sensitiveKeyParts, " / ") +
		"）已抹除。若排查仍需要原值，请自行脱敏后提供。\n"
	return header + string(pretty) + "\n"
}

// exportFileName 让包内其它地方（测试）也能拼出同一个文件名形状。
func exportFileName(now time.Time) string {
	return "qulv-logs-" + now.Format("20060102-150405") + ".zip"
}
