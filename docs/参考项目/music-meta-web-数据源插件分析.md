# music-meta-web v1.4.8 数据源插件技术分析报告

> 分析对象：`/home/liwenyao/projects/music-v2/.refs/music-meta-web`（tag `v1.4.8`）
> 逐行通读范围：`data-source-plugins/` 下 7 个单文件插件 + `app/server/musicmeta/{ratelimit,fingerprint,cache}.py` + `sources/{base,registry,fingerprint}.py`
> 目的：为在 Go 项目中重新实现同等多源刮削能力提供可直接落地的细节依据。
> 行号均为对应文件的真实行号。

---

## 0. 公共基础设施（先读，后面 7 个源都依赖）

### 0.1 数据模型 `SongMeta`（`app/server/musicmeta/sources/base.py:49-74`）

```python
@dataclass
class SongMeta:
    title: str = ""                                  # 歌曲名
    artist: str = ""                                 # 显示用歌手，多歌手用 " / " 连接
    artists: List[str] = field(default_factory=list)  # 歌手列表
    album: str = ""                                  # 专辑名
    album_artist: str = ""                           # 专辑艺人
    date: str = ""                                   # 发行日期 "YYYY" 或 "YYYY-MM-DD"
    genre: str = ""                                  # 流派
    track: str = ""                                  # 曲目号
    track_total: str = ""                            # 专辑总曲目
    disc: str = ""                                   # 碟片号
    publisher: str = ""                              # 唱片公司
    language: str = ""                               # 语言
    duration: int = 0                                # 时长（秒）
    comment: str = ""
    source: str = ""                                 # 来源名，如 "qqmusic"
    song_id: str = ""                                # 源内歌曲 ID（如 songmid）
    album_id: str = ""                               # 源内专辑 ID（如 albummid）
    confidence: float = 0.0                          # 匹配置信度，<=0 视为不匹配
    extra: dict = field(default_factory=dict)        # 源特有附加信息
```

**关键约定**：封面与歌词**不在 SongMeta 的正式字段里**，而是走 `extra`：
`extra["cover_url"]`（URL 字符串）、`extra["lyrics"]`（LRC 文本）。写盘层（mutagen）统一消费。
Go 侧建议同样用 `map[string]any` 或显式字段 `CoverURL/Lyrics`。

### 0.2 文本归一化 `normalize_text`（`base.py:16-25`）

```python
text = unicodedata.normalize("NFKC", str(text)).strip().lower()
return re.sub(r"[^\w]+", "", text, flags=re.UNICODE)
```

- NFKC：全角→半角（`Ｌｉｖｅ`→`live`），`" 晴天 (Live) "` → `"晴天live"`，`"L.A.Boyz"` → `"laboyz"`。
- 保留 `\w`（字母/数字/下划线/CJK），**去掉所有空白与标点**。
- Go 侧对应：`golang.org/x/text/unicode/norm` 做 NFKC + `unicode.IsLetter/IsDigit/Is(unicode.Pc)` 过滤。
- ⚠️ 注意：NFKC **不做**繁简转换。报告中提到的"繁简/别名归一化"在本项目里**并未实现**，是一个明确的改进空间（见 §9.6）。

### 0.3 轻量打分 `simple_score`（`base.py:28-46`）—— 5 个源的默认打分器

```python
score = 40.0
wt, gt = normalize_text(want_title), normalize_text(got_title)
if gt and wt:
    if gt == wt:                                        score += 30
    elif min(len(gt), len(wt)) >= 4 and (gt in wt or wt in gt): score += 12
wa = normalize_text(want_artist)
ga = "".join(normalize_text(a) for a in (got_artists or []))
if wa and ga:
    if wa == ga or wa in ga or ga in wa:                score += 20
return round(min(score, 100.0), 1)
```

取值域与语义（**重要**，决定了各源实际可用性）：

| 情形 | 分数 |
|---|---|
| 歌名歌手全对 | 40+30+20 = **90** |
| 歌名对、歌手字段为空 | 70 |
| 歌名对、歌手不匹配 | 60 |
| 歌名互相包含（≥4 字）、歌手对 | 82 |
| 歌名互相包含、歌手不匹配 | 52 |
| 歌名完全不对 | 40（**保底分！**） |

⚠️ **设计缺陷**：标题完全不匹配时仍有 40 分保底，且标题不匹配**不扣分**，只少加 30。SPEC.md:44 说"confidence 0~100，/100 与阈值比较"，`base.py:82` 的默认 `min_confidence = 0.0`，即**任何候选都能通过**。只有 qqmusic 自己把阈值提到 `40.0`（`qqmusic.py:167`），而 `simple_score` 的最低分恰好是 40.0 → **`40.0 < 40.0` 为 False，等价于无阈值**。Go 侧必须修正：标题不匹配要扣分（参考 qqmusic 的 `-40`）。

### 0.4 全局共享限速器 `musicmeta/ratelimit.py`（全文 38 行）

```python
_lock = threading.Lock()
_last_request_ts = 0.0

def throttle(min_interval: float = 0.3) -> None:
    global _last_request_ts
    if min_interval is None or min_interval <= 0:
        return
    interval = min_interval * (0.5 + random.random())  # 0.5x ~ 1.5x 抖动
    with _lock:
        wait = _last_request_ts + interval - time.monotonic()
        if wait > 0:
            time.sleep(wait)
        _last_request_ts = time.monotonic()
```

设计要点（**7 个源全部照抄同一模式**，见每个插件里的 `_throttle()`）：

1. **模块级单例**：`_lock` + `_last_request_ts` 是模块全局变量，不是实例属性 → 无论创建多少个 `QQMusicSource` 实例、多少刮削线程，**共用同一节奏**（这是防封的核心）。
2. **抖动乘在 interval 上**（0.5x~1.5x 随机），不是固定 `sleep(0.3)` → 请求间隔不规则，避免固定节奏被风控指纹识别。
3. **在锁内 sleep 并更新时间戳**：串行化，不会并发穿透。
4. `time.monotonic()` 而非 `time.time()`：不受系统时钟调整影响。
5. 每个插件的 `_throttle()` 形如 `_ratelimit.throttle(getattr(self, "min_interval", 0.3))`（如 `netease.py:42-44`）——**实例参数、全局生效**。

Go 侧对应实现：包级 `sync.Mutex` + `lastReq time.Time` + `rand.Float64()*1.0+0.5` 乘子，`time.Sleep` 在锁内。

### 0.5 插件注册机制（`sources/registry.py`）

- `register_source(name, factory)` L48-50：写全局 `_REGISTRY[name] = factory`，工厂接收 kwargs。
- `discover_plugins()` L68-108：扫描 `MMW_PLUGINS_DIR`（环境变量，优先）+ 包内 `plugins/`，用 `importlib.util.spec_from_file_location("musicmeta_source_plugin_<mod>", fpath)` L99-100 **按路径直接加载**，单文件失败只打印日志不影响其它源（L106-107）。
- **本安装包 `_BUILTIN_MODULES = ()` 为空**（L37）：7 个源全部是外部 `.py` 插件，复制到插件目录 + 重启生效。
- `MetaSource.lookup()`（`base.py:92-99`）= `search()` → 取 `max(confidence)` → 若 `< self.min_confidence` 返回 None → `enrich(best)`。

Go 侧对应：`type Source interface { Name() string; Search(title, artist string, limit int) ([]SongMeta, error); Enrich(*SongMeta) error }` + 一个 `map[string]func(...) Source` 注册表；插件化在 Go 里更自然的做法是编译期注册或 `plugin` 包，不建议学 Python 的动态加载。

---

## 1. QQ 音乐（`data-source-plugins/qqmusic.py`，519 行，最完整）

### 1.1 接口清单（`qqmusic.py:50-64`）

| 用途 | URL | 方法 | 关键参数 |
|---|---|---|---|
| 搜索（主） | `https://c.y.qq.com/soso/fcgi-bin/client_search_cp` | GET | 见 1.2 |
| 搜索（备用域） | `https://c6.y.qq.com` / `https://i.y.qq.com` + 同路径 | GET | 同主 |
| 搜索（兜底） | `https://u.y.qq.com/cgi-bin/musicu.fcg` | **POST JSON** | 见 1.3 |
| 专辑详情 | `https://{host}/v8/fcg-bin/fcg_v8_album_info_cp.fcg` | GET | `albummid`,`format=json` |
| 歌词（主） | `https://c.y.qq.com/lyric/fcgi-bin/fcg_query_lyric_new.fcg` | GET | `songmid`,`format=json`,`nobase64=1` |
| 歌词（备） | `https://c.y.qq.com/lyric/fcgi-bin/fcg_query_lyric.fcg` | GET | 同上（JSONP） |
| 封面 | `https://y.gtimg.cn/music/photo_new/T002R{size}x{size}M000{albummid}.jpg` | GET | 纯拼 URL，无接口 |

```python
_UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
       "(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
_REFERER = "https://y.qq.com/"                       # L52，所有请求都带
_SEARCH_HOSTS = ("https://c.y.qq.com", "https://c6.y.qq.com", "https://i.y.qq.com")  # L55
_COVER_URL = ("https://y.gtimg.cn/music/photo_new/T002R{size}x{size}M000{albummid}.jpg")  # L60
```

**Header 策略**：每个请求只带 `User-Agent`（桌面 Chrome）+ `Referer: https://y.qq.com/`（`_http_get_json` L111-112），**没有 cookie、没有 weapi/eapi 加密签名**。这是一个重要的可行性结论：QQ 的 `client_search_cp` 走的是**网页版公开 CGI**，不需要 `qqmusic_key`/`g_tk`/`comm` 签名。

**参数构造（`search()` L295-302）**：

```python
data = _http_get_json(host + _SEARCH_PATH, {
    # 2026-09 起腾讯对匿名搜索收紧：需带完整 web 播放器参数，
    # 否则返回 subcode=-10003（query error）空结果
    "ct": "24", "qqmusic_uin": "0", "format": "json",
    "inCharset": "utf8", "outCharset": "utf-8", "notice": "0",
    "platform": "yqq.json", "needNewCode": "0",
    "p": 1, "n": max(limit, 10), "w": query,
}, timeout=self.timeout, retries=self.retries)
```

- `ct=24` + `qqmusic_uin=0` + `platform=yqq.json` 是**必需**的"伪装 web 播放器"三件套，缺了会被判 query error。
- `n = max(limit, 10)`：请求数不低于 10，给打分留候选空间。
- `w` 是搜索词。

### 1.2 搜索词拼装（`search()` L286-288）

