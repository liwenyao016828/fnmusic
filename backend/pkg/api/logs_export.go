package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fn-lx-player/pkg/applog"
	"fn-lx-player/pkg/namefmt"
)

// handleLogsExport 把「运行日志 + 环境信息 + 脱敏后的配置」打成一个 zip 下载。
//
// 为什么要有它：内存缓冲与落盘日志分开保存，用户来报问题时，一次点击就能把
// 最近的运行现场、当前持久日志（含上一代轮转文件）和脱敏配置整包带走。
// 环境信息与配置一起进去 —— 少了配置，很多问题从日志上根本看不出成因
// （例如音质档位、命名格式、缓存目录）。
//
// 这个功能是照着参考实现（fnmusic-ext WebUI 的「一键导出运行日志」）做的：
// 那一条 release 里最不起眼、但对「用户愿意报问题」影响最大的就是它。
//
// 包内容：
//
//	app.log            内存环形缓冲（最近 2000 条，重启即清空）
//	qulv.log           当前落盘文件（有界读取，超 20MB 截断）
//	qulv.log.1         上一代轮转文件（同上，存在才带）
//	info.txt           环境快照 + 本次导出说明（含哪些文件没读到、为什么）
//	config.json        脱敏后的配置
//
// 只带内存缓冲是不够的：用户往往是「跑了几天才出问题」，那时缓冲早被后来的
// 日志顶掉了，真正有价值的正是 qulv.log 里的历史。
func (s *Server) handleLogsExport(w http.ResponseWriter, r *http.Request) {
	// 先全部在内存里拼好再写响应头：拼到一半失败的话，已经发出去的 200 会让
	// 浏览器存下一个半截 zip（下不下来、也看不出错）。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	if err := writeZipText(zw, "app.log", s.buildLogText()); err != nil {
		zipFail(w, err)
		return
	}
	// 落盘文件先写，这样 info.txt 能如实记录每个文件的写入结果。
	diskNotes := s.writeDiskLogs(zw)
	if err := writeZipText(zw, "info.txt", s.buildInfoText(diskNotes)); err != nil {
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

// writeZipText 把一段文本写成 zip 里的一个条目。
func writeZipText(zw *zip.Writer, name, body string) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(body))
	return err
}

// diskLogNote 记录一个落盘日志文件的打包结果，供 info.txt 如实交代。
type diskLogNote struct {
	Name      string // 包内条目名
	Path      string // 源文件绝对路径
	Written   int64  // 实际写出的字节数
	Truncated bool   // 源文件超过单文件上限，只带了前 20MB
	Err       string // 读取/写出失败原因；空表示成功
}

// writeDiskLogs 把当前与上一代落盘日志写进 zip，返回每个文件的结果。
//
// 单个文件失败**不中断**：少一份持久日志只是少一点现场，整个导出包 500
// 用户就什么都拿不到。失败原因经 info.txt 告知，用户能自己看出「包里为什么
// 没有 qulv.log」—— 例如设置了别的目录、目录被删、或磁盘写满。
func (s *Server) writeDiskLogs(zw *zip.Writer) []diskLogNote {
	files := applog.DiskFiles()
	if len(files) == 0 {
		return nil
	}
	notes := make([]diskLogNote, 0, len(files))
	for i, p := range files {
		name := "qulv.log"
		if i > 0 {
			name = "qulv.log." + strconv.Itoa(i) // 与磁盘上的 .1 后缀一致
		}
		n, truncated, err := applog.AddDiskFile(zw, p, name)
		note := diskLogNote{Name: name, Path: p, Written: n, Truncated: truncated}
		if err != nil {
			note.Err = err.Error()
		}
		notes = append(notes, note)
	}
	return notes
}

// buildLogText 把内存环形缓冲渲染成一行一条的文本。
//
// 用**文本**而不是原样的 JSON：用户会自己打开看一眼再发给我们，纯文本能读；
// 机器要解析的话这个格式也够（时间 级别 来源 消息）。
//
// 时间列用 RFC3339（带 offset，通常是 Z）：旧实现写的是 e.At（只有 HH:MM:SS），
// 既没有日期也没有时区，导出给开发看时无法还原绝对时刻 —— NAS 是 UTC，
// 用户在中国，光看「21:30:00」根本分不清指的是哪一天、哪个时区的 21:30。
func (s *Server) buildLogText() string {
	entries := applog.Default().Recent(2000)
	var b strings.Builder
	fmt.Fprintf(&b, "# 曲率运行日志（内存缓冲，共 %d 条；重启应用即清空）\n", applog.Default().Len())
	fmt.Fprintf(&b, "# 导出时间：%s（服务器时区 %s）\n", time.Now().Format(time.RFC3339), time.Now().Location())
	fmt.Fprintf(&b, "# 本次导出：%d 条\n", len(entries))
	fmt.Fprintf(&b, "# 时间列格式：RFC3339，带时区偏移；完整持久日志见包内 qulv.log\n\n")
	if len(entries) == 0 {
		b.WriteString("（缓冲里没有日志）\n")
		return b.String()
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\t%-5s\t%-10s\t%s\n", entryStamp(e), e.Level, e.Source, e.Message)
	}
	return b.String()
}

