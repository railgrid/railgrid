import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

// Hub serves this provider under /ui/providers/server-edges/. Custom
// element <kedge-provider-server-edges> mounts a fresh Vue 3 app per
// instance.
//
// Cross-provider reuse: EdgesPage / EdgeDetailPage / EdgeCreateModal
// live in the kubernetes-edges provider's source tree because they're
// shared "edge primitives" both providers render (parameterised by the
// `kind` prop). server-edges imports them via the `@kedge-edges` alias
// — this is the one documented cross-provider coupling in the codebase,
// and it mirrors the manifest's declared dependency model: server-edges
// and kubernetes-edges are sibling providers that both surface the
// same Edge CRD, partitioned only by spec.type.
//
// Store overrides match the kubernetes-edges provider — see
// providers/kubernetesedges/portal/vite.config.ts for rationale.
export default defineConfig({
  base: '/ui/providers/server-edges/',
  plugins: [vue()],
  // Replace Node-only globals at build time. Pinia, urql, and Vue's own
  // dev/prod guards check `process.env.NODE_ENV` at module-init; Vite's
  // library mode doesn't auto-stub it (the way SPA mode does), so the
  // first reference crashes the bundle with "process is not defined"
  // before `customElements.define` ever runs.
  define: {
    'process.env.NODE_ENV': JSON.stringify('production'),
    __VUE_OPTIONS_API__: 'true',
    __VUE_PROD_DEVTOOLS__: 'false',
    __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: 'false',
  },
  resolve: {
    alias: {
      '@': resolve(__dirname, '..', '..', '..', 'portal', 'src'),
      '@/stores/auth': resolve(__dirname, 'src', 'auth-adapter.ts'),
      '@/stores/terminalSessions': resolve(__dirname, 'src', 'terminal-adapter.ts'),
      '@kedge-edges': resolve(__dirname, '..', '..', 'kubernetesedges', 'portal', 'src'),
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    cssCodeSplit: false,
    lib: {
      entry: 'src/main.ts',
      formats: ['iife'],
      name: 'KedgeProviderServerEdges',
      fileName: () => 'main.js',
    },
    rollupOptions: {
      output: {
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: (info) => {
          if (info.name?.endsWith('.css')) return 'main.css'
          return 'assets/[name]-[hash][extname]'
        },
      },
    },
  },
})