```python
query_title = _clean_query_title(title)                      # L286
query_artist = re.sub(r"[-_]+", " ", (artist or "").strip()).strip()  # L287
query = query_title if not query_artist else f"{query_title} {query_artist}"  # L288
```

`_clean_query_title`（L150-158）做两级清洗：

```python
t = _PAREN_RE.sub("", title or "").strip()   # _PAREN_RE = r"[（(].*?[）)]"  L82
t = _SUFFIX_RE.sub("", t).strip()            # 去 "歌名 - 铃声/伴奏版"
return t or (title or "").strip()
```

例：`'蓝莲花(Live)' → '蓝莲花'`、`'江南(DJ白鹤版)' → '江南'`。
**歌手里的 `-` / `_` 换成空格**：`"刘欢-Sarah Brightman"` → `"刘欢 Sarah Brightman"`、`"周传雄_吴迪"` → `"周传雄 吴迪"`。这是针对"下载站文件名风格"的实用技巧。

⚠️ 注意 L327：打分时传入的 `want_title` 是**清洗后的** `query_title` 而不是原始 `title`：

```python
meta = self._parse_item(item, query_title, artist)   # L327
```

否则文件名里的 `(Live)` 会让已找到的录音室版被判"标题失配 -40"。**打分基准词与搜索词必须一致**——这是一个容易踩的坑。

### 1.3 多域名轮换 + 兜底（`search()` L290-319）

```python
for host in _SEARCH_HOSTS:                       # L292
    self._throttle()
    try:
        data = _http_get_json(host + _SEARCH_PATH, {...})
        if data and data.get("code") == 0:
            break                                # L304 成功
        last_err = QQMusicError(f"code={data.get('code')} subcode={...}")  # L306
        time.sleep(1.5 * (0.5 + random.random()))  # L308 换域前休息 0.75~2.25s
    except QQMusicError as exc:
        last_err = exc
        time.sleep(1.5 * (0.5 + random.random()))  # L311
if data is None or data.get("code") != 0:
    try:
        data = self._search_musicu(query)         # L315 兜底
    except Exception:
        return []                                 # L319 全部失败 → 空列表（降级）
```

三个机制叠加：**域名轮换** → **code!=0 视为风控并退避** → **换协议（GET CGI → POST musicu）兜底**。

**`_search_musicu`（L491-515）**：POST 到 `u.y.qq.com/cgi-bin/musicu.fcg`，payload 结构：

```python
payload = {
    "comm": {"ct": 24, "cv": 0},
    "req_1": {
        "module": "music.search.SearchCgiService",
        "method": "DoSearchForQQMusicDesktop",
        "param": {"grp": 1, "num_per_page": 20, "page_num": 1,
                  "query": query, "search_type": 0},
    },
}
```

响应路径 `data["req_1"]["data"]["body"]["song"]["list"]`，最后**归一化成与主接口同构**的 `{"code": 0, "data": {"song": {"list": [...]}}}`（L515）→ 调用方无需分支处理。这是很好的**适配器模式**：兜底接口负责把自己伪装成主接口形状。
⚠️ 代码注释（L492）自述"实测当前网络返回 0 命中，仅作兜底"——即 **musicu 这条链路实际不可用**，Go 侧可后置。

### 1.4 结果解析 → SongMeta（`_parse_item` L185-221）

```python
if item.get("type") not in (0, None):    # L187 只取普通歌曲，跳过 MV/视频
    return None
raw_name = item.get("songname") or item.get("name") or ""   # L189
artists: List[str] = []
for singer in item.get("singer") or []:
    name = (singer.get("name") or "").strip()
    for part in _ARTIST_SEP_RE.split(name):      # L196  r"[/;、，,&_]+"
        part = part.strip()
        if part and part not in artists:         # L198 去重
            artists.append(part)
if not artists:
    return None
meta = SongMeta(
    title=clean_songname(raw_name),                       # L204 去 " - 铃声" 后缀
    artist=" / ".join(artists),
    artists=artists,
    album=item.get("albumname") or "",                    # L207
    date=_pubtime_to_date(item.get("pubtime")),           # L208 Unix 秒 → YYYY-MM-DD
    duration=int(item.get("interval") or 0),              # L209 已是秒
    source=self.name,
    song_id=str(item.get("songmid") or item.get("songid") or ""),   # L211
    album_id=str(item.get("albummid") or ""),             # L212
    extra={"songname_raw": raw_name,                      # L214 保留原始名供打分
           "size_flac": int(item.get("sizeflac") or 0),   # L215 无损体积（可用于判断音质）
           "size_320": int(item.get("size320") or 0),
           "pubtime": item.get("pubtime")},
)
meta.confidence = self._score(meta, want_title, want_artist, item)   # L220
```

**字段来源映射表**（Go 重写时照抄）：

| SongMeta | QQ JSON 字段 | 备注 |
|---|---|---|
| title | `songname`（fallback `name`） | 需 `clean_songname()` |
| artist / artists | `singer[].name` | 再按 `[/;、，,&_]+` 二次切分 |
| album | `albumname` | |
| date | `pubtime`（Unix 秒） | `_pubtime_to_date` L129-140，`<=0` 返回 `""` |
| duration | `interval` | 已是秒 |
| song_id | `songmid`（fallback `songid`） | songmid 是字符串，用于歌词/专辑比对 |
| album_id | `albummid` | 用于拼封面 URL 和专辑详情 |
| extra.size_flac / size_320 | `sizeflac` / `size320` | 可判断是否有无损 |
| extra.songname_raw | 原始 songname | 打分时用（避免清洗后丢失版本信息） |

`clean_songname`（L143-147）+ `_SUFFIX_RE`（L76-80）：

```python
_SUFFIX_RE = re.compile(
    r"[-—–]\s*(?:铃声|伴奏|remix|mix|live|现场|演唱会|纯音乐|翻唱|cover|"
    r"instrumental|acoustic|dj|慢摇|加快|变调|串烧|伴奏版|纯音乐版)[版曲]?$",
    re.IGNORECASE)
def clean_songname(songname: str) -> str:
    s = (songname or "").strip()
    cleaned = _SUFFIX_RE.sub("", s).strip()
    return cleaned or s     # L147 清空则回退原名，避免把整名吃光
```

### 1.5 打分逻辑 `_score`（L223-276）—— 全项目最完整的打分器

```python
score = 0.0
want_t = normalize_text(want_title)
base = _PAREN_RE.sub("", meta.extra.get("songname_raw", meta.title)).strip()  # L228 去括号再比
got_t = normalize_text(base)

# ---- 标题 ----
if want_t:
    if got_t == want_t:                                  score += 60   # L234
    elif (len(got_t) >= 4 and len(want_t) >= 4
          and (got_t in want_t or want_t in got_t)):     score += 30   # L237
    else:                                                score -= 40   # L239 基本淘汰
else:
    score += 30

# ---- 歌手（三种比较） ----
want_names = {normalize_text(x) for x in _ARTIST_SEP_RE.split(want_artist or "") if x.strip()}
got_names  = {normalize_text(a) for a in meta.artists}
want_joined = normalize_text(want_artist)
got_joined  = "".join(normalize_text(a) for a in meta.artists)
if want_names:
    if (want_names == got_names
            or (want_joined and want_joined == got_joined)):  score += 30   # L255
    elif want_names <= got_names or got_names <= want_names:  score += 18   # L256 集合包含
    elif (min(len(want_joined), len(got_joined)) >= 2
          and (want_joined in got_joined or got_joined in want_joined)):
                                                              score += 18   # L260 子串
    else:                                                     score -= 25   # L262
else:
    score += 10

# ---- 版本标记扣分 ----
low = meta.extra.get("songname_raw", meta.title).lower()
flags = 0
if _SUFFIX_RE.search(meta.extra.get("songname_raw", meta.title)):  flags += 1   # L269
flags += sum(1 for m in _VERSION_MARKERS if m in low)                            # L271
score -= 15 * min(flags, 3)          # L272 上限 3 个 flag = -45
if not item.get("pubtime"):          # L274 无发行时间的很可能是翻录/变体
    score -= 10
return round(score, 2)
```

**打分矩阵**（满分基准 90 = 60+30）：

| 标题 | 歌手 | 版本标记 | 无 pubtime | 总分 |
|---|---|---|---|---|
| 完全一致 | 完全一致 | 无 | 有 pubtime | **90** |
| 完全一致 | 完全一致 | 1 个 | 有 | 75 |
| 完全一致 | 集合包含 | 无 | 有 | 78 |
| 完全一致 | 不匹配 | 无 | 有 | 35（<40 阈值 → 淘汰）|
| 互相包含 | 完全一致 | 无 | 有 | 60 |
| 不匹配 | 任意 | — | — | ≤ -40 → 淘汰 |

**二次校验**：QQ 源本身**没有时长比对**，但有三个等效的二次校验：
1. 标题**去括号后**再比（`_PAREN_RE.sub` L228）—— 解决 `"晴天"` vs `"晴天 (Live)"`；
2. 歌手**三路比较**（集合相等/集合包含/拼接子串）—— 覆盖 `"刘欢-Sarah Brightman"`、`"周传雄_吴迪"`、`"CoCo李玟"` vs `"李玟"`；
3. **版本标记扣分**（`_VERSION_MARKERS` L67-74，22 个词：live/现场/演唱会/remix/伴奏/纯音乐/instrumental/acoustic/铃声/karaoke/翻唱/cover/dj/慢摇/加快/变调/串烧/黑胶/重制/remaster/复刻/珍藏/限量/radio edit/drumless/周年/纪念）。

**阈值**：`__init__(min_confidence: float = 40.0)`（L167），`lookup()` 里 `best.confidence < 40.0` 即返回 None。结合上表：歌手不匹配（35）会被淘汰，这是**唯一实际生效的过滤**。

⚠️ **缺失项**：`item["interval"]`（时长）虽然被解析进 `meta.duration`，但**打分时从未参与比对**。对刮削场景来说，时长比对（±2~3s）是最强的二次校验信号，QQ 源没用上。Go 侧应补上。

### 1.6 enrich：专辑详情反查曲目号（L334-380）

