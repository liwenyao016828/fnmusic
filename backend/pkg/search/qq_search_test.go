package search

import (
	"slices"
	"strings"
	"testing"
)

// qqMobileFixture 是 c.y.qq.com/soso/fcgi-bin/search_for_qq_cp 真实响应的裁剪版：
// 键名与嵌套层级原样保留（2026-09-26 抓取），只砍掉本测试不关心的键和多余的歌。
// 三首歌分别覆盖：全档音质、只有 128k、一档都没有。
const qqMobileFixture = `{
  "code": 0,
  "subcode": 0,
  "data": {
    "keyword": "周杰伦",
    "song": {
      "curnum": 3,
      "curpage": 1,
      "totalnum": 600,
      "list": [
        {
          "songmid": "0039MnYb0qxYhV",
          "songname": "晴天",
          "singer": [{"id": 4558, "mid": "0025NhlN2yWrP4", "name": "周杰伦"}],
          "albumname": "叶惠美",
          "albummid": "000MkMni19ClKG",
          "interval": 269,
          "pubtime": 1059580800,
          "size128": 4317292,
          "size320": 10792943,
          "sizeflac": 55397039,
          "sizeape": 0,
          "pay": {"payplay": 1},
          "preview": {"trybegin": 84346, "tryend": 142843, "trysize": 960887}
        },
        {
          "songmid": "004Z8Ihr0JIu5s",
          "songname": "说了再见",
          "singer": [
            {"id": 4558, "mid": "0025NhlN2yWrP4", "name": "周杰伦"},
            {"id": 1234, "mid": "0012345678abcd", "name": "袁咏琳"}
          ],
          "albumname": "跨时代",
          "albummid": "",
          "interval": 261,
          "pubtime": 1273248000,
          "size128": 4194304,
          "size320": 0,
          "sizeflac": 0,
          "sizeape": 0
        },
        {
          "songmid": "005NoSizes00000",
          "songname": "无体积字段的歌",
          "singer": [{"id": 9, "mid": "00abcdefghijkl", "name": "某歌手"}],
          "albumname": "",
          "albummid": "001AlbumMidTest",
          "interval": 200,
          "pubtime": 0,
          "size128": 0,
          "size320": 0,
          "sizeflac": 0,
          "sizeape": 0
        }
      ]
    }
  }
}`

// qqAntiHotlinkShell 是不带 Referer 时 QQ 返回的真实拦截体 —— HTTP 状态码是 200，
// 所以只能靠 subcode 认出来，光看状态码会把它当成「搜到 0 条」。
const qqAntiHotlinkShell = `{"code":0,"message":"禁止跨域访问","notice":"","subcode":-10001,"time":1790440646,"tips":"Refer Error"}`

func TestParseQQMobileMapsFields(t *testing.T) {
	list := parseQQMobile([]byte(qqMobileFixture))
	if len(list) != 3 {
		t.Fatalf("解析出 %d 条，期望 3 条", len(list))
	}

	got := list[0]
	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"ID", got.ID, "tx_0039MnYb0qxYhV"},
		{"Songmid", got.Songmid, "0039MnYb0qxYhV"},
		{"Name", got.Name, "晴天"},
		{"Singer", got.Singer, "周杰伦"},
		{"Album", got.Album, "叶惠美"},
		{"AlbumMID", got.AlbumMID, "000MkMni19ClKG"},
		{"Cover", got.Cover, "https://y.gtimg.cn/music/photo_new/T002R300x300M000000MkMni19ClKG.jpg"},
		{"Duration", got.Duration, 269},
		{"Interval", got.Interval, 269},
		{"Source", got.Source, "tx"},
		{"RawSource", got.RawSource, "tx"},
		// pubtime=1059580800 → 2003；桌面接口下线后这是 QQ 唯一的年份来源
		{"Year", got.Year, 2003},
		// 本接口不返回 cdIdx/belongCD：曲序/光盘必须留 0，不许瞎猜
		{"Track", got.Track, 0},
		{"Disc", got.Disc, 0},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v，期望 %v", c.field, c.got, c.want)
		}
	}

	// 多歌手 → ", " 连接（与其它平台的拼法保持一致）
	if list[1].Singer != "周杰伦, 袁咏琳" {
		t.Errorf("多歌手 Singer = %q，期望 %q", list[1].Singer, "周杰伦, 袁咏琳")
	}
	// albummid 为空时不许拼出半个 URL
	if list[1].Cover != "" {
		t.Errorf("albummid 为空时 Cover = %q，期望空串", list[1].Cover)
	}
	if list[1].Year != 2010 {
		t.Errorf("pubtime=1273248000 的 Year = %d，期望 2010", list[1].Year)
	}
}

