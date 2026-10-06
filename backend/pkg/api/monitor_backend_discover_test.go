package api

import (
	"fmt"
	"strings"
	"testing"

	"fn-lx-player/pkg/account"
	"fn-lx-player/pkg/monitor"
	"fn-lx-player/pkg/push"
)

// 用桩函数替换后端抓取通道，验证分支决策而不碰网络
func withDiscoverStubs(t *testing.T,
	resolve func(string) (string, string, error),
	fetchSource func(store *account.Store, provider, kind, id string) (*push.Source, error),
	fetchByLink func(string) (*push.Source, error),
	lists func(cookie string) ([]map[string]any, error),
) {
	t.Helper()
	oResolve, oSource, oLink, oLists, oQQ := bdResolveLink, bdFetchSource, bdFetchByLink, bdNeteaseLists, bdQQLists
	if resolve != nil {
		bdResolveLink = resolve
	}
	if fetchSource != nil {
		bdFetchSource = fetchSource
	}
	if fetchByLink != nil {
		bdFetchByLink = fetchByLink
	}
	if lists != nil {
		bdNeteaseLists = lists
		bdQQLists = func(map[string]string) ([]map[string]any, error) { return lists("") }
	}
	t.Cleanup(func() {
		bdResolveLink, bdFetchSource, bdFetchByLink, bdNeteaseLists, bdQQLists = oResolve, oSource, oLink, oLists, oQQ
	})
}

func newDiscoverSrv(t *testing.T, cookies map[string]string) *Server {
	t.Helper()
	store := account.NewStore(t.TempDir())
	for p, c := range cookies {
		if err := store.Save(account.Account{Provider: p, Cookie: c, Status: "connected"}); err != nil {
			t.Fatal(err)
		}
	}
	return &Server{accountStore: store}
}

func sampleSource(id, title string) *push.Source {
	return &push.Source{
		ID: id, Title: title,
		Tracks: []push.Track{
			{Name: "夜曲", Artist: "周杰伦", Album: "十一月的萧邦", Duration: 227, SongID: "186016"},
			{Name: "", Artist: "无名", SongID: "999"}, // 空歌名应被丢掉
			{Name: "缺ID", Artist: "X", SongID: ""},  // 缺 ID 应被丢掉
		},
	}
}

func TestBackendDiscoverPlaylistPrefersLoggedInAccount(t *testing.T) {
	var linkCalled bool
	withDiscoverStubs(t,
		func(string) (string, string, error) { return "wy", "123", nil },
		func(_ *account.Store, provider, kind, id string) (*push.Source, error) {
			if provider != account.ProviderNetease || kind != push.KindPlaylist || id != "123" {
				t.Errorf("账号通道参数不符: %s %s %s", provider, kind, id)
			}
			return sampleSource(id, "我的歌单"), nil
		},
		func(string) (*push.Source, error) { linkCalled = true; return nil, nil },
		nil,
	)
	s := newDiscoverSrv(t, map[string]string{account.ProviderNetease: "MUSIC_U=x"})

	songs, warnings, handled, err := s.backendDiscover(&monitor.Monitor{
		Kind:   monitor.KindPlaylist,
		Target: monitor.Target{Playlists: []monitor.PlaylistRef{{Link: "https://music.163.com/playlist?id=123"}}},
	})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if linkCalled {
		t.Error("已登录且账号通道成功时不应再走公开链接通道")
	}
	if len(songs) != 1 || songs[0].Source != "wy" || songs[0].ID != "186016" || songs[0].Origin != "我的歌单" {
		t.Fatalf("曲目不符：%+v warnings=%v", songs, warnings)
	}
	if songs[0].URL != "" {
		t.Error("后端自抓的曲目不应带直链")
	}
}

