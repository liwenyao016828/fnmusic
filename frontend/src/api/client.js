import axios from 'axios'

const api = axios.create({
  baseURL: '/api',
  timeout: 30000,
})

export const ConfigAPI = {
  get: () => api.get('/config').then(res => res.data),
  save: (cfg) => api.post('/config', cfg).then(res => res.data),
}

export const SourcesAPI = {
  list: () => api.get('/sources').then(res => res.data),
  getScript: (id) => api.get(`/sources/${encodeURIComponent(id)}/script`).then(res => res.data),
  // active: true=加入启用池 / false=从池里摘掉 / 不传=旧语义（池里只留它）
  setActive: (id, active) => api.post('/sources/active',
    active === undefined ? { id } : { id, active }).then(res => res.data),
  importUrl: (url, name) => api.post('/sources/import_url', { url, name }).then(res => res.data),
  // 批量导入：一次提交多个 URL 与/或本地脚本，避免一个个导
  importBatch: (urls = [], scripts = []) =>
    api.post('/sources/import_batch', { urls, scripts }, { timeout: 180000 }).then(res => res.data),
  upload: (data) => api.post('/sources/upload', data).then(res => res.data),
  delete: (id) => api.delete(`/sources/${encodeURIComponent(id)}`).then(res => res.data),
  // 批量删除（用于「一键清理失效音源」）
  batchDelete: (ids) => api.post('/sources/batch_delete', { ids }).then(res => res.data),

  // ── 「按服务地址」接入的音源（服务型，后端直连，不执行第三方 JS）──
  apiList: () => api.get('/sources/api').then(res => res.data),
  apiDetect: (baseUrl, token = '') =>
    api.post('/sources/api/detect', { base_url: baseUrl, token }, { timeout: 90000 }).then(res => res.data),
  apiSave: (payload) => api.post('/sources/api', payload).then(res => res.data),
  apiDelete: (id) => api.delete('/sources/api', { params: { id } }).then(res => res.data),
  apiResolve: (id, platform, songId, quality = '320k') =>
    api.post('/sources/api/resolve', { id, platform, song_id: songId, quality }).then(res => res.data),
}

// ── musicdl 外挂音源（v2.1.89）──
//
// 三个只读接口：状态 / 源清单 / 单源检测。
// **开关不在这里写** —— 启用哪些源走 `ConfigAPI.save({ musicdl_sources })`，
// 与榜单注入、页面注入同一套口径（一个写配置的地方，好排查）。
export const MusicDLAPI = {
  status: () => api.get('/musicdl/status').then(res => res.data),
  sources: () => api.get('/musicdl/sources').then(res => res.data),
  // 检测：不传 sources 就测当前启用的那些；keyword 不传用 sidecar 的默认
  // ⚠️ 单独放宽超时：每个源要真搜一次，慢的源会跑到十几秒（默认 30s 会截断）
  probe: (sources, keyword) =>
    api.post('/musicdl/probe', { sources, keyword }, { timeout: 180000 }).then(res => res.data),
  // restart 让界面上的「重试启动」真的把挂掉的 sidecar 拉起来 ——
  // sidecar 会因瞬时原因挂掉（OOM / 装依赖断网 / 端口被占），没有这个入口
  // 用户只能看着「启动失败」，要么去动一个音源开关，要么重启整个应用。
  restart: () => api.post('/musicdl/restart', null, { timeout: 60000 }).then(res => res.data),
  // 试运行（预览一个未启用的源）：临时拉起 → 看状态 → 手动停。
  // 服务端默认 TTL 300 秒，到点自动回收（进程也停），**不写配置、不进搜索**。
  previewStatus: () => api.get('/musicdl/preview').then(res => res.data),
  // 起进程可能要等 sidecar 自己的状态机（首次建 venv 一两分钟），但接口是
  // **异步返回**的（真实进度看 previewStatus 的 state），所以超时不用给太长。
  previewStart: (source) => api.post('/musicdl/preview', { source }, { timeout: 60000 }).then(res => res.data),
  previewStop: () => api.delete('/musicdl/preview', { timeout: 30000 }).then(res => res.data),
}

