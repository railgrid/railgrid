import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
// Keep the executable design verifier outside Vite's config bundler (it has a shebang).
// Version the import so a Vite config restart reloads the catalog and review mappings.
const catalogURL = new URL('./catalog.mjs', import.meta.url)
catalogURL.searchParams.set('loaded', String(Date.now()))
const { repo } = await import(catalogURL.href)

const root = fileURLToPath(new URL('.', import.meta.url))
const require = createRequire(resolve(repo, 'portal/package.json'))
const { default: vue } = await import(require.resolve('@vitejs/plugin-vue'))
const { default: tailwind } = await import(require.resolve('@tailwindcss/vite'))
const aliases = Object.fromEntries([
  'vue', 'pinia', 'vue-router', 'lucide-vue-next', 'markdown-it',
  '@fontsource-variable/instrument-sans', '@fontsource-variable/archivo/wdth.css',
  '@fontsource/ibm-plex-mono/400.css', '@fontsource/ibm-plex-mono/500.css',
].map(id => [id, require.resolve(id)]))
const virtual = '\0virtual:design-review'
const sourcePrefix = 'virtual:design-review-source/'
const virtualSourcePrefix = '\0' + sourcePrefix
let sourceFiles = {}
// These are native elements: the filter is registered by PortalKit and the
// provider/tile tags are style-scoping wrappers around mounted Vue fixtures.
const nativeFixtureElements = new Set(['railgrid-form-select', 'railgrid-resource-table-filter', 'railgrid-provider-agents', 'railgrid-provider-infrastructure', 'railgrid-dashboard-tile-agents'])

export default {
  root,
  base: './',
  publicDir: false,
  cacheDir: resolve(repo, 'portal/.vite-cache/design-review'),
  plugins: [vue({ template: { compilerOptions: { isCustomElement: tag => nativeFixtureElements.has(tag) } } }), tailwind(), {
    name: 'railgrid-design-review-catalog',
    resolveId(id) {
      if (id === 'virtual:design-review') return virtual
      if (id.startsWith(sourcePrefix)) return '\0' + id
    },
    async load(id) {
      if (id.startsWith(virtualSourcePrefix)) {
        const path = decodeURIComponent(id.slice(virtualSourcePrefix.length, -3))
        if (!Object.hasOwn(sourceFiles, path)) throw new Error('Unknown design review source: ' + path)
        this.addWatchFile(resolve(repo, path))
        return 'export default ' + JSON.stringify(sourceFiles[path])
      }
      if (id !== virtual) return
      // Reload mappings as well as Markdown on every catalog rebuild.
      catalogURL.searchParams.set('loaded', `${Date.now()}-${Math.random()}`)
      const { reviewCatalog } = await import(catalogURL.href)
      const data = reviewCatalog()
      sourceFiles = data.sources
      this.addWatchFile(resolve(root, 'reviews.mjs'))
      this.addWatchFile(resolve(root, 'adoption.mjs'))
      for (const path of Object.keys(data.sources)) this.addWatchFile(resolve(repo, path))
      const loaders = Object.keys(data.sources).map(path => `${JSON.stringify(path)}: () => import(${JSON.stringify(sourcePrefix + encodeURIComponent(path) + '.js')})`).join(',')
      data.sources = Object.fromEntries(Object.keys(data.sources).map(path => [path, true]))
      return 'export default ' + JSON.stringify(data) + '; export const sourceLoaders = {' + loaders + '}'
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
    rollupOptions: { input: { terminalReview: resolve(root, 'terminal-review.html'), recoverydeadline: resolve(root, 'recovery-deadline.html'), authgrantsrecovery: resolve(root, 'auth-grants-recovery.html'), appstudiosharing: resolve(root, 'app-studio-sharing.html'), mcpaccess: resolve(root, 'mcp-access.html'), quickstart: resolve(root, 'quickstart-recovery.html'), codeRefresh: resolve(root, 'code-refresh.html'), codeConnections: resolve(root, 'code-connections.html'), agentsToast: resolve(root, 'agents-toast.html'), review: resolve(root, 'index.html'), onboarding: resolve(root, 'onboarding.html'), dex: resolve(root, 'dex.html'), handoff: resolve(root, 'handoff.html'), materials: resolve(root, 'material-sample.html'), kueryImpact: resolve(root, 'kuery-impact.html'), kueryTopology: resolve(root, 'kuery-topology.html') } },
  },
}
