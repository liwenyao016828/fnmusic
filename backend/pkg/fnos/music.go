// 飞牛音乐应用本地接口客户端。
//
// 与官方开放 API（openapi.go）不同，这是飞牛「音乐」应用自己的接口：
//   - Unix socket：/var/run/trim_music.socket
//   - 基地址：http://localhost/music/api/v1
//   - 认证：Authorization 头直接放音乐应用的登录令牌（**不带 Bearer 前缀**）
//   - 响应：{code, data, msg}，code == 0 表示成功
//
// 令牌来源见 token.go（环境变量 FNOS_TOKEN，或只读飞牛音乐库）。
package fnos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	musicSocketDefault = "/var/run/trim_music.socket"
	musicBaseDefault   = "http://localhost/music/api/v1"
	// addTrackBatch 为单次加曲上限，与飞牛音乐接口约定一致
	addTrackBatch = 50
	// playlistPageSize 为歌单曲目分页大小
	playlistPageSize = 50
)

// 声明为变量以便测试注入。
var (
	musicSocket = musicSocketDefault
	musicBase   = musicBaseDefault
)

// Music 是飞牛音乐本地接口客户端。
type Music struct {
	token  string
	socket string
	base   string
	client *http.Client
}

// NewMusic 创建客户端。token 为空时自动从环境变量/音乐库读取。
func NewMusic(token string) *Music {
	return NewMusicAt(token, musicSocket, musicBase)
}

