package search

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 在线歌手 / 原生专辑的**联网**用例（v2.33.0）。
//
// 默认 skip：要打 music.163.com 与 y.qq.com。跑法：
//
//	LIVE_NET_TEST=1 go test ./pkg/search/ -run Live -v
//
// # 为什么值得留一组联网用例
//
// 这几个接口的形状是**二手**的（同 intercept 的专辑用例：开发机连不上官方页面，
// 只能照参考实现 + 实测 curl 反推）。而它们全都属于「平台随时会改」的那一类：
// 参数名换了、字段挪了、路径下线了，症状是「歌手页静默空掉」，本地单测永远是绿的。
// 所以在真网络上钉一遍「拿得到、且关键字段非空」，是这套东西唯一的外部体检。

// liveSplitArtists 只是本文件里数一下歌手名的个数，用的分隔符与
// intercept 的 splitArtists / online.Track.Artist() 保持一致（本项目统一用 "/"）。
func liveSplitArtists(singer string) []string {
	out := make([]string, 0, 2)
	for _, part := range strings.FieldsFunc(singer, func(r rune) bool {
		return r == '/' || r == ',' || r == '，' || r == '、' || r == '&' || r == ';' || r == '；'
	}) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func liveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("LIVE_NET_TEST") == "" {
		t.Skip("联网用例：设 LIVE_NET_TEST=1 才跑")
	}
}

func liveCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLiveSearchArtistNETEASE(t *testing.T) {
	liveGate(t)
	hits := SearchArtistNETEASE(liveCtx(t), "周杰伦", 5)
	if len(hits) == 0 {
		t.Fatal("网易歌手搜索没结果（接口形状可能变了）")
	}
	if len(hits) > 5 {
		t.Fatalf("limit=5 却回了 %d 条", len(hits))
	}
	first := hits[0]
	if first.ID == "" || first.Name == "" {
		t.Fatalf("歌手命中缺 id / 名字：%#v", first)
	}
	if first.Cover == "" {
		t.Fatalf("歌手命中缺头像地址：%#v", first)
	}
	if first.AlbumCount <= 0 || first.TrackCount <= 0 {
		t.Fatalf("专辑数 / 曲目数没解出来：%#v", first)
	}
	t.Logf("歌手搜索首条：%s（%s）专辑 %d / 曲目 %d", first.Name, first.ID, first.AlbumCount, first.TrackCount)
}

func TestLiveFetchArtistNETEASE(t *testing.T) {
	liveGate(t)
	// 4558 是网易云里一个长期稳定的歌手 id；不信它就去搜索里取第一条。
	id := "4558"
	if hits := SearchArtistNETEASE(liveCtx(t), "周杰伦", 1); len(hits) > 0 {
		id = hits[0].ID
	}
	info, err := FetchArtistNETEASE(liveCtx(t), id)
	if err != nil {
		t.Fatalf("歌手详情失败：%v", err)
	}
	if info.Name == "" {
		t.Fatalf("歌手详情没有名字：%#v", info)
	}
	if info.Cover == "" {
		t.Fatalf("歌手详情没有头像：%#v", info)
	}
	t.Logf("歌手详情：%s（%s）简介 %d 字", info.Name, info.ID, len(info.BriefDesc))
}

