"""曲率 · musicdl sidecar —— 「外挂进程」形态的搜索器 / 解析器。

曲率的 Go 后端把「原生做不到」的平台交给它：musicdl 覆盖十几家音源，其中
咪咕 / 千千 / B站 是 Go 侧完全没有的，酷狗 / 酷我 是「搜得到、放不出」的。

# 三个契约（都是 JSON）

    GET  /healthz
      → {"ok":true,"version":"…","musicdl":true,"sources":["mg","bq","bi"],"cached":12}

    POST /search    {"keyword":"…","sources":["mg"],"limit":15}
      → {"ok":true,
         "items":[{"id","source","platform_id","title","artist","album","duration_s",
                   "ext","file_size","cover_url","download_url","album_mid","keyword"}],
         "errors":{"<短码>":{"kind":"unavailable|invalid","message":"…"}}}

    POST /resolve   {"items":[{"id","source","title","artist"}]}
      → {"ok":true,
         "items":[{"id","source","url","headers","ext","file_size","duration_s","cover_url","lyric"}],
         "errors":{"<id>":{"kind":"…","message":"…"}}}

# 为什么 resolve 要带歌名 / 歌手（而不是只给 id）

musicdl 的直链是**搜索结果对象上的一个字段**，不是「拿 id 去问」能拿到的。
所以解析一条曲目只有两条路：

  (a) sidecar 自己长期缓存搜索结果，之后按 id 反查；
  (b) 拿歌名 + 歌手回搜一次，再按 id / 歌名 / 歌手匹配。

参考实现走的是 (a)，代价是**进程一重启，所有已入库曲目全部解析失败**
（它给调用方返回「缓存过期了，请重新搜索」—— 而曲率的曲目是存在 NAS 库里的，
那些 id 不会因为 sidecar 重启就失效）。这里选 (b)：多一次搜索，但**无状态**、
重启无感；进程内缓存只当加速用，丢了也不影响正确性。

# 为什么每个源各自 try/except

一个平台被风控 / 超时，不该让整次搜索失败 —— 用户要的是「少一个平台」，
不是「搜索坏了」。所以每个源独立跑、独立的错误条目，`errors` 里如实写清原因
（`kind` 用来区分「平台不可用」与「我们参数写错了」，见 core.error_kind）。
"""

import asyncio
import os
import threading
import time

from fastapi import FastAPI
from fastapi.responses import JSONResponse

import core

VERSION = "1.0.0"

# ── 配置（全部走环境变量，由 Go 侧在 spawn 时注入）──────────────────────

PORT = int(os.environ.get("QULV_SIDECAR_PORT", "8901"))


def _registered_clients():
    """问 musicdl 要它注册了哪些源（真机上是 56 个）。

    拿不到（没装 musicdl / 上游改了内部结构）就返回空 —— 调用方会退回
    `core.ALIASES` 里那几个，功能少几个源而不是整个起不来。
    """
    if not MUSICDL_OK:
        return []
    try:
        builder = getattr(musicdl_lib, "MusicClientBuilder", None)
        reg = getattr(builder, "REGISTERED_MODULES", None)
        if isinstance(reg, dict):
            return sorted(reg.keys())
        if reg:
            return sorted(reg)
    except Exception as exc:  # pragma: no cover - 上游结构相关
        print("[sidecar] 读 musicdl 源注册表失败：%r" % (exc,), flush=True)
    return []


LIMIT = core.clamp_limit(os.environ.get("QULV_MUSICDL_LIMIT", ""))
WORK_DIR = os.environ.get("QULV_MUSICDL_WORKDIR", "/tmp/qulv-musicdl")
# 单次出站请求超时（秒）。musicdl 内部一次 search 会打多次请求，这里给的是**每次**。
REQUEST_TIMEOUT = float(os.environ.get("QULV_MUSICDL_TIMEOUT", "12"))
# 单源整次搜索的总预算（秒）。超了就放弃这个源 —— 一个源卡住不能拖垮整次搜索。
#
# ⚠️ 实测（v2.1.85 真机）：千千（qianqian）会跑满这个预算 —— 它自己按
# search_size 翻页，翻得慢。25 秒意味着「打开外挂音源之后，飞牛里每次搜索都要等
# 二十多秒」。降到 12 秒：正常的源（咪咕实测 2-5 秒）够用，慢的源宁可这次不出结果。
# Go 侧对「官方页面搜索合并」另有 8 秒总闸（见 pkg/intercept/search.go）。
SOURCE_BUDGET = float(os.environ.get("QULV_MUSICDL_SOURCE_BUDGET", "12"))

os.makedirs(WORK_DIR, exist_ok=True)

# musicdl 是**可选**依赖：没装（或 venv 还没建好）时 sidecar 照样要能起来并
# 如实报告自己不可用 —— 让 Go 侧「少几个平台」而不是「sidecar 起不来」。
try:
    # ⚠️ `MusicClient` 在**子模块** `musicdl.musicdl` 上，不在包根上。
    # `import musicdl` 之后写 `musicdl.MusicClient` 会炸在运行期，而且只有真去搜索
    # 才暴露 —— 真机上就是这么发现的：`module 'musicdl' has no attribute 'MusicClient'`
    # （`error_kind` 把它归成 invalid，所以日志里一眼能看出是**我们**写错了，
    # 不是平台不可用）。
    from musicdl import musicdl as musicdl_lib

    MUSICDL_OK = True
    MUSICDL_ERR = ""
