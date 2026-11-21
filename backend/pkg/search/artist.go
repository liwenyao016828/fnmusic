package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 本文件是「原生歌手 / 原生专辑」这一层的平台适配（v2.33.0 新增）。
//
// # 它解决的是哪两个问题
//
//  1. **搜不到歌手**：曲率既有的搜索层只有单曲搜索（`/api/cloudsearch/pc?type=1`），
//     所以官方「歌手」Tab 里永远只有本地歌手。这里补 type=100 的歌手搜索，
//     以及歌手详情 / 曲目 / 专辑三条后端接口。
//  2. **专辑页只有搜得到的几首**：专辑详情原来是「拿专辑名再搜一遍，凑到几首算几首」
//     （见 intercept/valbum.go）。有了平台内专辑 id，就能直接问原生专辑接口要
//     **完整曲目**（含曲序与碟号）。
//
// # 只做实测通的平台
//
// 网易云的四个歌手接口与专辑接口都在本机实测通过（2026-11-21）。QQ 的旧歌手接口
// （`fcg_v8_singer_track_cp` / `fcg_v8_singer_album_cp`）已经 **404 下线**，所以
// QQ 这轮只接**专辑详情**（`fcg_v8_album_info_cp` 实测正常）—— 歌手那头继续走
// 旧口径（不外挂身份、点击透传官方）。
//
// # 字段名的坑
//
// 网易同一份「歌曲对象」在不同端点上字段名**不一样**：
//
//	搜索 / 专辑详情：`ar[]` + `al` + `dt`
//	歌手曲目列表：   `artists[]` + `album` + `duration`
//
// 所以下面的 `neSong` 两套都解，取非空的那一套。少解一套就会得到「歌手页里
// 每一首歌都没有歌手、没有专辑、时长 0」这种没有任何报错的坏数据。

// ArtistHit 是歌手搜索的一条结果。
type ArtistHit struct {
	ID         string
	Name       string
	Cover      string
	AlbumCount int
	TrackCount int
}

// ArtistInfo 是歌手详情。
type ArtistInfo struct {
	ID         string
	Name       string
	Cover      string
	BriefDesc  string
	AlbumCount int
	TrackCount int
}

// AlbumHit 是歌手专辑列表里的一张专辑。
type AlbumHit struct {
	ID          string
	Name        string
	Cover       string
	Artist      string
	ArtistIDs   []string
	Size        int
	Year        int
	PublishTS   int64
	description string
}

// AlbumDetail 是一张专辑的详情 + 完整曲目。
type AlbumDetail struct {
	ID        string
	Name      string
	Cover     string
	Artist    string
	ArtistIDs []string
	Size      int
	Year      int
	Tracks    []UnifiedSong
}

const (
	// searchBudget 是单次原生接口请求的超时。
	//
	// 6 秒：与拦截层的 lyricFetchBudget / coverFetchBudget 同一量级。这几个接口
	// 都在「用户已经点进去了」的路径上，等太久不如快点回一个明确的失败。
	searchBudget = 6 * time.Second
)

// neArtistRef 是网易歌曲 / 专辑里内嵌的歌手对象（新旧两种形状通用的那几个字段）。
type neArtistRef struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	PicURL string `json:"picUrl"`
}

// neAlbumRef 是网易歌曲 / 专辑里内嵌的专辑对象。
type neAlbumRef struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	PicURL      string `json:"picUrl"`
	Size        int    `json:"size"`
	PublishTime int64  `json:"publishTime"`
}

// neSong 是网易的「歌曲对象」（两套字段名都解，见文件头）。
type neSong struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Duration    int               `json:"duration"` // ms（歌手曲目列表）
	DT          int               `json:"dt"`       // ms（搜索 / 专辑详情）
	No          int               `json:"no"`
	Disc        neFlexInt         `json:"disc"` // 有时是数字，有时是字符串（v2.33.0 联网用例抓到）
	Cd          string            `json:"cd"`
	PublishTime int64             `json:"publishTime"`
	Artists     []neArtistRef     `json:"artists"`
	Ar          []neArtistRef     `json:"ar"`
	Album       *neAlbumRef       `json:"album"`
	Al          *neAlbumRef       `json:"al"`
	L           *neteaseLevelInfo `json:"l"`
	M           *neteaseLevelInfo `json:"m"`
	H           *neteaseLevelInfo `json:"h"`
	Sq          *neteaseLevelInfo `json:"sq"`
	Hr          *neteaseLevelInfo `json:"hr"`
}

