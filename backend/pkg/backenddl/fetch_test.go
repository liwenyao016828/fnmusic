package backenddl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/match"
	"fn-lx-player/pkg/online"
)

// 取音这一层的测试盯三件最容易出错、又最伤用户的事：
// ① 把 Live/伴奏版当成原曲下下来；② 声明无损、实际 128k；③ 本机接口回了
// 「失败」却被当成成功（列表显示已下好，盘上没有文件）。

func TestCandidateOKRejectsVariantsAndPreviews(t *testing.T) {
	want := match.Catalog{Name: "晴天", Artist: "周杰伦", Duration: 269}
	cases := []struct {
		name string
		cand match.Catalog
		ok   bool
	}{
		{"同一版", match.Catalog{Name: "晴天", Artist: "周杰伦", Duration: 269}, true},
		{"Live 版", match.Catalog{Name: "晴天 (Live)", Artist: "周杰伦", Duration: 269}, false},
		{"伴奏版", match.Catalog{Name: "晴天 (伴奏)", Artist: "周杰伦", Duration: 269}, false},
		{"翻唱", match.Catalog{Name: "晴天", Artist: "某翻唱歌手", Duration: 269}, false},
		{"试听 30 秒", match.Catalog{Name: "晴天", Artist: "周杰伦", Duration: 30}, false},
		{"时长差 60 秒", match.Catalog{Name: "晴天", Artist: "周杰伦", Duration: 329}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := candidateOK(want, tc.cand); got != tc.ok {
				t.Fatalf("candidateOK = %v，想要 %v", got, tc.ok)
			}
		})
	}
}

func TestPickLxSongPrefersHighestQuality(t *testing.T) {
	it := Item{Name: "晴天", Singer: "周杰伦", Duration: 269}
	songs := []lxnode.Song{
		{Name: "晴天", Singer: "周杰伦", Duration: 269, Quality: "128k", Size: 4 << 20},
		{Name: "晴天", Singer: "周杰伦", Duration: 269, Quality: "flac", Size: 30 << 20},
		{Name: "晴天", Singer: "周杰伦", Duration: 269, Quality: "320k", Size: 10 << 20},
		// 高音质但 Live：不能因为音质高就选它。
		{Name: "晴天 (Live)", Singer: "周杰伦", Duration: 269, Quality: "flac", Size: 40 << 20},
	}
	got := pickLxSong(songs, it)
	if got == nil || got.Quality != "flac" || got.Name != "晴天" {
		t.Fatalf("应挑正式版的最高档，得到 %+v", got)
	}
}

func TestTierFromSizeOnlyDowngrades(t *testing.T) {
	// 295 秒给 4.1MB ≈ 113kbps：声称无损必须降到 128k。
	if got := tierFromSize(4<<20, 295, online.TierLossless); got != online.Tier128k {
		t.Fatalf("4MB/295s 应降到 128k，得到 %q", got)
	}
	// 真实无损（约 50MB/295s）保持。
	if got := tierFromSize(50<<20, 295, online.TierLossless); got != online.TierLossless {
		t.Fatalf("50MB/295s 不应降档，得到 %q", got)
	}
	// 只降不升：声称 128k、实测像无损也保持 128k。
	if got := tierFromSize(50<<20, 295, online.Tier128k); got != online.Tier128k {
		t.Fatalf("不能凭体积升档，得到 %q", got)
	}
	// 没证据就不改口。
	if got := tierFromSize(0, 295, online.TierLossless); got != online.TierLossless {
		t.Fatalf("没有体积证据不应改档，得到 %q", got)
	}
}

func TestTiersForRespectsRequestedCeiling(t *testing.T) {
	if got := tiersFor("128k"); len(got) != 1 || got[0] != online.Tier128k {
		t.Fatalf("点了 128k 不应尝试无损：%v", got)
	}
	got := tiersFor("highest")
	if len(got) < 2 || got[0] != online.TierLossless {
		t.Fatalf("最高音质应走完整降级阶梯：%v", got)
	}
}

// 本机下载接口只有 code=200 且有 path 才算成功；already_exists 同样成功。
func TestDownloadOneHonestSuccessAndPayload(t *testing.T) {
	dir := t.TempDir()
	flacPath := filepath.Join(dir, "晴天.flac")
	if err := os.WriteFile(flacPath, []byte("fLaC...."), 0o644); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/download/song" {
			t.Errorf("路径不对：%s", r.URL.Path)
		}
		if got := r.Header.Get("X-API-Token"); got != "tok" {
			t.Errorf("没有带 Token：%q", got)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Errorf("请求不是 JSON：%v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{"status": "already_exists", "path": flacPath},
		})
	}))
	defer srv.Close()

	cfg := FetcherConfig{LocalAPI: srv.URL, Token: "tok", Prefs: func() map[string]any {
		return map[string]any{"embed_cover": false}
	}}
	it := Item{Name: "晴天", Singer: "周杰伦", Platform: "wy", PlatformID: "123", Quality: "highest"}
	a := attempt{channel: configDLNative, url: "https://cdn/x.flac", tier: online.TierLossless, source: "wy"}
	res, err := downloadOne(context.Background(), srv.Client(), cfg, it, a)
	if err != nil {
		t.Fatalf("already_exists 应算成功：%v", err)
	}
	if res.Path != flacPath || res.Size == 0 {
		t.Fatalf("结果缺 path/size：%+v", res)
	}
	if res.Quality != online.TierLossless {
		t.Fatalf("落盘扩展名 .flac 应校正为无损，得到 %q", res.Quality)
	}
	if payload["songmid"] != "123" || payload["source"] != "wy" || payload["url"] != "https://cdn/x.flac" {
		t.Fatalf("请求没有带上平台身份/直链：%+v", payload)
	}
	if payload["upgrade_if_better"] != true {
		t.Fatalf("没有要求按更高音质替换：%+v", payload)
	}
	if payload["embed_cover"] != false {
		t.Fatalf("下载偏好没有叠加上去：%+v", payload)
	}
}

