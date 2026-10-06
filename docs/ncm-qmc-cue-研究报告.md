# music-tidy v1.12.0 加密转换与 CUE 拆分 研究报告

研究对象（只读）：
- `/tmp/music-tidy/app/server/converter.py`（937 行）
- `/tmp/music-tidy/app/server/cue_splitter.py`（339 行）

调用方补充证据（用于回答"回滚/并发/其他转换"）：
- `/tmp/music-tidy/app/server/tidy_engine.py`
- `/tmp/music-tidy/app/server/music_tidy.py`
- `/tmp/music-tidy/app/server/scraper.py`

---

## 0. 结论速览

| 项 | 事实 |
|---|---|
| 加密格式 | 仅两类：`.ncm`（网易云）与 QQ 音乐 QMC 家族（`_QMC_EXT_MAP`，约 28 个扩展名） |
| NCM 算法 | 头部 `CTENFDAM` 魔数 → key 异或 `0x64` → 自写 AES-128-ECB 解密 + PKCS#7 → 17 字节前缀丢弃 → RC4-KSA 派生 256 字节 keybox → 全量音频循环异或 |
| QMC 算法 | 三套：QMCv1 静态表掩码、QMCv2 Mask 映射、QMCv2 RC4（密钥长度 >300 走 RC4）；密钥来自文件尾部 QTag / 旧式尾部小端长度 |
| 关键限制 | **尾部 `STag` 直接报错**；无内嵌密钥的 QMCv2 无法解密（不静默产出垃圾） |
| 输出校验 | `sniff_audio_fmt()` 魔数嗅探，识别不出即报错，且以真实容器覆盖扩展名映射 |
| 命名 | NCM 优先 `歌手 - 歌名`（元数据），QMC 一律沿用原文件名 stem |
| 回滚 | **无临时文件/无原子 rename**；解密数据全在内存，成功后一次性写入；失败由调用方保留原密文 |
| 并发 | converter 内部**零并发**；`/api/convert` 串行 for 循环；CUE 拆分是单个 daemon 线程 + 409 任务锁 |
| 非加密转换 | converter.py **不做**；ape/wav→flac 在 `tidy_engine._post_download_convert()`，依赖外部 ffmpeg |
| CUE 依赖 | 强依赖外部 `ffmpeg` 可执行文件，缺失即 `RuntimeError`；**无 subprocess 超时** |

---

## 1. 支持的加密格式全清单

### 1.1 判定入口
`converter.py:743-750`：

```python
def detect_format(path: str):
    ext = os.path.splitext(path)[1].lower().lstrip(".")
    if ext in _NCM_EXTS:      # {"ncm"}  converter.py:740
        return "ncm"
    if ext in _QMC_EXTS:      # set(_QMC_EXT_MAP.keys())  converter.py:675
        return "qmc"
    return None
```

### 1.2 QMC 扩展名 → 输出格式映射表
`converter.py:664-673`：

```python
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
```

共 28 项。末尾 5 个是 hex 命名的伪装扩展名（`666c6163`＝"flac"、`6d7033`＝"mp3"、`6f6767`＝"ogg"、`6d3461`＝"m4a"、`776176`＝"wav"）。

`converter.py:677-678` 额外标注了"必须内嵌密钥否则不可解"的 V2 家族：

```python
_QMC_V2_EXTS = {"mflac", "mflac0", "mgg", "mgg0", "mgg1", "mggl", "mmp4"}
```

`scraper.py:46-53` 有一份**平行维护**的 `CONVERT_EXTS`（用于扫描阶段告诉用户"这些歌要先解密"），含 `.ncm` + 全部 QMC 扩展名；两处靠注释人工同步，是脆弱点。

### 1.3 各自的加密方案

| 家族 | 代表扩展名 | 加密方案 | 密钥来源 |
|---|---|---|---|
| NCM | `.ncm` | AES-128-ECB（核心 key）+ AES-128-ECB（meta key）+ RC4 风格 keybox 流异或 | 文件头 key 区 |
| QMCv1 静态 | `.qmc0/.qmc3/.bkcmp3/...` | 256 字节静态表 `box[(i*i+27)&0xff]` 掩码 | 内置常量，无需密钥 |
| QMCv2 Mask | `.mgg/.mflac/...` 小密钥 | `rotate(key[(off²+71214)%len], idx&7)` 掩码 | 文件尾部 QTag / 尾部长度的 base64 密钥 |
| QMCv2 RC4 | 同上，派生密钥 >300 字节 | 分段 RC4（首段 0x80 + 段长 5120） | 同上 |

算法来源在文件头注明为 unlock-music（MIT）：`converter.py:12-15`（`src/decrypt/ncm.ts`、`qmc.ts/qmc_key.ts/qmc_cipher.ts`、`qmc_cipher.hpp`、`src/utils/tea.ts`）。

---

## 2. NCM 解密完整流程

入口 `decrypt_ncm_file()`，`converter.py:210-280`。逐段拆解：

### 2.1 常量
`converter.py:188-190`：

```python
_NCM_CORE_KEY = bytes.fromhex("687a4852416d736f356b496e62617857")  # "hzHRAmso5kInbaxW"
_NCM_META_KEY = bytes.fromhex("2331346c6a6b5f215c5d2630553c2728")  # "#14ljk_!\]&0U<('"
_NCM_MAGIC    = b"CTENFDAM"
```

两者均为 16 字节，正好满足 AES-128。

### 2.2 头部与 key 区（`converter.py:220-234`）

```python
header = f.read(8)
if header != _NCM_MAGIC:                     # "CTENFDAM"
    raise ValueError("无效的 NCM 文件（magic 校验失败）")
f.seek(2, 1)                                  # 跳过 2 字节
key_len = struct.unpack("<I", f.read(4))[0]   # 小端 uint32
cipher_key = f.read(key_len)
cipher_key = bytes(b ^ 0x64 for b in cipher_key)          # ① 逐字节异或 0x64
plain_key = pkcs7_unpad(aes_128_ecb_decrypt(_NCM_CORE_KEY, cipher_key))  # ② AES-128-ECB
key_data = plain_key[17:]                     # ③ 丢弃 17 字节固定前缀
keybox = _ncm_key_box(key_data)               # ④ 派生 256 字节 keybox
```

要点：
1. **魔数**为 8 字节 ASCII `CTENFDAM`，位于偏移 0。
2. 偏移 8..9 的 2 字节被 `f.seek(2,1)` 无条件跳过（未做校验）。
3. key 密文先整体异或 `0x64` 再 AES 解密；解密结果再用 PKCS#7 严格去填充。
4. `plain_key[17:]` 丢弃固定 17 字节头，剩余部分才是 keybox 的种子。

### 2.3 keybox 生成（RC4 KSA + 自定义 PRGA）
`converter.py:193-207`：

