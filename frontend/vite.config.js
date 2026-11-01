import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 网关前缀：曲率以飞牛「统一网关微应用」形态发布，桌面入口挂在 /app/yinshu-ai 下
// （见 fpk-package/app/ui/config 的 gatewayPrefix）。**打包时必须**把 base 设成这个
// 前缀，否则 index.html 会引用站点根的 /assets/xxx.js —— 而站点根是 fnOS 门户自己的
// （真机实测 http://nas:4398/assets/任意文件 恒 404），页面会白屏。
//
// `npm run dev` 保持 '/'：开发服务器是纯浏览器直连 3000，没有网关那一层，
// 加了前缀反而要自己拼。用 VITE_BASE 可以覆盖（想在本地模拟网关形态时用）。
const GATEWAY_BASE = '/app/yinshu-ai/'

export default defineConfig(({ command }) => ({
  base: command === 'build' ? (process.env.VITE_BASE || GATEWAY_BASE) : '/',
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  server: {
    // 允许局域网访问开发服务器（便于在别的设备上预览）
    host: true,
    port: 3000,
    proxy: {
      '/api': {
        /**
         * 代理目标。
         *
         * ⚠️ 2026-10-29 用户报「服务端出错了」的根因：默认值原本是 8899（**正式版**端口），
         * 而 `scripts/dev.sh` 起的开发后端在 8900。于是所有接口都打到了正式版实例上 ——
         * 它的版本比本地代码旧，新接口直接 500（/api/monitors、/api/config 全挂），
         * 界面上就是一片「服务端出错了」，日志页还什么都没有。
         *
         * 现在默认值取**开发后端**（8900，dev.sh 的默认端口，与本机正式版 8899 互不影响）；
         * 需要打正式版或远程时用 VITE_API_TARGET 覆盖。
         */
        target: process.env.VITE_API_TARGET || 'http://127.0.0.1:8900',
        changeOrigin: true
      }
    }
  },
  build: {
    outDir: '../backend/dist',
    emptyOutDir: true
  }
}))