func TestDownloadOneRejectsHTTP200WithoutPath(t *testing.T) {
	cases := []map[string]any{
		{"code": 200, "data": map[string]any{"status": "failed", "error": "没空间了"}},
		{"code": 500, "data": map[string]any{"path": "/music/x.flac"}},
	}
	for i, body := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(body)
		}))
		cfg := FetcherConfig{LocalAPI: srv.URL}
		_, err := downloadOne(context.Background(), srv.Client(), cfg, Item{Name: "x"}, attempt{url: "u"})
		srv.Close()
		if err == nil {
			t.Fatalf("case %d：HTTP 200 但 code/path 不成立时必须报错", i)
		}
	}
}

// 常量直接用字符串，避免测试把 config 的实现细节固化（渠道短码是稳定契约）。
const configDLNative = "native"

// ── 跨渠道取音计划 ────────────────────────────────────────────────────

// fakeNative 只给 320k：模拟「平台自己那一档拿不到无损」。
type fakeNative struct{}

func (fakeNative) Platform() string { return "wy" }
func (fakeNative) Resolve(ctx context.Context, id string) (*online.Resolved, error) {
	return nil, online.ErrNotPlayable
}
func (fakeNative) ResolveBatch(ctx context.Context, ids []string) (map[string]*online.Resolved, error) {
	return nil, nil
}
func (fakeNative) ResolveAtTier(ctx context.Context, id, tier string) (*online.Resolved, error) {
	if tier == online.TierLossless {
		return nil, online.ErrTierUnavailable
	}
	return &online.Resolved{URL: "https://native/" + tier, Tier: tier, Format: "mp3", Bitrate: 320000}, nil
}

type fakeLx struct{}

func (fakeLx) Status() map[string]any { return map[string]any{"state": lxnode.StateReady} }
func (fakeLx) SearchAll(ctx context.Context, keyword string, limit int) ([]lxnode.Song, error) {
	return nil, nil
}
func (fakeLx) ResolveCandidates(ctx context.Context, source string, info map[string]any, quality string, scripts []string) ([]lxnode.Candidate, error) {
	return []lxnode.Candidate{{URL: "https://lx/flac", Bytes: 50 << 20}}, nil
}

// 平台只能给 320k 时，不能直接收下——洛雪可能给无损，必须问完再按**实际**
// 档位排序（这是「质量最高不能直接接受低档平台结果」那条铁律）。
func TestPlanAttemptsChecksLxBeforeAcceptingLowerNative(t *testing.T) {
	cfg := FetcherConfig{
		Pool:     online.NewPool(fakeNative{}),
		Lx:       fakeLx{},
		Channels: func() []string { return []string{"native", "lx"} },
		Scripts:  func() []string { return []string{"全豆要"} },
	}
	it := Item{Name: "晴天", Singer: "周杰伦", Platform: "wy", PlatformID: "123", Duration: 269, Quality: "highest"}
	list, err := planAttempts(context.Background(), cfg, it)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 || list[0].channel != "lx" || list[0].tier != online.TierLossless {
		t.Fatalf("应把洛雪无损排在最前，得到 %+v", list)
	}
	foundNative := false
	for _, a := range list {
		if a.channel == "native" && a.tier == online.Tier320k {
			foundNative = true
		}
	}
	if !foundNative {
		t.Fatalf("平台 320k 应留作兜底：%+v", list)
	}
}

// 用户点 128k 时不许偷偷去问无损（省流量的意图要守住）。
func TestPlanAttemptsRespectsLowQualityCeiling(t *testing.T) {
	cfg := FetcherConfig{
		Pool:     online.NewPool(fakeNative{}),
		Lx:       fakeLx{},
		Channels: func() []string { return []string{"native", "lx"} },
	}
	it := Item{Name: "晴天", Singer: "周杰伦", Platform: "wy", PlatformID: "123", Duration: 269, Quality: "128k"}
	list, err := planAttempts(context.Background(), cfg, it)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("没有候选")
	}
	for _, a := range list {
		if online.TierRank(a.tier) > online.TierRank(online.Tier128k) {
			t.Fatalf("点了 128k 却排进更高档：%+v", a)
		}
	}
}