```python
def _ncm_key_box(key_data: bytes) -> bytes:
    box = list(range(256))
    j = 0
    klen = len(key_data)
    for i in range(256):                                  # KSA
        j = (box[i] + j + key_data[i % klen]) & 0xff
        box[i], box[j] = box[j], box[i]
    keybox = bytearray(256)
    for i in range(256):                                  # 非标准 PRGA
        ii = (i + 1) & 0xff
        si = box[ii]
        sj = box[(ii + si) & 0xff]
        keybox[i] = box[(si + sj) & 0xff]
    return bytes(keybox)
```

注意这不是标准 RC4 输出：KSA 与标准一致；输出阶段固定按 `(i+1)` 取三处索引再查表，等价于 unlock-music `_getKeyBox` 的固定 256 字节表。**必须逐字节照抄，不能替换成 `crypto/rc4`**。

### 2.4 元数据（`converter.py:236-250`）

```python
meta_len = struct.unpack("<I", f.read(4))[0]
meta = {}
if meta_len > 0:
    cipher_meta = bytes(b ^ 0x63 for b in f.read(meta_len))          # 异或 0x63
    b64 = base64.b64decode(cipher_meta[22:])                         # 丢 22 字节前缀
    plain_meta = pkcs7_unpad(aes_128_ecb_decrypt(_NCM_META_KEY, b64)).decode("utf-8")
    idx = plain_meta.index(":")
    label, body = plain_meta[:idx], plain_meta[idx + 1:]
    meta = json.loads(body)
    if label == "dj" and isinstance(meta, dict):
        meta = meta.get("mainMusic", meta)                           # DJ 节目折叠
```

- 元数据密文异或 **`0x63`**（与 key 区的 `0x64` 不同）。
- 前 22 字节是固定前缀 `"163 key(Don't modify):"`，直接切片丢弃。
- 解密后形如 `music:{...json...}` 或 `dj:{...}`，按第一个 `:` 分标签与 JSON 体。
- `dj` 标签时取 `mainMusic` 子对象。整个元数据段包在 `try/except` 里，**任何异常都静默降级为 `meta = {}`**（`converter.py:249-250`），元数据损坏仍能出音频。

歌名/歌手提取在 `converter.py:270-279`：`artist` 是 `[[名字, id], ...]` 的嵌套数组，取每个元素的第 0 项用 `/` 连接，拼成 `"歌手 - 歌名"`。

### 2.5 封面与歌词

封面（`converter.py:252-258`）：

```python
f.seek(5, 1)                                            # 跳过 5 字节 CRC/间隔区
image_space = struct.unpack("<I", f.read(4))[0]
image_size  = struct.unpack("<I", f.read(4))[0]
image = f.read(image_size) if image_size else None
if image_space > image_size:
    f.seek(image_space - image_size, 1)                 # 跳过预留空间
```

`image` 只是原始字节（PNG/JPEG），**不做格式判定**；由调用方落盘为 sidecar。`music_tidy.py:1036-1041` 固定写 `.jpg` 后缀（若实际是 PNG，扩展名会撒谎，需注意移植时补魔数判定）。

歌词（`converter.py:283-294`）：从 `meta["lyric"]` 取 base64，解码为 UTF-8 文本（就是 LRC），`decode(..., "replace")` 容错，失败返回空串。

### 2.6 音频解密与输出（`converter.py:260-280`）

```python
audio = f.read()                                        # 剩余全部即密文音频
rep = (keybox * ((len(audio) + 255) // 256))[:len(audio)]
out = _xor_bytes(audio, rep)                            # 256 字节周期异或
```

`_xor_bytes`（`converter.py:177-181`）用大整数一次性异或，避免 Python 逐字节循环：

```python
return (int.from_bytes(a, "big") ^ int.from_bytes(b, "big")).to_bytes(len(a), "big")
```

**输出格式判定**（`converter.py:267-269`）：

```python
fmt = (meta.get("format") or "").lower()
if fmt not in ("mp3", "flac", "ogg", "m4a", "wav", "ape"):
    fmt = sniff_audio_fmt(out) or ("flac" if len(out) > 1024 * 1024 * 16 else "mp3")
```

即：优先信元数据 `format`，否则魔数嗅探，**再兜底按 16 MiB 大小猜**（`>16MiB` 猜 flac，否则猜 mp3）。这个兜底是弱启发，Go 移植建议去掉或改为扩展名提示。

返回 dict：`data / fmt / meta / image / music_name`（`converter.py:280`）。

---

## 3. 纯 Python AES-128-ECB 实现剖析

全部实现只做**解密**，位于 `converter.py:26-174`。结构：

| 组件 | 行号 | 说明 |
|---|---|---|
| `_INV_SBOX` | 30-47 | 逆 S 盒 256 项 |
| `_RCON` | 49 | 轮常量 11 项 |
| `_SBOX` | 52-69 | 正向 S 盒（仅密钥扩展 SubWord 用） |
| `_gmul(a,b)` | 72-83 | GF(2^8) 俄式乘法，模 `0x1b` |
| `_inv_mix_column` | 86-93 | 逆列混合 [0e,0b,0d,09] |
| `_aes_key_expansion` | 96-114 | 11 组轮密钥 |
| `_aes_inv_shift_rows` | 117-124 | 逆移位（列主序直接映射） |
| `_aes_decrypt_block` | 127-149 | 单块解密 |
| `aes_128_ecb_decrypt` | 152-162 | 任意长度分块 ECB |
| `pkcs7_unpad` | 165-174 | 严格 PKCS#7 去填充 |

### 3.1 正确性要点

1. **轮顺序**（`converter.py:130-148`）：`AddRoundKey(rk[10])` → 9 轮 `[InvShiftRows+InvSubBytes → AddRoundKey → InvMixColumns]` → 末轮 `[InvShiftRows+InvSubBytes → AddRoundKey(rk[0])]`。与 FIPS-197 等价轮结构一致。
2. **状态布局为列主序**（`state[r + 4c]`）。`_aes_inv_shift_rows` 用硬编码索引表代替循环，逐行核对无误：row1 右移 1（`new[1]=old[13], new[5]=old[1], new[9]=old[5], new[13]=old[9]`）、row2 右移 2、row3 右移 3。
3. `InvShiftRows` 与 `InvSubBytes` 都是**逐字节置换**，可交换顺序——代码先 ShiftRows 再 SubBytes，合法。
4. **密钥扩展**（`converter.py:96-114`）：`nk=4, nr=10`，`i%nk==0` 时 RotWord→SubWord→`temp[0]^=RCON[i//4]`；无 AES-256 的额外 SubWord 分支，因为是 AES-128。轮密钥按列优先拼成 16 字节。
5. **`_gmul`**：俄式乘，`a` 固定为 `0e/0b/0d/09`，越界减 `0x1b`，正确。
6. 入口强校验：key 必须 16 字节、数据长度必须是 16 的倍数（`converter.py:154-157`），否则抛 `ValueError`。
7. **FIPS-197 C.1 自检**内建（`converter.py:866-871`）：
   ```python
   key = bytes.fromhex("000102030405060708090a0b0c0d0e0f")
   ct  = bytes.fromhex("69c4e0d86a7b0430d8cdb78070b4c55a")
   pt  = bytes.fromhex("00112233445566778899aabbccddeeff")
   got = aes_128_ecb_decrypt(key, ct)
   results.append(("AES-128 FIPS-197", got == pt))
   ```
