// Keep a provider's lazy modules, CSS and fonts under the bootstrap's asset
// directory. The hub may put a sealed, provider-specific grant in that directory.
// No source rewriting occurs at serving time, so the bootstrap's SRI stays valid.
export function scopedProviderAssets(base) {
  return {
    name: 'railgrid-scoped-provider-assets',
    generateBundle: {
      order: 'post',
      handler(_options, bundle) {
        for (const chunk of Object.values(bundle)) {
          if (chunk.type !== 'chunk') continue
          const prefix = JSON.stringify(base).slice(0, -1)
          const matcher = new RegExp(prefix.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '([^"\\\\]*)"', 'g')
          let changed = false
          const code = chunk.code.replace(matcher, (literal, suffix) => {
            if (suffix && !suffix.startsWith('assets/')) return literal
            changed = true
            return suffix ? `(new URL(${JSON.stringify(suffix)},__railgridAssetBase).href)` : '(__railgridAssetBase)'
          })
          if (!changed) continue
          const levels = chunk.fileName.split('/').length - 1
          const root = chunk.isEntry
            ? 'new URL(".",document.currentScript.src).href'
            : `new URL(${JSON.stringify('../'.repeat(levels) || './')},import.meta.url).href`
          // Entry is a classic-script IIFE; keep the capture inside it so a
          // later organization cannot replace this generation's asset base.
          chunk.code = chunk.isEntry
            ? code.replace('(()=>{', `(()=>{const __railgridAssetBase=${root};`)
            : `const __railgridAssetBase=${root};\n${code}`
        }
      },
    },
  }
}
