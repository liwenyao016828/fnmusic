# -*- coding: utf-8 -*-
"""
music_source.py — 多源网络音源搜索与下载（纯 Python 标准库）

内置源（可多选启用，混合搜索）：
  - 网易云音乐 / 咪咕音乐 / 酷狗音乐 / 酷我音乐 / QQ 音乐

扩展：
  - 用户自定义 API 列表（可添加多个，兼容落雪音乐/music-api 等标准接口），
    每个 API 可单独勾选是否参与混合搜索

搜索结果统一字段：
  id / name / artist / album / duration(秒) / source / source_name
  cover(封面直链，可空) / size(字节，可空) / quality(音质标签，可空)
  bitrate(码率 kbps，可空) / format(容器格式，可空) / lyric_state(是否有歌词，可空)
"""

import base64
import json
import os
import re
import threading
import time
import urllib.request
import urllib.parse
import urllib.error

UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/120.0.0 Safari/537.36")
TIMEOUT = 15

# 源名称映射
SOURCE_NAMES = {
    "netease": "网易云",
    "migu": "咪咕",
    "kugou": "酷狗",
    "kuwo": "酷我",
    "qq": "QQ音乐",
    "custom": "自定义",
}

# 默认显示别名：用同音字代替平台名称，设置里可逐源改写（清空即回退真实名称）
SOURCE_ALIAS_DEFAULT = {
    "netease": "网忆云",
    "qq": "秋秋音乐",
    "migu": "米菇音乐",
    "kugou": "库狗音乐",
    "kuwo": "库我音乐",
}

# 各源请求 Referer
SOURCE_REFERERS = {
    "netease": "https://music.163.com/",
    "kugou": "http://www.kugou.com/",
    "kuwo": "http://www.kuwo.cn/",
    "migu": "https://music.migu.cn/",
    "qq": "https://y.qq.com/",
}


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _redirect_location(url, referer=None, cookie=None):
    """请求 url，返回 302 目标地址（不实际下载）。失败返回 ""。"""
    req = urllib.request.Request(url)
    req.add_header("User-Agent", UA)
    if referer:
        req.add_header("Referer", referer)
    if cookie:
        req.add_header("Cookie", cookie)
    opener = urllib.request.build_opener(_NoRedirect)
    try:
        opener.open(req, timeout=10)
        return ""
    except urllib.error.HTTPError as e:
        if e.code in (301, 302, 303, 307, 308):
            loc = e.headers.get("Location", "")
            if loc:
                return urllib.parse.urljoin(url, loc)
    except Exception:
        pass
    return ""


# ---------------------------------------------------------------------------
# HTTP 工具
# ---------------------------------------------------------------------------

def _http_post_form(url, data, referer=None, headers=None):
    """POST form-urlencoded，返回 bytes。"""
    body = urllib.parse.urlencode(data).encode("utf-8")
    req = urllib.request.Request(url, data=body, method="POST")
    req.add_header("User-Agent", UA)
    req.add_header("Content-Type", "application/x-www-form-urlencoded")
    req.add_header("Accept", "*/*")
    if referer:
        req.add_header("Referer", referer)
    if headers:
        for k, v in headers.items():
            req.add_header(k, v)
    with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
        return r.read()


def _http_get_bytes(url, referer=None, headers=None):
    """GET 返回 bytes。"""
    req = urllib.request.Request(url)
    req.add_header("User-Agent", UA)
    if referer:
        req.add_header("Referer", referer)
    if headers:
        for k, v in headers.items():
            req.add_header(k, v)
    with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
        return r.read()


def _http_get_json(url, referer=None, headers=None):
    """GET 返回 parsed JSON。"""
    raw = _http_get_bytes(url, referer=referer, headers=headers)
    return json.loads(_decode(raw))


def _decode(raw):
    """通用解码：utf-8 → gbk。"""
    for enc in ("utf-8", "gbk"):
        try:
            return raw.decode(enc)
        except Exception:
            continue
    return raw.decode("utf-8", errors="ignore")


def _quality_label(bitrate):
    """码率 → 音质标签（自动兼容 bps 与 kbps 两种单位）。"""
    try:
        br = int(bitrate)
    except Exception:
        return ""
    if not br:
        return ""
    if br > 1000:  # 网易云等以 bps 表示
        br = br // 1000
    if br >= 700:
        return "无损"
    if br >= 300:
        return "320k"
    if br >= 190:
        return "192k"
    if br >= 120:
        return "128k"
    return f"{br}k"


def _kbps(bitrate):
    """把各源混用的码率单位（bps / kbps）归一为 kbps，取不到返回 0。"""
    try:
        br = int(bitrate)
    except Exception:
        return 0
    return br // 1000 if br > 1000 else br


def _bitrate_of(size, duration, nominal=0):
    """优先由文件大小与时长实算 kbps，缺数据时回退标称码率。"""
    try:
        if size and duration and int(duration) > 0:
            return int(int(size) * 8 / int(duration) / 1000)
    except Exception:
        pass
    return _kbps(nominal)


# ---------------------------------------------------------------------------
# 网易云音乐
# ---------------------------------------------------------------------------

# cloudsearch 的档位字段（高 -> 低）：(key, 标称 kbps, 音质标签, 默认容器)
NETEASE_TIERS = (
    ("hr", 3000, "Hi-Res", "flac"),
    ("sq", 900, "无损", "flac"),
    ("h", 320, "320k", "mp3"),
    ("m", 192, "192k", "mp3"),
    ("l", 128, "128k", "mp3"),
)


def _netease_cover(al, width=300):
    """网易云专辑封面直链：升级 https 并按需服务端缩图。

    搜索页所有歌都没封面的根因：旧接口 api/v1/search/get 的 album
    只剩 picId、不再返回 picUrl；换用 cloudsearch 后 picUrl 是 http 原图
    （常 1MB 起），在 https 网关页面里会被当混合内容拦掉，所以统一
    转 https 并挂 param 缩图参数（600KB 级 -> 几十 KB，列表页省带宽）。
    """
    if not isinstance(al, dict):
        return ""
    u = al.get("picUrl") or al.get("blurPicUrl") or ""
    if not u.startswith("http"):
        return ""
    if u.startswith("http://"):
        u = "https://" + u[len("http://"):]
    if width and "param=" not in u:
        u += "?param=%dy%d" % (width, width)
    return u


def netease_search(keyword, limit=20):
    """网易云音乐搜索（含封面/音质/大小信息）。

    走 api/cloudsearch/pc：这是目前唯一仍在返回 al.picUrl 与各档位
    (hr/sq/h/m/l 的 br/size/sr) 的公开接口，旧接口两者都缺，导致
    封面恒为空、quality/format/bitrate/size 全 None。
    """
    data = {"s": keyword, "type": 1, "limit": limit, "offset": 0,
            "total": "true", "csrf_token": ""}
    raw = _http_post_form("https://music.163.com/api/cloudsearch/pc", data,
                          referer="https://music.163.com/")
    j = json.loads(_decode(raw))
    songs = []
    result = j.get("result", {}) or {}
    for s in result.get("songs", []) or []:
        # 该接口字段为 ar/al，兼容旧版的 artists/album
        ar = s.get("ar") or s.get("artists") or []
        artists = "/".join(a.get("name", "") for a in ar if isinstance(a, dict))
        al = s.get("al") or s.get("album") or {}
        if not artists:
            artists = (s.get("artistname") or s.get("artistsname") or "").replace("&", "/")
        album_name = al.get("name", "") if isinstance(al, dict) else str(al or "")
        song = {
            "id": str(s.get("id", "")),
            "name": s.get("name", ""),
            "artist": artists,
            "album": album_name,
            "album_id": str(al.get("id", "")) if isinstance(al, dict) else "",
            "duration": (s.get("duration") or s.get("dt") or 0) // 1000,
            "source": "netease",
            "cover": _netease_cover(al),
        }
        best = None
        for key, nominal, label, ext in NETEASE_TIERS:
            t = s.get(key)
            if isinstance(t, dict) and t.get("size"):
                best = {"size": t.get("size", 0), "label": label, "nominal": nominal,
                        "ext": (t.get("extension") or ext), "br": t.get("br") or 0}
                break
        if best:
            song["size"] = best["size"]
            song["quality"] = best["label"]
            song["format"] = best["ext"]
            song["bitrate"] = _bitrate_of(best["size"], song["duration"],
                                          best["br"] or best["nominal"])
        # fee>0 为付费/VIP 曲目（1=专辑付费 4=VIP 8=付费音乐包），直链通常不可得
        if s.get("fee"):
            song["vip"] = True
        songs.append(song)
    return songs


