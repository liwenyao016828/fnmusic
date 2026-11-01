<template>
  <!--
    授权引导条。

    为什么是这个形态（而不是一个弹窗、或者红字错误）：
      · 这不是「出错了」，是「还没开始」—— 飞牛的权限模型要求用户显式授权目录，
        新装用户**必然**会先经过这个状态。用红色报错会把正常流程说成故障。
      · 但它必须有存在感：不授权，曲率就扫不到任何歌，界面会空得像个 bug。
        所以放在内容区顶部、琥珀色、一直挂着直到用户处理（不做可关闭，
        关掉以后用户就再也找不到入口了）。
      · 三个动作的优先级按「最省事」排：一键授权目录 > 打开应用设置 > 刷新。
        最后一个是为了「我已经在别处授权过了，帮我把状态刷一下」这个真实场景。
  -->
  <div
    v-if="visible"
    class="mb-2 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-[11px] text-amber-800"
  >
    <span class="min-w-0 flex-1 leading-relaxed">{{ status.hint || defaultHint }}</span>

    <!-- 授权目录：官方 pickSharedFile（管理员共享授权，仅管理员可用）。
         ⚠️ 不要给这个按钮加 v-if 门控（2026-10-31 真机返工）：飞牛桌面里宿主能力探测
         恒为 unknown，加了门控按钮就不渲染，用户反馈「点了没反应」。 -->
    <button
      class="shrink-0 rounded-md bg-amber-600 px-2.5 py-1 text-[11px] font-medium text-white hover:bg-amber-700 disabled:opacity-60"
      :disabled="busy"
      @click="grantDir"
    >{{ busy ? '请稍候…' : '授权目录' }}</button>

    <!-- 打开本应用设置页：官方 JS SDK openAppSetting()（飞牛 1.2.0401 / App 1.34.0+）。
         这是官方文档给「没接入授权接口」的应用准备的兜底入口，也是手动管理
         「授权目录」唯一正确的一页。 -->
    <button
      class="shrink-0 rounded-md border border-amber-300 bg-white/70 px-2.5 py-1 text-[11px] font-medium text-amber-800 hover:bg-white disabled:opacity-60"
      :disabled="busy"
      @click="openSetting"
    >打开应用设置</button>

    <button
      class="shrink-0 underline decoration-dotted underline-offset-2 hover:no-underline"
      :disabled="busy"
      @click="reload"
    >我已授权，刷新</button>

    <!-- 动作结果一句话反馈（失败时把宿主给的原话显示出来，别自己编） -->
    <span v-if="msg" class="basis-full text-[11px] text-amber-700/90">{{ msg }}</span>
  </div>
</template>

<script setup>
/**
 * 「曲率还没被授权访问音乐目录」的提示条。
 *
 * 挂载点由调用方决定（当前挂在 App.vue 的内容列顶部），组件自己负责：
 *   1. 首次挂载时查一次授权状态；
 *   2. 用户在飞牛里授权后（一键授权 / 打开设置页回来）刷新状态并通知父级重扫目录；
 *   3. 非飞牛环境 / 已授权 → 整条不渲染。
 *
 * 为什么 `emit('granted')` 而不是自己去重扫目录：目录树的刷新逻辑属于父级
 * （App.vue 持有 nas 视图状态），这里只报事实。
 *
 * 2026-10-31：本应用只给管理员自己用，授权入口收敛为**唯一的「管理员共享授权」**
 * （pickSharedFile）。个人授权（pickUserFile）已移除：它绑在登录账号上，换账号或
 * uid 变化就要重新授权，单管理员场景只会多一个会失效的来源。
 */
import { computed, onMounted, ref, watch } from 'vue'
import {
  authMessage,
  authStatus,
  lastPick,
  loadAuthStatus,
  refreshAuthStatus,
  shouldPrompt,
} from '../services/fnosAuth'
import { detectTrimHost, openAppSetting, pickSharedDirectory } from '../services/trimHost'

const emit = defineEmits(['granted'])

const busy = ref(false)
const msg = ref('')

