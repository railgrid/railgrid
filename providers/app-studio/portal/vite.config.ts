import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'path'

export default defineConfig({
  base: '/ui/providers/app-studio/',
  plugins: [vue(), tailwindcss()],
  define: {
    'process.env.NODE_ENV': JSON.stringify('production'),
    __VUE_OPTIONS_API__: 'true',
    __VUE_PROD_DEVTOOLS__: 'false',
    __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: 'false',
  },
  resolve: {
    alias: {
      // Resolve to this provider's own src so the build is self-contained:
      // identical whether run from the monorepo (make) or a standalone Docker
      // build context. Shared components are vendored under src/components,
      // src/composables (kept in sync with the root portal). Pointing at the
      // root portal would only resolve in the monorepo and silently break the
      // image build.
      '@': resolve(__dirname, 'src'),
      'lucide-vue-next': resolve(__dirname, 'node_modules', 'lucide-vue-next', 'dist', 'esm', 'lucide-vue-next.js'),
      vue: resolve(__dirname, 'node_modules', 'vue', 'dist', 'vue.esm-bundler.js'),
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
      name: 'KedgeProviderAppStudio',
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
