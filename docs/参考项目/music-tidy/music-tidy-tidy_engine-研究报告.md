# music-tidy v1.12.0 `tidy_engine.py` 精读报告

> 只读研究，未修改 `/tmp/music-tidy/app/server/` 下任何文件。
> 所有引用格式为 `文件:行号`。核心引擎 `tidy_engine.py`(4118 行)，但关键算法大量下沉在 `scraper.py`、`music_source.py`、`ai_llm.py`，本报告一并覆盖。

---

## 0. 模块职责与总览

| 文件 | 行数 | 职责 |
|---|---|---|
| `tidy_engine.py` | 4118 | `TidyEngine` 类：SQLite 库、扫描/整理/去重/分类/歌单/分享/下载编排、任务状态机 |
| `scraper.py` | 1239 | 文件名解析、目录枚举对账、QQ/网易云搜索与评分匹配、歌词封面下载 |
| `music_source.py` | 1636 | 5 音源(网易云/咪咕/酷狗/酷我/QQ)并发搜索、播放/下载直链、断点续传 |
| `ai_llm.py` | 630 | LLM 兜底识别、命名规则生成、去重决策、AI 歌词/封面 |
| `music_tidy.py` | 1341 | HTTP 服务层，把引擎方法放到 `threading.Thread` 后台跑 |
| `watcher.py` | 165 | 轮询式增量监听线程 |
| `vendor/mutagen/` | — | 随包发布的 mutagen，写内嵌标签/封面 |

设计纲领(`tidy_engine.py:3-15`)：纯标准库；**Sidecar 优先**（同目录同名 `.lrc` + 逐曲同名 `.jpg`，飞牛音乐原生识别）；缺依赖时自动降级为「只写 sidecar」。

---

## 1. 整体流水线

### 1.1 三个主入口

| 入口 | 位置 | 作用 |
|---|---|---|
| `TidyEngine.probe_scan(folders)` | `tidy_engine.py:966-1103` | **只读体检**：只枚举计数、不入库、不算 SHA-1、不改文件；输出「磁盘 ↔ 库」对账 |
| `TidyEngine.scan(folders)` | `tidy_engine.py:1187-1356` | 三阶段增量入库（见 §1.2） |
| `TidyEngine.tidy(folders, files)` | `tidy_engine.py:1927-2007` | 批量整理 |
| `TidyEngine._tidy_file(path, override)` | `tidy_engine.py:1723-1854` | **单曲整理核心**，真正的流水线主体 |

HTTP 层统一用 `threading.Thread(target=eng.xxx, daemon=True)` 起后台任务（`music_tidy.py:832/845/857/912/930/937/944/965/976/1134/1149/1249/1268`），引擎内部用 `_lock` 保护任务结构体。

### 1.2 扫描：三阶段增量（`tidy_engine.py:1187-1356`）

```
阶段1 枚举  _enumerate_audio() → scraper.walk_audio()   只遍历目录树 + 轻量 stat，不读内容
阶段2 对比  (path,size,mtime,tidied,sha1) 与库比对       分类：新增 / 变化 / 未变化 / 磁盘已删除
阶段3 入库  只对 新增+变化+缺hash 的文件算 SHA-1 写库    未变化文件仅刷新 has_lyric/has_cover/code
```

- 变化判定：`st.st_size != row["size"] or abs(st.st_mtime - row["mtime"]) > 1`（`tidy_engine.py:1236`）。
- **安全门（最重要的设计）**：只有「完整枚举过」的根目录才有资格判定磁盘删文件（`tidy_engine.py:1244-1256`）。`trusted = exists && !errs && !depth_cut`；枚举不完整时缺失记录放入 `kept` 保留不删。注释直言旧实现「枚举到一半就拿来对比，把没扫到的那部分入库记录全删了」（`tidy_engine.py:1174-1185` 区段）。
- 入库先写行（sha1 留空）再回填 hash，任务中断后下次扫描会在阶段 2 检出 `not row["sha1"]` 补算（`tidy_engine.py:1271-1312`）。
- 用 `INSERT ... ON CONFLICT(path) DO NOTHING` 与监听线程并发入库不冲突（`tidy_engine.py:1289-1291`）。

### 1.3 单曲整理流水线 `_tidy_file`（`tidy_engine.py:1723-1854`）

