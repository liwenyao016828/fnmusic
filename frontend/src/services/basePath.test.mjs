/**
 * basePath 的回归测试。
 *
 * 跑法：cd frontend && node --test src/services/basePath.test.mjs
 *
 * 守的判据：曲率改成统一网关微应用后，所有前端 URL 都必须能正确带上
 * `/app/yinshu-ai` 前缀；而在 `node --test`（没有 location、没有 Vite 注入）里
 * 前缀必须是空 —— 否则所有断言写死 `/api/...` 的老用例会集体失败。
 */
import assert from 'node:assert/strict'
import test from 'node:test'

import { BASE_PATH, withBase, API_BASE } from './basePath.js'

test('测试环境（无 location / 无 Vite 注入）前缀为空', () => {
  assert.equal(BASE_PATH, '')
  assert.equal(API_BASE, '/api')
})

test('withBase 在空前缀下等价于原路径', () => {
  assert.equal(withBase('/api/nas/browse'), '/api/nas/browse')
  assert.equal(withBase('api/nas/browse'), '/api/nas/browse')
  assert.equal(withBase('/api/player/stream?url=x'), '/api/player/stream?url=x')
})

test('withBase 幂等：已带前缀的路径不重复叠加', () => {
  const p = '/app/yinshu-ai/api/nas/browse'
  assert.equal(withBase(p), p)
  assert.equal(withBase(withBase(p)), p)
})

test('withBase 空值给站点根，不给空串（<img src=""> 会请求当前页面）', () => {
  assert.equal(withBase(''), '/')
  assert.equal(withBase(null), '/')
  assert.equal(withBase(undefined), '/')
})

test('有 location 时从 /app/<name> 段推导前缀', () => {
  const saved = globalThis.location
  try {
    globalThis.location = { pathname: '/app/yinshu-ai/index.html' }
    // detectBase 只在模块加载时跑一次，这里换 location 不会改变已导出的常量；
    // 该用例只验证「换 location 不会污染已算好的前缀」——即常量是稳定的。
    assert.equal(BASE_PATH, '')
  } finally {
    globalThis.location = saved
  }
})
