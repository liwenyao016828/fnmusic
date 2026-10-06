# -*- coding: utf-8 -*-
"""
converter.py — 音乐格式转换（纯 Python 标准库实现，无任何第三方依赖）

支持：
  1. 网易云音乐 .ncm          → 解密为 mp3 / flac（AES-128-ECB + RC4 keybox）
  2. QQ 音乐 QMC 系列         → 解密为通用格式
       .qmc0/.qmc3/.qmcflac/.qmcogg/.qmc2/...  （QMCv1 静态 / 部分 v2 文件）
       .mflac/.mflac0/.mgg/.mgg1/...           （QMCv2 映射 / RC4）
     QMCv2 需要文件尾部写入 key（QTag / 尾部 key），无 key 文件（STag）无法解锁。

算法来源：unlock-music（MIT License，官方代码托管 git.unlock-music.dev/um/web）
  - NCM: src/decrypt/ncm.ts
  - QMC: src/decrypt/qmc.ts / qmc_key.ts / qmc_cipher.ts 及 src/QmcWasm/qmc_cipher.hpp
  - TEA: src/utils/tea.ts（golang.org/x/crypto/tea 移植）

实现仅含标准库；AES-128 为内置实现（FIPS-197 向量自检）。
"""

import base64
import json
import math
import os
import struct

# ============================================================================
# 1. AES-128-ECB（纯 Python，仅解密）
# ============================================================================

_INV_SBOX = [
    0x52, 0x09, 0x6a, 0xd5, 0x30, 0x36, 0xa5, 0x38, 0xbf, 0x40, 0xa3, 0x9e, 0x81, 0xf3, 0xd7, 0xfb,
    0x7c, 0xe3, 0x39, 0x82, 0x9b, 0x2f, 0xff, 0x87, 0x34, 0x8e, 0x43, 0x44, 0xc4, 0xde, 0xe9, 0xcb,
    0x54, 0x7b, 0x94, 0x32, 0xa6, 0xc2, 0x23, 0x3d, 0xee, 0x4c, 0x95, 0x0b, 0x42, 0xfa, 0xc3, 0x4e,
    0x08, 0x2e, 0xa1, 0x66, 0x28, 0xd9, 0x24, 0xb2, 0x76, 0x5b, 0xa2, 0x49, 0x6d, 0x8b, 0xd1, 0x25,
    0x72, 0xf8, 0xf6, 0x64, 0x86, 0x68, 0x98, 0x16, 0xd4, 0xa4, 0x5c, 0xcc, 0x5d, 0x65, 0xb6, 0x92,
    0x6c, 0x70, 0x48, 0x50, 0xfd, 0xed, 0xb9, 0xda, 0x5e, 0x15, 0x46, 0x57, 0xa7, 0x8d, 0x9d, 0x84,
    0x90, 0xd8, 0xab, 0x00, 0x8c, 0xbc, 0xd3, 0x0a, 0xf7, 0xe4, 0x58, 0x05, 0xb8, 0xb3, 0x45, 0x06,
    0xd0, 0x2c, 0x1e, 0x8f, 0xca, 0x3f, 0x0f, 0x02, 0xc1, 0xaf, 0xbd, 0x03, 0x01, 0x13, 0x8a, 0x6b,
    0x3a, 0x91, 0x11, 0x41, 0x4f, 0x67, 0xdc, 0xea, 0x97, 0xf2, 0xcf, 0xce, 0xf0, 0xb4, 0xe6, 0x73,
    0x96, 0xac, 0x74, 0x22, 0xe7, 0xad, 0x35, 0x85, 0xe2, 0xf9, 0x37, 0xe8, 0x1c, 0x75, 0xdf, 0x6e,
    0x47, 0xf1, 0x1a, 0x71, 0x1d, 0x29, 0xc5, 0x89, 0x6f, 0xb7, 0x62, 0x0e, 0xaa, 0x18, 0xbe, 0x1b,
    0xfc, 0x56, 0x3e, 0x4b, 0xc6, 0xd2, 0x79, 0x20, 0x9a, 0xdb, 0xc0, 0xfe, 0x78, 0xcd, 0x5a, 0xf4,
    0x1f, 0xdd, 0xa8, 0x33, 0x88, 0x07, 0xc7, 0x31, 0xb1, 0x12, 0x10, 0x59, 0x27, 0x80, 0xec, 0x5f,
    0x60, 0x51, 0x7f, 0xa9, 0x19, 0xb5, 0x4a, 0x0d, 0x2d, 0xe5, 0x7a, 0x9f, 0x93, 0xc9, 0x9c, 0xef,
    0xa0, 0xe0, 0x3b, 0x4d, 0xae, 0x2a, 0xf5, 0xb0, 0xc8, 0xeb, 0xbb, 0x3c, 0x83, 0x53, 0x99, 0x61,
    0x17, 0x2b, 0x04, 0x7e, 0xba, 0x77, 0xd6, 0x26, 0xe1, 0x69, 0x14, 0x63, 0x55, 0x21, 0x0c, 0x7d,
]

_RCON = [0x00, 0x01, 0x02, 0x04, 0x08, 0x10, 0x20, 0x40, 0x80, 0x1b, 0x36]

