import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'path'

export default defineConfig(({ command }) => ({
  // In production (embedded in hub), serve from /portal/. In dev, serve from root.
  base: command === 'build' ? '/portal/' : '/',
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
    },
  },
  server: {
    port: 3000,
    proxy: {
      '/graphql': {
        target: 'https://localhost:9443',
        changeOrigin: true,
        secure: false,
      },
      '/auth': {
        target: 'https://localhost:9443',
        changeOrigin: true,
        secure: false,
      },
      '/healthz': {
        target: 'https://localhost:9443',
        changeOrigin: true,
        secure: false,
      },
      '/services': {
        target: 'https://localhost:9443',
        changeOrigin: true,
        secure: false,
        ws: true,
        headers: {
          Origin: 'https://localhost:9443',
        },
      },
      '/clusters': {
        target: 'https://localhost:9443',
        changeOrigin: true,
        secure: false,
      },
    },
  },
  build: {
    outDir: 'dist',
  },
}))