```python
if not meta.album_id:                       # L340 无 albummid 直接返回
    return meta
self._throttle()
data = None
for host in _SEARCH_HOSTS:                  # L344 同样多域轮换
    try:
        data = _http_get_json(host + _ALBUM_PATH,
                              {"albummid": meta.album_id, "format": "json"}, ...)
        if data and data.get("code") == 0: break
        data = None
        time.sleep(1.5 * (0.5 + random.random()))
    except QQMusicError:
        time.sleep(1.5 * (0.5 + random.random()))
if not data or data.get("code") != 0:
    return meta                             # L356 失败静默降级，保留 search 阶段的字段
info = data.get("data") or {}
if info.get("aDate"):        meta.date = str(info["aDate"])              # L358-359
if info.get("genre"):        meta.genre = str(info["genre"])             # L360-361
if info.get("singername") and not meta.album_artist:
                             meta.album_artist = str(info["singername"]) # L362-363
if info.get("company"):      meta.publisher = str(info["company"])       # L364-365
                             meta.extra["company"] = str(info["company"])
if info.get("lan"):          meta.language = str(info["lan"])            # L367-369
                             meta.extra["language"] = str(info["lan"])
songlist = info.get("list") or []                                        # L370
if songlist:
    meta.track_total = str(len(songlist))                                # L372
    for i, item in enumerate(songlist, 1):                               # L373 1-based
        if str(item.get("songmid") or "") == meta.song_id:               # L374 用 songmid 反查
            meta.track = str(i)                                          # L375
            cd = item.get("cdIdx")
            if isinstance(cd, int) and cd >= 0:
                meta.disc = str(cd + 1)                                  # L378 cdIdx 0-based → disc 1-based
            break
```

**这是全项目最值得借鉴的技巧**：**用专辑 ID 拉整张专辑曲目表，再用 songmid 反查曲目号/碟号/总曲目**。
- 曲目号 `track` 无法从搜索接口得到，必须靠这个反查；
- `track_total = len(list)` 顺手就有；
- `disc` 来自每条的 `cdIdx`（0-based，需 +1）；
- 一次请求同时补齐 date/genre/publisher/language/track/track_total/disc/album_artist **8 个字段**——**性价比最高的一次请求**。

### 1.7 封面：纯 URL 拼接 + 多档分辨率（L384-417）

```python
@staticmethod
def cover_url(albummid: str, size: int = 300) -> str:
    """按 albummid 拼 QQ 音乐封面图 URL（size: 300/500/800）。"""
    return _COVER_URL.format(size=size, albummid=albummid)     # L387

def fetch_cover(self, albummid, size=300) -> Optional[bytes]:   # L389
    url = self.cover_url(albummid, size)
    self._throttle()
    req = urllib.request.Request(url, headers={"User-Agent": _UA, "Referer": _REFERER})
    with urllib.request.urlopen(req, timeout=self.timeout) as resp:
        data = resp.read()
    if data[:3] == b"\xff\xd8\xff" or data[:4] == b"\x89PNG":   # L401 魔数校验
        return data
    return None

def fetch_cover_best(self, albummid, sizes=(800, 500, 300)):     # L407
    for size in sizes:
        data = self.fetch_cover(albummid, size)
        if data:
            return data
    return None
```

- **零额外请求**拿到封面 URL：`T002R{size}x{size}M000{albummid}.jpg`，`albummid` 来自搜索结果。
- **多档降级**：先试 800，失败退 500，再退 300（L407-417），注释（L410-411）说明产品约定"图多大都可以用，但大的优先级高"。
- **魔数校验**（L401）：QQ 对不存在的 albummid 会返回一个 HTML/错误页而非 404，用 `\xff\xd8\xff`（JPEG）/`\x89PNG`（PNG）判断真实图片。**Go 侧务必照做**，否则会把 HTML 写进封面文件。

### 1.8 歌词：双接口 + JSONP + HTML 实体（L419-464）

```python
def fetch_lyrics(self, songmid: str) -> str:      # L419
    if not songmid: return ""
    self._throttle()
    # 主接口：fcg_query_lyric_new（纯 JSON）
    try:
        data = _http_get_json(_LYRIC_NEW_URL, {
            "songmid": songmid, "format": "json", "nobase64": 1,   # L427 nobase64=1 → 直接给明文 LRC
        }, timeout=self.timeout, retries=1)
        lyric = (data or {}).get("lyric")
        if lyric: return str(lyric)                                # L431
    except QQMusicError:
        pass
    # 备用：fcg_query_lyric（JSONP + HTML 实体）
    try:
        raw = self._http_get_raw(_LYRIC_OLD_URL, {"songmid": songmid, "format": "json", "nobase64": 1})
        start, end = raw.find("("), raw.rfind(")")                 # L439 剥 JSONP
        if start >= 0 and end > start:
            data = json.loads(raw[start + 1:end])
            lyric = data.get("lyric")
            if lyric: return html.unescape(str(lyric))             # L444 HTML 实体解码
    except Exception:
        pass
    return ""                                                       # L447 无歌词/失败 → 空串
```

- `nobase64=1` 是关键：不传会返回 base64，传了直接给明文 LRC（含 `[00:00.00]` 时间轴）。
- 备用接口的歌词含 `&apos;` / `&amp;` 等 HTML 实体，必须 `html.unescape`（Go：`html.UnescapeString`）。
- **只取 `lyric`，不取 `trans`（翻译）** —— 备用接口的 JSON 里其实有 `trans` 字段，插件没用。
- 返回空串而非抛异常：**歌词失败不影响主流程**。

### 1.9 重试/超时/降级汇总（QQ 源）

| 环节 | 超时 | 重试 | 退避 | 降级 |
|---|---|---|---|---|
| `_http_get_json` L104-126 | 15s | 默认 2 次 | `0.5 * 2^attempt * (0.5+rand)` = 0.25~0.75 / 0.5~1.5 / 1~3 s | 抛 `QQMusicError` |
| 搜索域轮换 L292-311 | 15s | 3 个域各 1 次 | 换域前 `1.5*(0.5+rand)` = 0.75~2.25s | 全部失败 → `_search_musicu` → 返回 `[]` |
| 专辑 enrich L344-354 | 15s | 3 域 | 同上 | 返回未补全的 meta（**静默**） |
| 封面 L389-405 | 15s | 0 | — | 返回 None；`fetch_cover_best` 逐档降级 |
| 歌词 L419-447 | 15s | 主接口 1 次 | 0.5~1.5 / 1~3 s | 备用接口 → 空串 |
| 指纹回查 L466-487 | — | — | — | 识别结果前 3 个各搜一次 |

### 1.10 指纹识别回查（L466-487）

```python
def recognize_by_fingerprint(self, path: str, acoustid_key: str, limit: int = 10) -> List[SongMeta]:
    from musicmeta.sources.fingerprint import recognize
    recs = recognize(path, acoustid_key)
    if not recs: return []
    results, seen = [], set()
    for rec in recs[:3]:                                   # L479 只试前 3 个识别结果
        for meta in self.search(rec["title"], rec["artist"], limit=limit):
            if meta.song_id and meta.song_id not in seen:
                seen.add(meta.song_id)
                meta.extra["fingerprint_title"]  = rec["title"]     # L483 留痕
                meta.extra["fingerprint_artist"] = rec["artist"]
                results.append(meta)
    results.sort(key=lambda m: m.confidence, reverse=True)
    return results
```

**两级漏斗**：AcoustID 给出"内容级"候选（可信度高）→ 用它去 QQ 搜"元数据级"候选 → 取 top3 识别结果合并去重。Go 侧同样可复用。

### 1.11 最值得借鉴的技巧（QQ 源）

1. **专辑 ID 反查曲目号/碟号/总曲目**（L370-379）—— 唯一能拿到 track/disc 的手段，一次请求补 8 个字段。
2. **多域名轮换 + code!=0 视为风控 + 退避**（L292-311）—— 单域被封不会全盘失败。
3. **兜底接口归一化成主接口形状**（L515）—— 调用方零分支。
4. **搜索词清洗与打分基准词分离**（`_clean_query_title` vs `_PAREN_RE.sub`）—— 搜索要"干净"，打分要"原始"。
5. **封面纯 URL 拼接 + 800/500/300 多档降级 + 魔数校验**（L384-417）。
6. **JSONP 自动剥离**（L115-119）写进了通用 `_http_get_json`，所有接口共用。
7. **版本标记（22 词）扣分 + 无 pubtime 扣分**（L266-275）—— 抑制"翻唱/伴奏/铃声"抢走原唱。
8. **全局共享限速器 + 抖动**（`ratelimit.py`）—— 多线程共用节奏，插件本身无状态。
9. `extra["songname_raw"]` 保留原始名（L214）—— 打分与展示解耦。

---

## 2. 网易云音乐（`netease.py`，107 行）

### 2.1 接口（`netease.py:35-37`）

```python
SEARCH_URL = "https://music.163.com/api/search/get/web"    # POST form
LYRIC_URL  = "https://music.163.com/api/song/lyric"        # GET
DETAIL_URL = "https://music.163.com/api/song/detail"       # GET
```

| 用途 | 方法 | 参数 | Header |
|---|---|---|---|
| 搜索 | **POST** | form body：`s=<title artist>`、`type=1`、`limit=max(limit,5)`、`offset=0` | UA + `Referer: https://music.163.com/` + `Content-Type: application/x-www-form-urlencoded` |
| 详情（封面） | GET | `?ids=[<song_id>]` | UA + Referer |
| 歌词 | GET | `?id=<song_id>&lv=-1&kv=-1&tv=-1` | UA + Referer |

```python
def _request(self, url: str, data: bytes = None) -> dict:      # L45
    self._throttle()
    headers = {"User-Agent": _UA, "Referer": "https://music.163.com/"}
    if data is not None:
        headers["Content-Type"] = "application/x-www-form-urlencoded"   # L49
    req = urllib.request.Request(url, data=data, headers=headers)
    with urllib.request.urlopen(req, timeout=15) as resp:
        return json.loads(resp.read().decode("utf-8", "replace"))
```

**没有 cookie、没有 weapi/eapi 加密**。走的是老的 `/api/` 而非 `/weapi/`（`/weapi/` 才需要 AES+RSA 双加密），`/api/` 是"公开网页接口"。
⚠️ **风险**：网易近年对 `/api/search/get/web` 有收紧，部分 IP/时段需要 `NMTID` cookie 或返回 460。插件里**没有任何 cookie 逻辑**，Go 侧应预留 cookie 注入位。

### 2.2 搜索与解析（`search()` L54-82）

