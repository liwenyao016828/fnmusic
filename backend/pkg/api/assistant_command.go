package api

// 指令条接口（注入面板专用）：一句话 → 结果卡片。
//
// # 为什么单独一个接口，而不是复用 /api/assistant/chat
//
// /api/assistant/chat 是 SSE 流式对话，注入面板那个小挂件里没有地方放聊天
// 记录，也没有流式渲染的余地 —— 它只有一个输入框。所以给它的是**卡片**：
// 打「关掉每日 AI 歌单」就渲染一张带开关状态的卡片，打「状态」就渲染诊断卡。
//
// 匹配是**本地规则**（见 pkg/assistant/command.go），不依赖大模型；只有用户
// 说了本地规则接不住的话才走一次 AI 兜底，AI 没配置就退化成帮助卡片。
//
// 这个接口是注入面板（官方音乐页面里的挂件）专用的同源通道后端：
// 面板通过 /music/_qulv/api/command 转发到这里（见 pkg/intercept/qulvbridge.go）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"fn-lx-player/pkg/assistant"
)

// HandleAssistantCommand POST /api/assistant/command —— 指令条
//
// 请求体二选一：
//   - {"text": "关掉收藏自动下载"} —— 一句自然语言
//   - {"key": "fav_auto_download", "on": true} —— 面板点开关时的直连
//
// 响应：{ code, data: Widget }
func (s *Server) HandleAssistantCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
		Key  string `json:"key"`
		On   *bool  `json:"on"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		errJSON(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}

	// ── 直连：面板点某个开关 ──
	if key := strings.TrimSpace(body.Key); key != "" {
		if body.On == nil {
			errJSON(w, http.StatusBadRequest, "缺少 on（true / false）")
			return
		}
		if _, ok := assistant.FindSwitchDef(key); !ok {
			errJSON(w, http.StatusBadRequest, "没有这个开关："+key)
			return
		}
		if err := s.cfgMgr.SetFeatureSwitch(key, *body.On); err != nil {
			errJSON(w, http.StatusInternalServerError, "切换失败："+err.Error())
			return
		}
		wid := s.switchesWidget(nil)
		wid.Title = assistant.ToggleDescription([]string{key}, *body.On)
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": wid})
		return
	}

	text := strings.TrimSpace(body.Text)
	if text == "" {
		errJSON(w, http.StatusBadRequest, "请求体需包含 text 或 key")
		return
	}

	intent := assistant.MatchCommand(text)
	switch intent.Kind {
	case "switches":
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": s.switchesWidget(intent.Switches)})
	case "toggle":
		for _, k := range intent.Switches {
			if _, ok := assistant.FindSwitchDef(k); !ok {
				continue
			}
			if err := s.cfgMgr.SetFeatureSwitch(k, intent.On); err != nil {
				errJSON(w, http.StatusInternalServerError, "切换失败："+err.Error())
				return
			}
		}
		wid := s.switchesWidget(intent.Switches)
		wid.Title = assistant.ToggleDescription(intent.Switches, intent.On)
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": wid})
	case "status":
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": s.statusWidget()})
	case "playlist":
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 200,
			"data": assistant.Widget{
				Kind:    "text",
				Title:   "歌单",
				Message: "在曲率首页的「榜单 / 歌单」里管理；面板里只提供开关与诊断，歌单内容请到曲率里看。",
			},
		})
	case "download":
		writeJSON(w, http.StatusOK, map[string]any{
			"code": 200,
			"data": assistant.Widget{
				Kind:    "download",
				Title:   "下载当前歌曲",
				Message: "点上面的「下载到曲率」即可下载正在播放的这首。",
			},
		})
	case "help":
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": s.helpWidget()})
	default: // "ai" —— 本地规则接不住，交给大模型兜底
		wid, err := s.aiFallbackWidget(r.Context(), text)
		if err != nil {
			// AI 没配置 / 调不通时给帮助卡片，而不是一句冷冰冰的报错 ——
			// 面板很小，用户要的是「现在能干什么」。
			//
			// 但**必须说清是哪种失败**：以前统一写成「没听懂这句」，
			// 于是「模型没配」和「模型答不上」在用户眼里是一回事，
			// 用户会以为「接入坏了」。分清楚这两种情况，见 aiUnavailableReason。
			wid = s.helpWidget()
			available := false
			wid.AIAvailable = &available
			wid.Message = aiUnavailableReason(err) +
				"\n能直接问的：\n· 「有哪些开关」—— 列出所有可切换的开关\n" +
				"· 「打开 / 关闭 收藏自动下载」—— 直接切某个开关\n" +
				"· 「状态」—— 曲库、下载队列、音源数"
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": wid})
	}
}

// switchesWidget 生成开关卡片。keys 为空 = 全部开关。
func (s *Server) switchesWidget(keys []string) assistant.Widget {
	cfg := s.cfgMgr.Get()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	rows := make([]assistant.SwitchRow, 0, len(assistant.SwitchDefs))
	for _, d := range assistant.SwitchDefs {
		if len(want) > 0 && !want[d.Key] {
			continue
		}
		on, ok := cfg.FeatureSwitchState(d.Key)
		row := assistant.SwitchRow{
			Key:       d.Key,
			Label:     d.Label,
			Group:     d.Group,
			DefaultOn: d.DefaultOn,
		}
		if ok {
			v := on
			row.On = &v
		}
		rows = append(rows, row)
	}
	return assistant.Widget{Kind: "switches", Title: "功能开关", Switches: rows}
}

// statusWidget 生成诊断卡片。
func (s *Server) statusWidget() assistant.Widget {
	cfg := s.cfgMgr.Get()
	kvs := []assistant.KV{
		{Label: "版本", Value: CurrentVersion},
	}
	if s.libIndex != nil {
		kvs = append(kvs, assistant.KV{Label: "曲库歌曲", Value: fmt.Sprintf("%d", s.libIndex.Len())})
	}
	if s.backendDL != nil {
		st := s.backendDL.Stats()
		kvs = append(kvs,
			assistant.KV{Label: "下载中", Value: fmt.Sprintf("%d", st.Downloading)},
			assistant.KV{Label: "等待中", Value: fmt.Sprintf("%d", st.Pending)},
			assistant.KV{Label: "已完成", Value: fmt.Sprintf("%d", st.Success)},
			assistant.KV{Label: "失败", Value: fmt.Sprintf("%d", st.Failed)},
		)
	}
	sources := len(cfg.CustomSources) + len(cfg.APISources)
	kvs = append(kvs, assistant.KV{Label: "音源", Value: fmt.Sprintf("%d", sources)})
	return assistant.Widget{Kind: "status", Title: "曲率状态", Status: kvs}
}

// helpWidget 生成帮助卡片。
func (s *Server) helpWidget() assistant.Widget {
	return assistant.Widget{
		Kind:  "help",
		Title: "能说什么",
		Message: "试试这些说法：\n" +
			"· 「有哪些开关」—— 列出所有可切换的开关\n" +
			"· 「打开 / 关闭 收藏自动下载」—— 直接切某个开关\n" +
			"· 「状态」—— 曲库、下载队列、音源数\n" +
			"· 其它问题会转给曲率的 AI 助手（需在设置里配好模型）",
	}
}

// aiFallbackWidget 把一句本地规则接不住的话交给大模型。
//
// 这里刻意**不**走助手运行时（那套带会话、工具调用、确认闸口）：面板只有一行
// 输入，用户要的是一句答案，不是一串工具调用。模型没配好就如实说没配好。
func (s *Server) aiFallbackWidget(ctx context.Context, text string) (assistant.Widget, error) {
	if s.aiClient == nil {
		return assistant.Widget{}, errAINotConfigured
	}
	// 模型没配好时**如实说没配好**，不要把「没配」伪装成「模型答不上」——
	// pkg/ai 的 Chat 在未配置时是静默返回空串的（那是主流程的降级约定），
	// 到了这里必须显式拦住，否则用户永远不知道要先去设置里配模型。
	if !s.aiClient.RawConfig().Available() {
		return assistant.Widget{}, errAINotConfigured
	}
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	system := "你是「曲率」这款本地音乐管理应用的助手，运行在用户的飞牛 NAS 上。" +
		"请用中文简洁回答用户关于曲率使用的问题（面板功能开关、下载、曲库、榜单等）。" +
		"如果问题与曲率无关，一句话说明即可。不要编造路径或功能名。"
	out, _, err := s.aiClient.Chat(cctx, "assistant_command", system, text)
	if err != nil {
		return assistant.Widget{}, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return assistant.Widget{}, fmt.Errorf("模型没有返回内容")
	}
	return assistant.Widget{Kind: "text", Title: "AI 回答", Message: out}, nil
}

// errAINotConfigured 表示「AI 大模型没配置」，与「模型调用失败」区分开。
var errAINotConfigured = errors.New("AI 大模型还没配置")

// aiUnavailableReason 把 AI 兜底失败的原因翻成一句用户看得懂的话。
//
// 分两种：
//   - 没配置（enabled=false / 缺 base_url / 缺 key）→ 明确告诉用户去哪配；
//   - 调不通（超时、网关报错）→ 说是模型没响应，并给出可试的下一步。
//
// 以前两者都写成「没听懂这句」，用户会以为「接入坏了」。
func aiUnavailableReason(err error) string {
	if errors.Is(err, errAINotConfigured) {
		return "（大模型还没配置，所以这句只能走本地规则。到曲率「设置 → 账号连接 → AI 大模型」填好地址和密钥，这儿就能问别的了。）"
	}
	return "（这句没能问到大模型：" + err.Error() + "。本地还能问：开关、状态、下载当前歌曲。）"
}
