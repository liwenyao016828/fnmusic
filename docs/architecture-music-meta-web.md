# music-meta-web v1.4.8 服务端架构分析

分析对象：`/home/liwenyao/projects/music-v2/.refs/music-meta-web`（tag v1.4.8，Python 3 + FastAPI + SQLite）。
目的：让另一个项目（Go）的作者能据此重新设计自己的「扫描 → 刮削 → 写入」流水线。

代码规模与职责：

| 文件 | 行数 | 职责 |
|---|---|---|
| `app/server/webapp/scheduler.py` | 832 | 后台刮削调度器（线程 + 匹配 + 写入 + 缓存编排） |
| `app/server/webapp/db.py` | 863 | 全部 SQLite 访问（5 张表 + 永久记忆 + 状态机） |
| `app/server/webapp/main.py` | 1277 | FastAPI 路由（33 个端点 + 1 中间件 + 1 startup hook）+ 扫描/导出后台任务 |
| `app/server/run_server.py` | 224 | 飞牛 fnOS 启动器：写插件目录、pip 补依赖、监听 Unix Socket、清残留进程 |
| `app/server/musicmeta/cache.py` | 297 | 应用级通用缓存（所有数据源插件共用） |
| `app/server/musicmeta/filenames.py` | 350 | 文件名清洗 → 候选查询词 → 反推校验 |
| `app/server/musicmeta/ratelimit.py` | 38 | 进程级全局限速器（跨源、跨线程共享） |
| `app/server/musicmeta/fields.py` | 87 | 字段定义单一来源 + 「生效字段」解析 |
| `app/server/musicmeta/writer.py` | 533 | mutagen 写标签（含写入侧第二道字段闸门） |
| `app/server/musicmeta/sources/registry.py` | 112 | 插件注册表（按文件路径 import） |
| `data-source-plugins/*.py` | 7 个 | qqmusic / netease / kugou / kuwo / lrclib / theaudiodb / itunes 插件 |

数据流（一次完整任务）：

```
POST /api/scan ──► _scan_worker(main.py:224)
                     ├─ collect_audio_files  (scheduler.py:30)   只列文件，不读标签
                     ├─ memory_status_map    (db.py:607)         文件级永久记忆比对
                     └─ add_tasks            (db.py:217)         写 tasks 表 status=pending
POST /api/run  ──► Scraper.run (scheduler.py:579)
                     ├─ _write_cached        (scheduler.py:610)  补写存量 auto_ok
                     └─ N× _worker           (scheduler.py:654)
                          ├─ db.claim_next   (db.py:263)         pending→processing（原子）
                          ├─ memory_status_map 短路（命中记忆则零请求）
                          ├─ _process        (scheduler.py:710)
                          │    ├─ match_file (scheduler.py:137) → search_cached → verify_detail
                          │    ├─ add_candidates (db.py:758)
                          │    └─ _write_fields (scheduler.py:378) → writer.write_*
                          └─ update_task_status(auto_ok | manual_pending | error)
```

---

## 1. 刮削流水线

### 1.1 阶段划分（一次「扫描 + 刮削 + 写入」共 7 个阶段）

**阶段 0 — 扫描（只学文件名，不碰文件内容）**

`collect_audio_files(path, recursive, progress=None)` (scheduler.py:30-67) 用 `os.walk`/`os.listdir` 收集 `SUPPORTED_EXTS = {'.flac','.mp3','.ogg','.ape'}`（filenames.py:25），每 200 个文件调一次 `progress(found, scanned)`。**这一阶段完全不打开音频文件**，所以对几万首库也很快。

扫描阶段自身的进度机在 `_scan_worker` (main.py:224-269)：

```python
_scan_state['phase'] = 'collecting'   # → matching → saving → done（异常时 'error'）
```

**阶段 1 — 永久记忆比对（零请求跳过）**

`db.memory_status_map(paths, hash_budget, progress)` (db.py:607-690)：先按路径命中（零成本），再按 `file_hash`（sha256）比对；返回 `{path: 'manual_done'|'error'|'skipped'}`。命中者在 `add_tasks` 里**直接以该状态入库**，永远不进刮削队列。

**阶段 2 — 入队（幂等）**

`db.add_tasks(files, memory)` (db.py:217-259)：已存在的行**只在 `status=='pending'` 时纠正**，其它状态（人工指定/已刮出结果）一律不动 —— 这就是「重复扫描不破坏人工成果」。

**阶段 3 — 原子认领**

`db.claim_next(scope)` (db.py:263-288)：

```python
cur.execute("BEGIN IMMEDIATE")
cur.execute("SELECT path FROM tasks WHERE status='pending' [AND path IN (...)] ORDER BY rowid LIMIT 1")
cur.execute("UPDATE tasks SET status='processing' WHERE path=?", (path,))
```

没有内存队列：**SQLite 表就是队列**，`BEGIN IMMEDIATE` 拿写锁保证两个 worker 不会拿到同一首。

**阶段 4 — 匹配（核心）**

`match_file(path, cfg, sources=None, force_live=False)` (scheduler.py:137-200) 是整条流水线的心脏：

```python
name = os.path.basename(path)
cleaned = clean_filename(name)                      # filenames.py:182
cands   = build_candidates(cleaned)                 # filenames.py:252
for cand in cands:                                  # 按分隔符优先级排序的候选词
    for src in sources:
        for meta in search_cached(src, cand.text, "", limit=plimit, force_live=force_live):
            if (src.name, meta.song_id) in seen: continue
            ok, reason = verify_detail(cleaned.stem, meta.title, meta.artist)   # filenames.py:302
            meta.extra['verified'], meta.extra['verify_reason'], meta.extra['query'] = ok, reason, cand.text
            meta.confidence = _rescore(meta, segs, fdur)                     # scheduler.py:497
            if ok: verified.append(meta)
    if verified: break                               # 命中即停（scheduler.py:194-195）
```

四步文件名模式（scheduler.py 模块 docstring L1-12）：

