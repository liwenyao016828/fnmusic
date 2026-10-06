package nas

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxTextBytes 读取文本文件的体积上限。音源脚本都是几十 KB，给 2MB 已经很宽松；
// 不设上限的话，一个误点的路径就能把内存打满。
const maxTextBytes = 2 << 20

// textFileExts 允许读取的扩展名白名单。
//
// ⚠️ **不加白名单就等于开了「任意文件读取」**：路径守卫只保证"在允许的根目录内"，
// 而根目录里同样躺着配置、密钥、数据库。这个接口的用途只有一个 —— 从 NAS 里挑一个音源脚本，
// 所以只放文本类扩展名。
var textFileExts = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".json": true, ".txt": true,
}

// ReadTextFile 读取允许根目录内的一个文本文件（用于「从 NAS 选择音源脚本」）。
//
//	GET /api/nas/read?path=/vol1/xxx/source.js
//
// 三道安全约束，缺一不可：
//  1. 路径必须过 `ResolveSafePath` —— 本包唯一的路径安全入口（越权 403 / 参数错 400 / 不存在 404）；
//  2. 扩展名白名单（见 textFileExts）；
//  3. 体积上限（见 maxTextBytes）。
//
// 另外拒绝非 UTF-8 内容：读出来的东西要直接当脚本正文提交，二进制只会变成乱码。
func ReadTextFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath == "" {
		writePathError(w, ErrPathEmpty)
		return
	}

	realPath, err := ResolveSafePath(rawPath)
	if err != nil {
		writePathError(w, err)
		return
	}

	ext := strings.ToLower(filepath.Ext(realPath))
	if !textFileExts[ext] {
		writeJSONError(w, http.StatusBadRequest, 400,
			"只允许读取文本类文件（.js / .mjs / .cjs / .json / .txt）")
		return
	}

	fi, err := os.Stat(realPath)
	if err != nil || fi.IsDir() {
		writePathError(w, ErrPathNotFound)
		return
	}
	if fi.Size() > maxTextBytes {
		writeJSONError(w, http.StatusBadRequest, 400,
			"文件太大（超过 2MB），音源脚本不会这么大 —— 确认选对文件了吗？")
		return
	}

	raw, err := os.ReadFile(realPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, 500, "读取失败："+err.Error())
		return
	}
	if !utf8.Valid(raw) {
		writeJSONError(w, http.StatusBadRequest, 400, "这不是文本文件（内容不是 UTF-8）")
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    200,
		"message": "ok",
		"data": map[string]interface{}{
			"path":    filepath.ToSlash(realPath),
			"name":    filepath.Base(realPath),
			"size":    fi.Size(),
			"content": string(raw),
		},
	})
}

// writeJSONError 统一的 JSON 错误体（与 writePathError 同一形状：{"code","message"}）。
func writeJSONError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    code,
		"message": message,
	})
}