// neFlexInt 解「同一字段有时是数字、有时是字符串」的整数。
//
// 网易的 disc 就是这样：专辑详情里给 1（数字），歌手曲目列表里给 "1"（字符串）。
// 直接写成 int 会让整批曲目解码失败 —— 线上症状是「歌手页的热门歌曲永远是空的」，
// 而本地夹具因为只抄了一种形态，测不出来。
type neFlexInt int

func (n *neFlexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	*n = neFlexInt(atoiLoose(s))
	return nil
}

// unified 把网易歌曲对象转成曲率自己的统一曲目。
func (s neSong) unified() (UnifiedSong, bool) {
	id := s.ID
	name := strings.TrimSpace(s.Name)
	if id <= 0 || name == "" {
		return UnifiedSong{}, false
	}
	refs := s.Ar
	if len(refs) == 0 {
		refs = s.Artists
	}
	names := make([]string, 0, len(refs))
	ids := make([]string, 0, len(refs))
	for _, a := range refs {
		names = append(names, a.Name)
		if a.ID > 0 {
			ids = append(ids, fmt.Sprintf("%d", a.ID))
		} else {
			ids = append(ids, "")
		}
	}
	alb := s.Al
	if alb == nil {
		alb = s.Album
	}
	albumName, albumID, cover := "", "", ""
	if alb != nil {
		albumName = alb.Name
		if alb.ID > 0 {
			albumID = fmt.Sprintf("%d", alb.ID)
		}
		cover = strings.Replace(strings.TrimSpace(alb.PicURL), "http://", "https://", 1)
	}
	ms := s.DT
	if ms <= 0 {
		ms = s.Duration
	}
	qs := neteaseQualitys(s.L, s.M, s.H, s.Sq, s.Hr)
	if len(qs) == 0 {
		qs = []string{Quality320k}
	}
	year := msToYear(s.PublishTime)
	if year == 0 && alb != nil {
		year = msToYear(alb.PublishTime)
	}
	disc := int(s.Disc)
	if disc == 0 {
		disc = atoiLoose(s.Cd)
	}
	return UnifiedSong{
		ID:        fmt.Sprintf("wy_%d", id),
		Songmid:   fmt.Sprintf("%d", id),
		Name:      name,
		Singer:    strings.Join(names, ", "),
		Album:     albumName,
		AlbumID:   albumID,
		ArtistIDs: nonEmptyIDs(ids),
		Cover:     cover,
		Source:    "wy",
		RawSource: "wy",
		Duration:  ms / 1000,
		Interval:  ms / 1000,
		Quality:   bestQuality(qs),
		Qualitys:  qs,
		Year:      year,
		Track:     s.No,
		Disc:      disc,
	}, true
}

// neEnvelope 是网易几个接口共同的「宽容信封」：只解我们自己要的字段。
type neEnvelope struct {
	Code int `json:"code"`
	// 搜索
	Result struct {
		Artists []struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			PicURL    string `json:"picUrl"`
			AlbumSize int    `json:"albumSize"`
			MusicSize int    `json:"musicSize"`
		} `json:"artists"`
		ArtistCount int `json:"artistCount"`
	} `json:"result"`
	// 歌手详情
	Artist *struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		PicURL    string `json:"picUrl"`
		BriefDesc string `json:"briefDesc"`
		AlbumSize int    `json:"albumSize"`
		MusicSize int    `json:"musicSize"`
	} `json:"artist"`
	// 歌手曲目 / 专辑详情
	Songs []neSong    `json:"songs"`
	Total int         `json:"total"`
	More  bool        `json:"more"`
	Album *neAlbumRef `json:"album"`
	// 歌手专辑
	HotAlbums []struct {
		ID          int64         `json:"id"`
		Name        string        `json:"name"`
		PicURL      string        `json:"picUrl"`
		Size        int           `json:"size"`
		PublishTime int64         `json:"publishTime"`
		Artists     []neArtistRef `json:"artists"`
	} `json:"hotAlbums"`
}