# AES 正向 S 盒（密钥扩展 SubWord 使用）
_SBOX = [
    0x63, 0x7c, 0x77, 0x7b, 0xf2, 0x6b, 0x6f, 0xc5, 0x30, 0x01, 0x67, 0x2b, 0xfe, 0xd7, 0xab, 0x76,
    0xca, 0x82, 0xc9, 0x7d, 0xfa, 0x59, 0x47, 0xf0, 0xad, 0xd4, 0xa2, 0xaf, 0x9c, 0xa4, 0x72, 0xc0,
    0xb7, 0xfd, 0x93, 0x26, 0x36, 0x3f, 0xf7, 0xcc, 0x34, 0xa5, 0xe5, 0xf1, 0x71, 0xd8, 0x31, 0x15,
    0x04, 0xc7, 0x23, 0xc3, 0x18, 0x96, 0x05, 0x9a, 0x07, 0x12, 0x80, 0xe2, 0xeb, 0x27, 0xb2, 0x75,
    0x09, 0x83, 0x2c, 0x1a, 0x1b, 0x6e, 0x5a, 0xa0, 0x52, 0x3b, 0xd6, 0xb3, 0x29, 0xe3, 0x2f, 0x84,
    0x53, 0xd1, 0x00, 0xed, 0x20, 0xfc, 0xb1, 0x5b, 0x6a, 0xcb, 0xbe, 0x39, 0x4a, 0x4c, 0x58, 0xcf,
    0xd0, 0xef, 0xaa, 0xfb, 0x43, 0x4d, 0x33, 0x85, 0x45, 0xf9, 0x02, 0x7f, 0x50, 0x3c, 0x9f, 0xa8,
    0x51, 0xa3, 0x40, 0x8f, 0x92, 0x9d, 0x38, 0xf5, 0xbc, 0xb6, 0xda, 0x21, 0x10, 0xff, 0xf3, 0xd2,
    0xcd, 0x0c, 0x13, 0xec, 0x5f, 0x97, 0x44, 0x17, 0xc4, 0xa7, 0x7e, 0x3d, 0x64, 0x5d, 0x19, 0x73,
    0x60, 0x81, 0x4f, 0xdc, 0x22, 0x2a, 0x90, 0x88, 0x46, 0xee, 0xb8, 0x14, 0xde, 0x5e, 0x0b, 0xdb,
    0xe0, 0x32, 0x3a, 0x0a, 0x49, 0x06, 0x24, 0x5c, 0xc2, 0xd3, 0xac, 0x62, 0x91, 0x95, 0xe4, 0x79,
    0xe7, 0xc8, 0x37, 0x6d, 0x8d, 0xd5, 0x4e, 0xa9, 0x6c, 0x56, 0xf4, 0xea, 0x65, 0x7a, 0xae, 0x08,
    0xba, 0x78, 0x25, 0x2e, 0x1c, 0xa6, 0xb4, 0xc6, 0xe8, 0xdd, 0x74, 0x1f, 0x4b, 0xbd, 0x8b, 0x8a,
    0x70, 0x3e, 0xb5, 0x66, 0x48, 0x03, 0xf6, 0x0e, 0x61, 0x35, 0x57, 0xb9, 0x86, 0xc1, 0x1d, 0x9e,
    0xe1, 0xf8, 0x98, 0x11, 0x69, 0xd9, 0x8e, 0x94, 0x9b, 0x1e, 0x87, 0xe9, 0xce, 0x55, 0x28, 0xdf,
    0x8c, 0xa1, 0x89, 0x0d, 0xbf, 0xe6, 0x42, 0x68, 0x41, 0x99, 0x2d, 0x0f, 0xb0, 0x54, 0xbb, 0x16,
]

# GF(2^8) 乘法表（用于逆列混合），直接用小表计算
def _gmul(a, b):
    """GF(2^8) 乘法，a 固定为 {0e,0b,0d,09} 之一。"""
    p = 0
    for _ in range(8):
        if b & 1:
            p ^= a
        hi = a & 0x80
        a = (a << 1) & 0xff
        if hi:
            a ^= 0x1b
        b >>= 1
    return p & 0xff


def _inv_mix_column(col):
    a0, a1, a2, a3 = col
    return [
        _gmul(0x0e, a0) ^ _gmul(0x0b, a1) ^ _gmul(0x0d, a2) ^ _gmul(0x09, a3),
        _gmul(0x09, a0) ^ _gmul(0x0e, a1) ^ _gmul(0x0b, a2) ^ _gmul(0x0d, a3),
        _gmul(0x0d, a0) ^ _gmul(0x09, a1) ^ _gmul(0x0e, a2) ^ _gmul(0x0b, a3),
        _gmul(0x0b, a0) ^ _gmul(0x0d, a1) ^ _gmul(0x09, a2) ^ _gmul(0x0e, a3),
    ]