```
1 取人工修正值 override + 库中 manual=1 的旧值          1723-1752
2 文件名解析 _parse_name() → 失败/无歌手且 LLM 可用 → LLM.recognize()  1754-1775
3 无 title → 返回 reason="parse_fail"                    1772-1775
4 enable_scrape: _resolve_meta() 消歧 + fetch_lyric()   1781-1797
5 lyric_garbled() 乱码则丢弃                             1788-1790
6 提取内嵌封面 _extract_embedded_cover()（有则跳过在线） 1799-1802
7 写 sidecar _write_sidecar()（.lrc + 逐曲 .jpg）        1803
8 无封面 → AI 文生图兜底 _maybe_ai_cover()               1804-1808
9 内嵌标签 _embed_tags()（mutagen，失败退化 ffmpeg）      1825-1826
10 enable_classify → _classify() 移动到 歌手/专辑/        1828-1833
11 UPDATE files SET artist,title,album,style,code,tidied=1,tidied_at,manual,path  1836-1840
异常 → files.tidied=2 + err 字段                          1846-1854
```

### 1.4 状态机

**任务级**（`tidy_engine.py:221`、`720-733`）：`idle / running / paused / done / stopped / error`
- 新增字段：`type`(scan|probe|tidy|organize|classify|touch|cover|download|dl_playlist|cue…)、`stage`(中文阶段串)、`seq`(任务序号防重启重复)、`bytes_done/bytes_total`。
- 暂停/停止只在**文件边界**生效：`_check_ctrl()` 循环 sleep(0.4) 等暂停（`tidy_engine.py:272-276`）。
- 终态语义修正：旧实现一律置 idle，现在区分 error/stopped/done（`tidy_engine.py:720-733`）。

**文件级** `files.tidied`：`0=未整理 / 1=已整理 / 2=失败`（`tidy_engine.py:65`、`1850`）。
- 断点续跑靠「已完成的不再重复处理」自然实现，而非游标。
- `tidy()` 的选行条件：开分类移动时 `tidied IN (0,1,2)`（全部重跑以应用新目录结构），否则 `tidied IN (0,2)`（`tidy_engine.py:1945`）。

**任务快照持久化**：`task_state.json`，节流 3 秒落盘，终态强制落盘；只恢复 `("tidy","scan","probe")` 三类，且**只恢复「可一键继续」的建议，绝不自动起线程重跑**（`tidy_engine.py:571-639`）。恢复时重新过一遍当下授权目录，路径用 token 往返防非 UTF-8 失真（`tidy_engine.py:641-667`）。

---

## 2. 文件名解析算法（`scraper.py:131-177`）

### 2.1 降级链

```
apply_name_rules(path, config.name_rules)   # 用户/AI 自定义命名分组正则，命中即返回
        ↓ 未命中(None)
parse_filename(path)                        # 内置三级
        ↓ 解析不出且 LLM 可用
TidyEngine 里 llm.recognize(basename)       # AI 兜底 → {artist,title,style}
```
入口 `tidy_engine.py:1700-1702`。

### 2.2 关键正则

| 用途 | 正则 | 位置 |
|---|---|---|
| 全维度代码 | `[\[（(]\s*(Y\d)-([SsEeCc]\d{1,2})-([SsEeCc]\d{1,2})-([SsEeCc]\d{1,2})-([SsEeCc]\d{1,2})-([VvZz]\d{2})\s*[\]\)）]` | `scraper.py:89-90` |
| 左右分隔 | `^\s*(?P<left>[^—–\|丨]+?)\s*(?:[-—–\|丨])\s*(?P<right>.+?)\s*$` | `scraper.py:151` |
| 轨道号前缀 | `^\d{1,3}\s*[._、\-\s]\s*` | `scraper.py:148` |
| 纯乱码名 | `^[\d\W_]+$` → 判为无法解析 | `scraper.py:174` |
| 括号注释剥离 | `[\[\(（【].*?[\]\)）】]` | `scraper.py:75` |
| 噪音词剥离 | `无损\|Hi-Res\|FLAC\|MP3\|320K\|24bit\|96k\|WAV\|DSD\|单曲\|专辑\|live\|Live\|remastered\|acoustic` | `scraper.py:77` |

### 2.3 关键规则与「顺序歧义」

- **带全维度代码的曲库统一「歌名 - 歌手」；普通文件默认「歌手 - 歌名」**（`scraper.py:156-163`）。同时返回 `left/right` 两个原始 token 供上层消歧。
- `_parse_name` 不直接信任默认解释：把 `(artist,title)` 和交换后的 `(title,artist)` 组成 `pairs` 交给 `resolve_and_match`，只有**曲库置信匹配**才采用（`tidy_engine.py:1704-1721`）。这是「宁可无结果也不覆盖文件名信息」的体现。
- 自定义规则：`normalize_rule_regex` 把 JS 风格 `(?<name>)` 归一为 Python `(?P<name>)`（`scraper.py:189-191`）；组名限定 `artist/title/album/track`（`scraper.py:186`）。规则可用 LLM 从「样例+一句话解释」生成（`ai_llm.py:123-157`）。
- 注意：`track` 字段仅自定义规则可产出，内置 `parse_filename` 只剥离轨道号、**不返回 track**（Go 移植时需补）。

