package account

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// QQMobileCreateQR：走 musicu.fcg 的 CreateQRCode，comm 必须是网页扫码那套（ct=23,cv=0）。
func TestQQMobileCreateQR(t *testing.T) {
	var gotModule, gotMethod string
	var gotComm, gotParam map[string]any
	fakeQQ(t, map[string]http.HandlerFunc{
		"/cgi-bin/musicu.fcg": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotComm, _ = body["comm"].(map[string]any)
			req := body["request"].(map[string]any)
			gotModule, _ = req["module"].(string)
			gotMethod, _ = req["method"].(string)
			gotParam, _ = req["param"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"request": map[string]any{"code": 0, "data": map[string]any{
					"qrcode":   "data:image/png;base64,AAA",
					"qrcodeID": "QRID-1",
				}},
			})
		},
	})

	var watched atomic.Value
	origWatch := qqMobileWatchStart
	t.Cleanup(func() { qqMobileWatchStart = origWatch })
	qqMobileWatchStart = func(id string) { watched.Store(id) }
	t.Cleanup(func() { qqMobileDrop("QRID-1") })

	qr, err := QQMobileCreateQR()
	if err != nil {
		t.Fatalf("QQMobileCreateQR: %v", err)
	}
	if qr.Key != "QRID-1" || qr.Image == "" {
		t.Fatalf("返回不对: %+v", qr)
	}
	if gotModule != "music.login.LoginServer" || gotMethod != "CreateQRCode" {
		t.Errorf("module/method = %s/%s", gotModule, gotMethod)
	}
	if fmt.Sprint(gotComm["ct"]) != "23" || fmt.Sprint(gotComm["cv"]) != "0" {
		t.Errorf("comm 应为网页扫码那套 ct=23,cv=0，实际 %v", gotComm)
	}
	if gotParam["tmeAppID"] != "qqmusic" {
		t.Errorf("param.tmeAppID = %v", gotParam["tmeAppID"])
	}
	// param 必须声明发起方客户端类型（安卓 ct=11,cv=14090008）——
	// 缺了服务端建出"未知客户端"会话，App 扫开后确认页空白（2.1.6 真机教训）
	if fmt.Sprint(gotParam["ct"]) != "11" || gotParam["cv"] != float64(14090008) {
		t.Errorf("param 应含安卓客户端类型 ct=11,cv=14090008，实际 %v", gotParam)
	}
	if watched.Load() != "QRID-1" {
		t.Error("申请后应启动 MQTT 监听")
	}
	if s := qqMobileLoad("QRID-1"); s == nil || s.state != QQStateWaiting {
		t.Errorf("会话应已建立且为 waiting，实际 %+v", s)
	}
}

// 推送事件状态机：scanned / canceled / timeout / loginFailed / 未知事件。
func TestQQMobileHandlePublishStates(t *testing.T) {
	cases := []struct {
		event    string
		payload  string
		wantSt   string
		terminal bool
	}{
		{"scanned", `{}`, QQStateScanned, false},
		{"canceled", `{}`, QQStateRefused, true},
		{"timeout", `{}`, QQStateExpired, true},
		{"loginFailed", `{"msg":"x"}`, QQStateFailed, true},
		{"whatever", `{}`, QQStateWaiting, false},
	}
	for _, c := range cases {
		id := "mob-" + c.event
		qqMobileSave(id, &qqMobileSession{state: QQStateWaiting, cookies: map[string]string{}})
		t.Cleanup(func() { qqMobileDrop(id) })

		terminalCalled := false
		qqMobileHandlePublish(id, c.event, []byte(c.payload), func() { terminalCalled = true })

		s := qqMobileLoad(id)
		if s.state != c.wantSt {
			t.Errorf("event=%s state=%q, want %q", c.event, s.state, c.wantSt)
		}
		if terminalCalled != c.terminal {
			t.Errorf("event=%s terminal=%v, want %v", c.event, terminalCalled, c.terminal)
		}
	}
}

