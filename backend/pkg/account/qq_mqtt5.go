package account

// 极简 MQTT 5.0 over WebSocket 客户端 —— 只覆盖 QQ 登录通道需要的报文。
//
// 为什么手写（2026-09-20 在线验证）：同一台腾讯 broker、同样的参数，
// Node mqtt.js 与手写报文都能收到 SUBACK，唯独 paho.golang 的 SUBSCRIBE
// 被直接断连 —— 与其赌库的编码兼容，不如只发参考实现发过的字段。
// 依赖只剩 coder/websocket。
//
// 报文编码对照 MQTT 5.0 规范：
//   CONNECT   = 0x10 | 剩余长度 | "MQTT" 0x05 flags keepalive | 属性 | clientID
//   CONNACK   = 0x20 | 2 字节头(sessionPresent, reasonCode) | 属性
//   SUBSCRIBE = 0x82 | 包标识符 | 属性 | (topic filter + options)
//   SUBACK    = 0x90 | 包标识符 | 属性 | reasonCode...
//   PUBLISH   = 0x30 | [topic] [包标识符(仅QoS>0)] | 属性 | payload
//   PINGREQ   = 0xC0 0x00 / DISCONNECT = 0xE0 | reason | 属性

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/coder/websocket"
)

const (
	mqtt5PropAuthMethod = 0x15
	mqtt5PropServerRef  = 0x1C
	mqtt5PropUser       = 0x26
)

type mqtt5UserProp struct {
	K, V string
}

// mqtt5Connack 解析后的 CONNACK。
type mqtt5Connack struct {
	ReasonCode   byte
	ServerRef    string
	ReasonString string
	User         map[string]string
}

// mqtt5Publish 解析后的下行 PUBLISH。
type mqtt5Publish struct {
	Topic   string
	User    map[string]string
	Payload []byte
}

// mqtt5Client 一条 WebSocket 上的 MQTT5 会话。
type mqtt5Client struct {
	ws   *websocket.Conn
	buf  []byte // 跨帧的半截报文缓冲
	next uint16 // 包标识符
}

// mqtt5Dial 建立 WebSocket（子协议 mqtt），不做 MQTT 握手。
func mqtt5Dial(ctx context.Context, url string, header http.Header) (*mqtt5Client, error) {
	dialOpts := &websocket.DialOptions{
		Subprotocols: []string{"mqtt"},
		HTTPHeader:   header,
		HTTPClient:   &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{}}},
	}
	conn, _, err := websocket.Dial(ctx, url, dialOpts)
	if err != nil {
		return nil, fmt.Errorf("websocket 拨号失败: %w", err)
	}
	conn.SetReadLimit(1 << 20)
	return &mqtt5Client{ws: conn, next: 1}, nil
}