### 2.4 全维度代码映射（`scraper.py:92-124`）

`S01-S08` 戏腔风格、`E01-E08` 情绪、`C01-C07` 场景（主+次）、`V00=原唱 / Z00=翻唱`、`Y0=2020及之前，Y1..Y6=2021..2026`。解析结果 `{code, year, style, emotion, scene1, scene2, version}` + 各 `*_label`。代码会被 `strip_tidy_code` 从参与解析的 basename 中剥掉。

### 2.5 token 清洗 `_clean_token`（`scraper.py:72-79`）

去括号注释 → 去音质/格式/来源噪音词 → 去尾部 `._-` → `strip(" .-_—–")`。

---

## 3. 匹配算法（`scraper.py:973-1111`）

### 3.1 归一化

```python
_title_norm  = 去括号注释 → 去所有 [\s\-_—–] → lower     # scraper.py:973-977
_artist_norm = lower → 去 [\s\-_—–&,/·、]                 # scraper.py:980-983
```

### 3.2 打分 `score_song`（`scraper.py:992-1029`）— **不是编辑距离，是加权命中 + 硬拒绝**

```
歌名精确 ==  → +100
歌名互相包含 → +45
歌手精确 ==  → +80
歌手互相包含 → +40
候选有封面   → +5
标题含 _COVER_FLAGS（翻唱/cover/女声/伴奏/remix/dj/live/纯音乐…21 词）→ -60
```

**两条硬拒绝（return 0）**：
1. 期望有歌手但候选歌手完全不沾边 → 0（`scraper.py:1015-1016`）
2. 期望有歌名但候选歌名完全不匹配 → 0（`scraper.py:1019-1020`）

注释点明根因：前者导致「补全全是同一张纸嫁衣封面」，后者导致「冷门歌拿到该歌手其它歌的专辑封面」。

### 3.3 阈值与排序

```python
MATCH_MIN = 85                                   # scraper.py:1042
pick_best(songs, want, min_score=MATCH_MIN)      # scraper.py:1045-1048
score_best → max(score_song)                     # scraper.py:1032-1037
```
85 这个值的效果：歌名精确(100) 单独就过；歌名部分(45)+歌手精确(80)=125 过；歌名部分(45)+歌手部分(40)=**恰好 85** 过；仅歌名部分(45) 不过。

### 3.4 搜索变体降级 `_variants`（`scraper.py:1051-1077`）

4 个变体按序尝试：`(原歌手,原歌名)`, `(主歌手,原歌名)`, `(原歌手,净歌名)`, `(主歌手,净歌名)`
- 净歌名：去括号注释 + 截断 `(DJ|Remix|Live|伴奏|纯音乐|女版|男版|氛围版).*$`
- 主歌手：去 `feat./ft./featuring` 后缀、去括号、按 `[&＆、,，/]` 或 `vs.` 取第一段

`search_confident` 按变体逐个搜、逐个评分，首个 ≥85 的采用；**但返回的 artist/title 永远是文件名解析的原值**（文件名优先原则，`scraper.py:1080-1095`）。

### 3.5 网络源与搜索降级

- 刮削侧 `search_song`：**先 QQ 音乐，无结果再网易云**（`scraper.py:1122-1142`），`limit=5`，并写 `diag["qq"]/diag["netease"]` 供上层记录。
- 下载侧 `MusicSearch.search`：5 个内置源 + 自定义 API **全部并发**（每源一线程），`fetch = max(10, min(limit, 30))`，`t.join(timeout=12)`（`music_source.py:1210-1244`）。
- 结果去重键 `name.lower()|artist.lower()`，保留 `_completeness` 更高的一条；再按 `_relevance_score` 排序；**保底每个启用源至少一条**（`music_source.py:1246-1291`）。
- `_relevance_score`（`music_source.py:1019-1050`）：关键词等于歌名 +150；关键词==歌名之一词 +100；包含 +60；逐词 +30；歌手含词 +20；专辑 +5；关键词==歌手 +40；有 quality +3、cover +2。
- VIP 处理：无 Cookie 的 VIP 标 `locked` 沉底；QQ 走 `qq_resolve` 批量 vkey 实测；其它源并发实测试听链，失败标 `unplayable`（`music_source.py:1257-1304`、`1335-1390`）。

---

## 4. 去重逻辑（`tidy_engine.py:2010-2201`）

### 4.1 两类重复

```python
duplicates(kind="both")   # tidy_engine.py:2010-2053
```
1. **content**：`sha1` 相同 → 内容完全一致，确定重复。组内排序 `(-size, -tidied)`（大文件优先）。
2. **name**：`_artist_norm(artist) + "|" + _title_norm(title)` 相同，**且组内 sha1 种类 ≥2**（纯内容重复归 content 组，`tidy_engine.py:2046`）。未整理的用文件名解析兜底补 artist/title（`tidy_engine.py:2037-2040`）。组内排序用 `_quality_key`。

