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
        // 默认代理到本地 8899；如本机已装了生产版极光音乐，可用
        // VITE_API_TARGET=http://127.0.0.1:8900 指向开发后端，避免端口冲突
        target: process.env.VITE_API_TARGET || 'http://localhost:8899',
        changeOrigin: true
      }
    }
  },
  build: {
    outDir: '../backend/dist',
    emptyOutDir: true
  }
})
