package search

import (
	"reflect"
	"testing"
)

func TestSortQualitysDesc(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "按档位从高到低",
			in:   []string{"128k", "flac", "320k", "flac24bit"},
			want: []string{"flac24bit", "flac", "320k", "128k"},
		},
		{
			name: "去重",
			in:   []string{"320k", "320k", "128k"},
			want: []string{"320k", "128k"},
		},
		{
			name: "大写与空白被归一化",
			in:   []string{" FLAC ", "320K"},
			want: []string{"flac", "320k"},
		},
		{
			name: "空串被丢弃",
			in:   []string{"", "  ", "128k"},
			want: []string{"128k"},
		},
		{
			name: "非阶梯值排在标准档位之后",
			in:   []string{"hires", "128k", "flac"},
			want: []string{"flac", "128k", "hires"},
		},
		{
			name: "空输入返回空切片",
			in:   nil,
			want: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sortQualitysDesc(c.in)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("sortQualitysDesc(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestBestQuality(t *testing.T) {
	if got := bestQuality([]string{"128k", "flac", "320k"}); got != "flac" {
		t.Fatalf("bestQuality = %q, want flac", got)
	}
	if got := bestQuality([]string{"flac", "flac24bit"}); got != "flac24bit" {
		t.Fatalf("bestQuality = %q, want flac24bit", got)
	}
	if got := bestQuality(nil); got != "" {
		t.Fatalf("bestQuality(nil) = %q, want 空串", got)
	}
}

func TestQualityFromBitrateKbps(t *testing.T) {
	cases := []struct {
		kbps int
		want string
	}{
		{48, ""},  // aac48，低于阶梯下限
		{96, ""},  // ogg96，低于阶梯下限
		{100, ""}, // ogg100
		{128, Quality128k},
		{192, Quality192k},
		{300, Quality320k},
		{320, Quality320k},
		{937, QualityFlac},
		{1642, QualityFlac},
	}
	for _, c := range cases {
		if got := qualityFromBitrateKbps(c.kbps); got != c.want {
			t.Errorf("qualityFromBitrateKbps(%d) = %q, want %q", c.kbps, got, c.want)
		}
	}
}

// 以下用例的输入全部取自真实 API 响应（2026-09 实测）。
func TestNeteaseQualitys(t *testing.T) {
	lv := func(br int) *neteaseLevelInfo { return &neteaseLevelInfo{Br: br} }

	cases := []struct {
		name            string
		l, m, h, sq, hr *neteaseLevelInfo
		want            []string
	}{
		{
			name: "普通无损曲目：128/192/320/sq，无 hr",
			l:    lv(128000), m: lv(192000), h: lv(320000), sq: lv(1607018), hr: nil,
			want: []string{"flac", "320k", "192k", "128k"},
		},
		{
			name: "Hi-Res 曲目：sq + hr 同时存在",
			l:    lv(128000), m: lv(192000), h: lv(320000), sq: lv(646443), hr: lv(1394128),
			want: []string{"flac24bit", "flac", "320k", "192k", "128k"},
		},
		{
			name: "仅低音质曲目",
			l:    lv(128000), m: nil, h: nil, sq: nil, hr: nil,
			want: []string{"128k"},
		},
		{
			name: "全部缺失时不臆造档位",
			l:    nil, m: nil, h: nil, sq: nil, hr: nil,
			want: []string{},
		},
		{
			name: "码率为 0 视为不可用",
			l:    lv(0), m: lv(0), h: lv(0), sq: lv(0), hr: lv(0),
			want: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := neteaseQualitys(c.l, c.m, c.h, c.sq, c.hr)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("neteaseQualitys = %v, want %v", got, c.want)
			}
		})
	}
}

func TestQQQualitys(t *testing.T) {
	cases := []struct {
		name                                       string
		size128, size320, sizeflac, sizehires, ape int64
		want                                       []string
	}{
		{
			name:    "实测《晴天》：128/320/flac 齐备，ape 为 0",
			size128: 4317292, size320: 10792943, sizeflac: 55397039, sizehires: 0, ape: 0,
			want: []string{"flac", "320k", "128k"},
		},
		{
			name:    "含 hires 时补上最高档",
			size128: 100, size320: 200, sizeflac: 300, sizehires: 400, ape: 0,
			want: []string{"flac24bit", "flac", "320k", "128k"},
		},
		{
			name:    "仅有 APE 时归入 flac 档",
			size128: 100, size320: 0, sizeflac: 0, sizehires: 0, ape: 500,
			want: []string{"flac", "128k"},
		},
		{
			name: "全为 0 时不产生档位",
			want: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := qqQualitys(c.size128, c.size320, c.sizeflac, c.sizehires, c.ape)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("qqQualitys = %v, want %v", got, c.want)
			}
		})
	}
}

func TestKugouQualitys(t *testing.T) {
	cases := []struct {
		name                     string
		file, hq, sq, res, super string
		want                     []string
	}{
		{
			name:  "实测《晴天》：128/320/SQ/Res 齐备，Super 为空",
			file:  "B3A52A7A958BF0AED0EBFBA2E9A818B7",
			hq:    "1B56126A8A03924F1DD066259C095CBC",
			sq:    "0A69169202DE95AAF24A9944CCF0730D",
			res:   "D667BC5EE93F201126697CB3BA745009",
			super: "",
			want:  []string{"flac24bit", "flac", "320k", "128k"},
		},
		{
			name: "仅 Super 非空时也应给出最高档",
			file: "a", super: "e",
			want: []string{"flac24bit", "128k"},
		},
		{
			name: "空白字符视为不可用",
			file: "  ", hq: "", sq: " ", res: "",
			want: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := kugouQualitys(c.file, c.hq, c.sq, c.res, c.super)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("kugouQualitys = %v, want %v", got, c.want)
			}
		})
	}
}