// neteaseGetJSON 发一个网易请求并解信封。
//
// Referer 必须带 music.163.com：少了它 `cloudsearch` 直接 200 + 空结果，
// 看起来像「搜不到」而不是「被拒」。
func neteaseJSON(ctx context.Context, rawURL string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, searchBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	req.Header.Set("Referer", "https://music.163.com/")
	resp, err := doHTTP(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("网易接口 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return err
	}
	return nil
}

// SearchArtistNETEASE 按关键词搜网易云歌手。
func SearchArtistNETEASE(ctx context.Context, keyword string, limit int) []ArtistHit {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil
	}
	if limit <= 0 {
		limit = 10
	}
	rawURL := fmt.Sprintf("https://music.163.com/api/cloudsearch/pc?s=%s&type=100&offset=0&limit=%d",
		url.QueryEscape(keyword), limit)
	var env neEnvelope
	if err := neteaseJSON(ctx, rawURL, &env); err != nil {
		return nil
	}
	out := make([]ArtistHit, 0, len(env.Result.Artists))
	for _, a := range env.Result.Artists {
		if a.ID <= 0 || strings.TrimSpace(a.Name) == "" {
			continue
		}
		out = append(out, ArtistHit{
			ID:         fmt.Sprintf("%d", a.ID),
			Name:       a.Name,
			Cover:      strings.Replace(strings.TrimSpace(a.PicURL), "http://", "https://", 1),
			AlbumCount: a.AlbumSize,
			TrackCount: a.MusicSize,
		})
	}
	return out
}

// FetchArtistNETEASE 取网易云歌手详情。
func FetchArtistNETEASE(ctx context.Context, id string) (ArtistInfo, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ArtistInfo{}, fmt.Errorf("歌手 id 为空")
	}
	var env neEnvelope
	if err := neteaseJSON(ctx, "https://music.163.com/api/v1/artist/"+url.PathEscape(id), &env); err != nil {
		return ArtistInfo{}, err
	}
	if env.Artist == nil || env.Artist.ID <= 0 {
		return ArtistInfo{}, fmt.Errorf("网易没有返回歌手 %s", id)
	}
	return ArtistInfo{
		ID:         fmt.Sprintf("%d", env.Artist.ID),
		Name:       env.Artist.Name,
		Cover:      strings.Replace(strings.TrimSpace(env.Artist.PicURL), "http://", "https://", 1),
		BriefDesc:  env.Artist.BriefDesc,
		AlbumCount: env.Artist.AlbumSize,
		TrackCount: env.Artist.MusicSize,
	}, nil
}

// FetchArtistTracksNETEASE 取网易云歌手的曲目列表。
//
// order 只认两个字面值：`hot`（热门）与 `time`（最新）。
//
// ⚠️ 实测：`order=true` 返回**空数组**，`order=time` 正常。所以别在调用方那
// 传 `true/false` 这种「看起来更像布尔」的值 —— 那会得到一份静默的空列表。
func FetchArtistTracksNETEASE(ctx context.Context, id string, offset, limit int, order string) ([]UnifiedSong, int, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, 0, fmt.Errorf("歌手 id 为空")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	if order != "time" {
		order = "hot"
	}
	rawURL := fmt.Sprintf("https://music.163.com/api/v1/artist/songs?id=%s&offset=%d&limit=%d&order=%s",
		url.PathEscape(id), offset, limit, order)
	var env neEnvelope
	if err := neteaseJSON(ctx, rawURL, &env); err != nil {
		return nil, 0, err
	}
	out := make([]UnifiedSong, 0, len(env.Songs))
	for _, s := range env.Songs {
		if u, ok := s.unified(); ok {
			out = append(out, u)
		}
	}
	return out, env.Total, nil
}