8. `pkcs7_unpad` 不仅看末字节，还逐字节校验填充值（`converter.py:170-173`），比"只看长度"更严格——副作用是对非标准填充文件可能拒解。

### 3.2 性能真相

纯 Python 逐块解密 + 每轮 list 推导，属于"能跑但慢"：元数据/密钥只有几十到几百字节，可接受；**音频流不经过 AES**（只有 keybox 异或），所以整体性能瓶颈不在 AES。真正需要注意 Python 侧开销的是 `_xor_bytes` 的大整数异或和大表预计算。

---

## 4. QMC 系列

### 4.1 三套密码

**(a) QMCv1 静态表**（`converter.py:302-359`）

```python
_QMC_STATIC_BOX = [0x77, 0x48, 0x32, ...]        # 256 字节，qmc_cipher.hpp
_QMC_MASK_MOD = 0x7fff
_QMC_STATIC_PRE = bytes(_QMC_STATIC_BOX[(i * i + 27) & 0xff]
                        for i in range(_QMC_MASK_MOD + 1))   # 32768 项预算表
```

掩码流语义（`_apply_mask_stream`，`converter.py:326-348`）：`mask(o) = pre[o]`（`o<=0x7fff`），`o>0x7fff` 时 `pre[o % 0x7fff]`。实现上做了分段快路径：前 `0x8000` 字节用 `pre`，之后用 `cycle = pre[1:0x7fff] + pre[0:1]`（长度 32767）周期重复。这个边界极易写错，是移植第一坑。

**(b) QMCv2 Mask 映射**（`converter.py:362-380`）

```python
def _rotate(value, bits):
    r = (bits + 4) % 8
    return ((value << r) | (value >> r)) & 0xff

def _get_mask(self, offset):
    if offset > _QMC_MASK_MOD:
        offset %= _QMC_MASK_MOD
    idx = (offset * offset + 71214) % len(self.key)
    return self._rotate(self.key[idx], idx & 0x7)
```

注意 `_rotate` **不是标准循环移位**，而是"左移 r 与右移 r 相或"，必须原样照搬。同样在构造时预计算 32768 项 `_pre`。

**(c) QMCv2 RC4**（`converter.py:383-471`）

```python
_FIRST_SEGMENT_SIZE = 0x80
_SEGMENT_SIZE = 5120

self.S = bytearray([i & 0xff for i in range(n)])     # S 长度 = len(key)，不是 256！
j = 0
for i in range(n):
    j = (self.S[i] + j + key[i % n]) % n
    self.S[i], self.S[j] = self.S[j], self.S[i]

h = 1
for v in key:                                        # uint32 截断 hash
    if v == 0: continue
    nh = (h * v) & 0xffffffff
    if nh == 0 or nh <= h: break
    h = nh
self.hash = h
```

分段密钥（`converter.py:406-415`）用**浮点除法**再截断：

```python
idx = (self.hash / ((sid + 1) * seed)) * 100.0
r = idx % len(self.key)
if math.isnan(r):        # 对应 JS NaN 索引 → 0
    return 0
return int(r)
```

段处理（`converter.py:417-434`）：首段（offset<0x80）用 `key[segment_key(offset+i)]` 直接异或；其余段 `skip_len = (offset % 5120) + segment_key(offset // 5120)`，循环从 `i = -skip_len` 起跑，`i>=0` 才写出，`j/k` 每步照常推进——这是"段内偏移对齐"的关键细节，写歪就会周期性错位。

### 4.2 尾部密钥解析（`QmcDecoder._search_key`，`converter.py:600-642`）

```python
last4 = self.file[-4:]
if last4 == b"STag":
    raise ValueError("该文件未内嵌密钥（STag 格式），无法解密；需从 QQ 音乐客户端导出密钥")
if last4 == b"QTag":
    key_size = struct.unpack(">I", self.file[-8:-4])[0]     # 大端
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
    ...
    self.song_id = int(id_buf[:id_end])
else:
    key_size = struct.unpack("<I", last4)[0]                # 小端（旧格式）
    tail_key = None
    if 16 <= key_size and key_size + 4 < self.size:
        cand = self.file[self.size - 4 - key_size:self.size - 4]
        if all(c in _B64_BYTES for c in cand):              # 全 base64 字符才认
            tail_key = cand
    if tail_key is not None:
        self.audio_size = self.size - key_size - 4
        self._set_cipher(tail_key)
        self.key_embedded = True
    else:
        self.audio_size = self.size
        self.cipher = _QmcStaticCipher()                    # 回退 QMCv1
```

- QTag 尾部结构：`[音频][base64密钥][,][songId][,][version?]\x00?` + **大端 uint32 key_size** + `"QTag"`。
- 旧格式：`[音频][base64密钥]` + **小端 uint32 密钥长度**，无魔数。
- 判定旧格式时**不按长度阈值**，而要求候选区"每个字节都是 base64 字符"，注释（`converter.py:629-631`）说明这是为了避免 EncV2 密钥（base64 长度可能接近甚至超过 0x400）被误判，也避免 QMCv1 静态文件被误判。
- 大小写：QTag/STag 严格区分大小写。

### 4.3 `_set_cipher`：Mask vs RC4（`converter.py:644-649`）

```python
key_dec = qmc_derive_key(key_raw)
if len(key_dec) > 300:
    self.cipher = _QmcRC4Cipher(key_dec)
else:
    self.cipher = _QmcMapCipher(key_dec)
```

**阈值是派生密钥长度 300 字节**，不是文件大小或扩展名。这是第二坑。

### 4.4 密钥派生链（`converter.py:557-585`）

```python
_QMC_MIX_KEY1 = bytes([0x33,0x38,0x36,0x5A,...,0x28])   # 16 字节
_QMC_MIX_KEY2 = bytes([0x2A,0x2A,0x23,0x21,...,0x54])   # 16 字节
_QMC_V2_PREFIX = b"QQMusic EncV2,Key:"

def _qmc_decrypt_v2_key(raw):
    if len(raw) < 18 or raw[:18] != _QMC_V2_PREFIX:
        return raw                                   # EncV1：原样返回
    out = _tea_decrypt_cbc(raw[18:], _QMC_MIX_KEY1)
    out = _tea_decrypt_cbc(out, _QMC_MIX_KEY2)
    key_dec = base64.b64decode(out)
    if len(key_dec) < 16: raise ValueError(...)
    return key_dec

def qmc_derive_key(raw):
    raw_dec = base64.b64decode(raw)
    if len(raw_dec) < 16: raise ValueError("key length is too short")
    raw_dec = bytearray(_qmc_decrypt_v2_key(bytes(raw_dec)))     # EncV2 双 TEA
    simple_key = _qmc_simple_make_key(106, 8)                    # tan 表
    tea_key = bytearray(16)
    for i in range(8):
        tea_key[i << 1]     = simple_key[i]
        tea_key[(i << 1)+1] = raw_dec[i]
    sub = _tea_decrypt_cbc(bytes(raw_dec[8:]), bytes(tea_key))
    return bytes(raw_dec[:8]) + sub
```