// cookies 事件 → Login 换凭据（tmeLoginType=6），成功后 state=success。
func TestQQMobileCookiesExchange(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/cgi-bin/musicu.fcg": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			comm := body["comm"].(map[string]any)
			if fmt.Sprint(comm["tmeLoginType"]) != "6" {
				t.Errorf("Login 的 tmeLoginType 应为 6，实际 %v", comm["tmeLoginType"])
			}
			req := body["request"].(map[string]any)
			param := req["param"].(map[string]any)
			if param["qrCodeID"] != "QRID-C" || param["token"] != "TOKEN-X" {
				t.Errorf("Login 参数不对: %v", param)
			}
			if _, ok := param["musicid"].(float64); !ok {
				t.Errorf("musicid 应为数字: %v", param["musicid"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"request": map[string]any{"code": 0, "data": map[string]any{
					"musicid":     12345,
					"str_musicid": "12345",
					"musickey":    "Q_H_L_mobile",
					"loginType":   6,
				}},
			})
		},
	})

	id := "QRID-C"
	qqMobileSave(id, &qqMobileSession{state: QQStateScanned, cookies: map[string]string{}})
	t.Cleanup(func() { qqMobileDrop(id) })

	terminalCalled := false
	qqMobileHandlePublish(id, "cookies", []byte(`{"cookies":{`+
		`"qqmusic_uin":{"value":"12345"},`+
		`"qqmusic_key":{"value":"TOKEN-X"}}}`), func() { terminalCalled = true })

	s := qqMobileLoad(id)
	if s.state != QQStateSuccess {
		t.Fatalf("state=%q err=%q, want success", s.state, s.err)
	}
	if !terminalCalled {
		t.Error("cookies 是终态，应触发 terminal")
	}
	if s.cookies["qm_keyst"] != "Q_H_L_mobile" || s.cookies["uin"] != "12345" {
		t.Errorf("凭据映射不对: %v", s.cookies)
	}
	if s.cookies["tmeLoginType"] != "6" {
		t.Errorf("tmeLoginType 应为 6: %v", s.cookies["tmeLoginType"])
	}
}

// mqtt5 报文编码回环：解析自己编码的 CONNECT 主体不 panic（粗校验），
// 以及 SERVER_REFERENCE 路径拼接语义。
func TestMqtt5JoinPath(t *testing.T) {
	got := mqtt5JoinPath("/ws/handshake", "1.2.3.4:29001")
	if got != "/ws/handshake/1.2.3.4:29001" {
		t.Errorf("首次重定向应追加节点段，实际 %q", got)
	}
	got = mqtt5JoinPath("/ws/handshake/1.2.3.4:29001", "5.6.7.8:29001")
	if got != "/ws/handshake/5.6.7.8:29001" {
		t.Errorf("二次重定向应替换节点段，实际 %q", got)
	}
}

// 真实抓包回归：broker 的 SUBACK 属性段是 27 字节的 timestamp 用户属性，
// 属性长度前缀是字节数不是条数（这里曾经把 reason 字节吃掉）。
func TestMqtt5ParseRealSuback(t *testing.T) {
	raw, _ := decodeHex("901f00011b26000974696d657374616d70000d3137383938393630313132313100")
	pkt, n, err := mqtt5Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(raw) {
		t.Errorf("消费字节数 %d != %d", n, len(raw))
	}
	if pkt.typ != 0x90 || pkt.subackID != 1 {
		t.Fatalf("typ=%X id=%d", pkt.typ, pkt.subackID)
	}
	if len(pkt.subackReasons) != 1 || pkt.subackReasons[0] != 0 {
		t.Fatalf("reasons=%v, want [0]", pkt.subackReasons)
	}
}

