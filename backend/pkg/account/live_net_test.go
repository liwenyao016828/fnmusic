package account

import (
	"os"
	"testing"
	"time"
)

func TestLiveQQProbe(t *testing.T) {
	if os.Getenv("LIVE_NET_TEST") == "" {
		t.Skip("set LIVE_NET_TEST=1")
	}
	qr, err := QQCreateQR()
	if err != nil {
		t.Fatalf("QQCreateQR: %v", err)
	}
	t.Logf("qrsig len=%d img=%d bytes", len(qr.Key), len(qr.Image))
	res, err := QQCheckQR(qr.Key)
	if err != nil {
		t.Fatalf("QQCheckQR: %v", err)
	}
	t.Logf("first poll state=%s", res.State)
	if res.State != QQStateWaiting {
		t.Errorf("期望 waiting(66)，实际 %s", res.State)
	}
	time.Sleep(2 * time.Second)
	res2, err := QQCheckQR(qr.Key)
	if err != nil {
		t.Fatalf("QQCheckQR 2: %v", err)
	}
	t.Logf("second poll state=%s", res2.State)
}

// TestLiveNeteaseQROfficialShape 在线验证官方形态的 unikey 申请真的能拿到 key，
// 且伪造 key 轮询返回正常的状态码（800/802 等，而不是报错页）。需要联网。
func TestLiveNeteaseQROfficialShape(t *testing.T) {
	if os.Getenv("LIVE_NET_TEST") == "" {
		t.Skip("set LIVE_NET_TEST=1")
	}
	qr, err := NeteaseQRKey()
	if err != nil {
		t.Fatalf("NeteaseQRKey: %v", err)
	}
	t.Logf("unikey=%s url=%s", qr.Key, qr.URL)
	if qr.Key == "" {
		t.Fatal("未拿到 unikey")
	}
	code, msg, _, err := NeteaseQRCheck(qr.Key)
	if err != nil {
		t.Fatalf("NeteaseQRCheck: %v", err)
	}
	t.Logf("首轮轮询 code=%d msg=%s", code, msg)
	if code != QRWaiting && code != QRExpired {
		t.Errorf("期望 801/800，实际 %d", code)
	}
}