1. **清洗**：去扩展名 → 去音轨号前缀（`_TRACK_PREFIX_RE`，filenames.py:31-35，**必须带分隔符**，否则「2002 年的第一场雪」会被吃掉）→ 整段丢弃括号噪音/版本词（`_NOISE_WORDS`/`_VERSION_WORDS`，filenames.py:38-54）→ 括号内剩余内容留作独立候选（`_BRACKET_MIN_LEN=2`）→ 去尾部裸标签 → 压缩空白。
2. **生成候选查询词**：`build_candidates` (filenames.py:252-293) 顺序 = ①整串(`full`) ②按**最高优先级存在的那一组**分隔符切出的左右段(`left`/`right`，优先级 `' - '` > `–/—` > `-` > `_` > `|`，最多 3 刀) ③括号内(`bracket`)，上限 `_MAX_CANDIDATES=8`。
3. **逐个候选搜索，命中即停**：只对**当前候选词**搜索，一旦出现 verified 结果就跳出整个双层循环。典型情况（「歌手 - 歌名」）只花 1 次搜索请求。
4. **用搜索结果反推**：`verify_detail(name, title, artist)` (filenames.py:302-330) 把 title 与 artist 都 normalize 后要求**都作为子串出现在原始文件名 stem 中**（顺序无关）；歌手整体不匹配时按 `[/;、,&]|feat|ft` 拆开，任一 ≥2 字的歌手命中即算通过。

**打分只用于排序，不参与命中判定**（scheduler.py:1-12 明确说明）：`_rank_key` (scheduler.py:86-93) = `(verified, confidence, 非空字段数)`；`_rescore` (scheduler.py:497-533) 用文件名 + 时长重算 0~100：`verified +40`、时长比值 ≥0.5 时 `+40*ratio`、歌名与候选词相等 `+15` / 互为子串 `+8`、歌手吻合 `+5`，`extra['confirmed']` 直接 100。

**阶段 5 — 候选落库 + 状态判定**

`db.add_candidates(path, [...])` (db.py:758-775) 是**替换语义**（先 `DELETE` 该 file_path 再 `INSERT OR REPLACE`），避免上次勾了 A 源、这次只勾 B 源时残留旧候选。读取按 `ORDER BY COALESCE(verified,0) DESC, score DESC`（db.py:778-784）。

`_process` (scheduler.py:710-801) 的状态判定：

| 情况 | 状态 | 依据 |
|---|---|---|
| 有 verified 结果 + `write_enabled=='1'` + 写入成功 | `auto_ok` | scheduler.py:795-800 |
| 有 verified 结果但写入抛异常 | `error` + `_cn_err(exc)` | scheduler.py:791-794 |
| 有结果但无一 verified | `manual_pending` | scheduler.py:775-781 |
| `err_count >= max(1, len(sources)//2)`（多数源报错） | `manual_pending`，文案「搜索失败（网络超时或数据源异常），可点「重新搜索」重试」 | scheduler.py:749-753 |
| metas 为空且源没报错 | `manual_pending`，文案「无匹配，待人工辅助…」 | scheduler.py:754-758 |
| 指纹模式未配 AcoustID Key / 无 qqmusic 插件 | `manual_pending` + 可操作提示 | scheduler.py:722-732 |

**阶段 6 — 写入**

`_write_fields(path, meta, src, cfg)` (scheduler.py:378-460) 三条硬规则：

- `active = set(active_field_keys(cfg))`，只构造**非空且已勾选**的字段，`has(v)` 判空 —— **候选为空的值绝不写入**，不覆盖文件里已有数据；
- 写入前后包一层 `with writer.active_fields_scope(active): writer.write_metadata(path, partial)`，写入器再拦一道（writer.py:88-120 `_guard`）；
- 封面：`extra['confirmed']` 且文件已有图 → 跳过下载直接算写入；否则 `extra['cover_url']` → `_download_cover` → `src.fetch_cover_best` → `cover_best_cached` → `_qq_fallback` 回查 QQ（scheduler.py:478-494，跨源兜底）；歌词同理（`extra['lyrics']` → `lyrics_cached` → `_qq_fallback`）。
- 末尾 `_restore_owner(path)` (scheduler.py:548-559) `os.chown(path, PUID, PGID)` 归还属主（容器内以 root 写入的场景）。

`_write_fields` 返回**实际写入的字段名列表**，最终存进 `tasks.written`（逗号串）。

**阶段 7 — 存量补写**

`Scraper.run` 在起 worker 之前，若 `write_enabled=='1'` 先跑 `_write_cached(cfg)` (scheduler.py:610-652)：把历史上「匹配到了但当时是学习模式没写」的 `status='auto_ok' AND written 为空` 的歌，按 `get_candidates(path)[0]` 直接补写。**原独立「补写」按钮被折进「开始刮削」**，用户一个动作系统做两件事（`/api/run` 的返回里带 `unwritten` 计数，main.py:678-702）。

### 1.2 状态机字段名

`tasks.status` 取值（db.py:15-58 SCHEMA 注释 + main.py:517-518 白名单）：

```
pending → processing → auto_ok          （自动写入成功）
                     → manual_pending   （需人工，或学习模式下已匹配）
                     → error            （写入失败/指纹失败）
manual_pending → auto_ok | manual_done | skipped
error → pending（retry_errors）
skipped → manual_pending（unskip_all）
```

- `processing` 只在调度器内部使用，**前端白名单 `MANUAL_STATUS` 不含它**（main.py:517）。
- `auto_ok` 的界面分组是 `('auto_ok','processing')`（db.py:356 `_STATUS_GROUPS`），因为正在处理的也要显示在「自动写入」档。
- 「是否真的写过文件」不看 status，看 `written` 列：`count_unwritten_auto_ok()` = `status='auto_ok' AND (written IS NULL OR TRIM(written)='')`（db.py:383-388）。**status 与 written 分离**是关键设计。
- `manual_done` / `error` / `skipped` 属于 `MEMORY_STATUSES`（db.py:92），会写入 `manual_done` 永久记忆表。

### 1.3 断点续跑与重试

