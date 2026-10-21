package flacok

import "testing"

// 真实编码器产出的文件必须判为「完好」。
//
// 这条用例是拿命换来的：以前的走帧解析有四处与规范不一致（LPC 精度/移位位宽、
// Rice 一元码的方向、FIXED 子帧漏读预热采样、浪费位数的一元码方向），结果
// **任何真实 FLAC 都被判成坏流**。而下游 `pkg/downloader` 见到 flac_corrupt 会
// 自动降档重下 —— 用户看到的表面现象是「无损下载总是变成 mp3」。
//
// fixtures 由 ffmpeg 生成（`-f lavfi -i sine`，0.6 秒，单声道）：
//   - fixed.flac：compression_level 0，只含 FIXED 子帧
//   - lpc16.flac：compression_level 8，含 LPC 子帧（16 bit）
//   - lpc24.flac：compression_level 8，24 bit（覆盖 24 位样本的位宽处理）
//   - wasted24.flac：24 bit 且低位恒 0（覆盖 wasted-bits 一元码分支）
func TestRealEncoderOutputIsValid(t *testing.T) {
	for _, name := range []string{
		"testdata/fixed.flac",
		"testdata/lpc16.flac",
		"testdata/lpc24.flac",
		"testdata/wasted24.flac",
	} {
		if err := ValidateFile(name); err != nil {
			t.Errorf("%s 被判成坏文件：%v", name, err)
		}
	}
}
