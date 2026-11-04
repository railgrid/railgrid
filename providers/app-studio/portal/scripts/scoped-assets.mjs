// Keep lazy modules, CSS and fonts under the bootstrap's asset directory so an
// org UI grant scopes every executable asset request to the selected copy.
// The build rewrites references once; response bodies and SRI pins stay intact.
import path from 'node:path'

export function scopedProviderAssets(base) {
  const isAssetSuffix = (suffix) => !suffix || suffix.startsWith('assets/') || /\.[A-Za-z0-9]+(?:[?#].*)?$/.test(suffix)
  return {
    name: 'railgrid-scoped-provider-assets',
    generateBundle: {
      order: 'post',
      handler(_options, bundle) {
        for (const chunk of Object.values(bundle)) {
          const isChunk = chunk.type === 'chunk'
          const isCSS = chunk.type === 'asset' && chunk.fileName.endsWith('.css') && typeof chunk.source === 'string'
          if (!isChunk && !isCSS) continue
          const prefix = JSON.stringify(base).slice(0, -1)
          const matcher = new RegExp(prefix.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '([^"\\\\]*)"', 'g')
          let changed = false
          const rewrite = (literal, suffix) => {
            if (!isAssetSuffix(suffix)) return literal
            changed = true
            if (isCSS) {
              let relative = path.posix.relative(path.posix.dirname(chunk.fileName), suffix || '')
              if (!relative.startsWith('.')) relative = `./${relative}`
              return relative
            }
            return suffix ? `(new URL(${JSON.stringify(suffix)},__railgridAssetBase).href)` : '(__railgridAssetBase)'
          }
          if (isCSS) {
            // CSS urls may be quoted or bare. Rewriting only the known app
            // base keeps third-party/external URLs untouched.
            const cssMatcher = new RegExp(base.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + "([^\"'\\s)]+)", 'g')
            chunk.source = chunk.source.replace(cssMatcher, rewrite)
            continue
          }
          const code = chunk.code.replace(matcher, rewrite)
          if (!changed) continue
          const levels = chunk.fileName.split('/').length - 1
          const root = chunk.isEntry
            ? 'new URL(".",document.currentScript.src).href'
            : `new URL(${JSON.stringify('../'.repeat(levels) || './')},import.meta.url).href`
          chunk.code = chunk.isEntry
            ? code.replace('(()=>{', `(()=>{const __railgridAssetBase=${root};`)
            : `const __railgridAssetBase=${root};\n${code}`
        }
      },
    },
  }
}
