/**
 * 飞牛「文件访问授权」状态。
 *
 * 背景（为什么必须有这一层）：飞牛的权限模型是「应用默认看不到用户的任何文件，
 * 必须由用户显式把目录授权给本应用」。曲率以 root 跑（要接管飞牛音乐的 socket，
 * 见 config/privilege 的注释），root 能读一切，但**这不代表用户授权过** ——
 * 系统侧的 ACL 与「用户意图」是两件事。以前曲率的做法是「扫整个 /vol1..16」，
 * 看起来能用，实际是把用户没打算给它的目录也扫了；用户 2026-10-31 反馈
 * 「文件权限还有获取路径感觉不对」，指的就是这个。
 *
 * 现在的做法：后端 /api/fnos/auth-status 汇总两路授权来源（管理员共享授权、
 * TRIM_DATA_ACCESSIBLE_PATHS），前端只读这个状态：
 *   - authorized=true  → 什么都不显示（别拿提示条烦人）
 *   - authorized=false → 顶部一条提示，带「授权目录」和「打开应用设置」两个动作
 *
 * ⚠️ 2026-10-31 用户明确「本应用只给管理员自己用」，个人授权（pickUserFile /
 * trim.file.userAccess / getUserAccessibleFolders）已整体移除，授权入口统一为
 * 管理员共享授权（pickSharedFile）。所以这里不再读 user_count / uid。
 *
 * 两个状态词要注意：`available=false` 表示**当前不在飞牛环境**（开发机就是），
 * 这时不该提示任何东西 —— 没装飞牛的用户不需要照着飞牛的话术操作。
 */

import { ref } from 'vue'
import { FnosAPI } from '../api/client'

/** 授权状态快照；null = 还没查过。 */
export const authStatus = ref(null)
/** 是否正在查（界面据此禁用按钮，防止连点）。 */
export const authLoading = ref(false)
/** 最近一次动作给用户看的一句话（成功/失败都放这儿，界面用一行小字显示）。 */
export const authMessage = ref('')

/**
 * 最近一次「选完目录」的结果（见 refreshAuthStatus）。
 *
 * 为什么单独一个 ref 而不是塞进 authStatus：authStatus 是「整机授权快照」，
 * 而这是「用户刚才选的那一下到底成没成立」，生命周期只有一次操作。
 */
export const lastPick = ref(null)

let inflight = null

/** 把后端的响应归一成固定的形状，字段缺省都给安全值。 */
function shape(data) {
  return {
    available: !!data?.available,
    apiAvailable: !!data?.api_available,
    authorized: !!data?.authorized,
    roots: Array.isArray(data?.roots) ? data.roots : [],
    fnosRoots: Array.isArray(data?.fnos_roots) ? data.fnos_roots : [],
    // grantedRoots = 用户/管理员真正授权过的目录（后端已剔除 data-share 自用目录）。
    // 判断「有没有给过音乐目录」只能看它，不能看 fnosRoots（含应用自己的数据目录）。
    grantedRoots: Array.isArray(data?.granted_roots) ? data.granted_roots : [],
    sharedCount: Number(data?.shared_count) || 0,
    canGrant: !!data?.can_grant,
    hint: typeof data?.hint === 'string' ? data.hint : '',
    hintAction: typeof data?.hint_action === 'string' ? data.hint_action : '',
  }
}

/**
 * 拉一次授权状态。并发调用共享同一个请求（页面多个组件同时挂载时不打三份）。
 * 失败不抛：授权状态查不到只是「不知道」，不该让整页报错。
 */
export async function loadAuthStatus({ force = false } = {}) {
  if (inflight && !force) return inflight
  authLoading.value = true
  inflight = (async () => {
    try {
      authStatus.value = shape(await FnosAPI.authStatus())
    } catch {
      // 老版本后端没有这个接口（404）或网络抖动：当作「不在飞牛环境」，静默。
      authStatus.value = shape(null)
    } finally {
      authLoading.value = false
      inflight = null
    }
    return authStatus.value
  })()
  return inflight
}

/**
 * 用户点完授权后调用：让后端丢掉 30 秒授权目录缓存并重查。
 *
 * 为什么要单独一个接口：授权目录在后端有缓存（避免每次浏览目录都走一遍
 * Unix socket）。用户刚授权完如果还读到旧缓存，界面就会显示「还是没授权」，
 * 那是最招人烦的一种假故障。
 */
export async function refreshAuthStatus(path = '') {
  authLoading.value = true
  // 后端对「刚选中的路径能不能读」的回答（picked_readable），只有传了 path 才有。
  // 它比 authorized 更贴近用户刚才那一下操作：authorized=true 但路径在授权范围外
  // 照样会 403，用户会以为「授权了却读不到」。
  let picked = null
  try {
    const res = await FnosAPI.authRefresh({ path })
    const d = res?.data || res || {}
    if (d && typeof d.picked_readable === 'boolean') {
      picked = {
        path: String(d.picked_path || path || ''),
        readable: d.picked_readable,
        realPath: String(d.picked_real_path || ''),
        hint: String(d.picked_hint || ''),
      }
    }
  } catch {
    // 后端刷新失败不影响下面的重查（重查本身也可能读到新值）。
  }
  const st = await loadAuthStatus({ force: true })
  lastPick.value = picked
  return st
}

/** 授权完成后的收尾：刷新状态 + 清空/设置提示语。 */
export async function afterGrant(message = '') {
  authMessage.value = message
  await refreshAuthStatus()
}

/**
 * 界面要不要显示「还没授权」的提示条。
 *
 * 三种情况不显示：还没查完（避免闪一下）、不在飞牛环境、已经授权。
 */
export function shouldPrompt(status = authStatus.value) {
  return !!status && status.available && !status.authorized
}
