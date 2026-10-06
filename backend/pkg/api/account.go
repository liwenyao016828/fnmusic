package api

// 第三方平台账号（扫码登录）。
//
// 设计说明：
//   - 采用扫码登录，不在应用内收集用户密码；凭据（Cookie）落盘权限 0600。
//   - 账号列表接口**不返回 Cookie**，避免凭据经接口外泄。
//   - 当前支持网易云；QQ 音乐的扫码流程更复杂（ptqrshow + ptqrtoken 哈希 + OAuth 换取），
//     尚未实现，见 README/HANDOVER 的待办。

import (
	"encoding/json"
	"net/http"
	"strings"

	"fn-lx-player/pkg/account"
)

// HandleAccounts 列出已连接账号 / 断开账号
//
// GET    /api/accounts             列出（不含 Cookie）
// DELETE /api/accounts?provider=netease   断开
func (s *Server) HandleAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{
				"accounts": s.accountStore.List(),
				"supported": []map[string]string{
					{"provider": account.ProviderNetease, "display_name": "网易云音乐", "login": "qr"},
					{"provider": account.ProviderQQ, "display_name": "QQ 音乐", "login": "qr"},
				},
				"hint": "账号凭据只保存在本机（权限 0600），接口不会返回 Cookie 本身。",
			},
		})

	case http.MethodDelete:
		provider := strings.TrimSpace(r.URL.Query().Get("provider"))
		if provider == "" {
			errJSON(w, http.StatusBadRequest, "请提供 provider")
			return
		}
		if !s.accountStore.Delete(provider) {
			errJSON(w, http.StatusNotFound, "该平台尚未连接: "+provider)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]interface{}{"disconnected": provider},
		})

	default:
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// HandleNeteaseQR 申请扫码登录二维码
//
// POST /api/accounts/netease/qr
//
// 返回 {key, url}：url 为二维码承载内容，前端据此生成二维码图片；
// 随后用 key 轮询 GET /api/accounts/netease/qr/check。
func (s *Server) HandleNeteaseQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	qr, err := account.NeteaseQRKey()
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"key": qr.Key, "url": qr.URL,
			"check_endpoint": "/api/accounts/netease/qr/check?key=" + qr.Key,
			"hint":           "请用网易云音乐 App 扫码；每 2 秒轮询一次 check_endpoint。状态码 800 过期 / 801 待扫 / 802 待确认 / 803 成功。",
		},
	})
}

// HandleNeteaseQRCheck 轮询扫码状态；成功时自动保存账号
//
// GET /api/accounts/netease/qr/check?key=xxx
func (s *Server) HandleNeteaseQRCheck(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		errJSON(w, http.StatusBadRequest, "请提供 key")
		return
	}

	code, message, cookie, err := account.NeteaseQRCheck(key)
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}

	data := map[string]interface{}{
		"code": code, "status": message, "verified": code == account.QRVerified,
	}

	// 授权成功：校验凭据并落盘
	if code == account.QRVerified {
		if strings.TrimSpace(cookie) == "" {
			data["saved"] = false
			data["error"] = "网易未返回 Cookie，请重试扫码"
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": data})
			return
		}
		profile, err := account.NeteaseAccount(cookie)
		if err != nil {
			data["saved"] = false
			data["error"] = "凭据校验失败: " + err.Error()
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": data})
			return
		}
		if err := s.accountStore.Save(account.Account{
			Provider:    account.ProviderNetease,
			DisplayName: "网易云音乐",
			Cookie:      cookie,
			UID:         profile.UID,
			Nickname:    profile.Nickname,
			Avatar:      profile.Avatar,
			Status:      "connected",
		}); err != nil {
			errJSON(w, http.StatusInternalServerError, "保存账号失败: "+err.Error())
			return
		}
		data["saved"] = true
		// 只回显昵称与头像，不回显 Cookie
		data["profile"] = map[string]interface{}{
			"uid": profile.UID, "nickname": profile.Nickname, "avatar": profile.Avatar,
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": data})
}

// HandleQQQR 申请 QQ 音乐扫码登录二维码
//
// POST /api/accounts/qq/qr
//
// 与网易不同，这里直接返回可用的 data URL 图片（上游就是返回图片字节），
// 前端无需自行生成二维码。
func (s *Server) HandleQQQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	qr, err := account.QQCreateQR()
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"key": qr.Key, "image": qr.Image,
			"check_endpoint": "/api/accounts/qq/qr/check?key=" + qr.Key,
			"hint":           "请用手机 QQ 扫码；每 2 秒轮询一次 check_endpoint。state: waiting 待扫 / scanned 已扫 / expired 过期 / success 成功。",
		},
	})
}