// entryStamp 取条目里带时区的绝对时刻；老数据没有 Time 时退回 At（有胜于无）。
func entryStamp(e applog.Entry) string {
	if e.Time != "" {
		return e.Time
	}
	return e.At
}

// offsetString 把 Zone() 返回的秒偏移渲染成 "UTC+08:00" 形式（0 → "UTC+00:00"）。
func offsetString(offset int) string {
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	h, m := offset/3600, (offset%3600)/60
	return fmt.Sprintf("UTC%s%02d:%02d", sign, h, m)
}

// buildInfoText 是本机环境快照。刻意**只放排障真正用得到的那些**：
// 版本 / 运行时 / 目录 / 鉴权模式；不放机器名、内网 IP、序列号之类与问题无关的东西。
//
// diskNotes 来自 writeDiskLogs：把「打包持久日志的结果」也写进来，
// 用户与开发都能一眼看出包里缺了哪份文件、为什么缺。
func (s *Server) buildInfoText(diskNotes []diskLogNote) string {
	cfg := s.cfgMgr.Get()
	var b strings.Builder
	fmt.Fprintf(&b, "应用：曲率 qulv (yinshu-ai)\n")
	fmt.Fprintf(&b, "版本：%s\n", CurrentVersion)
	fmt.Fprintf(&b, "Go：%s\n", runtime.Version())
	fmt.Fprintf(&b, "平台：%s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "启动时长：%s\n", time.Since(s.startedAt).Round(time.Second))
	fmt.Fprintf(&b, "导出时间：%s\n", time.Now().Format(time.RFC3339))
	// 服务器时区必须写清楚：日志里的时间是绝对时刻，排障的人要能和自己的本地时间对上。
	// time.Now().Location().String() 在纯 UTC 环境给 "UTC"、装了 tzdata 的环境给
	// "Local" —— "Local" 对读者毫无用处。Zone() 的缩写 + 数字偏移
	//（如 "UTC (UTC+00:00)"）任何时区的读者都能直接换算。
	if zone, offset := time.Now().Zone(); zone != "" {
		fmt.Fprintf(&b, "服务器时区：%s (%s)\n", zone, offsetString(offset))
	} else {
		fmt.Fprintf(&b, "服务器时区：%s\n", time.Now().Location())
	}
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
	b.WriteString(buildDiskSection(applog.Status(), diskNotes))
	return b.String()
}

// buildDiskSection 描述落盘日志的现状与打包结果。
//
// 「写盘失败」必须能被看见：落盘默认开启，磁盘满或目录被删时如果只在代码里
// 吞掉错误，用户会以为日志一直在记，等真出事才发现文件几个月没更新。
func buildDiskSection(st applog.DiskStatus, notes []diskLogNote) string {
	var b strings.Builder
	b.WriteString("\n# 持久日志（落盘）\n")
	if !st.Enabled {
		b.WriteString("状态：未启用或已失效（未在导出包中携带 qulv.log）\n")
	} else {
		fmt.Fprintf(&b, "文件：%s\n", st.Path)
		fmt.Fprintf(&b, "应用视角大小：%d 字节\n", st.Size)
	}
	if st.LastErr != "" {
		fmt.Fprintf(&b, "最近一次写盘错误：%s\n", st.LastErr)
	}
	if len(notes) == 0 {
		b.WriteString("打包结果：未找到可导出的持久日志文件\n")
		return b.String()
	}
	b.WriteString("打包结果：\n")
	for _, n := range notes {
		if n.Err != "" {
			fmt.Fprintf(&b, "  · %s 读取失败（%s）：%s\n", n.Name, n.Path, n.Err)
			continue
		}
		extra := ""
		if n.Truncated {
			extra = "，超过单文件上限已截断"
		}
		fmt.Fprintf(&b, "  · %s 已包含 %d 字节%s\n", n.Name, n.Written, extra)
	}
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