// 真实抓包回归：首跳 CONNACK 0x9D + ServerReference。
func TestMqtt5ParseRealRedirectConnack(t *testing.T) {
	raw, _ := decodeHex("2058009d552600096368616e6e656c4944001332313031363032333038333931323139323030260006736572766572001331312e3136382e32302e3230335f32393030311c001331312e3138312e39352e3234353a3239303031")
	pkt, _, err := mqtt5Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.connack.ReasonCode != 0x9D {
		t.Errorf("reason=0x%X", pkt.connack.ReasonCode)
	}
	if pkt.connack.ServerRef != "11.181.95.245:29001" {
		t.Errorf("serverRef=%q", pkt.connack.ServerRef)
	}
	if pkt.connack.User["channelID"] == "" {
		t.Error("应能解析出用户属性 channelID")
	}
}

func decodeHex(s string) ([]byte, error) {
	out := make([]byte, 0, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		var b byte
		for _, c := range s[i : i+2] {
			switch {
			case c >= '0' && c <= '9':
				b = b*16 + byte(c-'0')
			default:
				b = b*16 + byte(c-'a') + 10
			}
		}
		out = append(out, b)
	}
	return out, nil
}

// 在线验证：真实 CreateQRCode + 真实 MQTT 建连与订阅（不需要扫码——
// 订阅成功会置位会话的 ready 标记）。需要联网。
func TestLiveQQMobileChannel(t *testing.T) {
	if os.Getenv("LIVE_NET_TEST") == "" {
		t.Skip("set LIVE_NET_TEST=1")
	}
	orig := qqMobileWatchStart
	t.Cleanup(func() { qqMobileWatchStart = orig })
	qqMobileWatchStart = func(id string) { go qqMobileWatch(id) }

	qr, err := QQMobileCreateQR()
	if err != nil {
		t.Fatalf("CreateQRCode: %v", err)
	}
	t.Logf("qrcodeID=%s image=%d bytes", qr.Key[:16], len(qr.Image))

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		s := qqMobileLoad(qr.Key)
		if s == nil {
			t.Fatal("会话丢失")
		}
		if s.err != "" {
			t.Fatalf("MQTT 通道异常: %s", s.err)
		}
		if s.ready {
			t.Logf("MQTT 已建连并订阅成功（state=%s）", s.state)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("20 秒内 MQTT 未完成建连与订阅")
}

// 保活回归（2.1.5 真机教训：订阅后 61 秒被 broker 掐死，因为心跳从没发出去）。
// 订阅成功后静默等 80 秒，连接必须还活着（会话无 err）。
func TestLiveQQMobileKeepAlive(t *testing.T) {
	if os.Getenv("LIVE_NET_TEST") == "" || os.Getenv("LIVE_KEEPALIVE_TEST") == "" {
		t.Skip("set LIVE_NET_TEST=1 LIVE_KEEPALIVE_TEST=1")
	}
	qr, err := QQMobileCreateQR()
	if err != nil {
		t.Fatalf("CreateQRCode: %v", err)
	}
	t.Cleanup(func() { qqMobileDrop(qr.Key) })

	ready := false
	for i := 0; i < 40 && !ready; i++ {
		time.Sleep(500 * time.Millisecond)
		s := qqMobileLoad(qr.Key)
		if s == nil {
			t.Fatal("会话丢失")
		}
		if s.err != "" {
			t.Fatalf("建连失败: %s", s.err)
		}
		ready = s.ready
	}
	if !ready {
		t.Fatal("20 秒内未完成订阅")
	}
	t.Logf("订阅完成，静默等 80 秒验证心跳保活…")
	time.Sleep(80 * time.Second)
	s := qqMobileLoad(qr.Key)
	if s == nil {
		t.Fatal("80 秒后会话丢失")
	}
	if s.err != "" {
		t.Fatalf("保活失败（broker 已掐线）: %s", s.err)
	}
	t.Logf("80 秒后连接仍然存活 ✅")
}
