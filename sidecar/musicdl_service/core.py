"""曲率 · musicdl sidecar 的**纯逻辑**部分（不 import 任何第三方库）。

为什么把它单独拆出来：这一层要能在**没装 musicdl / fastapi 的机器上**跑单测
（`python3 -m unittest`，零依赖）。真正的网络调用与 HTTP 服务在 `app.py` 里。

# 平台短码

曲率内部用两字母短码标识平台（`UnifiedSong.Source`）。musicdl 用自己的源名
（`KuGouMusicClient` 这种）。两边的映射**必须显式写死**，不能靠字符串推导 ——
「去掉 MusicClient 再小写」那种写法会在 `HTQYY` / `FiveSing` 这类名字上出错，
参考实现为此专门绕了一圈去查注册表。

| musicdl 源 | 曲率短码 | 曲率原生支持情况 |
|---|---|---|
| NeteaseMusicClient  | `wy` | 搜索 ✓ 直链 ✓ |
| QQMusicClient       | `tx` | 搜索 ✓ 直链 ✓ |
| KuGouMusicClient    | `kg` | 搜索 ✓ **直链 ✗**（err_code 30020） |
| KuwoMusicClient     | `kw` | 搜索 ✓ **直链 ✗**（The request is illegal!） |
| MiguMusicClient     | `mg` | 都没有 |
| QianqianMusicClient | `bq` | 都没有 |
| BilibiliMusicClient | `bi` | 都没有 |

`wy` / `tx` 走 sidecar 是**重复能力**（原生那两条又快又稳），默认不开；
`kg` / `kw` 打开后**搜索与解析都改走 musicdl** —— 同一个平台的 id 不能一半原生
一半外挂，否则解析时拿到的 id 对不上。
"""

# 曲率短码 -> musicdl 客户端全名（**只列与曲率原生平台重叠、必须显式定短码的那几个**）
#
# 其余几十个源不在这里：它们的短码由 `short_code()` 从客户端名派生
# （`AppleMusicClient` → `applemusic`）。真机上 musicdl 注册了 **56 个源**，
# 一张写死的表既列不全、也会在上游加源时悄悄漏掉。
ALIASES = {
    "wy": "NeteaseMusicClient",
    "tx": "QQMusicClient",
    "kg": "KuGouMusicClient",
    "kw": "KuwoMusicClient",
    "mg": "MiguMusicClient",
    "bq": "QianqianMusicClient",
    "bi": "BilibiliMusicClient",
}

# 反查表：客户端全名 -> 曲率短码（小写比较）
_CLIENT_TO_SHORT = {full.lower(): short for short, full in ALIASES.items()}

_SUFFIX = "MusicClient"

# 已知短码集合：起始只有 ALIASES，`register_known()` 会把 musicdl 注册表派生出来的
# 也并进来。
#
# ⚠️ 为什么要这个集合而不是「非空就当平台」：`normalize_sources` 要能**丢掉**用户
# 写错的平台名（`nope`），`parse_song_id` 要能拒绝把 `zz:123` 这种外来 id 切开。
# 而源有 56 个、短码是派生的 —— 只能靠「注册表告诉过我们哪些」来判断，
# 不能靠「看着像个词就当平台」。
_KNOWN_SHORTS = set(ALIASES)


# 短码 -> 客户端全名（由 register_known 填充，只装我们真见过的名字）
_CLIENT_BY_SHORT = {}


def register_known(client_names):
    """把 musicdl 报出来的客户端全名并进已知集合（app.py 启动时调一次）。

    没装 musicdl 时不会被调 —— 那种情况下只认 ALIASES 里那几个，行为与以前一致。
    """
    for name in client_names or []:
        sid = short_code(name)
        if sid:
            _KNOWN_SHORTS.add(sid)
            _CLIENT_BY_SHORT[sid] = name
    return _KNOWN_SHORTS


def short_code(client_name):
    """musicdl 的客户端全名 → 曲率短码。

    已知的走 `ALIASES`；其余按规则派生：去掉 `MusicClient` 后缀再小写。
    派生而不是写死，是因为源有 56 个、上游还在加 —— 写死的表一定会漏，
    而漏掉的表现是「界面上看不到这个源」或者「用户配了它却被丢掉」。
    """
    name = str(client_name or "").strip()
    if not name:
        return ""
    hit = _CLIENT_TO_SHORT.get(name.lower())
    if hit:
        return hit
    if name.endswith(_SUFFIX):
        name = name[: -len(_SUFFIX)]
    return name.lower()