def netease_url(song_id, cookie=""):
    """网易云下载 URL。有 Cookie 时优先走登录接口取 VIP 直链，否则走 outer 302。"""
    if cookie:
        u = _netease_url_via_cookie(song_id, cookie)
        if u:
            return u
    return f"https://music.163.com/song/media/outer/url?id={song_id}.mp3"


def _netease_url_via_cookie(song_id, cookie):
    """用登录 Cookie 请求增强播放接口，VIP 账号可拿到完整直链。失败返回 ""。"""
    try:
        raw = _http_post_form(
            "https://music.163.com/api/song/enhance/player/url",
            {"ids": f"[{song_id}]", "br": 999000},
            referer="https://music.163.com/",
            headers={"Cookie": cookie})
        j = json.loads(_decode(raw))
        data = (j.get("data") or [{}])
        url = data[0].get("url") or ""
        return url if url.startswith("http") else ""
    except Exception:
        return ""


def netease_preview(song):
    """网易云试听直链（解析 302 得到真实 CDN 地址）。"""
    u = netease_url(song.get("id", ""))
    return _redirect_location(u, referer="https://music.163.com/") or u


def netease_lyric(song_id):
    """网易云歌词。"""
    url = (f"https://music.163.com/api/song/lyric?id={song_id}"
           f"&lv=1&kv=1&tv=-1")
    try:
        j = _http_get_json(url, referer="https://music.163.com/")
        return j.get("lrc", {}).get("lyric", "")
    except Exception:
        return ""


# ---------------------------------------------------------------------------
# 咪咕音乐
# ---------------------------------------------------------------------------

# 咪咕取链渠道号：必须作为 query 参数传，只放请求头会被接口判为「channel 为空」
MIGU_CHANNEL = "0140210"
MIGU_REFERER = "https://m.migumusic.com.cn/"
# rateFormats.formatType → (标称码率 kbps, 音质标签, 默认容器)
MIGU_TIERS = {
    "SQ": (900, "无损", "flac"),
    "HQ": (320, "320k", "mp3"),
    "PQ": (128, "128k", "mp3"),
    "LQ": (64, "64k", "mp3"),
}
MIGU_TIER_ORDER = ("SQ", "HQ", "PQ", "LQ")   # 高 → 低
MIGU_FREE_TIER = "PQ"                          # 实测匿名拿 toneFlag 任意值，CDN 都只回 PQ


def _migu_apply_tier(song, flag):
    """把展示与下载信息切到指定档位（格式/音质/码率/体积随之更新）。"""
    nominal, label, dft_ext = MIGU_TIERS.get(flag, MIGU_TIERS[MIGU_FREE_TIER])
    song["_tier"] = flag
    song["format"] = (song.get("_tier_fmts") or {}).get(flag) or dft_ext
    song["quality"] = label
    size = int((song.get("_tiers") or {}).get(flag) or 0)
    if size:
        song["size"] = size
    else:
        song.pop("size", None)
    song["bitrate"] = _bitrate_of(size, song.get("duration"), nominal)
    return song


def migu_search(keyword, limit=20):
    """咪咕音乐搜索——搜索结果自带各档位体积与 VIP 标记。"""
    sw = urllib.parse.quote(json.dumps(
        {"song": 1, "album": 0, "singer": 0, "tagSong": 1, "mvSong": 0, "bestShow": 1},
        separators=(",", ":")))
    params = urllib.parse.urlencode({
        "text": keyword, "pageNo": 1, "pageSize": limit,
        "isCopyright": 1, "sort": 1, "searchSwitch": sw,
    })
    url = f"https://c.musicapp.migu.cn/v1.0/content/search_all.do?{params}"
    j = _http_get_json(url)
    songs = []
    result_data = j.get("songResultData") or {}
    for s in result_data.get("result", [])[:limit]:
        singers = "/".join(sg.get("name", "") for sg in (s.get("singers") or []))
        albums = s.get("albums") or []
        album_name = albums[0].get("name", "") if albums else ""
        song = {
            "id": str(s.get("id", "")),
            "name": s.get("name", ""),
            "artist": singers,
            "album": album_name,
            "duration": int(s.get("duration", 0) or 0),
            "source": "migu",
            "_cid": str(s.get("copyrightId", "")),
            "_contentId": str(s.get("contentId", "")),
        }
        # 封面（imgItems 多尺寸，取最大）
        imgs = s.get("imgItems") or []
        if imgs:
            best = imgs[-1]
            ip = best.get("img") or best.get("tp") or ""
            if ip:
                song["cover"] = ip
        # 档位表：rateFormats 自带各档体积/容器/VIP 标记，展示固定取实际能下到的档位
        tiers, fmts, vips = {}, {}, {}
        for t in s.get("rateFormats") or []:
            flag = t.get("formatType")
            if flag not in MIGU_TIERS:
                continue
            try:
                size = int(t.get("size") or t.get("androidSize") or 0)
            except Exception:
                size = 0
            if size > 1024:
                tiers[flag] = size
                fmts[flag] = (t.get("fileType") or t.get("androidFileType") or "mp3").lower()
                vips[flag] = "vip" in (t.get("showTag") or [])
        song["_tiers"], song["_tier_fmts"], song["_tier_vip"] = tiers, fmts, vips
        if tiers:
            # 无 Cookie 时只能拿到 PQ；配了 Cookie 才按最高档预估（取链时会校准）
            want = MIGU_FREE_TIER if MIGU_FREE_TIER in tiers else next(
                (f for f in MIGU_TIER_ORDER if f in tiers), "")
            if want:
                _migu_apply_tier(song, want)
                if vips.get(want) or str(s.get("vipType") or "0") not in ("", "0"):
                    song["vip"] = True
        # 歌词地址
        lru = s.get("lyricUrl") or ""
        if lru:
            song["_lyric_url"] = lru
        songs.append(song)
    return songs


