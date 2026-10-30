import { createApp } from 'vue'
import App from './App.vue'
import './style.css'
import { initUiPrefs } from './services/prefs'
import { applyTheme, loadTheme } from './services/theme'
import { LogsAPI } from './api/client'
import { setLogReporter, logError } from './services/appLog'
import { describeError } from './services/userMsg'
import { setScopeFallback } from './services/feedback'
import { currentView } from './services/navBus'
import { VIEWS } from './services/navViews'

const app = createApp(App)

// 前端日志上报通道：界面上的报错只留一句人话，完整信息走这里进后端运行日志，
// 用户在「日志」页就能查到（约定与实现见 services/appLog.js）。
setLogReporter((level, message) => LogsAPI.report(level, message))

// 提示没给 scope 时，用「当前页面名」当日志 scope。
// 为什么放这里而不是 feedback.js 自己引 navBus：navBus 反向 import 了 feedback，
// 在 feedback 里引 navBus 会绕成循环依赖；main.js 是两者的共同上层，接在这里最干净。
setScopeFallback(() => {
  const v = VIEWS.find((x) => x.id === currentView.value)
  return v?.name || '界面提示'
})

// 兜底：组件里没被 catch 的异常，以前只进浏览器控制台（用户看不到），
// 现在也记进日志 —— 否则「页面某块没出来」这种情况在日志里毫无痕迹。
app.config.errorHandler = (err, instance, info) => {
  console.error('[Vue Global Error]:', err, info)
  logError('界面异常', String(info || ''), describeError(err))
}

// 后台预载窗口记忆（各页面点击状态）：从服务端文件拉取，成功后合并进内存/localStorage。
// 失败时静默降级到 localStorage，不阻塞应用启动。
initUiPrefs()

// 主题色（2026-10-29）：四套强调色（蓝/青玉绿/樱粉/石墨灰）。
// 先用本地缓存立刻上色（避免首屏闪一下默认蓝），再等 initUiPrefs 拉完服务端
// 偏好后用服务端的值覆盖 —— 服务端优先，多端共享。
applyTheme(loadTheme())
initUiPrefs().then(() => applyTheme(loadTheme()))

// 开发模式下把音源运行时挂到 window，便于在控制台排查取链问题，例如：
//   await lxRuntime.getMusicUrl('api_xxx', 'wy', { songmid: '421423808' }, '320k')
//   lxRuntime.getReadySourceIds()
// 仅 DEV 生效，生产构建里不会出现。
if (import.meta.env.DEV) {
  import('./engine/lx-runtime').then((m) => {
    window.lxRuntime = m.lxRuntime
  })
}

app.mount('#app')