export const ChartsAPI = {
  toplists: (source = 'kw') => api.get('/charts/toplists', { params: { source } }).then(res => res.data),
  chartDetail: (id, source = 'kw', name = '') => api.get('/charts/detail', { params: { id, source, name } }).then(res => res.data),
  playlists: (category = '全部', page = 1, limit = 24, source = 'kw') => 
    api.get('/charts/playlists', { params: { category, page, limit, source } }).then(res => res.data),
  playlistDetail: (id, source = 'kw', name = '') => api.get('/charts/playlist/detail', { params: { id, source, name } }).then(res => res.data),
  // 直接传歌单分享链接（用户从 App「复制链接」得到的就是链接，不是裸 id）
  playlistDetailByUrl: (url) => api.get('/charts/playlist/detail', { params: { url } }).then(res => res.data),
  // 某平台的歌单分类（各平台能力不同，没有分类的平台返回空数组）
  playlistCategories: (source = 'wy') =>
    api.get('/charts/playlist/categories', { params: { source } }).then(res => res.data),
}

// ── 榜单注入（把榜单挂成飞牛音乐里的在线歌单）──
//
// 这些键都存在整份配置里（`chart_playlists` / `chart_playlists_enabled` /
// `ui_inject_enabled`），所以读走 `/config`、写走 POST `/config` 的**局部 patch**
// （后端是选择性合并：没出现的键一律不动，所以这里可以一次只发一个键）。
export const ChartInjectAPI = {
  read: async () => {
    const res = await api.get('/config')
    const cfg = res.data?.data || res.data?.config || {}
    return {
      // 缺「开关」视为开（与 AppConfig.ChartsInjectEnabled 的口径一致），
      // 只有显式写了 false 才算关。下面两个开关都是这个口径。
      enabled: cfg.chart_playlists_enabled !== false,
      ids: Array.isArray(cfg.chart_playlists) ? cfg.chart_playlists : [],
      // 页面注入：飞牛自带音乐页里那份「下载到曲率」菜单项（以及以后的双向切换按钮）。
      uiInject: cfg.ui_inject_enabled !== false,
    }
  },
  saveIds: (ids) => api.post('/config', { chart_playlists: ids }).then(res => res.data),
  saveEnabled: (enabled) => api.post('/config', { chart_playlists_enabled: !!enabled }).then(res => res.data),
  // 关掉后飞牛自带音乐页**完全原样**：转发层根本不认领页面文档，逐字节透传
  saveUIInject: (enabled) => api.post('/config', { ui_inject_enabled: !!enabled }).then(res => res.data),
}

export const DownloadAPI = {
  getConfig: () => api.get('/download/config').then(res => res.data),
  saveConfig: (dir) => api.post('/download/config', { download_dir: dir }).then(res => res.data),
  downloadSong: (song) => api.post('/download/song', song).then(res => res.data),
  downloadBatch: (songs) => api.post('/download/batch', { songs }).then(res => res.data),
  getBatchStatus: (taskId) => api.get('/download/batch/status', { params: { task_id: taskId } }).then(res => res.data),
  checkDownloaded: (songs) => api.post('/download/check', { songs }).then(res => res.data),
}

/**
 * 下载队列的持久化（落盘在 NAS 上）。
 *
 * 队列状态机归 `services/downloadManager` 所有，后端只当它是不透明的 JSON 数组 ——
 * 所以这里是**整体读写**，不是按条增删。见 `backend/pkg/downloadqueue` 的包注释。
 */
export const DownloadQueueAPI = {
  load: () => api.get('/download/queue').then(res => res.data),
  save: (tasks) => api.put('/download/queue', { tasks }).then(res => res.data),
}

export const SearchAPI = {
  search: (keyword, source = 'all', page = 1) => 
    api.get('/search', { params: { q: keyword, source, page } }).then(res => res.data),
  lyric: (source, songmid, title = '', singer = '', duration = 0, hash = '') => 
    api.get('/search/lyric', { params: { source, songmid, title, singer, duration, hash } }).then(res => res.data),
}

// 音源协议：官方协议（后端内置，免脚本）与 LX 协议（浏览器脚本，取直链）
export const ProtocolAPI = {
  list: () => api.get('/protocols').then(res => res.data),
}

// 歌曲音质查询（官方协议，免音源；实现为「搜索 + 按 ID 匹配」）
export const MusicAPI = {
  qualities: (source, id, name = '', singer = '', hash = '') =>
    api.get('/music/qualities', { params: { source, id, name, singer, hash } }).then(res => res.data),
}

