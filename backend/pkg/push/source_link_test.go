package push

import (
	"os"
	"testing"
)

// 「手动链接来源」这条路径没有别的覆盖手段：
// 后端不落库、preview 又会卡在飞牛匹配（开发环境没有飞牛），
// 所以保留一个可手动开启的联网集成测试。
//
// 默认跳过 —— 它依赖网易云 / QQ 的公网接口，离线或 CI 环境会假失败。
// 需要时：LIVE_NET_TEST=1 go test ./pkg/push/ -run LinkLive -v
func TestFetchSourceByLinkLive(t *testing.T) {
	if os.Getenv("LIVE_NET_TEST") == "" {
		t.Skip("需要真实网络；设置 LIVE_NET_TEST=1 后运行")
	}

	cases := []struct {
		name string
		link string
	}{
		{"网易云歌单", "https://music.163.com/#/playlist?id=3778678"},
		{"QQ 歌单", "https://y.qq.com/n/ryqq/playlist/7826676216"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src, err := FetchSourceByLink(c.link)
			if err != nil {
				t.Fatalf("抓取失败：%v", err)
			}
			if src.Title == "" {
				t.Error("歌单名为空")
			}
			if len(src.Tracks) == 0 {
				t.Fatal("没有抓到曲目")
			}
			first := src.Tracks[0]
			t.Logf("歌单=%q 曲目数=%d | 首条=%q / %q / %ds",
				src.Title, len(src.Tracks), first.Name, first.Artist, first.Duration)
			if first.Name == "" {
				t.Error("首条曲目没有名字")
			}
			if first.Duration <= 0 {
				t.Error("首条曲目时长为 0，时长解析可能不对")
			}
		})
	}
}

// 坏输入要给出可读错误（这部分不需要网络）
func TestFetchSourceByLinkBadInput(t *testing.T) {
	if _, err := FetchSourceByLink("https://example.com/playlist/123"); err == nil {
		t.Error("不认识的平台应报错")
	}
	if _, err := FetchSourceByLink(""); err == nil {
		t.Error("空链接应报错")
	}
	if _, err := FetchSourceByLink("周杰伦 晴天"); err == nil {
		t.Error("非链接文本应报错")
	}
}