| 机制 | 位置 | 说明 |
|---|---|---|
| 崩溃恢复 | `recover_stale_processing()` (db.py:291-295) | `UPDATE tasks SET status='pending' WHERE status='processing'`，一条 SQL。在 `Scraper.run` (scheduler.py:583) 和 `/api/run` (main.py:678) 各调一次 |
| 状态即断点 | tasks 表 | 每首歌处理完立刻落库，进程被 kill 只丢当前这一首 |
| 单次运行上限 | `run_limit` + `_limit_reached()` (scheduler.py:701-706) | 达到上限时**把已认领的任务放回 pending**（scheduler.py:663-666、690），不丢任务 |
| 批量重试 | `retry_errors()` (db.py:839-846) | `error → pending`，并清该文件的永久记忆 |
| 整档重排 | `requeue_status(status)` (db.py:849-862) | 任意状态 → pending，**同时清 candidates/decisions/manual_done**（让重新刮削不被人造记录干扰） |
| 单曲重搜 | `/api/scrub` (main.py:551) | `force_live=True` 跳过所有缓存重新打上游 |
| 只重跑某档 | `/api/scrape-pending`(main.py:767)、`/api/reprocess-pending`(main.py:745) | 取该状态的全部 path 作为 `Scraper(scope=set(paths))` 的 scope，`claim_next(scope)` 用 `path IN (...)` 过滤 |
| 扫描幂等 | `add_tasks` | `INSERT OR IGNORE`，只纠正 pending 行 |

---

## 2. 并发模型

- **进程/线程模型**：单进程 uvicorn（`uvicorn.run('webapp.main:app', uds=SOCK)`，run_server.py:220）+ FastAPI；刮削是 `class Scraper(threading.Thread)`（scheduler.py:562），`daemon=True`。
- **worker 数**：`workers = max(1, int(cfg['concurrency']))`（scheduler.py:585），默认 `concurrency='1'`（db.py:60-89）。`run()` 起 `workers` 个 `threading.Thread(target=self._worker)` 后 `join` 全部（scheduler.py:596-604）。
- **队列**：无内存队列。`db.claim_next` 就是出队，SQLite 表就是队列（见 1.1 阶段 3）。
- **SQLite 锁竞争**：
  - `connect()` (db.py:113-119)：`sqlite3.connect(DB_PATH, timeout=30)`、`row_factory=Row`、`PRAGMA journal_mode=WAL`、`PRAGMA busy_timeout=30000`；
  - **每次调用新开连接**（用 `with connect() as conn` 自动关），不共享连接、不做连接池 —— 短事务 + WAL 下开销可接受，且天然规避了跨线程共享连接的坑；
  - 认领用 `BEGIN IMMEDIATE` 一次拿写锁，写事务都极短。
- **限速（对上游）**：`ratelimit.throttle(min_interval)` (ratelimit.py:28-38)：

```python
interval = min_interval * (0.5 + random.random())      # 0.5x~1.5x 抖动
with _lock:
    wait = _last_request_ts + interval - time.monotonic()
    if wait > 0: time.sleep(wait)
    _last_request_ts = time.monotonic()
```

  全局 `_lock` + 全局 `_last_request_ts`（ratelimit.py:24-25）：**所有源实例、所有 worker 线程、网页接口的并发请求共享同一节奏**。插件侧只是转调（qqmusic.py:98-100、181-183），插件本身不再自建限速器。这一点很重要：如果限速器是 per-worker 的，`concurrency=4` 会把上游 QPS 放大 4 倍。
- **暂停/节流**：`pause_every` / `pause_seconds`（默认 0 / 5）。`_worker` 每处理 `pause_every` 首后 `self._stop_flag.wait(self._pause_seconds)`（scheduler.py:695-699）—— 用 `Event.wait` 做**可被 stop 立即打断的 sleep**，而不是 `time.sleep`。
- **取消**：`stop_scraper()` (scheduler.py:827-830) → `Scraper.stop()` (scheduler.py:576-577) 置 `_stop_flag`；worker 循环条件 `while not self._stop_flag.is_set()`（scheduler.py:668）。**语义要说清楚**：`_process` 内部不检查 stop，所以「停止」是「当前这一首跑完 + 未认领的任务留在 pending」，不是硬中断。这是刻意的（写文件写到一半更糟）。
- **扫描并发**：`POST /api/scan` 默认后台线程 + `_scan_lock` 防重入（main.py:272-302），已在扫则返回 `{started:False, reason:'已有扫描在进行中'}`。
- **其它后台线程**：`_export_auto_ok_worker`（已确认歌曲缓存导出，main.py:1056-1166）同样用 `_export_lock` + `_export_state` 防重入并上报进度。
- **潜在串行点**：`cache.py` 用一个全局 `threading.Lock` + 单连接（`check_same_thread=False`，cache.py:54-55、80）保护所有缓存读写 —— 高并发下这是全局串行点，但缓存操作都是微秒级 SQLite 点查，实测不是瓶颈。

---

## 3. 数据库 schema

**文件位置**：`DB_PATH = os.environ.get('MMW_DB')`，否则 `<webapp>/data/app.db`（db.py:10-13）。缓存库另有一个文件（见第 4 节）。

五张表（db.py:15-58）：

```sql
config(key TEXT PRIMARY KEY, value TEXT NOT NULL)

tasks(
  path TEXT PRIMARY KEY,      -- 绝对路径，作为业务主键
  name TEXT NOT NULL,         -- 文件名（重命名时同步更新）
  status TEXT NOT NULL,       -- pending/processing/auto_ok/manual_pending/manual_done/skipped/error
  score REAL,
  title TEXT, artist TEXT, album TEXT, year TEXT,
  error TEXT,
  written TEXT,               -- 已写入字段名逗号串，如 "cover,artist,year,lyrics,title"
  updated_at TEXT
)

candidates(
  file_path TEXT, songmid TEXT, title TEXT, artist TEXT, album TEXT, year TEXT,
  albummid TEXT, duration INTEGER, score REAL,
  source TEXT,                -- 候选来自哪个源
  verified INTEGER,           -- 1 = 通过文件名反推校验
  PRIMARY KEY(file_path, songmid)     -- 天然去重：同一文件同一首歌只留一条
)

decisions(file_path TEXT PRIMARY KEY, songmid TEXT, decided_at TEXT)   -- songmid='' 表示「跳过」

manual_done(                  -- 文件级永久记忆
  file_path TEXT PRIMARY KEY,
  file_hash TEXT,             -- sha256（只对人工逐条操作计算）
  file_size INTEGER,
  done_at TEXT,
  status TEXT                 -- manual_done/error/skipped；旧库为空按 manual_done
)

CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_manual_done_hash ON manual_done(file_hash);
```