def migu_url(song_id, extra=None, cookie=""):
    """咪咕播放直链。

    接口成功时直接 302 到 CDN 音频文件（因此只能取重定向地址，不能当 JSON 解），
    失败时返回 JSON 错误体。档位逐高到低重试：无 Cookie 只会被给 PQ(128k)，配了
    Cookie 先从搜索结果里的最高档试起。副作用：把 song 的 quality/format/bitrate/size
    校准到实际取到的档位（与 qq_url 一致）。
    """
    extra = extra if extra is not None else {}
    tiers = extra.get("_tiers") or {}
    vips = extra.get("_tier_vip") or {}
    if cookie:
        order = [f for f in MIGU_TIER_ORDER if f in tiers]
    else:
        # 匿名一律被给 PQ，标了 VIP 的档位试了也拿不到，只试未标 VIP 的最高档兜底
        free = [f for f in MIGU_TIER_ORDER if f in tiers and not vips.get(f)]
        order = [MIGU_FREE_TIER] if MIGU_FREE_TIER in tiers else free[:1]
    for flag in (order or [MIGU_FREE_TIER]):
        params = urllib.parse.urlencode({
            "netType": "00", "resourceType": "2", "toneFlag": flag,
            "version": "6.0.0", "copyrightId": extra.get("_cid", ""),
            "contentId": extra.get("_contentId", ""), "songId": song_id,
            "albumId": "", "lowQuality": "128", "channel": MIGU_CHANNEL,
        })
        url = ("https://app.c.nf.migu.cn/MIGUM2.0/v1.0/content/sub/listenSong.do"
               f"?{params}")
        loc = _redirect_location(url, referer=MIGU_REFERER, cookie=cookie)
        if loc:
            _migu_apply_tier(extra, flag)
            return loc
    return ""


def migu_lyric(song):
    """咪咕歌词（直接抓取搜索结果中的 lyricUrl）。"""
    u = song.get("_lyric_url") or ""
    if not u:
        return ""
    try:
        return _decode(_http_get_bytes(u))
    except Exception:
        return ""


# ---------------------------------------------------------------------------
# 酷狗音乐
# ---------------------------------------------------------------------------

def kugou_search(keyword, limit=20):
    """酷狗音乐搜索——华语流行资源丰富。"""
    params = urllib.parse.urlencode({
        "keyword": keyword,
        "page": 1,
        "pagesize": limit,
    })
    url = f"http://mobilecdn.kugou.com/api/v3/search/song?{params}"
    j = _http_get_json(url)
    songs = []
    for s in j.get("data", {}).get("info", []):
        song = {
            "id": s.get("hash", ""),
            "name": s.get("songname", ""),
            "artist": s.get("singername", ""),
            "album": s.get("album_name", ""),
            "album_id": str(s.get("album_id", "")),
            "duration": s.get("duration", 0),
            "source": "kugou",
            "_album_id": s.get("album_id", ""),
        }
        # 文件大小（sizable 或 filesize）
        size = s.get("filesize") or s.get("filesize_flac") or 0
        if not size:
            try:
                size = int(s.get("sizable", 0) or 0)
            except Exception:
                size = 0
        if size:
            song["size"] = size
        br = _kbps(s.get("bitrate") or s.get("bitrate_flac") or 0)
        if br:
            song["quality"] = _quality_label(br)
            song["bitrate"] = br
        else:
            est = _bitrate_of(size, song["duration"])
            if est:
                song["bitrate"] = est
        ext = s.get("file_format") or ("flac" if (br and int(br) >= 700) else "mp3")
        song["format"] = ext
        # cover100.kugou.com/getCover 已失效（502/断连），改用 hash 对应的 stdmusic 封面图
        h = s.get("hash", "")
        song["cover"] = f"https://imge.kugou.com/stdmusic/300/{h}.jpg" if h else ""
        # pay_type 非 0 为付费/VIP 曲目
        try:
            if int(s.get("pay_type") or 0) > 0:
                song["vip"] = True
        except Exception:
            pass
        songs.append(song)
    return songs


def kugou_url(song_hash, album_id="", cookie=""):
    """酷狗音乐获取播放链接。

    免费曲目走移动端 getSongInfo.php?cmd=playInfo 可直接拿到 https 直链；
    配置 Cookie 时优先走 www getdata（VIP 提档），失败再回退移动端接口。
    """
    if not song_hash:
        return ""
    headers = {"Cookie": cookie} if cookie else None

    def _via_getdata():
        # 必须带 appid/mid/plat，否则接口直接返回 err_code 20010（取不到链）
        params = urllib.parse.urlencode({
            "hash": song_hash, "appid": 1014, "mid": "1", "plat": 0,
            "album_id": album_id,
        })
        j = _http_get_json(f"http://www.kugou.com/yy/index.php?r=play/getdata&{params}",
                           headers=headers)
        d = j.get("data") if isinstance(j, dict) else None
        if not isinstance(d, dict):
            return ""
        for k in ("play_url", "play_backup_url"):
            u = str(d.get(k) or "").strip()
            if u:
                return u
        return ""

    def _via_mobile():
        params = urllib.parse.urlencode({"cmd": "playInfo", "hash": song_hash})
        j = _http_get_json(f"http://m.kugou.com/app/i/getSongInfo.php?{params}",
                           headers=headers)
        if not isinstance(j, dict):
            return ""
        for k in ("url", "url_backup", "downurl", "downUrl"):
            u = str(j.get(k) or "").strip()
            if u:
                return u
        return ""

    order = (_via_getdata, _via_mobile) if cookie else (_via_mobile, _via_getdata)
    for fn in order:
        try:
            u = fn()
        except Exception:
            u = ""
        if u:
            return u
    return ""


def kugou_lyric(song_hash):
    """酷狗歌词接口。"""
    params = urllib.parse.urlencode({"hash": song_hash, "plat": "pc", "version": "1"})
    url = f"http://krcs.kugou.com/search?{params}"
    try:
        j = _http_get_json(url)
        cand = (j.get("candidates") or [{}])[0]
        kid, accesskey = cand.get("id", ""), cand.get("accesskey", "")
        if not kid:
            return ""
        p2 = urllib.parse.urlencode({"id": kid, "accesskey": accesskey,
                                     "fmt": "lrc", "charset": "utf8"})
        j2 = _http_get_json(f"http://krcs.kugou.com/download?{p2}")
        import base64
        raw = base64.b64decode(j2.get("lyricContent", ""))
        return _decode(raw)
    except Exception:
        return ""


# ---------------------------------------------------------------------------
# 酷我音乐
# ---------------------------------------------------------------------------

def _kuwo_entries(text):
    """解析酷我 r.s 响应（KEY=VALUE 行格式，字段字母序、按歌曲分块重复）。

    以 MUSICID= 行为锚点，用相邻锚点中点切块，返回每块的字段 dict 列表。
    """
    lines = text.splitlines()
    anchors = [i for i, l in enumerate(lines) if l.startswith("MUSICID=")]
    blocks = []
    for n, a in enumerate(anchors):
        lo = (anchors[n - 1] + a) // 2 if n > 0 else 0
        hi = (a + anchors[n + 1]) // 2 if n + 1 < len(anchors) else len(lines)
        d = {}
        for l in lines[lo:hi]:
            k, sep, v = l.partition("=")
            if sep and k and k not in d:
                d[k] = v
        blocks.append(d)
    return blocks


def _kuwo_field(blk, key):
    return blk.get(key, "") if isinstance(blk, dict) else ""


def _unescape_html(s):
    import html as _h
    return _h.unescape(s or "")


# 酷我 MINFO 里会混进加密封装（mgg/mflac/kgm/mkc）：这类档位只有 VIP 客户端
# 能解，bitrate 字段也是加密标志位（实测出现 24000/20501），拿来展示会算出
# “24k”这种鬼音质，拿来下载也播不了，所以选档时直接排除。
KUWO_DRM_FMT = ("mgg", "mflac", "kgm", "mkc")
KUWO_LOSSLESS_FMT = ("flac", "ape", "wav")