// FetchArtistAlbumsNETEASE 取网易云歌手的专辑列表（`/api/artist/albums/{id}`）。
func FetchArtistAlbumsNETEASE(ctx context.Context, id string, offset, limit int) ([]AlbumHit, int, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, 0, fmt.Errorf("歌手 id 为空")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rawURL := fmt.Sprintf("https://music.163.com/api/artist/albums/%s?limit=%d&offset=%d&total=true",
		url.PathEscape(id), limit, offset)
	var env neEnvelope
	if err := neteaseJSON(ctx, rawURL, &env); err != nil {
		return nil, 0, err
	}
	out := make([]AlbumHit, 0, len(env.HotAlbums))
	for _, a := range env.HotAlbums {
		if a.ID <= 0 || strings.TrimSpace(a.Name) == "" {
			continue
		}
		names := make([]string, 0, len(a.Artists))
		ids := make([]string, 0, len(a.Artists))
		for _, ar := range a.Artists {
			names = append(names, ar.Name)
			if ar.ID > 0 {
				ids = append(ids, fmt.Sprintf("%d", ar.ID))
			} else {
				ids = append(ids, "")
			}
		}
		out = append(out, AlbumHit{
			ID:        fmt.Sprintf("%d", a.ID),
			Name:      a.Name,
			Cover:     strings.Replace(strings.TrimSpace(a.PicURL), "http://", "https://", 1),
			Artist:    strings.Join(names, "/"),
			ArtistIDs: nonEmptyIDs(ids),
			Size:      a.Size,
			Year:      msToYear(a.PublishTime),
			PublishTS: a.PublishTime / 1000,
		})
	}
	// ⚠️ 这个接口**不返回总数**（只有 `more`）。total 给 `offset + len + 1` 会在
	// 最后一页多算一条，所以这里用 `len` 的语义：调用方只拿它判断「还有没有下一页」，
	// 真正的翻页依据是 `more`。为了让 `hasMore` 判断可用，末页存在时 total 取
	// offset+len，存在下一页时取 offset+len+1（客户端只会用它做「还有」判断）。
	total := offset + len(out)
	if env.More {
		total++
	}
	return out, total, nil
}

// FetchAlbumDetailNETEASE 取一张网易云专辑的详情与**完整曲目**。
func FetchAlbumDetailNETEASE(ctx context.Context, albumID string) (AlbumDetail, error) {
	albumID = strings.TrimSpace(albumID)
	if albumID == "" {
		return AlbumDetail{}, fmt.Errorf("专辑 id 为空")
	}
	var env neEnvelope
	if err := neteaseJSON(ctx, "https://music.163.com/api/v1/album/"+url.PathEscape(albumID), &env); err != nil {
		return AlbumDetail{}, err
	}
	out := AlbumDetail{ID: albumID}
	if env.Album != nil {
		out.Name = env.Album.Name
		out.Cover = strings.Replace(strings.TrimSpace(env.Album.PicURL), "http://", "https://", 1)
		out.Size = env.Album.Size
		out.Year = msToYear(env.Album.PublishTime)
	}
	for _, s := range env.Songs {
		u, ok := s.unified()
		if !ok {
			continue
		}
		out.Tracks = append(out.Tracks, u)
		// 专辑艺术家取第一首的（网易的 album.artists 与曲目 ar 同源）。
		if out.Artist == "" {
			out.Artist = strings.ReplaceAll(u.Singer, ", ", "/")
			out.ArtistIDs = u.ArtistIDs
		}
	}
	if out.Size == 0 {
		out.Size = len(out.Tracks)
	}
	return out, nil
}

