#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
tidy_engine.py — 音乐整理引擎

负责：
- 扫描音乐目录，收集音频文件入库（sqlite）。
- 逐文件整理：文件名解析 -> LLM 兜底识别 -> 曲库搜索 -> 写 LRC/封面 ->
  可选内嵌标签 -> 可选按歌手/专辑分类移动。
- 去重：按 SHA-1 指纹分组，供 UI 处理。
- 全程使用 Python 标准库，缺依赖时自动降级（只写 sidecar）。

Sidecar 策略：在音频文件同目录写「同名.lrc」和「cover.jpg」，
飞牛音乐等播放器会自动读取同目录歌词与封面，兼容性最好。
"""

import json
import os
import re
import shutil
import sqlite3
import subprocess
import sys
import threading
import time
import traceback
import urllib.parse
import urllib.request
from datetime import datetime

# 随包发布的第三方库（vendor/mutagen，用于内嵌标签与封面），优先于系统路径加载
_VENDOR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "vendor")
if os.path.isdir(_VENDOR) and _VENDOR not in sys.path:
    sys.path.insert(0, _VENDOR)

from scraper import (
    is_audio_file, parse_filename, apply_name_rules, parse_tidy_code, search_song, enrich_cover,
    fetch_lyric, download_cover, file_sha1, file_size, score_best, resolve_and_match,
    search_confident, walk_audio, is_audio_name, audio_ext, WALK_KIND_LABEL,
    lyric_garbled, MATCH_MIN, STYLE_MAP, EMOTION_MAP, SCENE_MAP, VERSION_MAP,
    llm_meta_sane,
    _artist_norm, _title_norm, _spath, path_tok, tok_path, list_subdirs, count_audio_tree,
    perm_probe, perm_brief, perm_fix_cmd, app_run_user,
)
from cue_splitter import find_cue_files, split_cue, parse_cue
import playlist as playlist_mod
from music_source import MusicSearch, SOURCE_ALIAS_DEFAULT, _http_get_json

# 数据库文件表字段
SCHEMA = """
CREATE TABLE IF NOT EXISTS files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  path TEXT UNIQUE,
  folder TEXT DEFAULT '',
  name TEXT DEFAULT '',
  size INTEGER DEFAULT 0,
  mtime REAL DEFAULT 0,
  sha1 TEXT DEFAULT '',
  artist TEXT DEFAULT '',
  title TEXT DEFAULT '',
  album TEXT DEFAULT '',
  style TEXT DEFAULT '',
  has_lyric INTEGER DEFAULT 0,
  has_cover INTEGER DEFAULT 0,
  code TEXT DEFAULT '',
  tidied INTEGER DEFAULT 0,
  tidied_at TEXT DEFAULT '',
  manual INTEGER DEFAULT 0,
  err TEXT DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_files_sha1 ON files(sha1);
CREATE INDEX IF NOT EXISTS idx_files_folder ON files(folder);
CREATE INDEX IF NOT EXISTS idx_files_code ON files(code);
CREATE TABLE IF NOT EXISTS task_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts TEXT,
  level TEXT,
  msg TEXT
);
CREATE TABLE IF NOT EXISTS playlists (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT UNIQUE,
  fav INTEGER DEFAULT 0,
  created TEXT DEFAULT '',
  archive_dir TEXT DEFAULT '',
  archive_mode TEXT DEFAULT '',
  auto_sync INTEGER DEFAULT 1
);
CREATE TABLE IF NOT EXISTS playlist_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  pid INTEGER NOT NULL,
  path TEXT DEFAULT '',
  artist TEXT DEFAULT '',
  title TEXT DEFAULT '',
  album TEXT DEFAULT '',
  duration INTEGER DEFAULT 0,
  song TEXT DEFAULT '',
  added TEXT DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pli_uniq ON playlist_items(pid, path);
