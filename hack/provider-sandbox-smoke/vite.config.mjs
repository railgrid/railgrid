import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const portalSrc = resolve(fileURLToPath(new URL('../../portal/src', import.meta.url)))

export default {
  base: '/ui/',
  appType: 'custom',
  resolve: { alias: { '@': portalSrc } },
  optimizeDeps: { noDiscovery: true, entries: [] },
  server: { host: '127.0.0.1', port: 5174, strictPort: true, hmr: false },
}