**唯一约束**：`tasks.path`（主键）、`candidates(file_path, songmid)`（复合主键，同时是唯一约束）、`decisions.file_path`、`manual_done.file_path`、`config.key`。索引只有两个：按状态查队列（最热）、按哈希查记忆。

**迁移方式**：没有 Alembic，`init_db()` (db.py:122-165) 每次启动做幂等迁移：

```python
conn.executescript(SCHEMA)                          # CREATE TABLE IF NOT EXISTS
cols = {r['name'] for r in conn.execute("PRAGMA table_info(tasks)")}
if 'written' not in cols: conn.execute("ALTER TABLE tasks ADD COLUMN written TEXT")
# 同样处理 candidates.source / candidates.verified / manual_done.file_size / manual_done.status
# manual_done.file_size 加列后用 os.path.getsize 回填；status 回填 'manual_done'
conn.execute("DELETE FROM config WHERE (key LIKE 'write\\_%' ESCAPE '\\' AND key <> 'write_enabled') OR key IN ('threshold','idle_exit_minutes')")
for k, v in DEFAULTS.items(): conn.execute("INSERT OR IGNORE INTO config(key,value) VALUES(?,?)", (k, v))
```

注意 db.py:150-160 的注释记录了一次真实事故：早期版本把 `write_enabled` 也当废弃键删了，导致**每次重启都退回学习模式**。所以现在删键有明确的 `key <> 'write_enabled'` 例外。

**为什么用绝对路径当主键**：任务是「文件」级而不是「歌曲」级；重命名时 `POST /api/rename`（main.py:790-820）要同步改 4 张表的路径（`tasks.path/name`、`candidates.file_path`、`decisions.file_path`、`manual_done.file_path`）—— 这是路径主键的代价。

---

## 4. 缓存层

`musicmeta/cache.py` 是**应用级**缓存，不是插件级。设计动机写在 docstring (cache.py:2-31)：插件保持「纯搜索」，缓存/限速/重试全在应用层 —— 换源、加源零成本复用。

### 4.1 键设计与 TTL

```python
def key(source, kind, *parts):
    segs = [source, kind] + [str(p) for p in parts if p not in (None, '')]
    return ':'.join(segs[:2]) + ':' + '|'.join(segs[2:])     # 形如 qqmusic:s:晴天|周杰伦
```

- 源名前缀避免跨源冲突；空 part 不参与拼接（`artist` 为空时键是 `qqmusic:s:晴天`）。
- TTL 表 `_TTL` (cache.py:43-49)：

| kind | 含义 | TTL |
|---|---|---|
| `s` | search 搜索结果 | 7 天 |
| `e` | enrich 专辑详情 | 30 天 |
| `l` | lyrics 歌词 | 30 天 |
| `c` | cover 图片字节（base64 存 TEXT） | 1 天 |
| `f` | confirmed 已确认歌曲（**永久记忆式**） | 90 天 |

  缺省 `_DEFAULT_TTL = 3600`。

### 4.2 持久化位置与降级

- SQLite 单文件：`MMW_CACHE_DIR` → `TRIM_PKGVAR` → `~/.musicmeta_cache` 下的 `musicmeta_cache.db`（cache.py:29-31、68-89）；
- 表 `meta_cache(key TEXT PRIMARY KEY, expire REAL, val TEXT)` + `idx_meta_cache_expire`；
- **目录不可写时自动降级为纯内存** `_mem_only`（内存模式上限 5000 条，超了清最旧 20%）；
- 行数上限 `_MAX_ROWS = 20000`，超出按 `expire` 升序清最旧一批（cache.py:124-161）；
- `set_dir(path)` (cache.py:205-224) 支持运行时切换目录（配置页改 `cache_dir` 即时生效，main.py:187-193）；
- `_migrate_legacy_qq()` (cache.py:164-202)：把旧版插件自带的 `qqmusic_cache.db`（表 `qq_cache`，旧键 `s:稻香|周杰伦`）迁移为 `qqmusic:s:稻香|周杰伦`，旧库改名 `.migrated`；幂等，失败不影响启动。

### 4.3 二次刮削如何避免重复请求

`search_cached(src, title, artist, limit, force_live)` (scheduler.py:215-266) 三级查找：

```python
# 1) f: 已确认歌曲缓存（人工逐条确认过的歌）—— 命中直接返回，confidence=95，extra['confirmed']=True
hit = cache.get(cache.key(name, 'f', q_title, q_artist))
# 2) s: 搜索结果缓存 —— 命中后按当前请求重新打分排序（search_hit, cache.py:269-290）
metas = cache.search_hit(name, q_title, q_artist, simple_score, limit)
# 3) 真实网络请求 —— 成功后 search_store 写回
```

关键细节：**缓存键必须与真正发给插件的查询词一致**（scheduler.py:235-238）—— `qqmusic` 用清洗后的 `(q_title, q_artist)`（`_search_query_keys`，scheduler.py:203-212），其它源用原文 `(title, artist)`，否则缓存永远打不中。

其它三类资源各自的键与复用：

- `enrich_cached` (scheduler.py:269-311)：键 `e:<album_id or song_id>`；`extra['confirmed']` 的歌**直接返回不发请求**；
- `lyrics_cached` (scheduler.py:314-327)：键 `l:<songmid>`；
- `cover_cached` (scheduler.py:330-347)：键 `c:<albummid>|<size>`，值 base64，解码失败则继续走网络；`cover_best_cached` (scheduler.py:350-357) 按 `(800,500,300)` 大图优先。

