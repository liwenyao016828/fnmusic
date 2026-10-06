import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  QR_TEXT,
  QR_POLL_MS,
  LOGIN_SUCCESS_CLOSE_MS,
  qrCheckView,
  qrSessionView,
  shouldCloseAfterLogin,
  disconnectConfirmText,
} from './qrLogin.js'

test('网易云：801 待扫 → 继续轮询', () => {
  const v = qrCheckView('netease', { code: 801 })
  assert.equal(v.status, QR_TEXT.waiting)
  assert.equal(v.done, false)
  assert.equal(v.expired, false)
  assert.equal(v.ok, false)
})

test('网易云：802 待确认 → 平台文案优先', () => {
  assert.equal(qrCheckView('netease', { code: 802 }).status, QR_TEXT.scanned)
  assert.equal(
    qrCheckView('netease', { code: 802, status: '已扫码，请在手机上确认' }).status,
    '已扫码，请在手机上确认',
  )
})

test('网易云：803 + saved → 登录成功、要关弹窗', () => {
  const v = qrCheckView('netease', { code: 803, saved: true })
  assert.equal(v.status, QR_TEXT.success)
  assert.equal(v.ok, true)
  assert.equal(v.done, true)
  assert.equal(v.saved, true)
  assert.equal(shouldCloseAfterLogin(v), true)
})

test('网易云：803 但没存盘 → 说失败原因，且不自动关弹窗', () => {
  const v = qrCheckView('netease', { code: 803, saved: false })
  assert.equal(v.status, QR_TEXT.saveFailed)
  assert.equal(v.saved, false)
  assert.equal(v.done, true)
  assert.equal(shouldCloseAfterLogin(v), false)

  const withErr = qrCheckView('netease', { code: 803, saved: false, error: '写凭据失败：权限不足' })
  assert.equal(withErr.status, '写凭据失败：权限不足')
})

test('网易云：800 → 过期，可刷新二维码', () => {
  const v = qrCheckView('netease', { code: 800 })
  assert.equal(v.status, QR_TEXT.expired)
  assert.equal(v.expired, true)
  assert.equal(v.done, true)
  assert.equal(shouldCloseAfterLogin(v), false)
})

test('网易云：认不出的码不猜状态，落到「等待扫码…」', () => {
  assert.equal(qrCheckView('netease', { code: 999 }).status, QR_TEXT.waiting)
  assert.equal(qrCheckView('netease', {}).status, QR_TEXT.waiting)
  assert.equal(qrCheckView('netease', null).status, QR_TEXT.waiting)
})

test('QQ：waiting / scanned / expired / success 四种状态各自映射', () => {
  assert.equal(qrCheckView('qq', { state: 'waiting' }).status, QR_TEXT.waiting)
  const scanned = qrCheckView('qq', { state: 'scanned' })
  assert.equal(scanned.status, QR_TEXT.scanned)
  assert.equal(scanned.done, false)

  const expired = qrCheckView('qq', { state: 'expired' })
  assert.equal(expired.status, QR_TEXT.expired)
  assert.equal(expired.expired, true)
  assert.equal(expired.done, true)

  const done = qrCheckView('qq', { state: 'success', saved: true })
  assert.equal(done.status, QR_TEXT.success)
  assert.equal(done.ok, true)
  assert.equal(shouldCloseAfterLogin(done), true)
})

test('QQ：认不出的 state 原样显示，不当成功', () => {
  const v = qrCheckView('qq', { state: 'something-new' })
  assert.equal(v.status, 'something-new')
  assert.equal(v.done, false)
  assert.equal(v.ok, false)
})

test('未知平台：只照抄平台文案（不猜成功）', () => {
  const v = qrCheckView('kugou', { status: '等待扫码…' })
  assert.equal(v.status, QR_TEXT.waiting)
  assert.equal(v.done, false)
  assert.equal(qrCheckView('kugou', {}).status, QR_TEXT.waiting)
})

test('qrSessionView：初始态是「等待扫码…」，未成功未过期', () => {
  const s = qrSessionView({
    platformId: 'netease',
    platformName: '网易云音乐',
    appName: '网易云音乐',
    key: 'k-1',
  })
  assert.deepEqual(s, {
    platformId: 'netease',
    platformName: '网易云音乐',
    appName: '网易云音乐',
    key: 'k-1',
    image: '',
    status: QR_TEXT.waiting,
    ok: false,
    expired: false,
    done: false,
    saved: false,
  })
})

test('shouldCloseAfterLogin：非成功态一律不关（含 undefined）', () => {
  assert.equal(shouldCloseAfterLogin(undefined), false)
  assert.equal(shouldCloseAfterLogin(null), false)
  assert.equal(shouldCloseAfterLogin(qrCheckView('qq', { state: 'waiting' })), false)
  assert.equal(shouldCloseAfterLogin(qrCheckView('qq', { state: 'expired' })), false)
})

test('常量：轮询间隔与自动关闭延时是固定值（改动要让用例一起改）', () => {
  assert.equal(QR_POLL_MS, 2000)
  assert.equal(LOGIN_SUCCESS_CLOSE_MS, 900)
})

test('disconnectConfirmText：带上平台名，说清要重新扫码', () => {
  assert.equal(disconnectConfirmText('QQ 音乐'), '确定断开「QQ 音乐」账号？断开后需重新扫码。')
})
