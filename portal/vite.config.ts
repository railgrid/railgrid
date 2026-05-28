import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'path'

export default defineConfig(() => ({
  // Always serve from /ui/ so URLs match whether the portal is:
  //  - embedded in the hub (production),
  //  - proxied by the hub from the Vite dev server (--portal-dev-url),
  //  - or accessed directly on the Vite port (http://localhost:3000/ui/).
  base: '/ui/',
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
      // Provider-extension surface: list API, backend HTTP proxy, and UI
      // proxy. All must route to the hub in dev so the iframe + handshake
      // observe the same same-origin guarantee they have in production.
      '/api/providers': {
        target: 'https://localhost:9443',
        changeOrigin: true,
        secure: false,
      },
      '/services/providers': {
        target: 'https://localhost:9443',
        changeOrigin: true,
        secure: false,
        ws: true,
      },
      '/ui/providers': {
        target: 'https://localhost:9443',
        changeOrigin: true,
        secure: false,
        // Mirror of pkg/hub/providers/proxy.go:isAssetPath — only forward
        // static-asset requests (main.js, icon.svg, …) to the hub so the
        // provider's HTTP server serves them. Bare /ui/providers/{name}
        // and nested SPA routes like /ui/providers/{name}/some-page must
        // be served locally by Vite's HTML5 history fallback so the Vue
        // SPA can render ProviderFrame. Without this bypass, the hub's
        // own SPA-fallback handler (which IS this Vite dev proxy) loops
        // back through Vite and overflows the Node HTTP header buffer.
        bypass(req) {
          const url = req.url || ''
          const pathOnly = url.split('?', 1)[0]
          const slash = pathOnly.lastIndexOf('/')
          const last = slash >= 0 ? pathOnly.slice(slash + 1) : pathOnly
          if (!last.includes('.')) {
            // Returning a path tells vite to serve that path locally
            // (its SPA middleware then falls back to index.html).
            return url
          }
        },
      },
    },
  },
  build: {
    outDir: 'dist',
  },
}))
