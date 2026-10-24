package appupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const releaseFixture = `{
  "tag_name": "v2.19.0",
  "name": "2.19.0",
  "body": "## 更新内容\n- 修复 X",
  "published_at": "2026-10-24T01:02:03Z",
  "html_url": "https://github.com/liwenyao016828/fnmusic/releases/tag/v2.19.0",
  "assets": [
    {"name": "checksums.txt", "browser_download_url": "https://example.com/checksums.txt", "size": 10},
    {"name": "yinshu-ai-2.19.0-arm64.fpk", "browser_download_url": "https://example.com/small.fpk", "size": 100},
    {"name": "yinshu-ai-2.19.0-x86_64.fpk", "browser_download_url": "https://example.com/big.fpk", "size": 900}
  ]
}`

func TestParseReleaseAndPickFPK(t *testing.T) {
	rel, err := ParseRelease([]byte(releaseFixture))
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v2.19.0" || rel.PublishedAt == "" || rel.HTMLURL == "" {
		t.Fatalf("基本字段没解析出来：%+v", rel)
	}
	asset := PickFPKAsset(rel.Assets)
	if asset == nil {
		t.Fatal("没找到 fpk")
	}
	if asset.Name != "yinshu-ai-2.19.0-x86_64.fpk" || asset.Size != 900 {
		t.Fatalf("应选最大的 fpk，实际 %+v", asset)
	}
}

func TestParseReleaseMissingTag(t *testing.T) {
	if _, err := ParseRelease([]byte(`{"assets":[]}`)); err == nil {
		t.Fatal("缺 tag_name 应报错")
	}
	if _, err := ParseRelease([]byte(`not json`)); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestPickFPKAssetIgnoresNonFPK(t *testing.T) {
	if a := PickFPKAsset([]ReleaseAsset{{Name: "notes.txt", DownloadURL: "x", Size: 5}}); a != nil {
		t.Fatalf("非 fpk 不应被选中：%+v", a)
	}
	if a := PickFPKAsset(nil); a != nil {
		t.Fatalf("空 assets 应返回 nil")
	}
}

func TestFetchLatestReleaseHTTPErrors(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusNotFound, "404"},
		{http.StatusForbidden, "限速"},
		{http.StatusInternalServerError, "HTTP 500"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte("nope"))
		}))
		_, err := FetchLatestRelease(context.Background(), srv.Client(), srv.URL, "o/r", "", "test")
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("status %d: err=%v want contains %q", c.status, err, c.want)
		}
	}
}

func TestFetchLatestReleaseSendsRequiredHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(releaseFixture))
	}))
	defer srv.Close()
	if _, err := FetchLatestRelease(context.Background(), srv.Client(), srv.URL, "o/r", "tok", "ua/1"); err != nil {
		t.Fatal(err)
	}
	if got.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("Accept=%q", got.Get("Accept"))
	}
	if got.Get("User-Agent") == "" {
		t.Error("缺少 User-Agent")
	}
	if got.Get("Authorization") != "Bearer tok" {
		t.Errorf("Authorization=%q", got.Get("Authorization"))
	}
}
