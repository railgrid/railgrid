import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { fixtureResponse, hostedFixture } from './fixtures.mjs'

const repo = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const here = fileURLToPath(new URL('.', import.meta.url))
const modules = resolve(repo, 'portal/node_modules')
const [{ defineConfig }, { default: vue }, { default: tailwind }] = await Promise.all([
  import(pathToFileURL(resolve(modules, 'vite/dist/node/index.js')).href),
  import(pathToFileURL(resolve(modules, '@vitejs/plugin-vue/dist/index.mjs')).href),
  import(pathToFileURL(resolve(modules, '@tailwindcss/vite/dist/index.mjs')).href),
])

// Run production code unchanged. Only authenticated API replies and the
// external checkout/portal pages are deterministic local fixtures.
export default defineConfig({
  root: resolve(repo, 'portal'),
  // The fixture and Tilt portal run concurrently with different configs.
  // Sharing optimized dependencies invalidates the live portal's module URLs.
  cacheDir: resolve(modules, '.vite-billing-flow'),
  base: '/ui/',
  plugins: [{
    name: 'billing-design-fixture',
    enforce: 'pre',
    transform(code, id) {
      if (id === resolve(repo, 'portal/src/main.ts')) {
        return `import ${JSON.stringify(resolve(here, 'bootstrap.js'))};\n${code}`
      }
    },
    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        const url = new URL(req.url, 'http://127.0.0.1')
        const hosted = hostedFixture(url)
        if (hosted) {
          res.setHeader('Content-Type', 'text/html; charset=utf-8')
          return res.end(hosted)
        }
        if (!/^\/(api|auth|clusters)(\/|$)/.test(url.pathname) && !['/healthz', '/version'].includes(url.pathname)) return next()
        let body = ''
        for await (const chunk of req) body += chunk
        const answer = fixtureResponse(url.pathname, req.method, {
          referer: new URL(req.headers.referer || '/', 'http://127.0.0.1'),
          cookie: req.headers.cookie || '', body,
        })
        res.statusCode = answer.status ?? 200
        res.setHeader('Content-Type', 'application/json')
        res.end(JSON.stringify(answer.body))
      })
    },
  }, vue(), tailwind()],
  resolve: { alias: {
    '@': resolve(repo, 'portal/src'),
    vue: resolve(modules, 'vue/dist/vue.esm-bundler.js'),
    'vue-router': resolve(modules, 'vue-router/dist/vue-router.mjs'),
    pinia: resolve(modules, 'pinia/dist/pinia.mjs'),
    'lucide-vue-next': resolve(modules, 'lucide-vue-next/dist/esm/lucide-vue-next.js'),
  }, dedupe: ['vue', 'pinia'] },
  server: { host: '127.0.0.1', port: Number(process.env.BILLING_PREVIEW_PORT || 5198), strictPort: true, fs: { allow: [repo] } },
})
