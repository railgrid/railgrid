/** Inspect Rollup's actual production graph, including transitive imports. */
export function bundlePerformance({ report = false } = {}) {
  return {
    name: 'app-studio-bundle-performance',
    generateBundle(_options, bundle) {
      const modules = new Map()
      for (const chunk of Object.values(bundle)) {
        if (chunk.type !== 'chunk') continue
        for (const [id, module] of Object.entries(chunk.modules)) {
          const normalized = id.replaceAll('\\', '/')
          if (/\/node_modules\/@vue\/compiler-(core|dom|sfc|ssr)\//.test(normalized)) {
            this.error(`App Studio must not ship Vue's template compiler: ${normalized}. Use vue.runtime.esm-bundler.js; Vite precompiles the templates.`)
          }
          modules.set(normalized, (modules.get(normalized) ?? 0) + module.renderedLength)
        }
      }
      if (report) {
        this.info('Largest rendered modules (before minification; use the budget report for transfer sizes):\n'
          + [...modules].sort((a, b) => b[1] - a[1]).slice(0, 20)
            .map(([id, bytes]) => `${bytes.toLocaleString('en-US').padStart(10)} bytes  ${id.replace(`${process.cwd()}/`, '')}`)
            .join('\n'))
      }
    },
  }
}
