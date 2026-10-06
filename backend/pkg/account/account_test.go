package account

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Store ──

func TestStoreSaveGetListDelete(t *testing.T) {
	s := NewStore(t.TempDir())

	if _, ok := s.Get(ProviderNetease); ok {
		t.Fatal("初始应为空")
	}

	err := s.Save(Account{
		Provider: ProviderNetease, DisplayName: "网易云音乐",
		Cookie: "MUSIC_U=abc", UID: "42", Nickname: "测试用户",
	})
	if err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	got, ok := s.Get(ProviderNetease)
	if !ok {
		t.Fatal("应能取回账号")
	}
	if got.Cookie != "MUSIC_U=abc" || got.UID != "42" || got.Nickname != "测试用户" {
		t.Fatalf("取回内容不对: %+v", got)
	}
	if got.Status != "connected" {
		t.Errorf("默认状态应为 connected，实际 %q", got.Status)
	}
	if got.UpdatedAt == 0 {
		t.Error("应记录 UpdatedAt")
	}

	if !s.Delete(ProviderNetease) {
		t.Error("Delete 应返回 true")
	}
	if s.Delete(ProviderNetease) {
		t.Error("重复 Delete 应返回 false")
	}
	if _, ok := s.Get(ProviderNetease); ok {
		t.Error("删除后不应再取到")
	}
}

// List 用于接口响应，必须抹掉 Cookie，避免凭据外泄。
func TestStoreListStripsCookie(t *testing.T) {
	s := NewStore(t.TempDir())
	_ = s.Save(Account{Provider: ProviderNetease, Cookie: "SECRET=1", Nickname: "n"})

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(list))
	}
	if list[0].Cookie != "" {
		t.Fatalf("List 不应返回 Cookie，实际 %q", list[0].Cookie)
	}
	// 但内部 Get 仍能拿到
	if c := s.Cookie(ProviderNetease); c != "SECRET=1" {
		t.Fatalf("Cookie() 应返回真实凭据，实际 %q", c)
	}
}

// 凭据落盘权限必须是 0600。
func TestStoreFilePermission(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save(Account{Provider: ProviderNetease, Cookie: "c"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatalf("账号文件应存在: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("权限应为 0600，实际 %o", perm)
	}
}

func TestStorePersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	s1 := NewStore(dir)
	_ = s1.Save(Account{Provider: ProviderNetease, Cookie: "persist-me", Nickname: "甲"})

	s2 := NewStore(dir)
	got, ok := s2.Get(ProviderNetease)
	if !ok || got.Cookie != "persist-me" || got.Nickname != "甲" {
		t.Fatalf("重启后应能读回，实际 %+v ok=%v", got, ok)
	}
}

func TestStoreRejectsEmptyProvider(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Save(Account{Cookie: "x"}); err == nil {
		t.Fatal("provider 为空应报错")
	}
}

func TestStoreToleratesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir) // 不应 panic
	if len(s.List()) != 0 {
		t.Fatal("损坏文件应被忽略")
	}
}

// ── 网易接口 ──

// fakeNetease 起一个假的网易服务，按路径返回预设 JSON。
// 同时替换 legacy 基址与 eapi 基址，让扫码链路都打到假服务。
func fakeNetease(t *testing.T, replies map[string]any) *[]map[string]string {
	t.Helper()
	var seen []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
		seen = append(seen, map[string]string{
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
			"cookie": r.Header.Get("Cookie"),
			"method": r.Method,
			"form":   string(body),
		})
		reply, ok := replies[r.URL.Path]
		if !ok {
			reply = map[string]any{"code": 200}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)

	orig := neteaseBase
	origE := neteaseEapiBase
	t.Cleanup(func() { neteaseBase = orig; neteaseEapiBase = origE })
	neteaseBase = srv.URL
	neteaseEapiBase = srv.URL
	return &seen
}

// seenReq 按 method+path 找被记录的请求；找不到返回 nil。
func seenReq(seen *[]map[string]string, method, path string) map[string]string {
	for _, s := range *seen {
		if s["method"] == method && s["path"] == path {
			return s
		}
	}
	return nil
}

