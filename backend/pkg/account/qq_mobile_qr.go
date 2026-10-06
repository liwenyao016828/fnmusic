package account

// QQ 音乐「APP 扫码登录」通道 —— 官方网页 (y.qq.com) 自己用的那条：
//
//  1. musicu.fcg  music.login.LoginServer.CreateQRCode   → qrcodeID + 二维码图片
//  2. MQTT 5.0 over WSS  wss://mu.y.qq.com/ws/handshake
//     CONNECT 属性：AuthMethod=pass + User(tmeAppID/business/hashTag/clientTag/userID)
//     （首跳必回 0x9D Server Moved，按 ServerReference 换路径重连，最多 3 次）
//     SUBSCRIBE management.qrcode_login/{qrcodeID}（User: authorization=tmelogin, pubsub=unicast）
//  3. 服务端推送事件（type 用户属性）：
//     scanned / canceled / timeout / loginFailed / cookies
//     cookies 事件 → music.login.LoginServer.Login{musicid, qrCodeID, token}（tmeLoginType=6）→ 凭据
//
// 为什么加这条通道（2026-09-19）：QQ 互联通道（ptqrshow→check_sig）被腾讯风控
// 拒发 p_skey（p_skey_forbid），与可用参考实现逐项一致仍被拒 —— 账号/环境级拒绝，
// 代码侧无法修。本通道凭据由服务端**推送**下发，完全绕开 check_sig 那一环。
// 实现逐项对照活跃维护的 QQMusicApi（L-1124）login.py 的 mobile 分支。
//
// MQTT 客户端为什么手写（2026-09-20 在线验证）：同一 broker 同一参数，
// Node mqtt.js 与手写报文都能收到 SUBACK，paho.golang 的 SUBSCRIBE 却被直接断连。
// 见 qq_mqtt5.go。
//
// 通道选择：QQCreateQR 优先走本通道，失败回退旧 QQ 互联通道；QQCheckQR 按 key
// 自动分派（两把 key 的命名空间不同，不会撞）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 变量以便测试注入。
var (
	qqMqttURL = "wss://mu.y.qq.com/ws/handshake"
	// qqMobileWatchStart 便于测试替换（真实实现是后台 goroutine）
	qqMobileWatchStart = func(qrcodeID string) { go qqMobileWatch(qrcodeID) }
)

// ── 会话状态 ──

// 移动通道的扩展状态（与 QQState* 并存）。
const (
	QQStateRefused = "refused" // 用户在手机上点了取消
	QQStateFailed  = "failed"  // 服务端报登录失败
)

type qqMobileSession struct {
	state   string
	ready   bool // MQTT 已连接且订阅成功（在线验证与前端可观测点）
	err     string
	cookies map[string]string
	until   time.Time
}

var (
	qqMobileMu       sync.Mutex
	qqMobileSessions = map[string]*qqMobileSession{}
)

const qqMobileTTL = 6 * time.Minute

func qqMobileSave(id string, s *qqMobileSession) {
	qqMobileMu.Lock()
	defer qqMobileMu.Unlock()
	now := time.Now()
	for k, old := range qqMobileSessions {
		if now.After(old.until) {
			delete(qqMobileSessions, k)
		}
	}
	s.until = now.Add(qqMobileTTL)
	qqMobileSessions[id] = s
}

func qqMobileLoad(id string) *qqMobileSession {
	qqMobileMu.Lock()
	defer qqMobileMu.Unlock()
	s, ok := qqMobileSessions[id]
	if !ok || time.Now().After(s.until) {
		return nil
	}
	return s
}

func qqMobileDrop(id string) {
	qqMobileMu.Lock()
	defer qqMobileMu.Unlock()
	delete(qqMobileSessions, id)
}

// qqMobileUpdate 原子更新一个会话；不存在时忽略。
func qqMobileUpdate(id string, fn func(*qqMobileSession)) {
	qqMobileMu.Lock()
	defer qqMobileMu.Unlock()
	if s, ok := qqMobileSessions[id]; ok {
		fn(s)
	}
}

// ── 创建二维码 ──

// QQMobileCreateQR 申请「QQ音乐 App 扫码」二维码。
func QQMobileCreateQR() (QQLogin, error) {
	data, err := qqMusicRequestWith(map[string]any{"ct": 23, "cv": 0}, nil,
		"music.login.LoginServer", "CreateQRCode",
		// param 里的 ct/cv 声明发起方客户端类型（安卓 profile，对照 QQMusicApi 的
		// _build_version_params）；缺了它服务端会建出「未知客户端」的二维码会话，
		// App 扫开后确认页空白（2026-09-20 真机现象）。
		map[string]any{"tmeAppID": "qqmusic", "ct": 11, "cv": 14090008}, 0)
	if err != nil {
		return QQLogin{}, err
	}
	qrcodeID := strField(data, "qrcodeID")
	image := strField(data, "qrcode")
	if qrcodeID == "" || image == "" {
		return QQLogin{}, fmt.Errorf("QQ音乐未返回二维码（qrcodeID=%q）", qrcodeID)
	}

	qqMobileSave(qrcodeID, &qqMobileSession{state: QQStateWaiting, cookies: map[string]string{}})
	qqMobileWatchStart(qrcodeID)
	log.Printf("[QQ扫码] APP 通道二维码已申请（qrcodeID=%s…）", qqShortID(qrcodeID))
	return QQLogin{Key: qrcodeID, Image: image}, nil
}