def _kuwo_pick_tier(*minfo_strs):
    """从多个 MINFO 串里选可播的最高档位，返回 dict 或 None。

    单位大小写不统一（实测同时存在 size:52.83Mb 与 size:83.67Kb），Kb 是
    千字节不是千比特（14 秒 48kbps 的 aac 正好 83.67Kb），按 1024 换算；
    正则若写成大小写敏感的 (Mb|kb) 会整段匹配失败，短视频/小样条目会凭空消失。
    """
    best = None
    for minfo in minfo_strs:
        for seg in (minfo or "").split(";"):
            mm = re.match(r"level:(\w+),bitrate:(\d+),format:(\w+),size:([\d.]+)(MB|KB)",
                          seg, re.IGNORECASE)
            if not mm:
                continue
            fmt = mm.group(3)
            if fmt in KUWO_DRM_FMT:
                continue
            try:
                br = int(mm.group(2))
                bytes_ = float(mm.group(4)) * (1048576 if mm.group(5).lower() == "mb" else 1024)
            except Exception:
                continue
            cand = {"br": br, "fmt": fmt, "size": int(bytes_)}
            if not best or cand["size"] > best["size"]:
                best = cand
        if best:
            break
    return best


def kuwo_search(keyword, limit=20):
    """酷我音乐搜索——补充源。"""
    params = urllib.parse.urlencode({
        "all": keyword, "ft": "music", "pn": 0, "rn": limit,
        "encoding": "utf8", "mobi": "0",
    })
    url = f"http://search.kuwo.cn/r.s?{params}"
    try:
        text = _decode(_http_get_bytes(url))
        songs = []
        for blk in _kuwo_entries(text)[:limit]:
            rid = _kuwo_field(blk, "MUSICRID").replace("MUSIC_", "")
            if not rid:
                continue
            name = _unescape_html(_kuwo_field(blk, "NAME") or _kuwo_field(blk, "SONGNAME"))
            artist = _unescape_html(_kuwo_field(blk, "ARTIST"))
            song = {
                "id": rid,
                "name": name,
                "artist": artist,
                "album": _unescape_html(_kuwo_field(blk, "ALBUM")),
                "duration": int(_kuwo_field(blk, "DURATION") or 0),
                "source": "kuwo",
                "cover": _kuwo_field(blk, "PIC") or _kuwo_field(blk, "PICPATH"),
            }
            # MINFO 只取可播档位（排除 mgg/mflac 等加密封装），按体积取最高
            best = _kuwo_pick_tier(_kuwo_field(blk, "MINFO"), _kuwo_field(blk, "N_MINFO"))
            if best:
                song["quality"] = ("无损" if best["fmt"] in KUWO_LOSSLESS_FMT
                                   else _quality_label(best["br"]))
                song["format"] = best["fmt"]
                song["size"] = best["size"]
                song["bitrate"] = _bitrate_of(best["size"], song["duration"], best["br"])
            # PAY/OPAY 非 0 为付费/VIP 曲目
            try:
                if int(_kuwo_field(blk, "PAY") or 0) > 0 or \
                   int(_kuwo_field(blk, "OPAY") or 0) > 0:
                    song["vip"] = True
            except Exception:
                pass
            songs.append(song)
        return songs
    except Exception:
        return []


def kuwo_url(song_id):
    """酷我音乐获取下载链接。"""
    params = urllib.parse.urlencode({
        "type": "convert_url", "format": "mp3", "response": "url",
        "rid": f"MUSIC_{song_id}",
    })
    url = f"http://antiserver.kuwo.cn/anti.s?{params}"
    try:
        raw = _decode(_http_get_bytes(url)).strip()
        if raw.startswith("http"):
            return raw
    except Exception:
        pass
    return ""


def kuwo_lyric(song_id):
    """酷我歌词。"""
    url = (f"http://m.kuwo.cn/newh5/singles/songinfoandlrc?musicId={song_id}"
           f"&httpsStatus=1")
    try:
        j = _http_get_json(url, referer="http://www.kuwo.cn/")
        lines = j.get("data", {}).get("lrclist", [])
        out = []
        for ln in lines:
            t = float(ln.get("time", 0))
            out.append(f"[{int(t // 60):02d}:{t % 60:05.2f}]{ln.get('lineLyric', '')}")
        return "\n".join(out)
    except Exception:
        return ""


# ---------------------------------------------------------------------------
# QQ 音乐（「小秋音乐」插件同款原生接口，无需 JS 插件）
# ---------------------------------------------------------------------------

QQ_FCG = ("https://u.y.qq.com/cgi-bin/musicu.fcg"
          "?g_tk=5381&format=json&inCharset=utf8&outCharset=utf-8")
# vkey 接口不返回 sip 时的候选主机（已实测可用）
QQ_STREAM_HOSTS = ("https://dl.stream.qqmusic.qq.com/",
                   "https://ws.stream.qqmusic.qq.com/",
                   "http://ws.stream.qqmusic.qq.com/")
# 档位前缀 → (容器, 标称码率 kbps, 搜索结果里的体积字段)
QQ_TIERS = {
    "F000": ("flac", 900, "size_flac"),
    "M800": ("mp3", 320, "size_320mp3"),
    "M500": ("mp3", 128, "size_128mp3"),
}
QQ_TIER_ORDER = ("F000", "M800", "M500")


def _qq_post(payload, cookie=""):
    """POST musicu.fcg 聚合接口。"""
    req = urllib.request.Request(QQ_FCG,
                                 data=json.dumps(payload).encode("utf-8"),
                                 method="POST")
    req.add_header("User-Agent", UA)
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "*/*")
    req.add_header("Referer", "https://y.qq.com/")
    req.add_header("Cookie", cookie or "uin=")
    with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
        return json.loads(_decode(r.read()))


def qq_uin(cookie=""):
    """从 QQ 音乐 Cookie 提取数字 uin（形如 uin=123456789 / uin=o123456789）。"""
    m = re.search(r"(?:^|;\s*)uin=[or]?(\d{5,})", cookie or "")
    return m.group(1) if m else "0"


def qq_filename(mid, prefix="M500"):
    """按档位前缀拼 QQ 音乐文件名（vkey 接口以此为主键回 purl）。"""
    ext = QQ_TIERS.get(prefix, ("mp3", 0, ""))[0]
    return f"{prefix}{mid}.{ext}"


def qq_vkey(pairs, cookie="", batch=15):
    """批量取 vkey 直链。

    pairs: [(mid, prefix), ...]，songmid 与 filename 必须一一对应：接口对每首
           只认第一条 filename（高档位拿不到不会自动降档），多传也不会降级。
    返回 {mid: url}；全部请求失败返回 None，区别于「成功但拿不到链」。
    """
    pairs = [x for x in (pairs or []) if x[0]]
    if not pairs:
        return {}
    out, failed = {}, False
    for i in range(0, len(pairs), batch):
        chunk = pairs[i:i + batch]
        mids = [m for m, _ in chunk]
        fns = [qq_filename(m, p or "M500") for m, p in chunk]
        try:
            j = _qq_post({"req_1": {
                "method": "CgiGetVkey", "module": "vkey.GetVkeyServer",
                "param": {"guid": "111111111", "songmid": mids,
                          "songtype": [0] * len(mids), "uin": qq_uin(cookie),
                          "loginmode": "normal", "specialtype": "",
                          "filename": fns},
            }}, cookie=cookie)
        except Exception:
            failed = True
            continue
        data = ((j.get("req_1") or {}).get("data") or {})
        hosts = [h for h in (data.get("sip") or []) if h]
        if hosts and not hosts[0].endswith("/"):
            hosts[0] += "/"
        host = (hosts[0] if hosts else QQ_STREAM_HOSTS[0])
        want = {fn: m for m, fn in zip(mids, fns)}
        for info in data.get("midurlinfo") or []:
            fn = (info.get("filename") or "").strip()
            purl = (info.get("purl") or "").strip()
            if not fn or not purl or fn not in want:
                continue
            url = purl if purl.startswith("http") else host + purl.lstrip("/")
            out[want[fn]] = url
    if failed and not out:
        return None
    return out


