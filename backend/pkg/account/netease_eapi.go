package account

// 网易云 eapi 加密通道。
//
// 为什么走 eapi（2026-09-19 两轮真机验证的结论）：
//   - 明文 /api/ 通道申请的 unikey 会被服务端判为**异常渠道** ——
//     轮询直接返回「请切换其他登录方式或升级新版本再试用」，与 type 值无关。
//   - 社区活跃库 @neteasecloudmusicapienhanced/api（4.40.1）默认
//     APP_CONF.encrypt=true → 全部走 eapi；fnmusic-flow 的网易云能登录就是靠它。
//
// 算法与社区库 util/crypto.js 的 eapi() / util/request.js 的 header 构造逐字段对齐：
//   text   = JSON(业务参数 + header 对象)
//   digest = md5("nobody" + uri + "use" + text + "md5forencrypt")
//   params = AES-128-ECB(pkcs7(uri + "-36cd479b6b5-" + text + "-36cd479b6b5-" + digest)) hex
//   POST {eapiBase}/eapi/<uri 去掉 /api 前缀>，表单体只有 params=
//   Cookie 头由 header 对象拼成（不是普通 Cookie jar —— eapi 靠 header 串会话）

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	eapiKey       = "e82ckenh8dichen8"
	eapiSeparator = "-36cd479b6b5-"
	// 与社区库 chooseUserAgent('api','iphone') 一致
	eapiUA = "NeteaseMusic 9.0.90/5038 (iPhone; iOS 16.2; zh_CN)"
	// register_anonimous 的设备号混淆 key
	anonXorKey = "3go8&$8*3*3h0k(2)2"
)

// neteaseEapiBase 为 eapi 网关；声明为变量以便测试注入。
var neteaseEapiBase = "https://interfacepc.music.163.com"

// eapiEncryptParams 生成 params 值（hex）。uri 用 /api/... 原始路径。
func eapiEncryptParams(uri, text string) string {
	digest := fmt.Sprintf("%x", md5.Sum([]byte("nobody"+uri+"use"+text+"md5forencrypt")))
	data := uri + eapiSeparator + text + eapiSeparator + digest
	return hex.EncodeToString(aesECBEncryptPKCS7([]byte(eapiKey), []byte(data)))
}

func aesECBEncryptPKCS7(key, plain []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		// eapiKey 固定 16 字节，不可能走到这里
		panic("account: eapiKey 长度非法: " + err.Error())
	}
	bs := block.BlockSize()
	padded := plain
	if pad := bs - len(plain)%bs; pad > 0 {
		padded = append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	}
	out := make([]byte, len(padded))
	for start := 0; start < len(padded); start += bs {
		block.Encrypt(out[start:start+bs], padded[start:start+bs])
	}
	return out
}

// eapiDeviceID 生成 52 位大写十六进制设备号（与社区库 generateDeviceId 同型）。
func eapiDeviceID() string {
	buf := make([]byte, 26)
	if _, err := rand.Read(buf); err != nil {
		return strings.ToUpper(strconv.FormatInt(time.Now().UnixNano(), 36))
	}
	return strings.ToUpper(hex.EncodeToString(buf))
}

func eapiRequestID() string {
	var n [2]byte
	_, _ = rand.Read(n[:])
	return fmt.Sprintf("%d_%04d", time.Now().UnixMilli(), (int(n[0])<<8|int(n[1]))%10000)
}

// eapiHeader 构造 eapi 的 header 参数（pc 形态，逐字段对照 request.js）。
// cookies 里若有 MUSIC_U/MUSIC_A 也要并入（服务端按它认身份）。
func eapiHeader(deviceID string, cookies map[string]string) map[string]string {
	h := map[string]string{
		"os":          "pc",
		"appver":      "3.1.17.204416",
		"osver":       "Microsoft-Windows-10-Professional-build-19045-64bit",
		"channel":     "netease",
		"deviceId":    deviceID,
		"versioncode": "140",
		"mobilename":  "",
		"buildver":    strconv.FormatInt(time.Now().Unix(), 10),
		"resolution":  "1920x1080",
		"__csrf":      "",
		"requestId":   eapiRequestID(),
	}
	if v := cookies["MUSIC_U"]; v != "" {
		h["MUSIC_U"] = v
	}
	if v := cookies["MUSIC_A"]; v != "" {
		h["MUSIC_A"] = v
	}
	return h
}

// eapiHeaderCookie 把 header 对象拼成 Cookie 请求头（createHeaderCookie 等价物）。
func eapiHeaderCookie(h map[string]string) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(h[k]))
	}
	return strings.Join(parts, "; ")
}

