#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
scraper.py — 曲库刮削模块
从文件名解析歌曲信息，并从多个在线曲库（网易云 / QQ音乐）抓取
歌词(LRC)、封面、标题/歌手/专辑等元数据。

设计原则：
- 全部使用 Python 标准库（urllib），不依赖第三方包，保证在 fnOS 裸 Python 运行时可用。
- 多源降级：网易云 -> QQ音乐 -> 返回空结果。
- 所有网络调用都带超时、异常兜底，失败不影响主流程。
- 尊重来源接口的公开行为，接口变化时仅影响本模块，不影响其他功能。
"""

import base64
import hashlib
import json
import os
import re
import shlex
import time
import urllib.parse
import urllib.request

# 支持的音频扩展名
AUDIO_EXTS = {
    ".mp3", ".flac", ".wav", ".m4a", ".aac", ".ogg",
    ".ape", ".wv", ".dsf", ".dff", ".opus", ".aiff", ".aif", ".alac",
    ".wma", ".mka", ".m4b", ".m4r", ".tak", ".tta", ".ac3", ".eac3", ".dts",
    # 部分曲库还会遇到的容器（v1.9.0 补齐，避免这些文件被当成非音频漏掉）
    ".oga", ".mpc", ".shn", ".caf", ".la", ".aifc", ".w64", ".rf64",
}

# 伴生文件：封面 / 歌词 / 列表 / 视频 / 文档 / 压缩包 / 系统垃圾。它们设计上就不入库，
# 单独归档之后，「未视为音频的扩展名统计」里剩下的才是真正需要看一眼的东西——
# 旧版 .jpg/.lrc/.cue 会把前 15 名占满，把 .ncm 这类加密容器挤得完全看不见。
SKIP_EXTS = {
    ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp", ".tiff", ".tif", ".ico", ".heic",
    ".lrc", ".txt", ".md", ".srt", ".ass", ".ssa", ".vtt", ".json", ".xml", ".csv",
    ".cue", ".log", ".m3u", ".m3u8", ".pls", ".nfo", ".ini", ".db", ".url", ".lnk",
    ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".mpg", ".mpeg", ".ts",
    ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".epub",
    ".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz",
}

# 本应用「格式转换」页能解密的加密容器（与 converter._QMC_EXT_MAP / _NCM_EXTS 同步）。
# 单独报一类，是为了让“为什么这些歌没扫进去”有直接答案：先解密，再扫描。
CONVERT_EXTS = {
    ".ncm", ".mgg", ".mgg0", ".mggl", ".mgg1", ".mflac", ".mflac0", ".mmp4",
    ".qmcflac", ".qmcogg", ".qmc0", ".qmc2", ".qmc3", ".qmc4", ".qmc6", ".qmc8",
    ".bkcmp3", ".bkcm4a", ".bkcflac", ".bkcwav", ".bkcape", ".bkcogg", ".bkcwma",
    ".tkm", ".666c6163", ".6d7033", ".6f6767", ".6d3461", ".776176",
}

DEFAULT_TIMEOUT = 12
USER_AGENT = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/120.0 Safari/537.36"
)

# ---------------------------------------------------------------------------
# 文件名解析
# ---------------------------------------------------------------------------

# 常见命名模式：
#   artist - title
#   artist - title (Live)
#   artist - title [flac]  /  artist - title - 无损 等后缀噪音
#   title  (无歌手)
#   track-01 之类无法解析的乱名

def _clean_token(s: str) -> str:
    s = s.strip()
    # 去掉方括号/圆括号里的音质/格式/来源标记
    s = re.sub(r"[\[\(（【].*?[\]\)）】]", "", s)
    # 去掉常见后缀噪音
    s = re.sub(r"(无损|Hi-Res|FLAC|MP3|320K|24bit|96k|WAV|DSD|单曲|专辑|live|Live|remastered|acoustic)", "", s)
    s = re.sub(r"[._\-]+$", "", s)
    return s.strip(" .-_—–")


# ---------------------------------------------------------------------------
# 分隔符与「歌单前缀」噪音（v1.13.0）
#   批量从歌单导出的文件，名字头部挂的是「这来自哪个歌单」而不是歌手：
#   好听的歌曲推荐-下山 - 麦小兜.mp3
# 旧版取第一个连字符硬切，切出歌手=好听的歌曲推荐 / 歌名=下山 - 麦小兜，两头都错。
# 现在按「带空白的分隔符才是字段边界」切，再剥掉首尾的歌单噪音段。
# ---------------------------------------------------------------------------

_DELIM_RUN = re.compile(r"[-—–|丨]+")
_STRONG_DELIM_CHARS = "—–|丨"

# 整段歌单噪音的判定保守：去掉符号后 3~12 字，且以这些结尾词收尾才算。
_NOISE_TAILS = ("推荐", "歌单", "合集", "精选", "串烧", "榜单", "排行榜", "热歌",
                "新歌", "金曲", "老歌", "情歌", "神曲", "歌曲", "音乐", "bgm", "榜")
# 段首的年份/音质标签（2024、2024年、320k）同样不是歌手也不是歌名，可跟噪音一起跳
_JUNK_TAG = re.compile(r"^(?:(?:19|20)\d{2}年?|\d{2,3}k|hi-?res|flac|mp3|wav|ape|无损|高音质)$")
# 看着像噪音但其实是有用取值（版本标记/曲风），不许当歌单名前缀剥掉
_NOT_NOISE = {"纯音乐", "伴奏", "轻音乐", "音乐剧", "原声带"}
# 上级目录名是这些通用词时，不能拿来当歌手线索
_GENERIC_DIR = re.compile(
    r"^(?:music|audio|歌曲|音乐|下载|未整理|未分类|待整理|全部|新歌|收藏|新建文件夹|"
    r"mp3|flac|wav|无损|\d{1,4})$", re.I)


def _norm_chunk(s: str) -> str:
    """比较用归一化：只留中英文与数字，丢掉空白/括号/连接符。"""
    return re.sub(r"[^0-9a-zA-Z\u4e00-\u9fff]+", "", str(s or "").lower())


def _is_noise_seg(s: str) -> bool:
    """整段是不是「歌单名/推荐语」而不是歌手或歌名。"""
    t = _norm_chunk(s)
    if len(t) < 3 or len(t) > 12 or t in _NOT_NOISE:
        return False
    return any(t.endswith(w) for w in _NOISE_TAILS)


def _is_head_junk(s: str) -> bool:
    """段首是不是年份/音质这类可跳过的标签（只用于剥前缀，尾部不动）。"""
    return bool(_JUNK_TAG.match(_norm_chunk(s)))


def _split_fields(s: str):
    """把名字切成候选字段。

    带空白的分隔符（` - `）与中文破折号/竖线算「歌手↔歌名」边界；夹在词里的
    紧连字符（`推荐-下山`）不算。一个强边界都没有时，退回按第一个连字符切，
    保持对 a-b-c 这类命名的旧行为不变。
    """
    spans = list(_DELIM_RUN.finditer(s))
    if not spans:
        return [s]

    def _strong(m):
        if any(c in _STRONG_DELIM_CHARS for c in m.group(0)):
            return True
        a, b = m.span()
        return (a > 0 and s[a - 1].isspace()) or (b < len(s) and s[b].isspace())

    picked = [m for m in spans if _strong(m)] or spans[:1]
    out, prev = [], 0
    for m in picked:
        out.append(s[prev:m.start()])
        prev = m.end()
    out.append(s[prev:])
    return out


def _strip_noise_seg(seg: str):
    """剥掉一段首尾的歌单噪音，返回 (正文, 是否剥过)。

    处理前缀与正文被紧连字符合成一段的情况（`好听的歌曲推荐-下山` → `下山`）。
    re.split 带捕获组，奇数位是分隔符原文，剥完按原文拼回，不会改动正文里的符号。
    永不剥到空：只剩一段时原样返回。
    """
    parts = re.split(r"([-—–|丨]+)", seg)
    hit = False
    while len(parts) >= 3 and (_is_noise_seg(_clean_token(parts[0])) or _is_head_junk(parts[0])):
        del parts[:2]
        hit = True
    while len(parts) >= 3 and _is_noise_seg(_clean_token(parts[-1])):
        del parts[-2:]
        hit = True
    return "".join(parts).strip(), hit


def _dir_artist_hint(path: str) -> str:
    """取上级目录名当歌手线索（按歌手建目录是最常见的库结构）。纯文件名入参返回空。"""
    p = str(path or "")
    d = os.path.dirname(p)
    if not d or d == p:
        return ""
    name = os.path.basename(os.path.normpath(d)).strip()
    if not name or len(name) > 20 or _GENERIC_DIR.match(name):
        return ""
    return name


def llm_meta_sane(raw_name: str, artist: str, title: str) -> bool:
    """校验大模型给的歌手/歌名确实来自文件名，防止它凭空另认一首（张冠李戴）。

    归一化后要求歌名是文件名的子串；歌手给了就必须也是。全角括号、《》、
    连字符这类符号在归一化时被丢掉，所以模型加不加书名号都不影响。
    """
    stem = os.path.splitext(os.path.basename(strip_tidy_code(str(raw_name or ""))))[0]
    hay = _norm_chunk(stem)
    t = _norm_chunk(title)
    a = _norm_chunk(artist)
    if not t or t not in hay:
        return False
    if a and a not in hay:
        return False
    return bool(a or not artist)


# ---------------------------------------------------------------------------
# 全维度代码标注（部分曲库命名规范）：
#   [Y3-S03-E03-C04-C01-V00]
#   Y=发行年份(Y0=2020及之前，Y1=2021...Y6=2026)
#   S=戏腔风格 S01-S08；E=情绪 E01-E08；C=场景(主+次两个) C01-C07；V00=原唱 Z00=翻唱
# ---------------------------------------------------------------------------

_TIDY_CODE_RE = re.compile(
    r"[\[（(]\s*(Y\d)-([SsEeCc]\d{1,2})-([SsEeCc]\d{1,2})-([SsEeCc]\d{1,2})-([SsEeCc]\d{1,2})-([VvZz]\d{2})\s*[\]\)）]")

STYLE_MAP = {
    "S01": "柔情抒情古风", "S02": "江湖侠气古风", "S03": "戏曲戏腔国风",
    "S04": "DJ/Remix改编", "S05": "禅意清雅国风", "S06": "大气磅礴国风",
    "S07": "国风说唱", "S08": "民谣国风",
}
EMOTION_MAP = {
    "E01": "温婉柔情", "E02": "潇洒豪迈", "E03": "伤感离愁", "E04": "治愈舒缓",
    "E05": "燃向力量", "E06": "轻快灵动", "E07": "情绪E07", "E08": "恢弘大气",
}
SCENE_MAP = {
    "C01": "日常循环", "C02": "车载出行", "C03": "睡前静心", "C04": "古风BGM",
    "C05": "运动健身", "C06": "办公学习", "C07": "汉服活动",
}
VERSION_MAP = {"V00": "原唱", "Z00": "翻唱"}


def parse_tidy_code(text: str) -> dict:
    """从文件名提取并解析全维度代码，返回
    {code, year, style, emotion, scene1, scene2, version}，无代码返回 {}。"""
    m = _TIDY_CODE_RE.search(text or "")
    if not m:
        return {}
    y, s, e, c1, c2, v = [g.upper() for g in m.groups()]
    year = "2020及之前" if y == "Y0" else str(2020 + int(y[1:]))
    return {
        "code": "-".join([y, s, e, c1, c2, v]),
        "year": y, "year_label": year,
        "style": s, "style_label": STYLE_MAP.get(s, s),
        "emotion": e, "emotion_label": EMOTION_MAP.get(e, e),
        "scene1": c1, "scene1_label": SCENE_MAP.get(c1, c1),
        "scene2": c2, "scene2_label": SCENE_MAP.get(c2, c2),
        "version": v, "version_label": VERSION_MAP.get(v, v),
    }


def strip_tidy_code(name: str) -> str:
    return _TIDY_CODE_RE.sub("", name or "")


def parse_filename(filename: str) -> dict:
    """从文件名解析出 {artist, title, code, suspect}。

    兼容命名：
      001.歌名 - 歌手 [Y0-S08-E03-C04-C01-V00].wav   （古风库：歌名在前）
      歌名 - 歌手 / 歌手 - 歌名 .mp3                  （两种顺序均可能）
      好听的歌曲推荐-下山 - 麦小兜.mp3                （头部歌单前缀 + 正文）
      008-2023年 等目录层级不参与解析
    左右两侧谁是歌手无法纯规则确定时，返回 left/right 供上层用曲库搜索消歧；
    规则自己没把握（剥过歌单前缀、或多于两个字段）时置 suspect=True，
    上层据此再请大模型判一次（v1.13.0）。
    """
    code = parse_tidy_code(filename)
    base = os.path.splitext(os.path.basename(strip_tidy_code(filename)))[0]
    # 头部的【抖音热歌】/(精选) 这类括号标签：先抓下来，它贴着哪一段就说明那一段是歌名
    mh = re.match(r"^\s*(?:[\[\(（【][^\]\)）】]*[\]\)）】]\s*[-—–|丨]?\s*)+", base)
    tag_head_noise = bool(mh) and any(
        _is_noise_seg(x) for x in re.split(r"[^0-9a-zA-Z\u4e00-\u9fff]+", mh.group(0)) if x)
    # 摘掉括号里的注释（(Live) / [flac] / 【抖音热歌】），里面的连字符不是字段分隔符
    base = re.sub(r"[\[\(（【][^\]\)）】]*[\]\)）】]", " ", base).strip()
    if not base:
        return {"artist": "", "title": "", "code": code.get("code", ""), "suspect": False}

    # 轨道号前缀：001. / 045- / 12_ 等
    def _strip_track(s):
        return re.sub(r"^\d{1,3}\s*[._、\-\s]\s*", "", s)

    empty = {"artist": "", "title": "", "code": code.get("code", ""), "suspect": False}
    raw = _split_fields(base)
    if raw:
        raw[0] = _strip_track(raw[0])    # 轨道号只可能在最前，右段不能洗（「50 英里」会被洗成「英里」）
    fields = [f for f in (_clean_token(x) for x in raw) if f]
    if not fields:
        return empty

    suspect = False
    # 1) 头部/尾部的整段歌单噪音：丢掉，但至少留一段正文（全剥完宁可不剥）
    while len(fields) > 1 and _is_noise_seg(fields[0]):
        fields.pop(0)
        suspect = True
    while len(fields) > 1 and _is_noise_seg(fields[-1]):
        fields.pop()
        suspect = True
    # 2) 前缀与正文被紧连字符合成了一段（`好听的歌曲推荐-下山`）：段内再剥一次
    glued_left = glued_right = False
    for idx in (0, len(fields) - 1):
        body, hit = _strip_noise_seg(fields[idx])
        if hit and body:
            fields[idx] = _clean_token(body)
            suspect = True
            if idx == 0:
                glued_left = True
            else:
                glued_right = True
    if len(fields) > 2:
        suspect = True                     # 三段以上：取哪一段当歌手本身就歧义
    if not fields[0]:
        return empty

    left = fields[0]
    right = " - ".join(fields[1:])
    if right:
        # 带全维度代码的曲库统一「歌名 - 歌手」；普通文件默认「歌手 - 歌名」，
        # 上层可用 resolve_and_match 按曲库搜索结果消歧。
        artist_is_left = not code
        ln, rn = _norm_chunk(left), _norm_chunk(right)
        # 上级目录名是最便宜的歌手线索：目录正好等于其中一段时直接定序，不用猜、
        # 也不用调模型。
        hint = _norm_chunk(_dir_artist_hint(filename))
        decided = bool(code)                     # 代码库的命名约定比启发式更硬
        if hint and not decided:
            if rn and hint == rn and hint != ln:
                artist_is_left, decided, suspect = False, True, False
            elif ln and hint == ln and hint != rn:
                artist_is_left, decided, suspect = True, True, False
        if not decided and (glued_left or tag_head_noise) and not glued_right:
            # 歌单前缀是紧贴着歌名写的（好听的歌曲推荐-下山 / 【抖音热歌】告白气球），
            # 被修饰的那段就是歌名，歌手在另侧
            artist_is_left = False
            suspect = True
        artist, title = (left, right) if artist_is_left else (right, left)
        return {"artist": artist, "title": title, "left": left, "right": right,
                "code": code.get("code", ""), "suspect": suspect}

    # 只有一段：歌单名就是它的全部，剩下当歌名（歌手空，交给上层搜索/大模型补）
    m2 = re.match(r"^\s*(?:[0-9]{1,3}\s*[-._、]?\s*)(?P<title>.+)$", left)
    if m2:
        left = _clean_token(m2.group("title")) or left
    if left and not re.match(r"^[\d\W_]+$", left):
        return {"artist": "", "title": left, "code": code.get("code", ""),
                "suspect": suspect or _is_noise_seg(left)}

    return empty


# ---------------------------------------------------------------------------
# 自定义名称解析规则（设置页「AI 分析规则」确认后写入 config.name_rules）
# 规则形如 {id, sample, desc, regex, enabled}；regex 为 Python 命名分组正则，
# 组名取 artist/title/album/track，命中即显式确定字段，不再走左右歧义消解。
# ---------------------------------------------------------------------------

_RULE_FIELDS = ("artist", "title", "album", "track")


def normalize_rule_regex(rx: str) -> str:
    """把 JS 风格命名分组 (?<name>) 归一为 Python 的 (?P<name>)。"""
    return re.sub(r"\(\?<([A-Za-z_][A-Za-z0-9_]*)>", r"(?P<\1>", rx or "")


def test_rule_regex(rx: str, sample: str) -> dict:
    """校验规则正则并给出对样例名的解析结果。

    返回 {ok, error, regex, parsed}；regex 为归一化后的正则，
    ok=True 时 parsed 至少含非空 artist 或 title。
    """
    rx = normalize_rule_regex(rx)
    if not rx:
        return {"ok": False, "error": "规则为空", "regex": "", "parsed": {}}
    try:
        pat = re.compile(rx)
    except re.error as e:
        return {"ok": False, "error": f"正则语法错误：{e}", "regex": rx, "parsed": {}}
    base = os.path.basename(str(sample or "").strip().strip('"'))
    if not base:
        return {"ok": False, "error": "样例文件名为空", "regex": rx, "parsed": {}}
    stem = os.path.splitext(base)[0]
    m = pat.search(base) or pat.search(stem)
    if not m:
        return {"ok": False, "error": "规则无法匹配样例文件名", "regex": rx, "parsed": {}}
    gd = {k: (v or "").strip(" .-_—–")
          for k, v in (m.groupdict() or {}).items() if k in _RULE_FIELDS}
    if not (gd.get("artist") or gd.get("title")):
        return {"ok": False, "error": "规则未提取到歌手或歌名", "regex": rx, "parsed": {}}
    return {"ok": True, "error": "", "regex": rx, "parsed": gd}


def apply_name_rules(path: str, rules) -> dict:
    """按自定义规则解析文件名；无任何规则命中返回 None（交回内置逻辑）。"""
    base = os.path.basename(str(path or ""))
    for r in rules or []:
        if not isinstance(r, dict) or r.get("enabled") is False:
            continue
        t = test_rule_regex(r.get("regex") or "", base)
        if not t["ok"]:
            continue
        gd = t["parsed"]
        code = parse_tidy_code(base)
        return {
            "artist": gd.get("artist", ""),
            "title": gd.get("title", ""),
            "album": gd.get("album", ""),
            "code": code.get("code", ""),
            "rule_id": str(r.get("id") or ""),
        }
    return None


def audio_ext(path_or_name: str) -> str:
    """取小写扩展名（不触盘）。"""
    return os.path.splitext(path_or_name)[1].lower()


def is_audio_name(path_or_name: str) -> bool:
    """只看扩展名判断是否音频，不 stat。枚举热路径专用。"""
    return audio_ext(path_or_name) in AUDIO_EXTS


def is_audio_file(path: str) -> bool:
    """判断是否为受支持的音频文件（排除 .lrc/.jpg 等附属文件）。

    先比扩展名再 isfile：目录下绝大多数是封面/歌词/日志/cue，
    这个顺序能去掉九成以上的 stat 系统调用（NAS 上差距明显）。
    """
    if not is_audio_name(path):
        return False
    return os.path.isfile(path)


# 目录读不了的常见 errno -> 人话（含处置方向）。NAS 上绝大多数是 13/1（未授权），
# 网络共享掉线是 112/113/110，软链成环是 40 —— 从数字就能直接判出是哪一类。
WALK_ERRNO_HINT = {
    1: "权限不足（Operation not permitted）",
    2: "路径不存在（软链目标已删除 / 存储卷未挂载）",
    5: "I/O 错误（磁盘或网络共享异常）",
    13: "权限不足（这个目录没能被授权给本应用：先确认外层路径已列在飞牛「应用设置 → 授权目录」里，再在该目录右键 → 详细信息 → 权限 → 高级 → 启用继承父级权限）",
    20: "路径中某一段不是目录",
    21: "路径类型不符（该位置不是目录）",
    36: "路径名过长",
    40: "软链环引用（路径中出现循环链接）",
    62: "路径名过长（系统上限）",
    110: "连接超时（网络共享无响应）",
    111: "连接被拒绝（共享服务未运行）",
    112: "主机不可达（网络共享掉线）",
    113: "无路由到主机（挂载点已失效）",
}

WALK_KIND_LABEL = {
    "denied": "读不到的目录",
    "missing": "不存在/不可进入的根目录",
    "depth": "超深度被截断的目录",
    "link": "软链重复或成环被剪枝",
    "excluded": "命中排除规则的目录",
    "file": "音频扩展名却访问不到",
}

WALK_KIND_HINT = {
    "denied": "这个目录和它下面的全部歌曲都没被扫到；给它开「继承父级权限」后重扫即可",
    "missing": "到「目录」页确认路径，或在飞牛重新挂载对应存储卷",
    "depth": "调高设置页「扫描深度上限」后重扫即可扫到",
    "link": "同一批文件已在别处计入，通常无需处理",
    "excluded": "是排除规则主动跳过的，需要扫它就到设置页去掉该条",
    "file": "多为权限不足 / 软链失效 / 文件名非 UTF-8，重命名或授权后可入库",
}


def _errno_hint(err):
    """把 OSError 翻成「什么原因 + 大概怎么办」，认不出 errno 时退回 strerror。"""
    if err is None:
        return ""
    no = getattr(err, "errno", None)
    if no in WALK_ERRNO_HINT:
        return WALK_ERRNO_HINT[no]
    det = getattr(err, "strerror", None) or str(err)
    return f"{type(err).__name__}: {det}"


# ---------- 读不到的目录：到底缺哪一位权限 ----------
# 光说「errno=13 权限不足」用户没法动手。飞牛的「授权目录」是按那一条路径生效的，
# 授权之后由别的账号（SMB / 文件管理器 / 离线下载）新建的子目录按创建者 umask 落地，
# 外层授权不会补到它们身上 —— 于是父目录进得去、子目录被挡。要让人一步修对，
# 就得把「这个目录自己的权限位 + 属主 + 本应用是谁 + 卡在祖先链哪一层」摆出来。
def _mode_str(mode):
    """st_mode -> drwx------ 这种九位串（只看目录，前缀固定写 d）。"""
    try:
        return "d" + "".join(
            ("r" if mode & (4 << s) else "-") + ("w" if mode & (2 << s) else "-") +
            ("x" if mode & (1 << s) else "-") for s in (6, 3, 0))
    except Exception:
        return "?"


def _name_of(lookup, ident):
    """uid/gid -> 账号名。NAS 上这些 id 常是别的设备建的，容器里查不到同名账号就退回数字。"""
    if ident is None:
        return ""
    try:
        rec = lookup(ident)
    except Exception:
        return str(ident)
    return getattr(rec, "pw_name", None) or getattr(rec, "gr_name", None) or str(ident)


def app_run_user():
    """本应用的运行账号（privilege 里 run-as: package，实际就是当前进程 uid 对应的用户）。"""
    try:
        import pwd
        return pwd.getpwuid(os.getuid()).pw_name
    except Exception:
        return "music-tidy"


def _blocked_ancestor(path, upto=12):
    """从 path 往上找最浅一层进不去（缺 x）的目录；返回 "" 表示每一层都能进入。

    只看 x 位：祖先里某一层连遍历都不行时，单独放开被挡的那个目录也没用，必须从
    那一层起修 —— 所以取**最浅**（离根最近）的那个，而不是就近的那个。
    """
    try:
        cur = os.path.abspath(path)
    except Exception:
        return ""
    blocked = ""
    for _ in range(upto):
        try:
            if not os.access(cur, os.X_OK):
                blocked = cur
        except Exception:
            pass
        parent = os.path.dirname(cur)
        if not parent or parent == cur:
            break
        cur = parent
    return blocked


def perm_probe(path):
    """对一个读不进去的目录做一次取证：它自己的权限位、属主、本进程身份、卡在哪一层。

    全程只读（stat / access），任何一步失败都只是少一项信息，绝不抛异常 ——
    一个进不去的目录不该把整份体检结果带崩。now_ok 用来回答「用户已经修好了吗」。
    """
    out = {"dir": _spath(path), "mode": "", "uid": None, "gid": None,
           "owner": "", "group": "", "can_read": False, "can_x": False,
           "self_uid": None, "self_user": "", "self_gid": None, "self_groups": [],
           "blocked_at": "", "now_ok": False, "gone": False}
    try:
        out["self_uid"] = os.getuid()
        out["self_gid"] = os.getgid()
        out["self_user"] = app_run_user()
        try:
            out["self_groups"] = sorted(os.getgroups())
        except Exception:
            out["self_groups"] = []
    except Exception:
        pass
    try:
        st = os.stat(path)
        out.update({"mode": _mode_str(st.st_mode), "uid": st.st_uid, "gid": st.st_gid})
    except Exception as e:
        # 连 stat 都要靠祖先链的 x 位，走到这里说明挡的层在更上面
        out["stat_error"] = f"{type(e).__name__}"
    if out["uid"] is not None:
        try:
            import pwd
            out["owner"] = _name_of(pwd.getpwuid, out["uid"])
        except Exception:
            out["owner"] = str(out["uid"])
    if out["gid"] is not None:
        try:
            import grp
            out["group"] = _name_of(grp.getgrgid, out["gid"])
        except Exception:
            out["group"] = str(out["gid"])
    for k, flag in (("can_read", os.R_OK), ("can_x", os.X_OK)):
        try:
            out[k] = bool(os.access(path, flag))
        except Exception:
            out[k] = False
    out["now_ok"] = bool(out["can_read"] and out["can_x"]) and os.path.isdir(path)
    # 祖先进不去时 stat 也会失败，所以“看不到”只是「已删除」与「父目录没权限」的弱信号；
    # 措辞里保留“或在读不到的父目录里”，不谎报已删除。
    out["gone"] = not out["now_ok"] and not _path_visible(path)
    if not out["now_ok"] and not out["gone"]:
        out["blocked_at"] = _blocked_ancestor(path)
    return out


def _path_visible(path):
    """能 stat 到、或能列父目录看见它的名字，就算“这个路径在” —— 区分「被删了」与「没权限」。"""
    try:
        os.stat(path)
        return True
    except Exception:
        pass
    try:
        parent = os.path.dirname(path) or "."
        name = os.path.basename(path)
        with os.scandir(parent) as it:
            for e in it:
                if e.name == name:
                    return True
    except Exception:
        pass
    return False


def perm_brief(probe):
    """把取证结果压成一行给人看的话：缺哪一位、是谁的目录、卡在哪一层、怎么修。"""
    if not probe:
        return ""
    if probe.get("now_ok"):
        return "现在已可读，大概是刚授权/改过权限，重新扫描即可"
    if probe.get("gone"):
        return ("这个路径现在已经看不到了（已删除/改名，或它的父目录本身就进不去），"
                "重扫后这一条会自己消失")
    if probe.get("stat_error"):
        ba = probe.get("blocked_at") or ""
        return (f"连查看权限都做不到（{probe['stat_error']}），卡点在上一层：{ba} —— 要先把它的「遍历」放开"
                if ba else f"连查看权限都做不到（{probe['stat_error']}）")
    who = probe.get("owner") or ""
    uid = probe.get("uid")
    owner = f"{who}({uid})" if who and who != str(uid) else (str(uid) if uid is not None else "?")
    m = probe.get("mode") or "?"
    me = probe.get("self_user") or ""
    self_id = f"{me}({probe.get('self_uid')})" if me else str(probe.get("self_uid"))
    if len(m) == 10 and m[7] == "r" and m[9] == "x":
        # 权限位看着是给足了还被挡，那就不是 mode 的事：ACL / 挂载参数 / 上层目录在动手
        return f"权限位看着够（{m}，属主 {owner}）却仍被挡，多半是上层目录或挂载/ACL 限制"
    tail = "父目录能进、卡点就在这一层" if not probe.get("blocked_at") or \
        probe.get("blocked_at") == probe.get("dir") else f"卡点在 {probe.get('blocked_at')}"
    grp = probe.get("group") or str(probe.get("gid"))
    return (f"本目录权限 {m}、属主 {owner}、属组 {grp}，而本应用以 {self_id} 运行 → 没给它任何一位；"
            f"{tail}（外层授权不覆盖后来新建的子目录）")


def perm_fix_cmd(path, user=None):
    """给一条可以直接粘进 SSH 的修复命令：只追加 ACL，不覆盖用户原有的权限位。"""
    u = user or app_run_user() or "music-tidy"
    try:
        p = shlex.quote(_spath(path))
    except Exception:
        p = '"' + str(path).replace('"', '') + '"'
    return f"sudo setfacl -R -m u:{u}:rx {p}"


def _spath(p):
    """路径可能带非 UTF-8 字节（surrogate），直接进 JSON 会编码失败，先统一洗一遍。"""
    try:
        return str(p).encode("utf-8", "replace").decode("utf-8")
    except Exception:
        return repr(p)


# ---------- 目录浏览（「指定目录扫描」选范围用：只走目录树，不读文件内容） ----------
# 为什么对外用 token 而不是把路径原样回传、前端再发回来：Linux 上目录名可能带
# 非 UTF-8 字节，_spath() 洗过之后那些字节已经是 \ufffd 了，前端再把它发回来就
# 是**另一个**路径 —— 用户选的是 A、系统扫的是 B，还查不出为什么。所以定位一律用
# base64url(os.fsencode(path)) 的无损 token，洗过的字符串只用来显示。
def path_tok(p) -> str:
    """路径 -> URL 安全的无损 token（可直接进 query，不需再 percent-encode）。"""
    try:
        raw = os.fsencode(p)
    except Exception:
        raw = str(p).encode("utf-8", "surrogateescape")
    return base64.urlsafe_b64encode(raw).decode("ascii").rstrip("=")


def tok_path(tok):
    """token -> 路径（保留 surrogate）。非法 token 返回 None，绝不抛异常。"""
    if not tok:
        return None
    try:
        raw = base64.urlsafe_b64decode(str(tok) + "=" * (-len(str(tok)) % 4))
    except Exception:
        return None
    try:
        return os.fsdecode(raw)
    except Exception:
        return None


def list_subdirs(path, exclude=(), limit=300):
    """列 path 的一级子目录名（按名称排序），返回 (names, truncated)。

    不隐藏 . 开头的目录：walk_audio 也照它们扫，若这里藏起来，上一层的音频数
    就会比各子目录之和多出一块对不上的账。读不了的目录返回 ([], False)，
    弹窗照常渲染 —— 一个进不去的目录不该让整个浏览接口报错。
    """
    ex = set(exclude or ())
    out, trunc = [], False
    try:
        with os.scandir(path) as it:
            for e in it:
                try:
                    if not e.is_dir(follow_symlinks=True):
                        continue
                except OSError:
                    continue  # 连类型都判不出（权限不足），只能当没有
                if e.name in ex:
                    continue
                out.append(e.name)
                if len(out) >= limit:
                    trunc = True
                    break
    except OSError:
        return [], False
    out.sort(key=lambda s: s.lower())
    return out, trunc


def count_audio_tree(path, exclude=(), max_depth=12, budget=None):
    """统计子树内音频数（只 stat 不读内容），返回 (数量, 是否完整)。

    budget 是 [剩余条目数] 的列表（传引用，让同一层的兄弟目录共用一份预算）：
    一屏可能列出上百个子目录，不设预算的话点开弹窗就等于把整块盘扫一遍，
    NAS 会直接卡住。超预算时 complete=False，前端在数字前加「≈」，不假装是准数。
    """
    ex = set(exclude or ())
    n, complete = 0, True
    try:
        base_depth = os.path.normpath(path).count(os.sep)
    except Exception:
        return 0, False
    seen = set()
    for dirpath, dirnames, filenames in os.walk(path, followlinks=True,
                                                onerror=lambda e: None):
        try:
            rp = os.path.realpath(dirpath)
        except OSError:
            rp = dirpath
        if rp in seen:
            dirnames[:] = []  # 软链成环/重复，与 walk_audio 同一套剪枝口径
            continue
        seen.add(rp)
        if os.path.normpath(dirpath).count(os.sep) - base_depth > max_depth:
            dirnames[:] = []
            complete = False
            continue
        dirnames[:] = [d for d in dirnames if d not in ex]
        if budget is not None:
            cost = len(filenames) + 1
            if budget[0] < cost:
                budget[0] = 0
                complete = False
                break
            budget[0] -= cost
        for fn in filenames:
            if is_audio_name(fn) and os.path.isfile(os.path.join(dirpath, fn)):
                n += 1
    return n, complete


def walk_audio(roots, exclude=(), max_depth=12, should_stop=None, sample_limit=8,
               issue_limit=300, on_root=None, on_dir=None):
    """枚举 roots 下的音频文件，返回 (路径列表, 对账信息 audit)。

    为何需要 audit：旧实现只报「找到多少音频」，无法回答“为什么比飞牛少”。
    audit 里的 complete 更是安全门：只要本次枚举不完整（权限失败 /
    深度截断 / 中途停止 / 目录不存在），调用方就**不得**拿它去删入库记录，
    否则一次扫到一半的目录就会把另一半的库记录清空。

    实现要点：
      - 扩展名先判断再 isfile，避免对每个非音频文件都 stat；
      - 跟随软链但按 realpath 剪枝，防软链环无限递归与重复计入；
      - 读不了的目录、被截断的深层目录、扩展名是音频但访问失败的文件（权限 /
        软链失效）、非音频扩展名分别计数，便于定位漏扫原因。

    v1.9.2 起 audit["issues"] 逐条记录「哪个目录没扫出来、为什么、该怎么办」
    （kind/dir/code/reason/hint），超出 issue_limit 只累加 issues_more；
    on_root(info) 每扫完一个根目录回调一次，供任务上报进度。

    v1.9.3 起每个根目录的 info 也带齐构成计数（skip_known / encrypted /
    ext_skipped / no_ext / inaccessible），所以逐目录那一行的账也能算平：
    files = audio + skip_known + encrypted + ext_skipped + no_ext + inaccessible。

    v1.12.0 起支持 on_dir(info) 逐目录回调：大曲库枚举要几十秒到几分钟，
    旧版只有 on_root（整个根目录扫完才响一次），授权了一个大目录时进度条全程
    停在 0/1，看着像卡死。on_dir 每处理完一个目录就报一次当下的累计量，
    回调只读不写，抛任何异常都不影响枚举本身。dirs_by_depth 顺手记下每层
    子目录数，供“共多少个一级/二级文件夹、多少首歌”这种健康报表。

    文件构成（v1.9.3 补齐，让「文件数远大于音频数」本身可解释）：
        files_total = audio_total + inaccessible + skip_known
                      + encrypted_total + ext_skipped_total + no_ext
    其中 ext_skipped / encrypted 只留前 15 名（按数量倒序），所以总数另走
    ext_skipped_total / encrypted_total 两个计数器，不因为截断而算错。
    """
    ex = set(exclude or ())
    paths = []
    kinds = {}
    audit = {
        "complete": True, "stopped": False, "roots": [], "files_total": 0,
        "audio_total": 0, "ext_skipped": {}, "no_ext": 0, "inaccessible": 0,
        "inaccessible_samples": [], "errors": [],
        "issues": [], "issues_more": 0, "kinds": kinds, "inaccessible_files": [],
        "dirs_visited": 0, "max_depth": max_depth, "issue_limit": issue_limit,
        "skip_known": 0, "encrypted": {}, "encrypted_total": 0, "ext_skipped_total": 0,
        "dirs_by_depth": {},
    }
    tally = {}
    enc_tally = {}

    def _issue(kind, path, err=None):
        kinds[kind] = kinds.get(kind, 0) + 1
        if len(audit["issues"]) >= issue_limit:
            audit["issues_more"] += 1
            return
        audit["issues"].append({
            "kind": kind, "dir": _spath(path),
            "code": getattr(err, "errno", None),
            "detail": (getattr(err, "strerror", None) or "") if err is not None else "",
            "reason": _errno_hint(err) if err is not None else WALK_KIND_HINT.get(kind, ""),
            "hint": WALK_KIND_HINT.get(kind, ""),
        })

    for root in roots or []:
        info = {"root": root, "exists": False, "files": 0, "audio": 0,
                "errs": 0, "depth_cut": 0, "link_cut": 0, "excluded": 0,
                "dirs": 0, "issues": 0, "skip_known": 0, "encrypted": 0,
                "ext_skipped": 0, "no_ext": 0, "inaccessible": 0}
        audit["roots"].append(info)
        if not root or not os.path.isdir(root):
            info["exists"] = False
            audit["complete"] = False
            _issue("missing", root)
            info["issues"] = 1
            if on_root:
                try:
                    on_root(info)
                except Exception:
                    pass
            continue
        info["exists"] = True
        seen = set()
        try:
            seen.add(os.path.realpath(root))
        except OSError:
            pass
        stopped = False
        root_no = len(audit["roots"])   # 当前根目录是第几个（1 起）：info 已先 append

        def _progress(dirpath, depth=0, _info=info, _rn=root_no):
            """枚举进度回调：把当下的累计量递出去，失败绝不能把遍历带崩。

            _info/_rn 走默认参是防闭包晚了：整个循环里 info 与 root_no 每个根目录
            都会重新赋值，直接引用会拿到最后一个根目录的值。
            """
            if on_dir is None:
                return
            try:
                on_dir({"dir": _spath(dirpath), "dirs": audit["dirs_visited"],
                        "files": audit["files_total"] + _info["files"],
                        "audio": audit["audio_total"] + _info["audio"],
                        "root": _rn, "roots": len(audit["roots"]), "depth": depth})
            except Exception:
                pass

        def _onerror(err, _info=info):
            _info["errs"] += 1
            fn = getattr(err, "filename", None)
            if len(audit["errors"]) < sample_limit:
                audit["errors"].append(str(fn if fn is not None else err))
            _issue("denied", fn if fn is not None else err, err)
            _info["issues"] += 1

        for dirpath, dirnames, filenames in os.walk(root, followlinks=True,
                                                   onerror=_onerror):
            if should_stop is not None and should_stop():
                stopped = True
                break
            keep = []
            for d in dirnames:
                full = os.path.join(dirpath, d)
                if d in ex:
                    info["excluded"] += 1
                    _issue("excluded", full)
                    info["issues"] += 1
                    continue
                try:
                    rp = os.path.realpath(full)
                except OSError:
                    rp = full
                if rp in seen:
                    info["link_cut"] += 1
                    _issue("link", full)
                    info["issues"] += 1
                    continue
                seen.add(rp)
                keep.append(d)
            dirnames[:] = keep
            audit["dirs_visited"] += 1
            info["dirs"] += 1
            rel = os.path.relpath(dirpath, root)
            depth = 0 if rel == "." else rel.count(os.sep) + 1
            audit["dirs_by_depth"][depth] = audit["dirs_by_depth"].get(depth, 0) + 1
            if depth > max_depth:
                dirnames[:] = []
                info["depth_cut"] += 1
                _issue("depth", dirpath)
                info["issues"] += 1
                _progress(dirpath, depth=depth)   # 被截断的目录也算“我还活着”
                continue
            info["files"] += len(filenames)
            for fn in filenames:
                if not is_audio_name(fn):
                    e = audio_ext(fn)
                    if e in SKIP_EXTS:
                        # 封面/歌词/列表等伴生文件，设计上就不入库，单独计入
                        info["skip_known"] += 1
                        audit["skip_known"] += 1
                    elif e in CONVERT_EXTS:
                        info["encrypted"] += 1
                        audit["encrypted_total"] += 1
                        enc_tally[e] = enc_tally.get(e, 0) + 1
                    elif e:
                        info["ext_skipped"] += 1
                        audit["ext_skipped_total"] += 1
                        tally[e] = tally.get(e, 0) + 1
                    else:
                        info["no_ext"] += 1
                        audit["no_ext"] += 1
                    continue
                p = os.path.join(dirpath, fn)
                if os.path.isfile(p):
                    paths.append(p)
                    info["audio"] += 1
                else:
                    # 扩展名是音频但 stat 不到：权限不足 / 软链目标失效 / 编码异常
                    info["inaccessible"] += 1
                    audit["inaccessible"] += 1
                    if len(audit["inaccessible_samples"]) < sample_limit:
                        audit["inaccessible_samples"].append(p)
                    if len(audit["inaccessible_files"]) < issue_limit:
                        audit["inaccessible_files"].append(_spath(p))
                    _issue("file", p)
                    info["issues"] += 1
            _progress(dirpath, depth=depth)
        if stopped:
            audit["stopped"] = True
            audit["complete"] = False
        if info["errs"] or info["depth_cut"]:
            audit["complete"] = False
        audit["files_total"] += info["files"]
        audit["audio_total"] += info["audio"]
        if on_root:
            try:
                on_root(info)
            except Exception:
                pass
    audit["ext_skipped"] = dict(sorted(tally.items(), key=lambda kv: -kv[1])[:15])
    audit["encrypted"] = dict(sorted(enc_tally.items(), key=lambda kv: -kv[1])[:15])
    # 软链剪枝不影响完整性：被剪掉的目录已经在另一处计入
    return paths, audit


# ---------------------------------------------------------------------------
# HTTP 工具
# ---------------------------------------------------------------------------

def _decode_bytes(raw: bytes, content_type: str = "") -> str:
    """按 charset 头 -> UTF-8 -> GBK 顺序解码。
    QQ 音乐部分接口返回 GBK 编码，直接 utf-8+errors=replace 会产生乱码字符。"""
    m = re.search(r"charset=([\w\-]+)", content_type or "", re.I)
    cands = []
    if m:
        cands.append(m.group(1))
    cands += ["utf-8", "gbk", "gb18030"]
    for enc in cands:
        try:
            return raw.decode(enc)
        except (UnicodeDecodeError, LookupError):
            continue
    return raw.decode("utf-8", errors="replace")


def _http_json(url: str, data=None, headers=None, timeout=DEFAULT_TIMEOUT):
    """GET/POST 请求并解析 JSON。失败抛异常，由调用方兜底。"""
    req_headers = {"User-Agent": USER_AGENT}
    if headers:
        req_headers.update(headers)
    if data is not None:
        body = data if isinstance(data, bytes) else urllib.parse.urlencode(data).encode("utf-8")
        req = urllib.request.Request(url, data=body, headers=req_headers, method="POST")
    else:
        req = urllib.request.Request(url, headers=req_headers, method="GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read()
        ctype = resp.headers.get("Content-Type", "")
    return json.loads(_decode_bytes(raw, ctype))


def _http_bytes(url: str, headers=None, timeout=DEFAULT_TIMEOUT):
    """下载二进制内容（用于封面图片）。失败抛异常。"""
    req_headers = {"User-Agent": USER_AGENT}
    if headers:
        req_headers.update(headers)
    req = urllib.request.Request(url, headers=req_headers, method="GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.read()


# ---------------------------------------------------------------------------
# 网易云音乐
# ---------------------------------------------------------------------------

def _netease_search(query: str, limit: int = 5):
    """网易云搜索（整理刮封面/歌词用）。

    旧接口 api/search/get/web 的 album 已不再返回 picUrl，所有结果
    album_pic 为空，靠逐首再请求详情接口补图（多一次往返且容易超时）。
    cloudsearch 一次就能拿到 picUrl，且 ar/al 与歌曲详情字段一致。
    """
    url = "https://music.163.com/api/cloudsearch/pc"
    data = {
        "s": query, "type": 1, "offset": 0, "total": True, "limit": limit,
        "csrf_token": "",
    }
    headers = {"Referer": "https://music.163.com/", "Content-Type": "application/x-www-form-urlencoded"}
    resp = _http_json(url, data=data, headers=headers)
    songs = []
    try:
        result = resp.get("result") or {}
        for s in (result.get("songs") or [])[:limit]:
            al = s.get("al") or s.get("album") or {}
            ar = s.get("ar") or s.get("artists") or []
            songs.append({
                "source": "netease",
                "song_id": str(s.get("id", "")),
                "title": (s.get("name") or "").strip(),
                "artist": ", ".join(a.get("name", "") for a in ar if a.get("name")),
                "album": (al or {}).get("name", ""),
                "album_pic": _clean_pic_url((al or {}).get("picUrl") or ""),
                "duration_ms": s.get("duration") or s.get("dt") or 0,
            })
    except Exception:
        songs = []
    return songs


def _clean_pic_url(u: str) -> str:
    """网易云封面统一转 https：整理时由服务端直接下载，不受页面
    混合内容限制影响，但 http 链接会被部分代理劫持/缓存污染，仍统一升级。"""
    if u.startswith("http://"):
        return "https://" + u[len("http://"):]
    return u if u.startswith("https://") else ""


def _netease_lyric(song_id: str):
    url = f"https://music.163.com/api/song/lyric?id={urllib.parse.quote(song_id)}&lv=-1&kv=-1&tv=-1"
    headers = {"Referer": "https://music.163.com/"}
    resp = _http_json(url, headers=headers)
    try:
        lrc = ((resp.get("lrc") or {}).get("lyric")) or ""
        if lrc:
            return lrc
    except Exception:
        pass
    return ""


# ---------------------------------------------------------------------------
# QQ 音乐
# ---------------------------------------------------------------------------

def _qq_search(query: str, limit: int = 5):
    url = "https://c.y.qq.com/soso/fcgi-bin/client_search_cp"
    params = {
        "format": "json", "w": query, "n": limit, "cr": 1, "t": 0,
        "remoteplace": "txt.yqq.center",
    }
    resp = _http_json(url + "?" + urllib.parse.urlencode(params),
                      headers={"Referer": "https://y.qq.com/"})
    songs = []
    try:
        for s in (resp.get("data") or {}).get("song", {}).get("list", [])[:limit]:
            songs.append({
                "source": "qq",
                "song_id": s.get("songmid", ""),
                "title": (s.get("songname") or "").strip(),
                "artist": " / ".join(a.get("name", "") for a in (s.get("singer") or []) if a.get("name")),
                "album": (s.get("albumname") or "").strip(),
                "album_pic": (s.get("albummid") and f"https://y.gtimg.cn/music/photo_new/T002R500x500M000{s.get('albummid')}.jpg") or "",
                "duration_ms": (s.get("interval") or 0) * 1000,
            })
    except Exception:
        songs = []
    return songs


def _qq_lyric(songmid: str):
    url = "https://c.y.qq.com/lyric/fcgi-bin/fcg_query_lyric_new.fcg"
    params = {"songmid": songmid, "format": "json", "nobase64": 1, "g_tk": "5381"}
    resp = _http_json(url + "?" + urllib.parse.urlencode(params),
                      headers={"Referer": "https://y.qq.com/"})
    try:
        lyric = resp.get("lyric", "")
        if isinstance(lyric, str) and lyric:
            return lyric
        lyric = ""
        for k in ("lyric", "trans"):
            if resp.get(k):
                import base64
                try:
                    # QQ base64 歌词正文常为 GBK 编码，按 utf-8 -> gbk 顺序解码
                    lyric = _decode_bytes(base64.b64decode(resp[k]))
                except Exception:
                    lyric = ""
            if lyric:
                break
        return lyric or ""
    except Exception:
        return ""


# ---------------------------------------------------------------------------
# 对外统一接口
# ---------------------------------------------------------------------------

def _netease_detail(song_id: str):
    """网易云歌曲详情（用于补封面和准确标题/歌手）。"""
    url = f"https://music.163.com/api/song/detail/?ids=[{urllib.parse.quote(song_id)}]"
    headers = {"Referer": "https://music.163.com/"}
    try:
        resp = _http_json(url, headers=headers)
        s = ((resp.get("songs") or [{}])[0])
        return {
            "source": "netease",
            "song_id": song_id,
            "title": (s.get("name") or "").strip(),
            "artist": ", ".join(a.get("name", "") for a in (s.get("artists") or s.get("ar") or []) if a.get("name")),
            "album": (s.get("album") or s.get("al") or {}).get("name", ""),
            "album_pic": _clean_pic_url((s.get("album") or s.get("al") or {}).get("picUrl") or ""),
            "duration_ms": s.get("duration") or s.get("dt") or 0,
        }
    except Exception:
        return {}


def _title_norm(s: str) -> str:
    """规范化歌名：去掉括号/方括号注释，统一空白与大小写。"""
    s = re.sub(r"[\[\(（【].*?[\]\)）】]", "", s or "")
    s = re.sub(r"[\s\-_—–]+", "", s).strip().lower()
    return s


def _artist_norm(s: str) -> str:
    s = (s or "").strip().lower()
    s = re.sub(r"[\s\-_—–&,/·、]+", "", s)
    return s


# 明显的翻唱/改编/现场标记，出现时降权（没有更好候选时仍可接受）
_COVER_FLAGS = ["翻唱", "cover", "女声", "男声", "钢琴", "吉他", "弹唱", "伴奏", "深情", "伤感",
                "remix", "dj", "现场", "live", "原唱", "女版", "男版", "纯音乐", "instrumental",
                "口琴", "萨克斯", "古筝", "二胡", "电音", "混剪"]


def score_song(s: dict, want: dict) -> int:
    """单个候选与期望(artist/title)的匹配分。

    评分：歌名精确+100/包含+45，歌手精确+80/包含+40，有封面+5，翻唱标记-60。
    特例：期望里有歌手但候选歌手完全不沾边 -> 直接 0 分。
    这是「补全全是同一张纸嫁衣封面」的根因：冷门歌名撞上热歌，
    歌手不匹配的候选必须拒绝，宁缺毋滥。
    """
    want_title = _title_norm(want.get("title", ""))
    want_artist = _artist_norm(want.get("artist", ""))
    t = _title_norm(s.get("title", ""))
    a = _artist_norm(s.get("artist", ""))
    sc = 0
    title_pts = 0
    if want_title and t == want_title:
        title_pts = 100
    elif want_title and (want_title in t or t in want_title):
        title_pts = 45
    artist_pts = 0
    if want_artist and a == want_artist:
        artist_pts = 80
    elif want_artist and (want_artist in a or a in want_artist):
        artist_pts = 40
    if want_artist and not artist_pts:
        return 0
    # 歌名完全不匹配时拒绝：仅歌手相同不足以判定是同一首歌，
    # 否则冷门歌会拿到该歌手其它歌曲的专辑封面（多首歌同一张图的根因）。
    if want_title and not title_pts:
        return 0
    sc += title_pts + artist_pts
    if s.get("album_pic"):
        sc += 5
    low = (s.get("title", "") or "").lower()
    for flag in _COVER_FLAGS:
        if flag in low:
            sc -= 60
            break
    return sc


def score_best(songs: list, want: dict):
    """返回 (最优候选, 其得分)。空列表返回 ({}, 0)。"""
    if not songs:
        return {}, 0
    best = max(songs, key=lambda s: score_song(s, want))
    return best, score_song(best, want)


# 采信阈值：歌名精确(100) + 歌手至少部分匹配(40) = 140 起；
# 无歌手信息时歌名精确(100)+封面(5) 也可采信。
MATCH_MIN = 85


def pick_best(songs: list, want: dict, min_score: int = MATCH_MIN) -> dict:
    """从搜索结果中挑选置信度足够的匹配项；不足返回空 dict（宁缺毋滥）。"""
    best, sc = score_best(songs, want)
    return best if best and sc >= min_score else {}


def _variants(artist: str, title: str):
    """生成搜索兜底变体：去掉 (DJxx版)/(氛围版) 等括号注释与 feat./& 合作后缀。

    DJ 改编版、feat. 合唱这类冷门命名在曲库常搜不到，清洗成「主歌手 + 干净歌名」
    后往往能命中原曲条目（封面与原曲同专辑，可接受）。
    """
    seen, out = set(), []

    def add(a, t):
        a, t = (a or "").strip(), (t or "").strip()
        if not t:
            return
        k = (a.lower(), t.lower())
        if k not in seen:
            seen.add(k)
            out.append((a, t))

    t_clean = re.sub(r"[\[\(（【].*?[\]\)）】]", "", title or "").strip()
    t_clean = re.sub(r"(DJ|Remix|Live|伴奏|纯音乐|女版|男版|氛围版).*$", "", t_clean, flags=re.I).strip()
    a_clean = re.sub(r"\s*(feat\.?|ft\.?|featuring)\s.*$", "", artist or "", flags=re.I)
    a_clean = re.sub(r"[\[\(（【].*?[\]\)）】]", "", a_clean)
    a_clean = re.split(r"[&＆、,，/]| vs\.? ", a_clean)[0].strip()
    add(artist, title)
    add(a_clean, title)
    add(artist, t_clean)
    add(a_clean, t_clean)
    return out


def search_confident(artist: str, title: str, diag: dict = None):
    """按原词 + 清洗变体依次搜曲库，返回首个置信匹配 (artist, title, song)。

    匹配按变体评分，但返回的歌手/歌名仍是文件名解析的原值（文件名优先原则）。
    全部低置信时返回 (artist, title, {})。
    """
    for a, t in _variants(artist, title):
        query = (a + " " + t).strip()
        try:
            songs = search_song(query, diag=diag)
        except Exception:
            songs = []
        song, sc = score_best(songs, {"artist": a, "title": t})
        if song and sc >= MATCH_MIN:
            return artist, title, (enrich_cover(song) or song)
    return artist, title, {}


def resolve_and_match(pairs, diag: dict = None):
    """「A - B」命名中歌手/歌名顺序不确定时，按给出的多种解释依次搜索曲库。

    pairs: [(artist, title), ...] 按优先级排列（默认解释在前）。
    每种解释再叠加 DJ版/feat. 清洗变体（search_confident）。
    返回 (artist, title, song)；首个达到置信阈值的解释被采用；
    全部低置信时返回默认解释 + 空 song（不强行采用搜索结果）。
    """
    for a0, t0 in pairs:
        a, t, song = search_confident(a0, t0, diag=diag)
        if song:
            return a0, t0, song
    a, t = pairs[0]
    return a, t, {}


def lyric_garbled(text: str) -> bool:
    """判断歌词文本是否乱码（含大量 U+FFFD 替换符/C1 控制符）。"""
    if not text:
        return False
    bad = text.count("\ufffd") + sum(1 for ch in text if 0x80 <= ord(ch) <= 0x9F)
    return bad > max(4, int(len(text) * 0.02))


def search_song(query: str, limit: int = 5, diag: dict = None):
    """按优先级依次从 QQ音乐、网易云搜索，返回结果列表。

    diag: 可选 dict，失败时写入 diag['qq'] / diag['netease'] 异常信息，
    供上层记录日志定位「全部缺歌词封面」是网络问题还是接口问题。
    """
    results = []
    try:
        results = _qq_search(query, limit)
    except Exception as e:
        if diag is not None:
            diag["qq"] = f"{type(e).__name__}: {e}"
        results = []
    if not results:
        try:
            results = _netease_search(query, limit)
        except Exception as e:
            if diag is not None:
                diag["netease"] = f"{type(e).__name__}: {e}"
            results = []
    return results


def enrich_cover(song: dict) -> dict:
    """若歌曲缺封面且来自网易云，通过歌曲详情补封面。返回补充后的 song。"""
    if not song or song.get("album_pic"):
        return song
    if song.get("source") == "netease":
        detail = _netease_detail(song.get("song_id", ""))
        if detail:
            song = {**song, **{k: v for k, v in detail.items() if v}}
    return song


def fetch_lyric(song: dict):
    """根据歌曲信息抓取 LRC 歌词。

    QQ 歌词接口对冷门/登录态要求变严（retcode=1101 返回空），
    因此当首选源拿不到歌词时，跨平台用另一曲库按「歌手+歌名」再搜一次兜底。
    """
    if not song:
        return ""
    try:
        if song.get("source") == "netease":
            lyr = _netease_lyric(song["song_id"])
            if lyr:
                return lyr
        elif song.get("source") == "qq":
            lyr = _qq_lyric(song["song_id"])
            if lyr:
                return lyr
    except Exception:
        pass
    # 跨平台兜底：用另一曲库搜索同名的歌再取歌词
    return _fetch_lyric_fallback(song)


def _fetch_lyric_fallback(song: dict):
    """首选源无歌词时，改用另一平台按 artist+title 搜索并取歌词。"""
    artist = song.get("artist", "")
    title = song.get("title", "")
    query = (artist + " " + title).strip()
    if not query:
        return ""
    want_other = "netease" if song.get("source") == "qq" else "qq"
    try:
        others = _netease_search(query) if want_other == "netease" else _qq_search(query)
    except Exception:
        others = []
    best = pick_best(others, {"artist": artist, "title": title}) if others else {}
    if not best:
        return ""
    try:
        if best.get("source") == "netease":
            return _netease_lyric(best["song_id"])
        if best.get("source") == "qq":
            return _qq_lyric(best["song_id"])
    except Exception:
        pass
    return ""


def download_cover(url: str, save_path: str) -> bool:
    """下载封面图片到 save_path，成功返回 True。"""
    if not url:
        return False
    try:
        data = _http_bytes(url)
        if len(data) < 100:
            return False
        os.makedirs(os.path.dirname(save_path) or ".", exist_ok=True)
        with open(save_path, "wb") as f:
            f.write(data)
        return True
    except Exception:
        return False


def file_sha1(path: str, chunk=1024 * 1024) -> str:
    """计算文件 SHA-1（用于去重）。"""
    h = hashlib.sha1()
    try:
        with open(path, "rb") as f:
            while True:
                block = f.read(chunk)
                if not block:
                    break
                h.update(block)
    except Exception:
        return ""
    return h.hexdigest()


def file_size(path: str) -> int:
    try:
        return os.path.getsize(path)
    except Exception:
        return 0
