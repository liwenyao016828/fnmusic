# -*- coding: utf-8 -*-
"""
playlist.py — 多平台歌单导入与本地匹配（纯 Python 标准库）

支持平台：
  网易云音乐  music.163.com            （api/playlist/detail）
  QQ 音乐     y.qq.com                 （fcg_ucc_getcdinfo_byids_cp）
  酷狗音乐    kugou.com                （mobiles.kugou.com/api/v5/special/song）
  酷我音乐    kuwo.cn                  （playlist_detail 页面 SSR 解析，仅页面可见歌曲）

功能：
  - 从分享链接/文本解析平台与歌单 ID
  - 拉取歌单歌曲列表（标题/歌手/专辑）
  - 与本地音乐库匹配（精确→模糊）
  - 生成 m3u8 播放列表 / 整理为「歌手/专辑」目录或「歌单名」专辑目录
"""

import os
import re
import json
import urllib.request
import urllib.parse
import shutil
import html as html_lib

UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
TIMEOUT = 15


class PlaylistError(Exception):
    pass


def _http_get(url, referer=None, headers=None):
    req = urllib.request.Request(url, headers={
        "User-Agent": UA,
        "Accept": "*/*",
    })
    if referer:
        req.add_header("Referer", referer)
    if headers:
        for k, v in headers.items():
            req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
            return r.read()
    except Exception as e:
        raise PlaylistError(f"网络请求失败: {e}")


# ----------------------------------------------------------------------------
# 1. 链接解析
# ----------------------------------------------------------------------------

_PLATFORM_PATTERNS = [
    ("netease", re.compile(r"(?:music\.163\.com|163\.cn).*?(?:playlist[=/]|playlist\?id=)(\d+)")),
    ("netease", re.compile(r"163\.cn/#/playlist\?id=(\d+)")),
    ("qq", re.compile(r"(?:y\.qq\.com|i\.y\.qq\.com).*?(?:playlist[=/]|playlist\?id=)(\d+)")),
    ("qq", re.compile(r"y\.qq\.com/n/ryqq/playlist/(\d+)")),
    ("kugou", re.compile(r"kugou\.com/yy/special/single/(\d+)")),
    ("kugou", re.compile(r"kugou\.com/yy/playlist/(\d+)")),
    ("kugou", re.compile(r"kugou\.com/share/.*?specialid=(\d+)")),
    ("kuwo", re.compile(r"kuwo\.cn/playlist_detail/(\d+)")),
]

_RAW_IDS = {
    "netease": re.compile(r"^\s*(\d{6,12})\s*$"),
}


def parse_link(text):
    """解析歌单链接/文本 → (platform, pid)。失败返回 None。"""
    text = (text or "").strip()
    if not text:
        return None
    for platform, pat in _PLATFORM_PATTERNS:
        m = pat.search(text)
        if m:
            return platform, m.group(1)
    # 纯数字：默认按网易云试（用户在 UI 可能只填 ID）
    for platform, pat in _RAW_IDS.items():
        m = pat.match(text)
        if m:
            return platform, m.group(1)
    return None


# ----------------------------------------------------------------------------
# 2. 各平台歌单获取
# ----------------------------------------------------------------------------

def _normalize_song(title, artist, album=None):
    title = (title or "").strip()
    artist = (artist or "").strip()
    if not title:
        return None
    return {"title": title, "artist": artist, "album": (album or "").strip()}


def _netease_get(url, referer="https://music.163.com/", data=None):
    headers = {"User-Agent": UA, "Referer": referer}
    req = urllib.request.Request(url, headers=headers, data=data)
    if data:
        req.add_header("Content-Type", "application/x-www-form-urlencoded")
    with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
        return json.loads(r.read().decode("utf-8", "ignore"))


def fetch_netease(pid):
    """网易云歌单：旧 api/playlist/detail 已普遍返回 20001（需登录），
    改用 v6/playlist/detail 拿全量 trackIds + v3/song/detail 分批补歌曲详情。"""
    playlist = None
    try:
        d = _netease_get(f"https://music.163.com/api/v6/playlist/detail?id={pid}&n=100000")
        playlist = d.get("playlist")
        code = d.get("code")
    except Exception as e:
        raise PlaylistError(f"网易云歌单请求失败: {e}")
    if not playlist:
        hint = {20001: "（歌单为隐私/需要登录）", 404: "（歌单不存在）"}.get(code, "")
        raise PlaylistError(f"网易云歌单无法访问{hint}")
    name = playlist.get("name") or "网易云歌单"
    cover = playlist.get("coverImgUrl") or ""
    track_ids = [t.get("id") for t in (playlist.get("trackIds") or []) if t.get("id")]
    songs = []
    # 分批取详情（每批 50，最多 2000 首，避免超长 URL/过久等待）
    for i in range(0, min(len(track_ids), 2000), 50):
        batch = track_ids[i:i + 50]
        try:
            body = urllib.parse.urlencode(
                {"c": json.dumps([{"id": x} for x in batch])}).encode()
            d2 = _netease_get("https://music.163.com/api/v3/song/detail", data=body)
        except Exception:
            continue
        for s in (d2.get("songs") or []):
            artist = "/".join(a.get("name", "") for a in (s.get("ar") or []) if a.get("name"))
            album = (s.get("al") or {}).get("name")
            n = _normalize_song(s.get("name"), artist, album)
            if n:
                songs.append(n)
    # 兜底：详情接口全失败时退回 playlist 内嵌 tracks（通常仅前 几十 首）
    if not songs:
        for t in (playlist.get("tracks") or []):
            artist = "/".join(a.get("name", "") for a in (t.get("ar") or t.get("artists") or []) if a.get("name"))
            n = _normalize_song(t.get("name"), artist, (t.get("al") or t.get("album") or {}).get("name"))
            if n:
                songs.append(n)
    if not songs:
        raise PlaylistError("歌单中没有可访问的歌曲（可能全部为 VIP/下架曲目）")
    return {"name": name, "cover": cover, "songs": songs}


