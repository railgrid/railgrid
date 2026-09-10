import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'path'

// Nested lazy routes must not cause Rollup to export Vite's shared preload
// helper from the classic bootstrap. Give the bootstrap its own helper module;
// page chunks can then share the normal helper without importing main.js.
function isolateBootstrapPreload(): Plugin {
  const helper = '\0vite/preload-helper.js'
  const isolated = '\0app-studio/bootstrap-preload.js'
  return {
    name: 'app-studio-bootstrap-preload',
    enforce: 'pre',
    resolveId(source, importer) {
      if (source === helper && importer?.endsWith('/src/element.ts')) return isolated
    },
    async load(id) {
      if (id === isolated) return (await this.load({ id: helper })).code
    },
  }
}

function isolateClassicBootstrap() {
  return {
    name: 'app-studio-isolate-classic-bootstrap',
    generateBundle: {
      order: 'post' as const,
      handler(_options: unknown, bundle: Record<string, { type: string; isEntry?: boolean; fileName: string; code?: string }>) {
        for (const artifact of Object.values(bundle)) {
          if (artifact.type !== 'chunk' || !artifact.isEntry || artifact.fileName !== 'main.js' || artifact.code === undefined) continue
          // The host loads main.js as a classic script. Keep its declarations
          // out of the shared global lexical scope while leaving lazy chunks as
          // ES modules for dynamic import(). Run after Vite's dependency mapper
          // has prepended its helper so the wrapper remains the outer boundary.
          artifact.code = `(()=>{${artifact.code}\n})();`
        }
      },
    },
  }
}

export default defineConfig({
  base: '/ui/providers/app-studio/',
  plugins: [
    isolateBootstrapPreload(),
    vue(),
    tailwindcss(),
    isolateClassicBootstrap(),
  ],
  define: {
    'process.env.NODE_ENV': JSON.stringify('production'),
    // App Studio uses Composition API exclusively. Excluding Options API keeps
    // the shared Vue runtime downloaded by either lazy surface smaller.
    __VUE_OPTIONS_API__: 'false',
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
    cssCodeSplit: true,
    manifest: true,
    modulePreload: false,
    rollupOptions: {
      input: resolve(__dirname, 'src/main.ts'),
      output: {
        entryFileNames: 'main.js',
        // Content hashes remain safe across Recreate deployments because the
        // synchronously loaded bootstrap refreshes element.ts's global loader
        // registry even when the browser retains the previously registered
        // custom-element classes. Each lazy import therefore uses the current
        // build's cohesive page/tile/shared chunk graph.
        chunkFileNames: 'assets/[name]-[hash].js',
        // Component CSS must change URL with its bytes. An active host session
        // can cross a provider rollout, so a fixed main.css URL risks reusing
        // stale rules against the newly selected hashed page chunk.
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
})
