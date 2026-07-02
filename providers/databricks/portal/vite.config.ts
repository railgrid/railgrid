import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue({
    template: { compilerOptions: { isCustomElement: (tag) => tag.startsWith('kedge-provider-') } },
  })],
  define: {
    'process.env.NODE_ENV': JSON.stringify('production'),
    __VUE_OPTIONS_API__: 'true',
    __VUE_PROD_DEVTOOLS__: 'false',
    __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: 'false',
  },
  base: '/ui/providers/databricks/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    cssCodeSplit: false,
    lib: {
      entry: 'src/main.ts',
      formats: ['iife'],
      name: 'KedgeProviderDatabricks',
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