def fetch_qq(pid):
    url = ("https://c.y.qq.com/qzone/fcg-bin/fcg_ucc_getcdinfo_byids_cp.fcg"
           f"?type=1&json=1&utf8=1&onlysong=0&disstid={pid}&format=json&new_format=1")
    try:
        raw = _http_get(url, referer="https://y.qq.com/")
        data = json.loads(raw.decode("utf-8", "ignore"))
    except Exception as e:
        raise PlaylistError(f"QQ 音乐歌单解析失败: {e}")
    cdlist = data.get("cdlist") or []
    if not cdlist:
        raise PlaylistError("QQ 音乐歌单不存在或为空")
    pl = cdlist[0]
    songs = []
    for t in (pl.get("songlist") or []):
        title = t.get("name") or t.get("songname") or t.get("title")
        artist = "/".join(s.get("name", "") for s in (t.get("singer") or []) if s.get("name"))
        album = (t.get("album") or {})
        album_name = album.get("name") if isinstance(album, dict) else None
        if not album_name:
            album_name = t.get("albumname")
        s = _normalize_song(title, artist, album_name)
        if s:
            songs.append(s)
    return {"name": pl.get("dissname") or "QQ 歌单", "cover": (pl.get("logo") or ""), "songs": songs}


def fetch_kugou(pid):
    songs = []
    page = 1
    total = None
    while True:
        url = f"https://mobiles.kugou.com/api/v5/special/song?specialid={pid}&page={page}&pagesize=50"
        try:
            raw = _http_get(url)
            data = json.loads(raw.decode("utf-8", "ignore"))
        except Exception as e:
            raise PlaylistError(f"酷狗歌单解析失败: {e}")
        info = (data.get("data") or {}).get("info") or []
        if not info:
            break
        for it in info:
            # filename 形如 "歌手 - 歌名"；或 songname/singername 单独字段
            fn = it.get("filename") or ""
            title, artist = None, None
            if " - " in fn:
                artist, title = fn.split(" - ", 1)
            if not title:
                title = it.get("songname")
            if not artist:
                artist = it.get("singername")
            s = _normalize_song(title, artist, it.get("album_name"))
            if s:
                songs.append(s)
        total = (data.get("data") or {}).get("total")
        if not total or page * 50 >= int(total):
            break
        page += 1
    if not songs:
        raise PlaylistError("酷狗歌单不存在或为空")
    return {"name": "酷狗歌单", "cover": "", "songs": songs}


def fetch_kuwo(pid):
    url = f"https://www.kuwo.cn/playlist_detail/{pid}"
    try:
        raw = _http_get(url, referer="https://www.kuwo.cn/")
        h = raw.decode("utf-8", "ignore")
    except Exception as e:
        raise PlaylistError(f"酷我歌单解析失败: {e}")
    m = re.search(r"<title>([^<]*)</title>", h)
    name = html_lib.unescape(m.group(1)).replace("_酷我音乐", "").strip() if m else "酷我歌单"
    items = re.findall(
        r'<a title="([^"]+)" href="/play_detail/(\d+)"[^>]*class="name"[^>]*>.*?</div>\s*<div class="song_artist"[^>]*>(.*?)</div>',
        h, re.S)
    songs = []
    for title, _sid, art_block in items:
        arts = re.findall(r"<a[^>]*>([^<]+)</a>|<span[^>]*>([^<]+)</span>", art_block)
        singers = [html_lib.unescape((a or b).strip()) for a, b in arts if (a or b).strip()]
        s = _normalize_song(html_lib.unescape(title), "/".join(singers))
        if s:
            songs.append(s)
    if not songs:
        raise PlaylistError("酷我歌单解析失败（页面无歌曲）")
    return {"name": name, "cover": "", "songs": songs, "limited": True}


_FETCHERS = {"netease": fetch_netease, "qq": fetch_qq, "kugou": fetch_kugou, "kuwo": fetch_kuwo}


def fetch_playlist(platform, pid):
    fn = _FETCHERS.get(platform)
    if not fn:
        raise PlaylistError(f"不支持的平台: {platform}")
    return fn(pid)