// ── 状态查询 ──

// QQMobileCheckQR 查询 APP 通道扫码状态。
func QQMobileCheckQR(id string) (QQCheckResult, error) {
	s := qqMobileLoad(id)
	if s == nil {
		return QQCheckResult{}, errors.New("二维码会话不存在或已过期，请重新获取二维码")
	}
	out := QQCheckResult{State: s.state, Cookies: s.cookies}
	if s.err != "" {
		return out, errors.New(s.err)
	}
	return out, nil
}

// ── MQTT 监听 ──

// qqMobileWatch 建立 MQTT 订阅并后台更新会话状态，直到出现终态、超时或出错。
func qqMobileWatch(qrcodeID string) {
	ctx, cancel := context.WithTimeout(context.Background(), qqMobileTTL)
	defer cancel()

	done := make(chan struct{})
	terminalOnce := sync.Once{}
	terminal := func() { terminalOnce.Do(func() { close(done) }) }

	client, err := qqMqttOpen(ctx, qrcodeID)
	if err != nil {
		log.Printf("[QQ扫码] MQTT 建连失败: %v", err)
		qqMobileUpdate(qrcodeID, func(s *qqMobileSession) {
			if s.state == QQStateWaiting || s.state == QQStateScanned {
				s.err = "与 QQ 登录服务的连接建立失败，请重新获取二维码"
			}
		})
		return
	}
	defer client.Close()
	log.Printf("[QQ扫码] MQTT 已连接并订阅 management.qrcode_login/%s…", qqShortID(qrcodeID))
	qqMobileUpdate(qrcodeID, func(s *qqMobileSession) { s.ready = true })

	// 心跳必须独立 goroutine 发 —— 之前把定时器放在阻塞读的同一轮 select 里，
	// 没有事件时定时器永远轮不到，broker 按 keepalive(45s)×1.5 判死，
	// 2.1.5 真机实测「订阅成功 → 61 秒后 EOF」。websocket 允许一读一写并发。
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-t.C:
				if err := client.Ping(pingCtx); err != nil {
					return
				}
			}
		}
	}()

	for {
		pkt, err := client.readPacket(ctx)
		if err != nil {
			// 终态事件已处理完（terminal 关掉了 done）→ 正常退出
			select {
			case <-done:
				return
			default:
			}
			if ctx.Err() != nil {
				qqMobileUpdate(qrcodeID, func(s *qqMobileSession) {
					if s.state == QQStateWaiting || s.state == QQStateScanned {
						s.state = QQStateExpired
						s.err = "二维码已过期，请重新获取"
					}
				})
				client.Disconnect()
				return
			}
			log.Printf("[QQ扫码] MQTT 读取失败: %v", err)
			qqMobileUpdate(qrcodeID, func(s *qqMobileSession) {
				if s.state != QQStateSuccess && s.err == "" {
					s.err = "与 QQ 登录服务的连接中断，请重新获取二维码"
				}
			})
			return
		}
		if pkt.typ == 0x30 && pkt.publish != nil {
			eventType := pkt.publish.User["type"]
			qqMobileHandlePublish(qrcodeID, eventType, pkt.publish.Payload, terminal)
			// 终态事件立刻收工，不再空等下一包
			select {
			case <-done:
				client.Disconnect()
				return
			default:
			}
		}
	}
}

