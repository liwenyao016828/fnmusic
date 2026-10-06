package account

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── hash33 ──

// 期望值由上游 JS 实现（qq-auth.mjs 的 hash33）算出，用于锁定算法一致性。
func TestHash33MatchesUpstream(t *testing.T) {
	cases := []struct {
		value string
		seed  uint32
		want  uint32
	}{
		{"test123", 0, 1432224758},
		{"qrsig-abc", 0, 821548281},
		{"skey-value", 5381, 1077312491},
		{"", 0, 0},
		{"a", 0, 97},
	}
	for _, c := range cases {
		if got := hash33(c.value, c.seed); got != c.want {
			t.Errorf("hash33(%q, %d) = %d, want %d", c.value, c.seed, got, c.want)
		}
	}
}

func TestHash33WithinInt31(t *testing.T) {
	// 结果必须落在 0..2^31-1，否则拼进 URL 会被上游拒绝
	for _, s := range []string{"qrsig", strings.Repeat("x", 200), "中文key", "!@#$%^&*()"} {
		if v := hash33(s, 0); v > 0x7fffffff {
			t.Errorf("hash33(%q) = %d 超出 31 位范围", s, v)
		}
	}
}

// ── Cookie 辅助 ──

func TestCookiesToHeaderAndBack(t *testing.T) {
	header := cookiesToHeader(map[string]string{"a": "1", "b": "2", "empty": ""})
	if strings.Contains(header, "empty") {
		t.Errorf("空值不应写入请求头: %q", header)
	}
	parsed := parseCookieHeader(header)
	if parsed["a"] != "1" || parsed["b"] != "2" {
		t.Fatalf("解析结果不对: %v", parsed)
	}
}

