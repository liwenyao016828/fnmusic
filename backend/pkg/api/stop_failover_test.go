package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fn-lx-player/pkg/config"
	"fn-lx-player/pkg/monitor"
	"fn-lx-player/pkg/push"
)

// 「停止同步」之后，手动执行推送必须拒绝 —— 定时调度那侧本来就有 Enabled 判定，
// 只有这个入口漏了：前端定时器（下载队列排空后的自动推送）还能推上去，
// 用户看到的就是「停止后一会儿又开始推送」。
func TestHandlePushRunRejectsDisabledTask(t *testing.T) {
	ps := push.NewStore(t.TempDir())
	if _, err := ps.Save(push.Task{
		ID: "t1", Name: "订阅同步", Provider: "netease",
		SourceKind: "link", SourceID: "https://music.163.com/playlist?id=1",
		Enabled: false,
	}); err != nil {
		t.Fatalf("写入推送任务失败：%v", err)
	}
	s := &Server{pushStore: ps}

	rec := httptest.NewRecorder()
	s.HandlePushRun(rec, httptest.NewRequest(http.MethodPost, "/api/push/run?id=t1", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("停用的任务应被拒绝（409），实际 %d %s", rec.Code, rec.Body.String())
	}
}

// ResolveURLCandidates 是「取链成功但下载失败 → 换下一个音源」的依据：
// 必须按「音源配置顺序 × 音质降级链」给出**去重后**的多个候选。
func TestResolveURLCandidatesDistinctAndOrdered(t *testing.T) {
	withResolveStub(t, func(protocol, baseURL, token, platform, songID, quality string) (string, error) {
		switch baseURL + "/" + quality {
		case "http://a/flac":
			return "https://cdn/1.flac", nil
		case "http://a/320k":
			return "https://cdn/1-320.mp3", nil
		case "http://b/flac":
			// 与音源 a 的首选同址 —— 去重后不该出现两次，否则「换源」是假换
			return "https://cdn/1.flac", nil
		case "http://b/320k":
			return "https://cdn/2.mp3", nil
		}
		return "", fmt.Errorf("该档位取不到")
	})
	d := &monitorDispatcher{srv: newResolveSrv(t,
		config.APISource{ID: "a", Name: "A", BaseURL: "http://a", Protocol: "lx_server", Enabled: true},
		config.APISource{ID: "b", Name: "B", BaseURL: "http://b", Protocol: "lx_server", Enabled: true},
	)}
	song := monitor.Song{Source: "wy", ID: "1"}
	want := []string{"https://cdn/1.flac", "https://cdn/1-320.mp3", "https://cdn/2.mp3"}

	urls, err := d.ResolveURLCandidates(resolveMon(), song, 3)
	if err != nil {
		t.Fatalf("取候选失败：%v", err)
	}
	if strings.Join(urls, ",") != strings.Join(want, ",") {
		t.Fatalf("候选顺序/去重不符合预期：%v，期望 %v", urls, want)
	}

	// max=1 与 ResolveURL 必须一致 —— 正常取链不该因为多了换源能力就多外呼
	one, err := d.ResolveURLCandidates(resolveMon(), song, 1)
	if err != nil || len(one) != 1 || one[0] != want[0] {
		t.Fatalf("max=1 应只返回首条，实际 %v err=%v", one, err)
	}
	single, err := d.ResolveURL(resolveMon(), song)
	if err != nil || single != want[0] {
		t.Fatalf("ResolveURL 应返回首条，实际 %q err=%v", single, err)
	}
}

// 候选全部取不到时，错误口径要指向「去配音源 / 已尝试什么」，而不是含糊的解析失败。
func TestResolveURLCandidatesAllFailReportsAttempts(t *testing.T) {
	withResolveStub(t, func(protocol, baseURL, token, platform, songID, quality string) (string, error) {
		return "", fmt.Errorf("上游 502")
	})
	d := &monitorDispatcher{srv: newResolveSrv(t,
		config.APISource{ID: "a", Name: "A", BaseURL: "http://a", Protocol: "lx_server", Enabled: true},
	)}
	urls, err := d.ResolveURLCandidates(resolveMon(), monitor.Song{Source: "wy", ID: "1"}, 3)
	if err == nil || len(urls) != 0 {
		t.Fatalf("应返回错误且无候选，实际 %v err=%v", urls, err)
	}
	if !strings.Contains(err.Error(), "上游 502") || !strings.Contains(err.Error(), "已尝试") {
		t.Fatalf("错误应包含原因与已尝试列表，实际 %q", err.Error())
	}
}
