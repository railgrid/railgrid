import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = dirname(fileURLToPath(import.meta.url))
const repo = resolve(root, '../..')
const require = createRequire(resolve(repo, 'providers/agents/portal/package.json'))
const portalRequire = createRequire(resolve(repo, 'portal/package.json'))
const { defineConfig } = await import(pathToFileURL(require.resolve('vitest/config')).href)
const { default: vue } = await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)

export default defineConfig({
  root,
  plugins: [vue()],
  resolve: {
    alias: {
      '@': resolve(repo, 'portal/src'),
      '@fontsource-variable/instrument-sans': portalRequire.resolve('@fontsource-variable/instrument-sans'),
      '@fontsource-variable/archivo/wdth.css': portalRequire.resolve('@fontsource-variable/archivo/wdth.css'),
      '@fontsource/ibm-plex-mono/400.css': portalRequire.resolve('@fontsource/ibm-plex-mono/400.css'),
      '@fontsource/ibm-plex-mono/500.css': portalRequire.resolve('@fontsource/ibm-plex-mono/500.css'),
      vue: resolve(repo, 'portal/node_modules/vue/dist/vue.esm-bundler.js'),
      'lucide-vue-next': require.resolve('lucide-vue-next'),
      pinia: portalRequire.resolve('pinia'),
      'vue-router': resolve(repo, 'portal/node_modules/vue-router/dist/vue-router.mjs'),
    },
    dedupe: ['vue'],
  },
  test: { environment: 'jsdom', include: ['*.test.ts'], setupFiles: ['dialog-test-setup.ts'] },
})
