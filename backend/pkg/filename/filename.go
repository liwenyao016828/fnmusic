// Package filename 统一负责「把任意字符串变成能安全落进 NAS 的文件名」。
//
// 为什么单独抽一个包（2026-10-24）：
//   - 下载落盘要用它，库体检（pkg/nas.Health）也要用它来算出「这个名字应该被改成什么」。
//     两处各写一份必然漂移 —— 体检说能修、修完还是非法，用户会以为功能坏了。
//   - 用户报过「飞牛文件管理和官方音乐 App 打开音乐文件夹显示未知错误」。
//     音源标签里的 GBK 脏字节能被 Go 原样写进文件名，而 fnOS 的文件/媒体接口
//     按 UTF-8 处理名字，遇到非法字节整层目录直接报错（不是某一首歌显示不出来）。
//     所以落盘前必须保证名字是合法 UTF-8，且不含控制字符。
package filename

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxBytes 文件名（不含扩展名）的字节上限，避免 ENAMETOOLONG。
//
// 取 180 而不是 255：扩展名与 " - " 连接符还要占字节，且下载过程会加临时后缀
// （.part、.crdownload），留出余量才不会在临时文件那一步撞上限。
const maxBytes = 180

// illegal 是跨平台都不能出现在文件名里的字符：Windows 的 \/:*?"<>| 加上换行类空白。
// 这些字符一旦落盘，Windows 客户端打不开、SMB 共享里也会变成乱七八糟的样子。
var illegal = regexp.MustCompile(`[\\/:*?"<>|\r\n\t]`)

// control 是其余 C0 控制字符与 DEL。它们肉眼不可见，
// 却是 fnOS 接口与 JSON 序列化的经典杀手。
var control = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// reserved 是 Windows 保留设备名：即使带扩展名也不能作为文件名。
var reserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// Sanitize 返回一个可安全落盘的名字（不含扩展名）。
//
// 处理顺序是有讲究的：
//  1. 非法字符先换成下划线（否则后面按字节截断可能切在半个 UTF-8 字符上）；
//  2. 非法 UTF-8 字节换成下划线 —— 这是本轮新增的关键一步，见包注释；
//  3. 去控制字符、去首尾空白；
//  4. 去首尾的点与空格（"." 结尾在 Windows/SMB 上是非法名，也容易被当成隐藏文件）；
//  5. 空名兜底成「未知」；
//  6. 按字节上限截断（截断点回退到 UTF-8 字符起始字节，避免切出半个汉字）。
func Sanitize(s string) string {
	s = illegal.ReplaceAllString(s, "_")
	// 非法编码用下划线而不是 U+FFFD：问号方块在文件名里既难看又仍可能被某些
	// 客户端当作异常字符，下划线与常规清洗结果一致。
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "_")
	}
	s = control.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	s = trimDotsAndSpaces(s)
	if s == "" {
		return "未知"
	}
	if reserved[strings.ToUpper(s)] {
		// 加后缀而不是直接改名，保留可读性（CON → CON_ 比 未知 更好找）
		s += "_"
	}
	if len(s) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = trimDotsAndSpaces(strings.TrimSpace(s[:cut]))
		if s == "" {
			return "未知"
		}
	}
	return s
}

// trimDotsAndSpaces 去掉首尾的点与空白。抽出来是因为两处都要用（正常清洗 + 截断后）。
func trimDotsAndSpaces(s string) string {
	return strings.Trim(s, ". ")
}

// IsClean 判断一个已存在的名字是否本来就合规。
// 库体检用它区分「要修的」与「看着奇怪但没问题」的条目。
func IsClean(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	if illegal.MatchString(name) || control.MatchString(name) {
		return false
	}
	if name != trimDotsAndSpaces(name) {
		return false
	}
	stem := name
	if i := strings.LastIndexByte(stem, '.'); i > 0 {
		stem = stem[:i]
	}
	if reserved[strings.ToUpper(stem)] {
		return false
	}
	return true
}