```python
query = f"{title} {artist}".strip()                            # L55 无条件拼接，不区分 artist 空
data = urllib.parse.urlencode(
    {"s": query, "type": 1, "limit": max(limit, 5), "offset": 0}).encode()   # L56-57
d = self._request(self.SEARCH_URL, data)
for s in (d.get("result") or {}).get("songs", []) or []:       # L60 路径：result.songs
    name = (s.get("name") or "").replace("\ufeff", "").strip()  # L61 去 BOM！
    artists = [a.get("name", "").strip()
               for a in (s.get("artists") or []) if a.get("name")]     # L62-63
    album = s.get("album") or {}
    date_ms = album.get("publishTime") or 0                     # L67 毫秒时间戳
    meta = SongMeta(
        title=name,
        artist=" / ".join(artists),
        artists=artists,
        album=(album.get("name") or "").replace("\ufeff", "").strip(),
        date=time.strftime("%Y-%m-%d", time.localtime(date_ms / 1000)) if date_ms else "",  # L73-74
        duration=int(s.get("duration") or 0) // 1000,           # L75 毫秒 → 秒
        source=self.name,
        song_id=str(s.get("id") or ""),
        album_id=str(album.get("id") or ""),
        confidence=simple_score(title, artist, name, artists),   # L79
    )
```

**字段映射**：`name`→title、`artists[].name`→artists、`album.name`→album、`album.publishTime`（ms）→date、`duration`（ms）→duration、`id`→song_id、`album.id`→album_id。

**两个细节**：
- `.replace("\ufeff", "")`（L61、L72）：网易的 name/album 字段常带 **BOM 字符 `\ufeff`**，不清洗会污染标签。Go：`strings.TrimPrefix(s, "\uFEFF")`。
- `time.localtime`（L73）→ **依赖本地时区**，跨时区结果会差一天。Go 侧应用 `time.Unix(ms/1000, 0).UTC()` 或明确指定时区。

**打分**：直接用 `simple_score`（L79），**无版本标记扣分、无时长比对**。阈值 `min_confidence` 未在 `__init__` 设置（L39-40 只收 `min_interval`）→ 继承基类 `0.0` → **无阈值**。

### 2.3 enrich：封面 + 歌词（L84-104）

```python
def enrich(self, meta: SongMeta) -> SongMeta:
    if meta.song_id:
        try:
            d = self._request(self.DETAIL_URL + "?ids=[" + meta.song_id + "]")   # L88 注意 ids=[id] 字面量
            songs = d.get("songs") or []
            if songs:
                al = songs[0].get("album") or {}
                if al.get("picUrl"):
                    meta.extra["cover_url"] = al["picUrl"]                        # L93
        except Exception:
            pass                                                                  # L95 静默
        try:
            d = self._request(self.LYRIC_URL + "?id=" + meta.song_id +
                              "&lv=-1&kv=-1&tv=-1")                               # L97-98
            lrc = (d.get("lrc") or {}).get("lyric") or ""                          # L99
            if lrc:
                meta.extra["lyrics"] = lrc                                        # L101
        except Exception:
            pass
    return meta
```

**`ids=[<id>]` 的方括号是字面量**（URL 里未编码），网易接口接受这种写法——**批量接口**：`ids=[1,2,3]` 可一次拿多首（插件只用单首）。
**歌词三参数**：
- `lv=-1` → 原文歌词（`lrc.lyric`）
- `kv=-1` → 卡拉OK 逐字歌词（`klyric`）
- `tv=-1` → **翻译歌词**（`tlyric`）
插件只取了 `lrc`，**没取翻译**——这是浪费。Go 侧应同时取 `tlyric.lyric` 并与原文按时间轴合并（这是国内源里最好的"歌词+翻译"来源）。

封面 `picUrl` 支持 `?param=300y300` 查询串动态改尺寸（插件未用）。

**额外请求次数**：enrich = 2 次（detail + lyric），比 QQ 的 1 次专辑详情 + 1 次歌词少一次（QQ 封面免费）。

### 2.4 限速/重试/降级

- `_request` L46 有 `self._throttle()`；**无重试、无多域名、无 JSONP 兼容**（`json.loads` 直解）。
- 两处 enrich 都是 `try/except Exception: pass`（L94-95、L102-103）——**完全静默降级**。
- 超时硬编码 15s（L51）。

### 2.5 最值得借鉴的技巧（网易）

1. **`ids=[...]` 批量查询**（L88）—— 一次请求拿多首详情，适合批量补封面。
2. **`lv/kv/tv=-1` 三档歌词参数**（L98）—— 原文 + 逐字 + 翻译，可组合出带翻译的 LRC。
3. **BOM 清洗**（L61、L72）—— 中文源必备。
4. **`album.publishTime` 直接给出毫秒级发行时间**（L67）—— 比 iTunes 的字符串可靠。
5. 搜索用 POST form（`s=`）而非 GET query，**规避 URL 编码差异导致的命中率波动**。

---

## 3. 酷狗（`kugou.py`，130 行）

### 3.1 接口（`kugou.py:36-38`）

```python
SEARCH_URL       = "https://songsearch.kugou.com/song_search_v2"   # GET
LYRIC_SEARCH_URL = "http://lyrics.kugou.com/search"                # GET（注意是 HTTP）
LYRIC_DOWN_URL   = "http://lyrics.kugou.com/download"              # GET（HTTP）
```

搜索参数（L54-55）：`keyword=<title artist>`、`page=1`、`pagesize=max(limit,5)`。**Header 只带 UA**（L48），无 Referer、无 cookie、无签名。

### 3.2 搜索解析 + 双层惩罚（`search()` L52-104）

```python
query = f"{title} {artist}".strip()                                     # L53
url = self.SEARCH_URL + "?" + urllib.parse.urlencode(
    {"keyword": query, "page": 1, "pagesize": max(limit, 5)})           # L54-55
d = self._get_json(url)
_FAKE_SINGERS = ("歌单", "热门", "DJ", "网友", "翻唱", "伴奏", "抖音", "快手")   # L59
def _is_fake_singer(sg):                                                # L60-62
    low = sg.lower()
    return any(x.lower() in low for x in _FAKE_SINGERS) or sg == "群星"
```

**技巧 A：伪歌手黑名单**（L59-62）。酷狗搜索结果里混入大量"歌单/热门/DJ/网友/抖音"这种非真实歌手条目，直接**丢弃**（L67 `continue`）而不是扣分。

**技巧 B：歌名包裹格式提取**（L70，⚠️ 死代码）：

```python
m2 = _re.search(r"[《《（(]([^》》）)]+)[》》）)]", name)   # L70 提取到 m2 后从未使用
low_name = name.lower()                                  # L71
```

原意是处理"歌手《歌名》"这种歌单条目，但 `m2` 结果**未被使用**（只保留了 `low_name`）——是残留死代码。Go 重写时若遇到此类条目，应真正用 `m2.group(1)` 替换 name。

**技巧 C：两层惩罚叠加**（L73-84）：

```python
version_penalty = 0
if any(v in low_name for v in ("dj", "live", "现场", "remix", "伴奏",
                               "改编", "mv", "mv版", "慢摇", "串烧",
                               "加快", "变调")):            # L74-76
    version_penalty = 15                                    # L77
fhash = s.get("FileHash") or ""
want_artists = {x.strip().lower() for x in _re.split(r"[/;、，,&_]", artist or "") if x.strip()}
got_artist_l = singer.lower()
artist_penalty = 0
if want_artists and not any(w in got_artist_l or got_artist_l in w for w in want_artists):
    artist_penalty = 25   # L84 歌手完全对不上，基本淘汰
```

- 版本标记：**固定 -15**（QQ 是 -15×min(flags,3)）；
- 歌手不匹配：**-25**（双向子串匹配，容忍"群星"式差异）；
- `confidence = max(1.0, simple_score(...) - version_penalty - artist_penalty)`（L94-95）——`max(1.0, ...)` 保证分数为正，避免被 `<=0 视为不匹配` 的约定误杀。

**字段映射**（L85-96）：`SongName`→title、`SingerName`→artist/artists、`AlbumName`→album、`Duration`→duration（**已是秒**）、`FileHash`→song_id、`AlbumID`→album_id。

⚠️ **`FileHash` 被当作 `song_id`** —— 这是"文件 hash"不是"歌曲 ID"，跨码率/跨来源同一首歌 hash 不同，**无法用于去重**。Go 侧应优先用 `EMixSongID`/`Audioid` 类字段（插件未取）。

**技巧 D：封面 URL 由 hash 直接推导，零额外请求**（L97-100）：

```python
if fhash:
    meta.extra["cover_url"] = f"https://imgessl.kugou.com/stdmusic/400/{fhash}.jpg"   # L99-100
```

`stdmusic/400/` 中的 `400` 是尺寸档位（可换 240/480 等）。**比 QQ 更省**：QQ 需要 `albummid`（搜索结果里就有），酷狗需要 `FileHash`（也在搜索结果里）——**两者都是"搜索结果自带 ID → 拼 URL"**，这是中文源封面获取的通用最优解。

最后 `metas.sort(key=lambda m: m.confidence, reverse=True)`（L103）——**原唱优先排序**。

### 3.3 enrich：两步式歌词（L106-127）

```python
def enrich(self, meta: SongMeta) -> SongMeta:
    if not meta.song_id: return meta
    try:
        d = self._get_json(self.LYRIC_SEARCH_URL + "?" + urllib.parse.urlencode(
            {"ver": 1, "man": "yes", "client": "pc",
             "keyword": f"{meta.title} {meta.artist}"}))                  # L111-113
        cand = ((d.get("candidates") or [{}])[0])                          # L114 只取第一个候选
        cid, cak = cand.get("id"), cand.get("accesskey")                   # L115
        if cid and cak:
            d2 = self._get_json(self.LYRIC_DOWN_URL + "?" + urllib.parse.urlencode(
                {"ver": 1, "client": "pc", "id": cid,
                 "accesskey": cak, "fmt": "lrc"}))                         # L117-119
            content = d2.get("content") or ""
            if content:
                lrc = base64.b64decode(content).decode("utf-8", "replace")  # L122 base64！
                if lrc.strip():
                    meta.extra["lyrics"] = lrc
    except Exception:
        pass
    return meta
```

**两步式歌词链路**（重要，Go 侧要照搬）：
1. `lyrics.kugou.com/search?ver=1&man=yes&client=pc&keyword=<歌名 歌手>` → `candidates[]`，每个有 `id` + `accesskey`；
2. `lyrics.kugou.com/download?ver=1&client=pc&id=<id>&accesskey=<ak>&fmt=lrc` → `content` 是 **base64 编码的 LRC** → `base64.b64decode` → utf-8。
- `man=yes` 请求人工校对歌词；`fmt=lrc` 指定 LRC 格式（也可 `fmt=krc` 拿逐字）。
- ⚠️ 只取 `candidates[0]`，**没有按时长/歌手做候选筛选** —— 酷狗的歌词候选常有多条（不同版本），直接用第 0 条有风险。Go 侧应按 `duration` 字段（candidates 里有）与 `meta.duration` 比对选最优。
- ⚠️ **keyword 用的是 `meta.title` + `meta.artist`**（L113），而 `meta.title` 已经过 search 阶段清洗 → 可能与真实歌名有偏差。
- ⚠️ 歌词接口是 **HTTP**（L37-38），不是 HTTPS。明文传输，且未来可能被强制跳转。