`_qmc_simple_make_key`（`converter.py:568-569`）即腾讯的 tan 表：

```python
return [0xff & int(math.trunc(abs(math.tan(salt + i * 0.1)) * 100.0))
        for i in range(length)]        # salt=106
```

`_tea_decrypt_block`（`converter.py:478-491`）是标准 TEA，`rounds=32`（unlock-music QMC 用的轮数，而标准 TEA 是 64 轮）；CBC 变体 `_tea_decrypt_cbc`（`converter.py:498-548`）带 2 字节 Salt 与 7 字节 Zero 校验（`_TEA_SALT_LEN=2`、`_TEA_ZERO_LEN=7`），校验失败抛 `zero check failed`。

### 4.5 哪些情况无法解密（明确报错）

| 情况 | 代码位置 | 报错 |
|---|---|---|
| 尾部 `STag`（新版客户端，密钥在外部 DB） | `converter.py:604-605` | `该文件未内嵌密钥（STag 格式），无法解密；需从 QQ 音乐客户端导出密钥` |
| QTag `key_size==0` 或越界 | `606-609` | `文件尾部 QTag 结构损坏，无法定位密钥` |
| QTag 内无逗号 / 无 songId 逗号 | `613-620` | `文件尾部密钥格式错误…` |
| 文件 <16 字节 | `601-602` | `文件过小，不是有效的 QQ 音乐加密文件` |
| base64 解码后 <16 字节 | `575-576` | `key length is too short` |
| EncV2 TEA 校验失败 | `547` | `zero check failed` |
| 解密结果魔数不识别且无内嵌密钥，且属 V2 家族 | `721-726` | `该文件未内嵌解密密钥（新版 QQ 音乐的 X 需从客户端导出密钥），无法解密` |
| 解密结果魔数不识别且无内嵌密钥（V1 家族） | `726` | `静态密钥解密失败，文件可能已损坏…` |
| 有内嵌密钥但结果仍不可识别 | `727-729` | `解密结果不是可识别的音频数据（头部 xxxx），密钥可能不匹配…` |
| `audio_size <= 0` | `654-655` | `invalid audio size` |
| 扩展名不在映射表 | `714-715` | `不支持的 QMC 格式: X` |

设计要点：**无密钥时宁可不产出，也不写垃圾文件**，注释见 `converter.py:677` 与负向量自检 `902-909`。

---

## 5. 输出、命名、校验、失败与并发

### 5.1 真实格式判定：`sniff_audio_fmt()`（`converter.py:681-706`）

```python
if head == b"fLaC": return "flac"
if head == b"OggS": return "ogg"
if head == b"MAC ": return "ape"
if head == b"RIFF" and data[8:12] == b"WAVE": return "wav"
if data[:4] == b"\x30\x26\xb2\x75": return "wma"
if data[4:8] in (b"ftyp", b"moov", b"mdat", b"free", b"skip", b"wide"): return "m4a"
if data[:3] == b"ID3": return "mp3"
if data[0] == 0xFF and (data[1] & 0xE0) == 0xE0: return "mp3"   # MP3 帧同步
return None
```

`decrypt_qmc_file()`（`converter.py:709-733`）用它做三重校验：识别不出→按"是否有内嵌密钥"分类报错；识别出的容器与扩展名映射不一致时**以真实容器为准**（`730-732`）。

### 5.2 命名规则（`converter.py:753-804`）

- NCM：`keep_name=True`（默认）且元数据有 `musicName` → `{歌手 - 歌名}.{fmt}`；否则 `{原 stem}.{fmt}`（`764-772`）。
- QMC：**永远** `{原 stem}.{fmt}`（`787`）。
- `_safe_filename`（`799-804`）：替换 `/\:*?"<>|` 共 9 个字符为 `_`，`strip().strip(".")`，截断 **150** 字符，空则 `"converted"`。注意**不做 Windows 保留名（CON/PRN/NUL…）与尾随空格处理**。

### 5.3 失败回滚

- `convert_file` 用**一个** `try/except` 包全场（`759-796`），异常 → `{"ok": False, "error": str(e)}`。
- 音频字节先在内存中全部解密完成，才 `open(out_path,"wb").write(...)`（`774-775`、`788-789`）——所以"解密失败"不会留下半成品。
- 但**没有 `.part` 临时文件 + `os.replace` 原子改名**：若写入中途磁盘满/进程被杀，会留半个文件；且会**静默覆盖**已存在的同名输出（无存在性检查）。
- 调用方语义（`music_tidy.py:526-530`、`tidy_engine.py:4063-4066`）：**成功后**才删除源密文；失败保留原文件并 `log("warn", ...)`。`tidy_engine.py:4089-4095` 的 ffmpeg 分支则是"先写 `.__tc__.` 临时文件，成功才 `os.replace`，失败删临时文件"——比 converter 更严谨，值得对齐。

### 5.4 并发

- `converter.py` 内部**没有任何线程/锁/异步**，全部同步函数。
- `/api/convert`（`music_tidy.py:1029-1057`）与 `/api/convert_upload`（`497-534`）都是**串行 for 循环**逐文件转换。
- HTTP 服务是 `ThreadingHTTPServer`（`music_tidy.py:26`、`1325`），所以两个用户同时点转换会并发进入 `convert_file`，而它没有任何输出路径互斥——同名输出存在互相覆盖的理论风险。
- CUE 拆分走单个 daemon 线程（`music_tidy.py:944-946`），入口用 `eng.task_status().get("running")` 做 409 互斥（`941-943`）。
- 自检：`run_selftest()`（`converter.py:860-929`）已验证 AES FIPS 向量、TEA-32 往返、两条内置 QMCv2 端到端向量（RC4+QTag+EncV1 / Mask128+EncV2）、无密钥负向量；`__main__` 可命令行跑（`932-937`）。这是非常值得抄的工程习惯。

---

## 6. 其他转换 & ffmpeg 依赖

`converter.py` **只做加密解密**，不含任何 ape/wav→flac 之类转换。非加密转码在 `tidy_engine.py:4049-4096`：