// eapiDecrypt 把 eapi 请求体的 params=<hex> 解回 JSON 供断言，并校验摘要。
// 格式（社区库 crypto.js eapi()）：uri-36cd479b6b5-{text}-36cd479b6b5-{md5(nobody+uri+use+text+md5forencrypt)}
func eapiDecrypt(t *testing.T, uriWant, form string) map[string]any {
	t.Helper()
	vals, err := url.ParseQuery(form)
	if err != nil {
		t.Fatalf("解析 form 失败: %v", err)
	}
	params := vals.Get("params")
	if params == "" {
		t.Fatal("eapi 请求体应为 params=<hex>")
	}
	raw, err := hex.DecodeString(params)
	if err != nil {
		t.Fatalf("params 不是 hex: %v", err)
	}
	plain := eapiTestDecryptECB(t, raw, []byte(eapiKey))
	sep := []byte(eapiSeparator)
	first := bytes.Index(plain, sep)
	if first <= 0 {
		t.Fatalf("明文格式不对: %q", plain)
	}
	uri := string(plain[:first])
	if uri != uriWant {
		t.Errorf("摘要绑定的 uri = %q，期望 %q", uri, uriWant)
	}
	rest := plain[first+len(sep):]
	last := bytes.LastIndex(rest, sep)
	if last <= 0 {
		t.Fatalf("明文格式不对: %q", rest)
	}
	text := rest[:last]
	sum := fmt.Sprintf("%x", md5.Sum([]byte("nobody"+uri+"use"+string(text)+"md5forencrypt")))
	if string(rest[last+len(sep):]) != sum {
		t.Errorf("eapi 摘要校验失败")
	}
	var m map[string]any
	if err := json.Unmarshal(text, &m); err != nil {
		t.Fatalf("eapi 明文不是 JSON: %v (%s)", err, text)
	}
	return m
}

// 扫码必须走 **eapi 加密通道**（POST /eapi/...，params=AES-ECB）。
// 依据（2026-09-19 真机两轮验证）：明文 /api/ 通道申请的 unikey 会被服务端
// 直接判为异常渠道（轮询返回「请切换其他登录方式」），社区活跃库默认就是 eapi。
func TestNeteaseQRKeyUsesEapi(t *testing.T) {
	got := fakeNetease(t, map[string]any{
		"/eapi/login/qrcode/unikey": map[string]any{"code": 200, "unikey": "key-123"},
	})

	qr, err := NeteaseQRKey()
	if err != nil {
		t.Fatalf("NeteaseQRKey 失败: %v", err)
	}
	if qr.Key != "key-123" {
		t.Errorf("Key = %q", qr.Key)
	}
	req := seenReq(got, http.MethodPost, "/eapi/login/qrcode/unikey")
	if req == nil {
		t.Fatal("申请 unikey 必须 POST 到 /eapi/login/qrcode/unikey")
	}
	m := eapiDecrypt(t, "/api/login/qrcode/unikey", req["form"])
	if fmt.Sprint(m["type"]) != "3" {
		t.Errorf("eapi 参数 type 应为 3（社区库实测值），实际 %v", m["type"])
	}
	hdr, _ := m["header"].(map[string]any)
	if hdr == nil || fmt.Sprint(hdr["deviceId"]) == "" {
		t.Errorf("eapi 参数必须内嵌 header.deviceId，实际 %v", m["header"])
	}
	if fmt.Sprint(hdr["os"]) != "pc" {
		t.Errorf("header.os 应为 pc，实际 %v", hdr["os"])
	}
	if !strings.Contains(qr.URL, "codekey=key-123") {
		t.Errorf("二维码 URL 应含 codekey=key-123，实际 %q", qr.URL)
	}
	if strings.Contains(qr.URL, "chainId") {
		t.Errorf("二维码 URL 不应带 chainId，实际 %q", qr.URL)
	}
}

// 轮询同样走 eapi，且与申请同一 deviceId（eapi 通道靠 header 参数串会话）。
func TestNeteaseQRCheckUsesSameType(t *testing.T) {
	got := fakeNetease(t, map[string]any{
		"/eapi/login/qrcode/client/login": map[string]any{"code": 801, "message": "等待扫码"},
	})
	if _, _, _, err := NeteaseQRCheck("key-abc"); err != nil {
		t.Fatalf("NeteaseQRCheck 失败: %v", err)
	}
	req := seenReq(got, http.MethodPost, "/eapi/login/qrcode/client/login")
	if req == nil {
		t.Fatal("轮询必须 POST 到 /eapi/login/qrcode/client/login")
	}
	m := eapiDecrypt(t, "/api/login/qrcode/client/login", req["form"])
	if fmt.Sprint(m["type"]) != "3" {
		t.Errorf("轮询 type 应为 3，实际 %v", m["type"])
	}
	if m["key"] != "key-abc" {
		t.Errorf("轮询应带 key，实际 %v", m["key"])
	}
}

func TestNeteaseQRKeyMissingUnikey(t *testing.T) {
	fakeNetease(t, map[string]any{
		"/eapi/login/qrcode/unikey": map[string]any{"code": 500},
	})
	if _, err := NeteaseQRKey(); err == nil {
		t.Fatal("未返回 unikey 时应报错")
	}
}

