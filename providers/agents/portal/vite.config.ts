import { defineConfig } from 'vite'

// The kedge hub serves this provider under /ui/providers/agents/. The
// ProviderFrame injects a <script src="/ui/providers/agents/main.js"> tag once
// and waits for the kedge-provider-agents custom element to be defined. So the
// build must:
//   1. Emit the entry script at exactly /main.js (no hash) so the hard-coded
//      portal URL keeps working across rebuilds.
//   2. Bundle in IIFE format — the script runs before module loaders are ready
//      and registers the custom element as a side effect.
//   3. Place lazy chunks under /assets/ so the hub's UI proxy routes them here.
export default defineConfig({
  base: '/ui/providers/agents/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    lib: {
      entry: 'src/main.ts',
      formats: ['iife'],
      name: 'KedgeProviderAgents',
      fileName: () => 'main.js',
    },
    rollupOptions: {
      output: {
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
})
