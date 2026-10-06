// 平台扫码登录（网易云 / QQ 音乐）的状态处理逻辑。
//
// 为什么单独成文件：这段状态机原先埋在「账号连接」页里，而两个平台的返回口径完全不一样
// —— 网易云给数字码（800 过期 / 801 待扫 / 802 待确认 / 803 成功），
// QQ 给字符串（waiting / scanned / expired / success）。界面上「等待扫码…」「二维码已过期」
// 这些话、能不能刷新二维码、要不要自动关弹窗，全部从这里派生。
// 映射写错的后果不是报错，而是**永远停在「等待扫码…」**——用户以为手机没扫上，
// 实际早就登录成功了；这种假状态比报错难查得多，所以逻辑抽出来单测。
//
// 2026-09-27：扫码登录入口从「账号连接」搬到「发现音乐 → 双平台推荐」
// （用户要求去掉重复的两处），逻辑只留这一份实现。

/** 轮询间隔（毫秒）。平台侧二维码状态大约一秒一变，两秒一轮够快也不至于打平台接口。 */
export const QR_POLL_MS = 2000

/** 登录成功后停留多久再自动关弹窗（让用户看清「登录成功」） */
export const LOGIN_SUCCESS_CLOSE_MS = 900

/** 界面固定文案：模板与测试共用同一份，避免两处写得不一样 */
export const QR_TEXT = {
  waiting: '等待扫码…',
  scanned: '已扫码，请在手机上确认',
  expired: '二维码已过期',
  success: '登录成功',
  saveFailed: '授权成功但保存失败',
  rendering: '二维码生成中…',
}

/** 网易云数字码兜底文案（801 待扫 / 802 待确认；平台给了 status 就用平台的） */
const NETEASE_TEXT = { 801: QR_TEXT.waiting, 802: QR_TEXT.scanned }

/** QQ 字符串状态 → 界面文案 */
const QQ_TEXT = {
  waiting: QR_TEXT.waiting,
  scanned: QR_TEXT.scanned,
  expired: QR_TEXT.expired,
}

/**
 * 一次轮询返回值的「界面看法」。
 *
 * `done` = 不用再轮询了（成功或过期）；`saved` = 凭据确实写进本机
 * ——「授权成功但保存失败」时必须为 false，否则界面会当成功处理。
 *
 * @param {'netease'|'qq'|string} platformId
 * @param {object} data 后端 `/accounts/<platform>/qr/check` 的 `data`
 * @returns {{status: string, ok: boolean, expired: boolean, done: boolean, saved: boolean}}
 */
export function qrCheckView(platformId, data) {
  const d = data || {}

  if (platformId === 'netease') {
    const code = Number(d.code)
    if (code === 803) return successView(d)
    if (code === 800) return expiredView()
    return pendingView(d.status || NETEASE_TEXT[code] || QR_TEXT.waiting)
  }

  if (platformId === 'qq') {
    const state = d.state
    if (state === 'success') return successView(d)
    if (state === 'expired') return expiredView()
    return pendingView(QQ_TEXT[state] || state || QR_TEXT.waiting)
  }

  // 未知平台：只照抄平台给的文案，不猜状态（猜错就是假「已登录」）
  return pendingView(d.status || d.state || QR_TEXT.waiting)
}

function successView(d) {
  return {
    status: d.saved ? QR_TEXT.success : (d.error || QR_TEXT.saveFailed),
    ok: true,
    expired: false,
    done: true,
    saved: !!d.saved,
  }
}

function expiredView() {
  return { status: QR_TEXT.expired, ok: false, expired: true, done: true, saved: false }
}

function pendingView(status) {
  return { status, ok: false, expired: false, done: false, saved: false }
}

/**
 * 二维码弹窗的初始会话对象。
 *
 * `image` 由调用方给：QQ 的接口直接返回图片 data URL，网易云只给二维码「内容」，
 * 需要前端用 QRCode 渲染（组件里做，不进这里——这里保持无 DOM、可单测）。
 */
export function qrSessionView({ platformId, platformName, appName, key, image = '' }) {
  return {
    platformId,
    platformName,
    appName,
    key,
    image,
    ...pendingView(QR_TEXT.waiting),
  }
}

/** 登录成功后要不要自动关弹窗：**存盘失败不关**，否则用户看不到那句失败原因 */
export function shouldCloseAfterLogin(view) {
  return !!(view && view.done && view.saved)
}

/** 断开账号前的确认文案（组件与测试共用一份） */
export function disconnectConfirmText(name) {
  return `确定断开「${name}」账号？断开后需重新扫码。`
}