const status = computed(() => authStatus.value || { hint: '', sharedCount: 0 })
const visible = computed(() => shouldPrompt())
const defaultHint = '曲率还没有被授权访问任何音乐目录。点「授权目录」选择你的音乐文件夹，或到「应用中心 → 曲率 → 设置 → 授权目录」里添加。'

// 服务层产生的提示语（比如 afterGrant 写入的「已授权」）统一显示在这里
watch(authMessage, (v) => {
  if (v) msg.value = v
})

onMounted(() => {
  loadAuthStatus()
  // 2026-10-31：这个组件以前从不探测宿主，而两个按钮又受 hasTrimPicker() 门控 ——
  // 结果就是提示条里的按钮永远不渲染，用户点了「没反应」。现在按钮常驻，
  // 探测只是为了让点击时能立刻复用已有结果，不再影响显隐。
  detectTrimHost()
})

/**
 * 管理员共享授权：官方 `pickSharedFile`（仅管理员可用）。
 *
 * 共享授权的目录**全应用可用**、不按用户区分，所以换账号/uid 变化都不受影响 ——
 * 这正是本应用（管理员自用）想要的形态。非管理员点这个按钮会收到宿主的
 * 「仅管理员可进行此操作」，如实转述即可。
 */
async function grantDir() {
  busy.value = true
  msg.value = ''
  try {
    const res = await pickSharedDirectory({ title: '选择要授权给曲率的目录', okText: '授权这个目录' })
    if (res.cancelled) {
      msg.value = ''
      return
    }
    if (!res.ok) {
      msg.value = res.error || '授权没有完成'
      return
    }
    // 宿主选目录时一般已顺手授权；这里再走一次 auth-refresh（带上刚选的路径），
    // 让后端立刻丢掉 30 秒缓存，并顺口回答「这个路径现在读得到吗」。
    //
    // 2026-10-31 用户口径：「飞牛选择器选完目录后直接可用，不再需要管理员授权那一步」。
    // 所以成功的判据从「authorized（整机有没有授权过任何目录）」收紧成
    // 「picked_readable（刚选的这个目录能不能读）」—— 机器上早先授权过别的目录时
    // authorized 恒为 true，用它判断会把「这次没生效」也报成成功。
    const st = await refreshAuthStatus(res.path)
    const pick = lastPick.value
    const usable = pick ? pick.readable : !!st?.authorized
    if (usable) {
      msg.value = `已授权并可用：${res.path}`
      emit('granted', res.path)
    } else {
      // 选中 ≠ 已生效：飞牛宿主侧的授权可能是异步落库的，也可能只「选」不「授」。
      // 文案必须能照着做（等几秒 / 重试 / 去设置页），不能只说「失败」。
      msg.value = pick?.hint || `已选择 ${res.path}，但系统还没确认授权。稍等几秒点「我已授权，刷新」，或到「应用设置 → 授权目录」确认。`
      // 还是把路径报上去：父级至少可以先把目录填好，用户点一下重试就通了。
      emit('granted', res.path)
    }
  } finally {
    busy.value = false
  }
}

/** 打开飞牛官方的「本应用设置页」—— 授权目录的正式入口就在那一页。 */
async function openSetting() {
  busy.value = true
  msg.value = ''
  try {
    const res = await openAppSetting()
    if (!res.ok) {
      msg.value = `${res.error}。也可以手动到「应用中心 → 曲率 → 设置 → 授权目录」。`
      return
    }
    // 设置页在飞牛自己的窗口里，我们拿不到「用户在里面点了什么」的回调；
    // 给一句提示，让用户回来点「我已授权，刷新」。
    msg.value = '设置页已打开。在「授权目录」里添加曲库目录后，回来点「我已授权，刷新」。'
  } finally {
    busy.value = false
  }
}

/** 重查（带缓存失效），顺带让父级刷新目录树。 */
async function reload() {
  busy.value = true
  msg.value = ''
  try {
    const st = await refreshAuthStatus()
    if (st?.authorized) {
      msg.value = '授权已生效。'
      emit('granted', '')
    } else {
      msg.value = '还没检测到授权。确认目录已在「应用设置 → 授权目录」里保存，并且重启过应用。'
    }
  } finally {
    busy.value = false
  }
}
</script>
