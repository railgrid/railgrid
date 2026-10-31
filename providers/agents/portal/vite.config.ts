import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createMcpAppProxyDocument } from './src/mcpAppHost'

// The railgrid hub serves this provider under /ui/providers/agents/. The
// ProviderFrame injects a <script src="/ui/providers/agents/main.js"> tag once
// and waits for the railgrid-provider-agents custom element to be defined. So the
// build must:
//   1. Emit the entry script at exactly /main.js (no hash) so the hard-coded
//      portal URL keeps working across rebuilds.
//   2. Bundle in IIFE format — the script runs before module loaders are ready
//      and registers the custom element as a side effect.
//   3. Place lazy chunks under /assets/ so the hub's UI proxy routes them here.
export default defineConfig({
  plugins: [vue(), {
    // The host loads only main.js. Include Vue SFC styles in that same
    // integrity-pinned script, including styles for teleported popovers.
    name: 'agents-inline-component-styles',
    generateBundle: {
      order: 'post',
      handler(_options, bundle) {
        const entry = bundle['main.js']
        if (entry?.type !== 'chunk') return
        const css = Object.values(bundle)
          .filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css'))
          .map(asset => asset.type === 'asset' ? String(asset.source) : '')
          .join('\n')
        if (!css) return
        entry.code = `(()=>{const id='railgrid-agents-component-css';if(!document.getElementById(id)){const style=document.createElement('style');style.id=id;style.textContent=${JSON.stringify(css)};document.head.appendChild(style)}})();\n${entry.code}`
      },
    },
  }, {
    name: 'agents-mcp-app-sandbox-document',
    generateBundle() {
      this.emitFile({ type: 'asset', fileName: 'mcp-app-sandbox.html', source: createMcpAppProxyDocument() })
    },
  }],
  define: {
    'process.env.NODE_ENV': JSON.stringify('production'),
    __VUE_OPTIONS_API__: 'true',
    __VUE_PROD_DEVTOOLS__: 'false',
    __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: 'false',
  },
  base: '/ui/providers/agents/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    cssCodeSplit: false,
    lib: {
      entry: 'src/main.ts',
      formats: ['iife'],
      name: 'RailgridProviderAgents',
      fileName: () => 'main.js',
    },
    rollupOptions: {
      output: {
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
        inlineDynamicImports: true,
      },
    },
  },
})