// eapiPost 发一次 eapi 请求。返回解析后的 JSON、本响应 Set-Cookie 对、合并后的 Cookie jar。
func eapiPost(uri string, data map[string]any, deviceID string, jar map[string]string) (map[string]any, []string, map[string]string, error) {
	hdr := eapiHeader(deviceID, jar)
	payload := make(map[string]any, len(data)+1)
	for k, v := range data {
		payload[k] = v
	}
	payload["header"] = hdr
	text, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, jar, err
	}
	form := url.Values{"params": {eapiEncryptParams(uri, string(text))}}.Encode()

	req, err := http.NewRequest(http.MethodPost,
		neteaseEapiBase+"/eapi/"+strings.TrimPrefix(uri, "/api/"), strings.NewReader(form))
	if err != nil {
		return nil, nil, jar, err
	}
	req.Header.Set("User-Agent", eapiUA)
	req.Header.Set("Referer", "https://music.163.com/")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", eapiHeaderCookie(hdr))

	resp, err := neteaseClient.Do(req)
	if err != nil {
		return nil, nil, jar, fmt.Errorf("请求网易 eapi 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, neteaseMaxRaw))
	if err != nil {
		return nil, nil, jar, fmt.Errorf("读取网易 eapi 响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, jar, fmt.Errorf("网易 eapi 返回 HTTP %d", resp.StatusCode)
	}
	pairs := setCookiePairs(resp)
	merged := mergeResponseCookies(resp, jar)

	var out map[string]any
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, string(raw))
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, nil, merged, fmt.Errorf("解析网易 eapi 响应失败: %w", err)
	}
	return out, pairs, merged, nil
}

// ── 游客身份（MUSIC_A）──
//
// 社区库启动时调 /api/register/anonimous 拿匿名 token，请求里没 MUSIC_U 就带 MUSIC_A。
// 我们进程级懒取一次；失败不拦（继续裸请求，让真机日志暴露服务端态度）。

var (
	neteaseAnonMu     sync.Mutex
	neteaseAnonDevice string
	neteaseAnonJar    map[string]string
	neteaseAnonTried  bool
)

// neteaseAnonIdentity 返回（deviceId, 含 MUSIC_A 的 jar，可能为空）。
func neteaseAnonIdentity() (string, map[string]string) {
	neteaseAnonMu.Lock()
	defer neteaseAnonMu.Unlock()
	if neteaseAnonTried {
		return neteaseAnonDevice, neteaseAnonJar
	}
	neteaseAnonTried = true
	neteaseAnonDevice = eapiDeviceID()
	out, _, jar, err := eapiPost("/api/register/anonimous",
		map[string]any{"username": anonUsername(neteaseAnonDevice)}, neteaseAnonDevice, nil)
	if err != nil {
		return neteaseAnonDevice, neteaseAnonJar
	}
	// 响应体也可能带 cookie 字段（老形态）
	if jar["MUSIC_A"] == "" {
		if bodyCookie, ok := out["cookie"].(string); ok && strings.Contains(bodyCookie, "MUSIC_A=") {
			jar = mergeResponseCookies2(jar, bodyCookie)
		}
	}
	neteaseAnonJar = jar
	return neteaseAnonDevice, jar
}

// anonUsername 复刻 register_anonimous：base64(deviceId + " " + base64(md5(xor(deviceId))))
func anonUsername(deviceID string) string {
	xored := []byte(deviceID)
	for i := range xored {
		xored[i] ^= anonXorKey[i%len(anonXorKey)]
	}
	dll := base64.StdEncoding.EncodeToString(md5Sum(xored))
	return base64.StdEncoding.EncodeToString([]byte(deviceID + " " + dll))
}

func md5Sum(b []byte) []byte {
	s := md5.Sum(b)
	return s[:]
}

// mergeResponseCookies2 把 "k=v; k2=v2" 形态的 Cookie 字符串并入 jar。
func mergeResponseCookies2(jar map[string]string, header string) map[string]string {
	out := make(map[string]string, len(jar)+2)
	for k, v := range jar {
		out[k] = v
	}
	for _, part := range strings.Split(header, ";") {
		idx := strings.Index(part, "=")
		if idx <= 0 {
			continue
		}
		k := strings.TrimSpace(part[:idx])
		v := strings.TrimSpace(part[idx+1:])
		if k != "" && v != "" {
			out[k] = v
		}
	}
	return out
}
