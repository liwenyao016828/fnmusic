package intercept

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// 凭据指纹算法重建后**不能让老数据失联**。
//
// 真机上已经有 `favorites/fp-485d391520ecf02c.json` 这样的文件。如果重建出来的指纹
// 算不到同一个键，用户就会看到「我的在线收藏全没了」—— 文件其实还在，只是找不着。
// 所以构造时对账：目录里恰好只有一个 fp-*.json 就沿用它的键。
func TestAdoptLegacyUserKey(t *testing.T) {
	dir := t.TempDir()
	// ⚠️ 按**生产约定**布置：Config.DataDir 就是 online 目录本身
	// （main.go 传 filepath.Join(*dataDir, "online")），favorites 在它下面。
	onlineDir := filepath.Join(dir, "online")
	favDir := filepath.Join(onlineDir, "favorites")
	if err := os.MkdirAll(favDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "fp-485d391520ecf02c"
	if err := os.WriteFile(filepath.Join(favDir, legacy+".json"), []byte(`{"items":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	it := New(Config{DataDir: onlineDir, Upstream: stubUpstream{}, Logf: func(string, ...any) {}})
	if it == nil {
		t.Fatal("该构造出拦截层")
	}
	if got := it.userKey(&http.Request{Header: http.Header{}}); got != legacy {
		t.Fatalf("⚠️ 该沿用磁盘上已有的键 %q，得到 %q（老收藏会看不见）", legacy, got)
	}
}

// 有多个既有键时**不猜** —— 多用户环境下宁可不采纳，也不要让两个人共用一个桶。
func TestAdoptLegacyUserKeyRefusesAmbiguity(t *testing.T) {
	dir := t.TempDir()
	onlineDir := filepath.Join(dir, "online")
	favDir := filepath.Join(onlineDir, "favorites")
	if err := os.MkdirAll(favDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"fp-aaaaaaaaaaaaaaaa", "fp-bbbbbbbbbbbbbbbb"} {
		if err := os.WriteFile(filepath.Join(favDir, k+".json"), []byte(`{"items":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	it := New(Config{DataDir: onlineDir, Upstream: stubUpstream{}, Logf: func(string, ...any) {}})
	if it.legacyUserKey != "" {
		t.Fatalf("多个候选时不该采纳：%q", it.legacyUserKey)
	}
	// 没有凭据 → 共享桶（与既有行为一致）
	if got := it.userKey(&http.Request{Header: http.Header{}}); got != "shared" {
		t.Fatalf("无凭据该用 shared，得到 %q", got)
	}
}

type stubUpstream struct{}

func (stubUpstream) Do(*http.Request) (*http.Response, error) { return nil, nil }
