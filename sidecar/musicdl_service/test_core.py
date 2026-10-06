"""`core.py` 的零依赖单测。

跑法（**不需要装 musicdl / fastapi**）：

    cd sidecar/musicdl_service && python3 -m unittest -v

为什么单测只覆盖 `core.py`：`app.py` 的价值在于接线（FastAPI 路由 + musicdl 调用），
真正的判据（源名映射、条目归一化、匹配哪一条、什么算可播、错误怎么归类）全在
`core.py` 里。把判据和接线分开，测的就是判据本身，而不是「我 mock 得像不像」。
"""

import unittest

import core


class FakeSong:
    """假装是 musicdl 的 SongInfo —— 只带真实实现里那几个字段。"""

    def __init__(self, **kw):
        self.identifier = kw.get("identifier", "")
        self.source = kw.get("source", "")
        self.song_name = kw.get("song_name", "")
        self.singers = kw.get("singers", "")
        self.album = kw.get("album", "")
        self.duration_s = kw.get("duration_s", 0)
        self.ext = kw.get("ext", "")
        self.file_size_bytes = kw.get("file_size_bytes", 0)
        self.cover_url = kw.get("cover_url", "")
        self.download_url = kw.get("download_url", "")
        self.lyric = kw.get("lyric", "")
        self.download_headers = kw.get("download_headers", {})


class TestSources(unittest.TestCase):
    def test_default_is_additive_only(self):
        # 默认只开曲率原生没有的那三个 —— 打开 sidecar 不该改变 wy/tx/kg/kw 的现有行为
        self.assertEqual(core.normalize_sources(None), ["mg", "bq", "bi"])
        self.assertEqual(core.normalize_sources(""), [])

    def test_accepts_short_codes_and_full_names(self):
        self.assertEqual(core.normalize_sources("mg,bq"), ["mg", "bq"])
        self.assertEqual(core.normalize_sources(["MG", " bq "]), ["mg", "bq"])
        # 用户照 musicdl 文档写全名也得认
        self.assertEqual(core.normalize_sources(["MiguMusicClient"]), ["mg"])
        self.assertEqual(core.normalize_sources(["kugou"]), ["kg"])

    def test_drops_unknown_and_dedupes(self):
        # 认不出的丢掉而不是报错：一个平台改名不该让整次搜索失败
        self.assertEqual(core.normalize_sources("mg,nope,mg,bq"), ["mg", "bq"])
        self.assertEqual(core.normalize_sources(["", None, "  "]), [])

    def test_clamp_limit(self):
        self.assertEqual(core.clamp_limit(None), core.DEFAULT_LIMIT)
        self.assertEqual(core.clamp_limit(""), core.DEFAULT_LIMIT)
        self.assertEqual(core.clamp_limit(0), core.DEFAULT_LIMIT)
        self.assertEqual(core.clamp_limit(-3), core.DEFAULT_LIMIT)
        self.assertEqual(core.clamp_limit("7"), 7)
        self.assertEqual(core.clamp_limit(999), core.MAX_LIMIT)
        self.assertEqual(core.clamp_limit("abc"), core.DEFAULT_LIMIT)


class TestSongID(unittest.TestCase):
    def test_round_trip(self):
        sid = core.song_id("mg", "abc123")
        self.assertEqual(sid, "mg:abc123")
        self.assertEqual(core.parse_song_id(sid), ("mg", "abc123"))

    def test_platform_id_may_contain_colon(self):
        # 平台内 id 本身可能带冒号 —— 只按**第一个**冒号切
        self.assertEqual(core.parse_song_id("bq:a:b:c"), ("bq", "a:b:c"))

    def test_unknown_prefix_is_not_a_platform(self):
        # 「xx:yy」里的 xx 不是已知短码 → 不当成平台前缀（否则会把别家的 id 切坏）
        self.assertEqual(core.parse_song_id("zz:123"), ("", "zz:123"))
        self.assertEqual(core.parse_song_id("123"), ("", "123"))
        self.assertEqual(core.parse_song_id(""), ("", ""))


