import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The kedge hub serves this provider under /ui/providers/kro-multicluster/.
// ProviderFrame injects <script src="/ui/providers/kro-multicluster/main.js">
// once and waits for the <kedge-provider-kro-multicluster> custom element
// to be defined. So the build must:
//
//   1. Emit the entry script at exactly /main.js (no hash, no /assets/
//      prefix) so the hardcoded portal URL keeps working across rebuilds.
//   2. Bundle IIFE so the script tag runs before module loaders are ready
//      and side-effects (customElements.define) fire.
//   3. Place lazy chunks under /assets/ — the hub's UI proxy treats
//      anything with a "." in the last segment as an asset and routes it
//      to this binary.
export default defineConfig({
  plugins: [vue({
    // Allow <kedge-provider-kro-multicluster> as a custom element tag
    // inside Vue templates without compiler warnings (the element is
    // OUR own host shell, not the Vue-rendered children, but Vue's
    // compiler also runs over user pages that may want to reference
    // it for debug.).
    template: { compilerOptions: { isCustomElement: (tag) => tag.startsWith('kedge-provider-') } },
  })],
  // Vite's library mode (`build.lib`) intentionally leaves
  // process.env.NODE_ENV and the __VUE_*__ feature flags
  // UNREPLACED because library packages are meant to be re-bundled
  // by their consumer. We are NOT consumed via npm — the portal
  // loads main.js straight as a <script> tag into the browser, so
  // Vue's runtime guards (`if (process.env.NODE_ENV !== 'production')`)
  // blow up at load time with "process is not defined" and the
  // customElements.define() call never runs. Pre-substitute the
  // constants Vue probes for. See:
  // https://link.vuejs.org/feature-flags
  define: {
    'process.env.NODE_ENV': JSON.stringify('production'),
    __VUE_OPTIONS_API__: 'true',
    __VUE_PROD_DEVTOOLS__: 'false',
    __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: 'false',
  },
  base: '/ui/providers/kro-multicluster/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    cssCodeSplit: false, // single style.css that the IIFE injects via ?inline
    lib: {
      entry: 'src/main.ts',
      formats: ['iife'],
      name: 'KedgeProviderKroMulticluster',
      fileName: () => 'main.js',
    },
    rollupOptions: {
      output: {
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
        // Inline all Vue runtime + our code into main.js to keep the
        // bundle a single round-trip from the portal. With lib mode +
        // iife there's no dynamic import surface anyway.
        inlineDynamicImports: true,
      },
    },
  },
})
