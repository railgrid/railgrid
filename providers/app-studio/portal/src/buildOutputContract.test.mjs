import assert from 'node:assert/strict'
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { pathToFileURL } from 'node:url'
import { join } from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

import {
  collectManifestEntryArtifacts,
  listBuildArtifacts,
  manifestEntryKey,
  totalArtifactSize,
} from '../scripts/build-budget-lib.mjs'

test('refreshes lazy loaders before guarded element registration so active sessions survive a provider rollout', async () => {
  const config = await readFile(new URL('../vite.config.ts', import.meta.url), 'utf8')
  const element = await readFile(new URL('./element.ts', import.meta.url), 'utf8')
  const main = await readFile(new URL('./main.ts', import.meta.url), 'utf8')
  const registry = await readFile(new URL('./lazyLoaderRegistry.ts', import.meta.url), 'utf8')
  const hostLoader = await readFile(new URL('../../../../portal/src/providers/providerScriptLoader.ts', import.meta.url), 'utf8')

  assert.match(config, /chunkFileNames:\s*'assets\/\[name\]-\[hash\]\.js'/)
  assert.match(config, /manifest:\s*true/)
  assert.match(config, /assetFileNames:\s*'assets\/\[name\]-\[hash\]\[extname\]'/)
  const registryInstall = element.indexOf('installCurrentAppStudioLazyLoaders<MountModule>(globalThis, bootstrapGeneration, {')
  const guardedRegistration = element.indexOf('if (!customElements.get(TAG))')
  assert.ok(registryInstall >= 0 && guardedRegistration > registryInstall)
  assert.match(main, /document\.currentScript[\s\S]*dataset\.farosProviderBootstrapGeneration/)
  assert.match(main, /registerAppStudioElements\(bootstrapGeneration\)/)
  for (const contractSource of [registry, hostLoader]) {
    assert.match(contractSource, /__farosProviderBootstrapGenerationsV1/)
  }
  assert.match(element, /loadCurrentAppStudioSurface<MountModule>\(globalThis, surface\)/)
  assert.match(element, /return loadCurrentMount\('page'\)/)
  assert.match(element, /return loadCurrentMount\('tile'\)/)
  assert.doesNotMatch(element, /protected loadMount\(\): Promise<MountModule> \{\s*return import\(/)
  assert.match(element, /private startLoad\(\): void[\s\S]*const generation = \+\+this\.generation/)
  assert.match(element, /retry\.textContent = 'Retry loading App Studio'/)
  assert.match(element, /status\.className = 'k-loading-reveal'/)
  assert.match(element, /status\.setAttribute\('aria-busy', 'true'\)/)
  assert.match(element, /window\.matchMedia\('\(max-width: 767px\)'\)\.matches/)
  assert.match(element, /minHeight: this\.loadingSurface === 'page' \? '100%' : '120px'/)
  assert.match(element, /status\.style\.gridTemplateColumns = compactPage[\s\S]*minmax\(0, 1fr\)[\s\S]*minmax\(3\.5rem, \.65fr\) minmax\(0, 2fr\) minmax\(4\.5rem, \.95fr\)/)
  assert.match(element, /const regionRows = compactPage \? \[5\] : \[3, 5, 4\]/)
  assert.match(element, /retry\.className = 'k-btn k-btn--danger'/)
  assert.match(element, /minHeight: '44px'/)
  assert.doesNotMatch(element, /retry\.className = 'app-studio-touch-target/)
  assert.match(element, /new CustomEvent\(PROVIDER_BOOTSTRAP_RETRY_EVENT,[\s\S]*cancelable: true/)
  assert.match(element, /detail: \{ providerName: 'app-studio' \}/)
  assert.match(element, /if \(!handled\) this\.startLoad\(\)/)
  assert.match(element, /if \(generation !== this\.generation \|\| !this\.isConnected\) return/)
})

test('late stale bootstrap body execution cannot overwrite the current lazy loaders', async (t) => {
  const vite = await createServer({
    appType: 'custom',
    cacheDir: join(tmpdir(), 'faros-vite-app-studio-bootstrap-generation'),
    configFile: false,
    optimizeDeps: { noDiscovery: true },
    server: { middlewareMode: true, hmr: false },
  })
  t.after(() => vite.close())
  const {
    APP_STUDIO_LOADER_REGISTRY_KEY,
    PROVIDER_BOOTSTRAP_GENERATIONS_KEY,
    installCurrentAppStudioLazyLoaders,
  } = await vite.ssrLoadModule('/src/lazyLoaderRegistry.ts')

  const root = {
    [PROVIDER_BOOTSTRAP_GENERATIONS_KEY]: { 'app-studio': 'generation-2' },
  }
  const executeBootstrapBody = (generation, label) => installCurrentAppStudioLazyLoaders(
    root,
    generation,
    {
      page: async () => `${label}-page`,
      tile: async () => `${label}-tile`,
    },
  )

  assert.equal(executeBootstrapBody('generation-2', 'current'), true)
  // Model the detached v1 classic script executing its body after v2.
  assert.equal(executeBootstrapBody('generation-1', 'stale'), false)
  assert.equal(await root[APP_STUDIO_LOADER_REGISTRY_KEY].page(), 'current-page')
  assert.equal(await root[APP_STUDIO_LOADER_REGISTRY_KEY].tile(), 'current-tile')
})

test('a retained wrapper resolves through loaders installed by the newest bootstrap', async (t) => {
  const vite = await createServer({
    appType: 'custom',
    cacheDir: join(tmpdir(), 'faros-vite-app-studio-loader-registry'),
    configFile: false,
    optimizeDeps: { noDiscovery: true },
    server: { middlewareMode: true, hmr: false },
  })
  t.after(() => vite.close())
  const { installAppStudioLazyLoaders, loadCurrentAppStudioSurface } =
    await vite.ssrLoadModule('/src/lazyLoaderRegistry.ts')
  const root = {}
  installAppStudioLazyLoaders(root, {
    page: async () => 'old-page',
    tile: async () => 'old-tile',
  })
  const retainedWrapperLoad = (surface) => loadCurrentAppStudioSurface(root, surface)
  assert.equal(await retainedWrapperLoad('page'), 'old-page')

  installAppStudioLazyLoaders(root, {
    page: async () => 'new-page',
    tile: async () => 'new-tile',
  })
  assert.equal(await retainedWrapperLoad('page'), 'new-page')
  assert.equal(await retainedWrapperLoad('tile'), 'new-tile')
})

test('a newly loaded styles module replaces stale fixed-id provider CSS', async (t) => {
  const vite = await createServer({
    appType: 'custom',
    cacheDir: join(tmpdir(), 'faros-vite-app-studio-style-refresh'),
    configFile: false,
    optimizeDeps: { noDiscovery: true },
    server: { middlewareMode: true, hmr: false },
  })
  t.after(() => vite.close())
  const { ensureAppStudioStyles } = await vite.ssrLoadModule('/src/styles.ts')
  const existing = { tagName: 'STYLE', textContent: 'old deployment CSS' }
  const previousDocument = globalThis.document
  globalThis.document = { getElementById: () => existing }
  try {
    ensureAppStudioStyles()
  } finally {
    if (previousDocument === undefined) delete globalThis.document
    else globalThis.document = previousDocument
  }
  assert.notEqual(existing.textContent, 'old deployment CSS')
  assert.match(existing.textContent, /@scope \(faros-provider-app-studio, faros-dashboard-tile-app-studio\)/)
})

test('route budgets follow transitive manifest imports and emitted CSS without charging sibling routes', () => {
  const manifest = {
    '_styles.js': { file: 'assets/styles-abc.js', name: 'styles' },
    'src/main.ts': {
      file: 'main.js', name: 'main', isEntry: true,
      dynamicImports: ['src/page-element.ts', 'src/tile-element.ts'],
    },
    'src/page-element.ts': {
      file: 'assets/page-element-abc.js', name: 'page-element', isDynamicEntry: true,
      imports: ['_styles.js'], css: ['assets/page-element-def.css'],
    },
    'src/tile-element.ts': {
      file: 'assets/tile-element-abc.js', name: 'tile-element', isDynamicEntry: true,
      imports: ['_styles.js'],
    },
  }
  const mainKey = manifestEntryKey(manifest, (entry) => entry.isEntry === true, 'main entry')
  const pageKey = manifestEntryKey(manifest, (entry) => entry.name === 'page-element', 'page entry')
  const tileKey = manifestEntryKey(manifest, (entry) => entry.name === 'tile-element', 'tile entry')

  assert.deepEqual(collectManifestEntryArtifacts(manifest, mainKey), ['main.js'])
  assert.deepEqual(collectManifestEntryArtifacts(manifest, pageKey), [
    'assets/page-element-abc.js',
    'assets/page-element-def.css',
    'assets/styles-abc.js',
  ])
  assert.deepEqual(collectManifestEntryArtifacts(manifest, tileKey), [
    'assets/styles-abc.js',
    'assets/tile-element-abc.js',
  ])
})

test('the total build inventory includes arbitrary nested production artifacts', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'app-studio-build-budget-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  await mkdir(join(root, 'assets', 'nested'), { recursive: true })
  await Promise.all([
    writeFile(join(root, 'main.js'), 'entry'),
    writeFile(join(root, 'main.css'), 'css'),
    writeFile(join(root, 'assets', 'page-element.js'), 'page'),
    writeFile(join(root, 'assets', 'nested', 'future-font.woff2'), 'font'),
  ])

  const names = await listBuildArtifacts(pathToFileURL(`${root}/`))
  assert.deepEqual(names, [
    'assets/nested/future-font.woff2',
    'assets/page-element.js',
    'main.css',
    'main.js',
  ])
  assert.deepEqual(
    totalArtifactSize(names.map((name) => ({ name, rawBytes: name.length, gzipBytes: 1 }))),
    { rawBytes: names.reduce((sum, name) => sum + name.length, 0), gzipBytes: names.length },
  )
})