func TestNeteaseQRCheckStates(t *testing.T) {
	cases := []struct {
		code     int
		message  string
		wantText string
	}{
		{QRWaiting, "等待扫码", "等待扫码"},
		{QRScanned, "待确认", "待确认"},
		{QRExpired, "二维码已过期", "二维码已过期"},
	}
	for _, c := range cases {
		fakeNetease(t, map[string]any{
			"/eapi/login/qrcode/client/login": map[string]any{"code": c.code, "message": c.message},
		})
		code, msg, cookie, err := NeteaseQRCheck("k")
		if err != nil {
			t.Fatalf("code=%d 时出错: %v", c.code, err)
		}
		if code != c.code || msg != c.wantText {
			t.Errorf("got (%d,%q), want (%d,%q)", code, msg, c.code, c.wantText)
		}
		if cookie != "" {
			t.Errorf("未成功时不应返回 cookie，实际 %q", cookie)
		}
	}
}

// 803 的登录 Cookie 优先取 **Set-Cookie 响应头**（官方浏览器与参考库都是这么拿的）。
func TestNeteaseQRCheckVerifiedCookieFromHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "client/login") {
			w.Header().Add("Set-Cookie", "MUSIC_U=hdr-token; Domain=music.163.com; Path=/")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 803})
	}))
	t.Cleanup(srv.Close)
	origE := neteaseEapiBase
	t.Cleanup(func() { neteaseEapiBase = origE })
	neteaseEapiBase = srv.URL

	_, _, cookie, err := NeteaseQRCheck("k")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cookie, "MUSIC_U=hdr-token") {
		t.Fatalf("应从 Set-Cookie 头取登录 Cookie，实际 %q", cookie)
	}
}

// 老接口在响应体里回传 cookie 字段，保留为兜底。
func TestNeteaseQRCheckVerifiedCookieBodyFallback(t *testing.T) {
	fakeNetease(t, map[string]any{
		"/eapi/login/qrcode/client/login": map[string]any{"code": 803, "cookie": "MUSIC_U=xyz"},
	})
	code, _, cookie, err := NeteaseQRCheck("k")
	if err != nil {
		t.Fatal(err)
	}
	if code != QRVerified || cookie != "MUSIC_U=xyz" {
		t.Fatalf("got code=%d cookie=%q", code, cookie)
	}
}

// eapi 通道的会话连续性靠 **header.deviceId**：申请与轮询必须是同一设备指纹
// （社区库用进程级 global.deviceId；我们按 unikey 存会话）。
func TestNeteaseQRCheckCarriesSessionDeviceID(t *testing.T) {
	var keyParams, pollParams string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
		form := string(body)
		switch {
		case strings.Contains(r.URL.Path, "/eapi/login/qrcode/unikey"):
			keyParams = form
			w.Header().Add("Set-Cookie", "NMTID=sess-1; Path=/")
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "unikey": "k-1"})
		case strings.Contains(r.URL.Path, "client/login"):
			pollParams = form
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 801, "message": "等待扫码"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200})
		}
	}))
	t.Cleanup(srv.Close)
	origE := neteaseEapiBase
	t.Cleanup(func() { neteaseEapiBase = origE })
	neteaseEapiBase = srv.URL

	if _, err := NeteaseQRKey(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := NeteaseQRCheck("k-1"); err != nil {
		t.Fatal(err)
	}
	if keyParams == "" || pollParams == "" {
		t.Fatal("两步请求都没抓到 params，扫码没走 eapi？")
	}
	km := eapiDecrypt(t, "/api/login/qrcode/unikey", keyParams)
	pm := eapiDecrypt(t, "/api/login/qrcode/client/login", pollParams)
	kh, _ := km["header"].(map[string]any)
	ph, _ := pm["header"].(map[string]any)
	if kh == nil || ph == nil || kh["deviceId"] != ph["deviceId"] {
		t.Errorf("轮询 deviceId 应与申请一致：key=%v poll=%v", kh, ph)
	}
}

// eapiTestDecryptECB 测试侧 AES-ECB 解密（生产代码只加密不解密）。
func eapiTestDecryptECB(t *testing.T, data, key []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%block.BlockSize() != 0 {
		t.Fatalf("密文长度 %d 不是块大小倍数", len(data))
	}
	out := make([]byte, len(data))
	for start := 0; start < len(data); start += block.BlockSize() {
		block.Decrypt(out[start:start+block.BlockSize()], data[start:start+block.BlockSize()])
	}
	pad := int(out[len(out)-1])
	if pad <= 0 || pad > block.BlockSize() || pad > len(out) {
		t.Fatalf("PKCS7 填充非法: %d", pad)
	}
	return out[:len(out)-pad]
}

