package account

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 已登录的凭据样例
func loggedInCookies() map[string]string {
	return map[string]string{
		"qm_str_musicid": "12345",
		"uin":            "o12345",
		"qm_keyst":       "Q_H_L_key",
		"tmeLoginType":   "2",
	}
}

// ── 日推 ──

func TestQQDailyParsesSonglist(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/cgi-bin/musicu.fcg": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)

			// 日推走 req_0 而非 request，且 comm 必须是网页版参数
			if _, ok := body["req_0"]; !ok {
				t.Error("日推请求体应使用 req_0")
			}
			req0, _ := body["req_0"].(map[string]any)
			if req0["module"] != "music.srfDissInfo.DissInfo" || req0["method"] != "CgiGetDiss" {
				t.Errorf("module/method 不对: %v", req0)
			}
			param, _ := req0["param"].(map[string]any)
			if param["dirid"] != float64(202) {
				t.Errorf("dirid 应为 202，实际 %v", param["dirid"])
			}
			comm, _ := body["comm"].(map[string]any)
			if comm["platform"] != "yqq.json" {
				t.Errorf("comm.platform 应为 yqq.json，实际 %v", comm["platform"])
			}
			if comm["authst"] != "Q_H_L_key" {
				t.Errorf("应带上 authst，实际 %v", comm["authst"])
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"req_0": map[string]any{
					"code": 0,
					"data": map[string]any{
						"songlist": []any{
							map[string]any{
								"songmid":  "mid-1",
								"songname": "日推歌一",
								"interval": float64(240),
								"singer":   []any{map[string]any{"name": "歌手甲"}, map[string]any{"name": "歌手乙"}},
								"album":    map[string]any{"name": "专辑一", "mid": "alb-1"},
							},
							map[string]any{
								"songmid":  "mid-2",
								"songname": "日推歌二",
								"interval": float64(200),
								"singer":   []any{map[string]any{"name": "歌手丙"}},
								"albummid": "alb-2",
							},
						},
					},
				},
			})
		},
	})

	songs, err := QQDaily(loggedInCookies())
	if err != nil {
		t.Fatalf("QQDaily 失败: %v", err)
	}
	if len(songs) != 2 {
		t.Fatalf("应有 2 首，实际 %d", len(songs))
	}

	first := songs[0]
	if first["song_id"] != "mid-1" || first["title"] != "日推歌一" {
		t.Errorf("基础字段不对: %v", first)
	}
	if first["artist"] != "歌手甲 / 歌手乙" {
		t.Errorf("多位歌手应用 / 连接，实际 %q", first["artist"])
	}
	if first["album"] != "专辑一" {
		t.Errorf("album = %v", first["album"])
	}
	if first["duration_ms"] != 240000 {
		t.Errorf("时长应换算为毫秒，实际 %v", first["duration_ms"])
	}
	if !strings.Contains(first["cover_url"].(string), "alb-1") {
		t.Errorf("封面应取自 album.mid，实际 %v", first["cover_url"])
	}
	// 第二条用 albummid 兜底
	if !strings.Contains(songs[1]["cover_url"].(string), "alb-2") {
		t.Errorf("封面应回退到 albummid，实际 %v", songs[1]["cover_url"])
	}
}

func TestQQDailyRequiresLogin(t *testing.T) {
	// 缺 musickey
	_, err := QQDaily(map[string]string{"uin": "12345"})
	if err == nil || !strings.Contains(err.Error(), "尚未登录") {
		t.Fatalf("未登录应报错，实际 %v", err)
	}
	// 缺账号
	_, err = QQDaily(map[string]string{"qm_keyst": "k"})
	if err == nil {
		t.Fatal("缺账号 ID 应报错")
	}
}

func TestQQDailyRejectsWhenServerRefuses(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/cgi-bin/musicu.fcg": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":  0,
				"req_0": map[string]any{"code": 1000, "data": map[string]any{}},
			})
		},
	})
	_, err := QQDaily(loggedInCookies())
	if err == nil || !strings.Contains(err.Error(), "失效") {
		t.Fatalf("内层 code 非 0 应提示登录失效，实际 %v", err)
	}
}

// ── 歌单列表 ──

func TestQQPlaylistsMergesCreatedAndCollected(t *testing.T) {
	got := fakeQQ(t, map[string]http.HandlerFunc{
		"/rsc/fcgi-bin/fcg_user_created_diss": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("hostuin") != "12345" {
				t.Errorf("应带上 hostuin，实际 %q", r.URL.Query().Get("hostuin"))
			}
			if ck := r.Header.Get("Cookie"); !strings.Contains(ck, "qm_keyst=") {
				t.Errorf("应带上登录 Cookie，实际 %q", ck)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"disslist": []any{
						map[string]any{"dissid": "c1", "dissname": "我创建的歌单", "song_cnt": float64(12), "logo": "http://a/1.jpg"},
					},
				},
			})
		},
		"/fav/fcgi-bin/fcg_get_profile_order_asset.fcg": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("reqtype") != "3" {
				t.Errorf("收藏歌单 reqtype 应为 3，实际 %q", r.URL.Query().Get("reqtype"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"cdlist": []any{
						map[string]any{"dissid": "k1", "dissname": "收藏的歌单", "song_cnt": float64(30)},
					},
				},
			})
		},
	})

	list, err := QQPlaylists(loggedInCookies())
	if err != nil {
		t.Fatalf("QQPlaylists 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应合并 2 个歌单，实际 %d", len(list))
	}
	if list[0]["kind"] != "created" || list[0]["name"] != "我创建的歌单" || list[0]["count"] != 12 {
		t.Errorf("创建的歌单解析不对: %v", list[0])
	}
	if list[1]["kind"] != "collected" || list[1]["name"] != "收藏的歌单" {
		t.Errorf("收藏的歌单解析不对: %v", list[1])
	}
	_ = got
}

