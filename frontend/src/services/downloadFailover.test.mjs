import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  decideFailover,
  isLocalFatal,
  isSourceLevelFailure,
  untriedSources,
} from './downloadFailover.js'

// 守的判据（用户 2026-09-22 原话）：
// 「歌曲下载失败怎么没有自动换源下载呢」——
// CDN 403 / 不是音频 / 文件过小 这几类**换个音源就能救**，
// 但改造前它们被判成不可重试直接 failed。

const ALL = ['src-a', 'src-b']

test('isSourceLevelFailure：403 / 坏链 / 试听片段属于「换源能救」', () => {
  assert.equal(isSourceLevelFailure('HTTP_FATAL', 'HTTP 错误: 403'), true)
  assert.equal(isSourceLevelFailure('PREVIEW_CLIP', '文件过小，疑似试听片段'), true)
  assert.equal(isSourceLevelFailure('DOWNLOAD_FAILED', '对端返回的不是音频'), true)
  // 文案兜底：后端没归类到具体码时也能认出来
  assert.equal(isSourceLevelFailure('DOWNLOAD_FAILED', 'http 404 not found'), true)
})

test('isSourceLevelFailure：本地问题不算链路问题（换源也写不下去）', () => {
  assert.equal(isLocalFatal('EACCES', '权限不足'), true)
  assert.equal(isLocalFatal('EROFS', 'read-only file system'), true)
  assert.equal(isSourceLevelFailure('EACCES', '权限不足'), false)
  // 空间不足保持既有行为（原实现把 ENOSPC 当可重试），不归入「换源能救」
  assert.equal(isSourceLevelFailure('ENOSPC', '空间不足'), false)
})

test('untriedSources：当前源与已试过的源都不能算「没试过」', () => {
  assert.deepEqual(untriedSources(['src-a'], ALL), ['src-b'])
  assert.deepEqual(untriedSources(['src-a', 'src-b'], ALL), [])
  assert.deepEqual(untriedSources([], ALL), ALL)
})

test('decideFailover：链路问题 + 还有别的源 → 换源重试', () => {
  const got = decideFailover({
    code: 'HTTP_FATAL',
    message: 'HTTP 错误: 403',
    attempts: 1,
    tried: ['src-a'],
    all: ALL,
    maxAttempts: 3,
    autoFailover: true,
    transientRetry: false,
  })
  assert.deepEqual(got, { retry: true, switchSource: true })
})

test('decideFailover：链路问题但源已试遍 → 不再空转，如实失败', () => {
  const got = decideFailover({
    code: 'HTTP_FATAL',
    message: 'HTTP 错误: 403',
    attempts: 1,
    tried: ALL,
    all: ALL,
    maxAttempts: 3,
    autoFailover: true,
    transientRetry: false,
  })
  assert.deepEqual(got, { retry: false, switchSource: false })
})

test('decideFailover：本地问题一律不重试（换源无用）', () => {
  const got = decideFailover({
    code: 'EACCES',
    message: '权限不足',
    attempts: 1,
    tried: [],
    all: ALL,
    maxAttempts: 3,
    autoFailover: true,
    transientRetry: true,
  })
  assert.deepEqual(got, { retry: false, switchSource: false })
})

test('decideFailover：瞬时错误即使没有别的源也重试同一个源（兼容旧行为）', () => {
  const got = decideFailover({
    code: 'NETWORK',
    message: '网络连接异常',
    attempts: 1,
    tried: [],
    all: [],
    maxAttempts: 3,
    autoFailover: true,
    transientRetry: true,
  })
  assert.deepEqual(got, { retry: true, switchSource: false })
})

test('decideFailover：达到上限或关掉自动换源 → 不重试', () => {
  const base = { code: 'HTTP_FATAL', message: 'HTTP 错误: 403', tried: ['src-a'], all: ALL }
  assert.equal(decideFailover({ ...base, attempts: 3, maxAttempts: 3, autoFailover: true }).retry, false)
  assert.equal(decideFailover({ ...base, attempts: 1, maxAttempts: 3, autoFailover: false }).retry, false)
})

test('decideFailover：未归类错误仍沿用旧判定（不因新逻辑而多试）', () => {
  const got = decideFailover({
    code: 'SOMETHING_ODD',
    message: '未知的后端错误',
    attempts: 1,
    tried: ['src-a'],
    all: ALL,
    maxAttempts: 3,
    autoFailover: true,
    transientRetry: false,
  })
  assert.deepEqual(got, { retry: false, switchSource: false })
})
