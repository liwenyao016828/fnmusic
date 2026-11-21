// Package audiok 做**容器结构级**的无损文件校验。
//
// # 为什么需要它
//
// pkg/flacok 能逐帧验 CRC，所以 FLAC 已经是「坏流绝不入库」的。这��还没有
// 对应能力的容器有 WAV（PCM）、M4A（ALAC/AAC）、WavPack、APE —— 它们现在只
// 靠「魔数对得上 + 大小对得上」判断，于是有两种坏文件会进库：
//
//  1. 截断流：服务器说 200、Content-Length 却是错的（或者干脆没有），
//     文件头正常、正文少一半 —— 表现为「下载成功，但播到一半就没声了」。
//  2. 容器自相矛盾：WAV 的 data 块长度大于文件、M4A 的 atom 链断在半路、
//     WavPack 的块长度加起来对不上文件大小 —— 这类文件任何播放器都会报错。
//
// 这里做的是**结构级**校验：把容器的块/atom 链走一遍，看长度能不能自洽。
// 它不解码音频内容（那需要解码器，而本项目刻意零第三方依赖），但能挡住
// 「明显坏掉」的文件 —— 这正是上游做全量解码校验时能挡住的绝大多数事故。
//
// 校验失败的文件由调用方（pkg/downloader）删掉并报「可降到 320K 重试」，
// 上层的降档重试链（pkg/backenddl）已经就位。
package audiok

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Validate 按扩展名挑对应的容器校验器。
//
// ext 不带点。返回 nil 表示「这个容器没问题」或「这个容器我们不校验」
// （不校验的容器返回 nil 而不是错误 —— 宁可放过，也不要因为校验器不成熟
// 把好文件判死）。
func Validate(path, ext string) error {
	switch ext {
	case "wav":
		return ValidateWAV(path)
	case "m4a", "mp4", "aac", "m4b":
		return ValidateMP4(path)
	case "wv":
		return ValidateWavPack(path)
	case "ape":
		return ValidateAPE(path)
	}
	return nil
}

// ── WAV（RIFF/PCM）──────────────────────────────────────────────────────

// ValidateWAV 检查 WAV 的块结构是否自洽。
//
// 判据：
//   - 开头是 RIFF....WAVE；
//   - 至少有一个 fmt 块和一个 data 块；
//   - 每个块的长度不许越出文件末尾（越界 = 文件被截断）；
//   - data 块声明的长度必须完整落在文件里（这是最常见的截断形态）。
func ValidateWAV(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()
	if size < 12 {
		return fmt.Errorf("WAV 文件只有 %d 字节，不可能是完整的", size)
	}

	head := make([]byte, 12)
	if _, err := io.ReadFull(f, head); err != nil {
		return fmt.Errorf("WAV 头读不出来：%w", err)
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return fmt.Errorf("不是 WAV 文件（RIFF/WAVE 头不对）")
	}
	// RIFF 声明的总长度：一般等于 文件大小-8。差太多说明文件被拼过。
	riffLen := int64(binary.LittleEndian.Uint32(head[4:8]))
	if riffLen+8 != size {
		// 有些写盘工具会多写尾巴，差值过大才判死
		if diff := riffLen + 8 - size; diff > 4096 || diff < -4096 {
			return fmt.Errorf("WAV 长度对不上：文件声明 %d 字节，实际 %d 字节", riffLen+8, size)
		}
	}

	off := int64(12)
	var sawFmt, sawData bool
	for off+8 <= size {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return err
		}
		ch := make([]byte, 8)
		if _, err := io.ReadFull(f, ch); err != nil {
			return fmt.Errorf("WAV 块头读不出来（偏移 %d）：%w", off, err)
		}
		id := string(ch[0:4])
		clen := int64(binary.LittleEndian.Uint32(ch[4:8]))
		body := off + 8
		if clen < 0 || body+clen > size {
			return fmt.Errorf("WAV 的 %s 块长度越界（声明 %d 字节，文件只剩 %d 字节）", id, clen, size-body)
		}
		switch id {
		case "fmt ":
			sawFmt = true
		case "data":
			sawData = true
		}
		off = body + clen
		if clen%2 == 1 {
			off++ // 块按偶数字节对齐
		}
	}
	if !sawFmt {
		return fmt.Errorf("WAV 里没有 fmt 块")
	}
	if !sawData {
		return fmt.Errorf("WAV 里没有 data 块")
	}
	return nil
}

// ── MP4 / M4A（ALAC、AAC 都在这个容器里）────────────────────────────────

