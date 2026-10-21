package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"fn-lx-player/pkg/intercept"
)

// 「替换音质」的两个接口（v2.17）。
//
// # 为什么是「起任务 + 轮询」而不是一次同步返回
//
// 一首歌要问 2–3 条取音渠道，洛雪那条失败一次就是 8 秒 × 脚本数；几百首就是十几分钟。
// 同步接口必然超时，所以形状与 musicdl 的试运行一致（见 pkg/api/musicdl.go）：
// 起一次任务、之后轮询状态。scan 一个路径吃掉 start/status/stop 三个动作 ——
// 它们是同一件事的三个面，分成三个路径只会让路由表长出三条近似记录。
//
// # 为什么替换要单独一条、而且限条数
//
// 替换是**写盘**（把 mp3 换成 flac，可能要重新取一份几十兆的文件）。
// 一次最多 50 条：这是「用户勾了一批」的量级，不是「整个曲库」的量级；
// 想全换就分几次点，别让一次请求占着存储跑半小时。

// SetQuality 注入「替换音质」的四个动作。由 main 提供（这里不 import main）。
func (s *Server) SetQuality(
	start func(mode string, limit int) (intercept.QualityStatus, error),
	status func() intercept.QualityStatus,
	stop func() intercept.QualityStatus,
	replace func(r *http.Request, paths []string) (any, error),
) {
	s.qualityStart = start
	s.qualityStatus = status
	s.qualityStop = stop
	s.qualityReplace = replace
}

// qualityBodyLimit 单次替换的条数上限。50 = 界面一屏勾选的合理量级。
const qualityBodyLimit = 50

// handleQualityScan 扫描曲库、逐首探各渠道可得档位。
//
// body：`{"action":"start"|"status"|"stop", "mode":"lossy"|"all", "limit":200}`
// - action 缺省 start（界面上就是「开始扫描」）。
// - mode 缺省 lossy：只扫**有损**文件 —— 无损的没什么可换的，扫它纯属浪费外呼。
// - limit 缺省 200、上限 2000；超出只是把 `truncated` 置真，不是错误。
//
// data 形如 `{state,mode,total,scanned,truncated,upgradeable,error,entries:[…]}`；
// `entries[].channels[]` 就是「这首歌在各渠道**实际**能拿到什么档位」的答案。
func (s *Server) handleQualityScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": 405, "message": "只支持 POST"})
		return
	}
	if s.qualityStart == nil {
		qualityUnavailable(w)
		return
	}
	var body struct {
		Action string `json:"action"`
		Mode   string `json:"mode"`
		Limit  int    `json:"limit"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	}
	switch strings.ToLower(strings.TrimSpace(body.Action)) {
	case "", "start":
		st, err := s.qualityStart(body.Mode, body.Limit)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "msg": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": st})
	case "status":
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": s.qualityStatus()})
	case "stop":
		if s.qualityStop == nil {
			qualityUnavailable(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": s.qualityStop()})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": 400,
			"msg":  "action 只支持 start / status / stop",
		})
	}
}

// handleQualityReplace 把勾中的文件按「各渠道实际可得最高档位」重新取音并覆盖。
//
// body：`{"paths":["/vol3/…/a.mp3", …]}`，一次最多 50 条。
// data：`{done:[{path,ok,new_path,tier,channel,note}], failed:[{path,error}]}`。
//
// 逐条独立：一条失败不影响后面的，界面按条显示结果。
// `ok:false` 配 `note` 的情形是**正常的**（例如「拿到的是同档位文件，没有覆盖」），
// 与 `failed`（探不到、路径越界、下载报错）分开，别让界面把两者说成同一件事。
func (s *Server) handleQualityReplace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": 405, "message": "只支持 POST"})
		return
	}
	if s.qualityReplace == nil {
		qualityUnavailable(w)
		return
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	}
	paths := make([]string, 0, len(body.Paths))
	for _, p := range body.Paths {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "msg": "paths 不能为空"})
		return
	}
	if len(paths) > qualityBodyLimit {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": 400,
			"msg":  "一次最多替换 50 首；想全换就分几次点（替换要重新下载，一条可能要几十秒）",
		})
		return
	}
	data, err := s.qualityReplace(r, paths)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 400, "msg": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": data})
}

// qualityUnavailable 是拦截层没起来时的统一应答（与 musicdlUnavailable 同形状）。
func qualityUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 502,
		"msg":  "取音通道没起来（应用还没就绪）",
		"data": map[string]any{"available": false},
	})
}