class TestNormalizeSong(unittest.TestCase):
    def test_maps_all_fields(self):
        song = FakeSong(identifier="42", source="MiguMusicClient", song_name="海屿你",
                        singers="马也_Crabbit", album="专辑", duration_s=295, ext="flac",
                        file_size_bytes=1234, cover_url="http://c/1.jpg",
                        download_url="http://d/1.flac", lyric="[00:00]la",
                        download_headers={"Referer": "http://x/"})
        got = core.normalize_song(song, "mg", "海屿你")
        self.assertEqual(got["id"], "mg:42")
        self.assertEqual(got["source"], "mg")       # 用**我们的**短码，不是 musicdl 的全名
        self.assertEqual(got["platform_id"], "42")
        self.assertEqual(got["title"], "海屿你")
        self.assertEqual(got["artist"], "马也_Crabbit")
        self.assertEqual(got["duration_s"], 295)
        self.assertEqual(got["ext"], "flac")
        self.assertEqual(got["file_size"], 1234)
        self.assertEqual(got["download_url"], "http://d/1.flac")
        self.assertEqual(got["download_headers"], {"Referer": "http://x/"})
        self.assertEqual(got["keyword"], "海屿你")

    def test_missing_fields_are_tolerated(self):
        # musicdl 各源字段不一致，缺字段是常态：不能抛，要给安全的默认值
        got = core.normalize_song(FakeSong(identifier="7"), "kg")
        self.assertEqual(got["id"], "kg:7")
        self.assertEqual(got["title"], "")
        self.assertEqual(got["duration_s"], 0)
        self.assertEqual(got["ext"], "mp3")          # 缺 ext 时的默认
        self.assertEqual(got["download_headers"], {})

    def test_none_fields_do_not_crash(self):
        song = FakeSong()
        song.song_name = None
        song.singers = None
        song.duration_s = None
        got = core.normalize_song(song, "bq")
        self.assertEqual(got["title"], "")
        self.assertEqual(got["duration_s"], 0)


class TestTrial(unittest.TestCase):
    def test_marks_trial_variants(self):
        for t in ["海屿你 (试听)", "海屿你（试听）", "试听版", "[Trial] Song", "trial"]:
            self.assertTrue(core.is_trial(t), t)
        # 只认「试听」这两个字：单一个「试」不算，空标题不算
        for t in ["海屿你", "试", ""]:
            self.assertFalse(core.is_trial(t), t)


class TestPickMatch(unittest.TestCase):
    def setUp(self):
        self.items = [
            {"id": "mg:1", "platform_id": "1", "title": "甲", "artist": "A"},
            {"id": "mg:2", "platform_id": "2", "title": "乙", "artist": "B"},
            {"id": "mg:3", "platform_id": "3", "title": "甲", "artist": "C"},
        ]

    def test_exact_id_wins(self):
        got = core.pick_match(self.items, "mg:3", "甲", "A")
        # id 完全一致优先 —— 即使歌名歌手更像第一条
        self.assertEqual(got["id"], "mg:3")

    def test_bare_platform_id_also_matches(self):
        self.assertEqual(core.pick_match(self.items, "2", "乙", "B")["id"], "mg:2")

    def test_falls_back_to_title_and_artist(self):
        got = core.pick_match(self.items, "mg:gone", "甲", "C")
        self.assertEqual(got["id"], "mg:3")

    def test_falls_back_to_title_only(self):
        # 歌手字段各源写法差别大（「A」vs「A / B」），对不上时只认歌名
        got = core.pick_match(self.items, "mg:gone", "乙", "完全不认识的人")
        self.assertEqual(got["id"], "mg:2")

    def test_returns_none_when_nothing_matches(self):
        # 宁可解析失败，也不要拿错歌
        self.assertIsNone(core.pick_match(self.items, "mg:gone", "丙", "D"))
        self.assertIsNone(core.pick_match(self.items, "mg:gone", "", "D"))
        self.assertIsNone(core.pick_match([], "mg:1", "甲", "A"))

    def test_whitespace_and_case_insensitive(self):
        items = [{"id": "x", "title": "We Don't Talk Anymore", "artist": "Charlie"}]
        got = core.pick_match(items, "", "we don't  talk anymore", "charlie")
        self.assertIsNotNone(got)