### 3.4 限速/重试/降级

- `_get_json` L47 有 throttle；**无重试、无退避**（`urlopen` 直接抛）。
- 超时硬编码 15s（L49）。
- enrich 整体 `try/except Exception: pass`（L125-126）。
- 搜索无 try/except → **网络异常会向上抛**（与其他源不一致，Go 侧统一成返回 error）。

### 3.5 最值得借鉴的技巧（酷狗）

1. **伪歌手黑名单直接丢弃**（L59-62）—— 比扣分更干脆，中文源普遍需要。
2. **封面 URL 由搜索结果自带的 hash 直接拼**（L99-100）—— 零请求。
3. **两步式歌词（search 拿 accesskey → download 拿 base64 LRC）**（L111-122）—— 这是酷狗歌词的唯一正路。
4. **`max(1.0, score - penalties)`**（L94）—— 防止分数 ≤0 被"视为不匹配"的约定误杀，配合排序实现"原唱优先但保留后备"。
5. 版本惩罚 + 歌手惩罚**分离计算**，便于各自调参。

---

## 4. 酷我（`kuwo.py`，92 行）

### 4.1 接口（`kuwo.py:36`）

```python
SEARCH_URL = "http://search.kuwo.cn/r.s"     # 注意：HTTP，且路径是 .s
```

请求参数（L54-57）：

```python
url = self.SEARCH_URL + "?" + urllib.parse.urlencode({
    "client": "kt", "all": query, "pn": 0, "rn": max(limit, 5), "uid": 0,
    "ver": "kwplayer_ar_9.2.2.1", "vipver": 1, "show_copyright_off": 1,
    "new_format": 1, "ft": "music", "encoding": "utf8", "rformat": "json"})
```

**参数构造技巧**：这一串参数是在**伪装酷我 Android 客户端**（`client=kt`、`ver=kwplayer_ar_9.2.2.1` 是客户端版本号、`vipver=1`、`show_copyright_off=1` 忽略版权下架、`new_format=1` 新格式、`ft=music` 过滤音乐）。Header 只带 UA（L48）。

⚠️ **`rformat=json` 参数名有误导性**：请求它返回 JSON，实际返回的是 **JS 字面量**（键无引号或单引号、含 `\u` 转义），不是严格 JSON。

### 4.2 解析：`ast.literal_eval` 兜底（`search()` L58-62）—— 本项目独有的技巧

```python
raw = self._get_text(url)
start, end = raw.find("{"), raw.rfind("}")        # L59 掐头去尾取最外层大括号
if start < 0 or end <= start:
    return []
d = ast.literal_eval(raw[start:end + 1])          # L62 用 Python 字面量求值器解析
```

- **`raw.find("{")` / `raw.rfind("}")`**：酷我返回内容前后可能包着 JS 变量赋值（`var data={...};`）或 BOM，用"第一个 `{` 到最后一个 `}`"截取。
- **`ast.literal_eval`** 而非 `json.loads`：能解析 Python/JS 字面量（单引号字符串、`True/False/None`、末尾逗号等）。
  **Go 侧对应**：Go 没有等价物，需 (a) 写一个宽松解析器，(b) 或把响应规范化（单引号→双引号、去尾逗号）后喂给 `encoding/json`，(c) 或用 `github.com/robertkrimen/otto` 跑 JS 求值。**这是本报告里 Go 移植成本最高的一处**。
- ⚠️ **安全提示**：`ast.literal_eval` 相对 `eval` 安全（不执行函数调用），但对不可信输入仍是攻击面。Go 侧应坚持纯解析而非求值。

**字段映射**（L65-86）：响应体 `d["abslist"]`（列表，`[:max(limit,5)]` 截断）：

| SongMeta | 酷我 JSON 字段 | 备注 |
|---|---|---|
| title | `SONGNAME` | 再经 `_SUFFIX_RE` 去 `-《...》` 副标题 |
| artist / artists | `ARTIST` | **单值**，多歌手可能挤在一个字符串里，未切分 |
| album | `ALBUM` | |
| duration | `DURATION` | 已是秒 |
| song_id | `MUSICRID` | 形如 `MUSIC_228908` |
| album_id | `ALBUMID` | |
| extra.cover_url | `web_albumpic_short` | 拼 `https://img2.kuwo.cn/star/albumcover/{pic}` |

**技巧：副标题后缀清洗**（L38、L67）：

```python
_SUFFIX_RE = re.compile(r"[-—–]\s*《.*?》.*$")     # L38  "歌名 -《网络电影插曲》"
name = self._SUFFIX_RE.sub("", raw_name).strip() or raw_name   # L67
```

酷我歌名常带 `-《xxx》` 形式的影视副标题，正则一次性剥掉。

**技巧：封面同样是"返回字段拼 URL"**（L82-85）：

```python
pic_short = s.get("web_albumpic_short") or ""
if pic_short:
    meta.extra["cover_url"] = "https://img2.kuwo.cn/star/albumcover/" + pic_short
```

`web_albumpic_short` 已经是**相对路径片段**（含 `/` 与尺寸目录），直接前缀拼接即可。

**打分**：`simple_score(title, artist, name, [singer])`（L80），无惩罚、无阈值（`min_confidence` 未设置）。

### 4.3 歌词：**不做**（L89 注释）

```python
# 歌词：酷我歌词接口已限制，交由应用回查 QQ 源补全（见 scheduler._write_fields）
```

- **酷我官方歌词接口已失效**（模块 docstring L13 说明返回"音乐查询失败"）。
- 应用层的降级策略：**kuwo 只提供 title/artist/album/cover，歌词由应用自动回查 QQ 插件补全**（SPEC.md:53 的兜底约定）。
- 因此 KuwoSource **没有 `enrich` 方法**（继承基类 `base.py:88-90` 的 no-op）。

### 4.4 限速/重试/降级

- `_get_text` L47 有 throttle；无重试、无退避。
- 超时硬编码 15s（L49）。
- 解析失败返回 `[]`（L60-61）—— 静默降级。

### 4.5 最值得借鉴的技巧（酷我）

1. **宽松解析策略：`find("{")`/`rfind("}")` + `literal_eval`**（L59-62）—— 应对非标准 JSON 的通用套路。
2. **客户端伪装参数组**（`client=kt` + `ver=kwplayer_ar_9.2.2.1` + `new_format=1`）（L55-57）—— 老接口靠这个保活。
3. **副标题正则清洗**（`-《...》`）（L38、L67）。
4. **封面用响应里的相对路径直接拼**（L84-85）。
5. **"不提供歌词 → 交由其它源补全"的职责分离设计**（L89）—— 插件不必全能，应用层做字段级合并。

---

## 5. LRC Lib（`lrclib.py`，134 行）

### 5.1 接口（`lrclib.py:31`）

```python
_API = "https://lrclib.net/api"
_UA = ("MusicMetaScraper/1.0 (local NAS music tag tool; contact: local@nas.local) "
       "+https://lrclib.net")     # L32-33
```

| 用途 | 方法 | 参数 |
|---|---|---|
| 搜索 | GET `/search` | `track_name`、`artist_name`（可选）、`limit=max(limit,5)` |
| 精确 | GET `/get` | `track_name`、`artist_name`、可选 `album_name`、`duration` |

**无需注册、无需 API key**。⚠️ **UA 是专门定制的**（L32-33）：带工具名 + 联系方式 + 指向 lrclib.net —— 这是 **LRCLIB 官方要求的"identifying User-Agent"**，不是可选项。Go 侧必须照做，否则可能被拉黑。

### 5.2 搜索策略（`search()` L73-109）

```python
params = {"limit": max(limit, 5)}
if artist.strip():
    params["artist_name"] = artist.strip()      # L76 歌手可选
params["track_name"] = title.strip()            # L77 歌名必填
try:
    data = self._request("/search", params)
except Exception:
    data = None                                 # L81 失败 → None（不抛）
metas, seen = [], set()
for item in data or []:
    meta = self._to_meta(item, title, artist)
    if meta is None: continue
    key = meta.song_id or meta.title            # L88 去重键
    if key in seen: continue
    seen.add(key); metas.append(meta)
    if len(metas) >= limit: break               # L93-94 提前截断
# 精确兜底
if not metas and artist.strip():                # L97 只在 search 空结果时才走
    try:
        item = self._request("/get", {
            "artist_name": artist.split("/")[0].strip() or artist.strip(),   # L100 只取第一个歌手
            "track_name": title.strip(),
        })
        if isinstance(item, dict):
            meta = self._to_meta(item, title, artist)
            if meta is not None: metas.append(meta)
    except Exception:
        pass
```

**技巧：`/search` → `/get` 两级降级**（L97-108）。注释（L95-96）说明原因："冷门歌未被收录到搜索索引"——LRCLIB 的搜索索引和精确查询走不同数据路径，`/search` 找不到的歌 `/get` 可能能找到。**只在 search 返回空时才回退**（省一次请求）。
⚠️ `artist.split("/")[0]`（L100）：LRCLIB 的 `artist_name` 只接受**单个歌手**，多歌手要用第一个（`" / "` 分隔符取首段）。这是 LRCLIB API 的硬约束。

### 5.3 解析（`_to_meta` L52-71）

```python
name = (item.get("trackName") or "").strip()
artists = [a.strip() for a in (item.get("artistName") or "").split(",") if a.strip()]  # L54 逗号分隔
meta = SongMeta(
    title=name,
    artist=" / ".join(artists),
    artists=artists or [item.get("artistName") or ""],
    album=(item.get("albumName") or "").strip(),
    duration=int(item.get("duration") or 0),
    source=self.name,
    song_id=str(item.get("id") or ""),
    confidence=simple_score(want_title, want_artist, name, artists),
)
# 同步歌词优先，纯文本兜底
lyrics = (item.get("syncedLyrics") or "").strip() or (item.get("plainLyrics") or "").strip()  # L68
if lyrics:
    meta.extra["lyrics"] = lyrics
```

**关键：歌词在 `search` 阶段就拿到了**（L68）——**不需要 enrich**。这是 LRCLIB 相对所有其它源的最大优势：**一次请求同时返回元数据 + 同步歌词**。

