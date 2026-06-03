import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

// Hub serves this provider under /ui/providers/kubernetes-edges/. The
// portal's ProviderFrame loads main.js, which registers the custom
// element <kedge-provider-kubernetes-edges> and mounts a fresh Vue 3
// app per element instance (own Pinia, own memory-history router).
//
// Two store overrides:
//   - @/stores/auth → src/auth-adapter.ts so composables read tokens
//     hydrated from kedgeContext instead of the portal's OIDC state.
//   - @/stores/terminalSessions → src/terminal-adapter.ts: terminals
//     route through a CustomEvent bridge to the portal's dock instead
//     of trying to mutate a portal Pinia store the provider has no
//     handle to.
//
// Everything else (@/components, @/composables, @/graphql, …) falls
// through to the main portal source tree via the @ alias; Vite bundles
// the referenced files into this provider's main.js. The Tailwind
// utility classes those files use are compiled by the host portal
// (see @source directives in portal/src/assets/main.css), so this
// bundle ships no CSS of its own.
export default defineConfig({
  base: '/ui/providers/kubernetes-edges/',
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
    // override aliases below never fire — the terminal-adapter is only
    // reachable via this alias (no direct import), so the wrong shadow
    // means EdgeDetailPage's "open terminal" button mutates a dead
    // Pinia store and the portal's TerminalDock never sees the
    // kedge-terminal-open event.
    alias: [
      { find: '@/stores/auth', replacement: resolve(__dirname, 'src', 'auth-adapter.ts') },
      { find: '@/stores/terminalSessions', replacement: resolve(__dirname, 'src', 'terminal-adapter.ts') },
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
      name: 'KedgeProviderKubernetesEdges',
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
