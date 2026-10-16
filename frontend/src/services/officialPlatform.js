/**
 * 官方协议（后端内置 pkg/search + pkg/charts）当前在用的平台。
 *
 * 2026-10-18 用户口径：「发现页的平台选择 tx、wy 迁移到音源管理的官方协议中，
 * 用按钮交互切换」。这个开关本来长在发现页 hero 卡里 —— 两个小胶囊，显示的还是
 * 裸平台码（`wy` / `tx`），看不出是「网易云」和「QQ 音乐」。但它的本质是
 * **官方协议的能力配置**（后端 pkg/charts 只有这两个平台有完整榜单/歌单能力），
 * 不是「这一次浏览」的临时选项，所以放在音源管理里与 LX 协议并列，语义才对得上。
 *
 * 因此它由本模块持有唯一的 ref：音源管理里切换，发现音乐 / 榜单段跟着变，
 * 不会再出现「这边改了那边还是旧的」。落盘沿用同一份 pref key（`chart_platform`），
 * 老用户存过的选择不会丢。
 */
import { ref, computed, watch } from 'vue'
import { loadPref, savePref } from './prefs'

export const PLATFORM_PREF_KEY = 'chart_platform'

/**
 * 官方协议真正支持、且界面上暴露的两个平台。
 *
 * 后端 pkg/charts 其实四个平台都认（wy/tx/kg/kw），但酷狗/酷我的歌单能力较弱
 * （没有分类筛选、榜单结构也简单），用户要求界面上不再暴露 —— 少而准比多而杂好。
 * **搜索平台列表不受影响**（那个走 LX 脚本，仍是全部平台）。
 */
export const OFFICIAL_PLATFORM_IDS = ['wy', 'tx']

/** 展示信息：界面上永远显示中文名，不再直接露出 `wy` / `tx` 这种平台码。 */
export const OFFICIAL_PLATFORM_META = {
  wy: {
    id: 'wy',
    name: '网易云音乐',
    short: '网易云',
    mark: '网',
    icon: '🔴',
    desc: '榜单与歌单最全，官方协议默认用它',
  },
  tx: {
    id: 'tx',
    name: 'QQ 音乐',
    short: 'QQ 音乐',
    mark: 'Q',
    icon: '🟢',
    desc: '华语流行曲库更全，排行榜更新更勤',
  },
}

/** 列表形状，界面直接 v-for */
export const OFFICIAL_PLATFORM_OPTIONS = OFFICIAL_PLATFORM_IDS.map((id) => OFFICIAL_PLATFORM_META[id])

function normalize(pid) {
  const v = String(pid ?? '').trim().toLowerCase()
  return OFFICIAL_PLATFORM_IDS.includes(v) ? v : ''
}

/**
 * 当前选中的平台（模块级单例）。
 *
 * 默认网易云（榜单/歌单最全）。旧版本可能把它持久化成 `kw` / `kg` —— 那两个
 * 已经不在列表里了，normalize 会把它折算回默认值。
 */
const selected = ref(normalize(loadPref(PLATFORM_PREF_KEY, 'wy')) || 'wy')

// 任何来源的改动都落盘（包括调用方直接赋值），省得每个调用点都记得 savePref。
watch(selected, (v) => {
  if (normalize(v) === v) savePref(PLATFORM_PREF_KEY, v)
})

export { selected as officialPlatform }

/** 当前平台的展示信息，给界面直接用 */
export const officialPlatformMeta = computed(
  () => OFFICIAL_PLATFORM_META[selected.value] || OFFICIAL_PLATFORM_META.wy,
)

/**
 * 切换平台。
 * @returns {boolean} 是否真的变了 —— 调用方据此决定要不要重新拉榜单/歌单。
 */
export function setOfficialPlatform(pid) {
  const next = normalize(pid)
  if (!next || next === selected.value) return false
  selected.value = next
  return true
}

/** 持久化的值已失效（换成不认得的平台 / 老数据）时回落到默认，返回是否发生了回落。 */
export function ensureOfficialPlatform() {
  if (normalize(selected.value) === selected.value) return false
  selected.value = OFFICIAL_PLATFORM_IDS[0]
  return true
}
