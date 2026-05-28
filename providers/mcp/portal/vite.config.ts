import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

// The kedge hub serves this provider under /ui/providers/mcp/. The
// portal's ProviderFrame injects <script src=".../main.js"> once and
// waits for <kedge-provider-mcp> to register. Build emits IIFE with a
// stable entry name so that URL is durable.
//
// `resolve.alias` lets the bundled .vue files reach back into the main
// portal's source tree for shared infrastructure (components, queries,
// utils). One alias is overridden: `@/stores/auth` resolves to a
// provider-local store that hydrates from the kedgeContext property the
// host portal sets on the custom element, rather than the portal's OIDC
// auth store (which would be a fresh, empty instance inside the
// provider's isolated Vue app).
//
// NOTE: provider-local builds do NOT include Tailwind. Their utility
// classes are compiled by the host portal (see @source directives in
// portal/src/assets/main.css). The custom element renders in light DOM,
// so the host stylesheet cascades in.
export default defineConfig({
  base: '/ui/providers/mcp/',
  plugins: [vue()],
  // Replace Node-only globals at build time. Pinia, urql, and Vue's own
  // dev/prod guards check `process.env.NODE_ENV` at module-init; Vite's
  // library mode doesn't auto-stub it (the way SPA mode does), so the
  // first reference crashes the bundle with "process is not defined"
  // before `customElements.define` ever runs. Hard-coding "production"
  // is correct: this bundle is built once and embedded in the binary.
  define: {
    'process.env.NODE_ENV': JSON.stringify('production'),
    __VUE_OPTIONS_API__: 'true',
    __VUE_PROD_DEVTOOLS__: 'false',
    __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: 'false',
  },
  resolve: {
    alias: {
      // Most @/* imports fall through to the portal's src tree.
      '@': resolve(__dirname, '..', '..', '..', 'portal', 'src'),
      // Auth store is overridden so the provider's Pinia instance sees
      // a store that's hydrated from kedgeContext, not OIDC state.
      '@/stores/auth': resolve(__dirname, 'src', 'auth-adapter.ts'),
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
      name: 'KedgeProviderMCP',
      fileName: () => 'main.js',
    },
    rollupOptions: {
      output: {
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: (info) => {
          // Vite emits the entry's CSS as a file alongside main.js;
          // we name it explicitly so assets.go can reference it if
          // needed. With cssCodeSplit: false the bundle has at most
          // one CSS file.
          if (info.name?.endsWith('.css')) return 'main.css'
          return 'assets/[name]-[hash][extname]'
        },
      },
    },
  },
})