字段：`trackName`、`artistName`（逗号分隔）、`albumName`、`duration`、`id`、**`syncedLyrics`（带时间轴 LRC）**、**`plainLyrics`（纯文本）**。
**优先级：`syncedLyrics` 优先，`plainLyrics` 兜底**（L68）——有 LRC 就用 LRC。

### 5.4 enrich：只在搜索没带歌词时触发（L111-131）

```python
def enrich(self, meta: SongMeta) -> SongMeta:
    if meta.extra.get("lyrics"):        # L113 已有歌词直接返回，不发请求
        return meta
    params = {"track_name": meta.title}
    if meta.artist:
        params["artist_name"] = meta.artist.replace(" / ", ",").split(",")[0].strip()  # L117
    try:
        data = self._request("/get", params)
        if isinstance(data, dict):
            lyrics = (data.get("syncedLyrics") or "").strip() or \
                     (data.get("plainLyrics") or "").strip()               # L121-122
            if lyrics: meta.extra["lyrics"] = lyrics
            if not meta.album and data.get("albumName"):  meta.album = data["albumName"]     # L125-126
            if not meta.duration and data.get("duration"): meta.duration = int(data["duration"])  # L127-128
    except Exception:
        pass
    return meta
```

- **短路优化**（L113）：`search` 阶段已带歌词就**不发请求**——这是全项目最省流量的设计。
- `/get` 还能**回填 album 和 duration**（L125-128），且只在字段为空时覆盖（`not meta.album` / `not meta.duration`）——**不覆盖已有值**，符合"多源字段级合并"原则。

### 5.5 限速/重试/降级

```python
def __init__(self, min_interval: float = 1.0, **kwargs):
    self.min_interval = max(float(min_interval), 1.0)   # L40 至少 1 秒/次
```

- **强制下限 1.0s**（L40）：`max(用户传入值, 1.0)`，用户无法把它调到更快 —— 对应 docstring L17 "官方建议低频访问"。
- 无重试、无退避；超时 15s（L49）。
- 三处 `try/except`（L80-81、L107-108、L129-130）→ 静默降级。

### 5.6 最值得借鉴的技巧（LRCLIB）

1. **一次请求同时返回元数据 + 同步歌词（`syncedLyrics`）**（L68）——**歌词首选源**。
2. **`syncedLyrics` → `plainLyrics` 降级**（L68、L121-122）。
3. **`/search` → `/get` 两级查询**（L97-108），只在空结果时回退。
4. **`enrich` 里 `if meta.extra.get("lyrics"): return meta` 短路**（L113）—— 零冗余请求。
5. **字段只填空不覆盖**（L125-128）—— 多源合并的正确姿势。
6. **标识性 UA**（L32-33）—— 遵守 API 提供方规范，避免被封。
7. **`min_interval = max(x, 1.0)` 硬下限**（L40）—— 把"尊重上游限速"写进代码而非文档。
8. **无 key、无 cookie、无签名** —— 移植成本最低。

---

## 6. TheAudioDB（`theaudiodb.py`，123 行）

### 6.1 接口（`theaudiodb.py:32`）

```python
_API = "https://www.theaudiodb.com/api/v1/json/2"     # L32  "2" 就是 demo key
```

| 用途 | 方法 | 参数 |
|---|---|---|
| 搜索 | GET `searchtrack.php` | `s=<artist or title>`、`t=<title>` |
| 详情 | GET `track.php` | `i=<idTrack>` |

- **`/json/2/` 里的 `2` 是免费 demo API key**（docstring L10 说明"demo key '2' 随 API 公开"）。
- ⚠️ demo key **限 1 次/秒**（docstring L18）→ `min_interval = max(float(min_interval), 1.0)`（L41），与 LRCLIB 同款硬下限。
- 无 cookie、无 key 申请、无签名。

### 6.2 搜索策略（`search()` L78-98）

```python
params = {"s": artist.strip() or title.strip()}   # L79  s = 歌手，歌手为空时退化为歌名
params["t"] = title.strip()                       # L80  t = 歌名
try:
    d = self._request("searchtrack.php?" + urllib.parse.urlencode(params))
except Exception:
    return []                                     # L84 失败 → 空
for item in d.get("track") or []:                 # L87  响应键是单数 "track"（数组）
    meta = self._to_meta(item, title, artist)
    if meta is None: continue
    key = meta.song_id or (meta.title + meta.artist)   # L91 去重键
    ...
    if len(metas) >= limit: break
```

**⚠️ 必须双参数**：`s`（歌手）+ `t`（歌名）。TheAudioDB 的 `searchtrack.php` 是**"歌手 + 曲目"精确匹配**，不是模糊搜索。**歌手为空时用歌名填 `s`**（L79）是一种退化兜底，实际命中率很低。
⚠️ **响应键是 `track`（单数）但值是数组** —— 反直觉，Go 侧 struct tag 别写错。

**字段映射（全项目字段最"厚"的一个源）**（L53-76）：

| SongMeta | TheAudioDB 字段 | 备注 |
|---|---|---|
| title | `strTrack` | |
| artist / artists | `strArtist` | 单值 |
| album | `strAlbum` | |
| date | `intYearReleased` | **只有年份**，非完整日期 |
| **genre** | `strGenre` | ✅ 中文源普遍缺失 |
| **publisher** | `strLabel` | ✅ 唱片公司 |
| song_id | `idTrack` | 用于 `track.php` 详情 |
| comment | `strDescriptionEN[:500]` | L74-75 **截断 500 字符** |
| extra.cover_url | `strTrackThumb` **or** `strAlbumThumb` | L58-59 两级兜底 |

```python
cover = (item.get("strTrackThumb") or "").strip() or \
        (item.get("strAlbumThumb") or "").strip()     # L58-59  曲目图优先，专辑图兜底
...
if item.get("strDescriptionEN"):
    meta.comment = item["strDescriptionEN"][:500]     # L74-75  截断，避免标签爆炸
```

**技巧**：`comment` 截断 500 字符（L75）—— 该字段是英文长文本简介，不截断会写爆 ID3 注释标签。

### 6.3 enrich（L100-120）—— "只填空"模式

```python
def enrich(self, meta: SongMeta) -> SongMeta:
    if not meta.song_id: return meta
    try:
        d = self._request("track.php?" + urllib.parse.urlencode({"i": meta.song_id}))
        t = (d.get("track") or [{}])[0]
        if not meta.genre:     meta.genre = (t.get("strGenre") or "").strip()        # L107-108
        if not meta.publisher: meta.publisher = (t.get("strLabel") or "").strip()    # L109-110
        if not meta.date:      meta.date = str(t.get("intYearReleased") or "").strip() # L111-112
        if not meta.extra.get("cover_url"):
            cover = (t.get("strTrackThumb") or "").strip() or \
                    (t.get("strAlbumThumb") or "").strip()
            if cover: meta.extra["cover_url"] = cover                                # L113-117
    except Exception:
        pass
    return meta
```

**每一个字段都带 `if not meta.X` 守卫**（L107/109/111/113）—— 与 LRCLIB 相同的"只填空不覆盖"原则。**这是多源合并的黄金法则**：后续源的 enrich 不能覆盖前面源已有的值。

### 6.4 限速/重试/降级

- `min_interval` 硬下限 1.0（L41）。
- `_request` L47 有 throttle；无重试；超时 15s（L50）。
- 搜索失败返回 `[]`（L83-84）；enrich 静默（L118-119）。

### 6.5 最值得借鉴的技巧（TheAudioDB）

1. **genre / publisher / comment 三个中文源拿不到的字段**（L66-67、L75）—— 互补型源的核心价值。
2. **`strTrackThumb` → `strAlbumThumb` 封面两级兜底**（L58-59）。
3. **长文本截断 500 字符**（L75）。
4. **`enrich` 全字段"只填空"守卫**（L107-113）—— 多源合并的正确姿势。
5. **demo key 写死在 URL 路径里**（L32）—— 零配置，但必须限速。

---

## 7. iTunes Search（`itunes.py`，71 行，官方示例源）

### 7.1 接口（`itunes.py:26`）

```python
SEARCH_URL = "https://itunes.apple.com/search"
```

请求（L43-45）：

```python
url = self.SEARCH_URL + "?" + urllib.parse.urlencode({
    "term": term, "media": "music", "limit": max(limit, 5),
    "country": "CN", "entity": "song"})
```

- `media=music` + `entity=song`：**限定返回歌曲级结果**（不含专辑/艺人/播客）。
- `country=CN`：中国区商店 —— 决定曲库与结果排序。
- Header：`User-Agent: music-meta-web/1.0 (source plugin)`（L37）——**标识性 UA**，非浏览器伪装。
- 无 key、无 cookie、无签名。

### 7.2 解析（`search()` L41-68）

```python
term = f"{title} {artist}".strip()                        # L42
for item in data.get("results", []):                      # L48
    title_  = (item.get("trackName") or "").strip()
    artist_ = (item.get("artistName") or "").strip()
    if not title_ or not artist_: continue
    meta = SongMeta(
        title=title_, artist=artist_,
        album=item.get("collectionName") or "",           # L55 专辑名
        date=(item.get("releaseDate") or "")[:10],        # L56 ISO8601 取前 10 位 → YYYY-MM-DD
        genre=item.get("primaryGenreName") or "",         # L57
        duration=int(item.get("trackTimeMillis") or 0) // 1000,   # L58 毫秒 → 秒
        source=self.name,
        song_id=str(item.get("trackId") or ""),
        album_id=str(item.get("collectionId") or ""),
        confidence=80.0,                                  # L62 ⚠️ 固定值！
    )
    art = item.get("artworkUrl100") or ""
    if art:
        meta.extra["cover_url"] = art.replace("100x100", "300x300")   # L66 尺寸替换
    metas.append(meta)
```

**字段映射**：`trackName`、`artistName`、`collectionName`、`releaseDate[:10]`、`primaryGenreName`、`trackTimeMillis//1000`、`trackId`、`collectionId`、`artworkUrl100`。

**技巧：封面尺寸字符串替换**（L66）：

```python
art.replace("100x100", "300x300")
```

`artworkUrl100` 形如 `https://.../100x100bb.jpg`，把 `100x100` 替换成 `300x300` 即可拿到高分辨率版（还可 `600x600`、`100000x100000` 拿原图）。**零额外请求、零接口调用**——iTunes 封面的标准技巧。

### 7.3 三个明显问题（Go 侧必须修正）

