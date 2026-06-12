import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The kedge hub serves this provider under /ui/providers/code/. ProviderFrame
// injects <script src="/ui/providers/code/main.js"> and waits for the
// <kedge-provider-code> custom element to be defined. So the build must emit
// the entry at exactly /main.js (no hash) as an IIFE whose side effects
// (customElements.define) fire on load. See the infrastructure provider's
// vite.config.ts for the full rationale.
export default defineConfig({
  plugins: [vue({
    template: { compilerOptions: { isCustomElement: (tag) => tag.startsWith('kedge-provider-') } },
  })],
  // Library mode leaves Vue's feature-flag globals unreplaced; pre-substitute
  // them so the IIFE runs in a bare <script> tag without "process is not defined".
  define: {
    'process.env.NODE_ENV': JSON.stringify('production'),
    __VUE_OPTIONS_API__: 'true',
    __VUE_PROD_DEVTOOLS__: 'false',
    __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: 'false',
  },
  base: '/ui/providers/code/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    cssCodeSplit: false,
    lib: {
      entry: 'src/main.ts',
      formats: ['iife'],
      name: 'KedgeProviderCode',
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
