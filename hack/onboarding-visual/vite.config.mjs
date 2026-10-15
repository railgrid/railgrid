import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
const repo = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const modules = resolve(repo, 'portal/node_modules')
const [{ defineConfig }, { default: vue }, { default: tailwind }] = await Promise.all([
  import(pathToFileURL(resolve(modules, 'vite/dist/node/index.js')).href),
  import(pathToFileURL(resolve(modules, '@vitejs/plugin-vue/dist/index.mjs')).href),
  import(pathToFileURL(resolve(modules, '@tailwindcss/vite/dist/index.mjs')).href),
])
export default defineConfig({
  root: fileURLToPath(new URL('.', import.meta.url)),
  plugins: [vue(), tailwind()],
  resolve: {
    alias: {
      '@': resolve(repo, 'portal/src'),
      vue: resolve(modules, 'vue/dist/vue.esm-bundler.js'),
      pinia: resolve(modules, 'pinia/dist/pinia.mjs'),
      'vue-router': resolve(modules, 'vue-router/dist/vue-router.mjs'),
      'lucide-vue-next': resolve(modules, 'lucide-vue-next/dist/esm/lucide-vue-next.js'),
      '@fontsource-variable/instrument-sans': resolve(modules, '@fontsource-variable/instrument-sans/index.css'),
      '@fontsource/ibm-plex-mono/400.css': resolve(modules, '@fontsource/ibm-plex-mono/400.css'),
    },
    dedupe: ['vue', 'pinia'],
  },
  server: { host: '127.0.0.1', port: 5196, strictPort: true, fs: { allow: [repo] } },
})