`force_live=True`（手动刮削 / 「重新搜索」）**跳过 f: 和 s: 但不跳过写回**：结果仍会进缓存。

### 4.4 负缓存

**没有负缓存**。`search_cached` 只在 `metas` 非空时 `search_store`；查不到不写任何条目，下次仍会真实请求上游。这是保守选择（避免「上游临时故障 → 永久记下查不到」），代价是「库里大量野路子文件名」时每次刮削都要重打上游。

另外要区分两套「缓存」：

| | 请求级缓存 `cache.py` | 处理结果记忆 `manual_done` 表 |
|---|---|---|
| 键 | `源:kind:查询词/专辑id` | `文件路径` + `sha256` + `file_size` |
| 目的 | 同一首歌不重复打上游 | 同一个文件不重复处理 |
| 命中效果 | 省一次 HTTP | 直接给状态（`manual_done`/`error`/`skipped`），**整个文件跳过** |
| 谁写入 | 自动（每次搜索/取详情） | 只有人工逐条操作、或刮削判为 error/skipped |

`manual_done` 表的写入策略（db.py:562-591）值得抄：`with_hash` **默认只有 `manual_done` 为 True**（人工逐条，一次一个文件，算 sha256 可接受）；`error`/`skipped` 可能批量产生，**只存 file_size 不读文件内容**，避免「一次点击读完整库」。

---

## 5. HTTP 接口清单（main.py）

中间件：`strip_gateway_prefix` (main.py:72-80) 按 `MMW_GATEWAY_PREFIX`（默认 `/app/music-meta-web`）剥前缀，兼容网关剥/不剥两种部署。所有接触文件的接口都先过 `_require_in_music_dir(path, what)` (main.py:29-48)：**先判空再判前缀**（注释：`os.path.abspath('')` 会返回当前目录），未配置 → 400，越界 → 403，不存在 → 404。

| 方法 | 路径 | 行 | 作用 | 关键参数 |
|---|---|---|---|---|
| GET | `/` | 98 | 静态页 index.html | `Cache-Control: no-store` |
| GET | `/api/config` | 129 | 读全部配置 | 附带只读 `data_dir`/`manual_stats`/`cache_db` |
| PUT | `/api/config` | 173 | 写配置 | ConfigBody；改 `cache_dir` 即时 `_apply_cache_dir` |
| GET | `/api/sources` | 147 | 列出已装数据源 | `{sources, plugins_dir}` |
| GET | `/api/fields` | 158 | 字段清单 | 每项带 `writable:is_writable(key)`（只读字段前端不可勾） |
| POST | `/api/scan` | 272 | 启动扫描 | `{path, recursive, wait}`；默认后台线程，`wait=true` 同步 |
| GET | `/api/scan-status` | 305 | 扫描进度 | 两段进度：`found/scanned` 与 `mem_done/mem_total/hits` |
| POST | `/api/tasks/reset` | 317 | 清空队列 | 清 tasks+candidates+decisions |
| POST | `/api/tasks/unskip` | 324 | 全部取消跳过 | `{restored:n}` |
| POST | `/api/tasks/delete` | 336 | 删任务（可删文件） | `{file, delete_file}`；**学习模式拒绝 delete_file** |
| POST | `/api/tasks/retry-errors` | 377 | error→pending | `{retried:n}` |
| GET | `/api/tasks` | 470 | 列表/搜索/筛选 | `status,limit=100,offset,q,filter`；有 filter 时 Python 侧分页 + 600s 结果缓存 |
| POST | `/api/task-status` | 526 | 人工改状态 | `{file,status}`；白名单外 400；非 manual_done 时清 decision |
| POST | `/api/scrub/{file:path}` | 551 | **手动刮削单曲** | `force_live=True`；返回逐字段 `current/candidates` + 歌词候选 |
| GET | `/api/cand-lyrics` | 639 | 取某候选的歌词 | `song_id`；qqmusic 走 `lyrics_cached`，其它源走 `enrich_cached` |
| GET | `/api/stats` | 673 | 各状态计数 | `db.task_stats()` |
| POST | `/api/run` | 678 | **启动刮削** | 先 `recover_stale_processing()`；返回 `{started,count,unwritten,reason,recovered}` |
| POST | `/api/stop` | 705 | 停止刮削 | 置 Event |
| GET | `/api/status` | 711 | 刮削进度 | `done=scraper._done`，`total=pending+unwritten`，附 `scan` 快照 |
| POST | `/api/reprocess-pending` | 745 | 重跑 manual_pending 档 | `requeue_status` 后 `start_scraper(paths)`（只刮本队列） |
| POST | `/api/scrape-pending` | 767 | 只刮 pending 档 | 同上，`scope=set(paths)` |
| POST | `/api/rename` | 790 | 重命名文件 | `{file,new_name}`；同步 4 张表路径 + 归还属主 |
| POST | `/api/decide/batch` | 827 | **批量按最高分写入 / 批量跳过** | `{mode:'best'|'skip'}`；best 需启用写入；单首失败 continue 留给人工 |
| GET | `/api/stream/{file:path}` | 869 | 音频流（试听） | FileResponse，支持 Range |
| GET | `/api/embedded-cover/{file:path}` | 881 | 内嵌封面 | 按魔数判 mime，无图 404 |
| POST | `/api/records/export-file` | 964 | 导出记录到文件 | 写 `music-meta-记录-*.json` 到导出目录，尝试飞牛 APP 原生下载 |
| POST | `/api/records/clear` | 989 | 清空记录 | `clear_records()`：记忆相关任务回 pending |
| GET | `/api/manual-done/export` | 998 | 导出记忆 JSON | `Content-Disposition: attachment` |
| POST | `/api/manual-done/import` | 1013 | 导入记忆 JSON | 空/>16MB/非 JSON → 400；导入后 `sync_memory_tasks()` |
| GET | `/api/health` | 1041 | 健康检查 | |
| POST | `/api/cache/export-auto-ok` | 1169 | **已确认歌曲导出到缓存** | 后台线程；把 auto_ok 的歌写成 `f:` 缓存条目 |
| GET | `/api/cache/export-status` | 1184 | 导出进度 | `{running,done,total,errors,last}` |
| POST | `/api/field-write` | 1206 | **单字段人工写入** | `{file,field,value}`；学习模式 400；写入器 `active_fields_scope` 再拦一道；成功后 `set_status(manual_done)` |

