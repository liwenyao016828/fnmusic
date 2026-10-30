import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  base: '/',
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
})