1. **`confidence = 80.0` 硬编码**（L62）—— **完全不比较歌名歌手**。任何搜索结果都拿 80 分。若 `min_confidence` 设为 80 或更低，会直接误写元数据。这是"示例源"的偷懒实现，**不可用于生产**。
2. **`artists` 字段未设置**（L53-63 无 `artists=` 参数）→ `SongMeta.artists` 保持空列表 `[]`，只有 `artist` 字符串。后续任何用 `artists` 做比对/去重的逻辑都会失效。
3. **无 `enrich` 方法**，无歌词能力。

### 7.4 限速/重试/降级

- `min_interval` 默认 0.3（L28），**无硬下限**（iTunes 官方限速约 20 次/分钟，0.3s 偏快）。
- `_get_json` L35 有 throttle；无重试；超时 15s（L38）。
- 无 try/except → 网络异常向上抛。

### 7.5 最值得借鉴的技巧（iTunes）

1. **`artworkUrl100` → `replace("100x100", "300x300")`**（L66）—— 封面多档分辨率的最低成本方案。
2. **`media=music` + `entity=song`**（L44）—— 限定实体类型，避免专辑/艺人污染结果。
3. **`releaseDate[:10]`**（L56）—— iTunes 返回 ISO8601，截取前 10 位即 `YYYY-MM-DD`。
4. **`trackTimeMillis // 1000`**（L58）—— 毫秒→秒的统一换算（与网易同）。
5. **标识性 UA**（L37）。

---

## 8. 音频指纹方案

### 8.1 用途

当**文件名匹配失败或不可信**时（如 `track01.mp3`、乱码文件名、`未知艺术家 - 未知歌曲`），用**音频内容本身**识别歌曲。链路：

```
本地音频文件
  → fpcalc（Chromaprint 算法）计算声学指纹
  → AcoustID 服务比对指纹库，返回 MusicBrainz 录音 ID + 歌名/歌手/专辑
  → 用识别结果去 QQ 音乐搜索，补齐封面/年份/歌词等完整元数据
```

指纹是**内容级**识别（不受文件名/标签影响），可信度高于任何文本匹配，是刮削器的最后一道兜底。

### 8.2 实现：`app/server/musicmeta/sources/fingerprint.py`（95 行）

```python
ACOUSTID_URL = "https://api.acoustid.org/v2/lookup"     # L20
FPCALC_DEFAULT = "/usr/bin/fpcalc"                      # L21
```

**入口 `recognize`（L24-32）**：

```python
def recognize(path: str, api_key: str, fpcalc: str = FPCALC_DEFAULT) -> List[dict]:
    duration, fp = fingerprint_file(path, fpcalc)
    return lookup_acoustid(fp, duration, api_key)
```

**第 1 步 `fingerprint_file`（L35-43）—— 调外部二进制**：

```python
def fingerprint_file(path: str, fpcalc: str = FPCALC_DEFAULT,
                     timeout: int = 180) -> Tuple[float, str]:
    proc = subprocess.run([fpcalc, "-json", path],
                          capture_output=True, text=True, timeout=timeout)   # L38
    if proc.returncode != 0:
        raise RuntimeError(f"fpcalc 失败: {(proc.stderr or proc.stdout)[:200]}")   # L41
    data = json.loads(proc.stdout)
    return float(data["duration"]), data["fingerprint"]     # L43
```

- 依赖 **`fpcalc` 可执行文件**（Chromaprint 工具，Debian 包 `libchromaprint-tools`，或官方静态版）。
- `-json` 让输出可解析；返回 `{"duration": 秒, "fingerprint": "<base64/压缩编码串>"}`。
- 超时 **180s**（L36）—— 本地计算，大文件也够。
- Go 侧：`exec.Command("fpcalc", "-json", path)`，或引入 `go-chromaprint`（cgo 绑定）。

**第 2 步 `lookup_acoustid`（L46-93）—— 查 AcoustID**：

```python
params = {
    "client": api_key,
    "duration": int(round(duration)),        # L54 必须与指纹匹配的时长
    "fingerprint": fingerprint,
    "meta": "recordings+releasegroups",      # L56 一次带出录音 + 发行组（专辑）
    "format": "json",
}
url = ACOUSTID_URL + "?" + urllib.parse.urlencode(params)      # L59 注意：GET 且指纹在 query 里
req = urllib.request.Request(
    url, headers={"User-Agent": "music-meta-web/1.0 (audio fingerprint)"})
with urllib.request.urlopen(req, timeout=timeout) as resp:
    data = json.loads(resp.read().decode("utf-8", "replace"))

if data.get("status") != "ok":                                 # L65 状态检查
    err = data.get("error") or {}
    raise RuntimeError(f"AcoustID 查询失败: {err.get('message') or data.get('status')}")

results: List[dict] = []
seen: set = set()
for result in data.get("results", []):                          # L72 外层：匹配结果（带 score）
    for rec in result.get("recordings", []):                    # L73 内层：录音
        title = (rec.get("title") or "").strip()
        artists = [a.get("name", "") for a in rec.get("artists", [])]
        artist = " / ".join(a for a in artists if a)
        if not title or not artist: continue                    # L77-78 缺一不可
        rgs = rec.get("releasegroups", [])
        album = rgs[0].get("title", "") if rgs else ""          # L80 只取第一个发行组
        key = (title.lower(), artist.lower())                   # L81 去重键
        if key in seen: continue
        seen.add(key)
        results.append({"title": title, "artist": artist, "album": album,
                        "score": float(result.get("score", 0))})   # L85-90
results.sort(key=lambda r: r["score"], reverse=True)             # L92 按匹配分降序
return results
```

关键设计点：

| 点 | 说明 |
|---|---|
| `duration` 必须随指纹一起提交 | AcoustID 用它做初筛，不传或传错会漏配 |
| `meta=recordings+releasegroups` | 一次请求同时拿"录音"和"发行组（专辑）"，**避免二次请求 MusicBrainz** |
| **双层遍历** `results[].recordings[]` | 外层 `score` 是"指纹匹配度"（0~1），内层是录音实体；**一个指纹可能匹配多条录音** |
| 去重键 `(title.lower(), artist.lower())` | 同一首歌可能被多个 MusicBrainz 录音条目指向 |
| `score` 透传并降序排序 | 让调用方（`qqmusic.recognize_by_fingerprint` L479）取 top3 |
| 明确只取 `title/artist/album` | docstring L50 说明："年份由 QQ 专辑详情提供更准确" —— **指纹只负责"找到是哪首歌"，元数据交给专业源** |

**调用方**：`qqmusic.py:466-487` 的 `recognize_by_fingerprint()`（见 §1.10）——取前 3 个识别结果各搜一次 QQ，合并去重后按 confidence 排序。

### 8.3 `app/server/musicmeta/fingerprint.py`（22 行）—— 兼容转发层

```python
"""`musicmeta.fingerprint` 兼容入口。

实现本体在 `musicmeta.sources.fingerprint`；早期发布的数据源插件写的是
`from musicmeta.fingerprint import recognize`（qpublicmusic 等插件至今如此），
为兼容**已安装的旧插件**（用户插件目录里的 .py 不会随应用升级而更新），
这里保留一层转发，避免指纹模式报 ModuleNotFoundError。

新代码请直接从 `musicmeta.sources.fingerprint` 导入。
"""
from musicmeta.sources.fingerprint import (  # noqa: F401
    ACOUSTID_URL, FPCALC_DEFAULT, fingerprint_file, lookup_acoustid, recognize,
)
__all__ = ["ACOUSTID_URL", "FPCALC_DEFAULT", "fingerprint_file", "lookup_acoustid", "recognize"]
```

**这是一层纯 re-export 垫片（shim）**，无任何逻辑。存在原因：模块从 `musicmeta.fingerprint` 迁移到了 `musicmeta.sources.fingerprint`，但**用户插件目录里的 `.py` 不会随应用升级而更新**（`registry.discover_plugins` 是按路径加载用户文件），旧插件仍 `from musicmeta.fingerprint import recognize` → 保留旧路径避免 `ModuleNotFoundError`。

**对 Go 项目的启示**：Go 是编译期链接，不存在这种"用户侧旧代码不更新"的问题；但如果你做了插件/脚本扩展机制（如 Lua/JS 脚本或 gRPC 插件），**API 路径的向后兼容垫片是必须的**。

### 8.4 前置条件与成本

| 项 | 要求 |
|---|---|
| `fpcalc` 二进制 | `/usr/bin/fpcalc`（`libchromaprint-tools` 或官方静态版） |
| AcoustID API key | 免费注册 https://acoustid.org/new-application |
| 计算成本 | 每首歌调用一次外部进程，超时 180s |
| 网络请求 | 1 次（GET，指纹串在 query string 里，**URL 可能很长**） |
| 依赖 | 零 Python 第三方库，但**强依赖外部二进制** |

---

## 9. 横向对比与工程结论

### 9.1 7 个源能力矩阵

| 源 | 需要 cookie/key | 歌词 | 封面 | 字段完整度 | 反爬强度 | 推荐使用顺序 |
|---|---|---|---|---|---|---|
| **qqmusic** | ❌ 无（需伪装 web 播放器参数 `ct=24`/`qqmusic_uin=0`/`platform=yqq.json`） | ✅✅ LRC（双接口 + JSONP + HTML 实体兜底） | ✅ albummid 拼 URL，800/500/300 三档 + 魔数校验 | ★★★★★ date/genre/publisher/language/track/disc/track_total/album_artist 全有（靠专辑详情反查） | 高（`subcode=-10003` 风控，需多域轮换 + 退避） | **1**（主力） |
| **netease** | ❌ 无（走 `/api/` 老接口，非 `/weapi/` 加密） | ✅✅ LRC 原文（`lv=-1`），另可拿翻译 `tv=-1` / 逐字 `kv=-1` | ✅ `album.picUrl`（支持 `?param=300y300`） | ★★★☆ title/artist/album/date(ms)/duration，无 genre/publisher | 中（近年收紧，可能需 `NMTID` cookie） | **2** |
| **kugou** | ❌ 无（只带 UA） | ✅ LRC（两步：search 拿 accesskey → download 拿 base64） | ✅ FileHash 拼 URL（零请求） | ★★★ title/artist/album/duration | 中低（接口稳定，但 FileHash 不能当稳定 ID） | **3** |
| **lrclib** | ❌ 无 key、无 cookie（**必须带标识性 UA**） | ✅✅✅ `syncedLyrics` 优先 / `plainLyrics` 兜底，**search 阶段即返回** | ❌ 无 | ★★☆ title/artist/album/duration | 极低（但强制 ≥1s/次） | **歌词首选**；元数据第 4 |
| **kuwo** | ❌ 无（伪装 `client=kt` + `ver=kwplayer_ar_9.2.2.1`） | ❌ **接口已失效**（由应用回查 QQ 补） | ✅ `web_albumpic_short` 拼 URL（零请求） | ★★☆ title/artist/album/duration，歌手不切分 | 低（HTTP + JS 字面量返回） | **5** |
| **theaudiodb** | ❌ 无（demo key `2` 在 URL 里） | ❌ 无 | ✅ `strTrackThumb` → `strAlbumThumb` 两级兜底 | ★★★★ **genre/publisher/comment** 是独有价值；**仅欧美/日韩** | 极低（demo key 限 1 次/秒） | **6**（国际曲目补充） |
| **itunes** | ❌ 无 | ❌ 无 | ✅ `artworkUrl100` → `replace("100x100","300x300")` | ★★★ genre/date/album/duration；**confidence 硬编码 80，无匹配校验** | 低（限速约 20 次/分） | **7**（示例源，需自行补打分才可用） |

