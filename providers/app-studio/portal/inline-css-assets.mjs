// App Studio is delivered to the host as a single IIFE script. Vite extracts
// styles from Vue SFCs into a CSS asset even when cssCodeSplit is disabled, so
// fold those component styles back into the entry chunk before it is served.
export function inlineCssAssets({ styleId }) {
  return {
    name: 'app-studio-inline-css-assets',
    enforce: 'post',
    generateBundle(_options, bundle) {
      const cssAssets = Object.values(bundle)
        .filter((item) => item.type === 'asset' && item.fileName.endsWith('.css'))
        .sort((left, right) => left.fileName.localeCompare(right.fileName, 'en'))

      if (cssAssets.length === 0) return

      const entry = Object.values(bundle).find((item) => item.type === 'chunk' && item.isEntry)
      if (!entry) throw new Error('cannot inline App Studio component CSS without an entry chunk')

      const css = cssAssets
        .map((asset) => typeof asset.source === 'string'
          ? asset.source
          : new TextDecoder().decode(asset.source))
        .join('\n')
      const id = JSON.stringify(styleId)
      // Vue SFC styles are emitted separately from the Tailwind entry CSS.
      // Scope that asset too, otherwise it can leak selectors into the host
      // document even though the runtime entry styles are scoped.
      const source = JSON.stringify(`@scope (faros-provider-app-studio) {\n${css}\n}`)

      entry.code = `;(()=>{if(typeof document==='undefined'||document.getElementById(${id}))return;const style=document.createElement('style');style.id=${id};style.textContent=${source};document.head.appendChild(style)})();${entry.code}`
      for (const asset of cssAssets) delete bundle[asset.fileName]
    },
  }
}
