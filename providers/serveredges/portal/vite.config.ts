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
    // Array form + specific-before-general order: @rollup/plugin-alias
    // returns on the first matching entry, and `@` matches anything
    // starting with `@/`. With `@` listed first (object form), the
    // store overrides below never fire — the terminal-adapter is only
    // reachable via this alias (no direct import), so the wrong shadow
    // means EdgeDetailPage's "open terminal" button mutates a dead
    // Pinia store and the portal's TerminalDock never sees the
    // kedge-terminal-open event.
    alias: [
      { find: '@/stores/auth', replacement: resolve(__dirname, 'src', 'auth-adapter.ts') },
      { find: '@/stores/terminalSessions', replacement: resolve(__dirname, 'src', 'terminal-adapter.ts') },
      { find: '@kedge-edges', replacement: resolve(__dirname, '..', '..', 'kubernetesedges', 'portal', 'src') },
      { find: '@', replacement: resolve(__dirname, '..', '..', '..', 'portal', 'src') },
    ],
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
