import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The faros hub serves this provider under /ui/providers/agents/. The
// ProviderFrame injects a <script src="/ui/providers/agents/main.js"> tag once
// and waits for the faros-provider-agents custom element to be defined. So the
// build must:
//   1. Emit the entry script at exactly /main.js (no hash) so the hard-coded
//      portal URL keeps working across rebuilds.
//   2. Bundle in IIFE format — the script runs before module loaders are ready
//      and registers the custom element as a side effect.
//   3. Place lazy chunks under /assets/ so the hub's UI proxy routes them here.
export default defineConfig({
  plugins: [vue()],
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
      name: 'FarosProviderAgents',
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
