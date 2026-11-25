package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"fn-lx-player/pkg/audioext"
	"fn-lx-player/pkg/audioprobe"
	"fn-lx-player/pkg/namefmt"
	"fn-lx-player/pkg/search"
	"time"
)

type audioAuditIssue struct {
	Path          string   `json:"path"`
	Name          string   `json:"name"`
	Title         string   `json:"title,omitempty"`
	Artist        string   `json:"artist,omitempty"`
	Ext           string   `json:"ext,omitempty"`
	Container     string   `json:"container,omitempty"`
	DurationSec   float64  `json:"duration_sec,omitempty"`
	Size          int64    `json:"size"`
	NominalKbps   int      `json:"nominal_kbps,omitempty"`
	EffectiveKbps float64  `json:"effective_kbps,omitempty"`
	SampleRate    int      `json:"sample_rate,omitempty"`
	Channels      int      `json:"channels,omitempty"`
	Issues        []string `json:"issues"`
}

// HandleAudioAudit 扫描索引目录内的音频文件头，报告可证实的格式/大小异常；不做听感判断。
func (s *Server) HandleAudioAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": 405, "message": "只支持 POST"})
		return
	}
	var body struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		errJSON(w, 400, "请求内容无效")
		return
	}
	dir := s.libraryDirOrDefault(body.Dir)
	if dir == "" {
		errJSON(w, http.StatusBadRequest, "还没有被授权访问任何目录")
		return
	}
	root, err := resolveLibraryPath(dir)
	if err != nil {
		errJSON(w, http.StatusForbidden, err.Error())
		return
	}
	entries, total := s.libraryIndex().ListDir(root, 0, 0)
	issues := make([]audioAuditIssue, 0)
	scanned := 0
	for _, entry := range entries {
		if r.Context().Err() != nil {
			return
		}
		if !audioext.Has(entry.Path) || strings.Contains(filepath.ToSlash(entry.Path), "/.tidy_trash/") {
			continue
		}
		path, err := resolveLibraryPath(entry.Path)
		if err != nil {
			continue
		}
		info, err := audioprobe.Probe(path)
		if err != nil && !errors.Is(err, audioprobe.ErrTooShort) {
			continue
		}
		scanned++
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
		var checks []string
		if info.Size < 12 {
			checks = append(checks, "文件过小，无法识别音频")
		}
		claimed := audioprobe.ExtContainer("." + ext)
		if claimed != "" && info.Container != "" && claimed != info.Container {
			checks = append(checks, "扩展名与实际容器不符")
		}
		if audioext.LosslessContainer("."+ext) && info.DurationSec > 1 && info.EffectiveKbps > 0 && info.EffectiveKbps < 200 {
			checks = append(checks, "无损文件等效码率低于 200 kbps，疑似假无损或截断")
		}
		if !audioext.LosslessContainer("."+ext) && info.DurationSec > 30 && info.EffectiveKbps > 0 && info.EffectiveKbps < 48 {
			checks = append(checks, "有损文件等效码率低于 48 kbps，疑似低码率或截断")
		}
		if len(checks) == 0 {
			continue
		}
		artist, title := namefmt.Parse(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		if entry.Title != "" {
			title = entry.Title
		}
		if entry.Artist != "" {
			artist = entry.Artist
		}
		issues = append(issues, audioAuditIssue{Path: path, Name: filepath.Base(path), Title: title, Artist: artist, Ext: ext, Container: info.Container, DurationSec: info.DurationSec, Size: info.Size, NominalKbps: info.NominalKbps, EffectiveKbps: info.EffectiveKbps, SampleRate: info.SampleRate, Channels: info.Channels, Issues: checks})
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200, "data": map[string]any{"dir": root, "total": total, "scanned": scanned, "issues": issues}})
}

// HandleRepairDuration reads only the selected file; platform matching is a separate search.
func (s *Server) HandleRepairDuration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, 405, "只支持 POST")
		return
	}
	var body struct {
		Path string `json:"path"`
		Dir  string `json:"dir"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || !audioext.Has(body.Path) {
		errJSON(w, 400, "请选择音频文件")
		return
	}
	root, err := resolveLibraryPath(s.libraryDirOrDefault(body.Dir))
	if err != nil {
		errJSON(w, 403, "曲库目录不可访问")
		return
	}
	path, err := resolveLibraryPath(body.Path)
	if err != nil {
		errJSON(w, 403, "音频文件不可访问")
		return
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		errJSON(w, 403, "文件不在当前曲库目录")
		return
	}
	info, err := audioprobe.Probe(path)
	if err != nil && !errors.Is(err, audioprobe.ErrTooShort) {
		errJSON(w, 400, "无法读取音频文件")
		return
	}
	writeJSON(w, 200, map[string]any{"code": 200, "data": info})
}

// HandleRepairVersionAudit compares local duration against title-matched platform candidates.
// It deliberately does not assert identity: equal duration is supporting evidence only.
func (s *Server) HandleRepairVersionAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, 405, "只支持 POST")
		return
	}
	var body struct {
		Dir   string   `json:"dir"`
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		errJSON(w, 400, "请求内容无效")
		return
	}
	if len(body.Paths) > 20 {
		body.Paths = body.Paths[:20]
	}
	root, err := resolveLibraryPath(s.libraryDirOrDefault(body.Dir))
	if err != nil {
		errJSON(w, 403, "曲库目录不可访问")
		return
	}
	index := s.libraryIndex()
	out := make([]map[string]any, 0, len(body.Paths))
	for _, raw := range body.Paths {
		if r.Context().Err() != nil {
			return
		}
		path, err := resolveLibraryPath(raw)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		info, err := audioprobe.Probe(path)
		if err != nil && !errors.Is(err, audioprobe.ErrTooShort) {
			continue
		}
		entry, _ := index.Get(filepath.ToSlash(path))
		title, artist := entry.Title, entry.Artist
		if title == "" {
			artist, title = namefmt.Parse(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		}
		row := map[string]any{"path": path, "title": title, "artist": artist, "local_duration_sec": info.DurationSec, "status": "unknown", "reason": "没有足够的同版本证据"}
		if title != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			var candidates []search.UnifiedSong
			for _, platform := range []string{"wy", "tx", "kg", "kw"} {
				candidates = append(candidates, search.SearchNativeContext(ctx, title, platform, 1, 8)...)
				if ctx.Err() != nil {
					break
				}
			}
			cancel()
			bestDiff := float64(1 << 30)
			best := 0.0
			bestName := ""
			for _, c := range candidates {
				if c.Duration <= 0 {
					continue
				}
				d := float64(c.Duration) - info.DurationSec
				if d < 0 {
					d = -d
				}
				if d < bestDiff {
					bestDiff = d
					best = float64(c.Duration)
					bestName = c.Name
				}
			}
			if best > 0 && info.DurationSec > 0 {
				row["candidate_duration_sec"] = best
				row["candidate_title"] = bestName
				row["delta_sec"] = bestDiff
				row["status"] = "matched"
				row["reason"] = "时长接近；仅供核对，不代表已确认同一版本"
				if bestDiff > 5 {
					row["status"] = "different"
					row["reason"] = "与同名平台候选时长相差较大，可能是不同版本"
				}
			}
		}
		out = append(out, row)
	}
	writeJSON(w, 200, map[string]any{"code": 200, "data": map[string]any{"items": out, "checked": len(out)}})
}
