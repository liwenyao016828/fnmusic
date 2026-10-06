package tags

import "encoding/binary"

// ── 图片尺寸与位深嗅探（写 FLAC PICTURE 块需要 width/height/depth） ──

// imageDimensions 返回宽、高、位深；无法识别时返回 0,0,0
func imageDimensions(data []byte) (width, height, depth int) {
	if w, h, d, ok := pngDimensions(data); ok {
		return w, h, d
	}
	if w, h, d, ok := jpegDimensions(data); ok {
		return w, h, d
	}
	if w, h, d, ok := gifDimensions(data); ok {
		return w, h, d
	}
	return 0, 0, 0
}

func pngDimensions(data []byte) (int, int, int, bool) {
	if len(data) < 26 {
		return 0, 0, 0, false
	}
	if !(data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47) {
		return 0, 0, 0, false
	}
	if string(data[12:16]) != "IHDR" {
		return 0, 0, 0, false
	}
	w := int(binary.BigEndian.Uint32(data[16:20]))
	h := int(binary.BigEndian.Uint32(data[20:24]))
	bitDepth := int(data[24])
	colorType := data[25]

	// 单像素总位数 = 位深 × 通道数
	channels := 1
	switch colorType {
	case 2:
		channels = 3
	case 3:
		channels = 1
	case 4:
		channels = 2
	case 6:
		channels = 4
	}
	return w, h, bitDepth * channels, true
}

func jpegDimensions(data []byte) (int, int, int, bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0, 0, 0, false
	}
	i := 2
	for i+9 < len(data) {
		if data[i] != 0xFF {
			i++
			continue
		}
		marker := data[i+1]
		// 填充字节
		if marker == 0xFF {
			i++
			continue
		}
		// 无长度字段的标记
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			i += 2
			continue
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 {
			return 0, 0, 0, false
		}
		// SOF0..SOF15，排除 DHT(C4)/JPG(C8)/DAC(CC)
		isSOF := marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC
		if isSOF {
			if i+9 >= len(data) {
				return 0, 0, 0, false
			}
			precision := int(data[i+4])
			h := int(binary.BigEndian.Uint16(data[i+5 : i+7]))
			w := int(binary.BigEndian.Uint16(data[i+7 : i+9]))
			components := int(data[i+9])
			if components <= 0 {
				components = 3
			}
			return w, h, precision * components, true
		}
		i += 2 + segLen
	}
	return 0, 0, 0, false
}

func gifDimensions(data []byte) (int, int, int, bool) {
	if len(data) < 13 {
		return 0, 0, 0, false
	}
	if string(data[0:3]) != "GIF" {
		return 0, 0, 0, false
	}
	w := int(binary.LittleEndian.Uint16(data[6:8]))
	h := int(binary.LittleEndian.Uint16(data[8:10]))
	// 逻辑屏幕描述符的 bit7 表示全局色表存在，低 3 位为色表位数
	packed := data[10]
	depth := 8
	if packed&0x80 != 0 {
		depth = 3 * (1 << ((packed & 0x07) + 1))
	}
	return w, h, depth, true
}