几个值得注意的接口设计：

- `/api/run` (main.py:678-702) 是「开始刮削」的唯一入口，返回里带 `unwritten`（待补写数量）和 `recovered`（恢复的僵尸任务数），前端能直接解释「为什么按钮点下去还有别的事在发生」。
- `/api/scrub` (main.py:551-636) 返回**逐字段的「当前值 + 候选值」**，歌词只给候选列表 + 首句 hint（`_lrc_hint`，main.py:610-620 跳过 LRC 时间标签行），正文点选时再取 —— 避免一次拉十几份歌词。
- `/api/field-write` (main.py:1206-1277) 是唯一「不经过候选」的写入路径（用户手输），所以它额外检查「字段是否在生效字段里」并复用同一把 `active_fields_scope` 闸门，成功后打 `manual_done` 标记（写进去的就是人工成果，不该被下次自动刮削覆盖）。
- `/api/decide/batch` (main.py:827-866) 批量按最高分写入时**构造 SongMeta 刻意不带 source**，避免把上一个源的标签写进文件。
- `/api/tasks` 的 filter（`no_lyrics`/`has_cover`/`no_year`…，main.py:417-433）是**读文件真实内嵌标签**判断的，且有 600s 结果缓存 + 按 mtime 失效的标签缓存（main.py:384-414）；`_invalidate_filter_cache(path)` (main.py:436-467) 在写完后**用最新标签重新判断该文件是否仍匹配缓存条件**，实现「补齐字段后立刻从过滤器消失」。
- `/api/rename` 改文件名的同时同步 4 张表，避免路径主键把任务拆成两条。
- 记录导出走飞牛 APP 原生下载：`_mint_native_download` (main.py:922-961) 拿会话 token 去 `http://127.0.0.1:5666/multiple-download` 换一次性下载链接，失败返回 None 由前端退回普通下载。

---

## 6. 配置模型

单一来源：`DEFAULTS` (db.py:60-89)，全部存 `config` 表（key/value TEXT）。`get_config()` (db.py:170-175) = `dict(DEFAULTS)` 再 `update` 数据库行；`set_config(patch)` (db.py:178-190) **白名单 `set(DEFAULTS)`**（未知键直接丢弃），`'true'/'false'` 归一为 `'1'/'0'`，`INSERT ... ON CONFLICT(key) DO UPDATE`。

| 配置项 | 默认值 | 作用 | 生效方式 |
|---|---|---|---|
| `music_dir` | `''` | 音乐库根目录 | 即时（每次接口读） |
| `recursive` | `'1'` | 是否递归扫描 | 即时 |
| `write_enabled` | `'0'` | **安全开关**：0=学习模式只匹配不写 | 即时 |
| `min_interval` | `'0.3'` | 上游请求最小间隔（秒） | 即时（每次 `resolve_sources` 传参） |
| `concurrency` | `'1'` | worker 线程数 | **下次开始刮削**（`run()` 时读取） |
| `request_timeout` | `'15'` | 上游超时（秒） | 即时 |
| `request_retries` | `'2'` | 上游重试次数 | 即时 |
| `run_limit` | `'0'` | 单次运行最多处理几首，0=不限 | 下次运行 |
| `pause_every` | `'0'` | 每处理 N 首暂停一次，0=不暂停 | 下次运行 |
| `pause_seconds` | `'5'` | 暂停时长（秒） | 下次运行 |
| `cache_dir` | `''` | 缓存目录，空=应用数据目录 | **即时**（`_apply_cache_dir`，main.py:187-193） |
| `source_limit` | `'10'` | 每源候选条数 1~20 | 即时 |
| `matching_mode` | `'filename'` | `filename` / `fingerprint` | 即时 |
| `acoustid_key` | `''` | 指纹识别用 AcoustID Key | 即时 |
| `plugins_dir` | `''` | 插件目录 | **需重启**（见下） |
| `source` | `''` | 启用的源，逗号分隔 | 即时 |
| `active_fields` | `'title,artist,cover,lyrics'` | 生效字段（**唯一一份字段开关**） | 即时 |

「生效字段」的单一来源在 `musicmeta/fields.py`：`FIELDS` 13 个字段定义 (fields.py:28-42)，`WRITABLE_FIELDS` (fields.py:51-54) 与 `READONLY_FIELDS = ('publisher','language')` (fields.py:56) —— 只读字段**界面不可勾选**（`is_writable`，fields.py:59-61），因为目标播放器（飞牛音乐）从来不读这两个标签。`parse_active` (fields.py:70-82) 把配置值解析为按 FIELDS 顺序的列表，并 `raw &= set(WRITABLE_FIELDS)`，最后兜底 `DEFAULT_ACTIVE`。

**何时需要重启**：只有插件目录的内容（`registry.py:111-112` 在模块导入时 `discover_plugins()` 扫一次）。界面文案也如实说明：「插件放到插件目录后需重启应用」（main.py:686）。其余配置全部即时生效。

另外 `run_server.py:136-165 _apply_wizard_env()` 有一个「只填空值」规则：安装向导传的环境变量（`MMW_MUSIC_DIR`/`MMW_PLUGINS_DIR`/`MMW_ACOUSTID_KEY`/`MMW_CACHE_DIR`）只在对应 config 行为空时写入，**网页改过的值不会被向导覆盖**（向导改配置时由 cmd/config_callback 先清空对应行）。

---

## 7. 错误处理与可观测性

**单曲失败隔离**

