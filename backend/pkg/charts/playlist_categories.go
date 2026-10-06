package charts

// 歌单分类（前端「快捷筛选」用的那排标签）。
//
// 各平台能力不同，不是都有分类：
//   - 网易云：接口直接吃分类**名字**（cat=华语），分类表是固定的
//   - QQ    ：有「分类配置」接口，**动态拉取**（不写死映射，避免它调整后静默失效）
//   - 酷狗 / 酷我：当前的歌单接口不吃分类参数，返回空列表
//
// 前端据此决定要不要显示筛选条 —— 没有分类的平台就不显示，
// 而不是显示一排点了没反应的按钮。

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
)

// PlaylistCategory 一个歌单分类。
type PlaylistCategory struct {
	// ID 平台内的分类标识：网易云用分类名，QQ 用数字 id
	ID string `json:"id"`
	// Name 展示名
	Name string `json:"name"`
}

// neteasePlaylistCategories 网易云的分类表。
// 网易接口按**名字**筛选，所以 ID 与 Name 相同。
var neteasePlaylistCategories = []PlaylistCategory{
	{ID: "全部", Name: "全部"},
	{ID: "华语", Name: "华语"},
	{ID: "流行", Name: "流行"},
	{ID: "摇滚", Name: "摇滚"},
	{ID: "民谣", Name: "民谣"},
	{ID: "经典怀旧", Name: "经典怀旧"},
	{ID: "轻音乐", Name: "轻音乐"},
	{ID: "ACG", Name: "ACG"},
	{ID: "车载", Name: "车载"},
	{ID: "电音", Name: "电音"},
	{ID: "欧美", Name: "欧美"},
	{ID: "粤语", Name: "粤语"},
}

// HandlePlaylistCategories 返回某平台的歌单分类列表。
//
//	GET /api/charts/playlist/categories?source=wy|tx
//
// 没有分类能力的平台返回空数组（而不是报错）—— 前端据此隐藏筛选条。
func (cm *ChartManager) HandlePlaylistCategories(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source == "" {
		source = "wy"
	}

	var (
		list []PlaylistCategory
		err  error
	)
	switch source {
	case "wy":
		list = neteasePlaylistCategories
	case "tx":
		list, err = cm.fetchQQPlaylistCategories()
	case "kg", "kw":
		// 这两个平台当前的歌单接口不吃分类参数，明确返回空
		list = []PlaylistCategory{}
	default:
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    400,
			"message": fmt.Sprintf("不支持的平台 %q（支持 wy / tx / kg / kw）", source),
		})
		return
	}

	if err != nil {
		// 拉不到分类不算致命：返回空列表 + 原因，前端隐藏筛选条即可
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"data": map[string]any{
				"source":     source,
				"categories": []PlaylistCategory{},
				"warning":    err.Error(),
			},
		})
		return
	}

	if list == nil {
		list = []PlaylistCategory{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 200,
		"data": map[string]any{
			"source":     source,
			"categories": list,
			"count":      len(list),
		},
	})
}

// fetchQQPlaylistCategories 从 QQ 的分类配置接口动态拉取分类。
//
// 为什么动态拉而不写死一张 id→名称 的表：分类 id 是 QQ 内部的编号，
// 写死的表在它调整后会**静默失效**（表现为筛选点了没反应）；
// 这个接口返回的才是权威数据。拉不到时上层会隐藏筛选条，不会误导用户。
func (cm *ChartManager) fetchQQPlaylistCategories() ([]PlaylistCategory, error) {
	apiURL := "https://c.y.qq.com/splcloud/fcgi-bin/fcg_get_diss_tag_conf.fcg" +
		"?format=json&inCharset=utf8&outCharset=utf-8"

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://y.qq.com/")

	resp, err := cm.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取 QQ 分类失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 QQ 分类响应失败: %w", err)
	}

	var parsed struct {
		Code int `json:"code"`
		Data struct {
			Categories []struct {
				GroupName string `json:"categoryGroupName"`
				Items     []struct {
					CategoryID   int    `json:"categoryId"`
					CategoryName string `json:"categoryName"`
					Usable       int    `json:"usable"`
				} `json:"items"`
			} `json:"categories"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("解析 QQ 分类失败: %w", err)
	}
	if parsed.Code != 0 {
		return nil, fmt.Errorf("QQ 分类接口返回 code=%d: %s", parsed.Code, parsed.Message)
	}

	// 「全部」用 QQ 约定的 categoryId=10000000（当前 fetchAndWriteQQPlaylists 的默认值）
	out := []PlaylistCategory{{ID: "10000000", Name: "全部"}}
	seen := map[string]bool{"10000000": true}

	for _, group := range parsed.Data.Categories {
		for _, item := range group.Items {
			// usable=0 表示该分类当前不可用；10000000 是「全部」，已加过
			if item.Usable == 0 || item.CategoryID == 0 || item.CategoryID == 10000000 {
				continue
			}
			// ⚠️ QQ 返回的分类名带 HTML 实体：实测「R&#38;B」应为「R&B」。
			// 前端用 {{ }} 渲染（会转义），不解码就会原样显示成 "R&#38;B"。
			name := strings.TrimSpace(html.UnescapeString(item.CategoryName))
			if name == "" {
				continue
			}
			id := fmt.Sprintf("%d", item.CategoryID)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, PlaylistCategory{ID: id, Name: name})
		}
	}
	return out, nil
}