func TestBackendDiscoverPlaylistFallsBackToPublicLink(t *testing.T) {
	withDiscoverStubs(t,
		func(string) (string, string, error) { return "tx", "456", nil },
		nil,
		func(link string) (*push.Source, error) {
			if !strings.Contains(link, "456") {
				t.Errorf("应按原始链接抓取：%s", link)
			}
			return sampleSource("456", "公开歌单"), nil
		},
		nil,
	)
	s := newDiscoverSrv(t, nil) // 未登录

	songs, _, handled, err := s.backendDiscover(&monitor.Monitor{
		Kind:   monitor.KindPlaylist,
		Target: monitor.Target{Playlists: []monitor.PlaylistRef{{Link: "https://y.qq.com/n/ryqq/playlist/456"}}},
	})
	if !handled || err != nil || len(songs) != 1 || songs[0].Source != "tx" {
		t.Fatalf("未登录应回退公开接口：handled=%v err=%v songs=%+v", handled, err, songs)
	}
}

func TestBackendDiscoverPlaylistByIDNeedsPlatformAndCookie(t *testing.T) {
	withDiscoverStubs(t, nil,
		func(_ *account.Store, provider, kind, id string) (*push.Source, error) {
			if provider != account.ProviderQQ || id != "777" {
				t.Errorf("参数不符: %s %s", provider, id)
			}
			return sampleSource(id, "QQ单"), nil
		}, nil, nil)

	// 有 cookie：走账号通道（能拉私有歌单，字段口径与推送一致）
	s := newDiscoverSrv(t, map[string]string{account.ProviderQQ: "qq-cookie"})
	songs, _, _, err := s.backendDiscover(&monitor.Monitor{
		Kind:   monitor.KindPlaylist,
		Target: monitor.Target{Playlists: []monitor.PlaylistRef{{ID: "777", Source: "tx"}}},
	})
	if err != nil || len(songs) != 1 || songs[0].Source != "tx" {
		t.Fatalf("按 ID 抓取失败：err=%v songs=%+v", err, songs)
	}
}

// 只给 ID、且**没连账号**时：应自己拼一条标准歌单链接走公开接口，而不是直接报错。
//
// 这是「精选歌单」「官方榜单」两条订阅入口的真实形态 —— 它们只有 id、没有分享链接。
// 不拼链接的话，没连账号的用户会看到「订阅成功但新歌永远不来」（每轮记一条
// 「尚未连接 xx 账号，无法按 ID 拉取」）。
func TestBackendDiscoverPlaylistByIDFallsBackToPublicLink(t *testing.T) {
	var gotLink string
	withDiscoverStubs(t,
		nil, // 用真实的链接解析（纯本地正则，不联网）
		func(*account.Store, string, string, string) (*push.Source, error) {
			t.Error("没有 cookie 时不该走账号通道")
			return nil, fmt.Errorf("不该被调用")
		},
		func(link string) (*push.Source, error) {
			gotLink = link
			return sampleSource("777", "公开单"), nil
		},
		nil,
	)

	s := newDiscoverSrv(t, nil) // 未登录
	songs, _, _, err := s.backendDiscover(&monitor.Monitor{
		Kind:   monitor.KindPlaylist,
		Target: monitor.Target{Playlists: []monitor.PlaylistRef{{ID: "777", Source: "tx"}}},
	})
	if err != nil || len(songs) != 1 || songs[0].Source != "tx" {
		t.Fatalf("应回退公开链接：err=%v songs=%+v", err, songs)
	}
	if !strings.Contains(gotLink, "777") {
		t.Fatalf("拼出来的链接应含歌单 id，实际 %q", gotLink)
	}
}

