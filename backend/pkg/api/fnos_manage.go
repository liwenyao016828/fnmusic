package api

// 「飞牛同步管理」页需要的两个后端能力。
//
// 背景：我们的令牌是**每次调用都从飞牛音乐数据库现读**的（见 pkg/fnos/token.go），
// 所以飞牛自己刷新令牌后我们下一轮就拿到新的 —— **天然就是自动续期**，
// 不需要参考实现（flow）那套「续期」机制。
//
// 但自动读取偶尔也会读不到（数据库路径变了、权限问题、飞牛没登录过），
// 这时需要一个手工令牌顶上排障。这两个接口就是为此。

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"fn-lx-player/pkg/fnos"
)

// HandleFnosCheck 真实探一次飞牛音乐连接。
//
//	POST /api/fnos/check
//
// 与 GET /api/fnos/status 的区别：status 是「看状态」（可能只是读本地文件/环境），
// 这个会**真的调一次飞牛接口**（/user/me），用来回答「现在到底通不通」。
func (s *Server) HandleFnosCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}

	m := fnos.NewMusic("")
	if !m.Available() {
		// 响应里只回一句人话；逐项诊断（缺哪个 socket、哪些库路径没读到令牌）进日志。
		reason := fnos.UnavailableReason()
		if reason == "" {
			reason = "飞牛音乐接口不可用（原因未知）"
		}
		if detail := fnos.UnavailableDetail(); detail != "" {
			log.Printf("[fnos] 检查连接失败，逐项检查：%s", detail)
		}
		errJSON(w, http.StatusServiceUnavailable, reason)
		return
	}

	account, err := m.Me()
	if err != nil {
		errJSON(w, http.StatusBadGateway, "连接飞牛音乐失败："+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{
			"ok":           true,
			"account":      account,
			"token_source": fnos.TokenSource(),
			"note":         "连接正常。令牌每次调用都从飞牛数据库现读，飞牛刷新后会自动跟随，无需手动续期。",
		},
	})
}

// HandleFnosToken 保存 / 清除手工令牌。
//
//	POST /api/fnos/token   body: {"token": "..."}
//
// 传空串表示**清除**，回到自动读取（从飞牛数据库读最新令牌）。
// 手工令牌的优先级高于环境变量与数据库 —— 它的用途就是「自动读不到时先顶上」。
func (s *Server) HandleFnosToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid payload: "+err.Error())
		return
	}

	token := strings.TrimSpace(req.Token)

	cfg := s.cfgMgr.Get()
	cfg.FnosToken = token
	if err := s.cfgMgr.Update(cfg); err != nil {
		errJSON(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	// 立刻生效，不用重启
	fnos.SetManualToken(token)

	msg := "已保存手工令牌，后续调用优先使用它"
	if token == "" {
		msg = "已清除手工令牌，回到自动读取（从飞牛数据库读最新令牌）"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{
			"has_manual_token": token != "",
			"token_source":     fnos.TokenSource(),
			"note":             msg,
		},
	})
}