class TestPlayable(unittest.TestCase):
    def test_needs_direct_link_and_no_trial(self):
        items = [
            {"id": "1", "title": "甲", "download_url": "http://d/1.mp3"},
            {"id": "2", "title": "乙", "download_url": ""},            # 没直链
            {"id": "3", "title": "丙 (试听)", "download_url": "http://d/3.mp3"},  # 试听片段
        ]
        got = core.playable_items(items)
        self.assertEqual([x["id"] for x in got], ["1"])

    def test_empty(self):
        self.assertEqual(core.playable_items([]), [])


class TestErrorKind(unittest.TestCase):
    def test_network_style_errors_are_unavailable(self):
        # 「少一个平台」—— 不该让整次搜索失败，也不该报成我们的 bug
        self.assertEqual(core.error_kind(TimeoutError("源 mg 超过 25s 预算")), "unavailable")
        self.assertEqual(core.error_kind(ConnectionError("connection reset")), "unavailable")
        self.assertEqual(core.error_kind(RuntimeError("musicdl 不可用：no module")), "unavailable")

    def test_programming_errors_are_invalid(self):
        # 参数/代码写错要显式暴露出来，不能被当成「平台抽风」吞掉
        self.assertEqual(core.error_kind(KeyError("id")), "invalid")
        self.assertEqual(core.error_kind(TypeError("bad")), "invalid")
        self.assertEqual(core.error_kind(ValueError("认不出这条曲目的平台")), "invalid")


class TestAppWiring(unittest.TestCase):
    """`app.py` 的接线判据（不能 import 它 —— 那需要 fastapi 与 musicdl）。

    这里读源码做静态断言。看着土，但它钉的是一个**真机上炸过的 bug**：
    `MusicClient` 在子模块 `musicdl.musicdl` 上，不在包根上 —— 写
    `import musicdl` + `musicdl.MusicClient(...)` 本地看不出问题（不 import 到
    那一行不会报错），只有真去搜索才炸，而且报错是
    `module 'musicdl' has no attribute 'MusicClient'`。
    """

    def setUp(self):
        import pathlib
        self.src = (pathlib.Path(__file__).parent / "app.py").read_text(encoding="utf-8")

    def test_uses_submodule_not_package_root(self):
        self.assertIn("from musicdl import musicdl as musicdl_lib", self.src,
                      "MusicClient 在子模块 musicdl.musicdl 上，必须从那里导入")
        self.assertNotIn("musicdl.MusicClient(", self.src,
                         "包根上没有 MusicClient —— 这行会炸在运行期")

    def test_three_contracts_present(self):
        for route in ('@app.get("/healthz")', '@app.post("/search")', '@app.post("/resolve")'):
            self.assertIn(route, self.src, "契约缺了：" + route)

    def test_source_budget_is_enforced(self):
        # 单源预算必须有 —— 一个源卡住不能拖垮整次搜索
        self.assertIn("SOURCE_BUDGET", self.src)
        self.assertIn("asyncio.wait_for", self.src)