// 拼不出链接的平台（kg / kw）**必须保持老行为**：没账号就报错，
// 而不是拼一条下游必然解析不了的链接（那会把「不支持」伪装成「链接失效」）。
func TestBackendDiscoverPlaylistByIDUnsupportedPlatformStillErrors(t *testing.T) {
	withDiscoverStubs(t,
		func(string) (string, string, error) { t.Error("不该解析链接"); return "", "", fmt.Errorf("x") },
		func(*account.Store, string, string, string) (*push.Source, error) {
			t.Error("没有 cookie 时不该走账号通道")
			return nil, fmt.Errorf("不该被调用")
		},
		func(string) (*push.Source, error) {
			t.Error("不该走公开链接通道")
			return nil, fmt.Errorf("x")
		},
		nil,
	)

	s := newDiscoverSrv(t, nil)
	_, _, _, err := s.backendDiscover(&monitor.Monitor{
		Kind:   monitor.KindPlaylist,
		Target: monitor.Target{Playlists: []monitor.PlaylistRef{{ID: "1234567", Source: "kg"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "所有来源都抓取失败") {
		t.Fatalf("全部失败时应返回错误，实际 err=%v", err)
	}
}

func TestBackendDiscoverPlaylistPartialSuccessKeepsWarnings(t *testing.T) {
	withDiscoverStubs(t,
		func(link string) (string, string, error) {
			if strings.Contains(link, "bad") {
				return "", "", fmt.Errorf("链接失效")
			}
			return "wy", "123", nil
		},
		func(*account.Store, string, string, string) (*push.Source, error) {
			return sampleSource("123", "好歌单"), nil
		},
		nil, nil)
	s := newDiscoverSrv(t, map[string]string{account.ProviderNetease: "MUSIC_U=x"})

	songs, warnings, _, err := s.backendDiscover(&monitor.Monitor{
		Kind: monitor.KindPlaylist,
		Target: monitor.Target{Playlists: []monitor.PlaylistRef{
			{Link: "https://music.163.com/playlist?id=123"},
			{Link: "https://music.163.com/playlist?id=bad"},
		}},
	})
	if err != nil {
		t.Fatalf("部分成功不应报错：%v", err)
	}
	if len(songs) != 1 || len(warnings) != 1 {
		t.Fatalf("期望 1 首曲目 + 1 条警告，实际 songs=%+v warnings=%v", songs, warnings)
	}
}

func TestBackendDiscoverChartNotHandled(t *testing.T) {
	s := newDiscoverSrv(t, nil)
	_, _, handled, err := s.backendDiscover(&monitor.Monitor{Kind: monitor.KindChart})
	if handled || err != nil {
		t.Fatalf("chart 应由浏览器上报通道处理：handled=%v err=%v", handled, err)
	}
}

func TestBackendDiscoverFavoritesResolvesLikedList(t *testing.T) {
	withDiscoverStubs(t, nil,
		func(_ *account.Store, provider, kind, id string) (*push.Source, error) {
			if id != "999" {
				t.Errorf("应拉取识别出的喜欢歌单 ID，实际 %s", id)
			}
			return sampleSource(id, "我喜欢的音乐"), nil
		}, nil,
		func(string) ([]map[string]any, error) {
			return []map[string]any{
				{"id": "999", "name": "我喜欢的音乐"},
				{"id": "111", "name": "日常歌单"},
			}, nil
		},
	)
	// QQ cookie 置为空串 → ParseQQCookies 也当未登录处理
	s := newDiscoverSrv(t, map[string]string{account.ProviderNetease: "MUSIC_U=x"})
	songs, _, handled, err := s.backendDiscover(&monitor.Monitor{Kind: monitor.KindFavorites})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if len(songs) != 1 || songs[0].Source != "wy" {
		t.Fatalf("收藏应展开为「喜欢」歌单曲目：%+v", songs)
	}
}

func TestBackendDiscoverFavoritesNeedsAccount(t *testing.T) {
	s := newDiscoverSrv(t, nil)
	_, _, _, err := s.backendDiscover(&monitor.Monitor{Kind: monitor.KindFavorites})
	if err == nil || !strings.Contains(err.Error(), "需要至少连接一个账号") {
		t.Fatalf("未连接账号应给出明确提示，实际 %v", err)
	}
}