`_quality_key`（`tidy_engine.py:2055-2062`）：
```python
(1 if ext in (.flac,.wav,.ape,.aiff,.aif,.dsf) else 0, tidied, size)   # 无损 > 已整理 > 大文件
```

**没有音频指纹/AcoustID**，只用 SHA-1 与「标题+歌手」归一化键。**没有时长参与判重**（时长只在 AI 决策时作为参考信息出现）。

### 4.2 处理方式

- `resolve_duplicates(keep, delete_paths, mode)`：`delete` 直接 `os.remove`；否则 `shutil.move` 到 `data_dir/.tidy_trash`（默认回收站，`tidy_engine.py:2064-2082`）。删除的同步 `DELETE FROM files`。
- `dedupe_all(mode, out_dir, ai, kind)`（`tidy_engine.py:2102-2201`）：
  - `mode=move` 必须给 out_dir，且**校验 out_dir 不在任何音乐库目录内**（`tidy_engine.py:2112-2123`）；落盘时保留相对音乐库根的目录结构，同名加 `.dup<时间戳>`。
  - `ai=True` 时 `ffprobe` 取时长/码率，调 `llm.dedupe_decide()` 决定 keep_index；失败回退启发式。
  - 混合检查时同一文件可能属两组，以**磁盘现状**过滤，keep 若已被前组处理则顺延到组内下一个仍存在的文件（`tidy_engine.py:2153-2162`）。

### 4.3 重复封面检测（刮削质量兜底）

`duplicate_covers(threshold=3)`（`tidy_engine.py:2318-2369`）：按逐曲同名封面的 **SHA-1** 分组，若「不同目录数 ≥ threshold」**或**「不同歌手数 ≥ threshold」判为刮削错误组。仅统计逐曲同名图，目录级 `cover.jpg` 排除。整理任务结束后自动跑一次（`tidy_engine.py:1996-2005`）。修复走 `fix_duplicate_covers` → `_refetch_cover`，重刮后若新图 SHA-1 与旧图相同则拒绝替换（`tidy_engine.py:2417-2427`）。

---

## 5. 目录整理规则

### 5.1 目录模板

**没有 `歌手/专辑/音轨 - 歌名` 这种重命名模板**。实际规则是：

```
<原文件所在目录>/<artist 清洗>/[<album 清洗>]/<原文件名不变>     # _classify, tidy_engine.py:1856-1889
<out_dir>/<artist 清洗>/[<album 清洗>]/<原文件名>                # organize_move, tidy_engine.py:2805-2879
<下载目录>/<artist> - <name>.<ext>                               # 下载侧命名, music_source.py:1499-1501
```

- 分类是**就地移动**（`shutil.move`，同卷原子），附属文件 `.lrc/.jpg/.jpeg/.png` 随迁（`tidy_engine.py:1877-1885`）。
- 同名冲突：`stem_1.ext`、`stem_2.ext` 递增（`tidy_engine.py:1870-1876`）。
- 目录名清洗 `_sanitize_dir`（`tidy_engine.py:4105-4108`）：
  ```python
  re.sub(r'[\\/:*?"<>|]', "_", name or "Unknown").strip()
  re.sub(r"[\s.]+$", "", name)   # 去尾部空格与点
  return name or "Unknown"
  ```
  **不处理**控制字符、名字过长、Windows 保留名（CON/NUL）——Go 移植建议补齐。
- 只有开启 `enable_classify` 才移动；`tidy()` 检测到开启时会把已整理的也重跑一遍以应用新结构（`tidy_engine.py:1944-1945`）。

### 5.2 命名占位符

没有占位符模板引擎。可复用点：下载命名 `"{artist} - {name}"`，且先做 `re.sub(r'[<>:"/\\|?*]', '', ...)`（`music_source.py:1499-1501`）。若要支持 `%artist%/%album%/%track% - %title%`，需自行实现（Go `text/template` 或自定义）。

---

## 6. 并发与性能

### 6.1 并发模型：**整理主流程严格串行，仅在网络/探测处并发**

- 扫描/整理/分类/touch/organize 都是 `for` 循环逐文件处理，单线程；并发只来自 HTTP 层每个任务一个 `threading.Thread(daemon=True)`。
- 下载队列也是**逐首串行**（`tidy_engine.py:3953-3963`）。
- 并发点：
  | 位置 | 并发度 | 超时 |
  |---|---|---|
  | `MusicSearch.search` 多源 | 每源 1 线程（5+N） | `join(timeout=12)` `music_source.py:1244` |
  | `_probe_lyrics` 歌词探测 | `Semaphore(8)` | `join(timeout=6)` `music_source.py:1312-1333` |
  | `_probe_playable` 试听链探测 | `Semaphore(6)` | `join(timeout=6)` `music_source.py:1373-1390` |
  | `music_source` 封面缓存 | `threading.Lock` | `music_source.py:1069` |
