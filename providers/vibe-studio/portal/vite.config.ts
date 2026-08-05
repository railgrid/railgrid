import { defineConfig } from 'vite'

// The kedge hub serves this provider under /ui/providers/vibe-studio/. The
// ProviderFrame component injects a <script src=".../main.js"> tag once and
// waits for the kedge-provider-vibe-studio custom element to be defined, so
// the build emits the entry at exactly /main.js (no hash) in IIFE format,
// with lazy chunks under /assets/ (routed by the hub's isAssetPath check).
export default defineConfig({
  base: '/ui/providers/vibe-studio/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    lib: {
      entry: 'src/main.ts',
      formats: ['iife'],
      name: 'KedgeProviderVibeStudio',
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
