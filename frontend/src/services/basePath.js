/**
 * 网关前缀（base path）推导 —— 前端所有 URL 的唯一拼装入口。
 *
 * ── 为什么要有它（2026-10-31）────────────────────────────────────────────
 *
 * 曲率改成飞牛「统一网关微应用」：`fpk-package/app/ui/config` 里声明
 * `gatewayPrefix: "/app/yinshu-ai"` + `gatewaySocket: "app.sock"`，桌面入口的
 * iframe 地址从 `http://nas:8898/` 变成 `http://nas:4398/app/yinshu-ai`。
 *
 * 于是前端**不能再写死** `/api/...`：
 *   · 网关只把 `/app/yinshu-ai/**` 这棵路径树转发给本应用的 socket；
 *   · 站点根那一层是 fnOS 门户自己的（实测 `GET /assets/xxx.js` 在 4398 上恒 404），
 *     所以写死 `/api/...` 会打到门户、全站 404；
 *   · 同理，静态资源也必须落在 `/app/yinshu-ai/assets/...`（由 Vite 的 `base` 负责）。
 *
 * ── 前缀从哪儿来（按优先级）──────────────────────────────────────────────
 *
 * 1. `import.meta.env.BASE_URL` —— Vite 打包时按 `base` 注入：
 *    生产构建是 `/app/yinshu-ai/`（见 vite.config.js），`npm run dev` 是 `/`。
 * 2. 运行时 `location.pathname` 里的 `/app/<name>` 段 —— 兜底。
 *    网关文档的说法是「可能已剥离前缀、也可能没剥离」，这里两处都认，避免
 *    打包 base 与实际挂载路径不一致时整站失灵。
 * 3. 都没有 → `'/'`（普通浏览器直连 8898 端口、`node --test` 里没有 location）。
 *
 * ── 为什么不用 axios 的 baseURL 一把梭 ────────────────────────────────────
 *
 * 曲率有一批 URL 是**直接塞进** `<audio src>` / `<img src>` / `window.open` 的
 * （音频流中继 `/api/player/stream`、曲库串流 `/api/nas/stream`、封面
 * `/api/nas/cover`），它们根本不走 axios，必须能单独拼出带前缀的地址。
 * 所以这里提供 `withBase(path)`，axios 的 `baseURL` 也用它算出来，两边同源。
 */

/** 把 `/app/x/`、`/app/x`、`'/'`、`''` 统一成 `''` 或 `/app/x`（无尾斜杠）。 */
function normalizeBase(raw) {
  const s = String(raw || '').trim()
  if (!s || s === '/') return ''
  const trimmed = s.replace(/\/+$/, '')
  return trimmed.startsWith('/') ? trimmed : '/' + trimmed
}

function detectBase() {
  let envBase = ''
  try {
    // Vite 注入；`node --test` 下 import.meta.env 是 undefined，取不到就走下面的兜底。
    envBase = (import.meta.env && import.meta.env.BASE_URL) || ''
  } catch (_) {
    envBase = ''
  }
  if (envBase) return envBase

  try {
    const p = (globalThis.location && globalThis.location.pathname) || ''
    const parts = p.split('/').filter(Boolean)
    if (parts[0] === 'app' && parts.length >= 2) return `/app/${parts[1]}`
  } catch (_) {
    /* 没有 location 就继续兜底 */
  }
  return '/'
}

/** 当前网关前缀；`''` 表示不需要前缀（dev / 端口直连）。 */
export const BASE_PATH = normalizeBase(detectBase())

/**
 * 给一个应用内路径加上网关前缀。
 *
 * `withBase('/api/nas/browse')` → `'/app/yinshu-ai/api/nas/browse'`（网关下）
 *                            → `'/api/nas/browse'`（dev / 端口直连）
 *
 * 幂等：已经带前缀的路径原样返回 —— 有些调用点会拿到后端返回的 URL 再拼一次，
 * 不幂等就会拼成 `/app/yinshu-ai/app/yinshu-ai/api/...`。
 */
export function withBase(path) {
  const p = String(path ?? '')
  if (!p) return BASE_PATH || '/'
  if (BASE_PATH && p.startsWith(BASE_PATH + '/')) return p
  if (BASE_PATH && p === BASE_PATH) return p
  const rel = p.startsWith('/') ? p : '/' + p
  return BASE_PATH + rel
}

/** axios 的 baseURL（`/api` 加上前缀）。 */
export const API_BASE = withBase('/api')