func mqtt5Varint(n int) []byte {
	var out []byte
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

func mqtt5Str(s string) []byte {
	b := make([]byte, 2+len(s))
	binary.BigEndian.PutUint16(b, uint16(len(s)))
	copy(b[2:], s)
	return b
}

// mqtt5UserPropsBlock 把用户属性（可带认证方法）编码成属性块（含长度前缀）。
func mqtt5UserPropsBlock(authMethod string, user []mqtt5UserProp) []byte {
	var body bytes.Buffer
	if authMethod != "" {
		body.WriteByte(mqtt5PropAuthMethod)
		body.Write(mqtt5Str(authMethod))
	}
	for _, up := range user {
		body.WriteByte(mqtt5PropUser)
		body.Write(mqtt5Str(up.K))
		body.Write(mqtt5Str(up.V))
	}
	out := mqtt5Varint(body.Len())
	return append(out, body.Bytes()...)
}

// mqtt5Packet 组装固定头 + 剩余长度 + 体。
func mqtt5Packet(header byte, body []byte) []byte {
	var b bytes.Buffer
	b.WriteByte(header)
	b.Write(mqtt5Varint(len(body)))
	b.Write(body)
	return b.Bytes()
}

// Connect 发送 CONNECT 并等待 CONNACK。
func (c *mqtt5Client) Connect(ctx context.Context, clientID string, keepAlive uint16, authMethod string, user []mqtt5UserProp) (mqtt5Connack, error) {
	var varhdr bytes.Buffer
	varhdr.Write(mqtt5Str("MQTT"))
	varhdr.WriteByte(5)
	varhdr.WriteByte(0x02) // clean start
	varhdr.Write([]byte{byte(keepAlive >> 8), byte(keepAlive & 0xff)})
	varhdr.Write(mqtt5UserPropsBlock(authMethod, user))
	varhdr.Write(mqtt5Str(clientID))

	if err := c.write(ctx, mqtt5Packet(0x10, varhdr.Bytes())); err != nil {
		return mqtt5Connack{}, err
	}
	for {
		pkt, err := c.readPacket(ctx)
		if err != nil {
			return mqtt5Connack{}, err
		}
		if pkt.typ == 0x20 {
			return pkt.connack, nil
		}
		// 连接阶段忽略其他包
	}
}

// Subscribe 发送 SUBSCRIBE 并等待匹配的 SUBACK，返回 reason code。
func (c *mqtt5Client) Subscribe(ctx context.Context, topic string, user []mqtt5UserProp) (byte, error) {
	pid := c.next
	c.next++
	if c.next == 0 {
		c.next = 1
	}
	var body bytes.Buffer
	var pidBuf [2]byte
	binary.BigEndian.PutUint16(pidBuf[:], pid)
	body.Write(pidBuf[:])
	body.Write(mqtt5UserPropsBlock("", user))
	body.Write(mqtt5Str(topic))
	body.WriteByte(0x00) // QoS 0

	if err := c.write(ctx, mqtt5Packet(0x82, body.Bytes())); err != nil {
		return 0, err
	}
	for {
		pkt, err := c.readPacket(ctx)
		if err != nil {
			return 0, err
		}
		if pkt.typ == 0x90 && pkt.subackID == pid {
			if len(pkt.subackReasons) == 0 {
				return 0, errors.New("SUBACK 没有 reason code")
			}
			return pkt.subackReasons[0], nil
		}
		if pkt.typ == 0x30 && pkt.publish != nil {
			// 订阅确认前的推送极少见；交给调用方处理更安全 —— 这里直接报错让上层决定
			return 0, fmt.Errorf("等待 SUBACK 时收到 PUBLISH（topic=%s）", pkt.publish.Topic)
		}
	}
}

// Ping 发送 PINGREQ。
func (c *mqtt5Client) Ping(ctx context.Context) error {
	return c.write(ctx, []byte{0xC0, 0x00})
}

// Disconnect 正常告别。
func (c *mqtt5Client) Disconnect() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = c.write(ctx, mqtt5Packet(0xE0, []byte{0x00}))
}

func (c *mqtt5Client) Close() {
	c.ws.Close(websocket.StatusNormalClosure, "")
}

func (c *mqtt5Client) write(ctx context.Context, data []byte) error {
	return c.ws.Write(ctx, websocket.MessageBinary, data)
}

// mqtt5PacketHead 一个已解析的报文。
type mqtt5PacketHead struct {
	typ           byte
	connack       mqtt5Connack
	subackID      uint16
	subackReasons []byte
	publish       *mqtt5Publish
}

// readPacket 从 WS 帧流里取一个完整 MQTT 报文（处理跨帧）。
func (c *mqtt5Client) readPacket(ctx context.Context) (mqtt5PacketHead, error) {
	for {
		if len(c.buf) > 0 {
			if pkt, n, err := mqtt5Parse(c.buf); err == nil {
				c.buf = c.buf[n:]
				return pkt, nil
			} else if !errors.Is(err, errMQTT5Short) {
				return mqtt5PacketHead{}, err
			}
		}
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return mqtt5PacketHead{}, err
		}
		if os.Getenv("QQ_MQTT_DEBUG") != "" {
			log.Printf("[mqtt5] 帧 %d 字节: % X", len(data), data)
		}
		c.buf = append(c.buf, data...)
	}
}

var errMQTT5Short = errors.New("mqtt5: 报文不完整")

// mqtt5Parse 解析一个完整报文，返回它占用的字节数。
func mqtt5Parse(raw []byte) (mqtt5PacketHead, int, error) {
	if len(raw) < 2 {
		return mqtt5PacketHead{}, 0, errMQTT5Short
	}
	typ := raw[0]
	remaining, rl := mqtt5DecodeVarint(raw[1:])
	if rl == 0 {
		return mqtt5PacketHead{}, 0, errMQTT5Short
	}
	total := 1 + rl + remaining
	if len(raw) < total {
		return mqtt5PacketHead{}, 0, errMQTT5Short
	}
	body := raw[1+rl : total]
	out := mqtt5PacketHead{typ: typ & 0xF0}
	r := bytes.NewReader(body)

	switch out.typ {
	case 0x20: // CONNACK
		r.ReadByte() // session present flags
		rc, _ := r.ReadByte()
		out.connack.ReasonCode = rc
		props := mqtt5DecodeProps(r)
		out.connack.ServerRef = props.serverRef
		out.connack.ReasonString = props.reasonString
		out.connack.User = props.user
	case 0x90: // SUBACK
		var pid [2]byte
		if _, err := io.ReadFull(r, pid[:]); err != nil {
			return out, total, nil
		}
		out.subackID = binary.BigEndian.Uint16(pid[:])
		mqtt5DecodeProps(r) // 跳过属性
		for r.Len() > 0 {
			b, err := r.ReadByte()
			if err != nil {
				break
			}
			out.subackReasons = append(out.subackReasons, b)
		}
	case 0x30: // PUBLISH
		topic := mqtt5ReadStr(r)
		qos := (raw[0] >> 1) & 0x03
		if qos > 0 {
			var pid [2]byte
			io.ReadFull(r, pid[:])
		}
		props := mqtt5DecodeProps(r)
		payload := make([]byte, r.Len())
		r.Read(payload)
		out.publish = &mqtt5Publish{Topic: topic, User: props.user, Payload: payload}
	case 0xD0: // PINGRESP — 忽略
	default:
		// 其他类型（AUTH 等）忽略但按长度消费
	}
	return out, total, nil
}

