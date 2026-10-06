#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
music_tidy.py — 轻乐集主服务

- 提供统一网关（Unix Socket）与 TCP 测试两种监听模式。
- 托管静态 Web UI（app/server/web/）与 REST API。
- 后台线程执行扫描 / 整理 / 去重任务。
- 纯 Python 标准库实现，可在 fnOS python312 运行时直接运行。

用法：
    python3 music_tidy.py --socket /var/apps/music-tidy/target/app.sock \
        --config /var/apps/music-tidy/etc/config.json \
        --data /var/apps/music-tidy/var
    python3 music_tidy.py --port 9099 --data ./data   # 本地测试
"""

import argparse
import json
import os
import re
import socketserver
import sys
import threading
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs, quote

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from tidy_engine import TidyEngine, DEFAULT_CONFIG
from ai_llm import LLMClient
from scraper import test_rule_regex, tok_path
import converter
import playlist as playlist_mod
from watcher import MusicWatcher

APP_VERSION = "1.14.0"
GATEWAY_PREFIX = "/app/music-tidy"
WEB_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "web")

# 全局单例
ENGINE = None
WATCHER = None
CONFIG_PATH = ""
DATA_DIR = ""
# 扫描体检互斥：磁盘遍历开销大，禁止并发重复触发
AUDIT_LOCK = threading.Lock()


def _watch_state(eng) -> dict:
    """增量监听的对外状态（/api/status 与 /api/watch 共用一份形状）。

    监听线程可能在任何时刻被 /api/watch 重建 / 未启动，拿不到 status 时也要给出
    一个能让前端画“未开启”的完整字典，而不是让前端去猜缺键。
    """
    try:
        st = dict(WATCHER.status()) if WATCHER else {}
    except Exception:
        st = {}
    st.setdefault("running", False)
    st.setdefault("busy", False)
    st.setdefault("stage", "")
    st.setdefault("files", 0)
    st.setdefault("checks", 0)
    st.setdefault("last_found", 0)
    st.setdefault("last_check", "")
    st.setdefault("last_time", "")
    st.setdefault("next_in", None)
    st.setdefault("last_run", {})
    st["interval"] = int(eng._config.get("watch_interval", 30) or 30)
    st["enable"] = bool(eng._config.get("enable_watch"))
    return st


# ---------------------------------------------------------------------------
# 配置
# ---------------------------------------------------------------------------

def load_config(path: str) -> dict:
    cfg = json.loads(json.dumps(DEFAULT_CONFIG))
    if path and os.path.exists(path):
        try:
            with open(path, "r", encoding="utf-8") as f:
                user = json.load(f)
            cfg = _merge(cfg, user)
        except Exception as e:
            print(f"[warn] 读取配置失败: {e}", file=sys.stderr)
    # 系统授权目录注入
    sys_folders = []
    env = os.environ.get("TRIM_DATA_ACCESSIBLE_PATHS", "")
    if env:
        for p in env.split(":"):
            p = p.strip().rstrip("/")
            if p and p not in sys_folders:
                sys_folders.append(p)
    cfg["system_folders"] = sys_folders
    return cfg


def save_config(cfg: dict, path: str):
    if not path:
        return
    try:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        # 持久化时排除运行时注入字段
        out = {k: v for k, v in cfg.items() if k != "system_folders"}
        with open(path, "w", encoding="utf-8") as f:
            json.dump(out, f, ensure_ascii=False, indent=2)
    except Exception as e:
        print(f"[warn] 保存配置失败: {e}", file=sys.stderr)


def _merge(base, extra):
    out = dict(base)
    for k, v in (extra or {}).items():
        if isinstance(v, dict) and isinstance(out.get(k), dict):
            out[k] = _merge(out[k], v)
        else:
            out[k] = v
    return out


# ---------------------------------------------------------------------------
# HTTP 服务
# ---------------------------------------------------------------------------

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "MusicTidy/" + APP_VERSION

    # -- 工具 ------------------------------------------------------------
    def _log(self, *a):
        pass  # 关闭默认访问日志，改由引擎记录

    def log_message(self, format, *args):
        # Unix Socket 模式下 client_address 为空字符串，
        # 覆盖 log_message 避免 address_string() 崩溃
        pass

    def address_string(self):
        # Unix Socket 无对端地址，返回安全占位
        return "unix"

    def _path(self):
        """剥离网关前缀，得到应用内部路径。"""
        p = urlparse(self.path).path
        if p == GATEWAY_PREFIX or p.startswith(GATEWAY_PREFIX + "/"):
            p = p[len(GATEWAY_PREFIX):] or "/"
        if not p.startswith("/"):
            p = "/" + p
        return p

    def _query(self):
        return parse_qs(urlparse(self.path).query)

    def _is_admin(self):
        """网关注入的 X-Trim-Isadmin；本地测试时默认管理员。"""
        val = self.headers.get("X-Trim-Isadmin")
        if val is not None:
            return val.strip().lower() in ("1", "true", "yes")
        return True  # 独立运行（本地测试/无网关）视为管理员

    def _user_id(self):
        """网关注入的 X-Trim-Userid（可信身份，仅用于日志与展示，不做权限依据）。"""
        val = self.headers.get("X-Trim-Userid")
        if val is not None:
            return val.strip()
        return ""

    def _json(self, obj, code=200):
        # errors="replace"：路径里带非 UTF-8 字节时（Linux 上的常态），str 会含 surrogate，
        # 直接 encode("utf-8") 抛 UnicodeEncodeError 会让整个接口 500 ——
        # 一个怪文件名不该把音乐库、状态、日志全打挂。
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8", "replace")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def _read_json(self):
        try:
            n = int(self.headers.get("Content-Length") or 0)
            if n <= 0 or n > 5 * 1024 * 1024:
                return {}
            raw = self.rfile.read(n)
            return json.loads(raw.decode("utf-8"))
        except Exception:
            return {}

    def _serve_static(self, path):
        if path in ("/", "/index.html"):
            path = "/index.html"
        # 防止路径穿越
        rel = path.lstrip("/")
        full = os.path.realpath(os.path.join(WEB_DIR, rel))
        if not full.startswith(os.path.realpath(WEB_DIR) + os.sep) and full != os.path.realpath(WEB_DIR):
            self._json({"error": "forbidden"}, 403)
            return
        if not os.path.isfile(full):
            self._json({"error": "not found"}, 404)
            return
        ext = os.path.splitext(full)[1].lower()
        mime = {
            ".html": "text/html; charset=utf-8",
            ".js": "application/javascript; charset=utf-8",
            ".css": "text/css; charset=utf-8",
            ".png": "image/png",
            ".jpg": "image/jpeg",
            ".svg": "image/svg+xml",
            ".json": "application/json; charset=utf-8",
            ".ico": "image/x-icon",
        }.get(ext, "application/octet-stream")
        with open(full, "rb") as f:
            body = f.read()
        self.send_response(200)
        self.send_header("Content-Type", mime)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        self.wfile.write(body)

    # -- 媒体文件 ----------------------------------------------------------
    def _media_ok(self, path: str) -> bool:
        """仅允许访问授权音乐目录内的文件（含其 sidecar），或已入库文件。"""
        eng = ENGINE
        rp = os.path.realpath(path)
        for root in eng.all_folders():
            rr = os.path.realpath(root)
            if rp == rr or rp.startswith(rr + os.sep):
                return True
        # 兜底：转换/上传后已登记入库的文件（可能位于 uploads 临时目录）
        try:
            with eng._db() as c:
                row = c.execute(
                    "SELECT 1 FROM files WHERE path=? OR path=? LIMIT 1",
                    (path, rp)).fetchone()
            if row is not None:
                return True
        except Exception:
            pass
        return False

    def _serve_file(self, full: str, ctype: str, allow_range: bool = True, disposition: str = None):
        try:
            size = os.path.getsize(full)
        except OSError:
            self._json({"error": "not found"}, 404)
            return
        start, end = 0, size - 1
        code = 200
        rng = self.headers.get("Range") if allow_range else None
        if rng:
            m = re.match(r"bytes=(\d*)-(\d*)", rng)
            if m:
                if m.group(1):
                    start = int(m.group(1))
                if m.group(2):
                    end = min(int(m.group(2)), size - 1)
                if not m.group(1) and m.group(2):  # suffix range
                    start = max(0, size - int(m.group(2)))
                    end = size - 1
                if start > end or start >= size:
                    self.send_response(416)
                    self.send_header("Content-Range", f"bytes */{size}")
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                code = 206
        length = end - start + 1
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(length))
        self.send_header("Accept-Ranges", "bytes")
        if disposition:
            self.send_header("Content-Disposition", disposition)
        if code == 206:
            self.send_header("Content-Range", f"bytes {start}-{end}/{size}")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        try:
            with open(full, "rb") as f:
                f.seek(start)
                remain = length
                while remain > 0:
                    chunk = f.read(min(65536, remain))
                    if not chunk:
                        break
                    self.wfile.write(chunk)
                    remain -= len(chunk)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def _serve_media(self, q):
        """GET /api/media?path=...&kind=audio|cover|lyric"""
        path = q.get("path", [""])[0]
        kind = q.get("kind", ["audio"])[0]
        if not path or not os.path.isabs(path) or not self._media_ok(path):
            self._json({"error": "forbidden"}, 403)
            return
        if kind == "audio":
            full = path
        elif kind == "cover":
            stem = os.path.splitext(path)[0]
            # 只认逐曲同名封面；目录 cover.jpg 常与具体歌曲不符，不回退
            full = ""
            for cand in (stem + ".jpg", stem + ".jpeg", stem + ".png"):
                if os.path.exists(cand):
                    full = cand
                    break
        elif kind == "lyric":
            stem = os.path.splitext(path)[0]
            full = stem + ".lrc" if os.path.exists(stem + ".lrc") else ""
        else:
            self._json({"error": "bad kind"}, 400)
            return
        if not full or not os.path.isfile(full):
            self._json({"error": "not found"}, 404)
            return
        ext = os.path.splitext(full)[1].lower()
        if kind == "lyric":
            self._serve_file(full, "text/plain; charset=utf-8", allow_range=False)
            return
        mime = {
            ".mp3": "audio/mpeg", ".flac": "audio/flac", ".wav": "audio/wav",
            ".ogg": "audio/ogg", ".oga": "audio/ogg", ".m4a": "audio/mp4",
            ".aac": "audio/aac", ".ape": "audio/ape", ".wma": "audio/x-ms-wma",
            ".aiff": "audio/aiff", ".aif": "audio/aiff", ".dsf": "audio/dsf",
            ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
        }.get(ext, "application/octet-stream")
        self._serve_file(full, mime)

    # -- 分享（对外只读，token 即凭证） ------------------------------------
    def _share_base(self) -> str:
        """分享链接前缀：优先用设置里的 share_base，否则用当前访问地址 + 网关前缀。"""
        pl = (ENGINE._config.get("playlist", {}) or {})
        base = (pl.get("share_base") or "").strip().rstrip("/")
        if base:
            return base
        host = self.headers.get("Host") or ""
        if not host:
            return ""
        proto = (self.headers.get("X-Forwarded-Proto") or "http").split(",")[0].strip() or "http"
        return "%s://%s%s" % (proto, host, GATEWAY_PREFIX)

    def _api_share(self, p, q):
        """GET /api/share/<token> 歌单清单；GET /api/share/<token>/file?i= 直链下载。"""
        eng = ENGINE
        parts = p[len("/api/share/"):].strip("/").split("/")
        token = parts[0]
        if len(parts) == 1:
            data = eng.share_data(token, count_hit=True)
            if not data:
                self._json({"error": "分享链接已失效或被取消"}, 410)
                return
            self._json(data)
            return
        if len(parts) == 2 and parts[1] == "file":
            try:
                iid = int(q.get("i", ["0"])[0])
            except (TypeError, ValueError):
                iid = 0
            path = eng.share_file(token, iid)
            if not path:
                self._json({"error": "分享人本地暂无该文件，或链接已失效"}, 404)
                return
            ext = os.path.splitext(path)[1].lower()
            mime = {
                ".mp3": "audio/mpeg", ".flac": "audio/flac", ".wav": "audio/wav",
                ".ogg": "audio/ogg", ".m4a": "audio/mp4", ".ape": "audio/ape",
            }.get(ext, "application/octet-stream")
            name = quote(os.path.basename(path))
            self._serve_file(path, mime, disposition="attachment; filename*=UTF-8''" + name)
            return
        self._json({"error": "not found"}, 404)

    # -- 路由 ------------------------------------------------------------
    def do_GET(self):
        p = self._path()
        try:
            if p.startswith("/api/"):
                self._api_get(p, self._query())
            else:
                self._serve_static(p)
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as e:
            try:
                self._json({"error": str(e)}, 500)
            except Exception:
                pass

    def do_POST(self):
        p = self._path()
        try:
            if p == "/api/convert_upload":
                self._api_convert_upload()
                return
            if p.startswith("/api/"):
                self._api_post(p, self._read_json())
            else:
                self._json({"error": "not found"}, 404)
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as e:
            try:
                self._json({"error": str(e)}, 500)
            except Exception:
                pass

    # -- 本地上传转码 ------------------------------------------------------
    def _read_multipart(self, save_dir):
        """解析 multipart/form-data：文件写入 save_dir，文本字段返回 dict。

        返回 (fields, saved_paths)。文件名做净化（防路径穿越），
        同名文件自动追加序号。
        """
        ctype = self.headers.get("Content-Type", "")
        m = re.search(r'boundary="?([^;"]+)"?', ctype)
        if not m:
            raise ValueError("缺少 multipart boundary")
        boundary = ("--" + m.group(1)).encode()
        total = int(self.headers.get("Content-Length") or 0)
        if total <= 0:
            raise ValueError("空请求体")
        if total > 2 * 1024 * 1024 * 1024:
            raise ValueError("单次上传总量不能超过 2GB")
        # 分块读取整个 body（浏览器会先完整发送，Content-Length 已知）
        buf = b""
        remain = total
        while remain > 0:
            chunk = self.rfile.read(min(remain, 1024 * 1024))
            if not chunk:
                break
            buf += chunk
            remain -= len(chunk)
        fields, saved = {}, []
        parts = buf.split(boundary)
        for part in parts[1:]:
            if part.startswith(b"--"):  # 结束标记
                break
            head, _, body = part.partition(b"\r\n\r\n")
            if not _:
                continue
            body = body[:-2] if body.endswith(b"\r\n") else body  # 去掉尾部 CRLF
            name_m = re.search(rb'name="([^"]*)"', head)
            file_m = re.search(rb'filename="([^"]*)"', head)
            if not name_m:
                continue
            name = name_m.group(1).decode("utf-8", "replace")
            if file_m:
                fname = os.path.basename(
                    file_m.group(1).decode("utf-8", "replace").replace("\\", "/"))
                fname = re.sub(r'[:*?"<>|]', "_", fname).strip()
                if not fname or fname in (".", ".."):
                    continue
                target = os.path.join(save_dir, fname)
                i = 1
                stem, ext = os.path.splitext(fname)
                while os.path.exists(target):
                    target = os.path.join(save_dir, f"{stem}_{i}{ext}")
                    i += 1
                with open(target, "wb") as f:
                    f.write(body)
                saved.append(target)
            else:
                fields[name] = body.decode("utf-8", "replace")
        return fields, saved

    def _api_convert_upload(self):
        """POST /api/convert_upload — 本地上传加密音乐文件并转码。"""
        eng = ENGINE
        if not self._is_admin():
            self._json({"error": "仅管理员可执行此操作"}, 403)
            return
        upload_dir = os.path.join(DATA_DIR, "uploads",
                                  datetime.now().strftime("%Y%m%d_%H%M%S"))
        try:
            os.makedirs(upload_dir, exist_ok=True)
            fields, files = self._read_multipart(upload_dir)
        except Exception as e:
            self._json({"error": f"上传解析失败: {e}"}, 400)
            return
        if not files:
            self._json({"error": "未收到任何文件"}, 400)
            return
        out_dir = (fields.get("out_dir") or "").strip() or upload_dir
        auto_tidy = (fields.get("auto_tidy") or "1") != "0"
        try:
            os.makedirs(out_dir, exist_ok=True)
        except Exception as e:
            self._json({"error": f"无法创建输出目录: {e}"}, 400)
            return
        eng.log("info", f"本地上传转码：{len(files)} 个文件 -> {out_dir}")
        results = []
        for f in files:
            r = converter.convert_file(f, out_dir)
            item = {"src": os.path.basename(f)}
            if r["ok"]:
                img = r.pop("image", None)
                if img:
                    try:
                        with open(os.path.splitext(r["out_path"])[0] + ".jpg", "wb") as fh:
                            fh.write(img)
                    except Exception:
                        pass
                lrc = r.pop("lyric", None)
                if lrc:
                    try:
                        with open(os.path.splitext(r["out_path"])[0] + ".lrc",
                                  "w", encoding="utf-8") as fh:
                            fh.write(lrc)
                    except Exception:
                        pass
                item.update({"ok": True, "out_path": r["out_path"], "fmt": r["fmt"],
                             "has_image": bool(img), "has_lyric": bool(lrc)})
                if auto_tidy:
                    try:
                        eng.ingest(r["out_path"])
                        tr = eng._tidy_file(r["out_path"])
                        item["auto_tidy"] = tr["ok"]
                    except Exception:
                        item["auto_tidy"] = False
                # 转码成功后删除上传的密文临时文件
                try:
                    os.remove(f)
                except Exception:
                    pass
            else:
                item.update({"ok": False, "error": r.get("error") or "转换失败"})
            results.append(item)
        self._json({"ok": True, "results": results, "out_dir": out_dir})

    # -- 扫描范围 --------------------------------------------------------
    def _pick_folders(self, paths):
        """把候选目录夹回授权范围，返回 (folders 或 None, 错误文本 或 None)。

        没给任何路径时返回 None = 全部授权目录（保持旧行为）。
        被拒的不默默丢掉：用户选完却发现扫了全部，比报错难查得多。
        """
        paths = [p for p in (paths or []) if p]
        if not paths:
            return None, None
        ok, bad = ENGINE.clamp_folders(paths)
        if bad:
            return None, "这些目录不能作为扫描范围：" + "；".join(bad[:6]) + (
                f"；另有 {len(bad) - 6} 个" if len(bad) > 6 else "")
        if not ok:
            return None, "所选目录都不可用，请重新选择扫描目录"
        return ok, None

    def _req_folders(self, body):
        """POST 入口的扫描范围：folder_tokens（目录弹窗，无损）+ folders（直传路径）。

        解不开的 token 必须直接报错，不能当没给：默默丢掉它等于把用户以为的
        「只扫这一个歌手」静默换成「扫全部授权目录」，正好是一路反方向。GET 入口
        （?tokens=）已经会拒非法标识，POST 不能比它松。
        """
        paths, bad = [], 0
        for x in (body.get("folders") or []):
            if x:
                paths.append(str(x))
        toks = body.get("folder_tokens") or []
        if isinstance(toks, str):
            toks = [toks]
        for t in toks:
            if not t:
                continue
            p = tok_path(t)
            if p:
                paths.append(p)
            else:
                bad += 1
        if bad:
            return None, f"有 {bad} 个目录标识已失效，请重新打开选择窗口再选一次"
        return self._pick_folders(paths)

    def _query_folders(self, q):
        """GET 入口的扫描范围：?tokens=a,b（base64url 本身就是 URL 安全字符）。"""
        raw = (q.get("tokens", [""])[0] or "").strip()
        if not raw:
            return None, None
        paths, bad = [], 0
        for t in raw.split(","):
            t = t.strip()
            if not t:
                continue
            p = tok_path(t)
            if p:
                paths.append(p)
            else:
                bad += 1
        if bad:
            return None, "目录标识无效，请重新选择扫描目录"
        return self._pick_folders(paths)

    # -- API GET ---------------------------------------------------------
    def _api_get(self, p, q):
        eng = ENGINE
        if p == "/api/status":
            folders = eng.all_folders()
            with eng._db() as c:
                total = c.execute("SELECT COUNT(*) n FROM files").fetchone()["n"]
                no_lyric = c.execute("SELECT COUNT(*) n FROM files WHERE has_lyric=0").fetchone()["n"]
                no_cover = c.execute("SELECT COUNT(*) n FROM files WHERE has_cover=0").fetchone()["n"]
                dup_groups = c.execute(
                    "SELECT COUNT(*) n FROM (SELECT sha1 FROM files WHERE sha1<>'' GROUP BY sha1 HAVING COUNT(*)>1)").fetchone()["n"]
            self._json({
                "version": APP_VERSION,
                "app": "music-tidy",
                "running": True,
                "admin": self._is_admin(),
                "uid": self._user_id(),
                "llm_configured": bool(eng.llm and eng.llm.available),
                "system_folders": eng.system_folders(),
                "manual_folders": eng.manual_folders(),
                "folders": folders,
                "task": eng.task_status(),
                # 概览页要显“增量监听工作中”，靠 30s 一次的 /api/status 顺带带回去；
                # 前端另开一路轮询只会多一份请求，拿到的还是同一个全局单例
                "watcher": _watch_state(eng),
                "stats": {"files": total, "no_lyric": no_lyric, "no_cover": no_cover, "dup_groups": dup_groups},
            })
        elif p == "/api/config":
            self._json({"config": eng.get_config(secret_masked=True)})
        elif p == "/api/library":
            self._json(eng.library(
                folder=q.get("folder", [""])[0],
                search=q.get("search", [""])[0],
                tidied=q.get("tidied", [""])[0],
                missing=q.get("missing", [""])[0],
                code=q.get("code", [""])[0],
                limit=int(q.get("limit", ["100"])[0]),
                offset=int(q.get("offset", ["0"])[0]),
            ))
        elif p == "/api/media":
            self._serve_media(q)
        elif p == "/api/music/preview":
            # 试听：解析真实直链后 302 跳转
            import json as _json
            try:
                song = _json.loads(q.get("song", ["{}"])[0])
            except Exception:
                self._json({"error": "参数错误"}, 400)
                return
            url = eng.song_preview(song)
            if not url:
                self._json({"error": "未获取到试听地址（可能为 VIP 歌曲）"}, 404)
                return
            self.send_response(302)
            self.send_header("Location", url)
            self.send_header("Content-Length", "0")
            self.end_headers()
        elif p == "/api/music/lyric":
            import json as _json
            try:
                song = _json.loads(q.get("song", ["{}"])[0])
            except Exception:
                self._json({"error": "参数错误"}, 400)
                return
            lyric = eng.song_lyric(song)
            self._json({"ok": True, "lyric": lyric or ""})
        elif p == "/api/music/cover":
            # 封面兜底：搜索结果自带封面的直接 302，为空的跨源找一张。
            # 由浏览器自己跟随重定向，不经服务端中转图片字节，不占 NAS 带宽。
            import json as _json
            try:
                song = _json.loads(q.get("song", ["{}"])[0])
            except Exception:
                self._json({"error": "参数错误"}, 400)
                return
            url = eng.song_cover(song)
            if not url:
                self._json({"error": "未找到封面"}, 404)
                return
            self.send_response(302)
            self.send_header("Location", url)
            self.send_header("Cache-Control", "public, max-age=86400")
            self.send_header("Content-Length", "0")
            self.end_headers()
        elif p == "/api/dirs":
            # 目录浏览：只列授权目录内部的子目录，给「指定目录扫描」选范围用。
            # 默认带上子树音频数（受引擎预算限制），前端才能按目录显示“这个文件夹里有多少首”。
            try:
                lim = int(q.get("limit", ["300"])[0] or 300)
            except ValueError:
                lim = 300
            payload, code = eng.browse_dirs(
                (q.get("token", [""])[0] or "").strip(),
                limit=max(1, min(1000, lim)),
                counts=(q.get("counts", ["1"])[0] or "1") != "0")
            self._json(payload, code)
        elif p == "/api/scan/audit":
            # 扫描体检：只走目录树不对比入库，回答“哪些音频没被扫到、为什么”。
            # 磁盘遍历可能耗时几秒，上一轮没结束就拒绝，避免并发扫盘拖死 NAS。
            scope, err = self._query_folders(q)
            if err:
                self._json({"error": err}, 400)
                return
            if not AUDIT_LOCK.acquire(blocking=False):
                self._json({"error": "体检正在进行中，请稍候"}, 409)
                return
            try:
                self._json({"ok": True, "audit": eng.audit_scan(scope),
                            "scoped": bool(scope)})
            finally:
                AUDIT_LOCK.release()
        elif p == "/api/scan/denied":
            # 读不到的目录：拿上次枚举的快照 + 当下重新取证。不踩磁盘遍历（只 stat 几个
            # 目录），所以不用 AUDIT_LOCK，概览页加载时就能放心调。
            self._json(eng.denied_dirs())
        elif p == "/api/duplicates":
            kind = q.get("kind", ["both"])[0]
            if kind not in ("both", "content", "name"):
                kind = "both"
            self._json({"groups": eng.duplicates(kind)})
        elif p == "/api/duplicate_covers":
            th = int(q.get("threshold", ["3"])[0] or 3)
            self._json(eng.duplicate_covers(th))
        elif p == "/api/code_stats":
            self._json(eng.code_stats())
        elif p == "/api/log":
            self._json({"logs": eng.recent_logs(int(q.get("limit", ["200"])[0]))})
        elif p == "/api/watch":
            self._json({"watcher": _watch_state(eng)})
        elif p == "/api/ai/usage":
            # 设置页 AI 面板：今日 / 总计 token 与次数。只读聚合，不碰任何文件
            self._json(eng.ai_usage_stats(int((q.get("days") or ["7"])[0] or 7)))
        elif p == "/api/playlists":
            d = eng.playlists()
            d["share_base_url"] = self._share_base()
            self._json(d)
        elif p == "/api/playlist/items":
            pid = int(q.get("pid", ["0"])[0] or 0)
            row = eng._playlist_row(pid) or {}
            self._json({"ok": True, "playlist": row.get("name", ""),
                        "fav": bool(row.get("fav")), "items": eng.playlist_items(pid)})
        elif p == "/api/favorites":
            self._json({"ok": True, "paths": eng.favorite_paths()})
        elif p == "/api/share/preview":
            # 在服务端做本地匹配，才能区分「对方有」与「你已有」两回事
            out = eng.share_preview(q.get("url", [""])[0])
            if out.get("error"):
                self._json({"error": out["error"]}, 400)
                return
            self._json(out)
        elif p.startswith("/api/share/"):
            self._api_share(p, q)
        elif p == "/api/playlist/parse":
            text = q.get("text", [""])[0]
            parsed = playlist_mod.parse_link(text)
            if not parsed:
                self._json({"error": "无法识别歌单链接"}, 400)
                return
            try:
                pl = playlist_mod.fetch_playlist(*parsed)
            except playlist_mod.PlaylistError as e:
                self._json({"error": str(e)}, 502)
                return
            self._json({
                "ok": True, "platform": parsed[0], "playlist_name": pl["name"],
                "cover": pl.get("cover", ""), "total": len(pl["songs"]),
                "limited": pl.get("limited", False),
                "songs": pl["songs"][:30],
            })
        else:
            self._json({"error": "not found"}, 404)

    # -- API POST --------------------------------------------------------
    def _api_post(self, p, body):
        eng = ENGINE
        if not self._is_admin():
            self._json({"error": "仅管理员可执行此操作"}, 403)
            return
        if p == "/api/config":
            new_cfg = body.get("config") or {}
            # 防止把脱敏 key 或空值覆盖回真实 key（留空 = 保持已保存的 key 不变）
            if isinstance(new_cfg.get("llm"), dict):
                cur = eng._config.get("llm", {})
                nk = new_cfg["llm"].get("api_key", "")
                if (not nk) or ("****" in nk):
                    new_cfg["llm"]["api_key"] = cur.get("api_key", "")
                nimg = new_cfg["llm"].get("image")
                if isinstance(nimg, dict):
                    cik = nimg.get("api_key", "")
                    if (not cik) or ("****" in cik):
                        nimg["api_key"] = (cur.get("image") or {}).get("api_key", "")
            # 音源自定义 API token 脱敏保护（留空/掩码 = 保持已保存值）
            if isinstance(new_cfg.get("music_source"), dict):
                cur_apis = {a.get("url"): a.get("token", "")
                            for a in (eng._config.get("music_source", {}) or {}).get("custom_apis") or []}
                for a in new_cfg["music_source"].get("custom_apis") or []:
                    t = a.get("token", "")
                    if (not t) or ("****" in t):
                        a["token"] = cur_apis.get(a.get("url"), "")
                # Cookie 脱敏保护（留空/掩码 = 保持已保存值）
                cur_ck = (eng._config.get("music_source", {}) or {}).get("cookies") or {}
                new_ck = new_cfg["music_source"].get("cookies")
                if isinstance(new_ck, dict):
                    for k in list(new_ck.keys()):
                        v = new_ck.get(k, "")
                        if (not v) or ("****" in v):
                            new_ck[k] = cur_ck.get(k, "")
            eng.merge_config(new_cfg)
            eng.llm = eng.make_llm(enabled=eng._config.get("enable_llm"))
            save_config(eng._config, CONFIG_PATH)
            self._json({"ok": True, "config": eng.get_config(secret_masked=True)})
        elif p == "/api/folders":
            folders = eng._config.get("folders") or []
            path = (body.get("path") or "").strip().rstrip("/")
            if path and path not in folders:
                folders.append(path)
            eng._config["folders"] = folders
            save_config(eng._config, CONFIG_PATH)
            self._json({"ok": True, "folders": folders})
        elif p == "/api/folders/del":
            path = (body.get("path") or "").strip().rstrip("/")
            folders = [f for f in (eng._config.get("folders") or []) if f != path]
            eng._config["folders"] = folders
            save_config(eng._config, CONFIG_PATH)
            self._json({"ok": True, "folders": folders})
        elif p == "/api/scan":
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行"}, 409)
                return
            folders, err = self._req_folders(body)
            if err:
                self._json({"error": err}, 400)
                return
            t = threading.Thread(target=eng.scan, args=(folders,), daemon=True)
            t.start()
            self._json({"ok": True, "task": "scan", "scoped": bool(folders)})
        elif p == "/api/scan/probe":
            # 仅全量扫描：只枚举目录数文件，不入库 / 不算 SHA-1 / 不改任何文件，
            # 逐目录原因全部写进日志，用于回答“哪些文件夹没扫出来、为什么”。
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            folders, err = self._req_folders(body)
            if err:
                self._json({"error": err}, 400)
                return
            t = threading.Thread(target=eng.probe_scan, args=(folders,), daemon=True)
            t.start()
            self._json({"ok": True, "task": "probe", "scoped": bool(folders)})
        elif p == "/api/tidy":
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行"}, 409)
                return
            folders, err = self._req_folders(body)
            if err:
                self._json({"error": err}, 400)
                return
            files = body.get("files") or None
            t = threading.Thread(target=eng.tidy, kwargs={"folders": folders, "files": files}, daemon=True)
            t.start()
            self._json({"ok": True, "task": "tidy", "scoped": bool(folders)})
        elif p == "/api/re_tidy":
            # 单曲重新整理 / 编辑识别信息后重新搜刮（同步执行，返回明细）
            path = (body.get("path") or "").strip()
            if not path:
                self._json({"error": "需要 path"}, 400)
                return
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            fields = {k: body.get(k) or "" for k in ("artist", "title", "album", "style")}
            res = eng.re_tidy(path, fields)
            self._json({"ok": bool(res.get("ok")), "result": res,
                        "error": "" if res.get("ok") else (res.get("reason") or "整理失败")})
        elif p == "/api/duplicates/resolve":
            keep = body.get("keep") or ""
            deletes = body.get("delete") or []
            mode = body.get("mode") or "trash"
            res = eng.resolve_duplicates(keep, deletes, mode)
            self._json({"ok": True, "results": res})
        elif p == "/api/library/delete":
            paths = body.get("paths") or []
            if not isinstance(paths, list) or not paths:
                self._json({"error": "需要 paths 数组"}, 400)
                return
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            res = eng.library_delete(paths)
            self._json({"ok": True, "result": res})
        elif p == "/api/library/organize":
            out_dir = (body.get("out_dir") or "").strip().rstrip("/")
            if not out_dir or not os.path.isabs(out_dir):
                self._json({"error": "请指定绝对路径的目标文件夹 out_dir"}, 400)
                return
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            try:
                os.makedirs(out_dir, exist_ok=True)
            except Exception as e:
                self._json({"error": f"无法创建目标文件夹: {e}"}, 400)
                return
            probe = os.path.join(out_dir, ".mt_write_test")
            try:
                os.makedirs(probe)
                os.rmdir(probe)
            except Exception as e:
                self._json({"error": f"应用对目标文件夹没有写入权限: {e}。"
                                     "应用以 music-tidy 用户运行，请在飞牛文件管理器中"
                                     "检查该文件夹权限（为应用授权写入），"
                                     "或改用已授权音乐库内的目录"}, 400)
                return
            t = threading.Thread(target=eng.organize_move, args=(out_dir,), daemon=True)
            t.start()
            self._json({"ok": True, "task": "organize"})
        elif p == "/api/library/analyze":
            path = (body.get("path") or "").strip()
            if not path or not os.path.isabs(path) or not os.path.exists(path):
                self._json({"error": "文件不存在"}, 400)
                return
            # prompt：用户在「AI 完整分析」上手写的提示词，非空时占最高优先级
            res = eng.analyze_song(path, body.get("prompt") or "")
            if not res.get("ok") and res.get("error"):
                self._json({"error": res["error"]})
            else:
                self._json(res)
        elif p == "/api/library/classify":
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            t = threading.Thread(target=eng.classify_style, daemon=True)
            t.start()
            self._json({"ok": True, "task": "classify"})
        elif p == "/api/library/touch":
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            t = threading.Thread(target=eng.touch_library, daemon=True)
            t.start()
            self._json({"ok": True, "task": "touch"})
        elif p == "/api/library/split-cue":
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            t = threading.Thread(target=eng.split_cue_files, daemon=True)
            t.start()
            self._json({"ok": True, "task": "split_cue"})
        elif p == "/api/music/search":
            keyword = body.get("keyword", "").strip()
            if not keyword:
                self._json({"error": "请输入搜索关键词"}, 400)
                return
            try:
                results = eng.search_songs(keyword, limit=int(body.get("limit", 20)))
                self._json({"ok": True, "songs": results})
            except Exception as e:
                self._json({"error": f"搜索失败: {e}"}, 500)
        elif p == "/api/music/download":
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            songs = body.get("songs", [])
            if not songs:
                self._json({"error": "请选择要下载的歌曲"}, 400)
                return
            t = threading.Thread(target=eng.download_songs, args=(songs,), daemon=True)
            t.start()
            self._json({"ok": True, "task": "download"})
        elif p == "/api/music/download-playlist":
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            link = body.get("link", "").strip()
            if not link:
                self._json({"error": "请输入歌单链接"}, 400)
                return
            t = threading.Thread(target=eng.download_playlist, args=(link,), daemon=True)
            t.start()
            self._json({"ok": True, "task": "dl_playlist"})
        elif p == "/api/watch":
            global WATCHER
            action = body.get("action") or ""
            if action == "start":
                if not WATCHER or not WATCHER.is_alive():
                    WATCHER = MusicWatcher(eng, int(eng._config.get("watch_interval", 30)))
                    WATCHER.start()
                eng._config["enable_watch"] = True
            elif action == "stop":
                if WATCHER and WATCHER.is_alive():
                    WATCHER.stop()
                eng._config["enable_watch"] = False
            elif action == "set_interval":
                sec = max(5, int(body.get("interval", 30)))
                if WATCHER and WATCHER.is_alive():
                    WATCHER.set_interval(sec)
                eng._config["watch_interval"] = sec
            else:
                self._json({"error": "action 必须为 start/stop/set_interval"}, 400)
                return
            save_config(eng._config, CONFIG_PATH)
            self._json({"ok": True, "watcher": _watch_state(eng)})
        elif p == "/api/playlist/import":
            text = body.get("text") or ""
            out_dir = (body.get("out_dir") or "").strip()
            mode = body.get("mode") or "m3u"
            if not out_dir:
                self._json({"error": "请指定输出目录 out_dir"}, 400)
                return
            if not os.path.isdir(out_dir):
                self._json({"error": f"输出目录不存在: {out_dir}"}, 400)
                return
            try:
                result = playlist_mod.import_playlist(eng, text, out_dir, mode)
            except playlist_mod.PlaylistError as e:
                self._json({"error": str(e)}, 502)
                return
            self._json(result)
        elif p == "/api/convert":
            files = body.get("files") or []
            out_dir = (body.get("out_dir") or "").strip()
            auto_tidy = body.get("auto_tidy", True)
            if not files or not out_dir:
                self._json({"error": "请指定待转换文件列表 files 与输出目录 out_dir"}, 400)
                return
            try:
                os.makedirs(out_dir, exist_ok=True)
            except Exception as e:
                self._json({"error": f"无法创建输出目录: {e}"}, 400)
                return
            results = []
            for f in files:
                r = converter.convert_file(f, out_dir)
                if r["ok"]:
                    # 写出封面 sidecar（NCM 内嵌封面）；image 为 bytes 不入 JSON
                    img = r.pop("image", None)
                    r["has_image"] = bool(img)
                    if img:
                        try:
                            with open(os.path.splitext(r["out_path"])[0] + ".jpg", "wb") as fh:
                                fh.write(img)
                        except Exception:
                            pass
                    # 写出歌词 sidecar（NCM 内嵌歌词），须在 ingest 前
                    lrc = r.pop("lyric", None)
                    r["has_lyric"] = bool(lrc)
                    if lrc:
                        try:
                            with open(os.path.splitext(r["out_path"])[0] + ".lrc",
                                      "w", encoding="utf-8") as fh:
                                fh.write(lrc)
                        except Exception:
                            pass
                    # 自动入库 + 增量刮削歌词/封面/分类
                    if auto_tidy:
                        eng.ingest(r["out_path"])
                        tr = eng._tidy_file(r["out_path"])
                        r["auto_tidy"] = tr["ok"]
                results.append(r)
            self._json({"ok": True, "results": results})
        elif p == "/api/convert_selftest":
            # 内置转码自检：验证 QMC/NCM 解密算法在当前环境下可用（无需外部测试数据）
            rows = []
            for name, ok in converter.run_selftest():
                rows.append({"name": name, "pass": ok is True,
                             "skip": str(ok).startswith("skip"),
                             "detail": "" if ok is True else str(ok)})
            self._json({"ok": all(r["pass"] or r["skip"] for r in rows), "results": rows})
        elif p == "/api/task":
            action = body.get("action") or ""
            if action == "pause":
                eng.pause_task()
                self._json({"ok": True, "task": eng.task_status()})
            elif action == "resume":
                eng.resume_task()
                self._json({"ok": True, "task": eng.task_status()})
            elif action == "stop":
                if not eng.task_status().get("running"):
                    self._json({"error": "当前没有运行中的任务"}, 409)
                    return
                eng.stop_task()
                self._json({"ok": True, "task": eng.task_status()})
            elif action == "reset":
                if eng.task_status().get("running"):
                    self._json({"error": "任务运行中，请先停止"}, 409)
                    return
                scope, err = self._req_folders(body)
                if err:
                    self._json({"error": err}, 400)
                    return
                # 「重新整理」也必须跟着范围走：不然范围外那些已整理的文件被重置成
                # 未整理，却又本轮不会被整理，表现为“重新整理一次，半数变未整理”。
                n = eng.reset_tidied(scope)
                self._json({"ok": True, "reset": n, "scoped": bool(scope)})
            else:
                self._json({"error": "action 必须为 pause/resume/stop/reset"}, 400)
        elif p == "/api/dedupe_all":
            mode = body.get("mode") or "move"
            out_dir = (body.get("out_dir") or "").strip()
            ai = bool(body.get("ai"))
            kind = body.get("kind") or "both"
            if kind not in ("both", "content", "name"):
                kind = "both"
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请稍候"}, 409)
                return
            if ai and not (eng.llm and eng.llm.available):
                self._json({"error": "AI 去重需先在设置页配置并启用 LLM"}, 400)
                return
            res = eng.dedupe_all(mode=mode, out_dir=out_dir, ai=ai, kind=kind)
            if res.get("error"):
                self._json({"error": res["error"]}, 400)
                return
            self._json({"ok": True, "result": res})
        elif p == "/api/ai_complete":
            files = body.get("files") or []
            if files:
                # 单文件：同步执行并返回明细
                if len(files) == 1:
                    res = eng.ai_complete(files[0])
                    self._json({"ok": bool(res.get("ok")), "result": res,
                                "error": res.get("error") or ""})
                    return
                self._json({"error": "批量补全请不传 files 或走后台任务"}, 400)
                return
            # 批量：后台任务处理全部缺失文件（先做同步前置检查）
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请稍候"}, 409)
                return
            if not (eng.llm and eng.llm.available):
                self._json({"error": "AI 补全需先在设置页配置并启用 LLM"}, 400)
                return
            if not eng.missing_files(limit=1):
                self._json({"error": "没有缺失歌词/封面的文件"}, 400)
                return
            t = threading.Thread(target=eng.ai_complete_task, kwargs={"files": None}, daemon=True)
            t.start()
            self._json({"ok": True, "task": "ai"})
        elif p == "/api/duplicate_covers/fix":
            th = int(body.get("threshold", 3) or 3)
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请稍候"}, 409)
                return
            if not (eng.llm and eng.llm.available):
                self._json({"error": "AI 修复封面需先在设置页配置并启用 LLM"}, 400)
                return
            det = eng.duplicate_covers(th)
            if not det["groups"]:
                self._json({"error": "未检测到大量相同的封面"}, 400)
                return
            t = threading.Thread(target=eng.fix_duplicate_covers, kwargs={"threshold": th}, daemon=True)
            t.start()
            self._json({"ok": True, "task": "cover", "groups": len(det["groups"])})
        elif p == "/api/export_code_playlists":
            out_dir = (body.get("out_dir") or "").strip()
            res = eng.export_code_playlists(out_dir)
            if res.get("error"):
                self._json({"error": res["error"]}, 400)
                return
            self._json(res)
        elif p == "/api/llm/test":
            # 用当前已保存的配置真实测试一次连通性
            res = eng.make_llm(enabled=True).test_connection()
            eng.log("info", "LLM 测试：%s" % ("成功" if res["ok"] else ("失败 - " + res["error"])))
            self._json(res)
        elif p == "/api/rules/analyze":
            sample = (body.get("sample") or "").strip()
            desc = (body.get("desc") or "").strip()
            if not sample:
                self._json({"error": "请先填写文件名样例"}, 400)
                return
            if not (eng.llm and eng.llm.available):
                self._json({"error": "AI 分析规则需先在设置页配置并启用 LLM"}, 400)
                return
            out = eng.llm.analyze_name_rule(sample, desc)
            if not out:
                self._json({"error": "AI 分析失败，请调整样例或解释后重试"}, 502)
                return
            chk = test_rule_regex(out.get("regex", ""), sample)
            if chk["ok"]:
                self._json({"ok": True, "regex": chk["regex"],
                            "note": out.get("note", ""), "parsed": chk["parsed"]})
            else:
                # 校验失败不走 error（前端会抛异常），改用 warn 让用户在弹窗里修正
                self._json({"ok": False, "regex": chk["regex"],
                            "note": out.get("note", ""), "warn": chk["error"], "parsed": {}})
        elif p == "/api/rules/add":
            sample = (body.get("sample") or "").strip()
            desc = (body.get("desc") or "").strip()
            regex = (body.get("regex") or "").strip()
            note = (body.get("note") or "").strip()
            chk = test_rule_regex(regex, sample)
            if not chk["ok"]:
                self._json({"error": "规则校验未通过：" + chk["error"]}, 400)
                return
            rule = {"id": "r" + datetime.now().strftime("%H%M%S") + os.urandom(2).hex(),
                    "sample": sample, "desc": desc, "regex": chk["regex"],
                    "note": note, "enabled": True}
            rules = list(eng._config.get("name_rules") or [])
            rules.append(rule)
            eng._config["name_rules"] = rules
            save_config(eng._config, CONFIG_PATH)
            eng.log("info", "新增自定义名称规则 %s" % (sample or chk["regex"]))
            self._json({"ok": True, "rules": rules})
        elif p == "/api/rules/del":
            rid = (body.get("id") or "").strip()
            rules = [r for r in (eng._config.get("name_rules") or [])
                     if not (isinstance(r, dict) and r.get("id") == rid)]
            eng._config["name_rules"] = rules
            save_config(eng._config, CONFIG_PATH)
            self._json({"ok": True, "rules": rules})
        # ---- 歌单 / 我的喜欢 / 归档 / 分享 -------------------------------
        elif p == "/api/playlist/create":
            res = eng.playlist_create(body.get("name") or "")
            self._json(res, 400 if res.get("error") else 200)
        elif p == "/api/playlist/update":
            asy = body.get("auto_sync")
            res = eng.playlist_update(
                int(body.get("id") or 0), name=body.get("name"),
                archive_dir=body.get("archive_dir"), archive_mode=body.get("archive_mode"),
                auto_sync=None if asy is None else bool(asy))
            self._json(res, 400 if res.get("error") else 200)
        elif p == "/api/playlist/delete":
            res = eng.playlist_delete(int(body.get("id") or 0))
            self._json(res, 400 if res.get("error") else 200)
        elif p == "/api/playlist/add":
            res = eng.playlist_add(int(body.get("pid") or 0),
                                   path=(body.get("path") or "").strip(),
                                   song=body.get("song") or None)
            self._json(res, 400 if res.get("error") else 200)
        elif p == "/api/playlist/remove":
            self._json(eng.playlist_remove(int(body.get("item_id") or 0)))
        elif p == "/api/fav":
            res = eng.fav_toggle(path=(body.get("path") or "").strip(),
                                 song=body.get("song") or None)
            self._json(res, 400 if res.get("error") else 200)
        elif p == "/api/playlist/archive":
            pid = int(body.get("id") or 0)
            if not pid:
                self._json({"error": "需要歌单 id"}, 400)
                return
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            croot, cmode, _auto = eng._archive_conf(pid)
            root = (body.get("root") or croot or "").strip()
            mode = (body.get("mode") or cmode or "copy").strip()
            if not root or not os.path.isabs(root):
                self._json({"error": "请先设置归档目录（设置 → 歌单与分享）"}, 400)
                return
            t = threading.Thread(target=eng.archive_playlist, args=(pid,),
                                 kwargs={"mode": mode, "root": root}, daemon=True)
            t.start()
            self._json({"ok": True, "task": "playlist_archive", "mode": mode, "root": root})
        elif p == "/api/playlist/import-share":
            url = (body.get("url") or "").strip()
            if not url:
                self._json({"error": "请粘贴分享链接"}, 400)
                return
            res = eng.share_import(url, (body.get("name") or "").strip())
            self._json(res, 400 if res.get("error") else 200)
        elif p == "/api/playlist/fetch-pending":
            pid = int(body.get("id") or 0)
            if not pid:
                self._json({"error": "需要歌单 id"}, 400)
                return
            if eng.task_status().get("running"):
                self._json({"error": "已有任务在运行，请等待完成后再试"}, 409)
                return
            t = threading.Thread(target=eng.share_fetch, args=(pid,), daemon=True)
            t.start()
            self._json({"ok": True, "task": "share_fetch"})
        elif p == "/api/share/create":
            res = eng.share_create(int(body.get("pid") or 0))
            if not res.get("error"):
                res["url"] = "%s/api/share/%s" % (self._share_base(), res["token"])
            self._json(res, 400 if res.get("error") else 200)
        elif p == "/api/share/revoke":
            self._json(eng.share_revoke((body.get("token") or "").strip()))
        else:
            self._json({"error": "not found"}, 404)


if hasattr(socketserver, "UnixStreamServer"):
    class ThreadingUnixHTTPServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
        daemon_threads = True
        allow_reuse_address = True
else:
    # Windows 本地开发无 Unix Socket，仅测试模式（--port）可用
    ThreadingUnixHTTPServer = None


# ---------------------------------------------------------------------------
# 启动
# ---------------------------------------------------------------------------

def main():
    global ENGINE, CONFIG_PATH, WATCHER, DATA_DIR
    ap = argparse.ArgumentParser(description="轻乐集服务")
    ap.add_argument("--socket", default="", help="Unix socket 路径（网关模式）")
    ap.add_argument("--port", type=int, default=0, help="TCP 端口（测试模式）")
    ap.add_argument("--config", default="", help="配置文件路径")
    ap.add_argument("--data", default="", help="数据目录")
    args = ap.parse_args()

    CONFIG_PATH = args.config
    data_dir = args.data or os.environ.get("TRIM_PKGVAR", os.path.join(os.getcwd(), "data"))
    DATA_DIR = data_dir
    cfg = load_config(CONFIG_PATH)
    ENGINE = TidyEngine(data_dir, cfg, llm=None)
    ENGINE.llm = ENGINE.make_llm(enabled=cfg.get("enable_llm"))
    ENGINE.log("info", f"MusicTidy v{APP_VERSION} 启动，数据目录: {data_dir}")
    ENGINE.log("info", f"系统授权目录: {cfg.get('system_folders') or []}")

    # 增量监听（默认开启，由配置 enable_watch 控制）
    if cfg.get("enable_watch", True):
        WATCHER = MusicWatcher(ENGINE, int(cfg.get("watch_interval", 30)))
        WATCHER.start()

    if args.socket:
        if os.path.exists(args.socket):
            os.remove(args.socket)
        httpd = ThreadingUnixHTTPServer(args.socket, Handler)
        os.chmod(args.socket, 0o666)
        ENGINE.log("info", f"监听 Unix Socket: {args.socket}")
    elif args.port:
        httpd = ThreadingHTTPServer(("0.0.0.0", args.port), Handler)
        ENGINE.log("info", f"监听 TCP: {args.port}")
    else:
        print("必须指定 --socket 或 --port", file=sys.stderr)
        return 1

    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        httpd.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