- 进度节流：枚举阶段 `ENUM_TICK_SEC = 0.35s`（`tidy_engine.py:746`），监听 `0.5s`（`watcher.py:81`），下载字节进度 `0.3s`（`tidy_engine.py:4039`）。

### 6.2 大目录不卡死的设计（值得抄）

1. **不预先 count**：`total=0` 是「分母未知」的约定，前端渲染不定长动画+递增计数，避免为算总数再遍历一遍盘（`tidy_engine.py:748-770`）。
2. **逐目录回调 `on_dir`**（v1.12.0 新增）：大曲库枚举几十秒到几分钟，旧版只在整根扫完才响一次，进度条全程 0/1「像卡死」（`scraper.py:610-614`）。回调异常被吞、绝不影响遍历（`scraper.py:682-690`）。
3. **扩展名先判、再 isfile**：去掉九成以上 stat（`scraper.py:252-260`）。
4. **软链剪枝**：`followlinks=True` 但按 `os.path.realpath` 去重，防环防重复计入（`scraper.py:700-724`）。
5. **深度截断**：`max_depth` 默认 12，超出 `dirnames[:] = []`（`scraper.py:730-736`）。
6. **排除目录**：`.tidy_trash/.trash/@eaDir/#recycle/.recycle/$RECYCLE.BIN/System Volume Information`（`tidy_engine.py:146`）。
7. **SQLite WAL** + 每操作短连接 `timeout=15`（`tidy_engine.py:289-293`）。
8. 批量更新按 500 分块，避免 SQLite 999 变量上限（`tidy_engine.py:1900-1904`）。
9. 日志表只保留最近 1000 行（`tidy_engine.py:303`）。

### 6.3 缓存/增量机制

- **增量扫描**：`(size, mtime)` 指纹，只对新增/变化算 SHA-1（§1.2）。
- **增量监听** `MusicWatcher`（`watcher.py:17-165`）：`threading.Thread` 默认间隔 30s，内存快照 `path -> (round(mtime,3), size)`；发现变化 → `ingest_many` → 逐个 `_tidy_file`。保护条件：整库突然枚举为空则保留上次快照不清库；枚举不完整时跳过 removed 清理（`watcher.py:132-147`）。
- **附属状态缓存**：库里的 `has_lyric/has_cover/code` 纯 stat 刷新。
- **响应缓存**：`music_source._cover_cache`（内存字典 + 锁）。
- 无磁盘级元数据缓存，无 mtime 索引；搜索接口无缓存（每次实时请求）。

---

## 7. 写标签能力

### 7.1 库与降级

`_embed_tags`（`tidy_engine.py:1535-1558`）：优先 `vendor/mutagen`（`sys.path.insert(0, _VENDOR)`，`tidy_engine.py:32-34`）；异常时降级 `ffmpeg`（`tidy_engine.py:1681-1698`）；都不可用则只写 sidecar 并打一次警告。受 `config.embed_tags` 开关控制。

### 7.2 分格式写入表（`_embed_mutagen`，`tidy_engine.py:1560-1679`）

| 格式 | 文本字段 | 歌词 | 封面 |
|---|---|---|---|
| `.flac` | `artist/title/album/genre(=style)/lyrics` Vorbis | `lyrics` | `Picture(type=3, mime)` + `clear_pictures()` |
| `.m4a/.mp4/.m4b/.m4v` | `©ART/©nam/©alb/©gen` | `©lyr` | `MP4Cover`(PNG/JPEG 按魔数) |
| `.ogg/.oga/.opus` | `artist/title/album/genre/lyrics` | `lyrics` | `metadata_block_picture` = base64(Picture.write()) |
| `.wav` | `TIT2/TPE1/TALB/TCON` (ID3 in RIFF) | `USLT(lang=chi)` | `APIC(type=3, desc=Cover)`，先 `delall("APIC")` |
| 其它(mp3 等) | ID3 `TIT2/TPE1/TALB/TCON` | `USLT(lang=chi)` | `APIC`，`save(v2_version=3)` |
| `.aiff/.aif/.aifc/.wma/.asf` | **拒绝**，避免写坏文件（`tidy_engine.py:1636-1637`） |

`_image_mime` 靠 PNG 魔数判断，否则一律 `image/jpeg`（`tidy_engine.py:1529-1533`）。

### 7.3 内嵌封面提取 `_extract_embedded_cover`（`tidy_engine.py:1359-1419`）