- 匹配阶段：`match_file` 里每个源的搜索独立 `try/except`（scheduler.py:184-190），异常只让 `err_count += 1` 并 `print`，不影响其它源/其它候选；
- 写入阶段：`_process` 里 `try: ... _write_fields ... except: db.update_task_status(path,'error', error=_cn_err(exc)); return`（scheduler.py:788-794），单首失败不影响整批；
- `_worker` 的 `try/finally` 只保证 `_done` 计数（scheduler.py:682-688），**不吞异常**（`Scraper.error` 记录）。
- 批量操作（`/api/decide/batch`）单首失败 `continue` 留给人工（main.py:827-866）。

**错误信息本地化**：`_cn_err(exc)` (scheduler.py:360-375) 把 `PermissionError`/`[Errno 13]` → 「没有写入权限…」、`FileNotFoundError`/`[Errno 2]` → 「文件不存在…」、`[Errno 28]` → 「磁盘空间不足，无法写入」、`[Errno 30]`/`Read-only` → 「文件系统只读」、其它 → `f"写入失败：{s}"`。错误直接落 `tasks.error` 并在界面上显示 —— 用户看到的是「没有写入权限」而不是 `[Errno 13] Permission denied`。

**上游异常降级**

- 插件缺失：`resolve_sources` (scheduler.py:103-120) `print` 警告后跳过，其余源继续；
- 插件签名不兼容：`get_source(name, **kwargs)` 抛 `TypeError` 时退回只传 `min_interval`（scheduler.py:110-115）；`src.search` 同样有 `TypeError` 退回 `(title, artist, limit)`（scheduler.py:263-265）—— 对第三方插件的宽容；
- 插件内部三层降级（qqmusic.py:289-319）：主接口 `for host in _SEARCH_HOSTS`（`c.y.qq.com` → `c6.y.qq.com` → `i.y.qq.com`，每次换域前 `sleep(1.5*(0.5+random()))`）→ 全失败后备用 `musicu` 接口 `_search_musicu` 兜底 → 仍失败返回空列表 —— 注意这里有个真实缺口：插件把「全部接口失败」也表达成空列表，`match_file` 的 `err_count` 就不会 +1，最终会被判为「无匹配，待人工辅助」而不是「搜索失败，可重试」（qqmusic.py:319）。这说明「异常」与「空结果」必须在插件契约层面区分开（见第 8 节第 8 条）；单请求内部还有指数退避 + 抖动（qqmusic.py:104-126，`0.5 * 2**attempt * (0.5+random())`）；歌词接口主备双通道（qqmusic.py:419-447，主接口 JSON、备用 JSONP + `html.unescape`）；
- 跨源兜底：封面/歌词拿不到时 `_qq_fallback` (scheduler.py:478-494) 回查 QQ 音乐（`get_source('qqmusic', min_interval=0.2)`），**任一源都能借 QQ 补封面和歌词**；
- 无源可用时：`_worker` (scheduler.py:661-666) 认领一首后立刻放回 pending 并返回，同时 `/api/run` 前置校验直接给文案「没有可用的数据源插件：请在设置里勾选已安装的插件（插件放到插件目录后需重启应用）」。

**进度上报**

- 扫描：全局 `_scan_state` (main.py:208-220) + `_scan_lock`，两段进度（列文件 `found/scanned`；记忆比对 `mem_done/mem_total/hits`），每 200 / 每 100 个文件回写一次（避免每文件一把锁）；
- 刮削：`Scraper._done` 计数器（`_done_lock` 保护）+ `GET /api/status` 现算 `total = pending + unwritten`、`remaining = max(0, total-done)`（main.py:711-739）；
- 导出：`_export_state`（main.py:1052-1053），每 100 首更新。
- **状态字段就是进度**：`task_stats()` 按 status 分组计数，界面不需要单独维护进度。

**持久化/恢复**

- `recover_stale_processing()` 是启动必经步骤（scheduler.py:583、main.py:678 各调一次，注释说明「否则重启后残留 processing 会被永远漏掉」）；
- 记忆表异常被吞（db.py:335-343 `sync_memory_for_status`），**记忆功能坏了不能挡住状态更新**；
- `memory_status_map` 的时间预算兜底 (db.py:607-690)：`FALLBACK_HASH_BUDGET = 15.0` 秒，`deadline = time.monotonic() + budget`，超时 `break`。db.py:615-621 注释记录了 2026-09-12 的真实故障：一条 `file_size=0` 的已删除文件记录，导致整库每个文件都算 sha256，表现为「扫描卡住 / 永远 0 首」。修法两条前提：①只有 `size > 0` 才算已知大小；②认不出来时不再对剩余所有文件逐个算哈希，必须有时间预算。快路径 (db.py:648-664) 只对「大小能对上某条记录」的文件算哈希。

**可观测性的短板**：整个服务端**没有用 `logging` 模块，全是 `print()`**（scheduler.py 的匹配日志、registry.py 的插件加载日志、writer.py 的字段跳过日志都是 print）。日志级别、轮转、结构化全部缺失，只有 uvicorn 自己的 `log_level='info'`。Go 版本如果照抄这个架构，这一块应该换掉。

---

## 8. 值得别的项目借鉴的设计（Go 重设计要点）

1. **「用结果反推」代替模糊打分做自动判定**（filenames.py:302-330）。
   `verify_detail` 要求搜索结果的 `title` 与 `artist` **都作为子串出现在原始文件名里**（normalize 后、顺序无关、多歌手任一命中）。打分（`_rescore`，scheduler.py:497-533）只用于**排序**，不参与「要不要自动写」。
   好处：不需要调相似度阈值就能把「自动写入」的误判率压到极低，而且误判会以「不自动写、转人工」的形式安全失败。Go 侧可以直接移植：`strings.Contains(normalize(stem), normalize(title)) && contains(artist)`。