def qq_resolve(songs, cookie=""):
    """逐档批量取链，为每首拿到当前身份下可用的最高档位。

    按无损 → 320k → 128k 发三批（只包当前档位未命中的歌），最多 3 次请求，
    比逐首重试省得多。返回 ({mid: (prefix, url)}, 是否有请求失败)。
    """
    todo = [s for s in (songs or []) if s.get("id")]
    out = {}
    failed = False
    for prefix in QQ_TIER_ORDER:
        pend = [s for s in todo if s["id"] not in out]
        cand = [(s["id"], prefix) for s in pend
                if prefix in (s.get("_tiers") or {}) or not (s.get("_tiers") or {})]
        if not cand:
            continue
        got = qq_vkey(cand, cookie)
        if got is None:
            failed = True
            continue
        for mid, url in got.items():
            out[mid] = (prefix, url)
    return out, failed


def _qq_apply_tier(song, prefix):
    """把展示与下载信息切到实际可取的档位（格式/音质/码率/体积随之更新）。"""
    fmt, nominal, _ = QQ_TIERS.get(prefix, ("mp3", 128, ""))
    song["_prefix"] = prefix
    song["format"] = fmt
    song["quality"] = "无损" if fmt == "flac" else _quality_label(nominal)
    size = int((song.get("_tiers") or {}).get(prefix) or 0)
    if size:
        song["size"] = size
    else:
        song.pop("size", None)
    song["bitrate"] = _bitrate_of(size, song.get("duration"), nominal)
    return song


def qq_search(keyword, limit=20, cookie=""):
    """QQ 音乐搜索（含封面/档位体积/付费标记）。"""
    j = _qq_post({"req_1": {
        "method": "DoSearchForQQMusicDesktop",
        "module": "music.search.SearchCgiService",
        "param": {"num_per_page": limit, "page_num": 1,
                  "query": keyword, "search_type": 0},
    }}, cookie=cookie)
    body = ((j.get("req_1") or {}).get("data") or {}).get("body") or {}
    songs = []
    for s in (body.get("song") or {}).get("list") or []:
        mid = s.get("mid") or s.get("songmid") or ""
        if not mid:
            continue
        al = s.get("album") or {}
        album_mid = al.get("mid") or s.get("albummid") or ""
        artist = "/".join(x.get("name", "") for x in (s.get("singer") or [])
                          if isinstance(x, dict) and x.get("name"))
        dur = int(s.get("interval") or 0)
        song = {
            "id": mid,                       # 取链统一用 songmid
            "song_id": str(s.get("id") or s.get("songid") or ""),
            "name": _unescape_html(s.get("title") or s.get("songname") or ""),
            "artist": artist,
            "album": _unescape_html(al.get("name") or s.get("albumname") or ""),
            "album_id": str(al.get("id") or ""),
            "_album_mid": album_mid,
            "duration": dur,
            "source": "qq",
            "cover": (f"https://y.gtimg.cn/music/photo_new/"
                      f"T002R800x800M000{album_mid}.jpg") if album_mid else "",
        }
        # 档位体积表（无损 FLAC / 320k / 128k），展示取最高档，取链时按此降级
        f = s.get("file") or {}
        tiers = {}
        for prefix in QQ_TIER_ORDER:
            size = int(f.get(QQ_TIERS[prefix][2]) or 0)
            if size > 1024:
                tiers[prefix] = size
        song["_tiers"] = tiers
        if tiers:
            _qq_apply_tier(song, next(p for p in QQ_TIER_ORDER if p in tiers))
        # pay_down/pay_play 非 0 为付费曲目（游客态拿不到 purl，配 Cookie 后实测解锁）
        pay = s.get("pay") or {}
        try:
            if int(pay.get("pay_down") or 0) > 0 or int(pay.get("pay_play") or 0) > 0:
                song["vip"] = True
        except Exception:
            pass
        songs.append(song)
    return songs


def qq_url(song, cookie=""):
    """QQ 音乐直链：从无损到 128k 取当前身份能拿到的最高档。

    副作用：会把 song 的 quality/format/bitrate/size 校准到实际拿到的档位，
    避免界面标「无损」而实际下载到 128k。
    """
    mid = song.get("id") or ""
    if not mid:
        return ""
    got, _ = qq_resolve([song], cookie)
    hit = got.get(mid)
    if not hit:
        return ""
    _qq_apply_tier(song, hit[0])
    return hit[1]


def qq_lyric(song_mid, cookie=""):
    """QQ 音乐歌词（JSONP + Base64）。"""
    url = ("https://c.y.qq.com/lyric/fcgi-bin/fcg_query_lyric_new.fcg"
           f"?songmid={song_mid}&g_tk=5381&loginUin=0&hostUin=0"
           "&inCharset=utf8&outCharset=utf-8&notice=0&platform=yqq"
           f"&needNewCode=0&pcachetime={int(time.time() * 1000)}")
    try:
        text = _decode(_http_get_bytes(url, referer="https://y.qq.com/",
                                       headers={"Cookie": cookie or "uin="}))
        j = json.loads(re.sub(r"^\s*[A-Za-z_]+\(|\)\s*$", "", text.strip()))
        raw = base64.b64decode(j.get("lyric") or "").decode("utf-8", "ignore")
        return _unescape_html(raw)
    except Exception:
        return ""


# ---------------------------------------------------------------------------
# 自定义 API（可配置多个）
# ---------------------------------------------------------------------------

def _custom_headers(token):
    hdrs = {}
    if token:
        hdrs["Authorization"] = f"Bearer {token}"
        hdrs["X-Token"] = token
    return hdrs


def custom_search(keyword, api_url, token="", limit=20):
    """自定义 API 搜索。"""
    base = api_url.rstrip("/")
    params = urllib.parse.urlencode({"q": keyword, "limit": limit})
    j = _http_get_json(f"{base}/search?{params}", headers=_custom_headers(token))
    items = j.get("data", j.get("result", []))
    if isinstance(items, dict):
        items = items.get("list", items.get("songs", []))
    songs = []
    for s in items if isinstance(items, list) else []:
        artist = s.get("artist", s.get("singer", ""))
        if isinstance(artist, list):
            artist = "/".join(str(a.get("name", a) if isinstance(a, dict) else a) for a in artist)
        song = {
            "id": str(s.get("id", s.get("songid", s.get("hash", "")))),
            "name": s.get("name", s.get("title", "")),
            "artist": str(artist),
            "album": str(s.get("album", s.get("albumName", ""))),
            "duration": s.get("duration", s.get("interval", 0)) or 0,
            "source": "custom",
            "cover": s.get("cover", s.get("pic", s.get("img", ""))) or "",
            "quality": s.get("quality", s.get("bitrate_label", "")) or "",
            "format": s.get("format", s.get("extension", "")) or "",
        }
        try:
            if s.get("size"):
                song["size"] = int(s["size"])
        except Exception:
            pass
        br = _kbps(s.get("bitrate") or s.get("br") or 0)
        if not br and song.get("size"):
            br = _bitrate_of(song["size"], song["duration"])
        if br:
            song["bitrate"] = br
            if not song.get("quality"):
                song["quality"] = _quality_label(br)
        if s.get("vip") or s.get("fee") or s.get("pay"):
            song["vip"] = True
        songs.append(song)
    return songs


