package charts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fn-lx-player/pkg/applog"
)

// 这组测试钉的是「拉不到 ≠ 空的」：上游失败必须变成错误状态 + 一条日志，
// 而不是 200 + track_count 0（见 HANDOVER §11 ㊼ 块九）。
// 上游地址是包级变量，测试把它指到 httptest 服务上。

func withURLVar(t *testing.T, target *string, serverURL string) {
	t.Helper()
	old := *target
	*target = serverURL
	t.Cleanup(func() { *target = old })
}

func loggedContaining(t *testing.T, needle string) bool {
	t.Helper()
	for _, e := range applog.Default().Recent(100) {
		if strings.Contains(e.Message, needle) {
			return true
		}
	}
	return false
}

const kugouOneSong = `{"info":{"rankname":"酷狗飙升榜","banner7url":"http://banner/{size}.jpg"},` +
	`"songs":{"list":[{"filename":"歌手A - 歌名A","songname":"歌名A","singername":"歌手A",` +
	`"hash":"HASHONE","duration":201,"album_sizable_cover":"","trans_param":{"union_cover":""}}]}}`

// 第 1 页就失败 → 502 + 日志；绝不能安静地回一个空榜单。
func TestKugouChartDetailReportsUpstreamFailure(t *testing.T) {
	applog.Default().Clear()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>403 forbidden</html>"))
	}))
	defer srv.Close()
	withURLVar(t, &kugouRankInfoURL, srv.URL+"/rank/info/?rankid=%s&page=%d&json=true")

	rec := httptest.NewRecorder()
	NewChartManager().fetchAndWriteKugouChartDetail(rec, "8888")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("上游解析失败应回 502，实际 %d，body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "拉取酷狗榜单失败") {
		t.Fatalf("502 应说明是拉取失败，实际：%s", rec.Body.String())
	}
	if !loggedContaining(t, "酷狗榜单 8888 拉取失败") {
		t.Fatalf("失败了却没有日志：%+v", applog.Default().Recent(5))
	}
}

// 第 2 页失败 → 保留第 1 页的歌（200），但必须留一条 warn，说明少了一页。
func TestKugouChartDetailKeepsEarlierPages(t *testing.T) {
	applog.Default().Clear()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(kugouOneSong))
			return
		}
		_, _ = w.Write([]byte("<!doctype html><html>rate limited</html>"))
	}))
	defer srv.Close()
	withURLVar(t, &kugouRankInfoURL, srv.URL+"/rank/info/?rankid=%s&page=%d&json=true")

	rec := httptest.NewRecorder()
	NewChartManager().fetchAndWriteKugouChartDetail(rec, "6666")
	if rec.Code != http.StatusOK {
		t.Fatalf("第 1 页成功就该回 200，实际 %d，body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			TrackCount int `json:"track_count"`
			Songs      []struct {
				ID string `json:"id"`
			} `json:"songs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	if payload.Data.TrackCount != 1 || len(payload.Data.Songs) != 1 {
		t.Fatalf("应保留第 1 页那 1 首，实际 track_count=%d songs=%d", payload.Data.TrackCount, len(payload.Data.Songs))
	}
	if !loggedContaining(t, "第 2 页拉取失败") {
		t.Fatalf("少了一页却没有日志：%+v", applog.Default().Recent(5))
	}
}

// 上游正常返回「空列表」→ 这才是真的空榜单，回 200、不该报错、不该记日志。
func TestKugouChartDetailEmptyListIsNotFailure(t *testing.T) {
	applog.Default().Clear()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"info":{"rankname":"空的榜"},"songs":{"list":[]}}`))
	}))
	defer srv.Close()
	withURLVar(t, &kugouRankInfoURL, srv.URL+"/rank/info/?rankid=%s&page=%d&json=true")

	rec := httptest.NewRecorder()
	NewChartManager().fetchAndWriteKugouChartDetail(rec, "7777")
	if rec.Code != http.StatusOK {
		t.Fatalf("空列表是正常结果，应回 200，实际 %d", rec.Code)
	}
	if loggedContaining(t, "酷狗榜单 7777 拉取失败") {
		t.Fatal("正常空列表不该当成失败记日志")
	}
}

const kuwoSearchBody = `{"abslist":[{"ARTIST":"歌手K","SONGNAME":"歌名K","ALBUM":"专辑K",` +
	`"DURATION":"233","DC_TARGETID":"998877","MUSICRID":"MUSIC_998877",` +
	`"web_albumpic_short":"","web_artistpic_short":""}]}`

// 兜底搜索失败必须能把错误抛给调用方（以前返回 nil，和「真的没搜到」混在一起）。
func TestSearchKuwoSongsReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>nope</html>"))
	}))
	defer srv.Close()
	withURLVar(t, &kuwoSearchURL, srv.URL+"/search?all=%s&ft=music")

	songs, err := NewChartManager().searchKuwoSongs("随便一首")
	if err == nil {
		t.Fatalf("解析失败应返回错误，实际 songs=%d", len(songs))
	}
	if songs != nil {
		t.Fatalf("出错时不该给半截结果，实际 %d 首", len(songs))
	}
}

func TestSearchKuwoSongsParsesList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(kuwoSearchBody))
	}))
	defer srv.Close()
	withURLVar(t, &kuwoSearchURL, srv.URL+"/search?all=%s&ft=music")

	songs, err := NewChartManager().searchKuwoSongs("随便一首")
	if err != nil {
		t.Fatalf("正常响应不该报错：%v", err)
	}
	if len(songs) != 1 {
		t.Fatalf("期望 1 首，实际 %d", len(songs))
	}
	if songs[0].Songmid != "998877" || songs[0].Source != "kw" || songs[0].Duration != 233 {
		t.Fatalf("字段解析不对：%+v", songs[0])
	}
}

// 酷我榜单主接口失败 → 兜底搜索前留一条带「停在哪一步」的日志，
// 不然界面上只是「这个榜单空了」。
func TestKuwoChartDetailLogsPrimaryFailureBeforeFallback(t *testing.T) {
	applog.Default().Clear()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/ksong") {
			_, _ = w.Write([]byte("<html>bad gateway</html>")) // 主接口坏
			return
		}
		_, _ = w.Write([]byte(`{"abslist":[]}`)) // 兜底搜索正常但没结果
	}))
	defer srv.Close()
	withURLVar(t, &kuwoBangURL, srv.URL+"/ksong?id=%s")
	withURLVar(t, &kuwoSearchURL, srv.URL+"/search?all=%s")

	rec := httptest.NewRecorder()
	NewChartManager().fetchAndWriteKuwoChartDetail(rec, "1", "某榜")
	if rec.Code != http.StatusOK {
		t.Fatalf("兜底搜索成功时应回 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !loggedContaining(t, "主接口没拿到曲目") {
		t.Fatalf("主接口失败没留日志：%+v", applog.Default().Recent(5))
	}
	if !loggedContaining(t, "停在「解析响应」") {
		t.Fatalf("日志应写明停在哪一步：%+v", applog.Default().Recent(5))
	}
}
