package nas

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"fn-lx-player/pkg/tags"
)

// 读标签接口必须把**风格/年份/曲序/光盘**一起回出来。
//
// 守的是曲库管家「补全结果卡片」：卡片要显示的是**文件里真实的**那四栏，
// 而不是补全时"打算写成什么"。以前这个接口只回 title/artist/album，
// 卡片就只能拿预览值凑，万一某首写失败，界面还在说它补好了。
func TestReadTagsReturnsNewFields(t *testing.T) {
	root := t.TempDir()
	setAllowedRoots(t, realPath(t, root))

	path := filepath.Join(root, "夜曲.mp3")
	raw := append([]byte{0xFF, 0xFB, 0x90, 0x00}, []byte("AUDIO-PAYLOAD")...)
	mustWrite(t, path, raw)

	if _, err := tags.Write(path, tags.Metadata{
		Title: "夜曲", Artist: "周杰伦", Album: "十一月的萧邦",
		Genre: "Pop 流行", Year: 2005, Track: 3, Disc: 1,
	}); err != nil {
		t.Fatalf("建立标签失败: %v", err)
	}

	rec := httptest.NewRecorder()
	HandleReadTags(rec, httptest.NewRequest(http.MethodGet, "/api/nas/tags?path="+path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			Title    string `json:"title"`
			Artist   string `json:"artist"`
			Album    string `json:"album"`
			Genre    string `json:"genre"`
			Year     int    `json:"year"`
			Track    int    `json:"track"`
			Disc     int    `json:"disc"`
			HasLyric bool   `json:"has_lyric"`
			HasCover bool   `json:"has_cover"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.Genre != "Pop 流行" || resp.Data.Year != 2005 || resp.Data.Track != 3 || resp.Data.Disc != 1 {
		t.Errorf("新字段未透出: %+v", resp.Data)
	}
	if resp.Data.Title != "夜曲" || resp.Data.Artist != "周杰伦" {
		t.Errorf("原有字段回退了: %+v", resp.Data)
	}
	// 没写的字段要回 0 / 空而不是缺键 —— 前端卡片按「值为 0 就不显示」渲染。
	if resp.Data.HasLyric || resp.Data.HasCover {
		t.Errorf("这份标签里没有歌词封面，却报了有: %+v", resp.Data)
	}
}

// 读不到的文件仍然要给出可读的错误，而不是把卡片渲染成"全空但成功"。
func TestReadTagsRejectsPathOutsideAllowedRoots(t *testing.T) {
	root := t.TempDir()
	setAllowedRoots(t, realPath(t, root))

	outside := filepath.Join(t.TempDir(), "别的目录.mp3")
	if err := os.WriteFile(outside, []byte{0xFF, 0xFB, 0x90, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	HandleReadTags(rec, httptest.NewRequest(http.MethodGet, "/api/nas/tags?path="+outside, nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("越界路径应 400，实际 %d %s", rec.Code, rec.Body.String())
	}
}