2. **命中即停 + 候选词按分隔符优先级排序**（scheduler.py:137-200、filenames.py:252-293）。
   候选查询词按 `' - ' > –/— > - > _ > |` 优先级分组，**只用最高优先级且确实存在的那一组**切分；逐候选搜索，一旦有 verified 结果就 `break` 两层循环。典型文件名 1 次请求搞定，最坏 8 次。这比「先猜歌手/歌名再搜索」稳健得多，也比「所有候选都搜一遍再挑最好的」省 8 倍请求。

3. **状态表即队列：用一条 SQL 做原子认领，崩溃恢复也只是一条 SQL**（db.py:263-288、291-295）。
   `BEGIN IMMEDIATE` + `SELECT ... LIMIT 1` + `UPDATE status='processing'`，配 `recover_stale_processing()` 在启动时把 `processing` 打回 `pending`。
   好处：不需要消息队列中间件，多 worker 安全，进程被 kill 后重启自动续跑。Go 侧用 `database/sql` + `BEGIN IMMEDIATE` 等价事务即可（注意 SQLite 要 `_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)`）。

4. **缓存与限速放应用层，插件保持纯函数**（cache.py:2-31、ratelimit.py:2-16、qqmusic.py:98-100）。
   插件只实现 `search/enrich/fetch_lyrics/fetch_cover_best`，不关心缓存与限速；应用层统一做三级缓存（`f:` → `s:` → 网络）和全局共享限速。加一个数据源 = 写一个纯搜索函数。
   反面教材就是「每个插件各自实现缓存/限速」：TTL 不一致、限速器各自为政（并发 worker 会把上游 QPS 乘上 worker 数）。

5. **限速器必须是进程级单例 + 随机抖动**（ratelimit.py:28-38）。
   `interval = min_interval * (0.5 + random())`，全局锁 + 全局时间戳。Go 侧用 `sync.Mutex` + 包级 `lastRequest time.Time`，或 `time/rate` 的 `Limiter` 加 jitter。
   额外一条：**暂停用可中断的等待**（`Event.wait(seconds)`，scheduler.py:695-699），Go 里对应 `select { case <-ctx.Done(): case <-time.After(d): }`，别用裸 `time.Sleep`。

6. **文件级永久记忆（path + sha256 + size），并且给哈希比对加时间预算**（db.py:562-690）。
   人工处理过的文件永远不再自动动它；同一首歌换了个文件名（内容相同）也能认出来。
   两条工程细节务必抄：①`error`/`skipped` 这类批量产生的记忆**只存 size 不读内容**；②只有当文件大小能对上已知记录时才计算哈希，且整个兜底路径有硬时间预算（15s）。否则一条脏记录（size=0）就能让全库扫描退化成「对每个文件算 sha256」而看起来像卡死。

7. **写入侧要有第二道字段闸门，且字段开关只有一个来源**（fields.py:28-56、writer.py:88-120）。
   `active_fields` 是唯一一份开关：界面显示、写入过滤、写入器 `_guard` 三处共用；`active_fields_scope(keys)` 是上下文管理器，作用域内不在集合里的字段一律不写，并且 `print` 一次「跳过字段 X：设置里没有勾选」。
   再加一条白名单：`READ_BY_FEINIU`（writer.py:72-75）只允许写下游播放器真正会读的标签键（该清单由字节级检索 + 真机对照实验得出，writer.py:62-71），「写了也是白写」的字段（publisher/language/comment）直接不写。
   Go 侧的价值：字段配置漏改一处、或调用方忘了过滤，都不会把脏标签写进用户的音乐文件。

8. **区分「搜不到」与「搜索失败」，并据此给不同文案和不同操作**（scheduler.py:749-758）。
   `MatchResult.err_count` 统计源异常次数，`err_count >= max(1, len(sources)//2)` 判为「搜索失败（网络超时或数据源异常），可点「重新搜索」重试」，否则判为「无匹配，待人工辅助」。用户知道该点重试还是该人工介入 —— 这是纯后端字段设计带来的产品体验，成本几乎为零。
   前提是**插件契约要能把「异常」和「空结果」分开**：qqmusic 插件把「所有接口都失败」也返回成空列表（qqmusic.py:319），于是这种情况会被归到「无匹配」，这是可以改进的点。Go 侧建议让 `Search` 返回 `(metas, err)` 而不是靠空切片表达失败。

补充几条小而实用的：

9. **候选表用「替换语义」而不是「合并语义」**（db.py:758-775）：每次刮削先 `DELETE` 该文件的旧候选再插新的，避免上次勾了 A 源、这次只勾 B 源时界面里还挂着 A 源的过期候选。
10. **status 与 written 分离**（db.py:383-388）：`auto_ok` 只表示「匹配到且判定可写」，`written` 列（逗号串）才表示「真的写了哪些字段」。学习模式下积累的 `auto_ok` 因此可以在开启写入后一键补写（`_write_cached`，scheduler.py:610-652），也支撑了「补写按钮折进开始刮削」这个产品决策。

---

## 附：给 Go 版本的模块划分建议（据上述事实直接映射）

```
cmd/server/main.go            启动：Unix socket 监听、插件目录扫描、残留进程清理（run_server.py）
internal/store/               db.go 的全部：tasks/candidates/decisions/manual_done/config
                              + WAL/busy_timeout + 幂等列迁移（PRAGMA table_info 思路 → 自建 schema_version 更好）
internal/match/               filenames.go + verify_detail + 候选词生成（纯函数，最好零依赖）
internal/scrape/              scheduler.go：Worker 池 + 状态机 + 写回编排
internal/cache/               cache.go：kind 化键 + TTL + SQLite/内存降级
internal/ratelimit/           ratelimit.go：包级单例 + jitter
internal/writer/              writer.go：字段白名单 + 作用域闸门 + mutagen 等价物（dhowden/tag）
internal/plugins/             插件注册表（Go 用 plugin 包不现实，建议子进程/HTTP sidecar 或代码内注册）
internal/api/                 main.go 的路由
```

最大的移植风险点是插件机制：Python 版靠 `importlib.util.spec_from_file_location` 加载单文件 `.py`（registry.py:96-107），Go 没有等价物，需要改成「编译期注册 + 配置文件启用」或「子进程插件协议」。