```python
_COMMON_EXTS = {".mp3", ".flac", ".m4a", ".wav", ".ogg"}          # 4047
...
if converter.detect_format(path):                                  # 4060 加密 → 内置解密
    r = converter.convert_file(path, out_dir, keep_name=False)
    ...
if shutil.which("ffmpeg"):                                         # 4073 其它 → ffmpeg
    lossless = ext in (".ape", ".wv", ".tta", ".dsf", ".dff", ".aiff")
    tgt = "flac" if lossless else "mp3"
    tmp = f"{stem}.__tc__.{tgt}"
    cmd = ["ffmpeg", "-y", "-i", path]
    if tgt == "mp3": cmd += ["-codec:a", "libmp3lame", "-q:a", "0"]
    cmd += [tmp]
    r = subprocess.run(cmd, capture_output=True, timeout=600)      # 有 600s 超时
```

结论：converter 本身**零外部依赖**；ffmpeg 只用于 ①非加密格式转码 ②CUE 拆分 ③`tidy_engine` 的内嵌标签兜底（`1536-1556`、`1681+`）④`ffprobe` 取时长码率（`2085-2090`）。缺失时各路径分别降级（保留原文件 / 报错）。

---

## 7. CUE 解析（cue_splitter.py）

### 7.1 数据结构
`CueTrack`（`cue_splitter.py:14-24`）：`number/title/artist/start_seconds/album/album_artist/date/genre`。
`CueSheet`（`27-35`）：`file_path/album/album_artist/date/genre/tracks`。

### 7.2 编码自动检测（`cue_splitter.py:60-76`）

```python
encodings = ["utf-8", "gbk", "cp936", "utf-16", "latin-1"]
for enc in encodings:
    try:
        with open(cue_path, "r", encoding=enc) as f:
            return f.read()
    except (UnicodeDecodeError, UnicodeError):
        continue
return None
```

- 顺序即优先级；`gbk` 与 `cp936` 在 Python 中近似同义（冗余但无害）。
- **`latin-1` 永不失败**，所以只要能打开文件必返回内容——它是兜底但会把乱码"伪装"成成功。
- **不处理 BOM**：UTF-8-BOM 文件用 `utf-8` 读会保留 `\ufeff`；若首行是 `REM GENRE`，正则 `^REM` 会失配（BOM 后首行解析丢失）。UTF-16 BOM 文件会被 `utf-8`/`gbk` 尝试失败后由 `utf-16` 接住，但**无 BOM 的 UTF-16LE 会直接落到 latin-1 产生乱码**。
- 无编码探测库（chardet 等），全靠异常回退。

### 7.3 支持字段（`cue_splitter.py:106-169`）

| 字段 | 正则 | 支持度 |
|---|---|---|
| `REM GENRE "x"` | `^REM\s+GENRE\s+"?([^"]*)"?$` (112) | ✅ 全局 |
| `REM DATE "x"` | `^REM\s+DATE\s+"?([^"]*)"?$` (118) | ✅ 全局 |
| `PERFORMER "x"` | `^PERFORMER\s+"([^"]*)"` (124) | ✅ 全局=album_artist，TRACK 后=track artist；**必须带双引号** |
| `TITLE "x"` | `^TITLE\s+"([^"]*)"` (133) | ✅ 全局=album，TRACK 后=track title；**必须带双引号** |
| `FILE "x" type` | `^FILE\s+"([^"]*)"` (142) | ⚠️ 只取文件名，忽略类型；多 FILE 会互相覆盖 |
| `TRACK nn AUDIO` | `^TRACK\s+(\d+)\s+AUDIO` (148) | ✅ 硬性要求 `AUDIO` 字样 |
| `INDEX 01 mm:ss:ff` | `^INDEX\s+01\s+(\d+:\d+:\d+)` (162) | ✅ 仅 01 |
| `INDEX 00` / `PREGAP` / `POSTGAP` | — | ❌ **完全不支持**，直接忽略 |
| `REM COMMENT` / `CATALOG` / `ISRC` / `FLAGS` / `SONGWRITER` | — | ❌ 忽略 |
| `TITLE` 无引号 | — | ❌ 不匹配 |

正则均带 `re.IGNORECASE`，行首 `strip()` 后匹配，忽略空行（`107-109`）。`current_track is None` 作为"全局段 vs 音轨段"的状态切换依据（`126`、`135`）。

### 7.4 时间戳解析（`cue_splitter.py:38-57`）

```python
parts = time_str.split(":")
if len(parts) != 3: return 0.0
minutes, seconds, frames = int(parts[0]), int(parts[1]), int(parts[2])
return minutes * 60 + seconds + frames / 75.0      # 75 帧/秒（CD 标准）
```

- 严格 3 段；非法值/非数字 → 返回 `0.0`（不报错）。
- 帧按 **75 fps** 换算为浮点秒；输出给 ffmpeg 时用 `str(float)`，例如 `245.66666666666666`。
- 缺 `INDEX 01` 的音轨 `start` 保持 `0.0` → 与首轨重叠（潜在坑）。

### 7.5 音频文件定位（`cue_splitter.py:174-193`）

```python
if audio_file:
    audio_path = os.path.join(cue_dir, audio_file)          # 相对 CUE 目录
    if not os.path.exists(audio_path):
        for f in os.listdir(cue_dir):                        # 大小写不敏感回退
            if f.lower() == audio_file.lower():
                audio_path = os.path.join(cue_dir, f); break
    sheet.file_path = audio_path
else:
    cue_stem = os.path.splitext(os.path.basename(cue_path))[0]
    audio_exts = [".flac", ".ape", ".wav", ".wv", ".tta", ".mp3", ".m4a"]
    for ext in audio_exts: ...                               # 同目录同名音频
```

- 仅做"整串文件名小写比较"，**不处理 Windows 路径反斜杠**、不做路径穿越校验、不递归子目录。
- 找不到文件**不在这里报错**：`file_path` 可能指向不存在的路径，由 `split_cue` 检查（`237-238`）。注意 `parse_cue` 单独使用时可能返回一个指向不存在文件的 sheet。

---

## 8. 拆分流程（`split_cue`，cue_splitter.py:217-319）

### 8.1 前置检查
1. `parse_cue` 失败 → `ValueError("无法解析 CUE 文件: ...")`（`234-235`）。
2. 音频不存在 → `ValueError("找不到关联的音频文件: ...")`（`237-238`）。
3. 无音轨 → `ValueError("CUE 文件中没有音轨信息")`（`240-241`）。
4. 输出目录：默认 `os.path.dirname(os.path.abspath(cue_path))`，`os.makedirs(exist_ok=True)`（`243-246`）。
5. ffmpeg 探测：`subprocess.run(["ffmpeg","-version"], capture_output=True, check=True)`，`CalledProcessError/FileNotFoundError` → `RuntimeError("找不到 ffmpeg，请先安装")`（`248-252`）。

### 8.2 时间切片：**不用 `-c copy`，全量重编码**
`cue_splitter.py:262-289`：