type mqtt5Props struct {
	serverRef    string
	reasonString string
	authMethod   string
	user         map[string]string
}

// mqtt5DecodeProps 按 MQTT5 规范逐类型消费属性字节（编码表对照 paho packets/properties.go）。
// 注意：属性段前缀是**字节长度**，不是属性条数 —— 按消耗字节数控制循环。
func mqtt5DecodeProps(r *bytes.Reader) mqtt5Props {
	out := mqtt5Props{user: map[string]string{}}
	n := mqtt5ReadVarint(r)
	consumed := 0
	for consumed < n && r.Len() > 0 {
		before := r.Len()
		id, err := r.ReadByte()
		if err != nil {
			return out
		}
		switch id {
		case 0x01, 0x17, 0x19, 0x24, 0x25, 0x28, 0x29, 0x2A: // 单字节
			r.ReadByte()
		case 0x13, 0x21, 0x22, 0x23: // 2 字节
			r.Seek(2, io.SeekCurrent)
		case 0x02, 0x11, 0x18, 0x27: // 4 字节
			r.Seek(4, io.SeekCurrent)
		case 0x0B: // 变长整数
			mqtt5ReadVarint(r)
		case 0x09, 0x16: // 二进制（长度前缀）
			l := mqtt5ReadUint16(r)
			r.Seek(int64(l), io.SeekCurrent)
		case 0x26: // 字符串对
			k := mqtt5ReadStr(r)
			v := mqtt5ReadStr(r)
			out.user[k] = v
		case 0x03, 0x08, 0x12, 0x15, 0x1A, 0x1C, 0x1F: // 字符串
			s := mqtt5ReadStr(r)
			switch id {
			case 0x15:
				out.authMethod = s
			case 0x1C:
				out.serverRef = s
			case 0x1F:
				out.reasonString = s
			}
		default:
			// 未知属性：无法安全跳变，停止解析（保守）
			return out
		}
		consumed += before - r.Len()
	}
	return out
}

func mqtt5ReadUint16(r *bytes.Reader) uint16 {
	var b [2]byte
	r.Read(b[:])
	return binary.BigEndian.Uint16(b[:])
}

func mqtt5ReadStr(r *bytes.Reader) string {
	if r.Len() < 2 {
		return ""
	}
	var l [2]byte
	r.Read(l[:])
	n := int(binary.BigEndian.Uint16(l[:]))
	if r.Len() < n {
		n = r.Len()
	}
	b := make([]byte, n)
	r.Read(b)
	return string(b)
}

func mqtt5ReadVarint(r *bytes.Reader) int {
	mult, val := 1, 0
	for {
		b, err := r.ReadByte()
		if err != nil {
			return val
		}
		val += int(b&127) * mult
		if b&128 == 0 {
			return val
		}
		mult *= 128
		if mult > 1<<21 {
			return val
		}
	}
}

func mqtt5DecodeVarint(raw []byte) (val int, used int) {
	mult := 1
	for i, b := range raw {
		if i >= 4 {
			return 0, 0
		}
		val += int(b&127) * mult
		if b&128 == 0 {
			return val, i + 1
		}
		mult *= 128
	}
	return 0, 0
}

// mqtt5JoinPath 处理 SERVER_REFERENCE：末段含 ":" 视为节点段替换，否则追加。
func mqtt5JoinPath(path, serverRef string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) > 0 && strings.Contains(parts[len(parts)-1], ":") {
		parts[len(parts)-1] = serverRef
		return strings.Join(parts, "/")
	}
	return strings.TrimRight(path, "/") + "/" + serverRef
}
