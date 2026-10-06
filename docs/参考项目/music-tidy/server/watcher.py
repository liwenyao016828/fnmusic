# -*- coding: utf-8 -*-
"""
watcher.py — 增量监听（纯 Python 标准库）

轮询授权目录，对比快照 (mtime,size)，发现新增/变化音频后自动入库并增量整理
（自动匹配歌词、封面、去重、分类），实现"搜刮建库 + 增量自动整理"。
"""

import os
import threading
import time
import traceback

from scraper import walk_audio


class MusicWatcher(threading.Thread):
    def __init__(self, engine, interval=30):
        super().__init__(daemon=True, name="MusicWatcher")
        self.engine = engine
        self.interval = max(5, int(interval))
        self._stop_evt = threading.Event()
        self._snapshot = {}
        self.last_audit = {"complete": True}
        self.last_run = {"time": 0, "changed": 0, "ok": 0, "fail": 0}
        # “工作中”标识：旧版只在发现新文件时才写日志，曲库几千首时每轮枚举要跑几十秒，
        # 前端看上去和“监听没生效”一模一样。这里把当下在做什么挂出来，不用猜。
        self.busy = False
        self.stage = ""
        self.files = 0          # 当前快照里的音频数（= 监听覆盖多少首）
        self.last_check = 0.0   # 上一轮枚举走完的时刻（无事可做也会更新）
        self.last_found = 0     # 上一轮发现的新增/变化数
        self.checks = 0         # 已检查轮数
        self._enum_at = 0.0

    # ------------------------------------------------------------------ api
    def stop(self):
        self._stop_evt.set()

    @staticmethod
    def _ts(ts):
        try:
            return time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(ts)) if ts else ""
        except Exception:
            return ""

    def status(self):
        return {
            "running": self.is_alive() and not self._stop_evt.is_set(),
            "interval": self.interval,
            "last_run": self.last_run,
            "busy": bool(self.busy),
            "stage": self.stage if self.busy else "",
            "files": self.files,
            "checks": self.checks,
            "last_found": self.last_found,
            "last_check": self._ts(self.last_check),
            "last_time": self._ts((self.last_run or {}).get("time") or 0),
            "next_in": max(0, int(self.interval - (time.time() - self.last_check)))
            if (self.last_check and not self.busy) else None,
        }

    def set_interval(self, seconds):
        self.interval = max(5, int(seconds))

    # ---------------------------------------------------------------- core
    def _walk_folders(self):
        """枚举当前音频快照 path -> (mtime, size)。

        与手动扫描共用 scraper.walk_audio（v1.9.0）：旧版自己写 os.walk 且不带
        onerror，子目录读不了会被静默吞掉，这些文件从快照里消失后就被
        drop_missing 删掉入库记录——每隔几十秒重复一次，库就越来越少。

        枚举本身可能跑几十秒，期间靠 on_dir 刷新 stage，前端的“工作中”小标才不至于
        长时间停在上一轮的文本上。
        """
        cfg = self.engine._config

        def _prog(info):
            now = time.time()
            if now - self._enum_at < 0.5:
                return
            self._enum_at = now
            self.stage = (f"检查中 · 已遍历 {info.get('dirs', 0)} 个子目录"
                          f" · 音频 {info.get('audio', 0)}")

        self.busy = True
        self.stage = "检查中 · 正在遍历目录"
        paths, audit = walk_audio(
            self.engine.enabled_folders(),
            exclude=cfg.get("exclude_dirs") or [],
            max_depth=int(cfg.get("max_depth", 12) or 12),
            on_dir=_prog)
        self.last_audit = audit
        self.stage = "检查中 · 正在比对文件变化"
        out = {}
        for p in paths:
            try:
                st = os.stat(p)
            except OSError:
                continue
            out[p] = (round(st.st_mtime, 3), st.st_size)
        self.files = len(out)
        return out

    def run(self):
        try:
            self._snapshot = self._walk_folders()
        except Exception:
            self._snapshot = {}
        finally:
            self.busy = False
            self.stage = ""
        self.last_check = time.time()
        self.engine.log("info", f"增量监听已启动，间隔 {self.interval}s（首轮快照 {self.files} 首）")
        while not self._stop_evt.is_set():
            if self._stop_evt.wait(self.interval):
                break
            try:
                self._tick()
            except Exception as e:
                self.engine.log("error", f"监听扫描异常: {e}\n{traceback.format_exc()}")
            finally:
                self.busy = False
                self.stage = ""
                self.last_check = time.time()
                self.checks += 1

    def _tick(self):
        current = self._walk_folders()
        complete = bool(self.last_audit.get("complete", True))
        if not current and self._snapshot:
            # 整个曲库突然枚举为空：几乎肯定是挂载/权限抖动，不拿它去对比
            self.engine.log("warn", "增量监听：本次未枚举到任何音频，保留上一次快照，不做清理")
            return
        changed = [p for p, sig in current.items() if self._snapshot.get(p) != sig]
        removed = [p for p in self._snapshot if p not in current]
        self._snapshot = current

        stats = {"changed": 0, "ok": 0, "fail": 0}
        if removed and not complete:
            self.engine.log(
                "warn", f"增量监听：目录枚举不完整（权限/深度），本轮跳过 {len(removed)} 条入库记录清理")
            removed = []
        if removed:
            self.engine.drop_missing(removed)
            self.engine.log("info", f"增量监听: {len(removed)} 个文件已移除，同步清理入库记录")
        if changed:
            stats["changed"] = len(changed)
            self.engine.log("info", f"增量监听: 发现 {len(changed)} 个新增/变化文件，开始自动整理")
            # 入库（新增/变化才整理）
            self.stage = f"发现 {len(changed)} 个变化 · 入库中"
            need = self.engine.ingest_many(changed)
            for i, p in enumerate(need):
                self.stage = f"自动整理中 {i + 1}/{len(need)} · {os.path.basename(p)}"
                r = self.engine._tidy_file(p)
                if r["ok"]:
                    stats["ok"] += 1
                else:
                    stats["fail"] += 1
            self.engine.log(
                "info",
                f"增量整理完成: 变化{stats['changed']} 新增待整理{len(need)} 成功{stats['ok']} 失败{stats['fail']}")
        self.last_found = stats["changed"]
        self.last_run = {"time": time.time(), **stats}