# ----------------------------------------------------------------------------
# 3. 本地匹配
# ----------------------------------------------------------------------------

def _norm(s):
    """规范化标题/歌手：去空白、标点、括号注释，转小写。"""
    if not s:
        return ""
    s = re.sub(r"[\(（\[【][^\)）\]】]*[\)）\]】]", "", s)  # 去括号内容
    s = re.sub(r"[^\w\u4e00-\u9fff]+", "", s.lower())
    return s.strip()


def match_song(song, local_files):
    """在本地文件列表中匹配单首歌。

    local_files: [{"path","title","artist","album"}]，title/artist 可能为空。
    返回 (best_path, score) 或 (None, 0)。score: 3 精确、2 仅标题、1 模糊。
    """
    t = _norm(song.get("title", ""))
    a = _norm(song.get("artist", ""))
    if not t:
        return None, 0
    best = None
    best_score = 0
    for lf in local_files:
        lt = _norm(lf.get("title") or "")
        la = _norm(lf.get("artist") or "")
        score = 0
        if lt and lt == t:
            if a and la and la == a:
                score = 3
            else:
                score = 2
        elif lt and (t in lt or lt in t):
            score = 1
        if score > best_score:
            best_score = score
            best = lf["path"]
    return best, best_score


# ----------------------------------------------------------------------------
# 4. 输出：m3u / 专辑目录
# ----------------------------------------------------------------------------

def _safe_name(name):
    name = re.sub(r"[\\/:*?\"<>|]", "_", name or "")
    name = name.strip().strip(".")
    return name[:120] or "歌单"


def write_m3u(songs, out_path):
    """写 m3u8 播放列表。songs 为已匹配条目列表。"""
    with open(out_path, "w", encoding="utf-8") as f:
        f.write("#EXTM3U\n")
        for s in songs:
            if s.get("path"):
                f.write(f"#EXTINF:-1,{s['artist']} - {s['title']}\n")
                f.write(f"{s['path']}\n")
    return out_path


def build_album_dir(songs, out_dir, mode="link"):
    """把匹配歌曲整理进「歌单名」目录。mode: link=硬链接（同卷）, copy=复制。"""
    os.makedirs(out_dir, exist_ok=True)
    made = []
    for s in songs:
        p = s.get("path")
        if not p:
            continue
        dest = os.path.join(out_dir, os.path.basename(p))
        if os.path.exists(dest):
            dest = os.path.join(out_dir, f"{s['artist']} - {s['title']}{os.path.splitext(p)[1]}")
        try:
            if mode == "link":
                try:
                    os.link(p, dest)
                except OSError:
                    shutil.copy2(p, dest)
            else:
                shutil.copy2(p, dest)
            made.append(dest)
        except Exception:
            continue
    return made


def import_playlist(engine, text, out_dir, mode="m3u"):
    """歌单导入主入口。

    engine: 提供 local_files() 返回 [{path,title,artist,album}] 与 all_folders()。
    mode: m3u | album
    返回结果 dict。
    """
    parsed = parse_link(text)
    if not parsed:
        raise PlaylistError("无法识别歌单链接，请粘贴分享链接或歌单 ID")
    platform, pid = parsed
    pl = fetch_playlist(platform, pid)
    if not pl["songs"]:
        raise PlaylistError("歌单中没有歌曲")
    # 本地文件列表
    local_files = engine.local_files() or []
    matched = []
    for song in pl["songs"]:
        path, score = match_song(song, local_files)
        matched.append({**song, "path": path, "score": score})
    hit = [m for m in matched if m["path"]]
    miss = [m for m in matched if not m["path"]]

    result = {
        "ok": True,
        "platform": platform,
        "playlist_name": pl["name"],
        "cover": pl.get("cover", ""),
        "total": len(pl["songs"]),
        "matched": len(hit),
        "missed": len(miss),
        "limited": pl.get("limited", False),
        "out_dir": out_dir,
        "files": [],
        "miss_list": [{"title": m["title"], "artist": m["artist"]} for m in miss[:50]],
    }
    if not os.path.isdir(out_dir):
        raise PlaylistError(f"输出目录不存在: {out_dir}")
    safe = _safe_name(pl["name"])
    if mode == "album":
        album_dir = os.path.join(out_dir, safe)
        made = build_album_dir(hit, album_dir, mode="link")
        result["mode"] = "album"
        result["album_dir"] = album_dir
        result["files"] = made
    else:
        m3u_path = os.path.join(out_dir, f"{safe}.m3u8")
        write_m3u(hit, m3u_path)
        result["mode"] = "m3u"
        result["playlist_file"] = m3u_path
    return result


if __name__ == "__main__":
    # 简单自测
    import sys
    link = sys.argv[1] if len(sys.argv) > 1 else ""
    parsed = parse_link(link)
    print("parsed:", parsed)
    if parsed:
        pl = fetch_playlist(*parsed)
        print("name:", pl["name"], "songs:", len(pl["songs"]))
        for s in pl["songs"][:5]:
            print(" ", s)