export const NasAPI = {
  directories: () => api.get('/nas/directories').then(res => res.data),
  // opts.files=true 时后端会额外返回普通文件列表（默认不返回，避免放大 JSON）
  browse: (path = '', opts = {}) => api.get('/nas/browse', {
    params: { path, ...(opts.files ? { files: 1, ext: opts.ext || '' } : {}) },
  }).then(res => res.data),
  // 读文本文件（从 NAS 选音源脚本用）。后端有扩展名白名单 + 2MB 上限，见 pkg/nas/readfile.go
  readText: (path) => api.get('/nas/read', { params: { path } }).then(res => res.data),
  scan: (dir, refresh = false) => 
    api.get('/nas/scan', { params: { dir, refresh } }).then(res => res.data),
  streamUrl: (path) => `/api/nas/stream?path=${encodeURIComponent(path)}`,
  coverUrl: (path) => `/api/nas/cover?path=${encodeURIComponent(path)}`,
  lyric: (path) => api.get('/nas/lyric', { params: { path } }).then(res => res.data),
}

export const ProxyAPI = {
  request: (options) => api.post('/proxy/http', options).then(res => res.data),
}

// 播放链路：中继（把第三方直链转成浏览器可播的流）与**地址可达性校验**。
export const PlayerAPI = {
  /**
   * 校验一个播放地址是否**真的能取到音频**。
   *
   * 后端只取 2 字节就断开（Range: bytes=0-1），不会下载整个音频。
   * 用途：音源可能返回「形状合法但文件不存在」的地址（实测聚合接口返回 QQ 的
   * `RS02...mp3`，上游回 `file not exist`），不校验的话界面会误判成取链成功。
   */
  check: (url, referer = '') =>
    api.get('/player/check', { params: { url, referer }, timeout: 20000 }).then(res => res.data),
}

// 音频标签：内嵌歌词 / 封面 / 元数据读写
export const TagsAPI = {
  read: (path) => api.get('/nas/tags', { params: { path } }).then(res => res.data),
  write: (payload) => api.post('/nas/tags', payload).then(res => res.data),
  // 搜封面候选图（手动换封面用）：按歌名+歌手多平台搜，返回若干张可选。
  // 多平台串行搜索，给足超时（默认超时太短会误报失败）。
  coverCandidates: (params) =>
    api.get('/library/cover-candidates', { params, timeout: 30000 }).then(res => res.data),
}

export const AppAPI = {
  getVersion: () => api.get('/app/version').then(res => res.data),
  health: () => api.get('/health').then(res => res.data),
  catalog: () => api.get('/catalog').then(res => res.data),
}

// 飞牛 fnOS 集成：状态、授权目录、飞牛音乐歌单
export const FnosAPI = {
  status: () => api.get('/fnos/status').then(res => res.data),
  folders: () => api.get('/fnos/folders').then(res => res.data),
  playlists: () => api.get('/fnos/playlists').then(res => res.data),
  push: (payload) => api.post('/fnos/push', payload).then(res => res.data),
  // 真实探一次连接（调飞牛 /user/me），与 status 的「看状态」不同
  check: () => api.post('/fnos/check', null, { timeout: 30000 }).then(res => res.data),
  // 手工令牌：传空串表示清除、回到自动读取
  saveToken: (token) => api.post('/fnos/token', { token }).then(res => res.data),
  // 触发曲库重扫（合并调度，立即返回不等待扫描完成）
  rescan: () => api.post('/fnos/rescan').then(res => res.data),
}

// 第三方平台账号（扫码登录）
export const AccountAPI = {
  list: () => api.get('/accounts').then(res => res.data),
  disconnect: (provider) => api.delete('/accounts', { params: { provider } }).then(res => res.data),
  // 归一化曲目：某平台某个来源（日推 / 歌单）的可播放曲目列表
  tracks: (provider, kind = 'daily', id = '', limit = 200) =>
    api.get('/accounts/tracks', { params: { provider, kind, id, limit } }).then(res => res.data),
  // 网易云
  neteaseQR: () => api.post('/accounts/netease/qr').then(res => res.data),
  neteaseQRCheck: (key) => api.get('/accounts/netease/qr/check', { params: { key } }).then(res => res.data),
  neteaseDaily: () => api.get('/accounts/netease/daily').then(res => res.data),
  neteasePlaylists: (limit = 100) => api.get('/accounts/netease/playlists', { params: { limit } }).then(res => res.data),
  // QQ 音乐
  qqQR: () => api.post('/accounts/qq/qr').then(res => res.data),
  qqQRCheck: (key) => api.get('/accounts/qq/qr/check', { params: { key } }).then(res => res.data),
  qqDaily: () => api.get('/accounts/qq/daily').then(res => res.data),
  qqPlaylists: () => api.get('/accounts/qq/playlists').then(res => res.data),
  qqPlaylist: (id) => api.get('/accounts/qq/playlist', { params: { id } }).then(res => res.data),
}

