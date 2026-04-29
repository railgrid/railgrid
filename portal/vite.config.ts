import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'path'

export default defineConfig(() => ({
  // Always serve from /console/ so URLs match whether the portal is:
  //  - embedded in the hub (production),
  //  - proxied by the hub from the Vite dev server (--portal-dev-url),
  //  - or accessed directly on the Vite port (http://localhost:3000/console/).
  base: '/console/',
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
    },
  },
  server: {
    port: 3000,
    proxy: {
      '/apis': {
        target: 'https://localhost:9443',
        changeOrigin: true,
        secure: false,
        ws: true,
        headers: {
          Origin: 'https://localhost:9443',
        },
      },
      '/healthz': {
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