// HandleQQQRCheck 轮询 QQ 扫码状态；成功时自动保存账号
//
// GET /api/accounts/qq/qr/check?key=xxx
func (s *Server) HandleQQQRCheck(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		errJSON(w, http.StatusBadRequest, "请提供 key")
		return
	}

	result, err := account.QQCheckQR(key)
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}

	data := map[string]interface{}{
		"state": result.State, "verified": result.State == account.QQStateSuccess,
	}
	if result.Nickname != "" {
		data["nickname"] = result.Nickname
	}

	if result.State == account.QQStateSuccess {
		if len(result.Cookies) == 0 {
			data["saved"] = false
			data["error"] = "QQ 未返回有效凭据，请重试扫码"
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": data})
			return
		}
		// 凭据以 Cookie 头形式存储；拉资料失败不阻断登录
		profile, perr := account.QQFetchProfile(result.Cookies)
		acc := account.Account{
			Provider:    account.ProviderQQ,
			DisplayName: "QQ 音乐",
			Cookie:      account.FormatQQCookies(result.Cookies),
			UID:         profile.UID,
			Nickname:    profile.Nickname,
			Avatar:      profile.Avatar,
			Status:      "connected",
		}
		if perr != nil {
			acc.UID = account.QQAccountIDOf(result.Cookies)
			data["profile_warning"] = perr.Error()
		}
		if err := s.accountStore.Save(acc); err != nil {
			errJSON(w, http.StatusInternalServerError, "保存账号失败: "+err.Error())
			return
		}
		data["saved"] = true
		data["profile"] = map[string]interface{}{
			"uid": acc.UID, "nickname": acc.Nickname, "avatar": acc.Avatar,
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "message": "ok", "data": data})
}

// GET /api/accounts/netease/daily
func (s *Server) HandleNeteaseDaily(w http.ResponseWriter, r *http.Request) {
	cookie := s.accountStore.Cookie(account.ProviderNetease)
	if cookie == "" {
		errJSON(w, http.StatusServiceUnavailable, "尚未连接网易云账号，请先扫码登录")
		return
	}
	songs, err := account.NeteaseDailySongs(cookie)
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"songs": songs, "count": len(songs)},
	})
}

// HandleNeteasePlaylists 账号的歌单列表（需先扫码登录）
//
// GET /api/accounts/netease/playlists?limit=100
func (s *Server) HandleNeteasePlaylists(w http.ResponseWriter, r *http.Request) {
	cookie := s.accountStore.Cookie(account.ProviderNetease)
	if cookie == "" {
		errJSON(w, http.StatusServiceUnavailable, "尚未连接网易云账号，请先扫码登录")
		return
	}
	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		var n int
		if err := json.Unmarshal([]byte(v), &n); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	playlists, err := account.NeteaseUserPlaylists(cookie, r.URL.Query().Get("uid"), limit)
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"playlists": playlists, "count": len(playlists)},
	})
}

// qqCookies 取出已连接 QQ 账号的 Cookie map；未连接时返回空 map。
func (s *Server) qqCookies() map[string]string {
	return account.ParseQQCookies(s.accountStore.Cookie(account.ProviderQQ))
}

// HandleQQDaily QQ 音乐每日推荐（需先扫码登录）
//
// GET /api/accounts/qq/daily
func (s *Server) HandleQQDaily(w http.ResponseWriter, r *http.Request) {
	cookies := s.qqCookies()
	if len(cookies) == 0 {
		errJSON(w, http.StatusServiceUnavailable, "尚未连接 QQ 音乐账号，请先扫码登录")
		return
	}
	songs, err := account.QQDaily(cookies)
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{"songs": songs, "count": len(songs)},
	})
}

// HandleQQPlaylists QQ 音乐歌单列表（自建 + 收藏，需先扫码登录）
//
// GET /api/accounts/qq/playlists
func (s *Server) HandleQQPlaylists(w http.ResponseWriter, r *http.Request) {
	cookies := s.qqCookies()
	if len(cookies) == 0 {
		errJSON(w, http.StatusServiceUnavailable, "尚未连接 QQ 音乐账号，请先扫码登录")
		return
	}
	playlists, err := account.QQPlaylists(cookies)
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok",
		"data": map[string]interface{}{
			"playlists": playlists, "count": len(playlists),
			"hint": "kind=created 为自建歌单，kind=collected 为收藏歌单；用返回的 id 调 /api/accounts/qq/playlist 取详情。",
		},
	})
}

// HandleQQPlaylistDetail QQ 音乐歌单详情（含曲目）
//
// GET /api/accounts/qq/playlist?id=xxx
func (s *Server) HandleQQPlaylistDetail(w http.ResponseWriter, r *http.Request) {
	// 先校验参数再校验登录态，让调用方拿到更精确的错误
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		errJSON(w, http.StatusBadRequest, "请提供 id（歌单 ID）")
		return
	}
	cookies := s.qqCookies()
	if len(cookies) == 0 {
		errJSON(w, http.StatusServiceUnavailable, "尚未连接 QQ 音乐账号，请先扫码登录")
		return
	}
	detail, err := account.QQPlaylistDetail(cookies, id)
	if err != nil {
		errJSON(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "message": "ok", "data": detail,
	})
}
