import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const repo = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const fixtureRoot = resolve(fileURLToPath(new URL('.', import.meta.url)))
const appPortal = resolve(repo, 'providers/app-studio/portal')
const appVue = resolve(appPortal, 'node_modules/vue/dist/vue.esm-bundler.js')
// The root portal owns these self-hosted webfont imports. Use its installed
// dependency tree by default; MODEL_FORM_FONT_NODE_MODULES can point at an
// equivalent installed tree when the root portal has not been bootstrapped.
const fontRoot = process.env.MODEL_FORM_FONT_NODE_MODULES || resolve(repo, 'portal/node_modules')

const [{ defineConfig }, { default: vue }, { default: tailwind }] = await Promise.all([
  import(pathToFileURL(resolve(appPortal, 'node_modules/vite/dist/node/index.js')).href),
  import(pathToFileURL(resolve(appPortal, 'node_modules/@vitejs/plugin-vue/dist/index.mjs')).href),
  import(pathToFileURL(resolve(appPortal, 'node_modules/@tailwindcss/vite/dist/index.mjs')).href),
])

export default defineConfig({
  root: fixtureRoot,
  plugins: [vue(), tailwind()],
  resolve: {
    alias: {
      // All roots must share the App Studio Vue runtime. Without this alias,
      // Vite can load multiple Vue copies from the provider node_modules and
      // custom-element mounts fail with split reactive runtimes.
      vue: appVue,
      // App Studio's production bundle is self-contained and its App.vue
      // imports shared sources through the canonical @ alias.
      '@': resolve(appPortal, 'src'),
      'lucide-vue-next': resolve(appPortal, 'node_modules/lucide-vue-next/dist/esm/lucide-vue-next.js'),
      tailwindcss: resolve(appPortal, 'node_modules/tailwindcss'),
      '@fontsource-variable/instrument-sans': `${fontRoot}/@fontsource-variable/instrument-sans/index.css`,
      '@fontsource-variable/archivo/wdth.css': `${fontRoot}/@fontsource-variable/archivo/wdth.css`,
      '@fontsource/ibm-plex-mono/400.css': `${fontRoot}/@fontsource/ibm-plex-mono/400.css`,
      '@fontsource/ibm-plex-mono/500.css': `${fontRoot}/@fontsource/ibm-plex-mono/500.css`,
      'faros-app-main': resolve(repo, 'providers/app-studio/portal/src/main.ts'),
      'faros-agents-main': resolve(repo, 'providers/agents/portal/src/main.ts'),
      'faros-databricks-main': resolve(repo, 'providers/databricks/portal/src/main.ts'),
    },
    dedupe: ['vue'],
  },
  server: {
    host: '127.0.0.1',
    port: Number(process.env.MODEL_FORM_PORT || 5198),
    strictPort: true,
    fs: { allow: [repo, fixtureRoot, fontRoot] },
  },
})