反向读取：FLAC `f.pictures[0].data`；MP4 `covr[0]`；OGG/Opus `metadata_block_picture` base64；mp3/其它 `ID3.getall("APIC")[0].data`。命中就写逐曲同名 `.jpg` 并**跳过在线搜索**。

### 7.4 Sidecar 写入 `_write_sidecar`（`tidy_engine.py:1473-1527`）

- `.lrc`：UTF-8，末尾补 `\n`；`overwrite_lyric` 或旧文件乱码才覆盖；乱码且无新词时**删除**乱码文件让状态回归「缺歌词」。
- 封面：只认**逐曲同名** `.jpg/.jpeg/.png`；显式不写目录级 `cover.jpg`（会被同目录多首歌共用造成「封面都一样」，`tidy_engine.py:1519-1520`）。
- 写完后 `_touch_for_rescan` 刷新音频+sidecar 的 mtime，让外部播放器的增量扫描感知变化（`tidy_engine.py:1457-1471`）。

---

## 8. 关键常量速查表

| 常量 | 值 | 位置 |
|---|---|---|
| `MATCH_MIN` | 85 | `scraper.py:1042` |
| 歌名精确/包含 | +100 / +45 | `scraper.py:1006-1009` |
| 歌手精确/包含 | +80 / +40 | `scraper.py:1011-1014` |
| 有封面 / 翻唱标记 | +5 / -60 | `scraper.py:1022-1028` |
| 相关度 | 150/100/60/30/20/5/40 | `music_source.py:1027-1044` |
| `DEFAULT_TIMEOUT`（HTTP） | 12s | `scraper.py:55` |
| 搜索线程 join | 12s | `music_source.py:1244` |
| 歌词探测并发/超时 | `Semaphore(8)` / join 6s | `music_source.py:1312-1333` |
| 试听探测并发/超时 | `Semaphore(6)` / join 6s | `music_source.py:1373-1390` |
| 搜索返回上限 | `max(10, min(limit,30))` | `music_source.py:1215` |
| `max_depth` | 12 | `tidy_engine.py:143` / `scraper.py:587` |
| `watch_interval` | 30s（下限 5） | `tidy_engine.py:145` / `watcher.py:21` |
| `ENUM_TICK_SEC` | 0.35s | `tidy_engine.py:746` |
| 监听进度节流 | 0.5s | `watcher.py:81` |
| 下载字节进度节流 | 0.3s | `tidy_engine.py:4039` |
| 任务快照落盘节流 | 3.0s | `tidy_engine.py:572` |
| `AUDIT_LOG_LINES` | 40 | `tidy_engine.py:851` |
| `DENIED_PROBE_LIMIT` | 60 | `tidy_engine.py:898` |
| 日志表上限 | 1000 行 | `tidy_engine.py:303` |
| SQLite 批更新块 | 500 | `tidy_engine.py:1900` |
| `duplicate_covers` 阈值 | 3（下限 2） | `tidy_engine.py:2318-2326` |
| ffprobe 超时 | 10s | `tidy_engine.py:2091` |
| ffmpeg 内嵌超时 | 120s | `tidy_engine.py:1692` |
| ffmpeg 转码超时 | 600s | `tidy_engine.py:4083` |
| 下载 HTTP 超时 / 块 | 60s / 65536B | `music_source.py:1515,1536` |
| LLM 默认超时 | 20s（生图 60s） | `tidy_engine.py:132` / `ai_llm.py:38,45` |
| 无损扩展名集合 | `.flac .wav .ape .aiff .aif .dsf` | `tidy_engine.py:2055` |
| 音频扩展名 | 32 种 | `scraper.py:26-32` |
| 伴生文件扩展名 | 51 种 | `scraper.py:37-44` |
| 加密容器扩展名 | 30 种(ncm/qmc 家族) | `scraper.py:48-53` |

---

## 9. 可借鉴要点清单（面向 Go + Vue 音乐应用）

> 现状：只有基础 NAS 扫描，无刮削/整理/去重。以下按「先落地、收益高」排序，每条给出可直接照抄的规则与 Go 侧落点。

### P0 — 增量扫描与安全门（替换现有全量扫描，风险最低、收益最大）

1. **三阶段扫描**：枚举（只 walk + 扩展名判断）→ `(size, mtime)` 对比 → 只对新/变化文件算哈希。
   - Go：`filepath.WalkDir` + `fs.DirEntry.Info()`；变化判定 `size != row.Size || math.Abs(mtime.Sub(row.MTime)) > time.Second`。
   - 数据库加 `INDEX(folder)`、`INDEX(sha1)`、`INDEX(code)`（照抄 `tidy_engine.py:70-72`）。
