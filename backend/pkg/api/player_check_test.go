package api

import (
	"net/http"
	"strings"
	"testing"
)

// 「探活过了却播不了」是用户最常见的抱怨之一，根因是防盗链 / 聚合接口的
// 失败响应**状态码也是 200**（回一页 HTML 或者 {"errorcode":-46628,...}）。
// 这组用例钉住判据：状态码 + 内容两条证据都要看。
func TestJudgePlayerCheck(t *testing.T) {
	flacHead := append([]byte("fLaC"), make([]byte, 40)...)
	mp3Head := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 40)...)
	htmlHead := []byte("\n<!DOCTYPE html><html><body>403 Forbidden</body></html>")
	jsonHead := []byte(`{"errorcode":-46628,"errormsg":"file not exist"}`)

	cases := []struct {
		name        string
		status      int
		statusText  string
		contentType string
		head        []byte
		wantOK      bool
		wantNoteHas string
	}{
		{"flac 206", http.StatusPartialContent, "206 Partial Content", "audio/flac", flacHead, true, ""},
		{"mp3 200", http.StatusOK, "200 OK", "audio/mpeg", mp3Head, true, ""},
		{"404", http.StatusNotFound, "404 Not Found", "text/html", htmlHead, false, "404"},
		{"200 但是 HTML 错误页", http.StatusOK, "200 OK", "text/html; charset=utf-8", htmlHead, false, "错误页"},
		{"200 但是 JSON 错误体", http.StatusOK, "200 OK", "application/json", jsonHead, false, "错误页"},
		{"403 且是错误页", http.StatusForbidden, "403 Forbidden", "text/plain", []byte("denied"), false, "403"},
		{"认不出容器（放行但说明）", http.StatusOK, "200 OK", "application/octet-stream", []byte{0x00, 0x01, 0x02, 0x03}, true, "认不出"},
		{"空体但状态正常", http.StatusOK, "200 OK", "audio/mpeg", nil, true, ""},
	}

	for _, c := range cases {
		ok, note := judgePlayerCheck(c.status, c.statusText, c.contentType, c.head)
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v，期望 %v（note=%q）", c.name, ok, c.wantOK, note)
			continue
		}
		if c.wantNoteHas != "" && !strings.Contains(note, c.wantNoteHas) {
			t.Errorf("%s: note=%q，期望包含 %q", c.name, note, c.wantNoteHas)
		}
		if c.wantNoteHas == "" && note != "" {
			t.Errorf("%s: 不该有 note，实际 %q", c.name, note)
		}
	}
}

// 预检读的是开头 2KB，不能把整个音频拉下来 —— 这条钉住「不会因为校验而下载整首歌」。
func TestJudgePlayerCheckAcceptsPrefixOnly(t *testing.T) {
	// 只有 2KB 前缀、没有完整文件时也要能判出来
	head := append([]byte("fLaC"), make([]byte, 2044)...)
	if ok, note := judgePlayerCheck(http.StatusPartialContent, "206", "audio/flac", head); !ok || note != "" {
		t.Fatalf("2KB 前缀应判为可播，实际 ok=%v note=%q", ok, note)
	}
}