// ValidateMP4 检查 MP4 容器（.m4a/.mp4）的 atom 链是否完整。
//
// 判据：
//   - 至少有一个 ftyp（文件类型）与一个 moov（索引/元数据）；
//   - 有 mdat（真正的音频数据）；
//   - 每个 atom 的长度都落在文件里 —— 断在半路的 atom = 文件被截断。
func ValidateMP4(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()
	if size < 16 {
		return fmt.Errorf("MP4 文件只有 %d 字节，不可能是完整的", size)
	}

	off := int64(0)
	var sawFtyp, sawMoov, sawMdat bool
	for off+8 <= size {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return err
		}
		hdr := make([]byte, 8)
		if _, err := io.ReadFull(f, hdr); err != nil {
			return fmt.Errorf("MP4 atom 头读不出来（偏移 %d）：%w", off, err)
		}
		alen := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		switch {
		case alen == 1:
			// 64 位长度：后面跟 8 字节长度
			if _, err := f.Seek(off+8, io.SeekStart); err != nil {
				return err
			}
			ext := make([]byte, 8)
			if _, err := io.ReadFull(f, ext); err != nil {
				return fmt.Errorf("MP4 的 %s atom 长度读不出来：%w", typ, err)
			}
			alen = int64(binary.BigEndian.Uint64(ext))
			off += 16
			if alen < 16 {
				return fmt.Errorf("MP4 的 %s atom 长度不对：%d", typ, alen)
			}
		case alen == 0:
			// 长度 0 = 这个 atom 一直到文件末尾
			alen = size - off
		}
		if alen < 8 || off+alen > size {
			return fmt.Errorf("MP4 的 %s atom 越界（声明 %d 字节，文件只剩 %d 字节）", typ, alen, size-off)
		}
		switch typ {
		case "ftyp":
			sawFtyp = true
		case "moov":
			sawMoov = true
		case "mdat":
			sawMdat = true
		}
		off += alen
	}
	if !sawFtyp {
		return fmt.Errorf("MP4 里没有 ftyp")
	}
	if !sawMoov {
		return fmt.Errorf("MP4 里没有 moov（索引缺失，播放器多半打不开）")
	}
	if !sawMdat {
		return fmt.Errorf("MP4 里没有 mdat（没有音频数据）")
	}
	return nil
}

// ── WavPack ─────────────────────────────────────────────────────────────

// ValidateWavPack 检查 WavPack 的块链是否自洽。
//
// WavPack 文件就是一串 'wvpk' 块，每块头里写着本块总长度；所有块长度加起来
// 应该正好等于文件大小（后面可以跟一个 ID3 标签）。对不上 = 文件被截断。
func ValidateWavPack(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()

	off := int64(0)
	blocks := 0
	for off+8 <= size {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return err
		}
		hdr := make([]byte, 8)
		if _, err := io.ReadFull(f, hdr); err != nil {
			return fmt.Errorf("WavPack 块头读不出来（偏移 %d）：%w", off, err)
		}
		if string(hdr[0:4]) != "wvpk" {
			// 后面可能是 ID3 标签之类，允许出现在末尾
			if blocks > 0 {
				break
			}
			return fmt.Errorf("不是 WavPack 文件（块标识 %q 不对）", string(hdr[0:4]))
		}
		blen := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		if blen < 24 || off+blen > size {
			return fmt.Errorf("WavPack 第 %d 块长度越界（声明 %d 字节，文件只剩 %d 字节）", blocks+1, blen, size-off)
		}
		blocks++
		off += blen
	}
	if blocks == 0 {
		return fmt.Errorf("WavPack 文件里没有块")
	}
	// 末尾允许跟一个标签（ID3 / APE），但不能差太多
	if rest := size - off; rest > 1024*1024 {
		return fmt.Errorf("WavPack 块链只覆盖了 %d 字节，文件却有 %d 字节", off, size)
	}
	return nil
}

// ── APE（Monkey's Audio）────────────────────────────────────────────────

// ValidateAPE 做 APE 的**头部级**检查。
//
// APE 的数据块链解析起来比 WavPack 麻烦（块长藏在描述符与块头位域里），
// 这里只查最要紧的几件事：头标识、版本、描述符自洽、文件里有实际数据。
// 能挡住「根本不是 APE」「头都坏了一半」这类坏文件；帧级损坏交给播放器。
func ValidateAPE(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()
	if size < 64 {
		return fmt.Errorf("APE 文件只有 %d 字节，不可能是完整的", size)
	}
	head := make([]byte, 64)
	if _, err := io.ReadFull(f, head); err != nil {
		return fmt.Errorf("APE 头读不出来：%w", err)
	}
	if string(head[0:4]) != "MAC " {
		return fmt.Errorf("不是 APE 文件（标识 %q 不对）", string(head[0:4]))
	}
	version := binary.LittleEndian.Uint16(head[4:6])
	if version < 3800 || version > 3999 {
		return fmt.Errorf("APE 版本号看着不对：%d", version)
	}
	// MAC 头里带着描述符与头部的长度（偏移 8 与 12）：这两个长度加上文件头
	// 必须落在文件内 —— 对不上就说明文件被截断或根本不是完整的 APE。
	descriptorBytes := int64(binary.LittleEndian.Uint32(head[8:12]))
	headerBytes := int64(binary.LittleEndian.Uint32(head[12:16]))
	if descriptorBytes < 46 || headerBytes < 24 {
		return fmt.Errorf("APE 描述符长度不对（描述符 %d 字节，头部 %d 字节）", descriptorBytes, headerBytes)
	}
	if need := int64(32) + descriptorBytes + headerBytes; need > size {
		return fmt.Errorf("APE 文件被截断：头部需要 %d 字节，文件只有 %d 字节", need, size)
	}
	return nil
}