func TestLiveFetchArtistTracksNETEASE(t *testing.T) {
	liveGate(t)
	id := "4558"
	if hits := SearchArtistNETEASE(liveCtx(t), "周杰伦", 1); len(hits) > 0 {
		id = hits[0].ID
	}
	songs, total, err := FetchArtistTracksNETEASE(liveCtx(t), id, 0, 10, "hot")
	if err != nil {
		t.Fatalf("歌手歌曲失败：%v", err)
	}
	if len(songs) == 0 {
		t.Fatal("歌手歌曲为空（路径或字段可能变了）")
	}
	if total <= 0 {
		t.Fatalf("total 没解出来：%d", total)
	}
	for i, s := range songs {
		if s.Songmid == "" || s.Name == "" {
			t.Fatalf("第 %d 首缺 id / 名字：%#v", i, s)
		}
		if s.Singer == "" {
			t.Fatalf("第 %d 首缺歌手：%#v", i, s)
		}
		if s.AlbumID == "" {
			t.Fatalf("第 %d 首缺专辑 id（专辑页要靠它取完整曲目）：%#v", i, s)
		}
		if len(s.ArtistIDs) != 0 && len(liveSplitArtists(s.Singer)) != len(s.ArtistIDs) {
			t.Fatalf("第 %d 首的歌手 id 与歌手名没对齐：%v / %q", i, s.ArtistIDs, s.Singer)
		}
	}
	t.Logf("歌手歌曲：%d 首（total %d），首条 %s - %s", len(songs), total, songs[0].Name, songs[0].Singer)

	// 换一种排序：不该报错（老实现里 order=true 会静默返回空数组）。
	byTime, _, err := FetchArtistTracksNETEASE(liveCtx(t), id, 0, 5, "time")
	if err != nil {
		t.Fatalf("按时间排序失败：%v", err)
	}
	if len(byTime) == 0 {
		t.Fatal("按时间排序返回空（order 参数口径可能变了）")
	}
}

func TestLiveFetchArtistAlbumsNETEASE(t *testing.T) {
	liveGate(t)
	id := "4558"
	if hits := SearchArtistNETEASE(liveCtx(t), "周杰伦", 1); len(hits) > 0 {
		id = hits[0].ID
	}
	albums, _, err := FetchArtistAlbumsNETEASE(liveCtx(t), id, 0, 10)
	if err != nil {
		t.Fatalf("歌手专辑失败：%v", err)
	}
	if len(albums) == 0 {
		t.Fatal("歌手专辑为空（路径或字段可能变了）")
	}
	for i, a := range albums {
		if a.ID == "" || a.Name == "" {
			t.Fatalf("第 %d 张缺 id / 名字：%#v", i, a)
		}
		if a.Size <= 0 {
			t.Fatalf("第 %d 张缺曲目数：%#v", i, a)
		}
	}
	t.Logf("歌手专辑：%d 张，首张 %s（%d 首）", len(albums), albums[0].Name, albums[0].Size)
}

func TestLiveFetchAlbumDetailNETEASE(t *testing.T) {
	liveGate(t)
	// 32311 是实测能拿到的一张网易专辑；不信它就走「歌手 → 专辑」这条真链路。
	albumID := "32311"
	if hits := SearchArtistNETEASE(liveCtx(t), "周杰伦", 1); len(hits) > 0 {
		if albums, _, err := FetchArtistAlbumsNETEASE(liveCtx(t), hits[0].ID, 0, 1); err == nil && len(albums) > 0 {
			albumID = albums[0].ID
		}
	}
	d, err := FetchAlbumDetailNETEASE(liveCtx(t), albumID)
	if err != nil {
		t.Fatalf("专辑详情失败：%v", err)
	}
	if d.Name == "" || len(d.Tracks) == 0 {
		t.Fatalf("专辑详情为空：%#v", d)
	}
	if d.Size > 0 && len(d.Tracks) > d.Size {
		t.Fatalf("曲目数 %d 超过平台声明的 %d", len(d.Tracks), d.Size)
	}
	for i, s := range d.Tracks {
		if s.Name == "" || s.Songmid == "" {
			t.Fatalf("第 %d 首缺名字 / id：%#v", i, s)
		}
	}
	t.Logf("专辑详情：%s（%s）%d 首", d.Name, d.ID, len(d.Tracks))
}

func TestLiveFetchAlbumDetailQQ(t *testing.T) {
	liveGate(t)
	d, err := FetchAlbumDetailQQ(liveCtx(t), "000MkMni19ClKG")
	if err != nil {
		t.Fatalf("QQ 专辑详情失败：%v", err)
	}
	if d.Name == "" || len(d.Tracks) == 0 {
		t.Fatalf("QQ 专辑详情为空：%#v", d)
	}
	for i, s := range d.Tracks {
		if s.Name == "" || s.Songmid == "" {
			t.Fatalf("第 %d 首缺名字 / id：%#v", i, s)
		}
	}
	t.Logf("QQ 专辑详情：%s（%s）%d 首", d.Name, d.ID, len(d.Tracks))
}
