import { defineConfig } from 'vite'

// The kedge hub serves this provider under /ui/providers/kuery/. The
// ProviderFrame component injects a <script src="/ui/providers/kuery/main.js">
// tag once and waits for the kedge-provider-kuery custom element to be
// defined. So the build needs to:
//
//   1. Emit the entry script at exactly /main.js (no hash, no /assets/ prefix)
//      so the hard-coded portal URL keeps working across rebuilds.
//   2. Bundle in IIFE format — the script tag runs before module loaders are
//      ready and we want to register the custom element as a side effect.
//   3. Place lazy-loaded chunks under /assets/ — the hub's UI proxy already
//      treats requests with a "." in the last segment as assets, so dynamic
//      import() URLs round-trip fine without further config.
//
// `base: '/ui/providers/kuery/'` makes Vite emit asset URLs relative to
// the portal's mount prefix so dynamic chunks resolve via the hub's UI proxy
// even when the page itself was navigated to from inside the portal SPA.
export default defineConfig({
  base: '/ui/providers/kuery/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    lib: {
      entry: 'src/main.ts',
      formats: ['iife'],
      name: 'KedgeProviderKuery',
      fileName: () => 'main.js',
    },
    rollupOptions: {
      output: {
        // Code-split chunks land in /assets/ alongside other static files;
        // the hub's isAssetPath heuristic routes them to this binary.
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
})
