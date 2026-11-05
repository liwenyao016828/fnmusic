import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  LEVEL_ALL,
  SOURCE_ALL,
  LEVEL_OPTIONS,
  normalizeQuery,
  matchesQuery,
  levelMatches,
  sourceMatches,
  serverEntryMatches,
  collectSources,
  sourceText,
} from './logFilter.js'

describe('normalizeQuery', () => {
  it('去首尾空白并转小写', () => {
    assert.equal(normalizeQuery('  Hello World  '), 'hello world')
    assert.equal(normalizeQuery('中文'), '中文')
  })

  it('null/undefined 返回空串', () => {
    assert.equal(normalizeQuery(null), '')
    assert.equal(normalizeQuery(undefined), '')
  })
})

describe('matchesQuery', () => {
  it('空搜索词恒为真', () => {
    assert.ok(matchesQuery('', 'anything'))
    assert.ok(matchesQuery('   ', 'anything'))
  })

  it('大小写不敏感', () => {
    assert.ok(matchesQuery('ERROR', 'An error occurred'))
    assert.ok(matchesQuery('failed', 'Task FAILED'))
  })

  it('跨多个参数匹配', () => {
    assert.ok(matchesQuery('backend', null, 'backend', 'message text'))
    assert.ok(matchesQuery('text', 'backend', null, 'message text'))
  })

  it('不匹配返回 false', () => {
    assert.ok(!matchesQuery('nope', 'backend', 'info', 'message'))
  })
})

describe('levelMatches', () => {
  it('all 放行全部', () => {
    assert.ok(levelMatches('error', LEVEL_ALL))
    assert.ok(levelMatches('info', LEVEL_ALL))
    assert.ok(levelMatches(undefined, LEVEL_ALL))
  })

  it('精确匹配级别', () => {
    assert.ok(levelMatches('error', 'error'))
    assert.ok(levelMatches('warn', 'warn'))
    assert.ok(!levelMatches('error', 'warn'))
  })

  it('未知 level 按 info 计', () => {
    assert.ok(levelMatches(undefined, 'info'))
    assert.ok(levelMatches('', 'info'))
  })
})

describe('sourceMatches', () => {
  it('all 放行全部', () => {
    assert.ok(sourceMatches('backend', SOURCE_ALL))
    assert.ok(sourceMatches('frontend', SOURCE_ALL))
  })

  it('精确匹配来源', () => {
    assert.ok(sourceMatches('backend', 'backend'))
    assert.ok(sourceMatches('frontend', 'frontend'))
    assert.ok(!sourceMatches('backend', 'frontend'))
  })

  it('空来源按 backend 计', () => {
    assert.ok(sourceMatches(undefined, 'backend'))
    assert.ok(sourceMatches('', 'backend'))
  })
})

describe('serverEntryMatches', () => {
  it('AND 语义：全部条件同时满足', () => {
    const e = { level: 'error', source: 'backend', message: 'failed to connect' }
    assert.ok(serverEntryMatches(e, { level: 'error', source: 'backend', query: 'connect' }))
    assert.ok(!serverEntryMatches(e, { level: 'warn', source: 'backend', query: 'connect' }))
    assert.ok(!serverEntryMatches(e, { level: 'error', source: 'frontend', query: 'connect' }))
    assert.ok(!serverEntryMatches(e, { level: 'error', source: 'backend', query: 'nope' }))
  })

  it('onlyFailed 与级别下拉取交集', () => {
    const e = { level: 'warn', message: 'slow' }
    // onlyFailed 要求 level=error，但级别下拉选了 warn → 交集为空
    assert.ok(!serverEntryMatches(e, { level: 'warn', onlyFailed: true }))
    // error 条目 + onlyFailed + level=all → 通过
    const err = { level: 'error', message: 'crash' }
    assert.ok(serverEntryMatches(err, { level: LEVEL_ALL, onlyFailed: true }))
  })

  it('空条目返回 false', () => {
    assert.ok(!serverEntryMatches(null))
    assert.ok(!serverEntryMatches(undefined))
  })
})

describe('collectSources', () => {
  it('返回全部来源在前，backend 优先', () => {
    const entries = [
      { source: 'frontend' },
      { source: 'backend' },
      { source: 'api' },
      { source: 'backend' },
    ]
    const opts = collectSources(entries)
    assert.equal(opts[0].value, SOURCE_ALL)
    assert.equal(opts[0].label, '全部来源')
    assert.equal(opts[1].value, 'backend')
    // api 和 frontend 按字母序
    const rest = opts.slice(2).map((o) => o.value)
    assert.ok(rest.includes('api'))
    assert.ok(rest.includes('frontend'))
  })

  it('空输入返回只有全部来源', () => {
    const opts = collectSources([])
    assert.equal(opts.length, 1)
    assert.equal(opts[0].value, SOURCE_ALL)
  })
})

describe('sourceText', () => {
  it('backend/frontend 翻译', () => {
    assert.equal(sourceText('backend'), '后端')
    assert.equal(sourceText('frontend'), '前端')
  })

  it('其余来源原样不臆测翻译', () => {
    assert.equal(sourceText('api'), 'api')
    assert.equal(sourceText('downloader'), 'downloader')
  })

  it('空来源按 backend 计', () => {
    assert.equal(sourceText(undefined), '后端')
    assert.equal(sourceText(''), '后端')
  })
})

describe('LEVEL_OPTIONS', () => {
  it('包含全部级别与三个具体级别', () => {
    assert.equal(LEVEL_OPTIONS.length, 4)
    assert.equal(LEVEL_OPTIONS[0].value, LEVEL_ALL)
    const values = LEVEL_OPTIONS.map((o) => o.value)
    assert.ok(values.includes('error'))
    assert.ok(values.includes('warn'))
    assert.ok(values.includes('info'))
  })
})