func TestQQAccountIDPriority(t *testing.T) {
	cases := []struct {
		name    string
		cookies map[string]string
		want    string
	}{
		{"优先 qm_str_musicid", map[string]string{"qm_str_musicid": "12345", "uin": "999"}, "12345"},
		{"回退 uin", map[string]string{"uin": "999"}, "999"},
		{"回退 wxuin", map[string]string{"wxuin": "888"}, "888"},
		{"去掉前导 o", map[string]string{"qm_str_musicid": "o12345"}, "12345"},
		{"去掉前导零", map[string]string{"uin": "00123"}, "123"},
		{"全空返回空", map[string]string{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := qqAccountID(c.cookies); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestMergeResponseCookies(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Add("Set-Cookie", "qrsig=abc123; Path=/; HttpOnly")
	rec.Header().Add("Set-Cookie", "uin=o0123; Domain=.qq.com")
	rec.Header().Add("Set-Cookie", "p_skey=secret; Path=/")
	resp := rec.Result()

	got := mergeResponseCookies(resp, map[string]string{"existing": "keep"})
	if got["existing"] != "keep" {
		t.Error("应保留已有 cookie")
	}
	if got["qrsig"] != "abc123" || got["uin"] != "o0123" || got["p_skey"] != "secret" {
		t.Fatalf("解析结果不对: %v", got)
	}
}

// ── credentialToSession ──

func TestCredentialToSessionQQ(t *testing.T) {
	credential := map[string]any{
		"str_musicid":        "o123456",
		"musickey":           "Q_H_L_abc",
		"loginType":          float64(2),
		"openid":             "openid-x",
		"unionid":            "unionid-x",
		"refresh_token":      "rt-x",
		"access_token":       "at-x",
		"expired_at":         float64(1800000000),
		"musickeyCreateTime": float64(1700000000),
		"keyExpiresIn":       float64(2592000),
	}
	s := credentialToSession(credential, 2, "")

	if s["uin"] != "123456" || s["qm_str_musicid"] != "123456" {
		t.Errorf("uin 应为去 o 前缀的 123456，实际 %v", s["uin"])
	}
	if s["qm_keyst"] != "Q_H_L_abc" || s["qqmusic_key"] != "Q_H_L_abc" {
		t.Errorf("musickey 映射错误: %v", s["qm_keyst"])
	}
	if s["tmeLoginType"] != "2" {
		t.Errorf("tmeLoginType = %v", s["tmeLoginType"])
	}
	if s["psrf_qqopenid"] != "openid-x" {
		t.Errorf("QQ 登录应写 psrf_qqopenid，实际 %v", s)
	}
	if s["psrf_qqrefresh_token"] != "rt-x" {
		t.Errorf("QQ 登录应写 psrf_qqrefresh_token")
	}
	if s["psrf_access_token_expiresAt"] != "1800000000" {
		t.Errorf("expired_at 应转字符串")
	}
	// QQ 登录不应写微信专属字段
	if _, ok := s["wxuin"]; ok {
		t.Error("loginType=2 不应写 wxuin")
	}
}

func TestCredentialToSessionWeChat(t *testing.T) {
	credential := map[string]any{"str_musicid": "999", "musickey": "W_X_abc", "loginType": float64(1)}
	s := credentialToSession(credential, 1, "")

	if s["wxuin"] != "999" {
		t.Errorf("微信登录应写 wxuin，实际 %v", s)
	}
	if s["tmeLoginType"] != "1" {
		t.Errorf("tmeLoginType = %v", s["tmeLoginType"])
	}
}

func TestCredentialToSessionFallbackAccountID(t *testing.T) {
	// 凭据里没有 id 时用跳转地址里的 uin 兜底
	s := credentialToSession(map[string]any{"musickey": "k"}, 2, "o777")
	if s["uin"] != "777" {
		t.Fatalf("应使用兜底账号 ID，实际 %q", s["uin"])
	}
}

// ── 扫码流程 ──

// fakeQQ 起一个假 QQ 服务，按路径分发。返回服务基地址，
// 便于测试构造绝对跳转地址（ptqrlogin 成功回调里给的就是绝对 URL）。
func fakeQQ(t *testing.T, handlers map[string]http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	origP, origGP, origG, origM, origC := qqPtloginBase, qqGraphPtloginBase, qqGraphBase, qqMusicBase, qqCGIBase
	t.Cleanup(func() {
		qqPtloginBase, qqGraphPtloginBase, qqGraphBase, qqMusicBase, qqCGIBase = origP, origGP, origG, origM, origC
	})
	qqPtloginBase, qqGraphPtloginBase, qqGraphBase, qqMusicBase, qqCGIBase = srv.URL, srv.URL, srv.URL, srv.URL, srv.URL
	return srv.URL
}

func TestQQCreateQR(t *testing.T) {
	pngBytes := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a}
	fakeQQ(t, map[string]http.HandlerFunc{
		"/ptqrshow": func(w http.ResponseWriter, r *http.Request) {
			// 校验上游要求的固定参数
			q := r.URL.Query()
			if q.Get("appid") != qqAppID || q.Get("pt_3rd_aid") != qqClientID {
				t.Errorf("appid/pt_3rd_aid 不对: %v", q)
			}
			http.SetCookie(w, &http.Cookie{Name: "qrsig", Value: "sig-xyz"})
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		},
	})

	qr, err := QQCreateQR()
	if err != nil {
		t.Fatalf("QQCreateQR 失败: %v", err)
	}
	if qr.Key != "sig-xyz" {
		t.Errorf("Key = %q，应为 qrsig", qr.Key)
	}
	if !strings.HasPrefix(qr.Image, "data:image/png;base64,") {
		t.Errorf("Image 应为 data URL，实际前 40 字符: %q", qr.Image[:min(40, len(qr.Image))])
	}
}

func TestQQCreateQRMissingQrsig(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/ptqrshow": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("no cookie"))
		},
	})
	if _, err := QQCreateQR(); err == nil {
		t.Fatal("没有 qrsig 时应报错")
	}
}

