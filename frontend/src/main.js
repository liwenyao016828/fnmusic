import { createApp } from 'vue'
import App from './App.vue'
import './style.css'
import { initUiPrefs } from './services/prefs'
import { LogsAPI } from './api/client'
import { setLogReporter, logError } from './services/appLog'
import { describeError } from './services/userMsg'

const app = createApp(App)

// 前端日志上报通道：界面上的报错只留一句人话，完整信息走这里进后端运行日志，
// 用户在「日志」页就能查到（约定与实现见 services/appLog.js）。
setLogReporter((level, message) => LogsAPI.report(level, message))

// 兜底：组件里没被 catch 的异常，以前只进浏览器控制台（用户看不到），
// 现在也记进日志 —— 否则「页面某块没出来」这种情况在日志里毫无痕迹。
app.config.errorHandler = (err, instance, info) => {
  console.error('[Vue Global Error]:', err, info)
  logError('界面异常', String(info || ''), describeError(err))
}

// 后台预载窗口记忆（各页面点击状态）：从服务端文件拉取，成功后合并进内存/localStorage。
// 失败时静默降级到 localStorage，不阻塞应用启动。
initUiPrefs()

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