2. **「枚举不完整就禁止删库」的安全门**（`tidy_engine.py:1244-1256`）——这是本引擎最值得抄的一条。每个根目录记录 `exists/errs/depthCut`，只有三者全部干净才允许把「本轮没枚举到」的库记录当作磁盘删除。否则挂载抖动会整库清空。
3. **枚举对账（audit）**：把 `files_total = audio + skip_known + encrypted + ext_skipped + no_ext + inaccessible` 算平（`scraper.py:616-620`）。用户问「为什么比飞牛少」时有直接答案。Go 侧定义 `WalkAudit` 结构体即可。
4. **逐目录进度回调 + 不预 count**（`scraper.py:676-690`）：`total=0` 表示分母未知，前端渲染不定长动画；进度回调必须 `recover()` 包裹，回调 panic 不能带崩遍历。
5. **软链按 realpath 剪枝、深度上限 12、排除目录集**（`scraper.py:700-736`、`tidy_engine.py:146`）。

### P0 — 文件名解析器

6. **三级降级**：用户自定义命名分组正则 → 内置正则 → 整段作为 title；解析不出再上 LLM。
   - Go：`regexp` 不支持命名分组回读？实际支持 `SubexpIndex(name)`，可用 `(?P<artist>...)`。
7. **四条内置正则**照抄 §2.2；`_clean_token` 的括号剥离与噪音词表照抄。
8. **「A - B」顺序歧义**：不要猜死。返回 `left/right`，用「搜索能否置信匹配」来消歧（`tidy_engine.py:1704-1721` + `scraper.py:1098-1111`）。
9. **文件名优先原则**：匹配成功后，返回给用户的 artist/title 仍是文件名解析值，网络结果只用于取歌词/封面（`scraper.py:1083`）。
10. 把全维度代码解析（`S/E/C/V/Y`）做成本地纯函数+映射表，零网络成本换「风格/情绪/场景」三个可用筛选维度（`scraper.py:92-124`）。

### P0 — 匹配打分（不要用编辑距离）

11. **照抄 `score_song` 的加权 + 硬拒绝模型**（§3.2）。中文歌名场景下 Levenshtein 会误配（「晴天」vs「晴天(jay版)」距离小，但「纸嫁衣」类冷门 vs 热歌也会因包含关系误配）。硬拒绝两条是防「整库同一张封面」的关键。
12. **阈值 85 + 变体降级 4 步**（`scraper.py:1042-1095`）。Go 侧实现 `variants()`：去括号、截 `DJ/Remix/Live/伴奏/纯音乐/女版/男版/氛围版`、截 `feat./ft.`、按 `[&＆、,，/]`/`vs.` 取主歌手。
13. **翻唱标记降权表** 21 个词照抄（`scraper.py:987-989`）。
14. **多源并发 + 单源失败不影响整体 + 保底每源一条**（`music_source.py:1213-1291`）。Go：`errgroup` + 每源 `context.WithTimeout(ctx, 12*time.Second)`；结果去重键 `lower(name)|lower(artist)`，保留信息更全者；排序用 `_relevance_score` 权重表。
15. **可播性实测**：拿到链才算可播，别信 `vip` 标记（`music_source.py:1335-1360`）。Go 侧并发信号量 6~8，全部带超时。

### P1 — 去重

16. **双通道去重**：`sha1` 通道（内容相同）+ `norm(artist)|norm(title)` 通道且要求组内 sha1 种类 ≥2（否则是纯内容重复，避免重复处理）——`tidy_engine.py:2026-2050`。
17. **归一化函数照抄**：歌名去括号+去空白标点+lower；歌手额外去 `&,/·、`（`scraper.py:973-983`）。
18. **保留优先级**：无损后缀集合 → 已整理 → 文件更大（`_quality_key`，`tidy_engine.py:2057-2062`）。**默认移入 `.tidy_trash` 而不是删除**（`tidy_engine.py:2064-2082`）。
19. **重复封面检测**：按封面 SHA-1 分组，`目录数 ≥ 3` 或 `歌手数 ≥ 3` 判为刮削错误（`tidy_engine.py:2318-2369`）。这是成本极低、效果显著的「刮削质量监控」。Go 侧可加上 `length` 与 perceptual hash 相似度做补充。
20. 混合检查时以**磁盘现状**为准过滤、keep 被前组移走则顺延（`tidy_engine.py:2153-2162`）；移出目录必须校验不在音乐库内（`tidy_engine.py:2112-2123`）。

### P1 — 目录整理与文件安全