```python
if i + 1 < len(sheet.tracks):
    end_time = sheet.tracks[i + 1].start_seconds     # 下一轨起点即本轨终点
else:
    end_time = None                                  # 最后一轨到文件结尾

cmd = ["ffmpeg", "-hide_banner", "-loglevel", "error", "-y"]
cmd.extend(["-ss", str(track.start_seconds)])        # 输入侧定位（-i 之前）
if end_time is not None:
    cmd.extend(["-to", str(end_time)])               # 输出侧时间戳
cmd.extend(["-i", audio_path])
cmd.extend(["-c:a", "flac", "-compression_level", "5"])   # 重编码为 FLAC
```

要点与坑：
- `-ss` 在 `-i` **之前**是快速输入定位；配合输出侧 `-to`，语义是"从定位点起写 `end-start` 时长"（输出时间戳重置为 0），所以 `-to` 传的是**下一轨的绝对起点**而非时长，依赖 ffmpeg 的 timestamp 语义，可读性差。
- 使用 `-to` **而不是 `-t`**（时长）是容易误读的一点；显式算时长更稳。
- **无 `-c copy`**：所有格式（含 ape/wav/mp3）一律解到 PCM 再压 `flac -compression_level 5`。对 mp3 源是"有损转无损"（体积暴涨），对已有 FLAC 是二次编码（非 bit-exact 复制）。每轨都从整轨文件头开始 `-ss` 定位，N 轨要打开/定位同一大文件 N 次，无并行。
- **无 `-vn` / `-map`**：未显式丢弃视频/封面流，也未显式只取音频；`-c:a flac` 时若源含 attached_pic 流，行为依赖 ffmpeg 默认流选择。
- 无 `-threads`、无 `-map_metadata`、无 `-avoid_negative_ts` 等调优项。

### 8.3 命名（`cue_splitter.py:268-273`）

```python
title = track.title or f"Track {track.number:02d}"
safe_title = re.sub(r'[<>:"/\\|?*]', "_", title)
output_name = f"{track.number:02d} - {safe_title}.flac"
```

即 `%02d - 标题.flac`，扩展名**恒为 `.flac`**（因为输出恒为 FLAC）。不清理首尾空格/点，不做 Windows 保留名处理，**不加专辑目录层级**，直接平铺在 `output_dir`。

### 8.4 元数据注入（`cue_splitter.py:291-304`）

```python
if track.title:        cmd.extend(["-metadata", f"title={track.title}"])
if track.artist:       cmd.extend(["-metadata", f"artist={track.artist}"])
if track.album:        cmd.extend(["-metadata", f"album={track.album}"])
if track.album_artist: cmd.extend(["-metadata", f"album_artist={track.album_artist}"])
if track.date:         cmd.extend(["-metadata", f"date={track.date}"])
if track.genre:        cmd.extend(["-metadata", f"genre={track.genre}"])
cmd.extend(["-metadata", f"track={track.number}"])
```

- 全部来自 CUE 文本（`album_artist` 继承全局 PERFORMER，`album/date/genre` 全局继承，见 `201-211`）。
- **封面完全没有继承**：没有 `-map 0:v? -c:v copy -disposition:v attached_pic`，也没有先抽封面再 `-i cover.jpg -map 1 -c:v mjpeg`。整轨 APE/FLAC 的内嵌封面在拆分后丢失。
- 未写 `disc`、`album_artist` 之外的 `performer`、`composer` 等字段；`track` 用裸数字（未补零）。
- 元数据值是**直接字符串拼接**，未转义 `=`、换行；含换行的标题可能破坏 metadata 解析。

### 8.5 执行与校验（`cue_splitter.py:309-314`）

```python
result = subprocess.run(cmd, capture_output=True, text=True)
if result.returncode != 0:
    raise RuntimeError(f"ffmpeg 拆分失败: {result.stderr}")
output_files.append(output_path)
```

- **无 timeout**：`subprocess.run` 未传 `timeout=`，损坏的大文件可永久挂住（对比 `tidy_engine.py:4083` 的 `timeout=600`）。
- **无结果校验**：不检查 `output_path` 是否生成、大小是否 >0、是否以 `fLaC` 开头。
- 用 `-y` 无条件覆盖已有同名文件。

---

## 9. CUE 错误处理与降级

| 场景 | 行为 | 位置 |
|---|---|---|
| CUE 编码都无法读 | `parse_cue` 返回 `None` → `split_cue` 抛 `ValueError` | `88-90`、`234-235` |
| CUE 无 TRACK | 返回 `None` | `171-172` |
| FILE 指向的文件不存在 | 仍返回 sheet（`file_path` 为不存在路径）；`split_cue` 检查并抛 `ValueError` | `177-184`、`237-238` |
| 无 FILE 且同名音频不存在 | `sheet.file_path` 为空串；`split_cue` 抛 `ValueError("找不到关联的音频文件: ")`（消息含空路径，不友好） | `185-193`、`237-238` |
| ffmpeg 缺失 | `RuntimeError("找不到 ffmpeg，请先安装")`，**不降级**（无内置拆分实现） | `248-252` |
| 某轨 ffmpeg 失败 | 立即 `RuntimeError`，**中止后续所有轨**；已成功的轨文件**保留不回滚**，返回列表也拿不到（异常路径） | `310-312` |
| 进度回调 | 每轨开始前回调 `(i, total, msg)`，结束回调 `(total, total, "拆分完成")`；异常时不回调结束 | `258-260`、`316-317` |
| 子进程 stderr | 失败时整段塞进异常消息，可能很长 | `312` |

上层补偿（`tidy_engine.py:3732-3774`）：逐个 CUE `try/except` 收集 `stats{fail}`，单个 CUE 失败不影响其它；**幂等跳过**逻辑：

```python
existing = [f for f in os.listdir(output_dir)
            if f.endswith(".flac") and re.match(r"^\d{2}\s*-", f)]
if len(existing) >= len(sheet.tracks):
    self.log("info", f"跳过已拆分的 CUE: {cue_name}")   # 3745-3754
```

拆分成功后逐文件 `self._add_file(out_path)` 入库（`3766-3770`）。

`find_cue_files`（`322-339`）：`os.walk` 递归给定目录，收集 `.cue`（大小写不敏感）。

---

## 10. 关键常量一览