except Exception as exc:  # pragma: no cover - 环境相关
    musicdl_lib = None
    MUSICDL_OK = False
    MUSICDL_ERR = "%s: %s" % (type(exc).__name__, exc)


# ⚠️ 这一段**必须在上面那个 try/except 之后**：`_registered_clients()` 会读
# `MUSICDL_OK`，而它是上面才赋值的。放在前面就是 `NameError` —— 而且现象很难查：
# uvicorn 加载不了 app → 进程直接退出，日志里只有一长串 traceback，不看到最后一行
# 看不出是「模块级语句顺序」问题（真机上就栽过一次，v2.1.89）。
REGISTERED = _registered_clients()
core.register_known(REGISTERED)

SOURCES = core.normalize_sources(os.environ.get("QULV_MUSICDL_SOURCES", ""))


# ── 进程内缓存（只当加速用）──────────────────────────────────────────────
#
# 命中就用，不命中回搜 —— 所以丢了不影响正确性。上限按条数控制，超了淘汰最旧一半。

_CACHE = {}
_CACHE_LOCK = threading.Lock()
CACHE_MAX = 512


def cache_get(sid):
    with _CACHE_LOCK:
        entry = _CACHE.get(sid)
    if not entry:
        return None
    return entry["item"]


def cache_put(item):
    if not item or not item.get("id"):
        return
    with _CACHE_LOCK:
        if len(_CACHE) >= CACHE_MAX:
            for key in sorted(_CACHE, key=lambda k: _CACHE[k]["ts"])[: CACHE_MAX // 2]:
                _CACHE.pop(key, None)
        _CACHE[item["id"]] = {"item": item, "ts": time.time()}


def cache_size():
    with _CACHE_LOCK:
        return len(_CACHE)


# ── musicdl 调用（阻塞，必须放到线程里跑）──────────────────────────────


def search_source_sync(short, keyword, limit):
    """在**单个源**上搜索，返回归一化后的条目列表（阻塞）。"""
    if not MUSICDL_OK:
        raise RuntimeError("musicdl 不可用：%s" % MUSICDL_ERR)
    # ⚠️ 用 `client_name()` 而不是查表：源有 57 个、短码是**派生**的
    # （`apple` ← `AppleMusicClient`），写死的表里只有与原生重叠的那几个。
    # 这里踩过一次：改名之后这里还写着 `core.SOURCES[short]`，于是每次搜索都
    # `AttributeError` —— 而它只在真机上暴露（本机导不进 app.py）。
    full = core.client_name(short)
    if not full:
        raise ValueError("认不出这个源：%r（没在 musicdl 注册表里报过）" % short)
    client = musicdl_lib.MusicClient(
        music_sources=[full],
        init_music_clients_cfg={
            full: {
                # 翻页单位，且每条都会先解析直链 —— 见 core 里 DEFAULT_LIMIT 的说明。
                "search_size_per_source": limit,
                "work_dir": WORK_DIR,
                "max_retries": 1,
            }
        },
        clients_threadings={full: 1},
        requests_overrides={full: {"timeout": REQUEST_TIMEOUT}},
    )
    result = client.search(keyword=keyword) or {}
    songs = result.get(full) or next(iter(result.values()), []) or []
    out = []
    for song in songs:
        item = core.normalize_song(song, short, keyword)
        if item["id"] == short + ":":
            continue  # 没有平台内 id 的条目留着也没用（解析时对不上）
        cache_put(item)
        out.append(item)
    return out


async def search_source(short, keyword, limit):
    """带超时的单源搜索：超时按「平台不可用」处理，不往上抛。"""
    try:
        return await asyncio.wait_for(
            asyncio.to_thread(search_source_sync, short, keyword, limit),
            timeout=SOURCE_BUDGET,
        )
    except asyncio.TimeoutError:
        raise TimeoutError("源 %s 超过 %.0fs 预算" % (short, SOURCE_BUDGET))


async def resolve_one(item):
    """解析一条曲目：先查进程内缓存，未命中就**回搜**再匹配（无状态）。"""
    sid = str(item.get("id") or "")
    short, platform_id = core.parse_song_id(sid)
    if not short:
        short = core.normalize_sources([item.get("source") or ""])
        short = short[0] if short else ""
    if not short:
        raise ValueError("认不出这条曲目的平台：id=%r source=%r" % (sid, item.get("source")))
    if not platform_id:
        platform_id = str(item.get("platform_id") or "")

    hit = cache_get(sid)
    if hit and hit.get("download_url"):
        return hit

    title = str(item.get("title") or "")
    artist = str(item.get("artist") or "")
    keyword = " ".join(x for x in (title, artist) if x).strip()
    if not keyword:
        raise ValueError("解析需要 title 或 artist（musicdl 的直链只能从搜索拿到）")

    candidates = await search_source(short, keyword, LIMIT)
    matched = core.pick_match(candidates, sid, title, artist)
    if matched is None:
        raise LookupError("回搜 %s 的「%s」没有匹配到该曲目" % (short, keyword))
    if not matched.get("download_url"):
        raise LookupError("匹配到了但 %s 没给直链（平台受限或该曲目不可播）" % short)
    cache_put(matched)
    return matched


# ── HTTP ────────────────────────────────────────────────────────────────

app = FastAPI(title="曲率 musicdl sidecar", version=VERSION)


def _err(kind, message):
    return {"kind": kind, "message": str(message)[:300]}


@app.get("/healthz")
async def healthz():
    return {
        "ok": True,
        "version": VERSION,
        "musicdl": MUSICDL_OK,
        "musicdl_error": MUSICDL_ERR,
        "sources": SOURCES,
        "limit": LIMIT,
        "cached": cache_size(),
    }


@app.get("/sources")
async def sources():
    """给「音源管理」页的清单：musicdl 注册了什么、哪些开着、哪些与原生重叠。"""
    return {
        "ok": True,
        "musicdl": MUSICDL_OK,
        "registered": len(REGISTERED),
        "enabled": SOURCES,
        "sources": core.catalog(REGISTERED, SOURCES),
    }


@app.post("/probe")
async def probe(payload: dict):
    """逐个源做一次**真实搜索**，回报耗时与条数。

    为什么要真搜而不是只看「注册了没」：真机实测里 56 个源有一大半在国内
    根本出不了结果（超时、被墙、站方改版）。「注册了」不代表「能用」，
    而这个页面的全部价值就是让用户看得出**哪些能用**。
    """
    wanted = core.normalize_sources((payload or {}).get("sources")) or SOURCES
    if not wanted:
        return {"ok": False, "error": "没有可检测的源（先启用几个）"}
    keyword = str((payload or {}).get("keyword") or "").strip() or "海阔天空"
    limit = core.clamp_limit((payload or {}).get("limit"), 3)
    budget = float(os.environ.get("QULV_MUSICDL_PROBE_BUDGET", "15"))

    async def one(short):
        t0 = time.time()
        try:
            items = await asyncio.wait_for(
                asyncio.to_thread(search_source_sync, short, keyword, limit),
                timeout=budget,
            )
            return {"id": short, "ms": int((time.time() - t0) * 1000),
                    "items": len(items), "ok": bool(items)}
        except Exception as exc:
            return {"id": short, "ms": int((time.time() - t0) * 1000),
                    "items": 0, "ok": False,
                    "error": {"kind": core.error_kind(exc), "message": str(exc)[:200]}}

    results = await asyncio.gather(*[one(s) for s in wanted])
    return {"ok": True, "keyword": keyword, "results": list(results)}


@app.post("/search")
async def search(payload: dict):
    keyword = str((payload or {}).get("keyword") or "").strip()
    if not keyword:
        return JSONResponse(status_code=400, content={"ok": False, "error": "keyword 不能为空"})
    sources = core.normalize_sources((payload or {}).get("sources")) or SOURCES
    limit = core.clamp_limit((payload or {}).get("limit"), LIMIT)

    items, errors = [], {}
    # 各源并发跑：单源预算 25s，串行跑三个源就要 75s，而搜索是用户点一下等着的操作。
    results = await asyncio.gather(
        *[search_source(s, keyword, limit) for s in sources], return_exceptions=True
    )
    for short, res in zip(sources, results):
        if isinstance(res, BaseException):
            errors[short] = _err(core.error_kind(res), res)
            continue
        items.extend(res)
    # 只回「能播」的：没有直链的条目在曲率那边会被判成不可播，
    # 提前滤掉能让搜索结果的长度如实反映「有多少首真的能听」。
    return {"ok": True, "keyword": keyword, "sources": sources,
            "items": core.playable_items(items), "errors": errors}


@app.post("/resolve")
async def resolve(payload: dict):
    wanted = (payload or {}).get("items")
    if not isinstance(wanted, list) or not wanted:
        return JSONResponse(status_code=400, content={"ok": False, "error": "items 不能为空"})

    results = await asyncio.gather(*[resolve_one(it or {}) for it in wanted],
                                    return_exceptions=True)
    items, errors = [], {}
    for req, res in zip(wanted, results):
        if isinstance(res, BaseException):
            errors[str((req or {}).get("id") or "")] = _err(core.error_kind(res), res)
            continue
        items.append({
            "id": res["id"],
            "source": res["source"],
            "url": res.get("download_url", ""),
            "headers": res.get("download_headers") or {},
            "ext": res.get("ext", ""),
            "file_size": res.get("file_size", 0),
            "duration_s": res.get("duration_s", 0),
            "cover_url": res.get("cover_url", ""),
            "lyric": res.get("lyric", ""),
        })
    return {"ok": True, "items": items, "errors": errors}
