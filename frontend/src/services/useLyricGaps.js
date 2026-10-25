import { ref, computed } from 'vue'
import { TidyAPI } from '../api/client'
import { apiError, fail as failText } from '../services/userMsg'
import { saveCache, loadCache } from '../services/prefs'

/**
 * 缺歌词的曲目（用户 2026-10-25 需求 N）。
 *
 * 和歌名修复是同一套形状：扫清单 → 勾选 → 批量「先找词预览」 → 确认后「写入文件」。
 * 单独写成 composable 而不是往 TidyView 的 <script setup> 里再塞 300 行 ——
 * 那个文件已经 2600 行了，加进去以后没人找得到东西。
 *
 * 状态全部在这里，TidyView 只管模板与按钮。
 */
export function useLyricGaps(deps) {
  // deps: { dir() => string, flash(msg, isErr) => void, afterWrite() => void }
  const dir = deps.dir
  const flash = deps.flash

  const items = ref([])        // [{path,name,title,artist,selected,lyric,ok,reason,note,written,err}]
  const total = ref(0)
  const scanning = ref(false)
  const fetching = ref(false)
  const writing = ref(false)
  const at = ref(0)
  const atText = computed(() => at.value
    ? `上次扫描 ${new Date(at.value * 1000).toLocaleString('zh-CN', { hour12: false })}`
    : '')
  const selectedCount = computed(() => items.value.filter(it => it.selected && !it.written).length)
  const foundCount = computed(() => items.value.filter(it => it.ok && !it.written).length)
  const allSelected = computed(() => items.value.length > 0 && items.value.every(it => it.selected || it.written))

  function cacheKey() {
    const d = (dir() || '').trim()
    return d ? `last_lyric_gaps:${d}` : ''
  }

  function save() {
    const k = cacheKey()
    if (!k) return
    saveCache(k, { at: at.value, total: total.value, items: items.value })
  }

  function restore() {
    const k = cacheKey()
    if (!k) return
    const c = loadCache(k)
    if (!c) return
    at.value = Number(c.at) || 0
    total.value = Number(c.total) || 0
    items.value = Array.isArray(c.items) ? c.items : []
  }

  function clear() {
    // prefs 没有「删键」接口，清空就是把空结果写回缓存：
    // 下次 restore 拿到空列表，界面回到空态，不会残留上一次的清单。
    items.value = []
    total.value = 0
    at.value = 0
    save()
  }

  async function scan() {
    const d = (dir() || '').trim()
    if (!d) return flash('先在「选项」里填曲库目录', true)
    scanning.value = true
    try {
      const res = await TidyAPI.lyricGaps({ dir: d, limit: 1000 })
      const bad = apiError(res, '扫描缺歌词失败')
      if (bad) return flash(bad, true)
      const list = (res.data?.items || []).map(it => ({ ...it, selected: false, written: false }))
      items.value = list
      total.value = Number(res.data?.total) || list.length
      at.value = Math.floor(Date.now() / 1000)
      save()
      if (!list.length) flash('这个目录里的歌都有歌词了')
    } catch (e) {
      flash(failText('扫描缺歌词', e), true)
    } finally {
      scanning.value = false
    }
  }

  function toggle(it) { it.selected = !it.selected; save() }

  function toggleAll() {
    const next = !allSelected.value
    for (const it of items.value) it.selected = next
    save()
  }

  // 只找词不写盘：先让用户看清「哪些找得到、哪些找不到、哪些超时了」
  async function fetchPreview() {
    const paths = items.value.filter(it => it.selected && !it.written).map(it => it.path)
    if (!paths.length) return flash('先勾几首', true)
    fetching.value = true
    try {
      const res = await TidyAPI.lyricFetch({ dir: (dir() || '').trim(), paths })
      const bad = apiError(res, '找歌词失败')
      if (bad) return flash(bad, true)
      applyHits(res.data?.items || [])
      const ok = items.value.filter(it => it.ok && !it.written).length
      flash(`找到 ${ok} 首的歌词，确认后点「写入文件」`)
    } catch (e) {
      flash(failText('找歌词', e), true)
    } finally {
      fetching.value = false
    }
  }

  async function write() {
    const targets = items.value.filter(it => it.selected && it.ok && !it.written).map(it => it.path)
    if (!targets.length) return flash('先「查找歌词」预览，再勾上要写的', true)
    writing.value = true
    try {
      const res = await TidyAPI.lyricWrite({ dir: (dir() || '').trim(), paths: targets })
      const bad = apiError(res, '写入歌词失败')
      if (bad) return flash(bad, true)
      applyHits(res.data?.items || [], true)
      const n = items.value.filter(it => it.written).length
      flash(`已写入 ${n} 首的歌词`, false)
      deps.afterWrite?.()
    } catch (e) {
      flash(failText('写入歌词', e), true)
    } finally {
      writing.value = false
    }
  }

  function applyHits(hits, written = false) {
    const byPath = new Map(hits.map(h => [h.path, h]))
    items.value = items.value.map(it => {
      const h = byPath.get(it.path)
      if (!h) return it
      return {
        ...it,
        title: h.title || it.title,
        artist: h.artist || it.artist,
        lyric: h.lyric || it.lyric || '',
        ok: !!h.ok,
        reason: h.reason || '',
        note: h.note || '',
        written: written ? !!h.written : it.written,
        err: h.err || '',
      }
    })
    save()
  }

  return {
    items, total, scanning, fetching, writing, atText, selectedCount, foundCount, allSelected,
    scan, restore, save, clear, toggle, toggleAll, fetchPreview, write,
  }
}