### converter.py
| 常量 | 值 | 行号 |
|---|---|---|
| `_NCM_MAGIC` | `b"CTENFDAM"` | 190 |
| NCM key 异或 | `0x64` | 229 |
| NCM meta 异或 | `0x63` | 240 |
| NCM meta 前缀长度 | `22` | 242 |
| NCM key 明文前缀丢弃 | `17` | 231 |
| NCM 跳过字节 | 头部 `2`、图片区 `5` | 224、253 |
| NCM 大小兜底阈值 | 16 MiB | 269 |
| `_QMC_MASK_MOD` | `0x7fff` (32767) | 320 |
| 静态掩码公式 | `box[(i*i+27)&0xff]` | 323 |
| Map 掩码公式 | `key[(off²+71214)%len]`，`rotate` 的 `(bits+4)%8` | 371、376 |
| RC4 首段大小 | `0x80` (128) | 384 |
| RC4 段大小 | `5120` | 385 |
| Mask/RC4 分界 | 派生密钥 `>300` 字节 | 646 |
| EncV2 前缀 | `b"QQMusic EncV2,Key:"` | 553 |
| TEA delta / 轮数 | `0x9E3779B9` / `32` | 475、479、505 |
| TEA Salt / Zero 长度 | `2` / `7` | 494-495 |
| tan 表 salt / 长度 | `106` / `8` | 578 |
| 文件名截断 | `150` | 804 |

### cue_splitter.py
| 常量 | 值 | 行号 |
|---|---|---|
| 帧率 | `75.0` fps | 55 |
| 编码回退顺序 | utf-8 → gbk → cp936 → utf-16 → latin-1 | 69 |
| 无 FILE 时探测扩展 | `.flac .ape .wav .wv .tta .mp3 .m4a` | 188 |
| 默认输出目录 | CUE 所在目录 | 244-245 |
| 命名模板 | `f"{track.number:02d} - {safe_title}.flac"` | 272 |
| FLAC 压缩等级 | `5` | 289 |
| ffmpeg 探测 | `ffmpeg -version`，`check=True` | 250 |
| ffmpeg 错误日志级别 | `-hide_banner -loglevel error -y` | 276 |
| **超时** | **无** | 310 |
| 库级 ffmpeg 转码超时（对比） | `600` 秒 | tidy_engine.py:4083 |
| 幂等跳过正则 | `^\d{2}\s*-` + `.flac` 计数 | tidy_engine.py:3748 |

---

## 11. 可借鉴要点清单（面向 Go 零第三方依赖 + Vue 播放器）

### 11.1 直接值得抄的做法

1. **魔数嗅探做输出校验**（`converter.py:681-706`）：解密后先验容器再落盘，且用真实容器修正扩展名。Go 里就是一个 `switch` 判前 12 字节，零依赖，务必移植。
2. **"无密钥宁可报错"的产品决策**（`converter.py:604`、`721-729`）：STag/V2 无密钥时明确提示"需从客户端导出密钥"，而不是产出噪声文件。前端可以据此给出可操作提示。
3. **内建自检 + HTTP 自检接口**（`converter.py:860-929`、`music_tidy.py:1059-1066`）：AES 用 FIPS-197 官方向量，TEA 用往返，QMC 用"确定性伪随机明文 + XOR 对称反向造密文"构造端到端向量（`_qmc_st_plain/_qmc_st_build`，`836-857`）。**这套自造测试向量的思路在 Go 里可以 1:1 复刻**，是跨语言实现互验的利器（用 Python 侧生成 ekey 向量，Go 侧断言明文）。
4. **元数据/封面/歌词三段独立容错**：元数据解析失败不影响出音频（`249-250`），封面缺失返回 `None`，歌词解码用 `replace`。Go 侧应同样分层。
5. **歌词/封面落 sidecar 再入库**（`music_tidy.py:1033-1051`）：`{同名}.jpg` + `{同名}.lrc`，且注释强调"须在 ingest 前"。顺序依赖在 Go 里同样成立，别写反。
6. **成功才删源密文、失败保留**（`tidy_engine.py:4063-4066`）。
7. **幂等重跑**（`tidy_engine.py:3745-3754`）：按 `^\d{2}\s*-.*\.flac` 计数跳过已拆 CUE。Go 侧同样应可重复执行。
8. **三态结果**：`sniffed is None`（失败）、`sniffed != 映射`（纠正）、一致（直通）——比"真假"二值更有信息量，适合映射到 API 的 `status/fmt/sniffed` 字段。

### 11.2 必须改进的坑（Go 版别照抄）

1. **无原子写**：Python 直接 `open(out_path,"wb")`。Go 应 `os.CreateTemp(dir, ".part-*")` → 写 → `Sync` → `os.Rename`，并处理跨设备 `EXDEV`。`tidy_engine.py:4077-4086` 的 `.__tc__.` + `os.replace` 才是正确姿势。
2. **无并发但有线程化 HTTP**：转换应显式加 workqueue/信号量（如 2–4 并发），而不是靠"没人同时点"。Go 用 `errgroup`+`semaphore`（标准库 `sync`/`context` 足够，零第三方）。
3. **CUE 的 `subprocess` 无超时**：Go 用 `exec.CommandContext` + `context.WithTimeout`（建议 15–30 分钟/轨或按文件大小动态），并在超时后 `Process.Kill()`。
4. **`-to` 语义用错方向**：Go 侧改为显式 `-ss <start> -t <duration>`，`duration = next.start - start`（末轨用 `-t` 省略），可读且不受 timestamp 语义影响。
5. **封面丢失**：拆分命令追加 `-map 0:a:0 -map 0:v? -c:v copy -disposition:v attached_pic`，或先 `ffmpeg -i src -an -c:v copy cover.jpg` 再以 `-i cover.jpg -map 1 -c:v mjpeg -disposition:v attached_pic` 注入。否则整轨封面在拆分后 100% 丢失。
6. **CUE 不支持 PREGAP/INDEX 00/多 FILE**：若要兼容老碟，至少把 `INDEX 00` 与 `PREGAP` 解析进来（`pregap` 会改变首轨 0 点），并支持每轨独立 `FILE`（现场专辑常见）。
7. **BOM 与无 BOM UTF-16**：Go 无 `chardet`。方案：先剥 `EF BB BF`/`FF FE`/`FE FF`；UTF-8 校验用 `utf8.Valid`；GBK/CP936 零第三方需**自带码表**（内嵌生成好的 GBK→Unicode 映射，约 2 万项，可用 `//go:embed` 压缩数组）。若不愿带表，产品层面提示用户"请另存为 UTF-8"。
8. **`latin-1` 兜底让乱码静默通过**：Go 侧应保留"最后手段"但打 warning 标记，让 UI 能显示"编码可能不对"。
9. **命名清理不足**：Go 需补 Windows 保留名（CON/PRN/AUX/NUL/COM1-9/LPT1-9）、尾随空格/点、保留名带扩展名的情形，并在 macOS 上考虑 Unicode NFD 归一化。
10. **`_safe_filename` 截断 150 会截断多字节**：Go 应按 rune 截断，避免非法 UTF-8 文件名。
11. **`_xor_bytes` 大整数技巧**：Go 直接 `for i := range buf { buf[i] ^= keybox[i&0xff] }`（或对 256 字节块循环 `subtle.XORBytes`），别学大整数。

### 11.3 Go 实现 NCM/QMC 解密的具体注意事项（重点）

