import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import vue from '../../portal/node_modules/@vitejs/plugin-vue/dist/index.mjs'
import tailwind from '../../portal/node_modules/@tailwindcss/vite/dist/index.mjs'
// Keep the executable design verifier outside Vite's config bundler (it has a shebang).
// Version the import so a Vite config restart reloads the catalog and review mappings.
const catalogURL = new URL('./catalog.mjs', import.meta.url)
catalogURL.searchParams.set('loaded', String(Date.now()))
const { reviewCatalog, repo } = await import(catalogURL.href)

const root = fileURLToPath(new URL('.', import.meta.url))
const require = createRequire(resolve(repo, 'portal/package.json'))
const aliases = Object.fromEntries([
  'vue', 'pinia', 'vue-router', 'lucide-vue-next', 'markdown-it',
  '@fontsource-variable/instrument-sans', '@fontsource-variable/archivo/wdth.css',
  '@fontsource/ibm-plex-mono/400.css', '@fontsource/ibm-plex-mono/500.css',
].map(id => [id, require.resolve(id)]))
const virtual = '\0virtual:design-review'

export default {
  root,
  base: './',
  publicDir: false,
  cacheDir: resolve(repo, 'portal/.vite-cache/design-review'),
  plugins: [vue({ template: { compilerOptions: { isCustomElement: tag => ['railgrid-form-select', 'railgrid-provider-agents', 'railgrid-provider-infrastructure'].includes(tag) } } }), tailwind(), {
    name: 'railgrid-design-review-catalog',
    resolveId(id) { if (id === 'virtual:design-review') return virtual },
    load(id) {
      if (id !== virtual) return
      const data = reviewCatalog()
      for (const path of Object.keys(data.sources)) this.addWatchFile(resolve(repo, path))
      return 'export default ' + JSON.stringify(data)
    },
  }],
  resolve: {
    alias: { '@': resolve(repo, 'portal/src'), ...aliases, vue: resolve(repo, 'portal/node_modules/vue/dist/vue.esm-bundler.js'), 'vue-router': resolve(repo, 'portal/node_modules/vue-router/dist/vue-router.mjs'), 'lucide-vue-next': resolve(repo, 'portal/node_modules/lucide-vue-next/dist/esm/lucide-vue-next.js') },
    dedupe: ['vue', 'pinia'],
  },
  server: { host: '127.0.0.1', port: 5197, strictPort: true, fs: { allow: [repo] } },
  build: {
    outDir: resolve(repo, '.kcp/design-review-dist'),
    emptyOutDir: true,
    target: 'esnext',
    rollupOptions: { input: { authgrantsrecovery: resolve(root, 'auth-grants-recovery.html'), appstudiosharing: resolve(root, 'app-studio-sharing.html'), mcpaccess: resolve(root, 'mcp-access.html'), quickstart: resolve(root, 'quickstart-recovery.html'), codeRefresh: resolve(root, 'code-refresh.html'), codeConnections: resolve(root, 'code-connections.html'), agentsToast: resolve(root, 'agents-toast.html'), review: resolve(root, 'index.html'), onboarding: resolve(root, 'onboarding.html'), dex: resolve(root, 'dex.html'), handoff: resolve(root, 'handoff.html'), materials: resolve(root, 'material-sample.html'), kueryImpact: resolve(root, 'kuery-impact.html'), kueryTopology: resolve(root, 'kuery-topology.html') } },
  },
}