// 一侧失败时仍应返回另一侧的结果
func TestQQPlaylistsToleratesPartialFailure(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/rsc/fcgi-bin/fcg_user_created_diss": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
		"/fav/fcgi-bin/fcg_get_profile_order_asset.fcg": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"cdlist": []any{map[string]any{"dissid": "k1", "dissname": "收藏的"}}},
			})
		},
	})

	list, err := QQPlaylists(loggedInCookies())
	if err != nil {
		t.Fatalf("单侧失败不应整体报错: %v", err)
	}
	if len(list) != 1 || list[0]["kind"] != "collected" {
		t.Fatalf("应返回成功的那一侧，实际 %v", list)
	}
}

func TestQQPlaylistsErrorsWhenBothFail(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/rsc/fcgi-bin/fcg_user_created_diss": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		},
		"/fav/fcgi-bin/fcg_get_profile_order_asset.fcg": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		},
	})
	if _, err := QQPlaylists(loggedInCookies()); err == nil {
		t.Fatal("两侧都失败时应报错")
	}
}

func TestQQPlaylistsRequiresLogin(t *testing.T) {
	if _, err := QQPlaylists(map[string]string{}); err == nil {
		t.Fatal("未登录应报错")
	}
}

// ── 歌单详情 ──

func TestQQPlaylistDetailStripsJSONP(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("disstid") != "999" {
				t.Errorf("应带上 disstid，实际 %q", r.URL.Query().Get("disstid"))
			}
			// 该接口返回 JSONP 包装
			payload := map[string]any{
				"cdlist": []any{
					map[string]any{
						"dissname": "目标歌单",
						"desc":     "描述文本",
						"logo":     "http://a/cover.jpg",
						"nickname": "创建者",
						"songlist": []any{
							map[string]any{
								"mid":      "s1",
								"songname": "歌一",
								"interval": float64(180),
								"singer":   []any{map[string]any{"name": "甲"}},
								"album":    map[string]any{"name": "专一", "mid": "a1"},
							},
							map[string]any{"mid": "", "songname": "缺 ID 应被丢弃"},
						},
					},
				},
			}
			raw, _ := json.Marshal(payload)
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte("jsonCallback(" + string(raw) + ");"))
		},
	})

	detail, err := QQPlaylistDetail(loggedInCookies(), "999")
	if err != nil {
		t.Fatalf("QQPlaylistDetail 失败: %v", err)
	}
	if detail["title"] != "目标歌单" || detail["owner"] != "创建者" {
		t.Errorf("歌单元信息不对: %v", detail)
	}
	items := detail["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("缺 ID 的曲目应被丢弃，实际剩 %d 条", len(items))
	}
	if items[0]["song_id"] != "s1" || items[0]["duration_ms"] != 180000 {
		t.Errorf("曲目解析不对: %v", items[0])
	}
}

func TestQQPlaylistDetailEmptyCdlist(t *testing.T) {
	fakeQQ(t, map[string]http.HandlerFunc{
		"/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("jsonCallback({\"cdlist\":[]});"))
		},
	})
	if _, err := QQPlaylistDetail(loggedInCookies(), "999"); err == nil {
		t.Fatal("歌单为空时应报错")
	}
}

func TestQQPlaylistDetailRequiresID(t *testing.T) {
	if _, err := QQPlaylistDetail(loggedInCookies(), "  "); err == nil {
		t.Fatal("缺 disstid 应报错")
	}
}

// ── 小工具 ──

func TestNormalizeQQTracksSkipsIncomplete(t *testing.T) {
	rows := []any{
		map[string]any{"mid": "ok", "songname": "完整"},
		map[string]any{"mid": "", "songname": "缺 ID"},
		map[string]any{"mid": "x", "songname": ""},
		"not-a-map",
	}
	out := normalizeQQTracks(rows)
	if len(out) != 1 || out[0]["song_id"] != "ok" {
		t.Fatalf("应只保留完整的一条，实际 %v", out)
	}
}

func TestQQCoverFallbacks(t *testing.T) {
	cases := []struct {
		name string
		row  map[string]any
		want string
	}{
		{"优先 album.mid", map[string]any{"album": map[string]any{"mid": "AM"}}, "T002R800x800M000AM.jpg"},
		{"回退 albummid", map[string]any{"albummid": "BM"}, "T002R800x800M000BM.jpg"},
		{"都没有返回空", map[string]any{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := qqCover(c.row)
			if c.want == "" {
				if got != "" {
					t.Fatalf("应为空，实际 %q", got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Fatalf("got %q, want 含 %q", got, c.want)
			}
		})
	}
}

func TestNumField(t *testing.T) {
	m := map[string]any{"a": float64(42), "b": "7", "c": nil, "d": json.Number("13")}
	if numField(m, "a") != 42 || numField(m, "b") != 7 || numField(m, "d") != 13 {
		t.Errorf("解析结果不对: %v %v %v", numField(m, "a"), numField(m, "b"), numField(m, "d"))
	}
	if numField(m, "c") != 0 || numField(m, "missing") != 0 {
		t.Error("缺失或 nil 应返回 0")
	}
	if numField(nil, "a") != 0 {
		t.Error("nil map 应返回 0")
	}
}