class TestSourceCatalog(unittest.TestCase):
    """源的短码派生与清单（v2.1.89：musicdl 真机上有 **56 个源**）。

    以前短码是写死的 7 个 —— 写死的表一定会漏（上游还在加源），而漏掉的表现是
    「界面上看不到这个源」或「用户配了它却被丢掉」。所以改成派生 + 注册表。
    """

    def test_known_sources_keep_two_letter_codes(self):
        # 与曲率原生平台重叠的**必须**保持两字母短码（配置里、URL 里、缓存键里都在用）
        self.assertEqual(core.short_code("NeteaseMusicClient"), "wy")
        self.assertEqual(core.short_code("QQMusicClient"), "tx")
        self.assertEqual(core.short_code("KuGouMusicClient"), "kg")
        self.assertEqual(core.short_code("KuwoMusicClient"), "kw")
        self.assertEqual(core.short_code("MiguMusicClient"), "mg")
        self.assertEqual(core.short_code("QianqianMusicClient"), "bq")
        self.assertEqual(core.short_code("BilibiliMusicClient"), "bi")

    def test_other_sources_are_derived(self):
        # 派生：去掉 MusicClient 后缀再小写。56 个源不可能一个个写死。
        # ⚠️ musicdl 的命名约定是 `<品牌>MusicClient` —— 剥掉后缀得到**品牌名**
        # （`AppleMusicClient` → `apple`，不是 `applemusic`）。参考实现
        # （fnmusic-ext 的 `_source_short`）也是这么剥的，跟它保持一致。
        self.assertEqual(core.short_code("AppleMusicClient"), "apple")
        self.assertEqual(core.short_code("YouTubeMusicClient"), "youtube")
        self.assertEqual(core.short_code("FiveSongMusicClient"), "fivesong")
        self.assertEqual(core.short_code("FiveSingMusicClient"), "fivesing")
        self.assertEqual(core.short_code("XiagebaMusicClient"), "xiageba")
        self.assertEqual(core.short_code(""), "")

    def test_derived_codes_do_not_collide(self):
        # 名字只差一两个字母的源（FiveSong/FiveSing、Gequbao/Gequhai）派生后必须不同，
        # 否则两个源会共用一条配置与一个缓存键。
        names = ["FiveSongMusicClient", "FiveSingMusicClient",
                 "GequbaoMusicClient", "GequhaiMusicClient",
                 "XiaoBaiMusicClient", "XiagebaMusicClient",
                 "YinyuedaoMusicClient", "YinyuekuMusicClient"]
        codes = [core.short_code(n) for n in names]
        self.assertEqual(len(codes), len(set(codes)), codes)

    def test_label_falls_back_to_code(self):
        self.assertEqual(core.label_for("mg"), "咪咕音乐")
        self.assertEqual(core.label_for("wy"), "网易云音乐")
        # 认不出的不编中文名，用短码本身
        self.assertEqual(core.label_for("someobscure"), "someobscure")

    def test_catalog_marks_native_overlap(self):
        got = core.catalog(["NeteaseMusicClient", "MiguMusicClient", "AppleMusicClient"],
                           enabled=["mg"])
        by_id = {x["id"]: x for x in got}
        self.assertTrue(by_id["wy"]["native"], "wy 与原生重叠，界面要提示「会换掉原生实现」")
        self.assertTrue(by_id["kg"]["native"])
        self.assertFalse(by_id["mg"]["native"], "咪咕原生没有 → 纯增量")
        self.assertFalse(by_id["apple"]["native"])
        self.assertTrue(by_id["mg"]["enabled"])
        self.assertFalse(by_id["wy"]["enabled"])
        self.assertEqual(by_id["apple"]["client"], "AppleMusicClient")
        # 排序稳定（界面靠它保持顺序不乱跳）
        self.assertEqual([x["id"] for x in got], sorted(by_id))

    def test_catalog_without_registry_still_lists_aliases(self):
        # 没装 musicdl 时也要能列出「与原生重叠的那几个」，界面不至于空着
        got = core.catalog(None, enabled=[])
        ids = [x["id"] for x in got]
        for want in ("wy", "tx", "kg", "kw", "mg", "bq", "bi"):
            self.assertIn(want, ids)

    def test_register_known_extends_what_counts_as_a_source(self):
        # ⚠️ 这条是「已知集合」那个设计的核心：**注册表没告诉过我们的，就该被丢掉**。
        # 没有这道闸，`normalize_sources` 会把用户写错的 `nope` 当成一个源收下，
        # 而 `parse_song_id` 会把 `zz:123` 这种外来 id 切开。
        saved_known = set(core._KNOWN_SHORTS)
        saved_by_short = dict(core._CLIENT_BY_SHORT)
        try:
            # 注册表还没报过 spotify → 丢掉
            self.assertEqual(core.normalize_sources(["spotify"]), [])
            self.assertEqual(core.parse_song_id("spotify:1"), ("", "spotify:1"))

            core.register_known(["SpotifyMusicClient", "AppleMusicClient"])

            # 报过之后 → 收下，并且能反查出全名
            self.assertEqual(core.normalize_sources(["spotify", "apple"]),
                             ["spotify", "apple"])
            self.assertEqual(core.parse_song_id("spotify:1"), ("spotify", "1"))
            self.assertEqual(core.client_name("spotify"), "SpotifyMusicClient")
            # 认不出的仍然丢掉
            self.assertEqual(core.normalize_sources(["nope"]), [])
        finally:
            core._KNOWN_SHORTS.clear()
            core._KNOWN_SHORTS.update(saved_known)
            core._CLIENT_BY_SHORT.clear()
            core._CLIENT_BY_SHORT.update(saved_by_short)

    def test_client_name_for_alias_does_not_need_registry(self):
        # 别名表里的永远认得出（它不依赖注册表）
        self.assertEqual(core.client_name("mg"), "MiguMusicClient")
        self.assertEqual(core.client_name("MG"), "MiguMusicClient")
        # 派生短码在没注册过的情况下认不出 → 返回空串让调用方报错，**不瞎猜大小写**
        self.assertEqual(core.client_name("someobscure"), "")