// 推送同步：账号歌单/日推 → 飞牛音乐歌单
export const PushAPI = {
  sources: (provider) => api.get('/push/sources', { params: { provider } }).then(res => res.data),
  tasks: () => api.get('/push/tasks').then(res => res.data),
  saveTask: (task) => api.post('/push/tasks', task).then(res => res.data),
  deleteTask: (id) => api.delete('/push/tasks', { params: { id } }).then(res => res.data),
  run: (id) => api.post('/push/run', null, { params: { id }, timeout: 300000 }).then(res => res.data),
  preview: (payload) => api.post('/push/preview', payload, { timeout: 300000 }).then(res => res.data),
  runs: (taskId = '', limit = 50) => api.get('/push/runs', { params: { task_id: taskId, limit } }).then(res => res.data),
}

// 窗口记忆：各页面点击状态（发现页 Tab、平台、NAS 目录、播放模式等）持久化到服务端文件
export const UiPrefsAPI = {
  getAll: () => api.get('/ui-prefs').then(res => res.data),
  merge: (patch) => api.post('/ui-prefs', patch).then(res => res.data),
}

// 后端日志：内存环形缓冲，重启应用即清空（不落盘）
export const LogsAPI = {
  list: (limit = 200) => api.get('/logs', { params: { limit } }).then(res => res.data),
  clear: () => api.delete('/logs').then(res => res.data),
  /**
   * 网页端上报前端错误。
   *
   * 界面上的错误提示只留一句人话（见 services/userMsg.js），完整信息
   * （接口路径、堆栈、原始 message）通过这里进运行日志，用户在「日志」页就能查到，
   * 不必去开浏览器控制台。
   */
  report: (level, message, source = 'frontend') =>
    api.post('/logs', { source, level, message }, { timeout: 10000 }).then(res => res.data),
}

// AI 解析桥：外部调用者提交解析任务，网页端领取后调用音源脚本完成取链
export const BridgeAPI = {
  resolve: (payload) => api.post('/bridge/resolve', payload).then(res => res.data),
  job: (id) => api.get(`/bridge/jobs/${encodeURIComponent(id)}`).then(res => res.data),
  pending: () => api.get('/bridge/pending').then(res => res.data),
  claim: (id) => api.post('/bridge/claim', { job_id: id }).then(res => res.data),
  result: (payload) => api.post('/bridge/result', payload).then(res => res.data),
  stats: () => api.get('/bridge/stats').then(res => res.data),
}

// 歌单监控：榜单/歌单/收藏夹的定时增量下载
export const MonitorAPI = {
  list: () => api.get('/monitors').then(res => res.data),
  create: (payload) => api.post('/monitors', payload).then(res => res.data),
  detail: (id) => api.get(`/monitors/${encodeURIComponent(id)}`).then(res => res.data),
  update: (id, patch) => api.patch(`/monitors/${encodeURIComponent(id)}`, patch).then(res => res.data),
  remove: (id) => api.delete(`/monitors/${encodeURIComponent(id)}`).then(res => res.data),
  discover: (id, payload) => api.post(`/monitors/${encodeURIComponent(id)}/discover`, payload).then(res => res.data),
  run: (id) => api.post(`/monitors/${encodeURIComponent(id)}/run`).then(res => res.data),
  preview: (payload) => api.post('/monitor/preview', payload).then(res => res.data),
  tracks: (params = {}) => api.get('/monitor/tracks', { params }).then(res => res.data),
  /** 补下载完成后回写曲目状态：{monitor_id, status?, items:[{source,song_id,file_path?}]} */
  markTracks: (payload) => api.post('/monitor/tracks/mark', payload).then(res => res.data),
  runs: (params = {}) => api.get('/monitor/runs', { params }).then(res => res.data),
  due: () => api.get('/monitor/due').then(res => res.data),
}

