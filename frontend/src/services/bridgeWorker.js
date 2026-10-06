/**
 * AI 解析桥的「网页端工作器」。
 *
 * 第三方音源脚本只能在浏览器里执行，因此当外部 AI 通过
 * POST /api/bridge/resolve 提交解析任务时，需要有一个正在运行的网页端
 * 来领取任务、调用音源脚本取直链，再把结果回传给后端。
 *
 * 本模块随应用启动自动运行，多标签页安全（后端 claim 是原子操作）。
 */
import { ref } from 'vue'
import { BridgeAPI } from '../api/client'
import { lxRuntime } from '../engine/lx-runtime'
import { downloadManager } from './downloadManager'

const POLL_INTERVAL_MS = 2500

const isRunning = ref(false)
const processedCount = ref(0)
const failedCount = ref(0)
const lastError = ref('')
const lastJobAt = ref(0)

let timer = null
let busy = false

function buildMusicInfo(req) {
  return {
    id: req.id || req.songmid || req.hash || '',
    songmid: req.songmid || req.id || '',
    hash: req.hash || '',
    name: req.name || '',
    singer: req.singer || '',
    album: req.album || '',
    source: req.platform,
    duration: req.duration || 0,
    interval: req.duration || 0,
  }
}

async function handleJob(job) {
  // 原子领取，避免多个标签页重复解析
  const claimRes = await BridgeAPI.claim(job.id)
  if (claimRes?.code !== 200) return

  const req = job.request || {}
  try {
    const musicInfo = buildMusicInfo(req)
    const res = await lxRuntime.getMusicUrl('', req.platform, musicInfo, req.quality || 'highest')
    if (!res?.url) throw new Error('音源未返回有效直链')

    const referer = res.headers?.Referer || res.headers?.referer || ''

    // 需要下载则直接加入下载队列（复用已解析的直链，避免二次解析）
    if (req.download) {
      downloadManager.addSong({
        ...musicInfo,
        url: res.url,
        referer,
        quality: res.quality || '',
      }, res.sourceId, false)
    }

    await BridgeAPI.result({
      job_id: job.id,
      url: res.url,
      quality: res.quality || '',
      source_id: res.sourceId || '',
      source_name: res.sourceName || '',
      referer,
    })
    processedCount.value++
    lastJobAt.value = Date.now()
  } catch (e) {
    failedCount.value++
    lastJobAt.value = Date.now()
    await BridgeAPI.result({
      job_id: job.id,
      error: e?.message || String(e),
    }).catch(() => {})
  }
}

async function tick() {
  if (busy || !isRunning.value) return
  busy = true
  try {
    const res = await BridgeAPI.pending()
    const items = res?.data?.items || res?.items || []
    for (const job of items) {
      if (!isRunning.value) break
      await handleJob(job)
    }
    lastError.value = ''
  } catch (e) {
    // 网络抖动（例如后端重启）不打扰用户，仅记录
    lastError.value = e?.message || String(e)
  } finally {
    busy = false
  }
}

export const bridgeWorker = {
  isRunning,
  processedCount,
  failedCount,
  lastError,
  lastJobAt,

  start() {
    if (timer) {
      isRunning.value = true
      return
    }
    isRunning.value = true
    timer = setInterval(tick, POLL_INTERVAL_MS)
    tick()
  },

  stop() {
    isRunning.value = false
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  },

  toggle() {
    if (isRunning.value) this.stop()
    else this.start()
  },
}
