package tags

// 让「写完标签/歌词」这件事对飞牛音乐可见。
//
// 背景（见 docs/飞牛刮削适配调研.md）：
// 飞牛音乐按 **「文件路径 + mtime / size」** 做增量扫描指纹。
// 只写同名 .lrc（或只改内嵌标签但恰好 size 没变）时，音频文件在飞牛看来
// 「没有任何变化」，于是**不会重新读取** ——
// 用户看到的就是「我明明改了歌词，飞牛里还是旧的」。
//
// 参考实现（轻乐集 v1.2.18）踩过同一个坑，做法是写完自动 touch。
// 这里保持一致：**只改时间戳，不动任何文件内容**。

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sidecarExtensions 与音频同名的附属文件扩展名（歌词 / 封面）。
// 它们同样靠时间戳被飞牛判断「要不要重读」，所以要一起推。
var sidecarExtensions = []string{".lrc", ".txt", ".jpg", ".jpeg", ".png", ".webp"}

// TouchForRescan 把音频文件与同名附属文件的修改时间刷新到「当下」。
//
// 在写完标签或歌词之后调用。不存在的附属文件会被跳过（不会凭空造文件）。
func TouchForRescan(audioPath string) {
	if strings.TrimSpace(audioPath) == "" {
		return
	}
	now := time.Now()
	touchFile(audioPath, now)

	base := strings.TrimSuffix(audioPath, filepath.Ext(audioPath))
	for _, ext := range sidecarExtensions {
		touchFile(base+ext, now)
	}
}

func touchFile(path string, t time.Time) {
	if _, err := os.Stat(path); err != nil {
		return // 不存在就跳过
	}
	_ = os.Chtimes(path, t, t)
}