// 整理与去重
export const TidyAPI = {
  run: (payload) => api.post('/tidy', payload).then(res => res.data),
  audit: (dir, sample) => api.get('/library/audit', { params: { dir, sample } }).then(res => res.data),
  index: (dir) => api.get('/library/index', { params: { dir } }).then(res => res.data),
  indexList: (dir, offset = 0, limit = 100) => api.get('/library/index/list', { params: { dir, offset, limit } }).then(res => res.data),
  indexStats: (dir) => api.get('/library/index/stats', { params: { dir } }).then(res => res.data),
  /**
   * 各字段缺口统计（只读索引，不开文件，秒回）。
   *
   * `data.unread` 是**还没读过标签**的条数 —— 这些记录的缺口无从判断，
   * 所以界面上要区分「真缺」和「还没扫描到」，后者提示先跑 `index(dir)`。
   */
  gaps: (dir) => api.get('/library/gaps', { params: { dir } }).then(res => res.data),
  duplicates: (dir, name) => api.get('/duplicates', { params: { dir, name } }).then(res => res.data),
  resolveDuplicates: (payload) => api.post('/duplicates/resolve', payload).then(res => res.data),
  /**
   * 按字段补全曲库（异步任务）。
   *
   * POST 只是**启动**任务并立即返回 202 —— 一首歌要走「搜索 + 取歌词」两次外网请求，
   * 几十首要几分钟，同步请求必然超时。进度靠 `completeStatus` 轮询。
   *
   * `fields`: ['lyric','cover','genre','year','track','disc'] 或 'all'。
   * **不传就只补歌词+封面**（老口径）。所有值都来自搜索命中的那条曲目，匹配不上就跳过 ——
   * 未勾选的字段也不会被改写（后端标签写入是合并语义）。
   */
  complete: (payload) => api.post('/library/complete', payload).then(res => res.data),
  completeStatus: () => api.get('/library/complete').then(res => res.data),
  /**
   * 预览：只解析「文件 → 匹配到的歌」，**不写任何文件**。
   *
   * 补全会真正改写用户的音频文件，所以界面走的是「预览 → 确认 → 写入」三步。
   * 返回里带 `plan_id`，执行时要把它传回去 —— 执行用的是**预览时那份计划**
   * （含匹配到的曲目标识），不重新搜一次，否则用户会「批准了 A、结果写了 B」。
   */
  previewComplete: (payload) =>
    api.post('/library/complete', { ...payload, dry_run: true }).then(res => res.data),
  /** 执行已确认的计划（计划只能用一次） */
  executeComplete: (planId) =>
    api.post('/library/complete', { plan_id: planId }).then(res => res.data),
  /**
   * 停止正在跑的补全任务。
   *
   * 一批最多 200 首、每首要打两次外网并改写文件，跑起来是分钟级的 ——
   * 点错了或发现挑出来的歌不对，得有办法叫停（否则只能等它改完）。
   *
   * 停止 = **不再开始新的曲目**：已写入的保留（可 undo 回退），正在写的那一首会写完
   * （写到一半中断会留下半个标签块）。没在跑时返回「没有正在跑的补全任务」。
   */
  cancelComplete: () => api.post('/library/complete/cancel').then(res => res.data),
  /**
   * 撤销最近一次补全：把改过的文件从备份还原。
   *
   * 补全会真正改写用户的音频文件，所以每次改写前都留了一份备份。
   * 撤销只做一次（没有「重做」）；确认不需要还原了可以用 discard 只删备份释放空间。
   */
  undoComplete: () => api.post('/library/complete/undo').then(res => res.data),
  discardCompleteBackup: () => api.delete('/library/complete/undo').then(res => res.data),
  /**
   * 命名解析规则。
   *
   * `saveNameRule` 有三种用法，靠 body 里有没有 `regex` 区分：
   *   ① { sample, desc }            → 让 AI 出正则，**校验后返回建议但不保存**
   *   ② { regex, sample, dry_run }  → 只回测
   *   ③ { regex, sample, ... }      → 校验通过才保存
   */
  nameRules: () => api.get('/library/name-rules').then(res => res.data),
  saveNameRule: (payload) => api.post('/library/name-rules', payload).then(res => res.data),
  removeNameRule: (id) => api.delete('/library/name-rules', { params: { id } }).then(res => res.data),
}

// AI 大模型
export const AIAPI = {
  getConfig: () => api.get('/ai/config').then(res => res.data),
  saveConfig: (cfg) => api.post('/ai/config', cfg).then(res => res.data),
  usage: (recent) => api.get('/ai/usage', { params: { recent } }).then(res => res.data),
  resetUsage: () => api.post('/ai/usage/reset').then(res => res.data),
  test: () => api.post('/ai/test').then(res => res.data),
  complete: (payload) => api.post('/ai/complete', payload).then(res => res.data),
}

export default api