### 9.2 推荐的多源流水线（给 Go 项目的落地建议）

```
阶段 0（可选）：文件名/标签解析失败 → fpcalc + AcoustID 指纹识别 → 得到 (title, artist)
阶段 1 搜索：qqmusic → netease → kugou → kuwo → itunes → theaudiodb
              （按 confidence 排序，取 top-1；未过阈值则降级下一源）
阶段 2 歌词：lrclib（syncedLyrics，最省事） → qqmusic（fcg_query_lyric_new）
              → netease（lv=-1 + tv=-1 合并翻译） → kugou（两步 base64）
阶段 3 封面：qqmusic（albummid 拼 URL，800 优先）→ netease（picUrl）
              → kugou（FileHash 拼 URL）→ kuwo（web_albumpic_short）
阶段 4 补充：theaudiodb（genre/publisher/year/comment，仅国际曲目）
阶段 5 曲目号：qqmusic 专辑详情反查（songmid → track/disc/track_total）
```

**关键原则**：
- 阶段 1 的**选中源**决定 song_id/album_id，后续 enrich 用**同一源**的 ID 体系；
- 阶段 2/3/4 是**字段级合并**：每个 enrich 都必须 `if not meta.X` 守卫，**只填空不覆盖**（LRCLIB L125-128、TheAudioDB L107-113 是范本）；
- 全程共用**一个全局限速器**，`min_interval` 取所有源中最保守的值（LRCLIB/TheAudioDB 的 1.0s）。

### 9.3 所有源共有的架构约定（Go 侧应照搬）

| 约定 | 实现位置 | Go 对应 |
|---|---|---|
| 统一数据模型 + `extra` 放封面/歌词 | `base.py:49-74` | struct + `Extra map[string]string` |
| 全局共享限速器 + 0.5~1.5x 抖动 | `ratelimit.py:28-38` | 包级 `sync.Mutex` + `lastReq` + `rand` |
| `search()` 返回按 confidence 降序的候选列表 | `base.py:85` | 同 |
| `enrich()` 失败静默返回原 meta | 各源 | 返回 `error` 但调用方忽略 |
| 插件注册表 + 按名实例化 | `registry.py:48-59` | `map[string]Factory` |
| 封面/歌词放 `extra`，插件不写盘 | `SPEC.md:45` | 同 |

### 9.4 各源"最值得借鉴的一条"速查

| 源 | 一句话技巧 |
|---|---|
| qqmusic | **用专辑 ID 反查曲目号/碟号/总曲目**（`qqmusic.py:370-379`），一次请求补 8 个字段 |
| netease | **`lv/kv/tv=-1` 三档歌词 + `ids=[...]` 批量详情**（`netease.py:88,97-98`） |
| kugou | **封面由 FileHash 拼 URL（零请求）+ 两步式 base64 歌词**（`kugou.py:99-100,111-122`） |
| kuwo | **`find("{")`/`rfind("}")` + `ast.literal_eval` 解析非标准 JSON**（`kuwo.py:59-62`） |
| lrclib | **search 阶段就返回 syncedLyrics，enrich 短路**（`lrclib.py:68,113`） |
| theaudiodb | **genre/publisher/comment 互补字段 + 全字段"只填空"守卫**（`theaudiodb.py:107-113`） |
| itunes | **`artworkUrl100.replace("100x100","300x300")` 免费换高清封面**（`itunes.py:66`） |
| fingerprint | **`meta=recordings+releasegroups` 一次请求拿全，指纹只负责"是哪首歌"**（`sources/fingerprint.py:56`） |

### 9.5 已发现的缺陷/风险清单（Go 实现时需修正）

| # | 位置 | 问题 | 影响 |
|---|---|---|---|
| 1 | `base.py:28-46` `simple_score` | 标题不匹配**不扣分**，只有 40 分保底 | 5 个源（netease/kugou/kuwo/lrclib/theaudiodb）在无阈值时会误匹配 |
| 2 | `base.py:82` `min_confidence = 0.0` | 基类默认阈值 0，除 qqmusic 外全部无过滤 | 误写元数据 |
| 3 | `qqmusic.py:167` | `min_confidence=40.0` 恰等于 `simple_score` 最低分 40.0 | `40 < 40` 为假 → **QQ 的阈值也形同虚设** |
| 4 | 全部 7 源 | **无任何时长比对**（`meta.duration` 拿到了但从不参与打分） | 丢掉最强的二次校验信号 |
| 5 | `netease.py:73` | `time.localtime` 依赖本地时区 | 跨时区日期差一天 |
| 6 | `kugou.py:78,92` | `FileHash` 当作 `song_id` | hash 随码率变化，**无法去重/缓存命中** |
| 7 | `kugou.py:70` | `m2` 正则提取结果未使用（死代码） | 歌单式条目 `歌手《歌名》` 未被清洗 |
| 8 | `kugou.py:114` | 歌词只取 `candidates[0]`，无时长筛选 | 可能拿到错误版本（现场/翻唱）的歌词 |
| 9 | `itunes.py:62` | `confidence = 80.0` 硬编码 | 任何结果都过阈值，**生产不可用** |
| 10 | `itunes.py:53-63` | 未设置 `artists` 列表字段 | 依赖 `artists` 的逻辑失效 |
| 11 | `netease.py:97-98` | 未取 `tlyric`（翻译歌词） | 浪费国内最好的翻译歌词源 |
| 12 | `qqmusic.py:429` | 未取 `trans`（翻译歌词） | 同上 |
| 13 | `kuwo.py:36,48` | 接口是 **HTTP**（明文） | 可能被强制跳转/劫持 |
| 14 | `kugou.py:37-38` | 歌词接口是 **HTTP** | 同上 |
| 15 | 全部 7 源 | **无繁简转换、无别名/罗马音归一化** | 繁体文件名 vs 简体元数据会失配（`normalize_text` 只做 NFKC） |
| 16 | `netease/kugou/kuwo/itunes` | 无重试、无多域名、无退避 | 单次网络抖动即失败（仅 qqmusic 做了） |
| 17 | `netease.py:39-40` | `__init__` 不接收 `min_confidence` | 与 qqmusic 接口不一致，无法配置阈值 |
| 18 | `kuwo.py:68` | `ARTIST` 字段未按分隔符切分多歌手 | `artists` 列表不准确 |

### 9.6 值得补充但项目未实现的能力（Go 侧的机会）

1. **繁简归一化**：用 OpenCC 或 `golang.org/x/text/transform` + 简繁映射表，在 `normalize_text` 里加一步 `t2s()`。对华语曲库命中率提升明显（`周杰倫` vs `周杰伦`）。
2. **别名/罗马音归一化**：`"Jay Chou"` vs `"周杰伦"`、`"Beyond"` vs `"BEYOND"`。可用别名表或拼音库（`github.com/mozillazg/go-pinyin`）。
3. **时长二次校验**：`abs(meta.duration - local_duration) <= 2` 才接受。QQ 源已经拿到了 `interval`，只差比对。
4. **歌词翻译合并**：netease 的 `tlyric` + `lrc` 按时间轴合并成双语 LRC。
5. **多源字段级合并引擎**：把 7 个源的 `enrich` 串成"按字段优先级填空"的 pipeline（LRCLIB/TheAudioDB 已示范单源内的做法）。
6. **搜索结果缓存**：`cache.py:269-297` 的 `search_hit`/`search_store` 提供"7 天搜索缓存 + 命中后重新打分"的完整范式（`search_hit` L285 重新调用 scorer，避免缓存了旧分数）。
7. **`normalize_text` 去标点但保留 CJK** 的实现细节值得保留（`[^\w]+` + `re.UNICODE`）—— Go 里注意 `\w` 在 Go regexp 中不支持 Unicode 类，需手写 `unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'`。

---

## 附：文件索引

| 文件 | 行数 | 说明 |
|---|---|---|
| `data-source-plugins/qqmusic.py` | 519 | 最完整：多域轮换/重试/退避/封面多档/歌词双接口/指纹回查 |
| `data-source-plugins/netease.py` | 107 | POST 搜索 + 详情封面 + 三档歌词参数 |
| `data-source-plugins/kugou.py` | 130 | 伪歌手黑名单 + hash 拼封面 + 两步 base64 歌词 |
| `data-source-plugins/kuwo.py` | 92 | 客户端伪装参数 + `literal_eval` 宽松解析，无歌词 |
| `data-source-plugins/lrclib.py` | 134 | 歌词首选源：search 即返回 syncedLyrics |
| `data-source-plugins/theaudiodb.py` | 123 | genre/publisher/comment 互补字段，仅国际曲目 |
| `data-source-plugins/itunes.py` | 71 | 官方示例源，confidence 硬编码，仅作参考 |
| `data-source-plugins/SPEC.md` | 56 | 插件开发规范（模板 + SongMeta 字段说明 + 打分约定） |
| `app/server/musicmeta/ratelimit.py` | 38 | 全局共享限速器（锁 + 时间戳 + 0.5~1.5x 抖动） |
| `app/server/musicmeta/sources/base.py` | 100 | `SongMeta` / `normalize_text` / `simple_score` / `MetaSource` |
| `app/server/musicmeta/sources/registry.py` | 112 | 插件注册表 + 按路径动态加载 |
| `app/server/musicmeta/sources/fingerprint.py` | 95 | fpcalc + AcoustID 实现本体 |
| `app/server/musicmeta/fingerprint.py` | 22 | 旧导入路径兼容垫片（纯 re-export） |
| `app/server/musicmeta/cache.py` | 297 | 搜索缓存范式（`search_hit` L269-290 命中后重新打分） |