// FetchAlbumDetailQQ 取一张 QQ 音乐专辑的详情与完整曲目。
//
// 走的是 `fcg_v8_album_info_cp`（v8 老接口，2026-11-21 实测正常）。QQ 的
// **歌手**接口已下线（404），专辑接口还在 —— 所以 QQ 这轮只接专辑这一头。
func FetchAlbumDetailQQ(ctx context.Context, albumMid string) (AlbumDetail, error) {
	albumMid = strings.TrimSpace(albumMid)
	if albumMid == "" {
		return AlbumDetail{}, fmt.Errorf("专辑 mid 为空")
	}
	ctx, cancel := context.WithTimeout(ctx, searchBudget)
	defer cancel()
	rawURL := "https://c.y.qq.com/v8/fcg-bin/fcg_v8_album_info_cp.fcg?albummid=" +
		url.QueryEscape(albumMid) + "&format=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return AlbumDetail{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	req.Header.Set("Referer", "https://y.qq.com/")
	resp, err := doHTTP(req)
	if err != nil {
		return AlbumDetail{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return AlbumDetail{}, fmt.Errorf("QQ 专辑接口 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return AlbumDetail{}, err
	}
	var raw struct {
		Code int `json:"code"`
		Data struct {
			Mid          string `json:"mid"`
			Name         string `json:"name"`
			ADate        string `json:"aDate"`
			Singername   string `json:"singername"`
			TotalSongNum int    `json:"total_song_num"`
			List         []struct {
				Songmid   string `json:"songmid"`
				Songname  string `json:"songname"`
				Albummid  string `json:"albummid"`
				Albumname string `json:"albumname"`
				Interval  int    `json:"interval"`
				CdIdx     int    `json:"cdIdx"`
				Size128   int64  `json:"size128"`
				Size320   int64  `json:"size320"`
				SizeFlac  int64  `json:"sizeflac"`
				SizeApe   int64  `json:"sizeape"`
				Singer    []struct {
					ID   int64  `json:"id"`
					Mid  string `json:"mid"`
					Name string `json:"name"`
				} `json:"singer"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return AlbumDetail{}, err
	}
	if raw.Code != 0 {
		return AlbumDetail{}, fmt.Errorf("QQ 专辑接口 code=%d", raw.Code)
	}
	out := AlbumDetail{
		ID:     firstNonEmptyStr(raw.Data.Mid, albumMid),
		Name:   raw.Data.Name,
		Artist: raw.Data.Singername,
		Size:   raw.Data.TotalSongNum,
		Year:   yearFromDate(raw.Data.ADate),
	}
	for _, s := range raw.Data.List {
		if strings.TrimSpace(s.Songmid) == "" || strings.TrimSpace(s.Songname) == "" {
			continue
		}
		names := make([]string, 0, len(s.Singer))
		for _, a := range s.Singer {
			names = append(names, a.Name)
		}
		qs := qqQualitys(s.Size128, s.Size320, s.SizeFlac, 0, s.SizeApe)
		if len(qs) == 0 {
			qs = []string{Quality320k}
		}
		cover := ""
		if s.Albummid != "" {
			cover = fmt.Sprintf("https://y.gtimg.cn/music/photo_new/T002R300x300M000%s.jpg", s.Albummid)
		}
		out.Tracks = append(out.Tracks, UnifiedSong{
			ID:        "tx_" + s.Songmid,
			Songmid:   s.Songmid,
			Name:      s.Songname,
			Singer:    strings.Join(names, ", "),
			Album:     firstNonEmptyStr(s.Albumname, raw.Data.Name),
			AlbumMID:  s.Albummid,
			AlbumID:   s.Albummid,
			Cover:     cover,
			Source:    "tx",
			Duration:  s.Interval,
			Interval:  s.Interval,
			Quality:   bestQuality(qs),
			Qualitys:  qs,
			RawSource: "tx",
			Disc:      s.CdIdx,
		})
		if out.Cover == "" {
			out.Cover = cover
		}
	}
	if out.Size == 0 {
		out.Size = len(out.Tracks)
	}
	return out, nil
}

// FetchAlbumDetail 按平台分流到各自的原生专辑接口。
func FetchAlbumDetail(ctx context.Context, platform, albumID string) (AlbumDetail, error) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "wy":
		return FetchAlbumDetailNETEASE(ctx, albumID)
	case "tx":
		return FetchAlbumDetailQQ(ctx, albumID)
	}
	return AlbumDetail{}, fmt.Errorf("平台 %q 没有可用的原生专辑接口", platform)
}

// albumIDString 把（可能为 0 的）平台专辑 id 转成字符串，0 → 空串。
func albumIDString(id int64) string {
	if id <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", id)
}

// nonEmptyIDs 在一组歌手 id 里**任何一位**为空时返回 nil。
//
// 这是「半对半错的身份比没有更危险」的落点：曲目 VO 会按位置把歌手名与歌手 id
// 配对，缺一位就会把 A 的 id 贴到 B 的名字上 —— 点进去是别人。
func nonEmptyIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	for n, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil
		}
		out[n] = id
	}
	return out
}

// firstNonEmptyStr 返回第一个非空（去空白后）的字符串。
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