func TestNeteaseAccountParsesProfile(t *testing.T) {
	got := fakeNetease(t, map[string]any{
		"/api/nuser/account/get": map[string]any{
			"code":    200,
			"account": map[string]any{"id": 42},
			"profile": map[string]any{"userId": 42, "nickname": "听歌的人", "avatarUrl": "https://a/b.jpg"},
		},
	})

	p, err := NeteaseAccount("MUSIC_U=abc")
	if err != nil {
		t.Fatalf("NeteaseAccount 失败: %v", err)
	}
	if p.UID != "42" || p.Nickname != "听歌的人" || p.Avatar != "https://a/b.jpg" {
		t.Fatalf("解析结果不对: %+v", p)
	}
	// Cookie 必须带上，否则接口会当成未登录
	if c := (*got)[0]["cookie"]; c != "MUSIC_U=abc" {
		t.Errorf("请求未带 Cookie，实际 %q", c)
	}
}

func TestNeteaseAccountNotLoggedIn(t *testing.T) {
	fakeNetease(t, map[string]any{
		"/api/nuser/account/get": map[string]any{"code": 200, "account": nil, "profile": nil},
	})
	if _, err := NeteaseAccount("bad"); err == nil {
		t.Fatal("未登录时应报错")
	}
}

func TestNeteaseDailySongs(t *testing.T) {
	fakeNetease(t, map[string]any{
		"/api/v1/discovery/recommend/songs": map[string]any{
			"code": 200,
			"recommend": []map[string]any{
				{"id": 1, "name": "歌一"},
				{"id": 2, "name": "歌二"},
			},
		},
	})
	songs, err := NeteaseDailySongs("MUSIC_U=abc")
	if err != nil {
		t.Fatalf("NeteaseDailySongs 失败: %v", err)
	}
	if len(songs) != 2 || songs[0]["name"] != "歌一" {
		t.Fatalf("解析结果不对: %v", songs)
	}
}

func TestNeteaseDailySongsEmptyExplainsWhy(t *testing.T) {
	fakeNetease(t, map[string]any{
		"/api/v1/discovery/recommend/songs": map[string]any{"code": 200, "recommend": []any{}},
	})
	_, err := NeteaseDailySongs("")
	if err == nil {
		t.Fatal("空日推应报错")
	}
	if !strings.Contains(err.Error(), "未登录") {
		t.Errorf("错误信息应提示可能原因，实际 %v", err)
	}
}

func TestNeteaseUserPlaylistsUsesProvidedUID(t *testing.T) {
	got := fakeNetease(t, map[string]any{
		"/api/user/playlist": map[string]any{
			"code":     200,
			"playlist": []map[string]any{{"id": 9, "name": "我喜欢的音乐"}},
		},
	})
	list, err := NeteaseUserPlaylists("MUSIC_U=abc", "777", 50)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if len(list) != 1 || list[0]["name"] != "我喜欢的音乐" {
		t.Fatalf("解析结果不对: %v", list)
	}
	qs := (*got)[0]["query"]
	if !strings.Contains(qs, "uid=777") || !strings.Contains(qs, "limit=50") {
		t.Errorf("查询串不对: %q", qs)
	}
}

// uid 为空时应先查账号资料再取歌单。
func TestNeteaseUserPlaylistsFallsBackToAccountUID(t *testing.T) {
	got := fakeNetease(t, map[string]any{
		"/api/nuser/account/get": map[string]any{
			"code": 200, "account": map[string]any{"id": 555},
		},
		"/api/user/playlist": map[string]any{"code": 200, "playlist": []any{}},
	})
	if _, err := NeteaseUserPlaylists("MUSIC_U=abc", "", 0); err != nil {
		t.Fatalf("失败: %v", err)
	}
	if len(*got) != 2 {
		t.Fatalf("应先查账号再取歌单，共 2 次请求，实际 %d", len(*got))
	}
	if qs := (*got)[1]["query"]; !strings.Contains(qs, "uid=555") {
		t.Errorf("应使用账号 uid，实际 %q", qs)
	}
}

func TestQRStatusText(t *testing.T) {
	if QRStatusText(QRWaiting) != "等待扫码" {
		t.Errorf("801 文案不对: %q", QRStatusText(QRWaiting))
	}
	if !strings.Contains(QRStatusText(QRVerified), "成功") {
		t.Errorf("803 文案不对: %q", QRStatusText(QRVerified))
	}
	if !strings.Contains(QRStatusText(9999), "9999") {
		t.Errorf("未知状态应回显状态码，实际 %q", QRStatusText(9999))
	}
}