// NewMusicAt 用指定的 socket 与基地址创建客户端。
// 供测试注入，或用于非常规部署（例如把飞牛 socket 转发到别的路径）。
func NewMusicAt(token, socket, base string) *Music {
	if strings.TrimSpace(token) == "" {
		token = MusicToken()
	}
	return &Music{
		token:  token,
		socket: socket,
		base:   base,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

// Available 报告能否调用飞牛音乐接口：socket 存在且能拿到令牌。
func (m *Music) Available() bool {
	if m.token == "" {
		return false
	}
	_, err := os.Stat(m.socket)
	return err == nil
}

type musicResponse struct {
	Code    int             `json:"code"`
	Msg     string          `json:"msg"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (r musicResponse) errText() string {
	if r.Msg != "" {
		return r.Msg
	}
	return r.Message
}

// call 发起一次请求并把 data 反序列化到 out（out 可为 nil）。
func (m *Music) call(method, apiPath string, body any, out any) error {
	if m.token == "" {
		return fmt.Errorf("飞牛音乐没有可用登录令牌")
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, m.base+apiPath, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// 飞牛音乐接口直接使用裸令牌，不加 Bearer 前缀
	req.Header.Set("Authorization", m.token)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("调用飞牛音乐 %s 失败: %w", apiPath, err)
	}
	defer resp.Body.Close()

	var result musicResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("飞牛音乐 %s 返回非 JSON (HTTP %d): %w", apiPath, resp.StatusCode, err)
	}
	if result.Code != 0 {
		return fmt.Errorf("飞牛音乐 %s 失败 (code=%d): %s", apiPath, result.Code, result.errText())
	}
	if out != nil && len(result.Data) > 0 {
		return json.Unmarshal(result.Data, out)
	}
	return nil
}

// rows 从飞牛音乐返回的 data 中提取对象数组。
// 接口有时直接给数组，有时包一层 list/items/tracks/data，这里统一处理。
func rows(raw json.RawMessage) []map[string]any {
	var direct []map[string]any
	if json.Unmarshal(raw, &direct) == nil {
		return direct
	}
	var wrapped map[string]json.RawMessage
	if json.Unmarshal(raw, &wrapped) != nil {
		return nil
	}
	for _, key := range []string{"list", "items", "tracks", "data"} {
		if nested, ok := wrapped[key]; ok {
			if r := rows(nested); len(r) > 0 {
				return r
			}
		}
	}
	return nil
}

// Me 返回当前登录账号信息，可用于校验令牌是否有效。
func (m *Music) Me() (map[string]any, error) {
	var data json.RawMessage
	if err := m.call(http.MethodGet, "/user/me", nil, &data); err != nil {
		return nil, err
	}
	var account map[string]any
	_ = json.Unmarshal(data, &account)
	return account, nil
}

// Playlists 返回飞牛音乐的歌单列表。
func (m *Music) Playlists() ([]map[string]any, error) {
	var data json.RawMessage
	if err := m.call(http.MethodGet, "/playlist/list", nil, &data); err != nil {
		return nil, err
	}
	return rows(data), nil
}

// SearchTracks 在飞牛曲库中按关键词搜索曲目。
func (m *Music) SearchTracks(keyword string) ([]map[string]any, error) {
	var data json.RawMessage
	apiPath := "/search/track?q=" + url.QueryEscape(keyword)
	if err := m.call(http.MethodGet, apiPath, nil, &data); err != nil {
		return nil, err
	}
	return rows(data), nil
}

// PlaylistTracks 返回歌单全部曲目（自动分页）。
func (m *Music) PlaylistTracks(guid string) ([]map[string]any, error) {
	all := make([]map[string]any, 0)
	for page := 1; page <= 1000; page++ {
		var data json.RawMessage
		apiPath := fmt.Sprintf("/track/playlist-detail/list?playlistGUID=%s&page=%d&pageSize=%d",
			url.QueryEscape(guid), page, playlistPageSize)
		if err := m.call(http.MethodGet, apiPath, nil, &data); err != nil {
			return nil, err
		}
		page_rows := rows(data)
		all = append(all, page_rows...)
		if len(page_rows) < playlistPageSize {
			return all, nil
		}
	}
	return all, nil
}

// CreatePlaylist 创建歌单并返回其 guid。
func (m *Music) CreatePlaylist(name, description string) (string, error) {
	var data map[string]any
	err := m.call(http.MethodPost, "/playlist/create", map[string]any{
		"name":        name,
		"visibility":  1,
		"description": description,
	}, &data)
	if err != nil {
		return "", err
	}
	if guid, ok := data["guid"].(string); ok && guid != "" {
		return guid, nil
	}
	return "", fmt.Errorf("飞牛音乐创建歌单未返回 guid")
}

// FindOrCreatePlaylist 按名称精确查找歌单，不存在则创建。
// 存在同名多个歌单时返回错误，避免误推。
func (m *Music) FindOrCreatePlaylist(name, description string) (guid string, created bool, err error) {
	list, err := m.Playlists()
	if err != nil {
		return "", false, err
	}
	matches := make([]string, 0, 1)
	for _, item := range list {
		if n, _ := item["name"].(string); n == name {
			if g, _ := item["guid"].(string); g != "" {
				matches = append(matches, g)
			}
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], false, nil
	case 0:
		guid, err := m.CreatePlaylist(name, description)
		return guid, true, err
	default:
		return "", false, fmt.Errorf("飞牛音乐存在 %d 个同名歌单 %q，请先处理重名", len(matches), name)
	}
}

// AddTracks 把曲目 guid 加入歌单，自动分批（每批 50）。
func (m *Music) AddTracks(playlistGUID string, trackGUIDs []string) error {
	for i := 0; i < len(trackGUIDs); i += addTrackBatch {
		end := i + addTrackBatch
		if end > len(trackGUIDs) {
			end = len(trackGUIDs)
		}
		err := m.call(http.MethodPost, "/playlist/add-track", map[string]any{
			"guid":       playlistGUID,
			"trackGUIDs": trackGUIDs[i:end],
		}, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

// RemoveTracks 把曲目 guid 从歌单移除，自动分批（每批 50）。
//
// 用于「推送保留期策略」：只移除**我们自己推送过**的曲目，
// 绝不碰用户手动加进歌单的歌（判断逻辑在 pkg/push/retention.go）。
func (m *Music) RemoveTracks(playlistGUID string, trackGUIDs []string) error {
	for i := 0; i < len(trackGUIDs); i += addTrackBatch {
		end := i + addTrackBatch
		if end > len(trackGUIDs) {
			end = len(trackGUIDs)
		}
		err := m.call(http.MethodPost, "/playlist/remove-track", map[string]any{
			"guid":       playlistGUID,
			"trackGUIDs": trackGUIDs[i:end],
		}, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

// SetPlaylistCover 用封面图片字节替换歌单封面。
// 先上传图片取得 coverId，再写回歌单；失败只返回错误，不影响已加入的曲目。
func (m *Music) SetPlaylistCover(playlistGUID, title string, image []byte, filename string) error {
	if len(image) == 0 {
		return nil
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(image); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, m.base+"/static/cover/playlist", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", m.token)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("上传歌单封面失败: %w", err)
	}
	defer resp.Body.Close()

	var uploaded musicResponse
	if err := json.NewDecoder(resp.Body).Decode(&uploaded); err != nil {
		return fmt.Errorf("上传歌单封面返回非 JSON: %w", err)
	}
	if uploaded.Code != 0 {
		return fmt.Errorf("上传歌单封面失败 (code=%d): %s", uploaded.Code, uploaded.errText())
	}

	coverID := findCoverID(uploaded.Data)
	if coverID == "" {
		return fmt.Errorf("上传歌单封面未返回 coverId")
	}
	// 服务端对可选的 description 字段较敏感，这里只改封面与名称
	return m.call(http.MethodPost, "/playlist/edit", map[string]any{
		"guid":    playlistGUID,
		"name":    title,
		"coverId": coverID,
	}, nil)
}

// ScanLibrary 触发飞牛音乐重新扫描共享曲库。
func (m *Music) ScanLibrary() error {
	return m.call(http.MethodPost, "/shared-library/scan-all", nil, nil)
}

// findCoverID 从上传响应中提取封面 id（接口可能多层嵌套）。
func findCoverID(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, key := range []string{"coverId", "id", "guid"} {
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
	}
	if nested, ok := m["data"]; ok {
		if b, err := json.Marshal(nested); err == nil {
			return findCoverID(b)
		}
	}
	return ""
}