// qqMqttOpen 拨号 + CONNECT（跟随 0x9C/0x9D 换节点）+ SUBSCRIBE。
func qqMqttOpen(ctx context.Context, qrcodeID string) (*mqtt5Client, error) {
	host, wsPath, found := strings.Cut(strings.TrimPrefix(qqMqttURL, "wss://"), "/")
	if !found {
		return nil, errors.New("qqMqttURL 缺少握手路径")
	}
	wsPath = "/" + wsPath
	header := http.Header{
		"Origin":     {"https://y.qq.com"},
		"Referer":    {"https://y.qq.com/"},
		"User-Agent": {qqWebUA},
	}
	user := []mqtt5UserProp{
		{K: "tmeAppID", V: "qqmusic"},
		{K: "business", V: "management"},
		{K: "hashTag", V: qrcodeID},
		{K: "clientTag", V: "management.user"},
		{K: "userID", V: qrcodeID},
	}
	clientID := fmt.Sprintf("%d%04d", time.Now().UnixMilli(), time.Now().UnixNano()%10000)

	for redirects := 0; redirects < 3; redirects++ {
		client, err := mqtt5Dial(ctx, "wss://"+host+wsPath, header)
		if err != nil {
			return nil, err
		}
		ca, err := client.Connect(ctx, clientID, 45, "pass", user)
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("MQTT CONNECT 失败: %w", err)
		}
		if ca.ReasonCode == 0x9C || ca.ReasonCode == 0x9D {
			client.Close()
			if ca.ServerRef == "" {
				return nil, fmt.Errorf("MQTT 要求换节点但没给 ServerReference（reason=0x%X）", ca.ReasonCode)
			}
			wsPath = mqtt5JoinPath(wsPath, ca.ServerRef)
			log.Printf("[QQ扫码] MQTT 换节点，改走 %s", wsPath)
			continue
		}
		if ca.ReasonCode >= 0x80 {
			client.Close()
			return nil, fmt.Errorf("MQTT CONNECT 被拒 reason=0x%X %s", ca.ReasonCode, ca.ReasonString)
		}
		rc, err := client.Subscribe(ctx, "management.qrcode_login/"+qrcodeID, []mqtt5UserProp{
			{K: "authorization", V: "tmelogin"},
			{K: "pubsub", V: "unicast"},
		})
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("MQTT 订阅失败: %w", err)
		}
		if rc >= 0x80 {
			client.Close()
			return nil, fmt.Errorf("MQTT 订阅被拒 reason=0x%X", rc)
		}
		return client, nil
	}
	return nil, errors.New("MQTT 节点重定向次数超限")
}

// qqMobileHandlePublish 解析一条推送事件并更新会话。
func qqMobileHandlePublish(qrcodeID, eventType string, raw []byte, terminal func()) {
	var payload map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			log.Printf("[QQ扫码] 无法解析推送载荷（type=%s）: %v", eventType, err)
			return
		}
	}

	switch eventType {
	case "scanned":
		qqMobileUpdate(qrcodeID, func(s *qqMobileSession) { s.state = QQStateScanned })
	case "canceled":
		qqMobileUpdate(qrcodeID, func(s *qqMobileSession) {
			s.state = QQStateRefused
			s.err = "已在手机上取消登录"
		})
		terminal()
	case "timeout":
		qqMobileUpdate(qrcodeID, func(s *qqMobileSession) {
			s.state = QQStateExpired
			s.err = "二维码已过期，请重新获取"
		})
		terminal()
	case "loginFailed":
		qqMobileUpdate(qrcodeID, func(s *qqMobileSession) {
			s.state = QQStateFailed
			s.err = "QQ音乐报告登录失败，请重试"
		})
		terminal()
	case "cookies":
		cookies, err := qqMobileExchange(qrcodeID, payload)
		qqMobileUpdate(qrcodeID, func(s *qqMobileSession) {
			if err != nil {
				s.state = QQStateFailed
				s.err = err.Error()
			} else {
				s.state = QQStateSuccess
				s.cookies = cookies
			}
		})
		terminal()
	default:
		log.Printf("[QQ扫码] 收到未知事件 type=%q", eventType)
	}
}

// qqMobileExchange 用 cookies 事件里的 uin/token 调 Login 换正式凭据。
func qqMobileExchange(qrcodeID string, payload map[string]any) (map[string]string, error) {
	cookiesTree, _ := payload["cookies"].(map[string]any)
	if cookiesTree == nil {
		return nil, errors.New("QQ音乐推送缺少 cookies 数据")
	}
	uinField, _ := cookiesTree["qqmusic_uin"].(map[string]any)
	keyField, _ := cookiesTree["qqmusic_key"].(map[string]any)
	uin := strField(uinField, "value")
	key := strField(keyField, "value")
	if uin == "" || key == "" {
		return nil, errors.New("QQ音乐推送的凭据缺少 uin/key")
	}
	uinNum, err := strconv.Atoi(strings.TrimPrefix(uin, "o"))
	if err != nil {
		return nil, fmt.Errorf("凭据 uin 无法解析: %q", uin)
	}

	data, err := qqMusicRequestWith(map[string]any{"tmeLoginType": 6}, nil,
		"music.login.LoginServer", "Login",
		map[string]any{"musicid": uinNum, "qrCodeID": qrcodeID, "token": key}, 6)
	if err != nil {
		return nil, err
	}
	cookies := credentialToSession(data, 6, uin)
	if qqAccountID(cookies) == "" || strField(data, "musickey") == "" {
		return nil, errors.New("QQ音乐登录响应缺少凭据")
	}
	log.Printf("[QQ扫码] APP 通道登录成功（uin=%s）", qqAccountID(cookies))
	return cookies, nil
}

// qqShortID 日志用的短 ID（避免长 qrcodeID 刷屏，也避免测试短 ID 越界）。
func qqShortID(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