class TestAppImports(unittest.TestCase):
    """用**假依赖**把 `app.py` 真的导一遍。

    这是唯一能在开发机上抓「模块级语句顺序」这类错的办法 —— 而它真出过一次
    （v2.1.89）：我把「读 musicdl 源注册表」放在了 `MUSICDL_OK = True` **之前**，
    于是导入时 `NameError` → uvicorn 加载不了 app → sidecar 进程直接退出。
    现象只是「sidecar 起不来」，日志里一长串 traceback，不看到最后一行看不出是
    顺序问题。而它在本机是「看不出来」的：本机没装 fastapi/musicdl，根本导不进来。

    静态断言（另一条用例）只能钉住「用没用子模块导入」，钉不住这个。
    """

    def _import_app(self, env=None, registered=None):
        import importlib.util
        import os
        import sys
        import types

        names = ("fastapi", "fastapi.responses", "musicdl", "musicdl.musicdl")
        saved = {k: sys.modules.get(k) for k in names}
        saved_env = {k: os.environ.get(k) for k in ("QULV_MUSICDL_SOURCES", "QULV_SIDECAR_PORT")}

        # 假 fastapi：只要有 FastAPI 类 + get/post 装饰器就够了（app.py 只用这些）
        fastapi = types.ModuleType("fastapi")

        class FastAPI(object):
            def __init__(self, *a, **k):
                pass

            def _deco(self, *a, **k):
                def deco(fn):
                    return fn
                return deco

            get = _deco
            post = _deco

        fastapi.FastAPI = FastAPI
        responses = types.ModuleType("fastapi.responses")

        class JSONResponse(object):
            def __init__(self, *a, **k):
                self.args = a

        responses.JSONResponse = JSONResponse

        # 假 musicdl：`MusicClientBuilder.REGISTERED_MODULES` 是 app.py 真正读的东西
        pkg = types.ModuleType("musicdl")
        inner = types.ModuleType("musicdl.musicdl")

        class MusicClientBuilder(object):
            REGISTERED_MODULES = list(registered or ["MiguMusicClient", "AppleMusicClient"])

        class FakeSong(object):
            def __init__(self):
                self.identifier = "1"
                self.source = "MiguMusicClient"
                self.song_name = "甲"
                self.singers = "乙"
                self.album = "丙"
                self.duration_s = 200
                self.ext = "mp3"
                self.file_size_bytes = 1000
                self.cover_url = ""
                self.download_url = "http://d/1.mp3"
                self.lyric = ""

        class MusicClient(object):
            def __init__(self, *a, **k):
                pass

            def search(self, keyword=None, **k):
                # 真实实现返回 `{客户端全名: [SongInfo]}`；这里照那个形状给一条
                return {MusicClientBuilder.REGISTERED_MODULES[0]: [FakeSong()]}

        inner.MusicClientBuilder = MusicClientBuilder
        inner.MusicClient = MusicClient
        pkg.musicdl = inner

        for name, mod in (("fastapi", fastapi), ("fastapi.responses", responses),
                          ("musicdl", pkg), ("musicdl.musicdl", inner)):
            sys.modules[name] = mod
        if env:
            os.environ.update(env)
        try:
            spec = importlib.util.spec_from_file_location(
                "app_under_test", os.path.join(os.path.dirname(__file__), "app.py"))
            mod = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(mod)   # ← 模块级顺序错了，这里就抛
            return mod
        finally:
            for k, v in saved.items():
                if v is None:
                    sys.modules.pop(k, None)
                else:
                    sys.modules[k] = v
            for k, v in saved_env.items():
                if v is None:
                    os.environ.pop(k, None)
                else:
                    os.environ[k] = v

    def test_imports_and_reads_registry(self):
        mod = self._import_app(env={"QULV_MUSICDL_SOURCES": "mg,apple"})
        # 注册表读到了 → 派生短码也能被认出来（这是「56 个源」那条路）
        self.assertIn("mg", mod.SOURCES)
        self.assertIn("apple", mod.SOURCES)
        # 三个契约路由都在
        for name in ("healthz", "sources", "probe", "search", "resolve"):
            self.assertTrue(callable(getattr(mod, name, None)), "缺少端点：" + name)
        self.assertTrue(mod.MUSICDL_OK, "假依赖下 musicdl 该被当成可用")

    def test_single_source_search_works_end_to_end(self):
        """真的调一次 `search_source_sync`。

        这条是补的：上面那条只保证「导得进来」，而 v2.1.89 还漏过一个只在**调用时**
        才炸的错 —— core 里把 `SOURCES` 改名成 `ALIASES` 之后，`app.py` 里
        `core.SOURCES[short]` 没跟着改，于是每次搜索都 `AttributeError`。
        导入测试抓不到它（那行不在模块级），必须真跑一次搜索。
        """
        mod = self._import_app(env={"QULV_MUSICDL_SOURCES": "mg"})
        items = mod.search_source_sync("mg", "甲", 3)
        self.assertEqual(len(items), 1)
        self.assertEqual(items[0]["id"], "mg:1")
        self.assertEqual(items[0]["source"], "mg")
        self.assertEqual(items[0]["title"], "甲")

    def test_single_source_search_rejects_unknown_source(self):
        # 认不出的源要**明确报错**（而不是 KeyError 崩掉或悄悄搜别家）
        mod = self._import_app(env={"QULV_MUSICDL_SOURCES": "mg"})
        with self.assertRaises(ValueError):
            mod.search_source_sync("nosuchsource", "甲", 3)

    def test_imports_when_musicdl_missing(self):
        # musicdl 没装（或 venv 还没建好）时**也要能导入**：sidecar 起来后如实报告
        # 自己不可用，而不是整个起不来 —— 曲率那边表现为「少几个平台」。
        import os
        import sys
        import types

        saved = {k: sys.modules.get(k) for k in ("fastapi", "fastapi.responses", "musicdl", "musicdl.musicdl")}
        fastapi = types.ModuleType("fastapi")

        class FastAPI(object):
            def __init__(self, *a, **k):
                pass

            def _deco(self, *a, **k):
                def deco(fn):
                    return fn
                return deco

            get = _deco
            post = _deco

        fastapi.FastAPI = FastAPI
        responses = types.ModuleType("fastapi.responses")
        responses.JSONResponse = object
        sys.modules["fastapi"] = fastapi
        sys.modules["fastapi.responses"] = responses
        sys.modules.pop("musicdl", None)
        sys.modules.pop("musicdl.musicdl", None)
        try:
            import importlib.util
            spec = importlib.util.spec_from_file_location(
                "app_under_test2", os.path.join(os.path.dirname(__file__), "app.py"))
            mod = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(mod)
            self.assertFalse(mod.MUSICDL_OK, "没装 musicdl 时该如实报不可用")
            self.assertEqual(mod.REGISTERED, [], "拿不到注册表时该是空列表，不是崩")
            # `SOURCES` 来自环境变量（Go 侧下发），不是注册表 —— 没下发就是空。
            # 真正要保证的是「模块导得进来」+「ALIASES 里那几个短码仍然认得出」。
            self.assertEqual(mod.SOURCES, [], "没下发源列表时该是空，而不是崩或乱猜")
            self.assertEqual(core.parse_song_id("mg:1"), ("mg", "1"),
                             "拿不到注册表时，ALIASES 里那几个短码仍要认得")
        finally:
            for k, v in saved.items():
                if v is None:
                    sys.modules.pop(k, None)
                else:
                    sys.modules[k] = v


if __name__ == "__main__":
    unittest.main()