func TestQQCheckQRStates(t *testing.T) {
	cases := []struct {
		name      string
		callback  string
		wantState string
		wantNick  string
	}{
		{"未扫码 66", `ptuiCB('66','0','','0','二维码未失效。','')`, QQStateWaiting, ""},
		{"已扫码 67", `ptuiCB('67','0','','0','二维码已扫描。','张三')`, QQStateScanned, "张三"},
		{"已过期 65", `ptuiCB('65','0','','0','二维码已失效。','')`, QQStateExpired, ""},
		{"无回调视为等待", `garbage`, QQStateWaiting, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeQQ(t, map[string]http.HandlerFunc{
				"/ptqrlogin": func(w http.ResponseWriter, r *http.Request) {
					// ptqrtoken 必须是数字（hash33 结果）
					token := r.URL.Query().Get("ptqrtoken")
					if token == "" || strings.ContainsAny(token, "abcxyz") {
						t.Errorf("ptqrtoken 应为数字，实际 %q", token)
					}
					if ck := r.Header.Get("Cookie"); !strings.Contains(ck, "qrsig=") {
						t.Errorf("轮询必须带上 qrsig，实际 %q", ck)
					}
					_, _ = w.Write([]byte("<script>" + c.callback + "</script>"))
				},
			})

			got, err := QQCheckQR("sig-xyz")
			if err != nil {
				t.Fatalf("QQCheckQR 失败: %v", err)
			}
			if got.State != c.wantState {
				t.Errorf("state = %q, want %q", got.State, c.wantState)
			}
			if got.Nickname != c.wantNick {
				t.Errorf("nickname = %q, want %q", got.Nickname, c.wantNick)
			}
		})
	}
}

func TestQQCheckQRRequiresKey(t *testing.T) {
	if _, err := QQCheckQR(""); err == nil {
		t.Fatal("key 为空应报错")
	}
}

