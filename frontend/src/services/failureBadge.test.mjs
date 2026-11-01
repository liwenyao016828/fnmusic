import test from 'node:test'
import assert from 'node:assert/strict'
import { explainFailure } from './failureBadge.js'

test('explainFailure：后端原因链拆成一句话结论 + 分渠道明细', () => {
  const r = explainFailure(
    '没有拿到任何可用直链：原生：该平台没有这一档音质；洛雪：服务端洛雪宿主未开启（默认关闭）',
  )
  assert.equal(r.label, '平台无此档')
  assert.deepEqual(r.detail, ['原生：该平台没有这一档音质', '洛雪：服务端洛雪宿主未开启（默认关闭）'])
})

test('explainFailure：只有一条原因时 detail 也只有一行', () => {
  const r = explainFailure('所有取音尝试都失败：原生：曲目在该平台不可播放')
  assert.equal(r.label, '平台不可播')
  assert.deepEqual(r.detail, ['原生：曲目在该平台不可播放'])
})

test('explainFailure：空原因给「下载失败」而不是空白', () => {
  assert.equal(explainFailure('').label, '下载失败')
  assert.deepEqual(explainFailure('').detail, [])
  assert.equal(explainFailure(null).label, '下载失败')
  assert.equal(explainFailure(undefined).label, '下载失败')
})

test('explainFailure：认得出写盘失败、超时、解析器未就绪', () => {
  assert.equal(explainFailure('写入文件失败: no space left on device').label, '写盘失败')
  assert.equal(explainFailure('context deadline exceeded：超时').label, '超时')
  assert.equal(explainFailure('原生：解析器未初始化').label, '解析器未就绪')
  assert.equal(explainFailure('外挂池：musicdl 未启用').label, '外挂池未启用')
})

test('explainFailure：认不出的原因截前 12 字，不返回空白', () => {
  const r = explainFailure('原生：上游返回了意料之外的东西，具体原因不明')
  assert.ok(r.label.endsWith('…'))
  assert.ok(r.label.length > 2)
  assert.deepEqual(r.detail, ['原生：上游返回了意料之外的东西，具体原因不明'])
})
