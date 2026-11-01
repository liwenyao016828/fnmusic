package backenddl

import (
	"context"
	"strings"
	"testing"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/lxnode"
	"fn-lx-player/pkg/online"
)

// 「有没有可用的取音渠道」这个判定（2026-11-01，issue #11）。
//
// 用户报的原话：「添加了 11 个洛雪源，全都测试正常，但是下载就全失败，提示没有任何
// 可用的取音渠道」。测速跑在**浏览器**里，跟服务端宿主一点关系没有 —— 洛雪源全绿
// 完全不能说明后台能取音。所以这个判定必须只看**必需渠道**（原生 / musicdl），
// 洛雪宿主只算增益；反过来，用户把必需渠道全关、只留洛雪时，宿主 ready 就是唯一
// 能取音的路，那种情况必须算能用。
func TestAvailabilityLxAloneDoesNotSatisfyReady(t *testing.T) {
	// 只开洛雪、宿主没起来、原生解析器还在（默认渠道含 native 时永远会 ready，
	// 所以这里把渠道裁到只剩 lx 才复现得出用户看到的那条提示）。
	cfg := FetcherConfig{
		Pool:     online.NewPool(),
		Channels: func() []string { return []string{config.DLChannelLx} },
		Lx:       nil,
	}
	av := cfg.Availability()
	if av.Ready {
		t.Fatal("只有洛雪一条渠道且宿主没起来时不该判成可用")
	}
	if !strings.Contains(av.Reason, "洛雪") {
		t.Fatalf("原因链要说出是洛雪那条断了，得到 %q", av.Reason)
	}
	// 必须给出**可操作**的指引，而不是只有一句「没有任何可用的取音渠道」。
	if !strings.Contains(av.Reason, "音源管理") {
		t.Fatalf("原因链要给出去哪儿打开的指引，得到 %q", av.Reason)
	}
	for _, cs := range av.Channels {
		if cs.ID == config.DLChannelLx && !cs.Optional {
			t.Fatal("洛雪渠道该被标成可选（Optional）")
		}
	}
}

// 必需渠道里任一个能用 → 整体可用，哪怕洛雪没开。
func TestAvailabilityRequiredChannelWinsOverLxOff(t *testing.T) {
	cfg := FetcherConfig{
		Pool:     online.NewPool(),
		Channels: func() []string { return []string{config.DLChannelLx, config.DLChannelNative} },
	}
	if av := cfg.Availability(); !av.Ready {
		t.Fatalf("原生可用就该整体可用，得到 %+v", av)
	}
}

// 用户把必需渠道全关、只留洛雪且宿主 ready → 可用（否则明明下得动却全被跳过）。
func TestAvailabilityLxOnlyIsReadyWhenHostRuns(t *testing.T) {
	cfg := FetcherConfig{
		Channels: func() []string { return []string{config.DLChannelLx} },
		Lx:       readyLxHost{},
	}
	if av := cfg.Availability(); !av.Ready {
		t.Fatalf("只留洛雪且宿主就绪时该判可用，得到 %+v", av)
	}
}

// 一条路都没有时，原因链要把「哪条断了」全说出来（用户拿去就能动手）。
func TestNoChannelReasonNamesEveryBrokenPath(t *testing.T) {
	cfg := FetcherConfig{
		Channels: func() []string {
			return []string{config.DLChannelLx, config.DLChannelMusicDL, config.DLChannelNative}
		},
	}
	got := cfg.noChannelReason()
	for _, want := range []string{"原生", "musicdl", "洛雪", "音源管理"} {
		if !strings.Contains(got, want) {
			t.Fatalf("原因链该提到 %q，得到 %q", want, got)
		}
	}
}

// readyLxHost 是「宿主已就绪」的最小假实现。
type readyLxHost struct{}

func (readyLxHost) Status() map[string]any { return map[string]any{"state": "ready"} }

func (readyLxHost) SearchAll(context.Context, string, int) ([]lxnode.Song, error) {
	return nil, nil
}

func (readyLxHost) ResolveCandidates(context.Context, string, map[string]any, string, []string) ([]lxnode.Candidate, error) {
	return nil, nil
}