CREATE INDEX IF NOT EXISTS idx_pli_pid ON playlist_items(pid);
CREATE TABLE IF NOT EXISTS shares (
  token TEXT PRIMARY KEY,
  pid INTEGER NOT NULL,
  name TEXT DEFAULT '',
  created TEXT DEFAULT '',
  active INTEGER DEFAULT 1,
  hits INTEGER DEFAULT 0,
  bytes INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS ai_usage (
  day TEXT NOT NULL,
  kind TEXT NOT NULL,
  calls INTEGER DEFAULT 0,
  fails INTEGER DEFAULT 0,
  miss INTEGER DEFAULT 0,
  ptok INTEGER DEFAULT 0,
  ctok INTEGER DEFAULT 0,
  ttok INTEGER DEFAULT 0,
  PRIMARY KEY (day, kind)
);
"""

DEFAULT_CONFIG = {
    "folders": [],            # 手动添加的目录（系统授权目录由 TRIM_DATA_ACCESSIBLE_PATHS 提供）
    "enable_scan": True,
    "enable_scrape": True,
    "enable_llm": False,
    "llm": {
        "base_url": "",
        "api_key": "",
        "model": "doubao-seed-1-6-250615",
        "timeout": 20,
        "image": {"base_url": "", "api_key": "", "model": ""},  # 文生图（AI 封面），缺省复用主 LLM 地址/Key
    },
    "enable_ai_cover": False,     # 曲库找不到封面时，用 AI 文生图兜底生成封面
    "overwrite_lyric": False,   # 已有歌词是否覆盖
    "overwrite_cover": False,   # 已有封面是否覆盖
    "embed_tags": True,         # 有 mutagen/ffmpeg 时尝试写内嵌标签
    "enable_classify": False,   # 是否按歌手/专辑移动文件
    "classify_keep_sidecar": True,
    "dedupe_mode": "keep_first",  # keep_first / keep_largest / manual
    "trash_dir": ".tidy_trash",
    "max_depth": 12,
    "enable_watch": True,       # 增量监听（新增/变化文件自动整理）
    "watch_interval": 30,       # 监听轮询间隔（秒）
    "exclude_dirs": [".tidy_trash", ".trash", "@eaDir", "#recycle", ".recycle", "$RECYCLE.BIN", "System Volume Information"],
    "name_rules": [],
    "music_source": {
        "enabled_sources": ["netease", "migu", "kugou", "kuwo", "qq"],  # 多选的内置音源
        "custom_apis": [],            # 自定义音源列表 [{name,url,token,enabled}]
        "download_dir": "",           # 下载保存目录，空则用首个音乐库目录下的 Downloads
        "cookies": {},                # 各平台 VIP Cookie {netease,kugou,kuwo,migu,qq}
        "transcode": True,            # 下载后自动转码为通用格式
        "alias": dict(SOURCE_ALIAS_DEFAULT),   # 平台显示别名（谐音），清空即回退真实名称
    },
    "playlist": {
        "archive_root": "",           # 歌单归档根目录，归档时落到 根目录/<歌单名>/
        "archive_mode": "copy",       # 默认方式：copy 另存一份 / move 移动
        "auto_sync": True,            # 新加入歌单的歌曲自动执行同样的归档动作
        "share_base": "",             # 分享链接基地址，留空则用访问时的地址
    },
}


def _now() -> str:
    return datetime.now().strftime("%Y-%m-%d %H:%M:%S")


def _safe_text(s) -> str:
    """把带 surrogate 的字符串（非 UTF-8 文件名在 Linux 上的常态）转成可写库/可显示文本。

    sqlite3 绑定含 surrogate 的 str 会直接抛 UnicodeEncodeError，旧实现里这会让
    整个 INSERT 静默失败（文件看起来“莫名入不了库”），连报错日志也一起丢。
    """
    try:
        s.encode("utf-8")
        return s
    except (UnicodeEncodeError, AttributeError):
        pass
    try:
        return str(s).encode("utf-8", "backslashreplace").decode("utf-8")
    except Exception:
        return repr(str(s))


def under_path(p, root) -> bool:
    """p 就是 root 或在其之下时为真。

    不用 SQL 的 folder LIKE root||'%' 判范围：旧写法会把选中「歌手A」连带
    命中「歌手AB」，目录名里的 _ 又被当成 LIKE 单字符通配，而且 SQLite 的 LIKE
    对 ASCII 大小写不敏感——“我只扫这一个文件夹”这种承诺不能靠通配符凑。以前只能
    整个授权目录一起跑，这三笔账看不出来；指定目录扫描上线后是一点就能撞到的。
    """
    p, root = str(p or ""), str(root or "")
    if not p or not root:
        return False
    return p == root or p.startswith(root.rstrip("/\\") + os.sep)


class TidyEngine:
    def __init__(self, data_dir: str, config: dict, llm=None, logger=None):
        self.data_dir = data_dir
        os.makedirs(data_dir, exist_ok=True)
        self.db_path = os.path.join(data_dir, "tidy.db")
        self.log_path = os.path.join(data_dir, "tidy.log")
        self._config = config or {}
        self.llm = llm
        self._logger = logger or (lambda level, msg: None)
        self._lock = threading.Lock()
        self._task = {
            "running": False,
            "type": "",
            "started_at": "",
            "finished_at": "",
            "total": 0,
            "done": 0,
            "ok": 0,
            "fail": 0,
            "current": "",
            "last_error": "",
            "state": "idle",   # running / paused / idle / done / stopped / error
            "seq": 0,          # 任务序号（每次 _start_task 递增），供前端区分“这一单”而不是“最近一单”
            "stage": "",       # 当前阶段（下载中 / 转码中 / 刮削入库…），供下载中心如实展示
            "skip": 0,
            # 下载中心：字节级进度（仅 download/dl_playlist 任务有效）
            "bytes_done": 0,
            "bytes_total": 0,
        }
        # 任务控制事件：_pause_event set=可继续，clear=暂停等待；_stop_event set=请求停止
        self._pause_event = threading.Event()
        self._pause_event.set()
        self._stop_event = threading.Event()
        self._last_audit = {}
        # 任务序号：前端下载中心靠它把面板条目与后端任务对齐（+ started_at 防重启后序号重值）
        self._task_seq = 0
        # 断点续跑：本轮任务作用在哪些目录上（随快照落盘，重启后才能原范围接着跑）
        self._task_folders = []
        self._persist_at = 0.0
        self._saved = None      # 上次落盘的任务快照；开跑新任务后置 None
        self._init_db()
        self._load_task_state()

    # ------------------------------------------------------------ task ctrl
    def pause_task(self):
        """暂停当前任务（在文件边界处生效）。"""
        self._pause_event.clear()
        with self._lock:
            if self._task["running"] and self._task["state"] == "running":
                self._task["state"] = "paused"
        self._save_task_state()
        return True

    def resume_task(self):
        """继续已暂停的任务。"""
        with self._lock:
            was_paused = self._task["running"] and self._task["state"] == "paused"
        self._pause_event.set()
        if was_paused:
            with self._lock:
                self._task["state"] = "running"
            self._save_task_state()
        return True

    def stop_task(self):
        """请求停止当前任务：中断等待，已完成文件保持进度，
        下次「开始整理」自动从未完成文件继续（断点续跑）。"""
        stopped = self._task.get("running")
        self._stop_event.set()
        self._pause_event.set()  # 若在暂停中，唤醒以便退出循环
        return bool(stopped)

    def _check_ctrl(self) -> bool:
        """文件边界处的控制点：暂停则阻塞等待；停止则返回 False 中断任务。"""
        while not self._pause_event.is_set() and not self._stop_event.is_set():
            time.sleep(0.4)
        return not self._stop_event.is_set()

    # ------------------------------------------------------------------ db
    def _init_db(self):
        with self._db() as c:
            # 旧库迁移：先补 code / manual 列，再建引用该列的索引
            cols = [r["name"] for r in c.execute("PRAGMA table_info(files)").fetchall()]
            if cols and "code" not in cols:
                c.execute("ALTER TABLE files ADD COLUMN code TEXT DEFAULT ''")
            if cols and "manual" not in cols:
                c.execute("ALTER TABLE files ADD COLUMN manual INTEGER DEFAULT 0")
            c.executescript(SCHEMA)

    def _db(self):
        conn = sqlite3.connect(self.db_path, timeout=15)
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA journal_mode=WAL")
        return conn

    def log(self, level: str, msg: str):
        ts = _now()
        msg = _safe_text(msg)
        try:
            with open(self.log_path, "a", encoding="utf-8") as f:
                f.write(f"[{ts}] [{level}] {msg}\n")
            with self._db() as c:
                c.execute("INSERT INTO task_log(ts,level,msg) VALUES(?,?,?)", (ts, level, msg))
                c.execute("DELETE FROM task_log WHERE id NOT IN (SELECT id FROM task_log ORDER BY id DESC LIMIT 1000)")
        except Exception:
            pass
        self._logger(level, msg)

    # ------------------------------------------------------------ ai usage
    # 用量按 (天, 用途) 累计一行，不是每次调用一行：一个几千首的库跑完整理能打出
    # 几千次调用，明细表会把存储撞大，而用户只要知道“今天用了多少 token、跑了几次、花在哪”。
    AI_KIND_LABEL = {
        "recognize": "文件名识别", "name_rule": "命名规则分析", "dedupe": "去重决策",
        "lyric": "AI 生成歌词", "analyze": "AI 分析歌词", "cover_prompt": "封面画面描述",
        "image": "AI 生图封面", "test": "连接测试", "chat": "其它对话", "other": "其它",
    }

    def make_llm(self, enabled=None):
        """造一个带用量回调的 LLMClient。

        三处创建点（启动 / 保存配置 / 测试连接）都必须走这里，否则新配置的 client
        不记账，统计会静默少一截。本文件顶部不导入 ai_llm（会循环依赖），局部导入。
        """
        from ai_llm import LLMClient
        return LLMClient(self._config.get("llm") or {}, enabled=enabled,
                         on_usage=self.record_ai_usage)

    def record_ai_usage(self, kind, ptok=0, ctok=0, ttok=0, ok=True, miss=0):
        """记一次 AI 调用的用量（LLMClient.on_usage 的落点）。"""
        day = datetime.now().strftime("%Y-%m-%d")
        try:
            with self._db() as c:
                c.execute(
                    "INSERT INTO ai_usage(day,kind,calls,fails,miss,ptok,ctok,ttok) "
                    "VALUES(?,?,?,?,?,?,?,?) "
                    "ON CONFLICT(day,kind) DO UPDATE SET "
                    "calls=calls+excluded.calls, fails=fails+excluded.fails, "
                    "miss=miss+excluded.miss, ptok=ptok+excluded.ptok, "
                    "ctok=ctok+excluded.ctok, ttok=ttok+excluded.ttok",
                    (day, str(kind or "other")[:24], 1, 0 if ok else 1, 1 if miss else 0,
                     max(0, int(ptok or 0)), max(0, int(ctok or 0)), max(0, int(ttok or 0))))
        except Exception:
            pass   # 统计写坏了不能把 AI 功能本身带坏
        return True

    def ai_usage_stats(self, days=7) -> dict:
        """设置页 AI 面板的数据：今日 / 总计 的 token 与次数 + 用途明细 + 近几天趋势。"""
        today = datetime.now().strftime("%Y-%m-%d")
        agg = ("COALESCE(SUM(calls),0) calls, COALESCE(SUM(fails),0) fails, "
               "COALESCE(SUM(miss),0) miss, COALESCE(SUM(ptok),0) ptok, "
               "COALESCE(SUM(ctok),0) ctok, COALESCE(SUM(ttok),0) ttok")
        blank = {"calls": 0, "fails": 0, "miss": 0, "ptok": 0, "ctok": 0, "ttok": 0}
        out = {"ok": True, "today": dict(blank), "total": dict(blank),
               "by_kind": [], "by_day": [], "kinds": self.AI_KIND_LABEL,
               "days": max(1, min(60, int(days or 7))), "first_day": "", "today_str": today}
        try:
            with self._db() as c:
                out["today"] = dict(c.execute(
                    f"SELECT {agg} FROM ai_usage WHERE day=?", (today,)).fetchone() or blank)
                out["total"] = dict(c.execute(f"SELECT {agg} FROM ai_usage").fetchone() or blank)
                out["first_day"] = (c.execute("SELECT MIN(day) d FROM ai_usage").fetchone()["d"]) or ""
                for row in c.execute(
                        "SELECT kind, " + agg +
                        " FROM ai_usage GROUP BY kind ORDER BY ttok DESC, calls DESC"):
                    d = dict(row)
                    d["label"] = self.AI_KIND_LABEL.get(d["kind"], d["kind"])
                    out["by_kind"].append(d)
                for row in c.execute(
                        "SELECT day, SUM(calls) calls, SUM(ttok) ttok, SUM(fails) fails "
                        "FROM ai_usage GROUP BY day ORDER BY day DESC LIMIT ?", (out["days"],)):
                    out["by_day"].append(dict(row))
        except Exception as e:
            out["ok"] = False
            out["error"] = str(e)
        return out

    # --------------------------------------------------------------- config
    def merge_config(self, new_cfg: dict):
        """合并用户配置（深合并，保留默认值）。"""
        merged = _deep_merge(DEFAULT_CONFIG, self._config)
        merged = _deep_merge(merged, new_cfg)
        self._config = merged
        return merged

    def get_config(self, secret_masked=True):
        cfg = json.loads(json.dumps(self._config))
        if secret_masked:
            if cfg.get("llm", {}).get("api_key"):
                k = cfg["llm"]["api_key"]
                cfg["llm"]["api_key"] = (k[:3] + "****" + k[-2:]) if len(k) > 5 else "****"
            ik = (cfg.get("llm", {}).get("image") or {}).get("api_key")
            if ik:
                cfg["llm"]["image"]["api_key"] = (ik[:3] + "****" + ik[-2:]) if len(ik) > 5 else "****"
            for a in (cfg.get("music_source", {}) or {}).get("custom_apis") or []:
                t = a.get("token") or ""
                if t:
                    a["token"] = (t[:3] + "****" + t[-2:]) if len(t) > 5 else "****"
            ck = (cfg.get("music_source", {}) or {}).get("cookies") or {}
            for k, v in list(ck.items()):
                if v:
                    ck[k] = (v[:3] + "****" + v[-2:]) if len(v) > 5 else "****"
        return cfg

    def system_folders(self) -> list:
        """系统授权目录（TRIM_DATA_ACCESSIBLE_PATHS）已由外部注入到环境，
        这里从配置的 'system_folders' 读取（启动时设置）。"""
        return [f for f in (self._config.get("system_folders") or []) if f]

    def all_folders(self) -> list:
        """合并系统授权目录 + 手动目录，去重保序。"""
        seen, out = set(), []
        for f in list(self.system_folders()) + list(self._config.get("folders") or []):
            f = (f or "").strip().rstrip("/")
            if f and f not in seen:
                seen.add(f)
                out.append(f)
        return out

    def enabled_folders(self) -> list:
        """当前启用的整理目录（默认全部启用）。"""
        return self.all_folders()

    def manual_folders(self) -> list:
        """只返回手动添加的目录。

        「目录」页以前拿合并后的 all_folders 渲染「手动添加目录」那一栏，
        结果系统授权目录也被列了一遍，还带着「移除」按钮（点了其实什么也不动）。
        """
        return [f for f in (self._config.get("folders") or []) if f]

    # ------------------------------------------------ 指定目录扫描（扫描范围）
    # 单次目录浏览最多过多少条目（目录+文件）。一屏上百个子目录，不设预算的话
    # 点开「选择扫描目录」就等于把整块盘扫一遍，NAS 会直接卡住。
    BROWSE_BUDGET = 30000

    def _rooted(self, path):
        """path 落在哪个授权目录下（不在任何授权目录内则返回 None）。按 realpath 前缀判。"""
        try:
            rp = os.path.realpath(path)
        except OSError:
            return None
        for r in self.all_folders():
            try:
                rr = os.path.realpath(r)
            except OSError:
                rr = r
            if rp == rr or rp.startswith(rr + os.sep):
                return r
        return None

    def clamp_folders(self, folders):
        """把外部传来的目录夹回授权范围内，返回 (可用列表, 被拒项的展示文本)。

        为什么必须校验：/api/scan、/api/tidy 一直就收了 body.folders 却不问出处，
        构造一个 /etc 就能让任务去读授权目录外的盘。媒体接口早有 _media_ok 这道
        门，任务入口不能是漏的。

        同时只保留最外层目录：选了「音乐」又选「音乐/歌手A」时，walk_audio 会把
        同一批文件枚举两遍、tidy 会跑两遍，日志里的音频数也就对不上磁盘了。
        """
        ok, bad, seen = [], [], set()
        for f in folders or []:
            f = str(f or "").strip().rstrip("/\\")
            if not f:
                continue
            if self._rooted(f) is None:
                # 先判范围再判存在：反过来会把「授权目录里已删的子目录」误报成越权
                bad.append(_spath(f) + "（不在授权目录内）")
                continue
            try:
                rp, isdir = os.path.realpath(f), os.path.isdir(f)
            except OSError:
                rp, isdir = f, False
            if not isdir:
                bad.append(_spath(f) + "（不存在或不可进入）")
                continue
            if rp in seen:
                continue      # 同一目录经软链重复出现，只留一份，免得同一路径扫两遍
            seen.add(rp)
            ok.append((rp, f))
        outer = []
        for rp, f in sorted(ok, key=lambda x: len(x[0])):
            if any(rp != q and rp.startswith(q + os.sep) for q, _ in outer):
                continue      # 已被某个外层范围覆盖
            outer.append((rp, f))
        return [f for _, f in outer], bad

    def browse_dirs(self, token="", limit=300, counts=True):
        """逐级列出授权目录下的子目录，供「选择扫描目录」弹窗下钻。返回 (载荷, HTTP 码)。

        token 为空 = 列全部授权目录；否则该目录必须落在授权范围内（越界 403）。
        每个节点带子树音频数，超预算时 counted/partial 会标出来，前端显示「≈」。
        父节点自子节点行已经报过数，这里不重算（否则整子树会被重复遍历）。
        """
        ex = self._config.get("exclude_dirs") or []
        md = int(self._config.get("max_depth", 12) or 12)
        budget = [self.BROWSE_BUDGET]

        def _node(p, name, do_count):
            if do_count and budget[0] > 0:
                n, comp = count_audio_tree(p, exclude=ex, max_depth=md, budget=budget)
                counted = True
            else:
                n, comp, counted = 0, False, bool(do_count)
            subs, strunc = list_subdirs(p, exclude=ex, limit=limit)
            return {"token": path_tok(p), "name": _spath(name), "path": _spath(p),
                    "audio": n, "counted": counted, "partial": not comp,
                    "nsub": len(subs), "nsub_more": strunc,
                    "has_kids": bool(subs) or strunc}, subs

        if not token:
            items = [_node(r, r, counts)[0] for r in self.enabled_folders()]
            for nd in items:
                nd["authorized"] = True
            return {"ok": True, "token": "", "parent": None, "items": items,
                    "counts": bool(counts), "budget_left": budget[0]}, 200
        cur = tok_path(token)
        if not cur:
            return {"ok": False, "error": "目录标识无效，请重新打开选择窗口"}, 400
        if self._rooted(cur) is None:
            return {"ok": False, "error": f"越权访问：{_spath(cur)} 不在授权目录内"}, 403
        if not os.path.isdir(cur):
            return {"ok": False, "error": "目录已不存在或被删除，请重新打开选择窗口"}, 410
        parent, subs = _node(cur, os.path.basename(os.path.normpath(cur)) or cur, False)
        kids = [_node(os.path.join(cur, d), d, counts)[0] for d in subs]
        return {"ok": True, "token": token, "parent": parent, "items": kids,
                "counts": bool(counts), "budget_left": budget[0]}, 200

    def is_scoped(self, folders) -> bool:
        """这次拿到的目录是不是只是授权目录的一部分（用户指定了范围）。

        对账必须跟着这个判断走：只扫了 歌手A 却拿全库入库数去比，会算出
        “磁盘 2 · 入库 1944 · 差 -1942”，还会把范围外每条记录都当成“磁盘上已消失”。

        空入参按「未限定」处理：调用方一般会先 `folders or enabled_folders()` 再进来，
        但助手函数不该把这个约定当前提，否则漏一层就直接把全库对账判成范围内对账。
        """
        fs = list(folders or [])
        if not fs:
            return False
        full = self.enabled_folders()
        return not (len(fs) == len(full) and all(f in full for f in fs))

    def in_scope(self, rows, folders):
        """从库记录里筛出落在本次范围内的行（行需带 folder / path）。"""
        return [r for r in rows
                if any(under_path(r["folder"] or r["path"], root) for root in folders)]

    def scope_note(self, folders) -> str:
        """范围不是全部授权目录时，把这行补到开始日志里。

        不然日志只写「扫描 1 个目录」，用户事后无从判断当时是不是只跑了指定目录，
        也解释不了为什么库里数量比磁盘少一大截。
        """
        fs = list(folders or [])
        if not self.is_scoped(fs):
            return ""
        body = "、".join(_spath(f) for f in fs[:8]) + (" …" if len(fs) > 8 else "")
        return f" · 指定目录扫描（非全部授权目录，共 {len(fs)} 个）：{body}"

    # ----------------------------------------------------------------- task
    def task_status(self) -> dict:
        with self._lock:
            out = dict(self._task)
        r = self.resume_info()
        if r:
            out["resume"] = r
        return out

    # 任务快照落盘：进度只活在内存里，关应用 / 升级 / 崩一次就全没了，
    # 用户看到的是一句“暂无任务”，几千首里到底跑到哪了无从得知。
    TASK_STATE_FILE = "task_state.json"
    TASK_PERSIST_SEC = 3.0
    # 只有这三种任务值得恢复；下载一类的短时任务重启后重新点一下就行
    RESUMABLE = ("tidy", "scan", "probe")

    def _task_path(self):
        return os.path.join(self.data_dir or ".", self.TASK_STATE_FILE)

    def _save_task_state(self):
        """把当下任务快照写进数据目录（自己取锁，不得在持锁区内调用）。"""
        with self._lock:
            t = self._task
            snap = {
                "type": t.get("type") or "", "state": t.get("state") or "",
                "started_at": t.get("started_at") or "", "finished_at": t.get("finished_at") or "",
                "total": t.get("total", 0), "done": t.get("done", 0), "ok": t.get("ok", 0),
                "fail": t.get("fail", 0), "skip": t.get("skip", 0),
                "current": t.get("current") or "", "last_error": t.get("last_error") or "",
                "stage": t.get("stage") or "", "probe": t.get("probe"),
                "folders": [_spath(f) for f in (self._task_folders or [])],
                "persisted_at": _now(),
            }
        try:
            tmp = self._task_path() + ".tmp"
            with open(tmp, "w", encoding="utf-8") as f:
                json.dump(snap, f, ensure_ascii=False)
            os.replace(tmp, self._task_path())   # 原子改写：别在写一半时被断开留下碎文件
        except Exception:
            pass   # 落盘只是为下次重启服务，失败不能把任务本身弄坏
        return snap

    def _load_task_state(self):
        """启动时读回上次快照：只恢复“看到的进度”，绝不自动起线程重跑。

        进程刚起来时目录授权、配置都还没确认，悄悄改用户的文件是不负责任的；
        所以把中断的那一单做成一个可一键继续的 resume 建议，要不要跑由用户定。
        """
        try:
            with open(self._task_path(), encoding="utf-8") as f:
                raw = json.load(f)
        except Exception:
            return None
        if not isinstance(raw, dict) or not raw.get("type"):
            return None
        try:
            ints = {k: int(raw.get(k) or 0) for k in ("total", "done", "ok", "fail", "skip")}
        except (TypeError, ValueError):
            return None
        state = str(raw.get("state") or "")
        with self._lock:
            self._task.update({
                "type": str(raw.get("type") or ""), "state": "stopped" if state in
                ("running", "paused") else (state or "idle"),
                "started_at": str(raw.get("started_at") or ""),
                "finished_at": str(raw.get("finished_at") or ""),
                "current": str(raw.get("current") or ""),
                "last_error": str(raw.get("last_error") or ""),
                # stage 不恢复：一个“下载中”挂在没在跑的任务上是假忙
                "stage": "", "probe": raw.get("probe") if isinstance(raw.get("probe"), dict) else None,
                "running": False, "restored": True,
            })
            self._task.update(ints)
            self._task_folders = [str(x) for x in (raw.get("folders") or []) if x]
        if state in ("running", "paused") and raw.get("type") in self.RESUMABLE:
            self._saved = dict(raw)
            self.log("warn", f"上次任务「{raw.get('type')}」在 {raw.get('persisted_at') or raw.get('started_at') or '?'}"
                             f" 被中断（已完成 {ints['done']}/{ints['total']}），已恢复进度；"
                             "概览页点「继续上次整理」可原范围重跑（已完成的不会重做）")
        return raw

    def resume_info(self):
        """把中断的那一单翻成前端的「继续上次整理」按钮信息；没得继续就返回 None。

        候选目录必须重新过一遍当下授权：重启期间用户完全可能改过授权列表，
        拿着旧快照直接跑等于把范围偷偷放大到全盘 —— 宁可少跑几个并要求重选。
        """
        raw = self._saved
        if not raw:
            return None
        saved = [str(x) for x in (raw.get("folders") or []) if x]
        try:
            allowed = self.all_folders()
        except Exception:
            return None
        keep = [f for f in saved if any(under_path(f, r) or under_path(r, f) for r in allowed)]
        if not keep:
            return None
        tpe = str(raw.get("type") or "")
        return {
            "type": tpe, "label": {"tidy": "整理", "scan": "扫描", "probe": "仅全量扫描"}.get(tpe, tpe),
            "at": str(raw.get("persisted_at") or raw.get("started_at") or ""),
            "done": int(raw.get("done") or 0), "total": int(raw.get("total") or 0),
            "folders": keep, "candidate": len(saved), "dropped": max(0, len(saved) - len(keep)),
            # 前端一律用 token 同一条通道回传（与「指定目录扫描」完全一致），
            # 避免路径里的非 UTF-8 字节在 URL/JSON 往返中被洗掉后“选 A 跑 B”
            "tokens": [path_tok(f) for f in keep],
        }

    def _start_task(self, ttype: str, folders=None):
        self._task_seq += 1
        self._stop_event.clear()
        self._pause_event.set()
        with self._lock:
            self._task.update({
                "running": True, "type": ttype, "state": "running",
                "seq": self._task_seq,
                "started_at": _now(), "finished_at": "",
                "total": 0, "done": 0, "ok": 0, "fail": 0, "skip": 0,
                "current": "", "last_error": "", "stage": "",
                "bytes_done": 0, "bytes_total": 0, "probe": None,
                "restored": False,
            })
            self._task_folders = [_spath(f) for f in (folders or [])]
            self._persist_at = time.time()
        self._saved = None          # 新任务一跑，旧的中断建议就作废
        self._save_task_state()

    def _tick(self, done=None, ok=None, fail=None, skip=None, current=None, total=None,
              bytes_done=None, bytes_total=None, stage=None, persist=None):
        need = False
        with self._lock:
            t = self._task
            if total is not None:
                t["total"] = total
            if done is not None:
                t["done"] = done
            if ok is not None:
                t["ok"] = ok
            if fail is not None:
                t["fail"] = fail
            if skip is not None:
                t["skip"] = skip
            if current is not None:
                t["current"] = current
            if bytes_done is not None:
                t["bytes_done"] = bytes_done
            if bytes_total is not None:
                t["bytes_total"] = bytes_total
            if stage is not None:
                t["stage"] = stage
            # 逐字段更新完再决定要不要落盘：每首一次写文件会把 SSD 小 IO 打满，
            # 太稀又会多丢一段进度；默认 3s 一次，终态（_finish_task）强制写。
            now = time.time()
            if persist or (persist is None and now - self._persist_at >= self.TASK_PERSIST_SEC):
                self._persist_at = now
                need = True
        if need:
            self._save_task_state()

    def _finish_task(self, err=""):
        """任务收尾：把终态写进 state，前端才能区分「跑完 / 被停止 / 异常」。

        旧实现一律置 idle，前端只能拿 done<total 猜「已停止」，而任何一条
        失败/跳过分支不涨 done，正常跑完的任务也会被显示成「已停止」。
        """
        with self._lock:
            self._task["running"] = False
            self._task["finished_at"] = _now()
            self._task["last_error"] = err
            self._task["stage"] = ""
            self._task["state"] = (
                "error" if err else ("stopped" if self._stop_event.is_set() else "done"))
        self._save_task_state()   # 终态必须落盘：重启后还能看到“上次跑到哪了”

    # ---------------------------------------------------------------- scan
    def _sidecar_state(self, path: str):
        """附属文件状态（只 stat，不读内容）：歌词 / 逐曲同名封面 / 分类代码。"""
        stem = os.path.splitext(path)[0]
        has_lrc = 1 if os.path.exists(stem + ".lrc") else 0
        # 封面状态只认逐曲同名图；目录级 cover.jpg 常与具体歌曲不符
        has_cov = 1 if any(os.path.exists(stem + s) for s in (".jpg", ".jpeg", ".png")) else 0
        code = parse_tidy_code(os.path.basename(path)).get("code", "")
        return has_lrc, has_cov, code

    # 枚举进度上报间隔（秒）：大曲库几万个目录，每个都 tick 会把任务锁与前端轮询压垮
    ENUM_TICK_SEC = 0.35

    def _enum_progress(self, n_roots):
        """给 walk_audio 的 on_dir：把「已遍历多少目录 / 多少文件 / 多少音频」实时报上去。

        total 故意留 0：枚举前先数一遍总量，等于把盘再遍历一次，比扫本身还慢。
        前端拿到 total=0 就渲染不定长动画条 + 递增计数，而不是对着一个永远 0% 的
        条猜还要等多久。每个根目录的最后一个目录必须报一次（节流不能吞掉阶段拐点）。
        """
        st = {"last": 0.0}

        def _cb(info):
            now = time.time()
            final = info.get("root") == info.get("roots")
            if not final and now - st["last"] < self.ENUM_TICK_SEC:
                return
            st["last"] = now
            d = info.get("dir") or ""
            stage = (f"枚举中 · 已遍历 {info.get('dirs', 0)} 个子目录"
                     f" · 文件 {info.get('files', 0)} · 音频 {info.get('audio', 0)}")
            if n_roots > 1:
                stage += f" · 目录 {info.get('root', 0)}/{n_roots}"
            self._tick(total=0, done=info.get("dirs") or 0, stage=stage,
                       current=os.path.basename(d.rstrip(os.sep)) if d else "")
        return _cb

    def _enumerate_audio(self, folders, progress=False):
        """阶段一：快速枚举（不读文件内容、不算 hash），实现见 scraper.walk_audio。

        返回 (音频路径列表, audit)。audit["complete"] 为 False 时调用方必须
        跳过「清理磁盘已删除记录」：旧实现枚举到一半就拿来对比，把没扫到的
        那部分入库记录全删了，表现为「几千首只扫出一半」且越扫越少。

        progress=True 时把逐目录进度写进任务状态（「扫描音乐库 / 仅全量扫描」用）；
        默认的 False 是给「扫描体检」这类不占任务状态的现场枚举用的，
        不能让它把正在跑的整理任务进度盖掉。
        """
        paths, audit = walk_audio(
            folders, exclude=self._config.get("exclude_dirs") or [],
            max_depth=int(self._config.get("max_depth", 12) or 12),
            should_stop=lambda: self._stop_event.is_set(),
            on_dir=(self._enum_progress(len(folders or [])) if progress else None))
        self._last_audit = audit
        self._record_denied(audit, folders)
        max_depth = int(self._config.get("max_depth", 12) or 12)
        for info in audit["roots"]:
            if not info["exists"]:
                self.log("warn", f"目录不存在或不可进入，跳过: {info['root']}")
                continue
            msg = f"枚举 {info['root']}：文件 {info['files']}，音频 {info['audio']}"
            if info["errs"]:
                msg += f"，无法读取子目录 {info['errs']} 个"
            if info["depth_cut"]:
                msg += f"，超深度 {max_depth} 被截断目录 {info['depth_cut']} 个"
            if info["link_cut"]:
                msg += f"，软链重复/环引用剪枝 {info['link_cut']} 个"
            self.log("info" if not info["errs"] and not info["depth_cut"] else "warn", msg)
        for e in audit["errors"]:
            self.log("warn", f"无法读取目录（权限？请给应用用户授权）: {e}")
        if audit["inaccessible"]:
            self.log("warn",
                     f"{audit['inaccessible']} 个扩展名为音频的文件无法访问（权限不足 / 软链失效 / 文件名编码异常），样例: "
                     + " | ".join(os.path.basename(p) for p in audit["inaccessible_samples"][:5]))
        self._log_file_makeup(audit)
        self._log_audit(audit)
        return paths, audit

    def _log_file_makeup(self, audit):
        """把「遍历了几千个文件、其中音频只有几百」这笔账算清楚：差掉的每个文件归哪一类。

        旧版只报一句“遍历 7651 个文件，其中音频 1943 个”，用户自然会问剩下 5708 个
        是什么、里面有没有歌；而扩展名统计又常被封面/歌词占满前几名，把加密容器挤到看不见。
        """
        unk = audit.get("ext_skipped_total") or sum((audit.get("ext_skipped") or {}).values())
        enc = audit.get("encrypted_total") or sum((audit.get("encrypted") or {}).values())
        parts = [f"音频 {audit.get('audio_total', 0)}",
                 f"封面/歌词/列表等伴生文件 {audit.get('skip_known', 0)}",
                 f"扩展名未识别 {unk}",
                 f"无扩展名 {audit.get('no_ext', 0)}"]
        if enc:
            parts.insert(2, f"加密音乐容器 {enc}")
        if audit.get("inaccessible"):
            parts.append(f"是音频却读不到 {audit['inaccessible']}")

        def _more(total, items):
            """清单被截断时说明还剩多少，免得用户以为只有列出的这些。"""
            shown = sum(v for _, v in items)
            return f"，另有 {total - shown} 个未列出" if total > shown else ""
        # audit 里的清单只留前 15 名（scraper 按数量倒序截断），日志就必须照 15 名打全，
        # 否则「前 15 名」只印出 10 个、而「另有 N 个」又是按 15 个的和算的 —— 又是一个说不圆的账。
        enc_items = list((audit.get("encrypted") or {}).items())[:15]
        unk_items = list((audit.get("ext_skipped") or {}).items())[:15]
        self.log("info", f"文件构成：{audit.get('files_total', 0)} 个文件 = " + " + ".join(parts))
        if enc:
            self.log("warn",
                     f"其中 {enc} 个是加密音乐容器（按数量倒序，前 {len(enc_items)} 名）："
                     + " ".join(f"{k}×{v}" for k, v in enc_items)
                     + _more(enc, enc_items)
                     + "；这类文件本应用不直接入库，到「格式转换」页解密后再扫描即可")
        if unk:
            self.log("info", f"未视为音频的扩展名统计（前 {len(unk_items)} 名，按数量倒序）："
                     + " ".join(f"{k}×{v}" for k, v in unk_items)
                     + _more(unk, unk_items))

    # 逐类最多列几条：全部输出会刷屏，且 task_log 只留存最近 1000 行
    AUDIT_LOG_LINES = 40

    def _log_audit(self, audit):
        """把枚举对账结果按「原因分类」逐条打进日志：哪个目录没扫出来、为什么、怎么办。

        旧版只列前 8 个读不了的目录且不给原因，用户遇到“几千首只扫出一半”时
        根本看不出是哪几个文件夹、为什么被漏掉。超限的条数会在日志里说明，
        完整清单在音乐库「扫描体检」弹窗里看。
        """
        issues = audit.get("issues") or []
        by = {}
        for it in issues:
            by.setdefault(it.get("kind") or "other", []).append(it)
        for kind in sorted(by, key=lambda k: -len(by[k])):
            rows = by[kind]
            label = WALK_KIND_LABEL.get(kind, kind)
            lv = "info" if kind == "link" else "warn"
            self.log(lv, f"【{label}】共 {len(rows)} 个：{rows[0].get('hint') or ''}")
            for it in rows[:self.AUDIT_LOG_LINES]:
                extra = f"（errno={it['code']} {it['reason']}）" if it.get("code") else (
                    f"（{it['reason']}）" if it.get("reason") else "")
                if kind == "denied":
                    # errno=13 只说“不让进”，不说“缺哪一位”。逐条补一次取证（纯 stat/access），
                    # 用户才知道是该给这个子目录加权限，还是外层本来就没授权。
                    extra += " " + perm_brief(perm_probe(it.get("dir") or ""))
                self.log(lv, f"  {label}: {it.get('dir')}{extra}")
            if kind == "denied" and rows:
                # 不强制修权限（本应用是非特权用户，chmod 只会 EPERM），只把三条可行路径摆出来。
                # 别写「授权目录里逐个添加子目录」：飞牛主目录授权后子目录根本选不动，那条路不通。
                self.log("warn", "  怎么修（三选一，都不影响文件内容）："
                             "① 飞牛里右键该目录 →「详细信息」→「权限」→「高级」→「启用继承父级权限」"
                             "（推荐：外层已授权，继承下来就好；主目录授权后无法再单独选子目录，别去添加列表里绕）；"
                             f"② 文件管理器里给该目录加账号 {app_run_user()} 的「读取+遍历」并应用到子项目；"
                             f"③ SSH 执行：{perm_fix_cmd(rows[0].get('dir') or '')}（只追加一条 ACL）；"
                             "处理后重扫即可，完整清单见概览页顶部提示条与音乐库「扫描体检」")
            if len(rows) > self.AUDIT_LOG_LINES:
                self.log("info", f"  {label} 另有 {len(rows) - self.AUDIT_LOG_LINES} 个未逐条列出，"
                                  f"完整清单看音乐库「扫描体检」")
        if audit.get("issues_more"):
            self.log("info", f"超出单次记录上限、未逐条列出的问题目录另有 {audit['issues_more']} 个"
                             f"（原因与上方同类一致，完整清单看「扫描体检」）")
        if not issues and audit.get("complete") is not False:
            self.log("info", "枚举无异常：所有目录都读到了底，没发现权限/深度/软链/排除导致漏扫的目录")

    # 读不到的目录快照：日志会滚掉、_last_audit 在内存里，重启就什么都不剩
    DENIED_SNAPSHOT = "scan_denied.json"
    # 打开面板时的现场取证上限：多了不探（每个是几次 syscall），只报总数
    DENIED_PROBE_LIMIT = 60

    def _record_denied(self, audit, folders=None):
        """把「读不到的目录」快照写进数据目录，给概览页提示条用。

        这份提醒必须跨得起重启：用户不会为了看“少了哪些歌”天天翻日志。只存目录与
        当时那条 errno，权限细节到展示时再现场取（可能已经被修好了）。
        扫干净时就写一份空快照，让提示条能自己消失，不留一个永远抹不掉的告警。

        但本轮只扫了一部分目录（「指定目录扫描」）时不更新：那一轮压根没看其它目录，
        拿它的“干净”去抹掉全局告警，等于骗用户说没问题。
        """
        if self.is_scoped(folders):
            return None
        denied = [{"dir": it.get("dir") or "", "code": it.get("code"),
                   "reason": it.get("reason") or ""}
                  for it in (audit.get("issues") or []) if it.get("kind") == "denied"]
        payload = {
            "ts": datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
            "count": len(denied),
            "dirs": denied[:self.DENIED_PROBE_LIMIT],
            "more": max(0, len(denied) - self.DENIED_PROBE_LIMIT),
            "roots": [_spath(r.get("root") or "") for r in (audit.get("roots") or [])],
        }
        try:
            with open(os.path.join(self.data_dir, self.DENIED_SNAPSHOT), "w",
                      encoding="utf-8") as f:
                json.dump(payload, f, ensure_ascii=False)
        except Exception:
            pass   # 快照只是提醒用，写失败不能把扫描本身弄坏
        return payload

    def denied_dirs(self) -> dict:
        """概览页提示条与「读不到的目录」弹窗的数据源：上次枚举的快照 + 当下重新取证。

        取证放在读的时候而不是扫的时候：一是可能几百个目录，不该给每次扫描白付几百
        次 syscall；二是用户很可能已经去修了 —— 打开面板就该看到「现在已可读」，
        而不是复述一遍旧状态。
        """
        snap = {}
        try:
            with open(os.path.join(self.data_dir, self.DENIED_SNAPSHOT), encoding="utf-8") as f:
                snap = json.load(f) or {}
        except Exception:
            snap = {}
        items = []
        ok_n = 0
        for i, it in enumerate(snap.get("dirs") or []):
            d = it.get("dir") or ""
            pr = perm_probe(d) if i < self.DENIED_PROBE_LIMIT else {}
            fixed = bool(pr.get("now_ok"))
            gone = bool(pr.get("gone")) if pr else not os.path.exists(d)
            if fixed:
                ok_n += 1
            items.append({
                "dir": d, "code": it.get("code"), "reason": it.get("reason") or "",
                "brief": perm_brief(pr), "mode": pr.get("mode") or "",
                "owner": pr.get("owner") or "", "uid": pr.get("uid"),
                "run_user": pr.get("self_user") or app_run_user(),
                "blocked_at": pr.get("blocked_at") or "",
                # gone 的不给修复命令：对着一个看不到的路径 setfacl 只会让人白跑一趟
                "gone": gone, "fixed": fixed,
                "fix": "" if (fixed or gone) else perm_fix_cmd(d),
            })
        return {"ok": True, "ts": snap.get("ts") or "", "count": snap.get("count", len(items)),
                "more": snap.get("more", 0), "items": items, "fixed": ok_n,
                "roots": snap.get("roots") or [], "run_user": app_run_user()}

    def probe_scan(self, folders=None):
        """仅全量扫描：只遍历目录、只数文件，不入库、不算 SHA-1、不改任何文件。

        存在的意义：正常「扫描」兼做入库与清理，日志里混着几百行入库信息，想弄清
        “哪些文件夹没扫出来、为什么”反而看不清楚。本任务只做枚举，并把逐目录
        结果与全部问题原因完整写进日志，最后给出磁盘音频数与当前入库数的对账。
        """
        folders = folders or self.enabled_folders()
        self._start_task("probe", folders)
        t0 = time.time()
        max_depth = int(self._config.get("max_depth", 12) or 12)
        ex = self._config.get("exclude_dirs") or []
        stats = {"files_total": 0, "audio_total": 0, "dirs": 0, "issues": 0,
                 "roots": len(folders), "in_db": 0, "complete": False, "kinds": {},
                 "only_db": 0, "only_disk": 0, "scoped": self.is_scoped(folders),
                 "db_total": 0, "sub_dirs": 0, "d1": 0, "d2": 0, "deep": 0,
                 "max_depth": 0, "secs": 0}
        try:
            self.log("info", f"开始仅全量扫描：{len(folders)} 个目录（只枚举计数，不入库 / 不算 SHA-1 / 不改动任何文件）"
                     + self.scope_note(folders))
            self.log("info", f"本次参数：扫描深度上限 {max_depth} · 排除目录 {'、'.join(ex) if ex else '（无）'} · 跟随软链（按 realpath 去重）")
            for i, f in enumerate(folders):
                mark = "" if os.path.isdir(f) else "  ← 不存在或不可进入，本目录一首也扫不到"
                self.log("warn" if mark else "info", f"  待扫目录 {i + 1}/{len(folders)}: {f}{mark}")
            # total=0 是“分母未知”的约定：枚举前数一遍总量等于把盘再遍历一遍，
            # 比扫本身还慢；前端据此渲染不定长动画条 + 递增计数（见 renderTask）。
            self._tick(total=0, stage="枚举中 · 正在遍历目录", current="")
            prog = [0]

            def _on_root(info):
                prog[0] += 1
                # 逐目录报一行：报完“扫到了多少”顺便报“哪些根本没扫到 / 被截 / 被排除”
                msg = (f"  枚举 {prog[0]}/{len(folders)} {info.get('root')}: "
                       f"子目录 {info.get('dirs', 0)} · 文件 {info.get('files', 0)} · "
                       f"音频 {info.get('audio', 0)}")
                if not info.get("exists"):
                    msg += " · 目录不存在或不可进入，本目录一首也扫不到"
                if info.get("errs"):
                    msg += f" · 读不到的子目录 {info['errs']} 个"
                if info.get("depth_cut"):
                    msg += f" · 超深度 {max_depth} 被截断 {info['depth_cut']} 个"
                if info.get("link_cut"):
                    msg += f" · 软链重复/成环被剪枝 {info['link_cut']} 个"
                if info.get("excluded"):
                    msg += f" · 排除规则跳过 {info['excluded']} 个"
                # 文件数远大于音频数时，就地说明差额去哪儿了（不用等到最后的总计）
                _mk = [("伴生", info.get("skip_known")), ("加密", info.get("encrypted")),
                       ("未识别", info.get("ext_skipped")), ("无扩展名", info.get("no_ext")),
                       ("是音频却读不到", info.get("inaccessible"))]
                _mk = [(k, v) for k, v in _mk if v]
                if _mk:
                    msg += " · 非音频 " + " + ".join(f"{k} {v}" for k, v in _mk)
                bad = (not info.get("exists")) or info.get("errs") or info.get("depth_cut")
                self.log("warn" if bad else "info", msg)
                # 进度不在这上报：done 已由 on_dir 按“已遍历子目录数”推进，
                # 这里再 tick 会把计数回退成根目录序号（1/2 比 3000 小得多，看着像倒退了）
                self._check_ctrl()   # 暂停在目录边界上生效，不会一暂停就空转

            _paths, audit = walk_audio(
                folders, exclude=ex, max_depth=max_depth,
                should_stop=lambda: self._stop_event.is_set(), on_root=_on_root,
                on_dir=self._enum_progress(len(folders)))
            self._last_audit = audit
            self._record_denied(audit, folders)
            _dbd = {int(k): v for k, v in (audit.get("dirs_by_depth") or {}).items()}
            stats.update({
                "files_total": audit["files_total"], "audio_total": audit["audio_total"],
                "dirs": audit.get("dirs_visited", 0), "issues": len(audit.get("issues") or []),
                "complete": bool(audit.get("complete")), "kinds": dict(audit.get("kinds") or {}),
                # 层级明细：一级 = 授权目录下的直接子目录（通常是歌手），二级再往里一层（专辑）
                "sub_dirs": max(0, audit.get("dirs_visited", 0) - len(folders)),
                "d1": _dbd.get(1, 0), "d2": _dbd.get(2, 0),
                "deep": sum(v for k, v in _dbd.items() if k >= 3),
                "max_depth": max(_dbd) if _dbd else 0,
            })
            _tot = max(audit.get("dirs_visited", 0), 1)   # 枚举完了，进度条收满再进对账阶段
            self._tick(total=_tot, done=_tot, stage="对账中", current="")
            with self._db() as c:
                all_rows = c.execute("SELECT path,folder FROM files").fetchall()
            # 指定了范围就只拿范围内的记录比：全库比会算出“磁盘 2 · 入库 1944 · 差 -1942”，
            # 还会把范围外每一条都误报成“磁盘上已没有这个文件”。
            rows = self.in_scope(all_rows, folders) if stats["scoped"] else all_rows
            db_paths = {r["path"] for r in rows}
            stats["in_db"] = len(db_paths)
            stats["db_total"] = len(all_rows)
            # 库里的 path 是当年写进去的文本（已洗过 surrogate），磁盘路径必须先过
            # _safe_text 才能对齐比较，否则名字带非 UTF-8 字节的文件会被当成“两边都有区别”。
            disk_map = {}
            for p in _paths:
                disk_map.setdefault(_safe_text(p), p)
            only_db = sorted(db_paths - set(disk_map))     # 在库、但本轮没枚到
            only_disk = sorted(set(disk_map) - db_paths)   # 本轮枚到、库里还没有
            stats["only_db"], stats["only_disk"] = len(only_db), len(only_disk)
            # 逐目录原因先摆清楚（“哪个文件夹没扫出来、为什么、怎么办”），
            # 再给完成/对账结论，日志从上往下读才连得通。
            self._log_audit(audit)
            secs = round(time.time() - t0, 1)
            stats["secs"] = secs
            if audit.get("stopped"):
                self.log("warn", f"仅全量扫描已停止：已遍历 {audit['dirs_visited']} 个目录、"
                                 f"音频 {audit['audio_total']} 个（未完成的这一轮不产生任何入库变更）")
                # 半截数字不许当结论下发（v1.9.2 定的契约）：probe 保持 None，
                # 前端才不会把这一轮没跑完的枚举数当成「上次扫描结果」长期挂在页面上。
                self._finish_task()
                return stats
            diff = audit["audio_total"] - stats["in_db"]
            self.log("info",
                     f"仅全量扫描完成：授权目录 {len(folders)} 个 · 子目录 {stats['sub_dirs']} 个"
                     f"（一级 {stats['d1']} · 二级 {stats['d2']}"
                     + (f" · 更深 {stats['deep']}" if stats["deep"] else "") + "）· "
                     f"音频 {audit['audio_total']} 首 / 遍历 {audit['files_total']} 个文件 · "
                     f"最深 {stats['max_depth']} 层 · 用时 {secs}s · 全程只读不写（未改动库记录与任何文件）")
            self._log_file_makeup(audit)
            if diff == 0 and (only_db or only_disk):
                # 真机案例：磁盘 1944 / 入库 1945 完全对得上，却有一首已消失、一首未入库
                tail = (f"总数相同只是巧合：{len(only_db)} 条库记录本轮没枚到、"
                        f"{len(only_disk)} 个磁盘文件还没入库（两边各错几个，正好互相抵消）")
            elif diff > 0:
                tail = f"差 {diff} 个未入库（若枚举不完整，差值全部或部分出在上方那些没扫到的目录里）"
            elif diff < 0:
                tail = f"入库比磁盘多 {-diff} 个：磁盘上已不存在（被移动/删除，或本次没枚举到）"
            else:
                tail = "两者一致，没有漏扫"
            self.log("info",
                     f"对账{'（仅指定目录，全库 ' + str(stats['db_total']) + ' 条不计入）' if stats['scoped'] else ''}："
                     f"磁盘音频 {audit['audio_total']} ↔ 当前入库 {stats['in_db']}，{tail}"
                     + ("" if audit.get("complete") else "（本轮枚举不完整，这个差值不能当结论）"))
            self._log_probe_diff(only_db, only_disk, audit, disk_map)
            self.log("info", "以上原因逐条列于上方；完整清单可随时看音乐库「扫描体检」，"
                              "确认无误后点「扫描音乐库」正式入库")
            with self._lock:
                self._task["probe"] = stats
            self._finish_task()
            return stats
        except Exception as e:
            self.log("error", f"仅全量扫描异常: {e}")
            self._finish_task(err=str(e))
            return stats

    def _log_probe_diff(self, only_db, only_disk, audit, disk_map):
        """把对账差值具体到文件：到底是哪几首、为什么。

        只报“入库比磁盘多 1 个”等于把判断工作丢回给用户——他要的本来就是“那一个是谁”。
        这里按能不能 stat、所在根目录是否读到底分类，直接给处置方向。
        """
        if not only_db and not only_disk:
            return

        # 区别“本轮没扫到”与“本来就不该扫到”的哨兵（两者处置完全相反）
        _OUTSIDE_ALL = "\x00outside"

        def _root_reason(p):
            """文件存在却没被枚到时，到它所属根目录的审计结果里查原因。"""
            for i in audit.get("roots") or []:
                r = i.get("root")
                if not r or not (p == r or p.startswith(r + os.sep)):
                    continue
                if not i.get("exists"):
                    return "所在根目录本轮不存在/进不去"
                if i.get("errs"):
                    return f"本轮有 {i['errs']} 个子目录读不到"
                if i.get("depth_cut"):
                    return (f"超出扫描深度上限（本轮 {i['depth_cut']} 个目录被截断），"
                            f"调高设置页「扫描深度上限」就能扫到")
            for f in self.all_folders():
                if f and (p == f or p.startswith(f + os.sep)):
                    return ""
            # 本轮 roots 里没它、全部授权目录里也没它：这类记录根本不该参与枚举。
            # 真机案例：上传/转码的临时目录在 /vol1/@appdata/music-tidy/uploads 下，
            # 旧版把它报成“多半命中排除规则”，把人一路引到设置页去翻排除列表。
            return _OUTSIDE_ALL

        if only_db:
            self.log("warn", f"在库、但本轮没扫到 {len(only_db)} 个（就是上面差值的来源）：")
            for p in only_db[:self.AUDIT_LOG_LINES]:
                if "\\udc" in p or "\\x" in p:
                    # 库里存的是转义后的路径，拿它去 stat 必定失败，不能据此说“文件已删”
                    why = "路径含非 UTF-8 字节（已转义显示），无法核对磁盘；先把文件名改成正常 UTF-8 再重扫"
                elif os.path.exists(p):
                    rsn = _root_reason(p)
                    if rsn == _OUTSIDE_ALL:
                        home = _spath(self.data_dir or "")
                        inside = home and (p == home or p.startswith(home + os.sep))
                        why = (("该记录在本应用自己的数据目录里（上传/转码的临时文件），不在任何授权目录下，"
                               "本来就不会被枚举到 —— 不是漏扫，也不用去查「排除目录」") if inside else
                               "该记录已不在任何授权目录下（目录被移走/改名，或当时就添在范围外），"
                               "本轮枚举看不到它属正常；要重新扫到它，到「目录」页把所在路径加为授权目录")
                    elif rsn:
                        why = f"文件还在，但这一轮没枚举到（{rsn}），按上方原因处理后重扫即可"
                    else:
                        why = "文件还在却没被枚举到：多半命中排除规则，检查设置页「排除目录」"
                else:
                    why = "磁盘上确实没有这个文件（已移动/删除/改名），正式扫描会清掉这条记录"
                self.log("warn", f"  未扫到: {p} —— {why}")
            if len(only_db) > self.AUDIT_LOG_LINES:
                self.log("info", f"  未扫到另有 {len(only_db) - self.AUDIT_LOG_LINES} 个未逐条列出")
        if only_disk:
            self.log("info", f"磁盘上有、库里还没有 {len(only_disk)} 个（点「扫描音乐库」就会补上）：")
            for p in only_disk[:self.AUDIT_LOG_LINES]:
                try:
                    disk_map.get(p, p).encode("utf-8")
                    why = ""
                except UnicodeEncodeError:
                    why = "（文件名含非 UTF-8 字节，不重命名会一直在“未入库”里）"
                self.log("info", f"  待入库: {p}{why}")
            if len(only_disk) > self.AUDIT_LOG_LINES:
                self.log("info", f"  待入库另有 {len(only_disk) - self.AUDIT_LOG_LINES} 个未逐条列出")

    def audit_scan(self, folders=None) -> dict:
        """只做目录枚举不对比入库，用于「扫描体检」：回答“哪些文件没被扫到、为什么”。"""
        folders = folders or self.enabled_folders()
        _, audit = self._enumerate_audio(folders)
        with self._db() as c:
            all_rows = c.execute("SELECT path,folder FROM files").fetchall()
        scoped = self.is_scoped(folders)
        rows = self.in_scope(all_rows, folders) if scoped else all_rows
        audit["in_db"] = len(rows)
        audit["db_total"] = len(all_rows)
        audit["scoped"] = scoped
        return audit

    def scan(self, folders=None):
        """三阶段增量扫描：

        1. 枚举：遍历目录统计「文件总数 / 音频数」，只走目录树不读内容；
        2. 对比：与库中 (size, mtime) 比较，区分 新增 / 变化 / 未变化，
           并统计未变化部分「已整理 / 未整理」，同时清理磁盘已删除的入库记录；
        3. 增量入库：只对新增与变化的音频计算 SHA-1 并写库，未变化的仅刷新
           歌词/封面附属状态（纯 stat，无内容读取）。

        旧实现对每个文件都全量读盘算 SHA-1，几千首的曲库耗时极长，
        任务常在完成前被中断，导致「飞牛扫出七千首、本软件只入库一千九」。
        """
        folders = folders or self.enabled_folders()
        self._start_task("scan", folders)
        self.log("info", f"开始扫描 {len(folders)} 个目录" + self.scope_note(folders))
        stats = {"files_total": 0, "found": 0, "new": 0, "updated": 0,
                 "unchanged": 0, "removed": 0, "tidied": 0, "errors": 0,
                 "kept_suspect": 0, "badname": 0}
        try:
            # ---- 阶段 1：枚举 ----
            self._tick(total=0, stage="枚举中 · 正在遍历目录", current="")
            audio_paths, audit = self._enumerate_audio(folders, progress=True)
            total_files = audit["files_total"]
            stats["files_total"] = total_files
            stats["found"] = len(audio_paths)
            self.log("info", f"枚举完成：遍历 {total_files} 个文件，其中音频 {stats['found']} 个"
                             + ("" if audit["complete"] else "（本次枚举不完整，详见上方告警）"))
            self._tick(total=stats["found"])
            if not self._check_ctrl():
                self.log("warn", "扫描已停止")
                self._finish_task()
                return stats

            # ---- 阶段 2：与库对比 ----
            with self._db() as c:
                existing = {r["path"]: r for r in
                            c.execute("SELECT id,path,size,mtime,tidied,sha1 FROM files").fetchall()}
            audio_set = set(audio_paths)
            todo = []
            for p in audio_paths:
                row = existing.get(p)
                if row is None:
                    todo.append(p)
                    continue
                try:
                    st = os.stat(p)
                except OSError:
                    todo.append(p)
                    continue
                if st.st_size != row["size"] or abs(st.st_mtime - row["mtime"]) > 1:
                    todo.append(p)
                elif not row["sha1"]:
                    todo.append(p)  # 上次任务中断时只入了库没算 hash，本轮补算
                else:
                    stats["unchanged"] += 1
                    if row["tidied"] == 1:
                        stats["tidied"] += 1
            # 只有「完整枚举过」的目录才能拿来说磁盘上少了文件：某个子目录读不了 /
            # 超深度被截断 / 目录不存在时，本次没看见不等于磁盘上没有。
            def _under(p, r):
                return p == r or p.startswith(r + os.sep)

            trusted = [i["root"] for i in audit["roots"]
                       if i["exists"] and not i["errs"] and not i["depth_cut"]]
            suspect = [i["root"] for i in audit["roots"] if i["root"] not in trusted]
            missing = [p for p in existing if p not in audio_set]
            removed = [p for p in missing if any(_under(p, r) for r in trusted)]
            kept = [p for p in missing
                    if not any(_under(p, r) for r in trusted)
                    and any(_under(p, r) for r in suspect)]
            stats["removed"] = len(removed)
            stats["kept_suspect"] = len(kept)
            self.log(
                "info",
                f"对比完成：待入库 {len(todo)}（新增/变化/补算hash）· 未变化 {stats['unchanged']}"
                f"（其中已整理 {stats['tidied']}、未整理 {stats['unchanged'] - stats['tidied']}）"
                f"· 磁盘已删除 {stats['removed']}")
            if kept:
                self.log("warn",
                         f"{len(kept)} 个已有记录本次未枚到，但所在目录枚举不完整（权限/深度/不存在），已保留不删；"
                         f"涉及：" + " | ".join(suspect[:5]))
            if removed:
                self.drop_missing(removed)

            # ---- 阶段 3：增量入库（只处理新增/变化/待补 hash）----
            # 先入库（sha1 留空），再计算 SHA-1 回填；任务中断时文件也已可见，
            # 未算的 hash 由下次扫描在阶段 2 检出补算。
            self._tick(total=len(todo))
            stopped = False
            for i, p in enumerate(todo):
                if not self._check_ctrl():
                    stopped = True
                    break
                need_sha1 = False
                try:
                    st = os.stat(p)
                    has_lrc, has_cov, code = self._sidecar_state(p)
                    with self._db() as c:
                        # upsert：与增量监听线程并发入库时避免 UNIQUE 冲突
                        row = c.execute("SELECT id,size,mtime,sha1 FROM files WHERE path=?", (p,)).fetchone()
                        if row is None:
                            cur = c.execute(
                                "INSERT INTO files(path,folder,name,size,mtime,sha1,has_lyric,has_cover,code) VALUES(?,?,?,?,?,?,?,?,?) "
                                "ON CONFLICT(path) DO NOTHING",
                                (p, os.path.dirname(p), os.path.basename(p), st.st_size, st.st_mtime, "", has_lrc, has_cov, code))
                            if cur.rowcount > 0:
                                stats["new"] += 1
                            need_sha1 = True
                        else:
                            if row["size"] != st.st_size or row["mtime"] != st.st_mtime:
                                c.execute(
                                    "UPDATE files SET size=?,mtime=?,sha1='',tidied=0 WHERE id=?",
                                    (st.st_size, st.st_mtime, row["id"]))
                                stats["updated"] += 1
                                need_sha1 = True
                            elif not row["sha1"]:
                                need_sha1 = True
                            c.execute("UPDATE files SET has_lyric=?,has_cover=?,code=? WHERE id=?",
                                      (has_lrc, has_cov, code, row["id"]))
                    if need_sha1 and self._check_ctrl():
                        try:
                            sha1 = file_sha1(p)
                            with self._db() as c:
                                c.execute("UPDATE files SET sha1=? WHERE path=?", (sha1, p))
                        except OSError as e:
                            self.log("warn", f"计算 SHA-1 失败 {p}: {e}（下次扫描补算）")
                except UnicodeEncodeError:
                    # 非 UTF-8 文件名：sqlite3 绑不了含 surrogate 的路径，整条 INSERT 会失败。
                    # 不单独计数的话，这些文件只会默默消失。
                    stats["badname"] += 1
                    stats["errors"] += 1
                    self.log("warn", f"文件名编码异常，无法入库（建议重命名）: {_safe_text(p)}")
                except Exception as e:
                    stats["errors"] += 1
                    self.log("warn", f"扫描失败 {p}: {e}")
                self._tick(done=i + 1)

            # 未变化的文件只刷新附属状态（纯 stat，不读内容），修正手工增删的歌词/封面
            if not stopped:
                todo_set = set(todo)
                refresh = [p for p in audio_paths if p not in todo_set]
                with self._db() as c:
                    for p in refresh:
                        row = existing.get(p)
                        if row is None:
                            continue
                        try:
                            has_lrc, has_cov, code = self._sidecar_state(p)
                        except OSError:
                            continue
                        c.execute("UPDATE files SET has_lyric=?,has_cover=?,code=? WHERE id=?",
                                  (has_lrc, has_cov, code, row["id"]))

            if stopped:
                self.log("warn", f"扫描已停止：本次入库 {stats['new'] + stats['updated']}，可再次扫描继续")
            else:
                self.log("info", f"扫描完成：音频 {stats['found']}，新增 {stats['new']}，"
                                 f"更新 {stats['updated']}，未变化 {stats['unchanged']}，"
                                 f"清理已删除 {stats['removed']}")
                if stats["kept_suspect"] or stats["badname"] or audit["inaccessible"]:
                    self.log("warn",
                             f"若入库数明显少于磁盘音频数，本次原因：枚举不完整而保留的旧记录 {stats['kept_suspect']} · "
                             f"无法访问的音频 {audit['inaccessible']} · 文件名编码异常 {stats['badname']}；"
                             f"详情见音乐库「扫描体检」")
        except Exception as e:
            self.log("error", f"扫描异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    # ------------------------------------------------------------- tidy one
    def _extract_embedded_cover(self, path: str) -> str:
        """从音频文件中提取内嵌封面，保存为逐曲同名 .jpg。

        如果音频已自带内嵌封面（专辑自带），直接提取使用，跳过在线搜索。
        返回提取后的封面路径，无封面或提取失败返回空字符串。
        """
        stem = os.path.splitext(path)[0]
        # 已有同名封面则跳过
        for cand in (stem + ".jpg", stem + ".jpeg", stem + ".png"):
            if os.path.exists(cand):
                return cand
        ext = os.path.splitext(path)[1].lower()
        img_data = b""
        try:
            if ext == ".flac":
                from mutagen.flac import FLAC
                f = FLAC(path)
                if f.pictures:
                    img_data = f.pictures[0].data
            elif ext in (".mp4", ".m4a", ".m4b", ".m4v"):
                from mutagen.mp4 import MP4
                f = MP4(path)
                covr = f.get("covr", [])
                if covr:
                    img_data = bytes(covr[0])
            elif ext in (".ogg", ".oga", ".opus"):
                import base64
                from mutagen.flac import Picture
                if ext == ".opus":
                    from mutagen.oggopus import OggOpus as Ogg
                else:
                    from mutagen.oggvorbis import OggVorbis as Ogg
                f = Ogg(path)
                pics = f.get("metadata_block_picture", [])
                if pics:
                    pic = Picture(base64.b64decode(pics[0]))
                    img_data = pic.data
            elif ext in (".aiff", ".aif", ".aifc", ".wma", ".asf"):
                return ""  # 不支持的格式
            else:
                # mp3 及其它按 ID3 处理的容器
                from mutagen.id3 import ID3, ID3NoHeaderError
                try:
                    tags = ID3(path)
                except ID3NoHeaderError:
                    return ""
                apics = tags.getall("APIC")
                if apics:
                    img_data = apics[0].data
        except Exception:
            return ""
        if not img_data:
            return ""
        # 写入同名 .jpg
        target = stem + ".jpg"
        try:
            with open(target, "wb") as f:
                f.write(img_data)
            return target
        except Exception:
            return ""

    def _maybe_ai_cover(self, path: str, artist: str, title: str, lyric: str = "", style: str = "") -> bool:
        """曲库找不到封面时，用 AI 文生图兜底生成封面（需开启 enable_ai_cover 且配置生图模型）。

        画面描述由 LLM 依据歌词大意与风格生成，封面包含歌名标题文字。
        成功写入逐曲同名 .jpg 返回 True；未开启/无模型/生成失败返回 False。
        """
        if not self._config.get("enable_ai_cover"):
            return False
        if not (self.llm and self.llm.available and self.llm.image_available and title):
            return False
        stem = os.path.splitext(path)[0]
        target = stem + ".jpg"
        try:
            prompt = self.llm.cover_prompt(artist, title, lyric, style)
            if not prompt:
                self.log("warn", f"AI 封面：画面描述生成失败 {os.path.basename(path)}")
                return False
            img = self.llm.generate_image(prompt)
            if not img or len(img) < 1024:
                reason = getattr(self.llm, "last_error", "") or "未知原因"
                self.log("warn", f"AI 封面：图片生成失败 {os.path.basename(path)}（{reason}）")
                return False
            tmp = target + ".tmp"
            with open(tmp, "wb") as f:
                f.write(img)
            os.replace(tmp, target)
            self.log("info", f"AI 封面：已生成 {os.path.basename(path)}（{len(img)//1024}KB）")
            return True
        except Exception as e:
            self.log("warn", f"AI 封面生成异常 {os.path.basename(path)}: {e}")
            try:
                os.remove(target + ".tmp")
            except Exception:
                pass
            return False

    def _touch_for_rescan(self, path: str):
        """把音频及其同名附属文件（.lrc/.jpg 等）的修改时间刷到当下。

        飞牛音乐按「路径+修改时间/大小」做增量扫描指纹；只改 sidecar 歌词时
        音频 mtime 不变，飞牛就不会重读。touch 后下次扫描会重新解析这些文件。
        """
        targets = [path]
        stem = os.path.splitext(path)[0]
        targets += [stem + sfx for sfx in self._SIDECAR_SUFFIXES]
        for p in targets:
            try:
                if os.path.exists(p):
                    os.utime(p, None)
            except Exception:
                pass  # 非属主文件可能无权限，静默跳过即可

    def _write_sidecar(self, path: str, lyric: str, cover_url: str):
        cfg = self._config
        stem = os.path.splitext(path)[0]
        result = {"lyric": False, "cover": False}
        # 乱码歌词防护：GBK 误解码等产生的替换符文本一律不落盘
        if lyric and lyric_garbled(lyric):
            self.log("warn", f"歌词疑似乱码，拒绝写入 {os.path.basename(path)}")
            lyric = ""
        # 歌词
        lrc_path = stem + ".lrc"
        # 已有歌词若是乱码（历史版本 GBK 误解码写入），视为缺失并强制覆盖
        exist_bad = False
        if os.path.exists(lrc_path):
            try:
                with open(lrc_path, "r", encoding="utf-8", errors="replace") as f:
                    exist_bad = lyric_garbled(f.read())
            except Exception:
                exist_bad = False
        if exist_bad:
            result["lyric"] = False
            # 本轮无新歌词时删除乱码文件，让状态回归「缺歌词」可被补全
            if not lyric:
                try:
                    os.remove(lrc_path)
                    self.log("info", f"已删除乱码歌词 {os.path.basename(lrc_path)}")
                except Exception:
                    pass
        if lyric and (not os.path.exists(lrc_path) or cfg.get("overwrite_lyric") or exist_bad):
            try:
                with open(lrc_path, "w", encoding="utf-8") as f:
                    f.write(lyric if lyric.endswith("\n") else lyric + "\n")
                result["lyric"] = True
                self._touch_for_rescan(path)  # 让飞牛增量扫描感知歌词变化
            except Exception as e:
                self.log("warn", f"写歌词失败 {lrc_path}: {e}")
        elif os.path.exists(lrc_path):
            result["lyric"] = True  # 已有则视为已具备

        # 封面：只认逐曲同名 .jpg/.png（目录级 cover.jpg 常与具体歌曲不符，不计入）
        stem_cover = ""
        for cand in (stem + ".jpg", stem + ".jpeg", stem + ".png"):
            if os.path.exists(cand):
                stem_cover = cand
                break
        cover_done = bool(stem_cover)
        result["cover"] = cover_done
        # 有置信封面时：缺同名封面，或开了「覆盖封面」，都重新下载
        # （不再写目录级 cover.jpg：它会被同目录其它歌曲共用，造成"封面都一样"）
        if cover_url and (not cover_done or cfg.get("overwrite_cover")):
            target = stem + ".jpg"
            if download_cover(cover_url, target):
                result["cover"] = True
        self.log("info", f"封面 {os.path.basename(path)}: 同名={'有' if stem_cover else '无'} "
                  f"置信URL={'有' if cover_url else '无'} 结果={'✓' if result['cover'] else '✗'}")
        return result

    @staticmethod
    def _image_mime(img_path: str, data: bytes) -> str:
        if data[:8].startswith(b"\x89PNG\r\n\x1a\n"):
            return "image/png"
        return "image/jpeg"

    def _embed_tags(self, path: str, meta: dict, cover_path=None, lyric: str = ""):
        """尝试用 mutagen 或 ffmpeg 写入内嵌标签。返回是否成功。"""
        if not self._config.get("embed_tags"):
            return False
        # mutagen（随包 vendor，正常必定可用）
        try:
            import mutagen  # noqa
            ok = self._embed_mutagen(path, meta, cover_path, lyric)
            if not ok:
                self.log("warn", f"内嵌标签失败（格式不支持或写入出错）: {os.path.basename(path)}")
            return ok
        except Exception as e:
            self.log("warn", f"内嵌标签异常 {os.path.basename(path)}: {e}")
        # ffmpeg 兜底
        if shutil.which("ffmpeg"):
            try:
                return self._embed_ffmpeg(path, meta, cover_path)
            except Exception:
                pass
        if not getattr(self, "_embed_warned", False):
            self._embed_warned = True
            self.log("warn", "无法内嵌标签/封面（mutagen 与 ffmpeg 均不可用），"
                             "仅写 sidecar，飞牛音乐可能不显示封面")
        return False

    def _embed_mutagen(self, path: str, meta: dict, cover_path=None, lyric=""):
        """按容器格式分派写入文本标签与内嵌封面。

        飞牛音乐读取的是音频内嵌封面（APIC/covr/Picture），
        逐曲同名 .jpg 它并不识别，因此封面必须真正写进文件。
        """
        ext = os.path.splitext(path)[1].lower()
        img = b""
        if cover_path and os.path.exists(cover_path):
            try:
                with open(cover_path, "rb") as fp:
                    img = fp.read()
            except OSError:
                img = b""
        try:
            if ext == ".flac":
                from mutagen.flac import FLAC, Picture
                f = FLAC(path)
                f["artist"] = meta.get("artist", "")
                f["title"] = meta.get("title", "")
                f["album"] = meta.get("album", "")
                if meta.get("style"):
                    f["genre"] = meta["style"]  # 飞牛音乐「风格」页读内嵌 genre
                if lyric:
                    f["lyrics"] = lyric
                if img:
                    f.clear_pictures()
                    pic = Picture()
                    pic.type = 3
                    pic.mime = self._image_mime(cover_path, img)
                    pic.data = img
                    f.add_picture(pic)
                f.save()
                return True
            if ext in (".mp4", ".m4a", ".m4b", ".m4v"):
                from mutagen.mp4 import MP4, MP4Cover
                f = MP4(path)
                for key, val in (("\xa9ART", meta.get("artist", "")),
                                 ("\xa9nam", meta.get("title", "")),
                                 ("\xa9alb", meta.get("album", "")),
                                 ("\xa9gen", meta.get("style", ""))):
                    if val:
                        f[key] = [val]
                if lyric:
                    f["\xa9lyr"] = [lyric]
                if img:
                    fmt = MP4Cover.FORMAT_PNG if img[:8].startswith(b"\x89PNG\r\n\x1a\n") else MP4Cover.FORMAT_JPEG
                    cov = MP4Cover(img, imageformat=fmt)
                    cov.description = b"Cover"
                    f["covr"] = [cov]
                f.save()
                return True
            if ext in (".ogg", ".oga", ".opus"):
                import base64
                from mutagen.flac import Picture
                if ext == ".opus":
                    from mutagen.oggopus import OggOpus as Ogg
                else:
                    from mutagen.oggvorbis import OggVorbis as Ogg
                f = Ogg(path)
                f["artist"] = meta.get("artist", "")
                f["title"] = meta.get("title", "")
                f["album"] = meta.get("album", "")
                if meta.get("style"):
                    f["genre"] = meta["style"]
                if lyric:
                    f["lyrics"] = lyric
                if img:
                    pic = Picture()
                    pic.type = 3
                    pic.mime = self._image_mime(cover_path, img)
                    pic.data = img
                    f["metadata_block_picture"] = [
                        base64.b64encode(pic.write()).decode("ascii")]
                f.save()
                return True
            if ext in (".aiff", ".aif", ".aifc", ".wma", ".asf"):
                return False  # ID3/MP3 写入器不适用于这些容器，避免写坏文件
            # mp3 及其它按 ID3 处理的容器
            if ext == ".wav":
                # WAV(RIFF) 容器：mutagen 支持把 ID3 写在文件尾部，飞牛可读取
                from mutagen.wave import WAVE
                from mutagen.id3 import TIT2, TPE1, TALB, APIC, USLT, TCON
                f = WAVE(path)
                if f.tags is None:
                    f.add_tags()
                f.tags.add(TIT2(encoding=3, text=meta.get("title", "")))
                f.tags.add(TPE1(encoding=3, text=meta.get("artist", "")))
                f.tags.add(TALB(encoding=3, text=meta.get("album", "")))
                if meta.get("style"):
                    f.tags.add(TCON(encoding=3, text=meta["style"]))
                if lyric:
                    f.tags.add(USLT(encoding=3, lang="chi", desc="", text=lyric))
                if img:
                    f.tags.delall("APIC")
                    f.tags.add(APIC(encoding=3, mime=self._image_mime(cover_path, img),
                                    type=3, desc="Cover", data=img))
                f.save()
                return True
            from mutagen.id3 import ID3, TIT2, TPE1, TALB, APIC, USLT, TCON, ID3NoHeaderError
            try:
                tags = ID3(path)
            except ID3NoHeaderError:
                tags = ID3()
            tags.add(TIT2(encoding=3, text=meta.get("title", "")))
            tags.add(TPE1(encoding=3, text=meta.get("artist", "")))
            tags.add(TALB(encoding=3, text=meta.get("album", "")))
            if meta.get("style"):
                tags.add(TCON(encoding=3, text=meta["style"]))
            if lyric:
                tags.add(USLT(encoding=3, lang="chi", desc="", text=lyric))
            if img:
                tags.delall("APIC")
                tags.add(APIC(encoding=3, mime=self._image_mime(cover_path, img),
                              type=3, desc="Cover", data=img))
            tags.save(path, v2_version=3)
            return True
        except Exception as e:
            self.log("warn", f"内嵌失败 {os.path.basename(path)} [{ext or '无扩展名'}]: {e}")
            return False

    def _embed_ffmpeg(self, path: str, meta: dict, cover_path=None):
        ext = os.path.splitext(path)[1].lower()
        tmp = path + ".tidy_tmp" + ext
        cmd = ["ffmpeg", "-y", "-i", path]
        for k, v in (("title", meta.get("title", "")), ("artist", meta.get("artist", "")),
                     ("album", meta.get("album", "")), ("genre", meta.get("style", ""))):
            if v:
                cmd += ["-metadata", f"{k}={v}"]
        if cover_path and os.path.exists(cover_path):
            cmd += ["-i", cover_path, "-map", "0", "-map", "1", "-c", "copy", "-disposition:v:1", "attached_pic"]
        cmd += [tmp]
        r = subprocess.run(cmd, capture_output=True, timeout=120)
        if r.returncode == 0 and os.path.exists(tmp):
            os.replace(tmp, path)
            return True
        if os.path.exists(tmp):
            os.remove(tmp)
        return False

    def _parse_name(self, path: str) -> dict:
        """文件名解析入口：自定义规则优先，未命中回退内置 parse_filename。"""
        return apply_name_rules(path, self._config.get("name_rules")) or parse_filename(path)

    def _llm_should_rescue(self, meta: dict) -> bool:
        """这份解析要不要请大模型判一次。

        三种情况：歌名没解析出来、歌手没解析出来，以及规则自己标了 suspect
        （剥过歌单前缀、或三段以上的命名）。suspect 分支是 v1.13.0 补的——旧版只
        在字段为空时才调模型，像「好听的歌曲推荐-下山 - 麦小兜」这种两头都错、
        但两头都非空的解析，永远进不了 AI 兜底。
        """
        if not (self.llm and getattr(self.llm, "available", False)):
            return False
        return bool(not meta.get("title") or not meta.get("artist") or meta.get("suspect"))

    def _llm_rescue(self, path: str, meta: dict) -> dict:
        """请大模型重认一次文件名，用「结果必须来自文件名」挡住幻觉。

        把规则切出的两种左右解释当候选一起递过去，模型只需挑对/纠正，比让它
        凭空猜准得多。判不出、或给了文件名里没出现的歌手歌名，一律返回 {}，
        上层沿用规则结果（宁缺毋滥，也不把模型的胡言写进标签）。
        """
        name = os.path.basename(str(path or ""))
        a0, t0 = meta.get("artist", ""), meta.get("title", "")
        cands = [(a0, t0)] if (a0 or t0) else []
        if a0 and t0:
            # 互换解释一起递过去：规则层定序靠的是启发式，模型只需要认哪个像歌手
            cands.append((t0, a0))
        judge = getattr(self.llm, "judge_name", None)
        try:
            # 旧版 mock 只带 recognize：没 judge_name 就退回原路径，不打破历史用例
            lm = (judge(name, cands) if callable(judge) else self.llm.recognize(name)) or {}
        except Exception as e:
            self.log("warn", f"AI 判名失败 {name}: {e}")
            return {}
        artist = str(lm.get("artist") or "").strip()
        title = str(lm.get("title") or "").strip()
        if not title or not llm_meta_sane(name, artist, title):
            return {}
        return {"artist": artist, "title": title,
                "style": str(lm.get("style") or "").strip(),
                "code": meta.get("code", "")}

    def _resolve_meta(self, path: str, meta: dict, diag: dict = None):
        """确定 (artist, title, song)。

        带全维度代码或常规命名均先按 parse_filename 的默认解释；
        「A - B」两种解释均可能时，用曲库搜索消歧（resolve_and_match），
        只有置信匹配才返回 song，避免错误结果覆盖文件名信息。
        """
        left, right = meta.get("left", ""), meta.get("right", "")
        if left and right:
            pairs = [(meta.get("artist", ""), meta.get("title", ""))]
            alt = (meta.get("title", ""), meta.get("artist", ""))  # 交换解释
            if alt != pairs[0]:
                pairs.append(alt)
            return resolve_and_match(pairs, diag=diag)
        artist, title = meta.get("artist", ""), meta.get("title", "")
        if not title:
            return artist, title, {}
        return search_confident(artist, title, diag=diag)

    def _tidy_file(self, path: str, override: dict = None) -> dict:
        """整理单个文件：识别 -> 刮削 -> 写歌词/封面 -> 可选分类。

        override 传 artist/title/album/style 有效值时视为人工修正信息，
        直接按该信息重新搜刮，并在结果记录上标记 manual=1；
        未传 override 且库中已有手工标记时，沿用库值，不被文件名解析覆盖。
        """
        cfg = self._config
        res = {"path": path, "ok": False, "artist": "", "title": "", "lyric": False,
               "cover": False, "reason": "", "ai": False}
        try:
            ovr = {k: str((override or {}).get(k) or "").strip()
                   for k in ("artist", "title", "album", "style")}
            db_manual = {}
            with self._db() as c:
                row = c.execute(
                    "SELECT artist,title,album,style,manual FROM files WHERE path=?",
                    (path,)).fetchone()
            if row and row["manual"] and row["title"]:
                db_manual = {k: row[k] for k in ("artist", "title", "album", "style") if row[k]}
            # 人工修正值优先，缺失字段沿用库中已有手工信息
            manual_vals = {}
            for k in ("artist", "title", "album", "style"):
                v = ovr.get(k) or db_manual.get(k, "")
                if v:
                    manual_vals[k] = v
            if manual_vals and not manual_vals.get("title"):
                manual_vals["title"] = self._parse_name(path).get("title", "")
            if not manual_vals.get("title"):
                manual_vals = {}

            used_llm = False
            if manual_vals:
                meta = {"artist": manual_vals.get("artist", ""),
                        "title": manual_vals.get("title", ""),
                        "album": manual_vals.get("album", ""),
                        "style": manual_vals.get("style", "")}
            else:
                meta = self._parse_name(path)
                # 正则解析失败、字段缺失，或规则自己判定「左右歧义/命中歌单前缀」时，
                # 用 LLM 兜底一次（v1.13.0：suspect 分支，旧版这种两头非空的错解析进不了 AI）
                if self._llm_should_rescue(meta):
                    before = (meta.get("artist", ""), meta.get("title", ""))
                    rescued = self._llm_rescue(path, meta)
                    if rescued:
                        used_llm = True
                        res["ai"] = True
                        if (rescued.get("artist"), rescued.get("title")) != before:
                            self.log("info", f"AI 判名纠正 {os.path.basename(path)}："
                                             f"{before[0]}/{before[1]} → "
                                             f"{rescued['artist']}/{rescued['title']}")
                        meta = rescued
            if not meta.get("title"):
                res["ok"] = False
                res["reason"] = "parse_fail"
                return res

            lyric = ""
            cover_url = ""
            song = {}
            diag = {}
            if cfg.get("enable_scrape"):
                artist, title, song = self._resolve_meta(path, meta, diag=diag)
                meta = dict(meta)
                meta["artist"], meta["title"] = artist, title
                if song:
                    cover_url = song.get("album_pic", "")
                    lyric = fetch_lyric(song)
                    if lyric_garbled(lyric):
                        self.log("warn", f"歌词乱码丢弃 {os.path.basename(path)}")
                        lyric = ""
                else:
                    res["reason"] = "no_confident_match"
                if not song and not diag:
                    diag["note"] = "searched_but_low_confidence"
                if diag:
                    res["reason"] = (res["reason"] + " " if res["reason"] else "") + \
                        json.dumps(diag, ensure_ascii=False)[:180]

            # 优先识别内嵌封面：如果音频已自带封面，提取使用，跳过在线搜索
            embedded_cover = self._extract_embedded_cover(path)
            if embedded_cover:
                self.log("info", f"使用内嵌封面 {os.path.basename(path)}")
            side = self._write_sidecar(path, lyric, cover_url)
            # 曲库没有封面且无内嵌封面 -> 按开关尝试 AI 文生图兜底
            if not side["cover"] and not embedded_cover:
                if self._maybe_ai_cover(path, meta.get("artist", ""), meta.get("title", ""),
                                        lyric, meta.get("style", "")):
                    side["cover"] = True
            res["lyric"] = side["lyric"]
            res["cover"] = side["cover"]

            artist = meta.get("artist", "")
            title = meta.get("title", "")
            scraped_album = song.get("album", "") if song else ""
            if manual_vals:
                # 手工编辑的专辑优先于搜刮结果
                album = meta.get("album", "") or scraped_album
            else:
                album = scraped_album or meta.get("album", "")
            style = meta.get("style", "") or (parse_tidy_code(os.path.basename(path)).get("style_label", ""))

            stem = os.path.splitext(path)[0]
            cover_path = next((c for c in (stem + ".jpg", stem + ".jpeg", stem + ".png")
                               if os.path.exists(c)), stem + ".jpg")
            self._embed_tags(path, {"artist": artist, "title": title, "album": album, "style": style},
                             cover_path if side["cover"] else None, lyric)

            # 分类移动
            new_path = path
            if cfg.get("enable_classify") and artist and self._config.get("classify_keep_sidecar"):
                new_path = self._classify(path, artist, album, song)
            if not new_path:
                new_path = path

            code = meta.get("code", "") or parse_tidy_code(os.path.basename(path)).get("code", "")
            with self._db() as c:
                c.execute(
                    "UPDATE files SET artist=?,title=?,album=?,style=?,code=?,has_lyric=?,has_cover=?,tidied=1,tidied_at=?,manual=?,err='',path=? WHERE path=?",
                    (artist, title, album, style, code, 1 if side["lyric"] else 0, 1 if side["cover"] else 0,
                     _now(), 1 if manual_vals else 0, new_path, path))
            res["ok"] = True
            res["artist"], res["title"] = artist, title
            res["manual"] = bool(manual_vals)
            res["final_path"] = new_path          # 分类移动后的真实路径（未移动时同 path）
            return res
        except Exception as e:
            self.log("error", f"整理失败 {path}: {e}\n{traceback.format_exc()}")
            try:
                with self._db() as c:
                    c.execute("UPDATE files SET tidied=2,err=? WHERE path=?", (str(e)[:500], path))
            except Exception:
                pass
            res["ok"] = False
            return res

    def _classify(self, path: str, artist: str, album: str, song: dict) -> str:
        """按 歌手/专辑 移动文件（同卷原子移动）。返回新路径，失败返回原路径。"""
        folder = os.path.dirname(path)
        if not album and song:
            album = song.get("album", "")
        target_dir = os.path.join(folder, _sanitize_dir(artist))
        if album:
            target_dir = os.path.join(target_dir, _sanitize_dir(album))
        try:
            os.makedirs(target_dir, exist_ok=True)
            base = os.path.basename(path)
            new_path = os.path.join(target_dir, base)
            if os.path.abspath(new_path) == os.path.abspath(path):
                return path
            if os.path.exists(new_path):
                # 同名冲突：加序号
                stem, ext = os.path.splitext(base)
                i = 1
                while os.path.exists(new_path):
                    new_path = os.path.join(target_dir, f"{stem}_{i}{ext}")
                    i += 1
            shutil.move(path, new_path)
            # 移动附属文件
            for suffix in (".lrc", ".jpg"):
                src = os.path.splitext(path)[0] + suffix
                if os.path.exists(src):
                    try:
                        shutil.move(src, os.path.splitext(new_path)[0] + suffix)
                    except Exception:
                        pass
            return new_path
        except Exception as e:
            self.log("warn", f"分类移动失败 {path}: {e}")
            return path

    def reset_tidied(self, folders=None) -> int:
        """把已整理/失败的文件重置为未整理（用于「重新整理」），返回重置数量。"""
        folders = folders or self.enabled_folders()
        n = 0
        with self._db() as c:
            rows = c.execute("SELECT id,path,folder FROM files WHERE tidied IN (1,2)").fetchall()
            hit = [r["id"] for r in rows
                   if any(under_path(r["folder"] or r["path"], root) for root in folders)]
            # 按批更新：一条 SQL 带几万个 id 会踩到 SQLite 变量数上限（旧版默认 999）
            for i in range(0, len(hit), 500):
                chunk = hit[i:i + 500]
                cur = c.execute("UPDATE files SET tidied=0 WHERE id IN ("
                                + ",".join("?" * len(chunk)) + ")", chunk)
                n += cur.rowcount
        self.log("info", f"已重置 {n} 个文件为未整理状态" + self.scope_note(folders))
        return n

    def re_tidy(self, path: str, fields: dict = None) -> dict:
        """单曲重新整理。fields 传编辑后的 artist/title/album/style 时按人工信息重新搜刮。"""
        if not is_audio_file(path) or not os.path.exists(path):
            return {"path": path, "ok": False, "reason": "file_missing"}
        with self._db() as c:
            exists = c.execute("SELECT 1 FROM files WHERE path=?", (path,)).fetchone()
        if not exists:
            self.ingest(path)
        try:
            fields = {k: str(fields.get(k) or "").strip()
                      for k in ("artist", "title", "album", "style")}
        except AttributeError:
            fields = {}
        if any(fields.values()):
            return self._tidy_file(path, override=fields)
        # 无编辑内容：库中有手工标记则沿用库值，否则按文件名重新识别（_tidy_file 内部处理）
        return self._tidy_file(path)

    # -------------------------------------------------------------- tidy all
    def tidy(self, folders=None, files=None):
        """整理未整理/失败的音频。folders 全量整理；files 指定路径增量整理。返回统计。"""
        folders = folders or self.enabled_folders()
        self._start_task("tidy", folders)
        self.log("info", f"开始整理 {len(folders)} 个目录" + self.scope_note(folders))
        stats = {"total": 0, "ok": 0, "fail": 0, "skipped": 0}
        try:
            paths = []
            if files:
                for p in files:
                    if is_audio_file(p) and os.path.exists(p):
                        paths.append(p)
            else:
                # 先一次拉出待整理行、再按路径前缀筛范围（见 under_path 的说明）：
                # 改成每个目录一条 LIKE 查询，除了通配符那三笔账，还会把「选 3 个目录」
                # 变成 3 次全表扫描，一次取回在内存里筛反而更快。
                with self._db() as c:
                    # 开启分类移动时，重新处理全部文件以应用新的目录结构
                    cond = "tidied IN (0,1,2)" if self._config.get("enable_classify") else "tidied IN (0,2)"
                    rows = c.execute(f"SELECT path,folder FROM files WHERE {cond}").fetchall()
                for r in rows:
                    if not any(under_path(r["folder"] or r["path"], root) for root in folders):
                        continue
                    if os.path.exists(r["path"]):
                        paths.append(r["path"])
            stats["total"] = len(paths)
            self._tick(total=len(paths))
            stopped = False
            diag = {"parse_fail": 0, "search_empty": 0, "no_media": 0, "err": 0,
                    "lyric_miss": 0, "cover_miss": 0}
            first_reasons = []
            for i, p in enumerate(paths):
                if not self._check_ctrl():
                    stopped = True
                    break
                r = self._tidy_file(p)
                if r["ok"]:
                    stats["ok"] += 1
                else:
                    stats["fail"] += 1
                    reason = r.get("reason") or "exception"
                    if reason.startswith("search_empty"):
                        diag["search_empty"] += 1
                    elif reason == "parse_fail":
                        diag["parse_fail"] += 1
                    elif reason == "matched_but_no_media":
                        diag["no_media"] += 1
                    else:
                        diag["err"] += 1
                    if len(first_reasons) < 3:
                        first_reasons.append(f"{os.path.basename(p)} -> {reason}")
                if not r.get("lyric"):
                    diag["lyric_miss"] += 1
                if not r.get("cover"):
                    diag["cover_miss"] += 1
                self._tick(done=i + 1, ok=stats["ok"], fail=stats["fail"], current=os.path.basename(p))
            # 诊断摘要：一眼看出失败集中在哪个环节（解析 / 搜索 / 媒体）
            self.log("info",
                     f"整理{'已停止' if stopped else '完成'}: 成功{stats['ok']} 失败{stats['fail']} · "
                     f"缺歌词{diag['lyric_miss']} 缺封面{diag['cover_miss']} · "
                     f"解析失败{diag['parse_fail']} 搜索无结果{diag['search_empty']} 匹配但无媒体{diag['no_media']} 异常{diag['err']}")
            if first_reasons:
                self.log("warn", "失败样例: " + " | ".join(first_reasons))
            if stopped:
                self.log("info", "已完成的文件不会重复处理，再次点击「开始整理」即从断点继续")
        except Exception as e:
            self.log("error", f"整理异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        # 整理完成后自动检测重复封面
        try:
            dup = self.duplicate_covers(threshold=3)
            if dup["groups"]:
                total_dup = sum(g["count"] for g in dup["groups"])
                self.log("warn",
                         f"检测到重复封面：{len(dup['groups'])} 组共 {total_dup} 首歌共用同一张图，"
                         f"可在「重复封面」页面一键修复")
        except Exception:
            pass  # 检测失败不影响整理结果
        self._finish_task()
        return stats

    # --------------------------------------------------------------- dedupe
    def duplicates(self, kind: str = "both"):
        """综合重复检查。

        - content: SHA-1 相同（文件内容完全一致，确定重复）
        - name:    歌手+歌名相同但内容不同（同一首歌的不同格式/码率副本，
                   如 flac 与 320k mp3 各存一份；建议保留无损/高码率）
        kind: both | content | name
        """
        with self._db() as c:
            rows = [dict(r) for r in c.execute(
                "SELECT path,size,sha1,artist,title,tidied,name FROM files WHERE sha1<>'' ORDER BY size DESC").fetchall()]
        out = []
        # 1) 内容重复
        by_sha = {}
        for r in rows:
            by_sha.setdefault(r["sha1"], []).append(r)
        if kind in ("both", "content"):
            for sha1, items in by_sha.items():
                if len(items) > 1:
                    items.sort(key=lambda x: (-(x.get("size") or 0), -(x.get("tidied") or 0)))
                    out.append({"type": "content", "sha1": sha1, "count": len(items),
                                "total_size": sum(x.get("size") or 0 for x in items), "items": items})
        # 2) 名称重复（同曲不同内容）
        if kind in ("both", "name"):
            by_name = {}
            for r in rows:
                a, t = r.get("artist") or "", r.get("title") or ""
                if not (a and t):  # 未整理的用文件名解析兜底
                    m = self._parse_name(r.get("name") or r["path"])
                    a = a or m.get("artist") or ""
                    t = t or m.get("title") or ""
                if not a or not t:
                    continue
                by_name.setdefault(_artist_norm(a) + "|" + _title_norm(t), []).append(r)
            for key, items in by_name.items():
                # 组内必须存在至少 2 种不同内容才算名称重复（纯内容重复归 content 组）
                if len(items) < 2 or len({it["sha1"] for it in items}) < 2:
                    continue
                items.sort(key=self._quality_key, reverse=True)
                out.append({"type": "name", "key": key, "count": len(items),
                            "total_size": sum(x.get("size") or 0 for x in items), "items": items})
        order = {"content": 0, "name": 1}
        out.sort(key=lambda g: (order[g["type"]], -g["total_size"]))
        return out

    _LOSSLESS_EXTS = (".flac", ".wav", ".ape", ".aiff", ".aif", ".dsf")

    def _quality_key(self, item: dict):
        """名称重复组的保留优先级：无损 > 已整理 > 大文件（通常高码率）。"""
        ext = os.path.splitext(item["path"])[1].lower()
        return (1 if ext in self._LOSSLESS_EXTS else 0,
                item.get("tidied") or 0,
                item.get("size") or 0)

    def resolve_duplicates(self, keep_path: str, delete_paths: list, mode: str = "trash"):
        """处理重复：保留 keep_path，其余按 mode(trash/delete) 处理。"""
        results = []
        trash_dir = os.path.join(self.data_dir, self._config.get("trash_dir", ".tidy_trash"))
        for p in delete_paths:
            if not p or p == keep_path:
                continue
            try:
                if mode == "delete":
                    os.remove(p)
                else:
                    os.makedirs(trash_dir, exist_ok=True)
                    shutil.move(p, os.path.join(trash_dir, os.path.basename(p)))
                with self._db() as c:
                    c.execute("DELETE FROM files WHERE path=?", (p,))
                results.append({"path": p, "ok": True})
            except Exception as e:
                results.append({"path": p, "ok": False, "err": str(e)})
        return results

    def _probe(self, path: str) -> dict:
        """尽力获取时长/码率（ffprobe 可用时），失败返回空 dict。"""
        if not shutil.which("ffprobe"):
            return {}
        try:
            r = subprocess.run(
                ["ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", path],
                capture_output=True, timeout=10)
            info = json.loads(r.stdout.decode("utf-8", "replace") or "{}").get("format") or {}
            out = {}
            if info.get("duration"):
                out["duration"] = f"{int(float(info['duration']) // 60)}:{float(info['duration']) % 60:04.1f}"
            if info.get("bit_rate"):
                out["bitrate"] = str(round(int(info["bit_rate"]) / 1000))
            return out
        except Exception:
            return {}

    def dedupe_all(self, mode: str = "move", out_dir: str = "", ai: bool = False, kind: str = "both") -> dict:
        """一键处理全部重复组。

        mode: delete=直接删除 | move=移入项目外目录 out_dir | trash=移入回收站
        ai:   True 时逐组调用 LLM 决定保留项（失败回退启发式：内容组大文件优先；名称组无损优先）
        kind: both | content | name —— 综合检查的组类型范围
        """
        groups = self.duplicates(kind)
        if not groups:
            return {"groups": 0, "removed": 0, "kept": [], "errors": []}
        if mode == "move":
            if not out_dir:
                return {"error": "mode=move 必须指定 out_dir（项目外目录）"}
            out_norm = os.path.normcase(os.path.normpath(os.path.abspath(out_dir)))
            for root in self.all_folders():
                root_norm = os.path.normcase(os.path.normpath(os.path.abspath(root)))
                if out_norm == root_norm or out_norm.startswith(root_norm + os.sep):
                    return {"error": f"移出目录 {out_dir} 不能位于音乐库目录内"}
            try:
                os.makedirs(out_dir, exist_ok=True)
            except Exception as e:
                return {"error": f"无法创建移出目录: {e}"}
        removed = 0
        kept = []
        errors = []
        for gi, g in enumerate(groups):
            items = g["items"]
            keep_path = None
            if ai and self.llm and self.llm.available:
                cand = []
                for i, it in enumerate(items):
                    pr = self._probe(it["path"])
                    cand.append({
                        "index": i, "path": it["path"], "size": it.get("size"),
                        "tidied": it.get("tidied"), "ext": os.path.splitext(it["path"])[1].lower(),
                        "duration": pr.get("duration"), "bitrate": pr.get("bitrate"),
                    })
                decision = self.llm.dedupe_decide(cand, gtype=g.get("type", "content"))
                if decision:
                    keep_path = items[decision["keep_index"]]["path"]
                    kept.append({"group": gi + 1, "type": g.get("type", "content"),
                                 "keep": keep_path, "reason": decision["reason"], "by": "ai"})
                else:
                    kept.append({"group": gi + 1, "type": g.get("type", "content"),
                                 "keep": items[0]["path"], "reason": "AI 判断失败，按启发式保留", "by": "heuristic"})
            if not keep_path:
                keep_path = items[0]["path"]  # duplicates() 已按最优排序（内容组：大文件优先；名称组：无损优先）
                if not ai:
                    reason = ("默认保留无损/高码率版本" if g.get("type") == "name" else "默认保留最优项")
                    kept.append({"group": gi + 1, "type": g.get("type", "content"),
                                 "keep": keep_path, "reason": reason, "by": "heuristic"})
            # 混合检查时同一文件可能同时属于内容组与名称组，先处理的组会移走文件；
            # 以磁盘现状为准过滤，keep 已被处理则顺延到组内下一个仍存在的文件
            items_exist = [it for it in items if os.path.exists(it["path"])]
            if len(items_exist) < 2:
                continue
            if keep_path not in [it["path"] for it in items_exist]:
                keep_path = items_exist[0]["path"]
                kept[:] = [k for k in kept if k.get("group") != gi + 1]
                kept.append({"group": gi + 1, "type": g.get("type", "content"),
                             "keep": keep_path, "reason": "原保留项已被前组处理，顺延", "by": "heuristic"})
            deletes = [it["path"] for it in items_exist if it["path"] != keep_path]
            if not deletes:
                continue
            if mode == "move":
                # 移入 out_dir 下按相对音乐库根的结构存放，避免同名冲突
                roots = self.all_folders()
                for p in deletes:
                    d = os.path.dirname(p)
                    rel = ""
                    for root in roots:
                        try:
                            r = os.path.relpath(d, root)
                        except ValueError:  # 跨盘符等
                            continue
                        if not r.startswith(".."):
                            rel = "" if r == "." else r
                            break
                    else:
                        rel = os.path.basename(d)
                    dst_dir = os.path.join(out_dir, rel) if rel else out_dir
                    try:
                        os.makedirs(dst_dir, exist_ok=True)
                        dst = os.path.join(dst_dir, os.path.basename(p))
                        if os.path.exists(dst):
                            stem, ext = os.path.splitext(os.path.basename(p))
                            dst = os.path.join(dst_dir, f"{stem}.dup{int(time.time())}{ext}")
                        shutil.move(p, dst)
                        with self._db() as c:
                            c.execute("DELETE FROM files WHERE path=?", (p,))
                        removed += 1
                    except Exception as e:
                        errors.append({"path": p, "err": str(e)})
            else:
                res = self.resolve_duplicates(keep_path, deletes, mode="delete" if mode == "delete" else "trash")
                removed += sum(1 for x in res if x["ok"])
                errors += [x for x in res if not x["ok"]]
            self.log("info", f"去重组{gi + 1}: 保留 {os.path.basename(keep_path)}，处理 {len(deletes)} 份（{mode}{'+AI' if ai else ''}）")
        self.log("info", f"一键去重完成: {len(groups)} 组，移除 {removed} 个文件" + (f"，失败 {len(errors)}" if errors else ""))
        return {"groups": len(groups), "removed": removed, "kept": kept, "errors": errors}

    def ai_complete(self, path: str) -> dict:
        """AI 补全单个文件的缺失素材：识别元数据 -> 刮削歌词/封面 -> 歌词缺失时 LLM 生成。"""
        res = {"path": path, "ok": False, "artist": "", "title": "",
               "lyric": False, "cover": False, "meta": False, "lyric_source": "", "error": ""}
        if not (self.llm and self.llm.available):
            res["error"] = "LLM 未启用或未配置，请先在设置页配置并测试连接"
            return res
        if not os.path.exists(path):
            res["error"] = "文件不存在"
            return res
        try:
            # 1) 元数据：优先正则（含代码命名消歧），缺失/歧义时用 LLM 识别
            meta = self._parse_name(path)
            if self._llm_should_rescue(meta):
                rescued = self._llm_rescue(path, meta)
                if rescued:
                    meta = rescued
                    res["meta"] = True
            if not meta.get("title"):
                res["error"] = "无法识别歌曲信息（文件名解析与 AI 识别均失败）"
                return res
            res["artist"] = meta.get("artist", "")
            res["title"] = meta.get("title", "")

            # 2) 曲库刮削歌词/封面：仅采用置信匹配（宁缺毋滥，防止张冠李戴）
            lyric = ""
            cover_url = ""
            song = {}
            _a, _t, song = self._resolve_meta(path, meta)
            if song:
                cover_url = song.get("album_pic", "")
                lyric = fetch_lyric(song)
                if lyric_garbled(lyric):
                    lyric = ""
                if lyric:
                    res["lyric_source"] = "scraper"

            # 3) 歌词仍缺（或已有但乱码）-> LLM 生成带时间码 LRC
            lrc_path = os.path.splitext(path)[0] + ".lrc"
            exist_ok = False
            if os.path.exists(lrc_path):
                try:
                    with open(lrc_path, "r", encoding="utf-8", errors="replace") as f:
                        exist_ok = not lyric_garbled(f.read())
                except Exception:
                    exist_ok = False
            if not lyric and not exist_ok:
                lyric = self.llm.generate_lyric(res["artist"], res["title"], meta.get("style", ""))
                if lyric:
                    res["lyric_source"] = "llm"

            # 4) 写 sidecar（歌词/封面均只在缺失时写入）
            side = self._write_sidecar(path, lyric, cover_url)
            # 曲库无封面 -> 按开关尝试 AI 文生图兜底（画面贴合歌词大意与风格，含歌名标题）
            if not side["cover"]:
                if self._maybe_ai_cover(path, res["artist"], res["title"], lyric, meta.get("style", "")):
                    side["cover"] = True
            res["lyric"] = side["lyric"]
            res["cover"] = side["cover"]

            album = song.get("album", "") if song else ""
            code = meta.get("code", "") or parse_tidy_code(os.path.basename(path)).get("code", "")
            with self._db() as c:
                c.execute(
                    "UPDATE files SET artist=?,title=?,album=?,style=?,code=?,has_lyric=?,has_cover=?,tidied=1,tidied_at=?,err='' WHERE path=?",
                    (res["artist"], res["title"], album, meta.get("style", ""), code,
                     1 if res["lyric"] else 0, 1 if res["cover"] else 0, _now(), path))
            res["ok"] = bool(res["lyric"] or res["cover"] or res["meta"])
            if not res["ok"]:
                res["error"] = "曲库无匹配且 AI 生成歌词失败"
            self.log("info", f"AI 补全 {os.path.basename(path)}: 歌词{'✓' if res['lyric'] else '✗'}({res['lyric_source'] or '-'}) 封面{'✓' if res['cover'] else '✗'}")
            return res
        except Exception as e:
            res["error"] = str(e)[:200]
            self.log("error", f"AI 补全失败 {path}: {e}")
            return res

    def ai_complete_task(self, files=None):
        """后台任务：批量 AI 补全（files 为空时处理全部缺失素材的文件）。"""
        if self._task.get("running"):
            return {"error": "已有任务在运行"}
        if not (self.llm and self.llm.available):
            return {"error": "LLM 未启用或未配置，请先在设置页配置并测试连接"}
        if files:
            paths = [p for p in files if os.path.exists(p)]
        else:
            paths = [r["path"] for r in self.missing_files(limit=100000) if os.path.exists(r["path"])]
        self._start_task("ai")
        self.log("info", f"开始 AI 补全 {len(paths)} 个文件")
        stats = {"total": len(paths), "ok": 0, "fail": 0}
        try:
            self._tick(total=len(paths))
            stopped = False
            for i, p in enumerate(paths):
                if not self._check_ctrl():
                    stopped = True
                    break
                r = self.ai_complete(p)
                if r["ok"]:
                    stats["ok"] += 1
                else:
                    stats["fail"] += 1
                self._tick(done=i + 1, ok=stats["ok"], fail=stats["fail"], current=os.path.basename(p))
            self.log("info", f"AI 补全{'已停止' if stopped else '完成'}: 成功{stats['ok']} 失败{stats['fail']}"
                     + ("；未完成的文件再次点击即可从断点继续" if stopped else ""))
        except Exception as e:
            self.log("error", f"AI 补全异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    # ------------------------------------------------------ duplicate cover
    def duplicate_covers(self, threshold: int = 3) -> dict:
        """检测「大量相同封面」：同一份封面图片内容（SHA-1 一致）被 >=threshold 个
        不同目录的歌曲共用，通常是刮削匹配错误导致多首歌拿到同一张图。

        同一专辑目录内同歌手多首歌共用封面属正常；但「不同目录数」或「不同歌手数」
        达到阈值都判定为可疑（同目录不同歌手共用一张图同样是刮削错误）。
        仅统计逐曲同名封面（song.jpg），目录级 cover.jpg 不纳入。
        """
        threshold = max(2, int(threshold or 3))
        with self._db() as c:
            rows = c.execute("SELECT path,folder,artist,title FROM files WHERE has_cover=1").fetchall()
        by_hash = {}
        for r in rows:
            p = r["path"]
            stem = os.path.splitext(p)[0]
            cp = ""
            for cand in (stem + ".jpg", stem + ".jpeg", stem + ".png"):
                if os.path.exists(cand):
                    cp = cand
                    break
            if not cp:
                continue
            h = file_sha1(cp)
            if not h:
                continue
            e = by_hash.setdefault(h, {"cover": cp, "size": file_size(cp), "items": []})
            e["items"].append({"song": p, "cover": cp, "folder": r["folder"] or os.path.dirname(p),
                               "artist": r["artist"] or "", "title": r["title"] or ""})
        groups = []
        for h, info in by_hash.items():
            folders = {}
            artists = set()
            for it in info["items"]:
                folders.setdefault(it["folder"], []).append(it)
                if it["artist"]:
                    artists.add(it["artist"])
            if len(folders) < threshold and len(artists) < threshold:
                continue
            groups.append({
                "sha1": h,
                "cover": info["cover"],
                "size": info["size"],
                "count": len(info["items"]),
                "folders": len(folders),
                "artists": len(artists),
                "songs": [it["song"] for it in info["items"]],
                "covers": {it["song"]: it["cover"] for it in info["items"]},
                "sample": [{"path": it["song"], "artist": it["artist"], "title": it["title"]}
                           for it in info["items"][:8]],
            })
        groups.sort(key=lambda g: (g["folders"], g["count"]), reverse=True)
        return {"threshold": threshold, "groups": groups}

    def _refetch_cover(self, song: str, cover_path: str, exclude_sha1: str = "") -> dict:
        """按歌手/歌名重新刮削封面并替换 cover_path。返回 {ok, artist, title, error}。"""
        out = {"ok": False, "artist": "", "title": "", "error": ""}
        with self._db() as c:
            row = c.execute("SELECT artist,title,name FROM files WHERE path=?", (song,)).fetchone()
        artist = (row["artist"] if row else "") or ""
        title = (row["title"] if row else "") or ""
        name = (row["name"] if row else "") or os.path.basename(song)
        if not artist or not title:
            meta = self._parse_name(name)
            artist = artist or meta.get("artist", "")
            title = title or meta.get("title", "")
        if (not artist or not title) and self._llm_should_rescue({"artist": artist, "title": title}):
            lm = self._llm_rescue(name, {"artist": artist, "title": title,
                                         "left": "", "right": ""})
            artist = artist or lm.get("artist", "")
            title = title or lm.get("title", "")
        out["artist"], out["title"] = artist, title
        # 回填识别到的元数据（仅补空字段）
        if artist or title:
            try:
                with self._db() as c:
                    c.execute(
                        "UPDATE files SET artist=COALESCE(NULLIF(artist,''),?), "
                        "title=COALESCE(NULLIF(title,''),?) WHERE path=?",
                        (artist, title, song))
            except Exception:
                pass
        if not title:
            out["error"] = "无法识别歌名（文件名解析与 AI 均失败）"
            return out
        # 用消歧后的置信匹配结果（低置信不采用，防止又拿到同一张错图）
        _a, _t, song_info = self._resolve_meta(song, {"artist": artist, "title": title,
                                                      "left": "", "right": ""})
        cover_url = song_info.get("album_pic", "") if song_info else ""
        if not cover_url:
            # 曲库无置信匹配 -> 按开关尝试 AI 文生图（生成图必然不同于旧图）
            if self._maybe_ai_cover(song, artist, title, style=parse_tidy_code(name).get("style_label", "")):
                out["ok"] = True
                return out
            out["error"] = "曲库无置信匹配，跳过（不采用低置信结果）"
            return out
        tmp = cover_path + ".tmp"
        if not download_cover(cover_url, tmp):
            out["error"] = "封面下载失败"
            return out
        new_sha = file_sha1(tmp)
        if new_sha and new_sha == exclude_sha1:
            try:
                os.remove(tmp)
            except Exception:
                pass
            # 仍是同一张 -> 按开关用 AI 生成一张全新封面兜底
            if self._maybe_ai_cover(song, artist, title, style=parse_tidy_code(name).get("style_label", "")):
                out["ok"] = True
                return out
            out["error"] = "重新获取的封面仍是同一张（该曲可能确无独立封面）"
            return out
        try:
            os.replace(tmp, cover_path)
        except Exception as e:
            try:
                os.remove(tmp)
            except Exception:
                pass
            out["error"] = f"写入封面失败: {e}"
            return out
        out["ok"] = True
        return out

    def fix_duplicate_covers(self, threshold: int = 3) -> dict:
        """后台任务：对检测出的大量相同封面逐首 AI 重新刮削替换。"""
        if self._task.get("running"):
            return {"error": "已有任务在运行"}
        det = self.duplicate_covers(threshold)
        groups = det["groups"]
        if not groups:
            return {"error": "未检测到大量相同的封面"}
        todo = []
        seen = set()
        for g in groups:
            for s in g["songs"]:
                if s not in seen:
                    seen.add(s)
                    todo.append((s, g["covers"].get(s, ""), g["sha1"]))
        self._start_task("cover")
        self.log("info", f"开始 AI 修复封面：{len(groups)} 组相同封面，待处理 {len(todo)} 首")
        stats = {"total": len(todo), "ok": 0, "fail": 0, "groups": len(groups)}
        try:
            self._tick(total=len(todo))
            stopped = False
            for i, (song, cover, sha) in enumerate(todo):
                if not self._check_ctrl():
                    stopped = True
                    break
                r = self._refetch_cover(song, cover, exclude_sha1=sha)
                if r["ok"]:
                    stats["ok"] += 1
                else:
                    stats["fail"] += 1
                self._tick(done=i + 1, ok=stats["ok"], fail=stats["fail"], current=os.path.basename(song))
                self.log("info", f"封面修复 {os.path.basename(song)}: " + ("✓ 已更换" if r["ok"] else "✗ " + r["error"]))
            self.log("info", f"AI 修复封面{'已停止' if stopped else '完成'}: 成功{stats['ok']} 失败{stats['fail']}")
        except Exception as e:
            self.log("error", f"AI 修复封面异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    def missing_files(self, limit: int = 500):
        """缺歌词或缺封面的文件列表（供音乐库筛选）。"""
        with self._db() as c:
            rows = c.execute(
                "SELECT * FROM files WHERE has_lyric=0 OR has_cover=0 ORDER BY id DESC LIMIT ?",
                (int(limit),)).fetchall()
        return [dict(r) for r in rows]

    # -------------------------------------------------------------- library
    def library(self, folder: str = "", search: str = "", tidied: str = "", limit: int = 100, offset: int = 0, missing: str = "", code: str = ""):
        where, args = [], []
        if folder:
            where.append("folder LIKE ?")
            args.append(folder + "%")
        if search:
            where.append("(name LIKE ? OR artist LIKE ? OR title LIKE ?)")
            like = f"%{search}%"
            args += [like, like, like]
        if tidied in ("0", "1", "2"):
            where.append("tidied=?")
            args.append(int(tidied))
        if missing == "lyric":
            where.append("has_lyric=0")
        elif missing == "cover":
            where.append("has_cover=0")
        elif missing == "any":
            where.append("(has_lyric=0 OR has_cover=0)")
        if code:
            # code 形如 S03 / E03 / C04 / Y3 / V00，匹配全维度代码列
            where.append("code LIKE ?")
            args.append("%" + code.upper() + "%")
        sql_where = ("WHERE " + " AND ".join(where)) if where else ""
        fav_col = ("(SELECT COUNT(*) FROM playlist_items i JOIN playlists p ON p.id=i.pid"
                   " WHERE p.fav=1 AND i.path=files.path)>0 AS fav")
        with self._db() as c:
            total = c.execute(f"SELECT COUNT(*) n FROM files {sql_where}", args).fetchone()["n"]
            rows = c.execute(
                f"SELECT *, {fav_col} FROM files {sql_where} ORDER BY id DESC LIMIT ? OFFSET ?",
                args + [int(limit), int(offset)]).fetchall()
        return {"total": total, "items": [dict(r) for r in rows]}

    def code_stats(self) -> dict:
        """统计带全维度代码的歌曲各维度分布，供前端筛选下拉与歌单预览。"""
        with self._db() as c:
            rows = c.execute("SELECT code FROM files WHERE code<>''").fetchall()
        dims = {"style": {}, "emotion": {}, "scene1": {}, "year": {}, "version": {}}
        total = 0
        for r in rows:
            cd = parse_tidy_code("[" + r["code"] + "]")
            if not cd:
                continue
            total += 1
            dims["style"][cd["style"]] = dims["style"].get(cd["style"], 0) + 1
            dims["emotion"][cd["emotion"]] = dims["emotion"].get(cd["emotion"], 0) + 1
            dims["scene1"][cd["scene1"]] = dims["scene1"].get(cd["scene1"], 0) + 1
            dims["year"][cd["year"]] = dims["year"].get(cd["year"], 0) + 1
            dims["version"][cd["version"]] = dims["version"].get(cd["version"], 0) + 1
        def _label(kind, k):
            return {"style": STYLE_MAP, "emotion": EMOTION_MAP,
                    "scene1": SCENE_MAP, "version": VERSION_MAP}.get(kind, {}).get(k, k)
        out = {"total": total}
        for kind, m in dims.items():
            out[kind] = sorted([{"code": k, "label": _label(kind, k), "count": v}
                                for k, v in m.items()], key=lambda x: -x["count"])
        return out

    def export_code_playlists(self, out_dir: str) -> dict:
        """按全维度代码生成分类 m3u 歌单（风格/情绪/场景/年份/版本）。"""
        if not out_dir:
            return {"error": "请指定歌单输出目录"}
        if out_dir in self.all_folders():
            return {"error": "歌单目录不能是音乐库目录本身"}
        with self._db() as c:
            rows = c.execute(
                "SELECT path,artist,title,code FROM files WHERE code<>'' ORDER BY artist,title").fetchall()
        if not rows:
            return {"error": "没有带全维度代码的歌曲"}
        try:
            os.makedirs(out_dir, exist_ok=True)
        except Exception as e:
            return {"error": f"无法创建目录: {e}"}
        buckets = {}  # (kind, code) -> [song]
        for r in rows:
            cd = parse_tidy_code("[" + r["code"] + "]")
            if not cd:
                continue
            song = {"path": r["path"], "artist": r["artist"] or "", "title": r["title"] or ""}
            for kind in ("style", "emotion", "scene1", "year", "version"):
                buckets.setdefault((kind, cd[kind]), []).append(song)
        made = []
        label_map = {"style": STYLE_MAP, "emotion": EMOTION_MAP, "scene1": SCENE_MAP,
                     "version": VERSION_MAP}
        year_lbl = lambda k: parse_tidy_code("[" + "-".join([k, "S01", "E01", "C01", "C01", "V00"]) + "]")["year_label"]
        for (kind, k), songs in sorted(buckets.items()):
            label = label_map.get(kind, {}).get(k, k) if kind != "year" else year_lbl(k)
            fname = _sanitize_dir(f"{label}（{len(songs)}首）") + ".m3u8"
            fp = os.path.join(out_dir, fname)
            try:
                with open(fp, "w", encoding="utf-8") as f:
                    f.write("#EXTM3U\n")
                    for s in songs:
                        f.write(f"#EXTINF:-1,{s['artist']} - {s['title']}\n{s['path']}\n")
                made.append({"file": fname, "count": len(songs)})
            except Exception as e:
                self.log("warn", f"写歌单失败 {fp}: {e}")
        return {"ok": True, "out_dir": out_dir, "playlists": made}

    def recent_logs(self, limit: int = 200):
        with self._db() as c:
            rows = c.execute("SELECT id,ts,level,msg FROM task_log ORDER BY id DESC LIMIT ?",
                             (int(limit),)).fetchall()
        return [dict(r) for r in rows]

    def local_files(self):
        """全量本地文件列表，供歌单匹配使用。"""
        with self._db() as c:
            rows = c.execute("SELECT path,name,title,artist,album FROM files").fetchall()
        out = []
        for r in rows:
            title = r["title"] or ""
            artist = r["artist"] or ""
            if not title or not artist:
                parsed = self._parse_name(r["name"] or "")
                if not title:
                    title = parsed.get("title") or ""
                if not artist:
                    artist = parsed.get("artist") or ""
            out.append({"path": r["path"], "title": title, "artist": artist, "album": r["album"] or ""})
        return out

    def ingest(self, path: str) -> bool:
        """单文件入库（新增或标记为需重整理）。返回 True 表示新增/内容变化。"""
        if not is_audio_file(path) or not os.path.exists(path):
            return False
        try:
            st = os.stat(path)
            sha1 = file_sha1(path)
            stem = os.path.splitext(path)[0]
            has_lrc = 1 if os.path.exists(stem + ".lrc") else 0
            has_cov = 1 if any(os.path.exists(stem + s) for s in (".jpg", ".jpeg", ".png")) else 0
            code = parse_tidy_code(os.path.basename(path)).get("code", "")
            with self._db() as c:
                row = c.execute("SELECT id,size,mtime,sha1 FROM files WHERE path=?", (path,)).fetchone()
                if row is None:
                    c.execute(
                        "INSERT INTO files(path,folder,name,size,mtime,sha1,has_lyric,has_cover,code) VALUES(?,?,?,?,?,?,?,?,?) "
                        "ON CONFLICT(path) DO NOTHING",
                        (path, os.path.dirname(path), os.path.basename(path), st.st_size, st.st_mtime, sha1, has_lrc, has_cov, code))
                    return True
                if row["size"] != st.st_size or row["mtime"] != st.st_mtime or row["sha1"] != sha1:
                    c.execute("UPDATE files SET size=?,mtime=?,sha1=?,tidied=0 WHERE id=?",
                              (st.st_size, st.st_mtime, sha1, row["id"]))
                    return True
            return False
        except UnicodeEncodeError:
            # 非 UTF-8 文件名：sqlite3 绑不了含 surrogate 的路径。旧实现一律
            # except: return False，这类歌仍永远入不了库且毫无线索。
            self.log("warn", f"入库失败（文件名编码异常，建议重命名）: {_safe_text(path)}")
            return False
        except Exception as e:
            self.log("warn", f"入库失败 {os.path.basename(path)}: {e}")
            return False

    def ingest_many(self, paths):
        """批量入库，返回需整理的新增/变化文件列表。"""
        need = []
        for p in paths:
            if self.ingest(p):
                need.append(p)
        return need

    def drop_missing(self, paths):
        """把已不存在的路径从库中移除。"""
        for p in paths:
            try:
                with self._db() as c:
                    c.execute("DELETE FROM files WHERE path=?", (p,))
            except Exception:
                pass

    # -------------------------------------------------------- library ops
    _SIDECAR_SUFFIXES = (".lrc", ".jpg", ".jpeg", ".png")

    def analyze_song(self, path: str, user_prompt: str = "") -> dict:
        """AI 完整分析单曲（不修改歌手/歌名）：核验歌词与歌手、歌名是否匹配。

        匹配 -> 从曲库重新刮取匹配歌词覆盖（刮不到保持现状）；
        不匹配或缺失 -> 优先联网刮取曲库歌词，其次 AI 生成带时间码 LRC 覆盖。
        user_prompt 是用户手写的提示词，非空时在「判定」与「AI 生成歌词」两步都占最高优先级。
        """
        user_prompt = str(user_prompt or "").strip()[:2000]
        res = {"ok": False, "path": path, "artist": "", "title": "", "matched": False,
               "confidence": 0, "reason": "", "actual": "", "action": "",
               "source": "", "error": "", "prompt_used": bool(user_prompt)}
        if not (self.llm and self.llm.available):
            res["error"] = "LLM 未启用或未配置，请先在设置页配置并测试连接"
            return res
        if not os.path.exists(path):
            res["error"] = "文件不存在"
            return res
        with self._db() as c:
            row = c.execute("SELECT artist,title,style FROM files WHERE path=?", (path,)).fetchone()
        artist = (row["artist"] if row and row["artist"] else "") or ""
        title = (row["title"] if row and row["title"] else "") or ""
        style = (row["style"] if row and row["style"] else "") or ""
        if not title:
            title = self._parse_name(path).get("title", "") or os.path.splitext(os.path.basename(path))[0]
        res["artist"], res["title"] = artist, title

        # 1) 读现有歌词（乱码视为无）
        lrc_path = os.path.splitext(path)[0] + ".lrc"
        exist = ""
        if os.path.exists(lrc_path):
            try:
                with open(lrc_path, "r", encoding="utf-8", errors="replace") as f:
                    exist = f.read()
                if lyric_garbled(exist):
                    exist = ""
                    res["reason"] = "现有歌词文件疑似乱码，按缺失处理。"
            except Exception:
                exist = ""

        # 2) LLM 匹配判定（结合模型知识/联网检索）
        if exist.strip():
            j = self.llm.analyze_song(artist, title, exist, user_prompt)
            if not j:
                res["reason"] = (res["reason"] + "AI 判定失败，按不匹配处理。").strip()
                matched = False
            else:
                matched = bool(j.get("match"))
                res.update(confidence=j.get("confidence", 0), reason=j.get("reason", ""),
                           actual=j.get("actual", ""))
        else:
            matched = False
            if not res["reason"]:
                res["reason"] = "歌词文件不存在。"
        res["matched"] = matched

        # 3) 曲库联网刮取（互联网检索能力）
        scraped = ""
        try:
            _a, _t, song = self._resolve_meta(path, {"artist": artist, "title": title})
            if song:
                scraped = fetch_lyric(song)
                if lyric_garbled(scraped):
                    scraped = ""
        except Exception as e:
            self.log("warn", f"分析时曲库刮取失败 {os.path.basename(path)}: {e}")

        # 4) 决策与写盘
        new_lyric = ""
        if matched:
            if scraped:
                new_lyric = scraped
                res["action"] = "歌词匹配，已从曲库重新刮取匹配歌词"
                res["source"] = "scraper"
            else:
                res["action"] = "歌词匹配，曲库暂无更优版本，保持现状"
                res["ok"] = True
        elif scraped:
            new_lyric = scraped
            res["action"] = "歌词不匹配/缺失，已从曲库刮取正确歌词"
            res["source"] = "scraper"
        else:
            gen = self.llm.generate_lyric(artist, title, style, user_prompt)
            if gen:
                new_lyric = gen
                res["action"] = "歌词不匹配/缺失，曲库无果，已 AI 生成歌词"
                res["source"] = "llm"
            else:
                res["error"] = "歌词不匹配/缺失，且曲库无果、AI 生成失败"
        if new_lyric:
            try:
                with open(lrc_path, "w", encoding="utf-8") as f:
                    f.write(new_lyric if new_lyric.endswith("\n") else new_lyric + "\n")
                res["ok"] = True
                self._touch_for_rescan(path)  # 让飞牛增量扫描感知歌词变化
                with self._db() as c:
                    c.execute("UPDATE files SET has_lyric=1 WHERE path=?", (path,))
            except Exception as e:
                res["ok"] = False
                res["error"] = f"写歌词文件失败: {e}"
        self.log("info", f"AI 完整分析 {os.path.basename(path)}"
               + ("（按自定义提示词）" if user_prompt else "") + ": "
               f"{'匹配' if matched else '不匹配'} -> {res['action'] or res['error']}")
        return res

    def library_delete(self, paths) -> dict:
        """删除库中注册过的文件：本地音频 + 同名附属文件（歌词/封面）一并物理删除。

        仅接受 files 表中已注册的路径（白名单），防止通过接口删除任意本地文件。
        物理文件全部清理成功后才删除库记录；失败的路径计入 failed 返回。
        """
        deleted, failed = 0, []
        for p in dict.fromkeys(paths or []):
            if not p:
                continue
            with self._db() as c:
                row = c.execute("SELECT id FROM files WHERE path=?", (p,)).fetchone()
            if row is None:
                failed.append({"path": p, "err": "未入库，已跳过"})
                continue
            errs = []
            stem = os.path.splitext(p)[0]
            if os.path.exists(p):
                try:
                    os.remove(p)
                except Exception as e:
                    errs.append(f"音频: {e}")
            for suf in self._SIDECAR_SUFFIXES:
                sp = stem + suf
                if os.path.exists(sp):
                    try:
                        os.remove(sp)
                    except Exception as e:
                        errs.append(f"{suf}: {e}")
            if errs:
                failed.append({"path": p, "err": "; ".join(errs)})
                continue
            with self._db() as c:
                c.execute("DELETE FROM files WHERE path=?", (p,))
            deleted += 1
        self.log("info", f"音乐库删除: 成功 {deleted} 个（含本地文件），失败 {len(failed)} 个")
        return {"deleted": deleted, "failed": failed}

    def organize_move(self, out_dir: str) -> dict:
        """把已整理（识别出歌手）的文件按「歌手/专辑」格式批量移动到 out_dir 下（后台任务）。

        目录结构与整理时的自动分类一致：out_dir/歌手[/专辑]/文件名，
        同名冲突追加 _1/_2…，附属文件（.lrc/.jpg/.jpeg/.png）随迁，
        移动成功后更新 files 表的 path/folder/name。
        """
        out_root = os.path.abspath(out_dir)
        self._start_task("organize")
        stats = {"total": 0, "moved": 0, "skipped": 0, "fail": 0}
        try:
            with self._db() as c:
                rows = c.execute(
                    "SELECT path,artist,album FROM files WHERE tidied=1 AND artist<>'' ORDER BY id"
                ).fetchall()
            items = [r for r in rows if os.path.exists(r["path"])]
            stats["total"] = len(items)
            self._tick(total=len(items))
            self.log("info", f"开始格式移动：{len(items)} 个已整理文件 -> {out_root}")
            folders = self.all_folders()
            stopped = False
            for i, r in enumerate(items):
                if not self._check_ctrl():
                    stopped = True
                    break
                src = r["path"]
                base = os.path.basename(src)
                target_dir = os.path.join(out_root, _sanitize_dir(r["artist"]))
                if r["album"]:
                    target_dir = os.path.join(target_dir, _sanitize_dir(r["album"]))
                new_path = os.path.join(target_dir, base)
                if os.path.abspath(new_path) == os.path.abspath(src):
                    stats["skipped"] += 1
                    self._tick(done=i + 1, ok=stats["moved"], fail=stats["fail"], current=base)
                    continue
                try:
                    os.makedirs(target_dir, exist_ok=True)
                    if os.path.exists(new_path):
                        stem0, ext0 = os.path.splitext(base)
                        k = 1
                        while os.path.exists(new_path):
                            new_path = os.path.join(target_dir, f"{stem0}_{k}{ext0}")
                            k += 1
                    shutil.move(src, new_path)
                    src_stem = os.path.splitext(src)[0]
                    for suf in self._SIDECAR_SUFFIXES:
                        sp = src_stem + suf
                        if os.path.exists(sp):
                            try:
                                shutil.move(sp, os.path.splitext(new_path)[0] + suf)
                            except Exception as e:
                                self.log("warn", f"附属文件移动失败 {sp}: {e}")
                    new_folder = out_dir
                    abs_new = os.path.abspath(new_path)
                    for f in folders:
                        if abs_new.startswith(os.path.abspath(f) + os.sep):
                            new_folder = f
                            break
                    with self._db() as c:
                        c.execute("UPDATE files SET path=?,folder=?,name=? WHERE path=?",
                                  (new_path, new_folder, os.path.basename(new_path), src))
                    stats["moved"] += 1
                except Exception as e:
                    stats["fail"] += 1
                    self.log("warn", f"格式移动失败 {src}: {e}")
                self._tick(done=i + 1, ok=stats["moved"], fail=stats["fail"], current=base)
            self.log("info",
                     f"格式移动{'已停止' if stopped else '完成'}: 移动 {stats['moved']}，"
                     f"跳过 {stats['skipped']}，失败 {stats['fail']}")
        except Exception as e:
            self.log("error", f"格式移动异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    # ==================================================================
    # 我的喜欢 / 我的歌单 / 歌单归档 / 歌单分享
    # ==================================================================

    FAV_NAME = "我的喜欢"

    def _ensure_fav(self) -> int:
        """取（没有则建）内置「我的喜欢」歌单 id。"""
        with self._db() as c:
            row = c.execute("SELECT id FROM playlists WHERE fav=1").fetchone()
            if row:
                return row["id"]
            c.execute("INSERT INTO playlists(name,fav,created,auto_sync) VALUES(?,1,?,1)",
                      (self.FAV_NAME, _now()))
            return c.execute("SELECT last_insert_rowid() AS id").fetchone()["id"]

    def _playlist_row(self, pid):
        with self._db() as c:
            r = c.execute("SELECT * FROM playlists WHERE id=?", (pid,)).fetchone()
        return dict(r) if r else None

    def playlists(self) -> dict:
        """歌单总览：含条数/在库数/生效归档目录，以及分享列表。"""
        fav = self._ensure_fav()
        with self._db() as c:
            rows = c.execute(
                "SELECT p.*,"
                " (SELECT COUNT(*) FROM playlist_items i WHERE i.pid=p.id) n,"
                " (SELECT COUNT(*) FROM playlist_items i JOIN files f ON f.path=i.path"
                "   WHERE i.pid=p.id) local_n"
                " FROM playlists p ORDER BY p.fav DESC, p.id ASC").fetchall()
            sh = c.execute("SELECT * FROM shares WHERE active=1 ORDER BY created DESC").fetchall()
        out = []
        for r in rows:
            d = dict(r)
            d["pending"] = max(0, d["n"] - d["local_n"])
            root, mode, auto = self._archive_conf(d["id"])
            d["archive_root"] = root
            d["archive_mode"] = mode
            d["auto_sync"] = bool(d.get("auto_sync", 1))
            d["target_dir"] = os.path.join(root, _sanitize_dir(d["name"])) if root else ""
            d["archivable"] = bool(root and auto)
            out.append(d)
        pl = self._config.get("playlist", {}) or {}
        return {"playlists": out, "fav_id": fav,
                "archive_root": pl.get("archive_root", ""),
                "archive_mode": pl.get("archive_mode", "copy"),
                "auto_archive": bool(pl.get("auto_sync", True)),
                "shares": [dict(s) for s in sh]}

    def playlist_create(self, name: str) -> dict:
        name = (name or "").strip()[:40]
        if not name:
            return {"error": "歌单名不能为空"}
        with self._db() as c:
            if c.execute("SELECT 1 FROM playlists WHERE name=?", (name,)).fetchone():
                return {"error": f"已有同名歌单：{name}"}
            c.execute("INSERT INTO playlists(name,fav,created,auto_sync) VALUES(?,0,?,1)",
                      (name, _now()))
            pid = c.execute("SELECT last_insert_rowid() AS id").fetchone()["id"]
        self.log("info", f"新建歌单：{name}")
        return {"ok": True, "id": pid, "name": name}

    def playlist_delete(self, pid: int) -> dict:
        row = self._playlist_row(pid)
        if not row:
            return {"error": "歌单不存在"}
        if row.get("fav"):
            return {"error": "「我的喜欢」不能删除"}
        with self._db() as c:
            c.execute("DELETE FROM playlist_items WHERE pid=?", (pid,))
            c.execute("UPDATE shares SET active=0 WHERE pid=?", (pid,))
            c.execute("DELETE FROM playlists WHERE id=?", (pid,))
        self.log("info", f"删除歌单：{row['name']}")
        return {"ok": True}

    def playlist_update(self, pid: int, name=None, archive_dir=None,
                        archive_mode=None, auto_sync=None) -> dict:
        fields, args = [], []
        if name is not None:
            nm = name.strip()[:40]
            if not nm:
                return {"error": "歌单名不能为空"}
            with self._db() as c:
                dup = c.execute("SELECT 1 FROM playlists WHERE name=? AND id<>?",
                                (nm, pid)).fetchone()
            if dup:
                return {"error": f"已有同名歌单：{nm}"}
            fields.append("name=?")
            args.append(nm)
        if archive_dir is not None:
            fields.append("archive_dir=?")
            args.append(archive_dir.strip())
        if archive_mode is not None:
            fields.append("archive_mode=?")
            args.append(archive_mode if archive_mode in ("move", "copy", "") else "")
        if auto_sync is not None:
            fields.append("auto_sync=?")
            args.append(1 if auto_sync else 0)
        if not fields:
            return {"error": "没有需要保存的修改"}
        args.append(pid)
        with self._db() as c:
            c.execute(f"UPDATE playlists SET {','.join(fields)} WHERE id=?", args)
        return {"ok": True}

    def playlist_items(self, pid: int) -> list:
        with self._db() as c:
            rows = c.execute(
                "SELECT i.*, f.id AS file_id, f.name, f.size, f.has_lyric, f.has_cover,"
                " f.artist AS l_artist, f.title AS l_title, f.album AS l_album"
                " FROM playlist_items i LEFT JOIN files f ON f.path=i.path"
                " WHERE i.pid=? ORDER BY i.id DESC", (pid,)).fetchall()
        return [self._pl_item(dict(r)) for r in rows]

    def _pl_item(self, d: dict) -> dict:
        """补全歌单条目的展示字段。

        取值优先级：加入时的快照 > 库内当前标签 > 条内 song 快照 > 文件名解析。
        未整理的歌 files.title/artist 为空，若不回退文件名，明细区与分享清单
        都会显示成空白歌名，导入方也匹配不上，故此处统一兜底。
        """
        sj = d.get("song")
        if isinstance(sj, str):
            try:
                sj = json.loads(sj or "{}")
            except Exception:
                sj = {}
        sj = sj if isinstance(sj, dict) else {}
        d["song"] = sj                                      # 前端直接用字典
        path = str(d.get("path") or "")
        pend = path.startswith("pending://")
        name = d.get("name") or ("" if pend else os.path.basename(path))
        artist = d.get("artist") or ""
        title = d.get("title") or ""
        album = d.get("album") or ""
        if not artist:
            artist = d.get("l_artist") or sj.get("artist") or ""
        if not title:
            title = d.get("l_title") or sj.get("name") or ""
        if not album:
            album = d.get("l_album") or sj.get("album") or ""
        if not title and name:
            p = self._parse_name(name)
            title = p.get("title") or ""
            artist = artist or p.get("artist") or ""
        d["name"] = name
        d["artist"] = artist
        d["title"] = title or name
        d["album"] = album
        d["pending"] = pend
        d["local"] = bool(d.get("file_id"))
        if not d.get("duration"):
            d["duration"] = sj.get("duration") or 0
        d["fetchable"] = bool(sj.get("remote_url"))     # 分享人本地有，可直接补下
        return d

    def favorite_paths(self) -> list:
        """已在「我的喜欢」里的本地路径（供列表/播放器渲染红心状态）。"""
        pid = self._ensure_fav()
        with self._db() as c:
            rows = c.execute("SELECT path FROM playlist_items WHERE pid=?", (pid,)).fetchall()
        return [r["path"] for r in rows]

    def playlist_add(self, pid: int, path: str = "", song: dict = None) -> dict:
        """把在库文件（path）或网络搜索结果（song，尚未下载）加入歌单/我的喜欢。"""
        pid = int(pid or 0)
        if not pid:
            return {"error": "缺少歌单 id"}
        row = self._playlist_row(pid)
        if not row:
            return {"error": "歌单不存在"}
        dur, sj = 0, ""
        if path:
            with self._db() as c:
                f = c.execute("SELECT path,name,artist,title,album FROM files WHERE path=?",
                              (path,)).fetchone()
            if not f:
                return {"error": "该文件不在音乐库中"}
            key, artist, title, album = f["path"], f["artist"], f["title"], f["album"]
            if not title or not artist:                     # 未整理的歌快照也要带上名字
                p = self._parse_name(f["name"] or os.path.basename(key))
                artist = artist or p.get("artist") or ""
                title = title or p.get("title") or os.path.basename(key)
        elif song:
            artist = song.get("artist", "") or ""
            title = song.get("name", "") or ""
            album = song.get("album", "") or ""
            dur = int(song.get("duration") or 0)
            key = "pending://%s/%s" % (song.get("source", ""), song.get("id", ""))
            sj = json.dumps(song, ensure_ascii=False)[:4000]
        else:
            return {"error": "缺少歌曲信息"}
        with self._db() as c:
            # 仅在确有歌名时按「歌手+歌名」判重，否则两首都没名字的歌会被误判重复
            dup = c.execute(
                "SELECT id FROM playlist_items WHERE pid=? AND (path=? OR (?!='' AND title=? AND artist=?))",
                (pid, key, title, title, artist)).fetchone()
            if dup:
                return {"ok": True, "existed": True, "id": dup["id"], "playlist": row["name"]}
            c.execute("INSERT INTO playlist_items(pid,path,artist,title,album,duration,song,added)"
                      " VALUES(?,?,?,?,?,?,?,?)",
                      (pid, key, artist, title, album, dur, sj, _now()))
            iid = c.execute("SELECT last_insert_rowid() AS id").fetchone()["id"]
        self.log("info", f"加入歌单「{row['name']}」：{artist} - {title}")
        res = {"ok": True, "id": iid, "playlist": row["name"]}
        if path and self._archive_enabled(pid):        # 后续加入的歌执行同样的归档动作
            res["archive"] = self.archive_playlist(pid, only=iid)
        return res

    def playlist_remove(self, item_id: int) -> dict:
        with self._db() as c:
            c.execute("DELETE FROM playlist_items WHERE id=?", (item_id,))
        return {"ok": True}

    def fav_toggle(self, path: str = "", song: dict = None) -> dict:
        """「我的喜欢」开关：已在其中则移除，否则加入（支持本地文件与网络搜索结果）。"""
        pid = self._ensure_fav()
        key = path or "pending://%s/%s" % ((song or {}).get("source", ""),
                                           (song or {}).get("id", ""))
        if not path and not song:
            return {"error": "缺少歌曲信息"}
        with self._db() as c:
            row = c.execute("SELECT id FROM playlist_items WHERE pid=? AND path=?",
                            (pid, key)).fetchone()
            if not row and song:
                row = c.execute("SELECT id FROM playlist_items WHERE pid=? AND title=? AND artist=?",
                                (pid, song.get("name", ""), song.get("artist", ""))).fetchone()
            if not row and path:
                f = c.execute("SELECT title,artist FROM files WHERE path=?", (path,)).fetchone()
                if f:
                    row = c.execute("SELECT id FROM playlist_items WHERE pid=? AND title=? AND artist=?",
                                    (pid, f["title"], f["artist"])).fetchone()
            if row:
                c.execute("DELETE FROM playlist_items WHERE id=?", (row["id"],))
                return {"ok": True, "fav": False}
        res = self.playlist_add(pid, path=path, song=song)
        if res.get("error"):
            return res
        return {"ok": True, "fav": True, "playlist": res.get("playlist", self.FAV_NAME)}

    # ---- 分享导入：把别人分享的歌单拉成本地歌单 --------------------------

    @staticmethod
    def split_share_url(text):
        """从分享链接（或粘贴的一整段文字）里取出 (base, token)。"""
        m = re.search(r"(https?://[^\s'\"]*?)/api/share/([0-9a-zA-Z_-]{6,64})",
                      (text or "").strip())
        if not m:
            return "", ""
        return m.group(1).rstrip("/"), m.group(2)

    def share_open(self, url):
        """拉远端分享的歌单清单，返回 (info, error)。info 含 base/token/data。"""
        base, token = self.split_share_url(url)
        if not base or not base.lower().startswith(("http://", "https://")):
            return None, "链接无法识别，应形如 http://NAS地址:端口/…/api/share/xxxxxxxx"
        try:
            data = _http_get_json("%s/api/share/%s" % (base, token))
        except Exception as e:
            return None, f"读取分享链接失败：{e}"
        if not isinstance(data, dict) or data.get("app") != "music-tidy":
            return None, "对方返回的内容不是本应用的歌单分享"
        if data.get("error"):
            return None, str(data["error"])
        return {"base": base, "token": token, "data": data}, ""

    def share_preview(self, url) -> dict:
        """导入前预览：在分享清单上叠加「本机是否已有」的匹配结果。

        分享方的 file 字段只表示对方本地有（能不能走直链补下），
        与接收方是否已有无关，两者必须分开算，否则文案会自相矛盾。
        """
        info, err = self.share_open(url)
        if err:
            return {"error": err}
        data = dict(info["data"])
        local = self.local_files() or []
        songs, matched = [], 0
        for s in data.get("songs") or []:
            s = dict(s)
            t = s.get("title") or os.path.splitext(s.get("name") or "")[0]
            path, _score = playlist_mod.match_song(
                {"title": t, "artist": s.get("artist") or "", "album": s.get("album") or ""}, local)
            s["local"] = bool(path)
            if path:
                matched += 1
            songs.append(s)
        data["songs"] = songs
        data["matched"] = matched
        data["missing"] = len(songs) - matched
        data["fetchable"] = sum(1 for s in songs if not s["local"] and s.get("file"))
        return {"ok": True, **data}

    def _unique_playlist_name(self, base: str) -> str:
        base = (base or "分享的歌单").strip()[:40] or "分享的歌单"
        with self._db() as c:
            if not c.execute("SELECT 1 FROM playlists WHERE name=?", (base,)).fetchone():
                return base
            for k in range(2, 100):
                nm = f"{base}({k})"[:40]
                if not c.execute("SELECT 1 FROM playlists WHERE name=?", (nm,)).fetchone():
                    return nm
        return base + "-new"

    def share_import(self, url, name: str = "") -> dict:
        """导入分享歌单：本地已有的直接挂上，没有的记为待下载（走分享人直链）。"""
        info, err = self.share_open(url)
        if err:
            return {"error": err}
        base, token, data = info["base"], info["token"], info["data"]
        songs = data.get("songs") or []
        if not songs:
            return {"error": "分享的歌单里没有歌曲"}
        pname = self._unique_playlist_name((name or "").strip() or data.get("name") or "分享的歌单")
        res = self.playlist_create(pname)
        if res.get("error"):
            return res
        pid = res["id"]
        local = self.local_files() or []
        hit = fetch = added = skipped = 0
        for s in songs:
            nm = os.path.splitext(s.get("name") or "")[0]
            title = s.get("title") or nm        # 旧版分享清单无标签时退回文件名
            artist = s.get("artist") or ""
            if not title:
                skipped += 1                    # 连文件名都没有，只能跳过
                continue
            path, _score = playlist_mod.match_song(
                {"title": title, "artist": artist, "album": s.get("album") or ""}, local)
            if path:
                self.playlist_add(pid, path=path)
                hit += 1
                added += 1
                continue
            song = {"name": title, "artist": artist, "album": s.get("album") or "",
                    "duration": s.get("duration") or 0, "source": "share", "id": str(s.get("i"))}
            if s.get("file"):
                # 分享人本地有这首歌 → 记直链，后续可从对方那里补下
                song["remote_url"] = "%s/api/share/%s/file?i=%s" % (base, token, s.get("i"))
                fetch += 1
            self.playlist_add(pid, song=song)
            added += 1
        self.log("info", f"导入分享歌单：「{pname}」共 {added} 首，本地已有 {hit} 首，"
                         f"可从分享人下载 {fetch} 首")
        return {"ok": True, "id": pid, "name": pname, "total": added,
                "matched": hit, "pending": added - hit, "fetchable": fetch,
                "skipped": skipped}

    def _share_pending(self, pid):
        with self._db() as c:
            rows = c.execute("SELECT * FROM playlist_items WHERE pid=? AND path LIKE 'pending://share/%'",
                             (pid,)).fetchall()
        out = []
        for r in rows:
            d = dict(r)
            try:
                d["_song"] = json.loads(d.get("song") or "{}")
            except Exception:
                d["_song"] = {}
            if d["_song"].get("remote_url"):
                out.append(d)
        return out

    def _http_to_file(self, url, dst):
        """流式下载远端直链到本地，边下边报进度。

        返回 (最终路径, 失败说明)；服务端给了真实文件名时沿用其扩展名，
        避免 flac 等被写成 .mp3。失败/停止时返回 ("", 原因)。
        """
        req = urllib.request.Request(url, headers={"User-Agent": "music-tidy/1.8"})
        got, cd = 0, ""
        try:
            with urllib.request.urlopen(req, timeout=60) as r, open(dst, "wb") as f:
                cd = r.headers.get("Content-Disposition") or ""
                total = int(r.headers.get("Content-Length") or 0)
                while True:
                    if self._stop_event.is_set():
                        break
                    chunk = r.read(65536)
                    if not chunk:
                        break
                    f.write(chunk)
                    got += len(chunk)
                    self._tick(bytes_done=got, bytes_total=total)
        except Exception as e:
            self.log("warn", f"分享直链下载失败 {url}: {e}")
            try:
                os.remove(dst)
            except OSError:
                pass
            return "", str(e)[:200]
        if self._stop_event.is_set():
            try:
                os.remove(dst)
            except OSError:
                pass
            return "", "已停止"
        if got <= 1024:
            try:
                os.remove(dst)
            except OSError:
                pass
            return "", "下载内容为空"
        m = re.search(r"filename\*=\S*?''([^;]+)", cd)
        if m:
            real_ext = os.path.splitext(urllib.parse.unquote(m.group(1).strip().strip('"')))[1].lower()
            if real_ext in self._COMMON_EXTS and real_ext != os.path.splitext(dst)[1].lower():
                moved = os.path.splitext(dst)[0] + real_ext
                try:
                    os.replace(dst, moved)
                    dst = moved
                except OSError:
                    pass
        return dst, ""

    def share_fetch(self, pid) -> dict:
        """把导入时「本地没有」的歌，从分享人的直链补下到本地并入库。"""
        row = self._playlist_row(pid)
        if not row:
            return {"error": "歌单不存在"}
        items = self._share_pending(pid)
        if not items:
            return {"ok": True, "total": 0, "msg": "该歌单没有待下载的歌曲"}
        dl_dir = (self._config.get("music_source", {}) or {}).get("download_dir", "")
        if not dl_dir:
            folders = self.all_folders()
            dl_dir = os.path.join(folders[0], "Downloads") if folders else self.data_dir
        try:
            os.makedirs(dl_dir, exist_ok=True)
        except Exception as e:
            return {"error": f"下载目录不可写：{dl_dir}（{e}）"}
        self._start_task("share_fetch")
        stats = {"total": len(items), "ok": 0, "fail": 0}
        try:
            self._tick(total=len(items))
            self.log("info", f"开始从分享人补下载：「{row['name']}」{len(items)} 首")
            for i, it in enumerate(items):
                if not self._check_ctrl():
                    break
                song = it["_song"]
                nm = _sanitize_dir("%s - %s" % (song.get("artist", ""), song.get("name", "")))[:120]
                ext = os.path.splitext(urllib.parse.urlparse(song["remote_url"]).path)[1].lower()
                dst = os.path.join(dl_dir, nm + (ext if ext in self._COMMON_EXTS else ".mp3"))
                if os.path.exists(dst):
                    dst = os.path.join(dl_dir, "%s_%d%s" % (nm, i + 1, os.path.splitext(dst)[1]))
                got, err = self._http_to_file(song["remote_url"], dst)
                if not got:
                    stats["fail"] += 1
                    self.log("warn", f"补下载失败 {song.get('name', '')}: {err}")
                    self._tick(done=i + 1, fail=stats["fail"], current=song.get("name", ""))
                    continue
                dst = got
                self.ingest(dst)
                final = dst
                try:
                    tr = self._tidy_file(dst)
                    final = tr.get("final_path") or dst
                except Exception:
                    pass
                with self._db() as c:
                    c.execute("UPDATE playlist_items SET path=?,album=?,duration=? WHERE id=?",
                              (final, song.get("album", ""), song.get("duration") or 0, it["id"]))
                stats["ok"] += 1
                self._tick(done=i + 1, ok=stats["ok"], fail=stats["fail"], current=song.get("name", ""))
                if self._archive_enabled(pid):
                    self.archive_playlist(pid, only=it["id"])
            self.log("info", f"分享补下载完成：成功 {stats['ok']}，失败 {stats['fail']}")
        except Exception as e:
            self.log("error", f"分享补下载异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    # ---- 归档：移动 / 另存到 根目录/<歌单名>/ ----------------------------

    def _archive_conf(self, pid):
        """该歌单生效的 (归档根目录, 方式, 是否自动)：歌单自身优先，其次全局设置。"""
        pl = self._config.get("playlist", {}) or {}
        row = self._playlist_row(pid) or {}
        root = (row.get("archive_dir") or "").strip() or (pl.get("archive_root") or "").strip()
        mode = (row.get("archive_mode") or "").strip() or (pl.get("archive_mode") or "copy")
        auto = bool(row.get("auto_sync", 1)) and bool(pl.get("auto_sync", True))
        return root, (mode if mode in ("move", "copy") else "copy"), auto

    def _archive_enabled(self, pid) -> bool:
        root, _mode, auto = self._archive_conf(pid)
        return bool(auto and root and os.path.isabs(root))

    def _update_path_after_move(self, src, dst, fallback=""):
        folder = fallback
        abs_new = os.path.abspath(dst)
        for f in self.all_folders():
            if abs_new.startswith(os.path.abspath(f) + os.sep):
                folder = f
                break
        with self._db() as c:
            c.execute("UPDATE files SET path=?,folder=?,name=? WHERE path=?",
                      (dst, folder, os.path.basename(dst), src))

    def _archive_one(self, pid, item, root, mode, name=""):
        """归档单条：返回 (状态, 说明)。状态 ok/skip/pending/fail。"""
        src = item["path"]
        if src.startswith("pending://") or not os.path.isfile(src):
            return "pending", "未下载到本地"
        name = name or (self._playlist_row(pid) or {}).get("name") or "歌单"
        target_dir = os.path.join(root, _sanitize_dir(name))
        base = os.path.basename(src)
        dst = os.path.join(target_dir, base)
        if os.path.abspath(dst) == os.path.abspath(src):
            return "skip", "已在目标位置"
        try:
            os.makedirs(target_dir, exist_ok=True)
            if os.path.exists(dst):
                # 目标已有同名同大小文件 → 视为先前已归档，重复执行不产生副本堆
                try:
                    if os.path.getsize(dst) == os.path.getsize(src):
                        return "skip", "已归档（同名同大小）"
                except OSError:
                    pass
                stem0, ext0 = os.path.splitext(base)
                k = 1
                while os.path.exists(dst):
                    dst = os.path.join(target_dir, f"{stem0}_{k}{ext0}")
                    k += 1
            stem = os.path.splitext(src)[0]
            dstem = os.path.splitext(dst)[0]
            if mode == "move":
                shutil.move(src, dst)
                for suf in self._SIDECAR_SUFFIXES:
                    if os.path.exists(stem + suf):
                        shutil.move(stem + suf, dstem + suf)
                self._update_path_after_move(src, dst, fallback=root)
                # 歌单条目（含其它歌单里的同一路径）跟着文件走，否则会变成失效引用
                with self._db() as c:
                    c.execute("UPDATE playlist_items SET path=? WHERE path=?", (dst, src))
                    if item.get("id"):
                        c.execute("UPDATE playlist_items SET path=? WHERE id=?", (dst, item["id"]))
            else:
                shutil.copy2(src, dst)
                for suf in self._SIDECAR_SUFFIXES:
                    if os.path.exists(stem + suf):
                        shutil.copy2(stem + suf, dstem + suf)
            return "ok", dst
        except Exception as e:
            self.log("warn", f"歌单归档失败 {src}: {e}")
            return "fail", str(e)

    def archive_playlist(self, pid, mode=None, root=None, only=None) -> dict:
        """把歌单里的歌移动/另存到 root/<歌单名>/。only=条目 id 时只处理单条（同步）。"""
        row = self._playlist_row(pid)
        if not row:
            return {"error": "歌单不存在"}
        croot, cmode, _auto = self._archive_conf(pid)
        root = (root or croot or "").strip()
        mode = (mode or cmode or "copy").strip()
        if mode not in ("move", "copy"):
            mode = "copy"
        if not root or not os.path.isabs(root):
            return {"error": "请先设置归档目录（设置 → 歌单归档，或本歌单的归档目录）"}
        try:
            os.makedirs(os.path.join(root, _sanitize_dir(row["name"])), exist_ok=True)
        except Exception as e:
            return {"error": f"归档目录不可写：{root}（{e}）"}
        with self._db() as c:
            if only:
                items = [dict(r) for r in c.execute(
                    "SELECT * FROM playlist_items WHERE id=?", (only,)).fetchall()]
            else:
                items = [dict(r) for r in c.execute(
                    "SELECT * FROM playlist_items WHERE pid=? ORDER BY id", (pid,)).fetchall()]
        if only:
            if not items:
                return {"error": "条目不存在"}
            st, info = self._archive_one(pid, items[0], root, mode, row["name"])
            return {"ok": st in ("ok", "skip"), "state": st, "detail": info, "mode": mode}
        self._start_task("playlist_archive")
        stats = {"total": len(items), "ok": 0, "skip": 0, "pending": 0, "fail": 0}
        try:
            self._tick(total=len(items))
            self.log("info", f"开始歌单归档：「{row['name']}」{len(items)} 首"
                             f" {'移动' if mode == 'move' else '另存'} -> {root}")
            for i, it in enumerate(items):
                if not self._check_ctrl():
                    break
                st, _info = self._archive_one(pid, it, root, mode, row["name"])
                stats[st] = stats.get(st, 0) + 1
                self._tick(done=i + 1, ok=stats["ok"], fail=stats["fail"],
                           current=it["title"] or os.path.basename(it["path"]))
            self.log("info", f"歌单归档完成：成功 {stats['ok']}，跳过 {stats['skip']}，"
                             f"待下载 {stats['pending']}，失败 {stats['fail']}")
        except Exception as e:
            self.log("error", f"歌单归档异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    # ---- 分享 --------------------------------------------------------

    def shares(self) -> list:
        with self._db() as c:
            rows = c.execute("SELECT * FROM shares WHERE active=1 ORDER BY created DESC").fetchall()
        return [dict(r) for r in rows]

    def share_create(self, pid) -> dict:
        row = self._playlist_row(int(pid or 0))
        if not row:
            return {"error": "歌单不存在"}
        items = self.playlist_items(row["id"])
        if not items:
            return {"error": "歌单是空的，先把歌曲加进来再分享"}
        token = "".join("%02x" % b for b in os.urandom(9))
        with self._db() as c:
            c.execute("UPDATE shares SET active=0 WHERE pid=?", (row["id"],))
            c.execute("INSERT INTO shares(token,pid,name,created,active,hits,bytes)"
                      " VALUES(?,?,?,?,1,0,0)", (token, row["id"], row["name"], _now()))
        self.log("info", f"生成分享链接：「{row['name']}」{len(items)} 首")
        return {"ok": True, "token": token, "name": row["name"], "count": len(items),
                "local": sum(1 for i in items if i["local"])}

    def share_revoke(self, token) -> dict:
        with self._db() as c:
            c.execute("UPDATE shares SET active=0 WHERE token=?", (token,))
        return {"ok": True}

    def share_data(self, token, count_hit=False):
        """分享出去的歌单清单（不暴露本地绝对路径）。"""
        with self._db() as c:
            s = c.execute("SELECT * FROM shares WHERE token=? AND active=1", (token,)).fetchone()
            if not s:
                return None
            if count_hit:
                c.execute("UPDATE shares SET hits=hits+1 WHERE token=?", (token,))
            pid, pname = s["pid"], (c.execute(
                "SELECT name FROM playlists WHERE id=?", (s["pid"],)).fetchone() or {"name": s["name"]})["name"]
        items = sorted(self.playlist_items(pid), key=lambda x: x["id"])
        songs = [{"i": it["id"], "artist": it["artist"], "title": it["title"],
                  "album": it["album"], "duration": it["duration"],
                  "name": os.path.basename(it.get("name") or ""),
                  "size": it.get("size") or 0, "file": bool(it.get("file_id"))}
                 for it in items]
        return {"app": "music-tidy", "v": 1, "name": pname,
                "count": len(songs), "songs": songs}

    def share_file(self, token, item_id):
        """分享下载：校验 token 后回本地文件路径，并累计已转发字节（带宽展示）。"""
        with self._db() as c:
            s = c.execute("SELECT 1 FROM shares WHERE token=? AND active=1", (token,)).fetchone()
            if not s:
                return None
            it = c.execute("SELECT path FROM playlist_items WHERE id=? AND pid=("
                           "SELECT pid FROM shares WHERE token=? AND active=1)",
                           (item_id, token)).fetchone()
        if not it or it["path"].startswith("pending://") or not os.path.isfile(it["path"]):
            return None
        try:
            size = os.path.getsize(it["path"])
        except OSError:
            return None
        with self._db() as c:
            c.execute("UPDATE shares SET bytes=bytes+? WHERE token=?", (size, token))
        return it["path"]

    def _embed_genre(self, path: str, genre: str) -> bool:
        """只写内嵌 genre 标签，不动其它已有标签（供一键分类使用）。"""
        ext = os.path.splitext(path)[1].lower()
        try:
            if ext == ".flac":
                from mutagen.flac import FLAC
                f = FLAC(path)
                f["genre"] = genre
                f.save()
                return True
            if ext in (".mp4", ".m4a", ".m4b", ".m4v"):
                from mutagen.mp4 import MP4
                f = MP4(path)
                f["\xa9gen"] = [genre]
                f.save()
                return True
            if ext in (".ogg", ".oga", ".opus"):
                if ext == ".opus":
                    from mutagen.oggopus import OggOpus as Ogg
                else:
                    from mutagen.oggvorbis import OggVorbis as Ogg
                f = Ogg(path)
                f["genre"] = genre
                f.save()
                return True
            if ext in (".aiff", ".aif", ".aifc", ".wma", ".asf"):
                return False  # 与 _embed_mutagen 保持一致，避免写坏文件
            if ext == ".wav":
                from mutagen.wave import WAVE
                from mutagen.id3 import TCON
                f = WAVE(path)
                if f.tags is None:
                    f.add_tags()
                f.tags.add(TCON(encoding=3, text=genre))
                f.save()
                return True
            from mutagen.id3 import ID3, TCON, ID3NoHeaderError
            try:
                tags = ID3(path)
            except ID3NoHeaderError:
                tags = ID3()
            tags.add(TCON(encoding=3, text=genre))
            tags.save(path, v2_version=3)
            return True
        except Exception as e:
            self.log("warn", f"genre 写入失败 {os.path.basename(path)} [{ext or '无扩展名'}]: {e}")
            return False

    def classify_style(self) -> dict:
        """一键风格分类（后台任务）。

        遍历音乐库，风格取值优先 DB（识别/手工结果），其次按文件名全维度
        代码解析出的风格标签；解析不到则跳过。确定风格后：
        1) 回填 files.style（原本为空的）；
        2) 写入音频内嵌 genre 标签，让飞牛音乐「风格」页可以按风格归类。
        """
        self._start_task("classify")
        stats = {"total": 0, "tagged": 0, "skipped": 0, "fail": 0}
        try:
            with self._db() as c:
                rows = c.execute("SELECT path,name,style FROM files ORDER BY id").fetchall()
            items = [r for r in rows if os.path.exists(r["path"])]
            stats["total"] = len(items)
            self._tick(total=len(items))
            self.log("info", f"开始风格分类：{len(items)} 个文件")
            stopped = False
            for i, r in enumerate(items):
                if not self._check_ctrl():
                    stopped = True
                    break
                path = r["path"]
                base = os.path.basename(path)
                style = (r["style"] or "").strip()
                if not style:
                    name = r["name"] or base
                    style = parse_tidy_code(name).get("style_label", "")
                if not style:
                    stats["skipped"] += 1
                    self._tick(done=i + 1, ok=stats["tagged"], fail=stats["fail"], current=base)
                    continue
                try:
                    if not (r["style"] or "").strip():
                        with self._db() as c:
                            c.execute("UPDATE files SET style=? WHERE path=?", (style, path))
                    if self._embed_genre(path, style):
                        stats["tagged"] += 1
                    else:
                        stats["fail"] += 1
                except Exception as e:
                    stats["fail"] += 1
                    self.log("warn", f"风格分类失败 {path}: {e}")
                self._tick(done=i + 1, ok=stats["tagged"], fail=stats["fail"], current=base)
            self.log("info",
                     f"风格分类{'已停止' if stopped else '完成'}: 写入 {stats['tagged']}，"
                     f"无风格跳过 {stats['skipped']}，失败 {stats['fail']}。"
                     "请在飞牛音乐中重新扫描音乐库以刷新「风格」页")
        except Exception as e:
            self.log("error", f"风格分类异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    def touch_library(self) -> dict:
        """一键触发飞牛重扫（后台任务）。

        不改动任何文件内容，只把库内音频及其附属文件（.lrc/封面）的修改时间
        刷到当下。飞牛音乐的增量扫描以「路径+修改时间/大小」为文件指纹，
        touch 之后飞牛下次扫描（自动或手动）会把这些文件当作变化项重新解析，
        从而读到新写入的标签、歌词与封面。
        """
        self._start_task("touch")
        stats = {"total": 0, "touched": 0, "fail": 0}
        try:
            with self._db() as c:
                rows = c.execute("SELECT path FROM files ORDER BY id").fetchall()
            items = [r["path"] for r in rows if os.path.exists(r["path"])]
            stats["total"] = len(items)
            self._tick(total=len(items))
            self.log("info", f"开始触发重扫：touch {len(items)} 个文件")
            stopped = False
            for i, path in enumerate(items):
                if not self._check_ctrl():
                    stopped = True
                    break
                base = os.path.basename(path)
                try:
                    os.utime(path, None)
                    stem = os.path.splitext(path)[0]
                    for sfx in self._SIDECAR_SUFFIXES:
                        p = stem + sfx
                        try:
                            if os.path.exists(p):
                                os.utime(p, None)
                        except Exception:
                            pass
                    stats["touched"] += 1
                except Exception as e:
                    stats["fail"] += 1
                    self.log("warn", f"touch 失败 {path}: {e}")
                self._tick(done=i + 1, ok=stats["touched"], fail=stats["fail"], current=base)
            self.log("info",
                     f"触发重扫{'已停止' if stopped else '完成'}: 更新 {stats['touched']}，"
                     f"失败 {stats['fail']}。飞牛音乐将在下次扫描时重新读取这些文件")
        except Exception as e:
            self.log("error", f"触发重扫异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    # ----------------------------------------------------------------- CUE 整轨拆分
    def split_cue_files(self) -> dict:
        """拆分 CUE 整轨音频为逐曲文件（后台任务）。

        扫描库内所有目录，找到 .cue 文件，用 ffmpeg 按时间戳拆分为逐曲 FLAC，
        拆分后的文件自动入库走正常整理流程。
        """
        self._start_task("split_cue")
        stats = {"total": 0, "split": 0, "tracks": 0, "fail": 0}
        try:
            # 获取库内所有目录
            with self._db() as c:
                rows = c.execute("SELECT DISTINCT folder FROM files").fetchall()
            folders = [r["folder"] for r in rows if r["folder"] and os.path.isdir(r["folder"])]

            # 查找 CUE 文件
            cue_files = find_cue_files(folders)
            stats["total"] = len(cue_files)
            self._tick(total=len(cue_files))
            self.log("info", f"找到 {len(cue_files)} 个 CUE 文件")

            if not cue_files:
                self.log("info", "没有找到 CUE 文件")
                self._finish_task()
                return stats

            stopped = False
            for i, cue_path in enumerate(cue_files):
                if not self._check_ctrl():
                    stopped = True
                    break

                cue_name = os.path.basename(cue_path)
                self.log("info", f"处理 CUE: {cue_name}")

                try:
                    # 解析 CUE
                    sheet = parse_cue(cue_path)
                    if not sheet:
                        self.log("warn", f"无法解析 CUE: {cue_path}")
                        stats["fail"] += 1
                        continue

                    if not sheet.file_path or not os.path.exists(sheet.file_path):
                        self.log("warn", f"找不到关联的音频文件: {sheet.file_path}")
                        stats["fail"] += 1
                        continue

                    # 检查是否已拆分过（同目录有拆分后的文件）
                    output_dir = os.path.dirname(os.path.abspath(cue_path))
                    existing = [f for f in os.listdir(output_dir)
                                if f.endswith(".flac") and re.match(r"^\d{2}\s*-", f)]
                    if len(existing) >= len(sheet.tracks):
                        self.log("info", f"跳过已拆分的 CUE: {cue_name}")
                        stats["split"] += 1
                        stats["tracks"] += len(sheet.tracks)
                        self._tick(done=i + 1, ok=stats["split"], fail=stats["fail"], current=cue_name)
                        continue

                    # 拆分
                    def progress_cb(current, total, msg):
                        self.log("info", f"  {msg}")

                    output_files = split_cue(cue_path, output_dir, progress_cb)
                    stats["split"] += 1
                    stats["tracks"] += len(output_files)
                    self.log("info", f"拆分成功: {cue_name} -> {len(output_files)} 首")

                    # 拆分后的文件入库
                    for out_path in output_files:
                        try:
                            self._add_file(out_path)
                        except Exception as e:
                            self.log("warn", f"入库失败 {out_path}: {e}")

                except Exception as e:
                    stats["fail"] += 1
                    self.log("error", f"CUE 拆分失败 {cue_path}: {e}")

                self._tick(done=i + 1, ok=stats["split"], fail=stats["fail"], current=cue_name)

            self.log("info",
                     f"CUE 拆分{'已停止' if stopped else '完成'}: "
                     f"处理 {stats['split']} 个 CUE，生成 {stats['tracks']} 首歌曲，"
                     f"失败 {stats['fail']}")
        except Exception as e:
            self.log("error", f"CUE 拆分异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    # ---- 网络音源搜索下载 ----

    def search_songs(self, keyword, limit=20):
        """搜索网络音源，返回歌曲列表。"""
        ms_cfg = self._config.get("music_source", {})
        ms = MusicSearch(ms_cfg)
        return ms.search(keyword, limit)

    def song_preview(self, song):
        """获取试听播放直链。"""
        ms = MusicSearch(self._config.get("music_source", {}))
        return ms.get_preview(song)

    def song_lyric(self, song):
        """获取歌词文本。"""
        ms = MusicSearch(self._config.get("music_source", {}))
        return ms.get_lyric(song)

    def song_cover(self, song):
        """搜索结果封面直链（自带为空时跨源补）。"""
        ms = MusicSearch(self._config.get("music_source", {}))
        return ms.get_cover(song or {})

    # ---- 下载队列共用助手 ----

    def _db_name_index(self):
        """一次拉平库，加上下载自建的歌名索引，供整批「跳过已存在」判定。

        旧判定在循环里每首都 `self._db_list_files()` 全量取库，几千首就是
        几千次全表查询；而且只看「歌名是文件名子串」，同名的不同歌手
        翻唱会被误跳过。改为一趟取数 + 歌名与歌手双重比对。
        """
        with self._db() as c:
            rows = c.execute("SELECT path, artist, title FROM files").fetchall()
        return [[os.path.basename(r["path"] or "").lower(),
                 os.path.dirname(r["path"] or "").lower(),
                 (r["artist"] or "").lower(), (r["title"] or "").lower()] for r in rows]

    @staticmethod
    def _name_index_hit(index, name, artist=""):
        """本地已有这首歌？歌名必须对得上，给了歌手就还得对得上歌手。

        库里从未整理过的文件没有 artist 字段，此时回退到“歌手出现在
        文件名或目录名里”；宁可重下一首也不要误跳过，所以对不上就不算存在。
        """
        nm = (name or "").strip().lower()
        if not nm or not index:
            return False
        arts = [a.strip() for a in re.split(r"[/、,，&]", (artist or "").lower()) if a.strip()]
        for bn, folder, db_artist, db_title in index:
            if not (db_title == nm or nm in bn):
                continue
            if not arts:
                return True
            if any(a in db_artist or a in bn or a in folder for a in arts):
                return True
        return False

    @staticmethod
    def _count_dl(stats, result):
        """把单曲结果累进任务统计（'stop' 不计）。"""
        if result == "ok":
            stats["ok"] += 1
        elif result == "skip":
            stats["skip"] += 1
        elif result == "fail":
            stats["fail"] += 1

    def _download_one(self, ms, song, dl_dir, index):
        """下载并整理单曲，返回 'ok' / 'skip' / 'fail' / 'stop'。

        把搜索/跳过/下载/转码/刮削收在一个入口，是为了让下载中心能拿到
        真实状态：旧实现各失败分支直接 continue，done 不涨，整批跑完前端
        仍按 done<total 判成「已停止」，跳过也不计入进度。
        同时按阶段上报 stage，前端就能区分「搜索音源 / 下载中 / 转码中 / 刮削入库」。
        """
        name = song.get("name", "") or ""
        artist = song.get("artist", "") or ""
        keyword = f"{artist} {name}".strip() or name.strip()
        if not keyword:
            self.log("warn", "跳过一首：歌名为空，无法搜索")
            return "fail"
        self._tick(current=keyword, stage="检查本地", bytes_done=0)
        if self._name_index_hit(index, name, artist):
            self.log("info", f"跳过已存在: {keyword}")
            return "skip"
        # 用户在结果里点名的这首歌要先按原样取链：旧实现一律拿「歌手 歌名」重搜
        # 一遍再取 results[0]，同名翻唱 / Beat 会把用户选的那版换成另一版。
        picked = song if (song.get("source") and song.get("id")) else None
        dl = {"ok": False, "error": ""}
        if picked:
            self._tick(stage="下载中")
            dl = self._download_with_progress(ms, picked, dl_dir)
            if not dl["ok"] and not dl.get("partial"):
                self.log("info", f"所选结果取链失败（{dl.get('error') or '未知原因'}），改按歌名重搜其它版本")
                picked = None
        if not picked:
            self._tick(stage="搜索音源")
            try:
                results = ms.search(keyword, limit=3)
            except Exception as e:
                self.log("warn", f"搜索失败 [{keyword}]: {e}")
                return "fail"
            if not results:
                self.log("warn", f"未找到音源: {keyword}")
                return "fail"
            self._tick(stage="下载中")
            dl = self._download_with_progress(ms, results[0], dl_dir)
        if not dl["ok"]:
            if dl.get("partial"):
                self.log("info", f"已停止，断点保留: {keyword}")
                return "stop"
            self.log("warn", f"下载失败 [{keyword}]: {dl['error']}")
            return "fail"
        path = dl["path"]
        self.log("info", f"已下载: {os.path.basename(path)}")
        self._tick(stage="转码中", current=os.path.basename(path))
        path = self._post_download_convert(path)
        self._tick(stage="刮削入库", current=os.path.basename(path))
        # 先入库再整理：_tidy_file 收尾是 UPDATE files SET ... WHERE path=?，
        # 新下载的文件库里还没有行，顺序反了会把刮到的歌手/歌名/歌词状态全部丢弃。
        try:
            self.ingest(path)
        except Exception as e:
            self.log("warn", f"入库失败 {os.path.basename(path)}: {e}")
        tidy_ok = False
        final = path
        try:
            r = self._tidy_file(path)
            tidy_ok = bool(r.get("ok"))
            final = r.get("final_path") or path
        except Exception as e:
            self.log("warn", f"整理失败 [{path}]: {e}")
        if not tidy_ok:
            # 文件已经拿到手就是下载成功，只是没识别出歌手；归为失败会让
            # 下载中心与曲库自相矛盾（歌已在库却显示失败），只记一条提醒。
            self.log("warn", f"已下载但未识别出歌手（可在曲库单独重新整理）: {os.path.basename(final)}")
        # 本批后续重复勾选同一首时能命中跳过
        index.append([os.path.basename(final).lower(),
                      os.path.dirname(final).lower(), artist.lower(), name.lower()])
        return "ok"

    def download_songs(self, songs) -> dict:
        """后台任务：逐首搜索下载 + 自动整理刮削。"""
        stats = {"ok": 0, "fail": 0, "skip": 0, "total": len(songs)}
        ms_cfg = self._config.get("music_source", {})
        ms = MusicSearch(ms_cfg)

        # 下载目录
        dl_dir = ms_cfg.get("download_dir", "")
        if not dl_dir:
            folders = self._config.get("folders", [])
            if folders:
                dl_dir = os.path.join(folders[0], "Downloads")
            else:
                self.log("error", "下载目录未配置，请先在设置中配置音源下载目录")
                return {"error": "下载目录未配置"}

        os.makedirs(dl_dir, exist_ok=True)
        self._start_task("download")
        index = self._db_name_index()
        try:
            self._tick(total=len(songs))
            stopped = False
            for i, song in enumerate(songs):
                if not self._check_ctrl():
                    stopped = True
                    break
                result = self._download_one(ms, song, dl_dir, index)
                if result == "stop":
                    stopped = True
                    break
                self._count_dl(stats, result)
                self._tick(done=i + 1, ok=stats["ok"], fail=stats["fail"],
                           skip=stats["skip"], bytes_done=0)

            self.log("info",
                     f"下载{'已停止' if stopped else '完成'}: 成功 {stats['ok']}，"
                     f"失败 {stats['fail']}，跳过已存在 {stats['skip']}，"
                     f"待处理 {max(0, len(songs) - stats['ok'] - stats['fail'] - stats['skip'])} 首")
        except Exception as e:
            self.log("error", f"下载异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    def download_playlist(self, link) -> dict:
        """后台任务：解析歌单 → 逐首搜索下载 → 自动整理。"""
        from playlist import parse_link, fetch_playlist
        stats = {"ok": 0, "fail": 0, "skip": 0, "total": 0}

        parsed = parse_link(link)
        if not parsed:
            return {"error": "无法识别歌单链接，请粘贴完整的歌单分享地址"}
        platform, pid = parsed

        self._start_task("dl_playlist")
        try:
            self._tick(stage="解析歌单")
            self.log("info", f"解析歌单: {platform} {pid}")
            pl = fetch_playlist(platform, pid)
            songs = pl.get("songs", [])
            stats["total"] = len(songs)
            self.log("info", f"歌单「{pl.get('name','')}」共 {len(songs)} 首")
            if not songs:
                self.log("warn", "歌单为空或取不到曲目")

            ms_cfg = self._config.get("music_source", {})
            ms = MusicSearch(ms_cfg)
            dl_dir = ms_cfg.get("download_dir", "")
            if not dl_dir:
                folders = self._config.get("folders", [])
                if folders:
                    dl_dir = os.path.join(folders[0], "Downloads")
            os.makedirs(dl_dir, exist_ok=True)
            index = self._db_name_index()

            self._tick(total=len(songs))
            stopped = False
            for i, song in enumerate(songs):
                if not self._check_ctrl():
                    stopped = True
                    break
                result = self._download_one(ms, song, dl_dir, index)
                if result == "stop":
                    stopped = True
                    break
                self._count_dl(stats, result)
                self._tick(done=i + 1, ok=stats["ok"], fail=stats["fail"],
                           skip=stats["skip"], bytes_done=0)

            self.log("info",
                     f"歌单下载{'已停止' if stopped else '完成'}: 成功 {stats['ok']}，"
                     f"失败 {stats['fail']}，跳过已存在 {stats['skip']}，"
                     f"待处理 {max(0, len(songs) - stats['ok'] - stats['fail'] - stats['skip'])} 首")
        except Exception as e:
            self.log("error", f"歌单下载异常: {e}\n{traceback.format_exc()}")
            self._finish_task(err=str(e))
            return stats
        self._finish_task()
        return stats

    def _download_with_progress(self, ms, song, dl_dir):
        """调用 ms.download 并实时上报字节进度，支持停止时保留断点。"""
        import time as _t
        last = [0.0]

        def progress(received, total):
            now = _t.time()
            if now - last[0] >= 0.3 or (total and received >= total):
                last[0] = now
                self._tick(bytes_done=received, bytes_total=total or 0)

        return ms.download(song, dl_dir, progress=progress,
                           cancel=lambda: self._stop_event.is_set())

    # 通用可播格式（无需转码）
    _COMMON_EXTS = {".mp3", ".flac", ".m4a", ".wav", ".ogg"}

    def _post_download_convert(self, path):
        """下载后转码为通用格式：加密格式用内置解密器，其它非通用格式用 ffmpeg。
        返回最终文件路径（失败时返回原路径）。"""
        if not (self._config.get("music_source") or {}).get("transcode", True):
            return path
        ext = os.path.splitext(path)[1].lower()
        if ext in self._COMMON_EXTS:
            return path
        import converter
        out_dir = os.path.dirname(path)
        # 1) 加密格式（ncm/qmc 家族）→ 内置解密
        if converter.detect_format(path):
            r = converter.convert_file(path, out_dir, keep_name=False)
            if r.get("ok") and r.get("out_path"):
                try:
                    os.remove(path)
                except OSError:
                    pass
                self.log("info", f"已解密转码: {os.path.basename(path)} → "
                                 f"{os.path.basename(r['out_path'])}")
                return r["out_path"]
            self.log("warn", f"解密失败（保留原文件）: {r.get('error', '')}")
            return path
        # 2) 其它非通用格式（wma/aac/ape 等）→ ffmpeg 转 mp3/flac
        if shutil.which("ffmpeg"):
            stem = os.path.splitext(path)[0]
            lossless = ext in (".ape", ".wv", ".tta", ".dsf", ".dff", ".aiff")
            tgt = "flac" if lossless else "mp3"
            tmp = f"{stem}.__tc__.{tgt}"
            cmd = ["ffmpeg", "-y", "-i", path]
            if tgt == "mp3":
                cmd += ["-codec:a", "libmp3lame", "-q:a", "0"]
            cmd += [tmp]
            try:
                r = subprocess.run(cmd, capture_output=True, timeout=600)
                if r.returncode == 0 and os.path.exists(tmp) and os.path.getsize(tmp) > 1024:
                    os.remove(path)
                    os.replace(tmp, f"{stem}.{tgt}")
                    self.log("info", f"已转码: {os.path.basename(path)} → {os.path.basename(stem)}.{tgt}")
                    return f"{stem}.{tgt}"
                if os.path.exists(tmp):
                    os.remove(tmp)
                self.log("warn", f"ffmpeg 转码失败（保留原文件）: {os.path.basename(path)}")
            except Exception as e:
                if os.path.exists(tmp):
                    os.remove(tmp)
                self.log("warn", f"转码异常（保留原文件）: {e}")
        return path

    def _db_list_files(self):
        """返回库中所有文件路径列表。"""
        with self._db() as c:
            rows = c.execute("SELECT path FROM files").fetchall()
        return [r[0] for r in rows]


def _sanitize_dir(name: str) -> str:
    name = re.sub(r'[\\/:*?"<>|]', "_", name or "Unknown").strip()
    name = re.sub(r"[\s.]+$", "", name)
    return name or "Unknown"


def _deep_merge(base: dict, extra: dict) -> dict:
    out = dict(base)
    for k, v in (extra or {}).items():
        if isinstance(v, dict) and isinstance(out.get(k), dict):
            out[k] = _deep_merge(out[k], v)
        else:
            out[k] = v
    return out