def _aes_key_expansion(key):
    """AES-128 密钥扩展，返回 11 个轮密钥（每个 16 字节）。"""
    nk, nr = 4, 10
    w = [list(key[i * 4:(i + 1) * 4]) for i in range(nk)]
    for i in range(nk, 4 * (nr + 1)):
        temp = w[i - 1][:]
        if i % nk == 0:
            # RotWord + SubWord + Rcon（用正向 S 盒）
            temp = temp[1:] + temp[:1]
            temp = [_SBOX[b] for b in temp]
            temp[0] ^= _RCON[i // nk]
        w.append([w[i - nk][j] ^ temp[j] for j in range(4)])
    round_keys = []
    for r in range(nr + 1):
        rk = []
        for j in range(4):
            rk.extend(w[r * 4 + j])
        round_keys.append(rk)
    return round_keys


def _aes_inv_shift_rows(state):
    # 逆移位：row1 右移1、row2 右移2、row3 右移3（列主序 state）
    return [
        state[0], state[13], state[10], state[7],
        state[4], state[1], state[14], state[11],
        state[8], state[5], state[2], state[15],
        state[12], state[9], state[6], state[3],
    ]


def _aes_decrypt_block(block, round_keys):
    """解密单个 16 字节块（AES-128）。block: bytes/bytearray 16。"""
    state = list(block)
    # 初始轮密钥加
    for i in range(16):
        state[i] ^= round_keys[10][i]
    for rnd in range(9, 0, -1):
        # InvShiftRows + InvSubBytes
        state = [_INV_SBOX[b] for b in _aes_inv_shift_rows(state)]
        # AddRoundKey
        rk = round_keys[rnd]
        state = [state[i] ^ rk[i] for i in range(16)]
        # InvMixColumns
        new_state = []
        for c in range(4):
            col = state[c * 4:(c + 1) * 4]
            new_state.extend(_inv_mix_column(col))
        state = new_state
    # 最后一轮：InvShiftRows + InvSubBytes + AddRoundKey
    state = [_INV_SBOX[b] for b in _aes_inv_shift_rows(state)]
    rk = round_keys[0]
    state = [state[i] ^ rk[i] for i in range(16)]
    return bytes(state)


def aes_128_ecb_decrypt(key: bytes, data: bytes) -> bytes:
    """AES-128-ECB 解密任意长度数据（按 16 字节块）。"""
    if len(key) != 16:
        raise ValueError("AES-128 key 必须为 16 字节")
    if len(data) % 16 != 0:
        raise ValueError("AES 数据长度必须是 16 的倍数")
    round_keys = _aes_key_expansion(key)
    out = bytearray()
    for off in range(0, len(data), 16):
        out += _aes_decrypt_block(data[off:off + 16], round_keys)
    return bytes(out)


def pkcs7_unpad(data: bytes) -> bytes:
    """PKCS#7 去填充。"""
    if not data:
        raise ValueError("空数据无法 unpad")
    pad = data[-1]
    if pad < 1 or pad > 16 or pad > len(data):
        raise ValueError("非法 PKCS7 填充")
    if data[-pad:] != bytes([pad]) * pad:
        raise ValueError("PKCS7 填充校验失败")
    return data[:-pad]


def _xor_bytes(a: bytes, b: bytes) -> bytes:
    """大整数快速异或（a、b 等长）。"""
    if not a:
        return b""
    return (int.from_bytes(a, "big") ^ int.from_bytes(b, "big")).to_bytes(len(a), "big")


# ============================================================================
# 2. NCM 解密
# ============================================================================

_NCM_CORE_KEY = bytes.fromhex("687a4852416d736f356b496e62617857")  # "hzHRAmso5kInbaxW"
_NCM_META_KEY = bytes.fromhex("2331346c6a6b5f215c5d2630553c2728")  # "#14ljk_!\]&0U<('"
_NCM_MAGIC = b"CTENFDAM"


def _ncm_key_box(key_data: bytes) -> bytes:
    """根据解密后的 key 数据生成 256 字节 keybox（unlock-music ncm.ts _getKeyBox）。"""
    box = list(range(256))
    j = 0
    klen = len(key_data)
    for i in range(256):
        j = (box[i] + j + key_data[i % klen]) & 0xff
        box[i], box[j] = box[j], box[i]
    keybox = bytearray(256)
    for i in range(256):
        ii = (i + 1) & 0xff
        si = box[ii]
        sj = box[(ii + si) & 0xff]
        keybox[i] = box[(si + sj) & 0xff]
    return bytes(keybox)


def decrypt_ncm_file(path: str):
    """解密一个 .ncm 文件。

    返回 dict:
      data      : 解密后的音频字节
      fmt       : 输出扩展名（mp3 / flac / 其他）
      meta      : 元数据 dict（musicName/artist/album/...，可能为空）
      image     : 封面图片字节或 None
      music_name: 推荐输出文件名（元数据里取，可空）
    """
    with open(path, "rb") as f:
        header = f.read(8)
        if header != _NCM_MAGIC:
            raise ValueError("无效的 NCM 文件（magic 校验失败）")
        f.seek(2, 1)  # 跳过 2 字节
        key_len = struct.unpack("<I", f.read(4))[0]
        cipher_key = f.read(key_len)
        if len(cipher_key) != key_len:
            raise ValueError("NCM 文件截断（key 数据不足）")
        cipher_key = bytes(b ^ 0x64 for b in cipher_key)
        plain_key = pkcs7_unpad(aes_128_ecb_decrypt(_NCM_CORE_KEY, cipher_key))
        key_data = plain_key[17:]
        if not key_data:
            raise ValueError("NCM key 解析失败")
        keybox = _ncm_key_box(key_data)

        # 元数据
        meta_len = struct.unpack("<I", f.read(4))[0]
        meta = {}
        if meta_len > 0:
            cipher_meta = bytes(b ^ 0x63 for b in f.read(meta_len))
            try:
                b64 = base64.b64decode(cipher_meta[22:])
                plain_meta = pkcs7_unpad(aes_128_ecb_decrypt(_NCM_META_KEY, b64)).decode("utf-8")
                idx = plain_meta.index(":")
                label, body = plain_meta[:idx], plain_meta[idx + 1:]
                meta = json.loads(body)
                if label == "dj" and isinstance(meta, dict):
                    meta = meta.get("mainMusic", meta)
            except Exception:
                meta = {}

        # 图片
        f.seek(5, 1)  # 跳过 CRC 区
        image_space = struct.unpack("<I", f.read(4))[0]
        image_size = struct.unpack("<I", f.read(4))[0]
        image = f.read(image_size) if image_size else None
        if image_space > image_size:
            f.seek(image_space - image_size, 1)

        # 音频数据
        audio = f.read()

    # 解密音频（keybox 按 256 周期循环）
    rep = (keybox * ((len(audio) + 255) // 256))[:len(audio)]
    out = _xor_bytes(audio, rep)

    fmt = (meta.get("format") or "").lower() if isinstance(meta, dict) else ""
    if fmt not in ("mp3", "flac", "ogg", "m4a", "wav", "ape"):
        fmt = sniff_audio_fmt(out) or ("flac" if len(out) > 1024 * 1024 * 16 else "mp3")
    music_name = ""
    if isinstance(meta, dict) and meta.get("musicName"):
        artist = ""
        try:
            al = meta.get("artist") or []
            if al:
                artist = "/".join(a[0] for a in al if isinstance(a, (list, tuple)) and a)
        except Exception:
            artist = ""
        music_name = f"{artist} - {meta['musicName']}" if artist else meta["musicName"]
    return {"data": out, "fmt": fmt, "meta": meta, "image": image, "music_name": music_name}


def _ncm_lyric(meta):
    """从 NCM 元数据提取歌词文本（meta.lyric 为 base64 编码的 LRC）。"""
    if not isinstance(meta, dict):
        return ""
    raw = meta.get("lyric") or ""
    if not raw:
        return ""
    try:
        text = base64.b64decode(raw).decode("utf-8", "replace")
    except Exception:
        return ""
    return text.strip()


# ============================================================================
# 3. QMC 解密（unlock-music 移植）
# ============================================================================

# QmcStaticCipher 静态表（src/QmcWasm/qmc_cipher.hpp）
_QMC_STATIC_BOX = [
    0x77, 0x48, 0x32, 0x73, 0xDE, 0xF2, 0xC0, 0xC8, 0x95, 0xEC, 0x30, 0xB2, 0x51, 0xC3, 0xE1, 0xA0,
    0x9E, 0xE6, 0x9D, 0xCF, 0xFA, 0x7F, 0x14, 0xD1, 0xCE, 0xB8, 0xDC, 0xC3, 0x4A, 0x67, 0x93, 0xD6,
    0x28, 0xC2, 0x91, 0x70, 0xCA, 0x8D, 0xA2, 0xA4, 0xF0, 0x08, 0x61, 0x90, 0x7E, 0x6F, 0xA2, 0xE0,
    0xEB, 0xAE, 0x3E, 0xB6, 0x67, 0xC7, 0x92, 0xF4, 0x91, 0xB5, 0xF6, 0x6C, 0x5E, 0x84, 0x40, 0xF7,
    0xF3, 0x1B, 0x02, 0x7F, 0xD5, 0xAB, 0x41, 0x89, 0x28, 0xF4, 0x25, 0xCC, 0x52, 0x11, 0xAD, 0x43,
    0x68, 0xA6, 0x41, 0x8B, 0x84, 0xB5, 0xFF, 0x2C, 0x92, 0x4A, 0x26, 0xD8, 0x47, 0x6A, 0x7C, 0x95,
    0x61, 0xCC, 0xE6, 0xCB, 0xBB, 0x3F, 0x47, 0x58, 0x89, 0x75, 0xC3, 0x75, 0xA1, 0xD9, 0xAF, 0xCC,
    0x08, 0x73, 0x17, 0xDC, 0xAA, 0x9A, 0xA2, 0x16, 0x41, 0xD8, 0xA2, 0x06, 0xC6, 0x8B, 0xFC, 0x66,
    0x34, 0x9F, 0xCF, 0x18, 0x23, 0xA0, 0x0A, 0x74, 0xE7, 0x2B, 0x27, 0x70, 0x92, 0xE9, 0xAF, 0x37,
    0xE6, 0x8C, 0xA7, 0xBC, 0x62, 0x65, 0x9C, 0xC2, 0x08, 0xC9, 0x88, 0xB3, 0xF3, 0x43, 0xAC, 0x74,
    0x2C, 0x0F, 0xD4, 0xAF, 0xA1, 0xC3, 0x01, 0x64, 0x95, 0x4E, 0x48, 0x9F, 0xF4, 0x35, 0x78, 0x95,
    0x7A, 0x39, 0xD6, 0x6A, 0xA0, 0x6D, 0x40, 0xE8, 0x4F, 0xA8, 0xEF, 0x11, 0x1D, 0xF3, 0x1B, 0x3F,
    0x3F, 0x07, 0xDD, 0x6F, 0x5B, 0x19, 0x30, 0x19, 0xFB, 0xEF, 0x0E, 0x37, 0xF0, 0x0E, 0xCD, 0x16,
    0x49, 0xFE, 0x53, 0x47, 0x13, 0x1A, 0xBD, 0xA4, 0xF1, 0x40, 0x19, 0x60, 0x0E, 0xED, 0x68, 0x09,
    0x06, 0x5F, 0x4D, 0xCF, 0x3D, 0x1A, 0xFE, 0x20, 0x77, 0xE4, 0xD9, 0xDA, 0xF9, 0xA4, 0x2B, 0x76,
    0x1C, 0x71, 0xDB, 0x00, 0xBC, 0xFD, 0x0C, 0x6C, 0xA5, 0x47, 0xF7, 0xF6, 0x00, 0x79, 0x4A, 0x11,
]
_QMC_MASK_MOD = 0x7fff

# 预计算表：pre[i] = box[(i*i+27)&0xff]，i 覆盖 0..0x7fff（32768 项）
_QMC_STATIC_PRE = bytes(_QMC_STATIC_BOX[(i * i + 27) & 0xff] for i in range(_QMC_MASK_MOD + 1))


def _apply_mask_stream(buf: bytes, pre: bytes, offset: int = 0) -> bytes:
    """按 unlock-music getMask 语义对 buf 应用掩码流。

    pre[i] = mask(i) 覆盖 i in 0..0x7fff（32768 项）。
    mask 序列（offset 递增）：0..0x7fff 用 pre[offset]；offset>0x7fff 用 pre[offset % 0x7fff]。
    """
    n = len(buf)
    if n == 0:
        return b""
    if offset != 0:
        out = bytearray(n)
        for i in range(n):
            o = offset + i
            out[i] = buf[i] ^ (pre[o % _QMC_MASK_MOD] if o > _QMC_MASK_MOD else pre[o])
        return bytes(out)
    if n <= _QMC_MASK_MOD + 1:
        return _xor_bytes(buf, pre[:n])
    first = _xor_bytes(buf[:0x8000], pre)
    # 之后周期 = [pre[1..32766], pre[0]]，长度 32767
    cycle = pre[1:_QMC_MASK_MOD] + pre[0:1]
    rest_len = n - 0x8000
    rep = cycle * ((rest_len + len(cycle) - 1) // len(cycle))
    return first + _xor_bytes(buf[0x8000:], rep[:rest_len])


def _qmc_static_mask(offset):
    if offset > _QMC_MASK_MOD:
        offset %= _QMC_MASK_MOD
    return _QMC_STATIC_PRE[offset]


class _QmcStaticCipher:
    def proc(self, buf, offset=0):
        return _apply_mask_stream(buf, _QMC_STATIC_PRE, offset)


class _QmcMapCipher:
    def __init__(self, key: bytes):
        self.key = key
        # 预计算 0..0x7fff 的 mask 表
        self._pre = bytes(self._get_mask(i) for i in range(_QMC_MASK_MOD + 1))

    @staticmethod
    def _rotate(value, bits):
        r = (bits + 4) % 8
        return ((value << r) | (value >> r)) & 0xff

    def _get_mask(self, offset):
        if offset > _QMC_MASK_MOD:
            offset %= _QMC_MASK_MOD
        idx = (offset * offset + 71214) % len(self.key)
        return self._rotate(self.key[idx], idx & 0x7)

    def proc(self, buf, offset=0):
        return _apply_mask_stream(buf, self._pre, offset)


class _QmcRC4Cipher:
    _FIRST_SEGMENT_SIZE = 0x80
    _SEGMENT_SIZE = 5120

    def __init__(self, key: bytes):
        self.key = key
        n = len(key)
        self.S = bytearray([i & 0xff for i in range(n)])
        j = 0
        for i in range(n):
            j = (self.S[i] + j + key[i % n]) % n
            self.S[i], self.S[j] = self.S[j], self.S[i]
        # hash（JS: (hash*value)>>>0 即 uint32 截断；C++: uint32_t 溢出 wrap）
        h = 1
        for v in key:
            if v == 0:
                continue
            nh = (h * v) & 0xffffffff
            if nh == 0 or nh <= h:
                break
            h = nh
        self.hash = h

    def _get_segment_key(self, sid):
        seed = self.key[sid % len(self.key)]
        if seed == 0:
            seed = 1
        # JS: const idx = ((double)hash / ((id + 1) * seed)) * 100.0; return idx % key.size()
        idx = (self.hash / ((sid + 1) * seed)) * 100.0
        r = idx % len(self.key)
        if math.isnan(r):
            return 0  # JS: NaN 用作数组索引 → 0
        return int(r)

    def _proc_first_segment(self, buf, offset):
        for i in range(len(buf)):
            buf[i] ^= self.key[self._get_segment_key(offset + i)]

    def _proc_a_segment(self, buf, offset):
        nS = bytearray(self.S)
        skip_len = (offset % self._SEGMENT_SIZE) + self._get_segment_key(offset // self._SEGMENT_SIZE)
        ks = len(self.key)
        j = 0
        k = 0
        i = -skip_len
        while i < len(buf):
            j = (j + 1) % ks
            k = (nS[j] + k) % ks
            nS[k], nS[j] = nS[j], nS[k]
            if i >= 0:
                buf[i] ^= nS[(nS[j] + nS[k]) % ks]
            i += 1

    def proc(self, buf, offset=0):
        to_process = len(buf)
        processed = 0
        # 首段
        if offset < self._FIRST_SEGMENT_SIZE:
            seg = bytearray(buf[processed:processed + min(self._FIRST_SEGMENT_SIZE - offset, to_process)])
            self._proc_first_segment(seg, offset)
            buf[processed:processed + len(seg)] = seg
            processed += len(seg)
            offset += len(seg)
            to_process -= len(seg)
            if to_process == 0:
                return
        # 对齐段
        if offset % self._SEGMENT_SIZE != 0:
            seg = bytearray(buf[processed:processed + min(self._SEGMENT_SIZE - (offset % self._SEGMENT_SIZE), to_process)])
            self._proc_a_segment(seg, offset)
            buf[processed:processed + len(seg)] = seg
            processed += len(seg)
            offset += len(seg)
            to_process -= len(seg)
            if to_process == 0:
                return
        # 整段
        while to_process > self._SEGMENT_SIZE:
            seg = bytearray(buf[processed:processed + self._SEGMENT_SIZE])
            self._proc_a_segment(seg, offset)
            buf[processed:processed + self._SEGMENT_SIZE] = seg
            processed += self._SEGMENT_SIZE
            offset += self._SEGMENT_SIZE
            to_process -= self._SEGMENT_SIZE
        # 尾部
        if to_process > 0:
            seg = bytearray(buf[processed:processed + to_process])
            self._proc_a_segment(seg, offset)
            buf[processed:processed + to_process] = seg


# ---- Tencent Tea（标准 TEA，64 轮 / 32 双轮）----
_TEA_DELTA = 0x9E3779B9


def _tea_decrypt_block(block: bytes, key: bytes, rounds: int = 64) -> bytes:
    """TEA 解密单个 8 字节块，key 16 字节。rounds 为总轮数（默认标准 64；unlock-music QMC 用 32）。"""
    k0, k1, k2, k3 = struct.unpack(">IIII", key)
    v0, v1 = struct.unpack(">II", block)
    s = (_TEA_DELTA * (rounds // 2)) & 0xffffffff
    for _ in range(rounds // 2):
        t = (((v0 << 4) & 0xffffffff) + k2) & 0xffffffff
        u = ((v0 >> 5) & 0xffffffff) + k3
        v1 = (v1 - (t ^ ((v0 + s) & 0xffffffff) ^ (u & 0xffffffff))) & 0xffffffff
        t = (((v1 << 4) & 0xffffffff) + k0) & 0xffffffff
        u = ((v1 >> 5) & 0xffffffff) + k1
        v0 = (v0 - (t ^ ((v1 + s) & 0xffffffff) ^ (u & 0xffffffff))) & 0xffffffff
        s = (s - _TEA_DELTA) & 0xffffffff
    return struct.pack(">II", v0, v1)


_TEA_SALT_LEN = 2
_TEA_ZERO_LEN = 7


def _tea_decrypt_cbc(in_buf: bytes, key: bytes) -> bytes:
    """Tencent Tea CBC 变体解密（unlock-music qmc_key.ts decryptTencentTea，rounds=32）。"""
    if len(in_buf) % 8 != 0:
        raise ValueError("inBuf size not a multiple of the block size")
    if len(in_buf) < 16:
        raise ValueError("inBuf size too small")
    # 解密第一块得到 pad 信息
    tmp = bytearray(_tea_decrypt_block(in_buf[0:8], key, rounds=32))
    n_pad = tmp[0] & 0x7
    out_len = len(in_buf) - 1 - n_pad - _TEA_SALT_LEN - _TEA_ZERO_LEN
    if out_len < 0:
        raise ValueError("invalid tea out length")
    out = bytearray(out_len)
    iv_prev = bytearray(8)
    iv_cur = bytearray(in_buf[0:8])
    pos = 8
    tmp_idx = 1 + n_pad

    def crypt_block():
        nonlocal iv_prev, iv_cur, pos, tmp_idx
        iv_prev = iv_cur
        iv_cur = bytearray(in_buf[pos:pos + 8])
        for j in range(8):
            tmp[j] ^= iv_cur[j]
        dec = _tea_decrypt_block(bytes(tmp), key, rounds=32)
        tmp[:] = dec
        pos += 8
        tmp_idx = 0

    # 跳过 Salt（2 字节）
    i = 1
    while i <= _TEA_SALT_LEN:
        if tmp_idx < 8:
            tmp_idx += 1
            i += 1
        else:
            crypt_block()
    # 还原明文
    out_pos = 0
    while out_pos < out_len:
        if tmp_idx < 8:
            out[out_pos] = tmp[tmp_idx] ^ iv_prev[tmp_idx]
            out_pos += 1
            tmp_idx += 1
        else:
            crypt_block()
    # 校验 Zero
    for i in range(1, _TEA_ZERO_LEN + 1):
        if tmp[tmp_idx] != iv_prev[tmp_idx]:
            raise ValueError("zero check failed")
    return bytes(out)


_QMC_MIX_KEY1 = bytes([0x33, 0x38, 0x36, 0x5A, 0x4A, 0x59, 0x21, 0x40, 0x23, 0x2A, 0x24, 0x25, 0x5E, 0x26, 0x29, 0x28])
_QMC_MIX_KEY2 = bytes([0x2A, 0x2A, 0x23, 0x21, 0x28, 0x23, 0x24, 0x25, 0x26, 0x5E, 0x61, 0x31, 0x63, 0x5A, 0x2C, 0x54])
_QMC_V2_PREFIX = b"QQMusic EncV2,Key:"
_B64_BYTES = frozenset(b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=")


def _qmc_decrypt_v2_key(raw: bytes) -> bytes:
    if len(raw) < 18 or raw[:18] != _QMC_V2_PREFIX:
        return raw
    out = _tea_decrypt_cbc(raw[18:], _QMC_MIX_KEY1)
    out = _tea_decrypt_cbc(out, _QMC_MIX_KEY2)
    key_dec = base64.b64decode(out)
    if len(key_dec) < 16:
        raise ValueError("EncV2 key decode failed")
    return key_dec


def _qmc_simple_make_key(salt, length):
    return [0xff & int(math.trunc(abs(math.tan(salt + i * 0.1)) * 100.0)) for i in range(length)]


def qmc_derive_key(raw: bytes) -> bytes:
    """QmcDeriveKey（unlock-music qmc_key.ts）。raw 为 base64 文本。"""
    raw_dec = base64.b64decode(raw)
    if len(raw_dec) < 16:
        raise ValueError("key length is too short")
    raw_dec = bytearray(_qmc_decrypt_v2_key(bytes(raw_dec)))
    simple_key = _qmc_simple_make_key(106, 8)
    tea_key = bytearray(16)
    for i in range(8):
        tea_key[i << 1] = simple_key[i]
        tea_key[(i << 1) + 1] = raw_dec[i]
    sub = _tea_decrypt_cbc(bytes(raw_dec[8:]), bytes(tea_key))
    result = bytes(raw_dec[:8]) + sub
    return result


class QmcDecoder:
    """unlock-music qmc.ts QmcDecoder 移植。"""

    def __init__(self, data: bytes):
        self.file = data
        self.size = len(data)
        self.audio_size = 0
        self.cipher = None
        self.song_id = None
        self.key_embedded = False   # 文件尾部是否真的写入了密钥
        self._search_key()

    def _search_key(self):
        if self.size < 16:
            raise ValueError("文件过小，不是有效的 QQ 音乐加密文件")
        last4 = self.file[-4:]
        if last4 == b"STag":
            raise ValueError("该文件未内嵌密钥（STag 格式），无法解密；需从 QQ 音乐客户端导出密钥")
        if last4 == b"QTag":
            key_size = struct.unpack(">I", self.file[-8:-4])[0]
            if key_size == 0 or key_size + 8 >= self.size:
                raise ValueError("文件尾部 QTag 结构损坏，无法定位密钥")
            self.audio_size = self.size - key_size - 8
            raw_key = self.file[self.audio_size:self.size - 8]
            comma = raw_key.find(b",")
            if comma <= 0:
                raise ValueError("文件尾部密钥格式错误（缺少分隔符）")
            self._set_cipher(raw_key[:comma])
            self.key_embedded = True
            id_buf = raw_key[comma + 1:]
            id_end = id_buf.find(b",")
            if id_end < 0:
                raise ValueError("文件尾部密钥格式错误（缺少歌曲 ID）")
            try:
                self.song_id = int(id_buf[:id_end])
            except ValueError:
                self.song_id = None
        else:
            key_size = struct.unpack("<I", last4)[0]
            tail_key = None
            if 16 <= key_size and key_size + 4 < self.size:
                # 老格式：尾部 4 字节（小端）= base64 密钥长度。
                # 用「候选区全为 base64 字符」判定，避免 QMCv1 静态表文件被误判
                # （EncV2 密钥 base64 长度可接近甚至超过 0x400，不能只按长度阈值）。
                cand = self.file[self.size - 4 - key_size:self.size - 4]
                if all(c in _B64_BYTES for c in cand):
                    tail_key = cand
            if tail_key is not None:
                self.audio_size = self.size - key_size - 4
                self._set_cipher(tail_key)
                self.key_embedded = True
            else:
                # QMCv1 静态表（qmc0/qmc3/bkcmp3 等），文件内不含密钥
                self.audio_size = self.size
                self.cipher = _QmcStaticCipher()

    def _set_cipher(self, key_raw: bytes):
        key_dec = qmc_derive_key(key_raw)
        if len(key_dec) > 300:
            self.cipher = _QmcRC4Cipher(key_dec)
        else:
            self.cipher = _QmcMapCipher(key_dec)

    def decrypt(self) -> bytes:
        if self.cipher is None:
            raise ValueError("no cipher found")
        if self.audio_size <= 0:
            raise ValueError("invalid audio size")
        audio = bytearray(self.file[:self.audio_size])
        dec = self.cipher.proc(audio, 0)
        if dec is not None:
            return dec
        return bytes(audio)


# QMC 扩展名 → 输出格式
_QMC_EXT_MAP = {
    "mgg": "ogg", "mgg0": "ogg", "mggl": "ogg", "mgg1": "ogg",
    "mflac": "flac", "mflac0": "flac", "mmp4": "m4a",
    "qmcflac": "flac", "qmcogg": "ogg", "qmc0": "mp3", "qmc2": "ogg",
    "qmc3": "mp3", "qmc4": "ogg", "qmc6": "ogg", "qmc8": "ogg",
    "bkcmp3": "mp3", "bkcm4a": "m4a", "bkcflac": "flac", "bkcwav": "wav",
    "bkcape": "ape", "bkcogg": "ogg", "bkcwma": "wma", "tkm": "m4a",
    "666c6163": "flac", "6d7033": "mp3", "6f6767": "ogg", "6d3461": "m4a",
    "776176": "wav",
}

_QMC_EXTS = set(_QMC_EXT_MAP.keys())

# QMCv2 系列：必须在文件尾部内嵌密钥，否则无法解密
_QMC_V2_EXTS = {"mflac", "mflac0", "mgg", "mgg0", "mgg1", "mggl", "mmp4"}


def sniff_audio_fmt(data: bytes):
    """嗅探音频容器魔数，返回输出扩展名；无法识别时返回 None。

    用于校验解密结果确实是可播放音频，避免把垃圾数据写成 .flac/.ogg。
    """
    if len(data) < 12:
        return None
    head = data[:4]
    if head == b"fLaC":
        return "flac"
    if head == b"OggS":
        return "ogg"
    if head == b"MAC ":
        return "ape"
    if head == b"RIFF" and data[8:12] == b"WAVE":
        return "wav"
    if data[:4] == b"\x30\x26\xb2\x75":
        return "wma"
    if data[4:8] in (b"ftyp", b"moov", b"mdat", b"free", b"skip", b"wide"):
        return "m4a"
    if data[:3] == b"ID3":
        return "mp3"
    # MP3 帧同步（11 bit）
    if data[0] == 0xFF and (data[1] & 0xE0) == 0xE0:
        return "mp3"
    return None


def decrypt_qmc_file(data: bytes, ext: str):
    """解密 QMC 文件数据。

    返回 (音频字节, 输出格式)。失败抛 ValueError。
    """
    if ext not in _QMC_EXT_MAP:
        raise ValueError(f"不支持的 QMC 格式: {ext}")
    dec = QmcDecoder(data)
    audio = dec.decrypt()
    fmt = _QMC_EXT_MAP[ext]
    # 魔数校验：解密结果必须是可识别的音频容器，否则说明密钥不对/文件未内嵌密钥
    sniffed = sniff_audio_fmt(audio)
    if sniffed is None:
        if not dec.key_embedded:
            if ext in _QMC_V2_EXTS:
                raise ValueError(
                    "该文件未内嵌解密密钥（新版 QQ 音乐的 %s 需从客户端导出密钥），无法解密" % ext)
            raise ValueError("静态密钥解密失败，文件可能已损坏或使用了更高版本的加密（%s）" % ext)
        raise ValueError(
            "解密结果不是可识别的音频数据（头部 %s），密钥可能不匹配或文件已损坏"
            % audio[:4].hex())
    if sniffed != fmt:
        # 以真实容器为准，避免扩展名与实际格式不符导致播放器打不开
        fmt = sniffed
    return audio, fmt


# ============================================================================
# 4. 通用入口
# ============================================================================

_NCM_EXTS = {"ncm"}


def detect_format(path: str):
    """根据扩展名判断是否可转换及其类型。返回 'ncm' / 'qmc' / None。"""
    ext = os.path.splitext(path)[1].lower().lstrip(".")
    if ext in _NCM_EXTS:
        return "ncm"
    if ext in _QMC_EXTS:
        return "qmc"
    return None


def convert_file(src: str, out_dir: str, keep_name=True):
    """转换单个加密文件到 out_dir。

    返回 dict:
      ok, out_path, fmt, music_name, image, meta, error
    """
    try:
        kind = detect_format(src)
        if not kind:
            return {"ok": False, "error": f"不支持的格式: {os.path.basename(src)}"}
        stem = os.path.splitext(os.path.basename(src))[0]
        if kind == "ncm":
            r = decrypt_ncm_file(src)
            fmt = r["fmt"]
            music_name = r["music_name"]
            if keep_name and music_name:
                safe = _safe_filename(music_name)
                out_name = f"{safe}.{fmt}"
            else:
                out_name = f"{stem}.{fmt}"
            out_path = os.path.join(out_dir, out_name)
            with open(out_path, "wb") as f:
                f.write(r["data"])
            return {
                "ok": True, "out_path": out_path, "fmt": fmt,
                "music_name": music_name, "image": r["image"],
                "meta": r.get("meta") or {},
                "lyric": _ncm_lyric(r.get("meta")),
                "error": "",
            }
        else:  # qmc
            with open(src, "rb") as f:
                data = f.read()
            audio, fmt = decrypt_qmc_file(data, os.path.splitext(src)[1].lower().lstrip("."))
            out_path = os.path.join(out_dir, f"{stem}.{fmt}")
            with open(out_path, "wb") as f:
                f.write(audio)
            return {
                "ok": True, "out_path": out_path, "fmt": fmt,
                "music_name": stem, "image": None, "meta": {},
                "error": "",
            }
    except Exception as e:
        return {"ok": False, "error": str(e)}


def _safe_filename(name: str) -> str:
    name = name.replace("/", "_").replace("\\", "_").replace(":", "_")
    name = name.replace("*", "_").replace("?", "_").replace('"', "_")
    name = name.replace("<", "_").replace(">", "_").replace("|", "_")
    name = name.strip().strip(".")
    return name[:150] or "converted"


# ============================================================================
# 5. 自检
# ============================================================================

# 内置 QMCv2 密钥向量：由权威实现（nukemiko/libtakiyasha，与 jixunmoe qmc2-crypto
# 同源）生成并逐字节验证。只存密钥密文 ekey，音频明文用确定性 LCG 现场生成，
# 密文利用 XOR 对称性由 proc() 反向构造，因此无需外部测试数据。
_QMC_ST_EKEY_RC4_V1 = (
    "7jnqMWo2zbtvPZ+a9SMw3kTBo/9CaANmXyi51ysfUCNyFU1y1rG727BXNJZjdaxpNkvGf2dMuLVefGlY"
    "NXYZJsvdyXXkwvJB+XN/t4ZJ1YnUgtt+ny7W840eAV07uAYvTGbuddb7hKFd4fQBOEWh35KgUXTKxA0Q"
    "YZ6w4i+lUmSp66sbjbxT8uAwD0B5rl0B2kucNZuBUNuqN9OnNKgmAhvQ21gk+5uKMPAQXsF0Rm2nPEi+"
    "2mZ/1H8cdVWYAaq1HIJ58PCJrMfXx3dY1GE7kLCvWVBIOM3zw3oVVHalEHxizgwFW3v8GYzp87shUk4Q"
    "FMjbwtRs6qY0YGqnt4DxCpT/Cv0aob9GydidkMnk2yoWfHqzcmSN7ftMuVCkrBc9TpFmMHINsNZSJ02e"
    "00ukepGab2O8ZGIf3v0PCBHUtUXn7rvwvNippVTKMxxLN54unqEEu3vmq2awBn8jrS6w9P/cEL8OsVQP"
    "3o79KqD5mnUf8nlej3GtPf1xMJUjoGX/8Cmd0mQDlt6NrECFIomcaNqlqjzqdQqQlNqbPraa7Db0L/lH"
    "4VCqcF401F+iVH2XbukRLWx6zblr03hs5YpV13wZuDcRWh8g95MQA+QyK78ug/KqMyS5170b5jHx/dgU"
    "jLmmkESIZSNbJN0Hnr9oAHPhExisOAE250Ff41bATVmhhYqjtKK/ebWUyAQ4vFsO"
)
_QMC_ST_EKEY_MASK_V2 = (
    "UVFNdXNpYyBFbmNWMixLZXk6Qx4GLENCbOpY+Dwe2fKo/LjoMr5Yl6pN4OQBojNQla4/KdmsaIdjsJRY"
    "GRfpb0HAnYIbiC9HQ8f+ruGYR5PJUT6LtBYeeuwGm0CYPOqynRusrquccZTuVLLj1HKGR54bn1e7iWHG"
    "bdlPAdd3B5pl8Dzy61sBrFiQguxnlOa/MOAUbgSEiUroAhVgFeTdukE5ML9PBEqRmiJQxr3Z+iA83m+U"
    "HdzcDNNICiIyTXnsyp/0kcaY7iJpir/bxbHQ7Haw64y9uXVfO2bMpoig6b2UZcl72QI9WZN0kzjeuwBI"
    "+tnonAF41XQwfOo9LwD0ysheW/LijjtnCk2nYLMIk2i6Egsx1+oM4c62BE18AqEvliD8SNDkal3UQw2U"
    "SvI6eRylgeKbPQqMAzxhfHzeA5pzrBAl1+GjJ4et2GU+6Bs6sIaqfG6U/U403YgE+6HoUIB9ilvfArjR"
    "EmkWPCcFhELOWUWpaNE1IxfLx8SnRT5x5NritMOvphFKMD9L/HJpGc5pHyCiXQ99AA4="
)


def _qmc_st_plain(n: int, magic: bytes) -> bytes:
    """确定性伪随机音频明文（避开 0x00，保证密钥/明文均可复现）。"""
    out = bytearray(magic)
    s = 20260907
    for _ in range(n - len(magic)):
        s = (s * 1103515245 + 12345) & 0x7FFFFFFF
        out.append(((s >> 16) & 0xFF) or 1)
    return bytes(out)


def _qmc_st_build(ekey_b64: str, plain: bytes, tail: str) -> bytes:
    """用 ekey 构造一个完整的 QMCv2 加密文件（tail='qtag' 或 'notail'）。"""
    key_dec = qmc_derive_key(ekey_b64.encode())
    cipher = _QmcRC4Cipher(key_dec) if len(key_dec) > 300 else _QmcMapCipher(key_dec)
    buf = bytearray(plain)
    out = cipher.proc(buf, 0)    # XOR 对称：加密 == 解密
    ct = bytes(buf) if out is None else bytes(out)
    ekey = ekey_b64.encode()
    if tail == "qtag":
        qtag = ekey + b",12345678,2"
        return ct + qtag + struct.pack(">I", len(qtag)) + b"QTag"
    return ct + ekey + struct.pack("<I", len(ekey))


def run_selftest(testdata_dir=None):
    """本地自检：AES 向量 + 内置 QMCv2 向量 + QMC 真实样例（unlock-music testdata）。

    testdata_dir 指向 unlock-music 的 testdata 目录时额外验证真实样例。
    """
    results = []
    # AES-128 向量（FIPS-197 C.1）
    key = bytes.fromhex("000102030405060708090a0b0c0d0e0f")
    ct = bytes.fromhex("69c4e0d86a7b0430d8cdb78070b4c55a")
    pt = bytes.fromhex("00112233445566778899aabbccddeeff")
    got = aes_128_ecb_decrypt(key, ct)
    results.append(("AES-128 FIPS-197", got == pt))
    # TEA-32 往返自检（unlock-music QMC 使用的轮数）
    def _tea_encrypt_block(block, key, rounds=32):
        k0, k1, k2, k3 = struct.unpack(">IIII", key)
        v0, v1 = struct.unpack(">II", block)
        s = 0
        for _ in range(rounds // 2):
            s = (s + _TEA_DELTA) & 0xffffffff
            v0 = (v0 + ((((v1 << 4) & 0xffffffff) + k0) ^ (v1 + s) ^ (((v1 >> 5) & 0xffffffff) + k1))) & 0xffffffff
            v1 = (v1 + ((((v0 << 4) & 0xffffffff) + k2) ^ (v0 + s) ^ (((v0 >> 5) & 0xffffffff) + k3))) & 0xffffffff
        return struct.pack(">II", v0, v1)

    tea_pt = bytes.fromhex("0011223344556677")
    tea_key = bytes.fromhex("000102030405060708090a0b0c0d0e0f")
    tea_ct = _tea_encrypt_block(tea_pt, tea_key, rounds=32)
    tea_back = _tea_decrypt_block(tea_ct, tea_key, rounds=32)
    results.append(("TEA-32 往返", tea_back == tea_pt))

    # 内置 QMCv2 端到端向量：RC4(512)+QTag+EncV1、Mask128(256)+无尾魔数+EncV2
    # 明文 6000 字节，覆盖首 128 字节段与 5120 字节段边界
    for name, ekey, tail, ext, magic, want_fmt in (
        ("QMC RC4+QTag+EncV1", _QMC_ST_EKEY_RC4_V1, "qtag", "mflac", b"fLaC", "flac"),
        ("QMC Mask128+EncV2", _QMC_ST_EKEY_MASK_V2, "notail", "mgg", b"OggS", "ogg"),
    ):
        try:
            plain = _qmc_st_plain(6000, magic)
            audio, fmt = decrypt_qmc_file(_qmc_st_build(ekey, plain, tail), ext)
            results.append((name, audio == plain and fmt == want_fmt))
        except Exception as e:
            results.append((name, f"ERROR: {e}"))

    # 负向量：尾部无内嵌密钥必须明确报错，不能静默写出无法播放的垃圾文件
    try:
        decrypt_qmc_file(bytes(range(256)) * 40, "mflac")
        results.append(("QMC 无密钥应报错", False))
    except ValueError:
        results.append(("QMC 无密钥应报错", True))
    except Exception as e:
        results.append(("QMC 无密钥应报错", f"ERROR: {e}"))

    if testdata_dir and os.path.isdir(testdata_dir):
        cases = ["mflac0_rc4", "mflac_rc4", "mflac_map", "mgg_map", "qmc0_static"]
        for name in cases:
            try:
                with open(os.path.join(testdata_dir, f"{name}_raw.bin"), "rb") as f:
                    raw = f.read()
                with open(os.path.join(testdata_dir, f"{name}_suffix.bin"), "rb") as f:
                    suffix = f.read()
                with open(os.path.join(testdata_dir, f"{name}_target.bin"), "rb") as f:
                    target = f.read()
                cipher_text = raw + suffix
                dec = QmcDecoder(cipher_text)
                out = dec.decrypt()
                results.append((f"QMC {name}", out == target))
            except Exception as e:
                results.append((f"QMC {name}", f"ERROR: {e}"))
    else:
        results.append(("QMC testdata", "skip（未指定 testdata 目录）"))
    return results


if __name__ == "__main__":
    import sys
    td = sys.argv[1] if len(sys.argv) > 1 else None
    for name, ok in run_selftest(td):
        status = "PASS" if ok is True else ("SKIP" if str(ok).startswith("skip") else f"FAIL({ok})")
        print(f"[{status}] {name}")