// 完整成功链路：
// ptqrlogin → 解析跳转地址的 uin/ptsigx → check_sig 换 p_skey → oauth 换 code → musicu 换凭据
func TestQQCheckQRFullSuccessFlow(t *testing.T) {
	var jumpURL string
	sigxUsed := ""

	base := fakeQQ(t, map[string]http.HandlerFunc{
		"/ptqrlogin": func(w http.ResponseWriter, _ *http.Request) {
			http.SetCookie(w, &http.Cookie{Name: "ptcz", Value: "cz-value"})
			_, _ = w.Write([]byte("<script>ptuiCB('0','0','" + jumpURL + "','0','登录成功！','')</script>"))
		},
		// ⚠️ 关键一步：换 p_skey 必须打这里，而不是直接请求跳转地址。
		// 这是本项目的历史 bug（表现为「已扫码」后卡住报「没有返回 p_skey」）。
		// 2026-09-19 真机教训：check_sig **不能带任何 Cookie** —— 带上整个会话 jar
		// （superkey/ETK/RK 等）会被腾讯判异常，回 p_skey_forbid + 空 p_skey。
		// 参考实现（QQMusicApi）就是裸请求。
		"/check_sig": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			sigxUsed = q.Get("ptsigx")
			if ck := r.Header.Get("Cookie"); ck != "" {
				t.Errorf("check_sig 必须不带任何 Cookie，实际 %q", ck)
			}
			if q.Get("uin") != "o12345" {
				t.Errorf("check_sig 应带 uin=o12345（原样带 o 前缀），实际 %q", q.Get("uin"))
			}
			if q.Get("pt_3rd_aid") != qqClientID || q.Get("aid") != qqAppID {
				t.Errorf("check_sig 的 aid/pt_3rd_aid 不对: %v", q)
			}
			if q.Get("service") != "ptqrlogin" {
				t.Errorf("check_sig 的 service 应为 ptqrlogin，实际 %q", q.Get("service"))
			}
			http.SetCookie(w, &http.Cookie{Name: "p_skey", Value: "skey-value"})
			w.WriteHeader(http.StatusFound)
		},
		"/oauth2.0/authorize": func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			if r.Form.Get("client_id") != qqClientID {
				t.Errorf("client_id = %q", r.Form.Get("client_id"))
			}
			// authorize 只带 check_sig 新下发的那份（含 p_skey），
			// 不得混入 qrsig / ptqrlogin 的 ptcz —— 参考实现如此，多带会引入风控变量
			ck := r.Header.Get("Cookie")
			if !strings.Contains(ck, "p_skey=skey-value") {
				t.Errorf("authorize 请求应带 p_skey，实际 %q", ck)
			}
			if strings.Contains(ck, "qrsig") || strings.Contains(ck, "ptcz") {
				t.Errorf("authorize 混入了 check_sig 之外的会话 Cookie: %q", ck)
			}
			// g_tk 必须是 hash33(p_skey, 5381) 的结果
			wantGTK := "1077312491" // hash33("skey-value", 5381) 由上游算法算出
			if r.Form.Get("g_tk") != wantGTK {
				t.Errorf("g_tk = %q, want %q", r.Form.Get("g_tk"), wantGTK)
			}
			w.Header().Set("Location", "https://y.qq.com/portal/wx_redirect.html?code=AUTHCODE&state=state")
			w.WriteHeader(http.StatusFound)
		},
		"/cgi-bin/musicu.fcg": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			req := body["request"].(map[string]any)
			if req["module"] != "QQConnectLogin.LoginServer" || req["method"] != "QQLogin" {
				t.Errorf("module/method 不对: %v", req)
			}
			param := req["param"].(map[string]any)
			if param["code"] != "AUTHCODE" {
				t.Errorf("应带上授权 code，实际 %v", param)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"request": map[string]any{
					"code": 0,
					"data": map[string]any{
						"str_musicid": "o12345",
						"musickey":    "Q_H_L_final",
						"loginType":   2,
					},
				},
			})
		},
	})

	// 真实跳转地址的形态：带 uin、service、ptsigx、s_url
	jumpURL = base + "/oauth2.0/login_jump?uin=o12345&service=ptqrlogin&ptsigx=SIGX_TOKEN&s_url=https%3A%2F%2Fgraph.qq.com%2Foauth2.0%2Flogin_jump"

	got, err := QQCheckQR("sig-xyz")
	if err != nil {
		t.Fatalf("完整链路失败: %v", err)
	}
	if sigxUsed != "SIGX_TOKEN" {
		t.Errorf("check_sig 的 ptsigx = %q, 应为 SIGX_TOKEN", sigxUsed)
	}
	if got.State != QQStateSuccess {
		t.Fatalf("state = %q, want success", got.State)
	}
	if got.Cookies["qm_keyst"] != "Q_H_L_final" {
		t.Errorf("musickey 未正确保存: %v", got.Cookies["qm_keyst"])
	}
	if got.Cookies["uin"] != "12345" {
		t.Errorf("uin = %q", got.Cookies["uin"])
	}
}

// 回归：跳转地址里没有 ptsigx/uin 时要明确报错，而不是拿整串当 URL 请求后
// 报一个含糊的「没有返回 p_skey」—— 排查时那句会把人带偏。
func TestQQCheckQRMissingSigx(t *testing.T) {
	var jumpURL string
	fakeQQ(t, map[string]http.HandlerFunc{
		"/ptqrlogin": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<script>ptuiCB('0','0','" + jumpURL + "','0','登录成功！','')</script>"))
		},
	})
	jumpURL = "https://graph.qq.com/oauth2.0/login_jump?uin=o12345&ok=1" // 缺 ptsigx

	_, err := QQCheckQR("sig-xyz")
	if err == nil {
		t.Fatal("跳转地址缺 ptsigx 时应报错")
	}
	if !strings.Contains(err.Error(), "ptsigx") {
		t.Errorf("错误信息应点明缺 ptsigx，实际: %v", err)
	}
}