func TestKuwoQualitys(t *testing.T) {
	cases := []struct {
		name  string
		minfo string
		want  []string
	}{
		{
			name:  "实测：含 ff(flac) 与 zp(臻品)",
			minfo: "level:ff,bitrate:2000,format:flac,size:12.59Mb;level:p,bitrate:192,format:ogg,size:2.61Mb;level:h,bitrate:100,format:ogg,size:1.36Mb;level:p,bitrate:320,format:mp3,size:5.11Mb;level:h,bitrate:128,format:mp3,size:2.04Mb;level:s,bitrate:48,format:aac,size:798.94Kb;level:zp,bitrate:20000,format:zp,size:zpMb",
			want:  []string{"flac24bit", "flac", "320k", "192k", "128k"},
		},
		{
			name:  "实测：无无损，只有 320/192/128/aac48",
			minfo: "level:p,bitrate:192,format:ogg,size:11.37Mb;level:h,bitrate:100,format:ogg,size:5.4Mb;level:p,bitrate:320,format:mp3,size:18.1Mb;level:h,bitrate:128,format:mp3,size:7.24Mb;level:s,bitrate:48,format:aac,size:2.74Mb",
			want:  []string{"320k", "192k", "128k"},
		},
		{
			name:  "实测：最低档（仅 128 mp3 + 48 aac）",
			minfo: "level:h,bitrate:128,format:mp3,size:358.49Kb;level:s,bitrate:48,format:aac,size:134.77Kb",
			want:  []string{"128k"},
		},
		{
			name:  "zp 的哨兵码率不得被误判为无损以外的档位",
			minfo: "level:zp,bitrate:20000,format:zp,size:zpMb",
			want:  []string{"flac24bit"},
		},
		{
			name:  "空输入",
			minfo: "",
			want:  []string{},
		},
		{
			name:  "残缺字段不影响解析",
			minfo: "level:p,bitrate:320,format:mp3;garbage;level:h",
			want:  []string{"320k"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := kuwoQualitys(c.minfo)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("kuwoQualitys(%q) = %v, want %v", c.minfo, got, c.want)
			}
		})
	}
}

func TestKuwoQualitysFromFormats(t *testing.T) {
	cases := []struct {
		name    string
		formats string
		want    []string
	}{
		{
			name:    "实测：含 ALFLAC 与 MP3H",
			formats: "AAC48|ALFLAC|MP3128|MP3H|WMA128|WMA96|OGG192|OGG96",
			want:    []string{"flac", "320k", "192k", "128k"},
		},
		{
			name:    "实测：无无损",
			formats: "AAC48|MP3128|OGG96|WMA128|WMA96",
			want:    []string{"128k"},
		},
		{
			name:    "空输入",
			formats: "",
			want:    []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := kuwoQualitysFromFormats(c.formats)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("kuwoQualitysFromFormats(%q) = %v, want %v", c.formats, got, c.want)
			}
		})
	}
}

func TestKuwoResolveQualitys(t *testing.T) {
	// N_MINFO 优先
	got := kuwoResolveQualitys("level:p,bitrate:320,format:mp3", "", "AAC48|ALFLAC")
	want := []string{"320k"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("应优先使用 N_MINFO: got %v, want %v", got, want)
	}

	// N_MINFO 为空时回退 MINFO
	got = kuwoResolveQualitys("", "level:ff,bitrate:2000,format:flac", "")
	want = []string{"flac"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("应回退 MINFO: got %v, want %v", got, want)
	}

	// 两者都为空时回退 FORMATS
	got = kuwoResolveQualitys("", "", "AAC48|MP3128")
	want = []string{"128k"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("应回退 FORMATS: got %v, want %v", got, want)
	}
}

// 保证所有解析产出的档位都在前端 QUALITY_LADDER 内（hires 之类的非阶梯值不应再出现）。
func TestAllProducedQualitiesAreOnLadder(t *testing.T) {
	ladder := map[string]bool{
		QualityFlac24: true, QualityFlac: true,
		Quality320k: true, Quality192k: true, Quality128k: true,
	}
	produced := [][]string{
		neteaseQualitys(&neteaseLevelInfo{Br: 1}, &neteaseLevelInfo{Br: 1}, &neteaseLevelInfo{Br: 1}, &neteaseLevelInfo{Br: 1}, &neteaseLevelInfo{Br: 1}),
		qqQualitys(1, 1, 1, 1, 1),
		kugouQualitys("a", "b", "c", "d", "e"),
		kuwoQualitys("level:ff,bitrate:2000,format:flac;level:zp,bitrate:20000,format:zp;level:p,bitrate:320,format:mp3;level:p,bitrate:192,format:ogg;level:h,bitrate:128,format:mp3"),
		kuwoQualitysFromFormats("ALFLAC|MP3H|MP3128|OGG192"),
	}
	for i, qs := range produced {
		for _, q := range qs {
			if !ladder[q] {
				t.Errorf("第 %d 组产出非阶梯档位 %q —— 会导致前端排序错乱", i, q)
			}
		}
	}
}