def client_name(short):
    """曲率短码 → musicdl 客户端全名。认不出返回空串。

    ⚠️ 派生短码（`applemusic`）**没法靠规则还原大小写**（真名是
    `AppleMusicClient`），所以这里只信 ALIASES 与 `register_known()` 记下来的
    那张反查表；两者都没有就返回空串，让调用方报错而不是瞎猜一个名字。
    """
    key = str(short or "").strip().lower()
    if key in ALIASES:
        return ALIASES[key]
    return _CLIENT_BY_SHORT.get(key, "")


# 几个常见源的中文名（界面上好看）。没列到的就用派生名，不硬凑。
LABELS = {
    "wy": "网易云音乐", "tx": "QQ 音乐", "kg": "酷狗音乐", "kw": "酷我音乐",
    "mg": "咪咕音乐", "bq": "千千音乐", "bi": "B站",
    "spotify": "Spotify", "applemusic": "Apple Music", "youtube": "YouTube",
    "tidal": "TIDAL", "deezer": "Deezer", "qobuz": "Qobuz", "joox": "JOOX",
    "soundcloud": "SoundCloud", "jamendo": "Jamendo", "audius": "Audius",
    "itunes": "iTunes", "moov": "MOOV", "jiosaavn": "JioSaavn",
    "ximalaya": "喜马拉雅", "qingting": "蜻蜓 FM", "lizhi": "荔枝",
    "soda": "汽水音乐", "migu": "咪咕音乐", "suno": "Suno",
}


def label_for(short):
    """短码 → 界面显示名。认不出就用短码本身（不编中文名）。"""
    return LABELS.get(str(short or "").strip().lower(), str(short or ""))


def catalog(registered=None, enabled=None):
    """把 musicdl 注册的源整理成界面要的清单。

    `registered` 是 musicdl 运行时报出来的客户端全名列表（拿不到就只列 ALIASES）。
    返回按「曲率短码」排序的列表，每项：`{id, label, client, enabled, native}`。

    `native=True` 表示这个源与曲率原生平台重叠（wy/tx/kg/kw）—— 界面上要提示
    「走它会换掉原生实现」，因为一个平台只能有一个 id 空间。
    """
    enabled = set(enabled or [])
    native = {"wy", "tx", "kg", "kw"}
    names = list(registered) if registered else list(ALIASES.values())
    out, seen = [], set()
    for name in names:
        sid = short_code(name)
        if not sid or sid in seen:
            continue
        seen.add(sid)
        out.append({
            "id": sid,
            "label": label_for(sid),
            "client": name,
            "enabled": sid in enabled,
            "native": sid in native,
        })
    # 兜底：注册表里没有、但 ALIASES 里有的（例如 musicdl 版本差异）也要出现
    for sid, name in ALIASES.items():
        if sid in seen:
            continue
        out.append({"id": sid, "label": label_for(sid), "client": name,
                    "enabled": sid in enabled, "native": sid in native})
    return sorted(out, key=lambda x: x["id"])

# 默认启用的源：**只加新能力，不动现有行为**。
#
# 咪咕 / 千千 / B站 曲率原生没有 → 纯增量，默认开。
# 酷狗 / 酷我 原生「搜得到、放不出」，交给 musicdl 才能播 —— 但那会把搜索也换掉
# （id 空间要一致），属于改变现有行为，所以默认关，用户想听那两家再开。
# 网易 / QQ 原生就是全的，走 sidecar 纯属重复，默认关。
DEFAULT_SOURCES = ["mg", "bq", "bi"]

# 默认单源返回条数。
#
# ⚠️ 不能贪多：musicdl 的库按这个数量**翻页**，而且每一条都会先解析直链 ——
# 30 条会变成 6 页，整次 search() 在超时前一条都交不出来（参考实现踩过这个坑）。
DEFAULT_LIMIT = 15
MAX_LIMIT = 30

# musicdl 自己的短名（`KuGouMusicClient` → `kugou`）也认：用户多半是照着 musicdl
# 的文档写的，不该因为我们内部叫 `kg` 就把他的配置丢掉。
_SHORT_ALIASES = {
    full[: -len(_SUFFIX)].lower(): short
    for short, full in ALIASES.items()
    if full.endswith(_SUFFIX)
}


def normalize_sources(raw):
    """把外部传进来的源列表归一成短码列表：去空白、小写、去重、丢掉不认识的。

    认不出的**丢掉而不是报错**：一个平台改名不该让整次搜索失败；调用方从
    `/healthz` 的 `sources` 里能看到实际生效了哪些。
    """
    if raw is None:
        return list(DEFAULT_SOURCES)
    if isinstance(raw, str):
        raw = raw.split(",")
    out = []
    for item in raw:
        key = str(item or "").strip().lower()
        # 认得的写法：曲率短码（`mg`）/ musicdl 短名（`migu`）/ 客户端全名（`MiguMusicClient`）
        if key in _SHORT_ALIASES:
            key = _SHORT_ALIASES[key]
        elif key.endswith(_SUFFIX.lower()):
            key = short_code(key)
        # 认不出的**丢掉**（不报错）：一个平台改名不该让整次搜索失败。
        # 判据是「注册表告诉过我们」而不是「看着像个词」—— 见 _KNOWN_SHORTS。
        if key in _KNOWN_SHORTS and key not in out:
            out.append(key)
    return out