func TestParseQQMobileQualitys(t *testing.T) {
	list := parseQQMobile([]byte(qqMobileFixture))
	if len(list) != 3 {
		t.Fatalf("解析出 %d 条，期望 3 条", len(list))
	}

	// 128/320/flac 三档齐全 → 最优是 flac
	if list[0].Quality != QualityFlac {
		t.Errorf("晴天 Quality = %q，期望 %q", list[0].Quality, QualityFlac)
	}
	for _, q := range []string{Quality128k, Quality320k, QualityFlac} {
		if !slices.Contains(list[0].Qualitys, q) {
			t.Errorf("晴天 Qualitys = %v，缺少 %q", list[0].Qualitys, q)
		}
	}

	// 只有 128k
	if list[1].Quality != Quality128k {
		t.Errorf("说了再见 Quality = %q，期望 %q", list[1].Quality, Quality128k)
	}

	// 一档都没有 → 兜底给 320k（沿用改动前的行为）
	if list[2].Quality != Quality320k || len(list[2].Qualitys) != 1 {
		t.Errorf("无体积字段的歌 Quality/Qualitys = %q/%v，期望 %q/[%q]",
			list[2].Quality, list[2].Qualitys, Quality320k, Quality320k)
	}
}

func TestParseQQMobileTreatsAntiHotlinkShellAsFailure(t *testing.T) {
	// 拦截体是 200 + 空壳：必须当失败（nil），不能当「0 条结果」——
	// 否则平台故障会被上层读成「这首歌不存在」。
	if got := parseQQMobile([]byte(qqAntiHotlinkShell)); got != nil {
		t.Errorf("拦截体应返回 nil，实际 %d 条", len(got))
	}
}

func TestParseQQMobileEmptyAndBadBody(t *testing.T) {
	if got := parseQQMobile([]byte(`{"code":0,"subcode":0,"data":{"song":{"list":[],"totalnum":0}}}`)); len(got) != 0 {
		t.Errorf("空列表应解析出 0 条，实际 %d 条", len(got))
	}
	if got := parseQQMobile([]byte(`<html>500</html>`)); got != nil {
		t.Errorf("非 JSON 应返回 nil，实际 %d 条", len(got))
	}
	if got := parseQQMobile(nil); got != nil {
		t.Errorf("空 body 应返回 nil，实际 %d 条", len(got))
	}
}

func TestQQMobileSearchURL(t *testing.T) {
	got := qqMobileSearchURL("周杰伦", 2, 30)
	// 端点写错 = HTTP 500 + body 空 → 静默「搜到 0 条」的静默故障，钉死它。
	// （原桌面接口 client_search_cp 就是这么死的，见 qqSearchEndpoint 注释。）
	if !strings.HasPrefix(got, "https://c.y.qq.com/soso/fcgi-bin/search_for_qq_cp?") {
		t.Fatalf("端点不对：%s", got)
	}
	if !strings.HasSuffix(got, "&p=2&n=30&format=json") {
		t.Errorf("分页参数没接上：%s", got)
	}
	if !strings.Contains(got, "w=%E5%91%A8%E6%9D%B0%E4%BC%A6") {
		t.Errorf("关键词没转义：%s", got)
	}

	// 关键词里的 & 必须转义成 %26，否则会把查询串截断（空格转成 + 也是对的）
	nasty := qqMobileSearchURL("周杰伦 & 晴天", 1, 20)
	if !strings.Contains(nasty, "%26") {
		t.Errorf("关键词里的 & 没转义，会截断查询串：%s", nasty)
	}
	if strings.Contains(nasty, " ") || strings.HasSuffix(nasty, "晴天") {
		t.Errorf("关键词没整体转义：%s", nasty)
	}
}