// 回归：check_sig 没下发 p_skey 时，错误信息要给出可行动提示
// （哪怕这个分支只会在二维码过期等异常下出现）。
func TestQQCheckQRCheckSigNoPsKey(t *testing.T) {
	var jumpURL string
	fakeQQ(t, map[string]http.HandlerFunc{
		"/ptqrlogin": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<script>ptuiCB('0','0','" + jumpURL + "','0','登录成功！','')</script>"))
		},
		"/check_sig": func(w http.ResponseWriter, _ *http.Request) {
			// 故意不下发 p_skey
			w.WriteHeader(http.StatusFound)
		},
	})
	jumpURL = "https://graph.qq.com/oauth2.0/login_jump?uin=o12345&service=ptqrlogin&ptsigx=SIGX&s_url=https%3A%2F%2Fgraph.qq.com%2Foauth2.0%2Flogin_jump"

	_, err := QQCheckQR("sig-xyz")
	if err == nil {
		t.Fatal("check_sig 未下发 p_skey 时应报错")
	}
	if !strings.Contains(err.Error(), "p_skey") {
		t.Errorf("错误信息应点明 p_skey，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "重新扫码") {
		t.Errorf("错误信息应给出可行动提示（重新扫码），实际: %v", err)
	}
}

func TestQQFetchProfile(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/cgi-bin/musicu.fcg": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"request": map[string]any{
					"code": 0,
					"data": map[string]any{
						"info": map[string]any{"nick": "听歌的人", "logo": "http://thirdqq.qlogo.cn/a.jpg"},
					},
				},
			})
		},
	})

	cookies := map[string]string{"qm_str_musicid": "12345", "qm_keyst": "key"}
	p, err := QQFetchProfile(cookies)
	if err != nil {
		t.Fatalf("QQFetchProfile 失败: %v", err)
	}
	if p.UID != "12345" || p.Nickname != "听歌的人" {
		t.Fatalf("资料解析不对: %+v", p)
	}
	// http 头像应升级为 https
	if !strings.HasPrefix(p.Avatar, "https://") {
		t.Errorf("头像应升级为 https，实际 %q", p.Avatar)
	}
}

func TestQQFetchProfileNotLoggedIn(t *testing.T) {
	if _, err := QQFetchProfile(map[string]string{}); err == nil {
		t.Fatal("无账号 ID 时应报错")
	}
}

// musicu 返回非 0 时错误信息应包含内外层码，便于排障
func TestQQMusicRequestErrorIncludesCodes(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/cgi-bin/musicu.fcg": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"request": map[string]any{
					"code": 1000,
					"data": map[string]any{},
				},
			})
		},
	})
	_, err := qqMusicRequest(map[string]string{"uin": "1"}, "m", "n", nil, 2)
	if err == nil {
		t.Fatal("inner code != 0 时应报错")
	}
	if !strings.Contains(err.Error(), "1000") {
		t.Errorf("错误信息应含 inner code，实际 %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 2.1.9 真机修复：QQ 的 disslst 会带一行「我的 created 歌单」表头汇总行
// （没有 dissid），以前原样透出 → 前端点它 → tracks 空 id → 502「需提供 source_id」。
func TestQQPlaylistsSkipsHeaderRows(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/rsc/fcgi-bin/fcg_user_created_diss": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"disslist": []any{
					map[string]any{"dissname": "我的创建歌单", "totalnum": 2}, // 表头行：没有 dissid
					map[string]any{"dissid": "7001", "dissname": "深夜歌单", "song_cnt": 12},
				},
			}})
		},
		"/fav/fcgi-bin/fcg_get_profile_order_asset.fcg": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"cdlist": []any{}}})
		},
	})
	cookies := map[string]string{"uin": "12345"}
	lists, err := QQPlaylists(cookies)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 1 {
		t.Fatalf("应只剩 1 条有效歌单，实际 %d: %+v", len(lists), lists)
	}
	if lists[0]["id"] != "7001" {
		t.Errorf("id = %v", lists[0]["id"])
	}
}