def clamp_limit(n, default=DEFAULT_LIMIT):
    try:
        n = int(n)
    except (TypeError, ValueError):
        return default
    if n <= 0:
        return default
    return min(n, MAX_LIMIT)


def song_id(short, identifier):
    """曲目 id 的拼法：`<短码>:<平台内 id>`。

    带上短码是为了让 id 自解释 —— 排障时看一眼就知道这条是哪个源给的，
    也让「不同源的平台内 id 撞号」不会串。
    """
    return "%s:%s" % (short, identifier or "")


def parse_song_id(sid):
    """`<短码>:<平台内 id>` → (短码, 平台内 id)。认不出短码时返回 ("", 原串)。"""
    s = str(sid or "")
    if ":" not in s:
        return "", s
    short, _, rest = s.partition(":")
    if short in _KNOWN_SHORTS:
        return short, rest
    return "", s


def normalize_song(song, short, keyword=""):
    """musicdl 的 SongInfo 对象 → 我们自己的 JSON 条目。

    ⚠️ 全程 `getattr(..., 默认值)`：musicdl 各源的字段**不一致**（有的没有
    albummid、有的没有 file_size），缺字段是常态不是异常。
    """
    identifier = str(getattr(song, "identifier", "") or "")
    return {
        "id": song_id(short, identifier),
        "source": short,
        "platform_id": identifier,
        "title": str(getattr(song, "song_name", "") or ""),
        "artist": str(getattr(song, "singers", "") or ""),
        "album": str(getattr(song, "album", "") or ""),
        "duration_s": int(getattr(song, "duration_s", 0) or 0),
        "ext": str(getattr(song, "ext", "") or "mp3"),
        "file_size": int(getattr(song, "file_size_bytes", 0) or 0),
        "cover_url": str(getattr(song, "cover_url", "") or ""),
        "album_mid": str(getattr(song, "albummid", "") or getattr(song, "album_mid", "") or ""),
        "download_url": str(getattr(song, "download_url", "") or ""),
        "download_headers": dict(getattr(song, "download_headers", None) or {}),
        "lyric": str(getattr(song, "lyric", "") or ""),
        "keyword": keyword,
    }


def is_trial(title):
    """标题里带「试听」标记 = 平台只给了片段。

    这类条目不算可播：时长对不上、听一半就断，收进曲库只会让人困惑。
    """
    t = str(title or "").lower()
    return "试听" in t or "trial" in t


def pick_match(items, want_id, title, artist):
    """从一批搜索结果里挑出「要解析的那一条」。

    三级判据，从严到宽：
      1. id 完全一致（sidecar 自己给的 id，最可靠）；
      2. 歌名一致且歌手有交集（平台换了 id 时的兜底）；
      3. 歌名一致（歌手字段各源写法差别大，只对歌名）。
    都对不上返回 None —— **宁可解析失败，也不要拿错歌**：下错一首比下不到难查得多。
    """
    if not items:
        return None

    want = str(want_id or "")
    if want:
        for it in items:
            if it.get("id") == want or it.get("platform_id") == want:
                return it

    def norm(s):
        return "".join(str(s or "").lower().split())

    nt, na = norm(title), norm(artist)
    if not nt:
        return None
    same_title = [it for it in items if norm(it.get("title")) == nt]
    if not same_title:
        return None
    if na:
        for it in same_title:
            singer = norm(it.get("artist"))
            if singer and (na in singer or singer in na):
                return it
    return same_title[0]


def playable_items(items):
    """过滤出「能播」的条目：有直链、且不是试听片段。"""
    out = []
    for it in items:
        if not it.get("download_url"):
            continue
        if is_trial(it.get("title", "")):
            continue
        out.append(it)
    return out


def error_kind(exc):
    """把异常归类成调用方能用的判据。

    曲率的 Go 侧要能区分「这个平台当前不可用」（网络/风控/超时 —— 少几个平台而已，
    别整批失败）与「参数错了」（我们自己的 bug，要显式暴露）。混在一起的话，
    一次风控就会让用户看到「搜索失败」而不是「少了一个平台」。
    """
    name = type(exc).__name__.lower()
    text = str(exc).lower()
    for marker in ("timeout", "timedout", "connection", "proxy", "ssl", "network", "dns"):
        if marker in name or marker in text:
            return "unavailable"
    for marker in ("keyerror", "typeerror", "valueerror", "attributeerror", "indexerror"):
        if marker in name:
            return "invalid"
    return "unavailable"