**通用**
- **ECB 要自己拼**：Go 标准库没有 ECB 模式。`block, _ := aes.NewCipher(key)` 后手动每 16 字节 `block.Decrypt(dst[i:i+16], src[i:i+16])`。这是唯一"Python 手写、Go 反而简单"的地方——但**不要**用 `cipher.NewCBCDecrypter` 之类的替代，NCM 明确是 ECB。
- **PKCS#7 自己做**：Go 无 `pkcs7` 标准库；校验 `pad` 在 1..16 且所有填充字节相同（对齐 Python `pkcs7_unpad` 的严格度，否则行为不一致）。
- **base64 严格性差异**：Python `base64.b64decode` 默认容忍缺失 padding、且丢弃非法字符（`validate=False`）；**Go `encoding/base64.StdEncoding` 是严格的**。QMC 尾部密钥、EncV2 解出的 base64、NCM meta 的 base64 都可能缺 `=` 或含换行。做法：先 `base64.StdEncoding.DecodeString`，失败退 `RawStdEncoding`，再失败可先剥 `\r\n` 并把 `-`/`_` 归一为 `+`/`/`。
- **字节序别混**：NCM 全部小端（`encoding/binary.LittleEndian`）；QTag 的 `key_size` 是**大端**；旧式尾部长度是**小端**；TEA 块用**大端** `>IIII`/`>II`。
- **整数溢出语义**：TEA/hash 依赖 uint32 回绕，Go 的 `uint32` 天然回绕（Go 1.13+ 有 `math/bits` 但不需要）；不要用 `int` 再取模，直接 `uint32`。
- **浮点截断**：`_get_segment_key` 是 `float64( hash / ((id+1)*seed) * 100 )` 再取整。Go 里 `uint32(float64)` 对 **NaN/负数/超范围是未定义行为**，必须先像 Python 一样显式判 `math.IsNaN` 并 clamp，否则不同架构结果不同。`seed==0` 时 Python 置 1，Go 同样处理。
- **`_rotate` 不是循环移位**：`r := (bits+4)%8; return uint8((v<<r)|(v>>r))`——两个方向都是逻辑移位、结果 OR。写成 `bits.RotateLeft8` 会全错。
- **QMC 静态掩码边界**：`o <= 0x7fff → pre[o]`，`o > 0x7fff → pre[o % 0x7fff]`。Go 里建议直接实现 `maskAt(o int) byte` 一次判定（慢一点但不会错），或照抄 Python 的分段快路径（前 0x8000 用表，之后周期 `pre[1:0x7fff]+pre[0]`，周期长 32767）。**别写成 `o % 0x8000`**——这是最常见的移植错误。
- **QMC RC4 的 S 盒长度 = len(key)**，取模基数是 `len(key)` 而不是 256；`skip_len` 逻辑必须在 Go 里逐行对照（`i` 从负值起步、只有 `i>=0` 才写）。
- **NCM keybox 不是 RC4 输出**：KSA 同 RC4，但 256 字节输出用的是 `box[(i+1)]`、`box[(i+1+si)]`、`box[(si+sj)]` 三跳查表。用 `crypto/rc4` 会直接失败。
- **EncV2 双 TEA-CBC**：`_TEA_SALT_LEN=2`、`_TEA_ZERO_LEN=7`、`rounds=32`（不是标准 64）；Zero 校验失败要报错而不是继续。这两处轮数与长度是硬编码，务必写常量并加注释。
- **tan 表**：`int(math.Trunc(math.Abs(math.Tan(106 + float64(i)*0.1)) * 100)) & 0xff`。Go 的 `math.Tan` 与 C/Python 的 libm 可能有 1 ulp 差异，极端情况下 `*100` 后 `Trunc` 会跨整数边界。稳妥做法：**把 8 个字节的表直接内联成常量数组**（`[8]byte`），不要运行时计算。这是零依赖且最稳的选择。
- **大文件内存**：Python 把整文件读进内存，QMC 还要整份拷成 `bytearray` 再全量 XOR（峰值 ≈ 2–3×文件大小）。Go 侧建议：只读文件尾 4+8 字节解析密钥结构 → `io.NewSectionReader`/`Seek` 定位音频区 → 用 `bufio` 分块（如 1 MiB）流式 XOR 到临时文件 → rename。RC4 的 `offset` 只需按块推进即可（`proc(buf, offset)` 本来就是流式语义）。注意 QTag 解析仍需读尾部约 `key_size+8` 字节。
- **跨语言自验**：保留 Python 的 `_QMC_ST_EKEY_*` 向量（`converter.py:814-833`）作为 pytest/golden 数据；Go 侧建议同时放一份"解密后用 `sniff_audio_fmt` 判容器 + 与 Python 输出逐字节 diff"的测试。QMC 用 XOR 对称（`_qmc_st_build` 注释 `851`）构造密文，< 100 行就能造出互验用例。
- **安全**：这些 key 是公开的解锁社区常量，不是隐私；但 Go 服务端不要把 ekey/song_id 记进日志（`song_id` 会暴露用户曲库），Python 侧保留了 `song_id` 字段（`622`），移植时建议丢弃或哈希。

### 11.4 Vue 侧接口建议（基于 Python 实现推导）

`/api/convert`（`music_tidy.py:1017-1058`）的输入输出可直接映射：

```jsonc
// POST /api/convert
{ "files": ["/path/a.ncm"], "out_dir": "/music/in", "auto_tidy": true }

// 200
{ "ok": true, "results": [
  { "ok": true, "out_path": "...", "fmt": "flac",
    "music_name": "歌手 - 歌名", "has_image": true, "has_lyric": true,
    "meta": {...}, "auto_tidy": true },
  { "ok": false, "error": "该文件未内嵌解密密钥（STag 格式）…" }
]}
```

前端可借鉴的展示点：
- 逐文件结果卡片（成功/失败混排），失败直接展示 `error` 原文——Python 的错误文案已经区分"无密钥/密钥不符/格式不支持/文件损坏"，前端可据此给"去客户端导出密钥""重试""跳过"三种动作。
- `has_image`/`has_lyric` 决定是否显示 sidecar 徽标。
- `auto_tidy` 可开关（上传接口 `/api/convert_upload` 有 `auto_tidy` 字段，`music_tidy.py:490`）。
- 上传接口固定把文件放 `DATA_DIR/uploads/YYYYmmdd_HHMMSS/`（`478-479`）并**成功后删除上传密文**（`526-530`），前端需知道"临时上传目录可能被清空"。
- CUE 拆分应放到"长任务 + 轮询任务状态"模型（`/api/library/split-cue` → 立即返回 `{"task":"split_cue"}`，`music_tidy.py:944-946`），而不是同步等待；且同一时刻只允许一个库任务（409）。
- 自检接口 `/api/convert_selftest`（`1059-1066`）非常适合做成设置页的"解密能力自检"按钮。