21. 目录模板先用 `播放器根/<artist>/[<album>]/<原文件名>`，同名加 `_1/_2`；`.lrc/.jpg/.jpeg/.png` 附属文件必须随迁（`tidy_engine.py:1856-1889`）。
22. 目录名清洗照抄 `_sanitize_dir`，并**补齐**：控制字符、名字长度（如 200 字节/段）、Windows 保留名、Unicode 归一化 NFC。Go：`strings.Map` + 白名单。
23. 移动用 `os.Rename`（同卷原子），跨卷 `EXDEV` 时回退「复制到临时名 → fsync → rename → 删源」。所有 sidecar 写入都走 `.tmp` + rename（此引擎在 `_maybe_ai_cover`/下载里做了）。
24. **写完后 touch 文件 mtime**，让下游播放器/飞牛重新索引（`tidy_engine.py:1457-1471`）。Go 侧 `os.Chtimes`。
25. 若要做「玩家端可见整理」，加一个 `dry-run` 模式（本引擎的 `probe_scan` 就是只读体检，`tidy_engine.py:966-1103`），先出报告再动文件。

### P1 — 写标签

26. Go 没有 mutagen，选型建议：
    - 读：`github.com/dhowden/tag`（多容器只读）。
    - 写：`bogem/id3v2`(mp3/wav ID3)、`go-flac/flacvorbis` 或 `mewkiz/flac`(FLAC picture)、`abema/go-mp4`/自定义(MP4 `covr`)、OGG 用 `metadata_block_picture` base64。
    - 稳妥方案：**内嵌写标签统一交给 ffmpeg 子进程**（`-metadata` + `-disposition:v:1 attached_pic`，照抄 `tidy_engine.py:1681-1698`），失败只写 sidecar，保证永不写坏文件。这也和本引擎的降级思路一致。
27. **拒绝写会损坏的容器**：AIFF/WMA/ASF 直接返回 false（`tidy_engine.py:1636-1637`）。
28. 字段映射表照抄 §7.2，注意 `genre` 复用 `style`（本引擎用它驱动外部播放器的「风格」页）。
29. **sidecar 优先**是兼容性最强的策略：同名 `.lrc` + 同名 `.jpg`，播放器普遍识别；且**绝不写目录级 `cover.jpg`**（会被同目录歌曲共用）。

### P2 — 任务框架与观测

30. **任务状态机 + 快照落盘**：`idle/running/paused/done/stopped/error` + `stage` 中文阶段串 + `seq`；节流 3s 落盘、终态强制落盘；重启后只给「一键继续」建议、不自动重跑（`tidy_engine.py:571-639`）。Vue 侧 `renderTask` 消费 `total==0` 表示不定长。
31. **暂停/停止只在文件边界生效**：`_check_ctrl()`（`tidy_engine.py:272-276`）。Go：每个文件开头 `select { case <-ctx.Done(): return; default: }`。
32. **失败原因分类统计**（`parse_fail / search_empty / matched_but_no_media / err`，`tidy_engine.py:1955-1989`），并在日志里打 3 条失败样例。这是运营可观测性的最低成本。
33. **权限问题的可执行指引**：把 errno 映射成人话 + 三条修复路径（继承父级权限 / ACL / `setfacl`）（`tidy_engine.py:880-885`、`scraper.py:265-300`）。NAS 场景用户最需要这个。
34. **非 UTF-8 文件名**：入库前做 `\backslashreplace` 清洗，否则 SQLite/JSON 绑定直接抛错导致「文件莫名入不了库」（`tidy_engine.py:169-183`、`1313-1318`）。Go 侧注意 `[]byte`→`string` 的非法 UTF-8，JSON 编码会替换为 U+FFFD，需先 `strings.ToValidUTF8` 并记录原始字节（如 base64 备份）。
35. **乱码歌词防护**：`lyric_garbled` = U+FFFD + C1 控制符数量 > `max(4, 2%*len)` 则丢弃/删除旧文件（`scraper.py:1114-1119`、`tidy_engine.py:1477-1499`）。
36. **批量 SQL 分块 500**、日志表保留 1000 行、SQLite WAL。

### 可暂缓/需替换

- 全维度代码 `S/E/C/V` 是本曲库私有规范，Go 应用可保留为「可选插件式规则」，不要写死。
- LLM 兜底（`ai_llm.py:106-121` 的 JSON 契约识别、`159-200` 的去重决策）建议做成可选开关；提示词可直接复用。若接入，务必像本引擎一样带 `_with_user` 式的「格式契约不可覆盖」保护（`ai_llm.py:202-213`）。
- 下载器的 `.part` 断点续传 + `Range` 206 校验（`music_source.py:1487-1554`）对音乐播放器价值不大，除非也做下载中心。
- 本引擎**没有** track 号、没有多碟、没有 `disc/track` 目录模板、没有音频指纹 — 若 Go 应用要做「同一首歌不同版本智能归并」，需自行引入 Chromaprint/AcoustID 或音频时长+码率联合判定。