def custom_url(song_id, api_url, token="", quality="flac"):
    """自定义 API 获取下载 URL。"""
    base = api_url.rstrip("/")
    params = urllib.parse.urlencode({"id": song_id, "quality": quality})
    j = _http_get_json(f"{base}/url?{params}", headers=_custom_headers(token))
    d = j.get("data", j)
    if isinstance(d, dict):
        return d.get("url", d.get("src", ""))
    return ""


def custom_lyric(song_id, api_url, token=""):
    """自定义 API 获取歌词。"""
    base = api_url.rstrip("/")
    params = urllib.parse.urlencode({"id": song_id})
    try:
        j = _http_get_json(f"{base}/lyric?{params}", headers=_custom_headers(token))
        d = j.get("data", j)
        if isinstance(d, dict):
            return d.get("lyric", d.get("lrc", "")) or ""
    except Exception:
        pass
    return ""


# ---------------------------------------------------------------------------
# 相关度评分
# ---------------------------------------------------------------------------

def _relevance_score(song, keyword):
    """计算歌曲与关键词的相关度分数（越高越相关）。"""
    name = song.get("name", "").lower()
    artist = song.get("artist", "").lower()
    album = song.get("album", "").lower()
    kw = keyword.lower().strip()
    kws = kw.split()

    score = 0
    if kw == name:
        score += 150
    elif any(w == name for w in kws):
        score += 100  # 歌名与关键词某部分完全一致（如关键词含歌手名时）
    elif kw in name:
        score += 60   # 歌名整串包含关键词：多为翻唱“歌手 - 歌名”命名，弱信号
    else:
        for w in kws:
            if w in name:
                score += 30
    for w in kws:
        if w in artist:
            score += 20
        if w in album:
            score += 5
    if kw == artist:
        score += 40
    # 信息完整度微调（有音质/大小信息的结果略优先）
    if song.get("quality"):
        score += 3
    if song.get("cover"):
        score += 2
    return score


# ---------------------------------------------------------------------------
# 统一入口
# ---------------------------------------------------------------------------

# 内置源注册表
BUILTIN_SOURCES = {
    "netease": {"name": "网易云", "search": netease_search},
    "migu": {"name": "咪咕", "search": migu_search},
    "kugou": {"name": "酷狗", "search": kugou_search},
    "kuwo": {"name": "酷我", "search": kuwo_search},
    "qq": {"name": "QQ音乐", "search": qq_search},
}

# 封面补源：按准确度优先逐个尝试，只列目前仍能返回封面直链的源
COVER_SOURCES = ("netease", "qq", "kugou", "migu")
_cover_cache = {}
_cover_cache_lock = threading.Lock()
COVER_CACHE_MAX = 512


def find_cover(name, artist="", album="", ttl=3600):
    """跨源找封面：返回 https 直链，找不到返回 ""。

    为何需要：酷我搜索接口的 PIC/PICPATH 已全为空，自建图床端点
    （track.kuwo.cn / mobile.kuwo.cn）也已 502/403，与其依赖单源修图床，
    不如拿歌名+歌手去仍有封面的源查一张。歌手必须能对上，
    否则冷门歌会拿到同歌名其它歌手的专辑图。
    结果（包括“没找到”）缓存 ttl 秒，避免列表每帧重新发搜索请求。
    """
    name = (name or "").strip()
    artist = (artist or "").strip()
    if not name:
        return ""
    key = (artist.lower(), name.lower())
    now = time.time()
    with _cover_cache_lock:
        hit = _cover_cache.get(key)
        if hit and hit[1] > now:
            return hit[0]
    kw = " ".join(x for x in (artist, name) if x)
    first_artist = artist.split("/")[0].strip().lower() if artist else ""
    url, score = "", 0
    for src in COVER_SOURCES:
        fn = (BUILTIN_SOURCES.get(src) or {}).get("search")
        if not fn:
            continue
        try:
            rows = fn(kw, 10) or []
        except Exception:
            continue
        for r in rows:
            cov = (r.get("cover") or "").strip()
            if not cov:
                continue
            if cov.startswith("http://"):
                cov = "https://" + cov[7:]
            if first_artist and first_artist not in (r.get("artist") or "").lower():
                continue
            sc = _relevance_score(r, kw)
            if sc > score:
                url, score = cov, sc
        if score >= 100:
            break
    with _cover_cache_lock:
        if len(_cover_cache) >= COVER_CACHE_MAX:
            _cover_cache.clear()
        _cover_cache[key] = (url, now + (ttl if url else 300))
    return url


def _norm_sources(value, allowed):
    """把 list / 逗号串 / 单个字符串归一化为字符串列表。"""
    if isinstance(value, str):
        return [v.strip() for v in value.split(",") if v.strip() in allowed]
    if isinstance(value, (list, tuple)):
        return [str(v).strip() for v in value if str(v).strip() in allowed]
    return []


def _is_error_payload(head: bytes, content_type: str = "") -> bool:
    """文件头/Content-Type 判定“这不是音频”。

    只认确定是文本的情况（<html/{json/文本 MIME），不做“可打印字符占比”
    这类启发式判断：带大 ID3v2 标签的合法 mp3 头部几乎全是可以打印的 ASCII，
    误判会把正常歌曲拒掉。
    """
    ct = (content_type or "").lower()
    if "text/" in ct or "application/json" in ct or "application/javascript" in ct:
        return True
    if head[:5].lower() == b"<!doc" or head[:5] == b"<html" or head[:5] == b"<?xml":
        return True
    if head[:1] in (b"{", b"["):
        return True
    return False


