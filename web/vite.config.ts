import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// 构建产物输出到 dist/app，由 Go 通过 embed 打包（见 web/web.go）。
// dist/.gitkeep 保证未构建前端时 Go 也能编译。
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist/app',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8780', changeOrigin: true },
    },
  },
})