class MusicSearch:
    """多源网络音源搜索 / 下载 / 试听 / 歌词。"""

    def __init__(self, config: dict):
        config = config or {}
        # 启用的内置源（多选）
        self.enabled_sources = _norm_sources(
            config.get("enabled_sources", ""), list(BUILTIN_SOURCES.keys()))
        if not self.enabled_sources:
            self.enabled_sources = list(BUILTIN_SOURCES.keys())
        # 自定义 API 列表（可多个，每项带 enabled 开关）
        apis = []
        raw_apis = config.get("custom_apis")
        if isinstance(raw_apis, list):
            for a in raw_apis:
                if not isinstance(a, dict):
                    continue
                url = (a.get("url") or "").strip()
                if not url:
                    continue
                apis.append({
                    "name": (a.get("name") or "自定义").strip()[:20],
                    "url": url,
                    "token": (a.get("token") or "").strip(),
                    "enabled": bool(a.get("enabled", True)),
                })
        # 兼容旧配置（单个 custom_api_url）
        if not apis and config.get("custom_api_url"):
            apis.append({
                "name": "自定义",
                "url": config["custom_api_url"].strip(),
                "token": (config.get("custom_api_token") or "").strip(),
                "enabled": True,
            })
        self.custom_apis = apis
        # 平台显示别名（谐音）：默认启用；设置里显式清空某项 → 该项回退真实名称
        al = config.get("alias") or {}
        self.alias = dict(SOURCE_ALIAS_DEFAULT)
        if isinstance(al, dict):
            for k in SOURCE_ALIAS_DEFAULT:
                if k not in al:
                    continue
                v = str(al.get(k) or "").strip()[:12]
                if v:
                    self.alias[k] = v
                else:
                    self.alias.pop(k, None)
        # 各平台 VIP Cookie（用于试听/下载会员歌曲）
        ck = config.get("cookies") or {}
        self.cookies = {}
        if isinstance(ck, dict):
            for k in ("netease", "kugou", "kuwo", "migu", "qq"):
                v = (ck.get(k) or "").strip()
                if v and "****" not in v:
                    self.cookies[k] = v

    def src_name(self, key):
        """平台的显示名：优先谐音别名，未配别名用真实名称。"""
        return self.alias.get(key) or SOURCE_NAMES.get(key, key or "")

    # ---- 搜索 ----------------------------------------------------------
    def search(self, keyword, limit=20):
        """多源混合搜索：所有启用的内置源 + 启用的自定义 API 并发查询。

        limit 为合并后的返回上限；单源拉取量固定在 10~30，避免为凑数把每个源都拉满。
        """
        fetch = max(10, min(int(limit or 20), 30))
        results = []
        lock = threading.Lock()
        threads = []

        def _do(label, fn):
            try:
                songs = fn()
                with lock:
                    results.extend(songs)
            except Exception:
                pass  # 单源失败不影响其他源

        for key in self.enabled_sources:
            src = BUILTIN_SOURCES.get(key)
            if src:
                threads.append(threading.Thread(
                    target=_do, args=(key, lambda f=src["search"]: f(keyword, fetch))))

        for api in self.custom_apis:
            if api["enabled"]:
                threads.append(threading.Thread(
                    target=_do,
                    args=(api["name"],
                          lambda a=api: self._search_one_api(keyword, fetch, a))))

        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=12)

        # 去重（同名+同歌手只保留信息最全的一条）
        by_key = {}
        for s in results:
            key = f"{s.get('name','').lower()}|{s.get('artist','').lower()}"
            if not key.strip("|"):
                continue
            old = by_key.get(key)
            if old is None or self._completeness(s) > self._completeness(old):
                by_key[key] = s
        unique = list(by_key.values())

        # VIP 曲目：未配置对应平台 Cookie 时视为不可播放/下载（locked）
        for s in unique:
            if s.get("vip"):
                src = s.get("source", "")
                if src == "qq":
                    continue  # 付费标记不等于下不动，改由批量 vkey 实测判定
                if src == "custom" or not self.cookies.get(src):
                    s["locked"] = True

        # 可播放的排前，锁定（VIP 无 Cookie）的沉底；组内按相关度降序
        unique.sort(key=lambda s: (1 if s.get("locked") else 0,
                                   -_relevance_score(s, keyword)))

        # 保底：每个启用源至少保留一条最相关结果，避免被单一源的长尾淹没
        picked = unique[:limit]
        if len(picked) == limit:
            have = {s.get("source") for s in picked}
            missing = [k for k in self.enabled_sources if k not in have]
            if missing:
                pool = {}
                for s in unique:  # unique 已按分数降序，首个即该源最相关
                    pool.setdefault(s.get("source"), s)
                cands = [pool[k] for k in missing if k in pool and pool[k] not in picked]
                if cands:
                    reserved = {id(c) for c in cands}
                    idx = len(picked) - 1
                    for c in cands:
                        while idx >= 0 and id(picked[idx]) in reserved:
                            idx -= 1
                        if idx < 0:
                            break
                        picked[idx] = c
                        idx -= 1
                    picked.sort(key=lambda s: (1 if s.get("locked") else 0,
                                               -_relevance_score(s, keyword)))

        # 配置了 Cookie 的 VIP 曲目：实测能否取到试听链，取不到标记 unplayable 并沉底
        self._probe_playable(picked)
        # QQ 音乐：按档位逐批 vkey 实测，并校准确实能拿到的最高档位
        self._probe_qq(picked)
        picked.sort(key=lambda s: (1 if (s.get("locked") or s.get("unplayable")) else 0,
                                   -_relevance_score(s, keyword)))

        for s in picked:
            if s.get("source") == "custom":
                s["source_name"] = s.get("_api_name") or "自定义"
            else:
                s["source_name"] = self.src_name(s.get("source", ""))
        self._probe_lyrics(picked)
        return picked

    def _probe_lyrics(self, songs):
        """并发探测每条结果是否有歌词，写入 has_lyric（探测失败/超时则不置该字段）。"""
        if not songs:
            return
        sem = threading.Semaphore(8)

        def one(s):
            if s.get("source") == "migu":
                s["has_lyric"] = bool(s.get("_lyric_url"))
                return
            if not sem.acquire(timeout=5):
                return
            try:
                txt = self.get_lyric(s)
                s["has_lyric"] = bool(txt and txt.strip())
            except Exception:
                pass
            finally:
                sem.release()

        ts = [threading.Thread(target=one, args=(s,), daemon=True) for s in songs]
        for t in ts:
            t.start()
        deadline = 6.0
        for t in ts:
            t.join(timeout=deadline)

    def _probe_qq(self, songs):
        """QQ 音乐取链实测：按档位逐批 vkey，判定哪些曲目当前真的能播/能下。

        pay_down 只能说明“属付费曲目”，不等于拿不到链（大量付费标记曲游客态仍可取
        128k）；反过来有 Cookie 时 VIP 曲也能解锁。因此统一以接口返回为准：
        拿到 purl 就清除 locked/unplayable，拿不到才置位，避免“能下却置灰”误报。
        """
        todo = [s for s in songs if s.get("source") == "qq" and s.get("id")]
        if not todo:
            return
        cookie = self.cookies.get("qq", "")
        got, failed = qq_resolve(todo, cookie)
        for s in todo:
            hit = got.get(s["id"])
            if hit:
                _qq_apply_tier(s, hit[0])
                s.pop("locked", None)
                s.pop("unplayable", None)
            elif failed:
                continue  # 取链请求本身失败，不能据此判定不可播
            elif s.get("vip") and not cookie:
                s["locked"] = True
            else:
                s["unplayable"] = True

    def _probe_playable(self, songs):
        """并发探测「配置了 Cookie 的 VIP 曲目」能否真正取到试听链。

        配置 Cookie 后 VIP 曲目不再标记 locked，但 Cookie 无效/账号无会员时
        取链仍会失败，造成“按钮可点却播不了”；此处提前探测并标记 unplayable，
        由前端置灰播放按钮并沉底显示。
        """
        todo = [s for s in songs
                if s.get("vip") and not s.get("locked")
                and s.get("source") != "qq"          # QQ 走 _probe_qq 批量实测
                and self.cookies.get(s.get("source", ""))]
        if not todo:
            return
        sem = threading.Semaphore(6)

        def one(s):
            if not sem.acquire(timeout=5):
                return
            try:
                if not self.get_url(s):
                    s["unplayable"] = True
            except Exception:
                s["unplayable"] = True
            finally:
                sem.release()

        ts = [threading.Thread(target=one, args=(s,), daemon=True) for s in todo]
        for t in ts:
            t.start()
        for t in ts:
            t.join(timeout=6)

    @staticmethod
    def _completeness(song):
        n = 0
        for k in ("cover", "size", "quality", "bitrate", "format", "duration", "album"):
            if song.get(k):
                n += 1
        return n

    def _search_one_api(self, keyword, limit, api):
        songs = custom_search(keyword, api["url"], api["token"], limit)
        for s in songs:
            s["_api_name"] = api["name"]
            s["_api_url"] = api["url"]
            s["_api_token"] = api["token"]
        return songs

    # ---- 播放 / 歌词 / 下载链接 ------------------------------------------
    def _find_api(self, song):
        name = song.get("_api_name")
        url = song.get("_api_url")
        for a in self.custom_apis:
            if url and a["url"] == url:
                return a
            if name and a["name"] == name:
                return a
        return self.custom_apis[0] if self.custom_apis else None

    def get_url(self, song):
        """获取歌曲下载 URL（可能为 302 前置地址）。"""
        source = song.get("source", "netease")
        if source == "custom":
            api = self._find_api(song)
            if api:
                return custom_url(song.get("id", ""), api["url"], api["token"])
            return ""
        sid = song.get("id", "")
        if source == "netease":
            return netease_url(sid, self.cookies.get("netease", ""))
        if source == "migu":
            return migu_url(sid, song, self.cookies.get("migu", ""))
        if source == "kugou":
            return kugou_url(sid, song.get("_album_id", song.get("album_id", "")),
                            self.cookies.get("kugou", ""))
        if source == "kuwo":
            return kuwo_url(sid)
        if source == "qq":
            return qq_url(song, self.cookies.get("qq", ""))
        return ""

    def get_preview(self, song):
        """获取试听播放地址（解析重定向后的真实直链）。"""
        url = self.get_url(song)
        if not url:
            return ""
        # 尝试解析一次重定向，失败则原样返回（浏览器可跟随）
        source = song.get("source", "")
        real = _redirect_location(url, referer=SOURCE_REFERERS.get(source, "https://music.163.com/"),
                                  cookie=self.cookies.get(source, ""))
        return real or url

    def get_cover(self, song):
        """结果封面直链：自带则用（统一升 https），为空时跨源补一张。

        旧行为是“没封面就空着”，前端只能显示占位图；而酷我等源的
        图床接口已失效，补一次跨源查询成本只有一个 GET。
        """
        cov = (song.get("cover") or "").strip()
        if cov.startswith("http://"):
            cov = "https://" + cov[7:]
        if cov:
            return cov
        return find_cover(song.get("name", ""), song.get("artist", ""),
                          song.get("album", ""))

    def get_lyric(self, song):
        """获取歌词文本。"""
        source = song.get("source", "")
        sid = song.get("id", "")
        if source == "netease":
            return netease_lyric(sid)
        if source == "kugou":
            return kugou_lyric(sid)
        if source == "kuwo":
            return kuwo_lyric(sid)
        if source == "migu":
            return migu_lyric(song)
        if source == "qq":
            return qq_lyric(sid, self.cookies.get("qq", ""))
        if source == "custom":
            api = self._find_api(song)
            if api:
                return custom_lyric(sid, api["url"], api["token"])
        return ""

    # ---- 下载 ----------------------------------------------------------
    def download(self, song, save_dir, progress=None, cancel=None):
        """下载歌曲到 save_dir，返回 {ok, path, error}。

        progress(received, total): 字节级进度回调（total 可能为 0=未知）。
        cancel(): 返回 True 时中断并保留 .part 断点，下次续传。
        支持断点续传：服务端返回 206 时从 .part 已有大小继续。
        """
        try:
            url = self.get_url(song)
            if not url:
                return {"ok": False, "error": "未获取到下载链接（可能为 VIP 歌曲）"}

            artist = re.sub(r'[<>:"/\\|?*]', '', song.get("artist", ""))
            name = re.sub(r'[<>:"/\\|?*]', '', song.get("name", "unknown"))
            stem = f"{artist} - {name}" if artist else name
            part = os.path.join(save_dir, stem + ".part")
            os.makedirs(save_dir, exist_ok=True)

            have = os.path.getsize(part) if os.path.exists(part) else 0
            req = urllib.request.Request(url)
            req.add_header("User-Agent", UA)
            referer = SOURCE_REFERERS.get(song.get("source", ""), "https://music.163.com/")
            req.add_header("Referer", referer)
            ck = self.cookies.get(song.get("source", ""), "")
            if ck:
                req.add_header("Cookie", ck)
            if have > 0:
                req.add_header("Range", f"bytes={have}-")
            resp = urllib.request.urlopen(req, timeout=60)

            status = getattr(resp, "status", 200)
            if have > 0 and status != 206:
                have = 0  # 服务端不支持续传，从头下载
            total = have
            clen = resp.headers.get("Content-Length")
            if clen:
                try:
                    total += int(clen)
                except ValueError:
                    pass

            head = b""
            mode = "ab" if have > 0 else "wb"
            received = have
            try:
                with open(part, mode) as f:
                    while True:
                        if cancel and cancel():
                            return {"ok": False, "error": "cancelled", "partial": True}
                        chunk = resp.read(65536)
                        if not chunk:
                            break
                        if len(head) < 16:
                            head = (head + chunk)[:16]
                        f.write(chunk)
                        received += len(chunk)
                        if progress:
                            progress(received, total)
            except Exception:
                # 网络中断：保留 .part 供下次续传
                raise

            if total and received < total:
                # 对端提前断流时 read() 只会返回空并结束循环，旧实现当作下载完成，
                # 截断文件改名入库，表现为「下载成功但播不了 / 时长不对」。
                # 保留 .part，下次重新发起时按 Range 续传。
                return {"ok": False, "partial": True,
                        "error": f"下载不完整（{received}/{total} 字节），已保留断点"}

            ct = ""
            try:
                ct = (resp.headers.get("Content-Type") or "").lower()
            except Exception:
                pass
            if _is_error_payload(head, ct):
                # 防盗链 / 需登录 / 限流时 CDN 会回一段 HTML 或 JSON，
                # 旧实现不校验内容，错误页会被存成 .mp3 并计为“下载成功”。
                try:
                    os.remove(part)
                except OSError:
                    pass
                return {"ok": False, "error": "对端返回的不是音频（可能需登录或被限流）"}

            if received - have < 1024 and have == 0:
                try:
                    os.remove(part)
                except OSError:
                    pass
                return {"ok": False, "error": "下载文件过小，可能无效"}

            ext = self._detect_ext(resp.geturl() or url, resp, head)
            filepath = self._unique_path(os.path.join(save_dir, f"{stem}.{ext}"))
            try:
                os.replace(part, filepath)
            except OSError:
                with open(part, "rb") as src, open(filepath, "wb") as dst:
                    while True:
                        b = src.read(1 << 20)
                        if not b:
                            break
                        dst.write(b)
                os.remove(part)

            return {"ok": True, "path": filepath}
        except urllib.error.HTTPError as e:
            return {"ok": False, "error": f"HTTP {e.code}: {e.reason}"}
        except Exception as e:
            return {"ok": False, "error": str(e)}

    @staticmethod
    def _detect_ext(url, resp, data):
        """从 URL/Content-Type/文件头检测音频扩展名。"""
        path = urllib.parse.urlparse(url).path.lower()
        for e in ("flac", "mp3", "m4a", "ogg", "opus", "wav", "ape"):
            if f".{e}" in path:
                return e

        ct = ""
        try:
            ct = resp.headers.get("Content-Type", "").lower()
        except Exception:
            pass
        for ext, mime in (("flac", "audio/flac"), ("flac", "audio/x-flac"),
                          ("mp3", "audio/mpeg"), ("mp3", "audio/mp3"),
                          ("m4a", "audio/mp4"), ("m4a", "audio/m4a"), ("m4a", "audio/aac"),
                          ("ogg", "audio/ogg"), ("opus", "audio/opus"),
                          ("wav", "audio/wav"), ("wav", "audio/wave")):
            if mime in ct:
                return ext

        if data[:4] == b"fLaC":
            return "flac"
        if data[:3] == b"ID3" or data[:2] == b"\xff\xfb":
            return "mp3"
        if data[4:8] == b"ftyp":
            return "m4a"
        if data[:4] == b"OggS":
            return "ogg"
        return "mp3"

    @staticmethod
    def _unique_path(filepath):
        """避免重名，自动加编号。"""
        if not os.path.exists(filepath):
            return filepath
        base, ext = os.path.splitext(filepath)
        i = 1
        while os.path.exists(f"{base} ({i}){ext}"):
            i += 1
        return f"{base} ({i}){ext}"
