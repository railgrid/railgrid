import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'faros-vite-provider-script-loader'),
  configFile: false,
  optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false },
})
const {
  ProviderPageReloadRequiredError,
  invalidateProviderScript,
  loadProviderScript,
} = await vite.ssrLoadModule('/src/providers/providerScriptLoader.ts')
const { createProviderLoadGeneration } = await vite.ssrLoadModule('/src/providers/providerLoadGeneration.ts')
test.after(() => vite.close())

function providerDocument() {
  let current = null
  const appended = []
  return {
    appended,
    defaultView: {},
    getElementById: (id) => current?.id === id ? current : null,
    createElement: () => {
      const script = {
        dataset: {},
        remove() {
          if (current === script) current = null
        },
      }
      return script
    },
    head: {
      appendChild(script) {
        current = script
        appended.push(script)
      },
    },
  }
}

test('coalesces same-version consumers and supersedes an unresolved older version', async () => {
  const doc = providerDocument()
  const first = loadProviderScript('app-studio', '1', doc)
  const duplicate = loadProviderScript('app-studio', '1', doc)
  assert.strictEqual(duplicate, first)
  await Promise.resolve()
  assert.equal(doc.appended.length, 1)
  assert.equal(doc.appended[0].src, '/ui/providers/app-studio/main.js?v=1')
  const staleGeneration = doc.appended[0].dataset.farosProviderBootstrapGeneration
  const staleOnload = doc.appended[0].onload

  const next = loadProviderScript('app-studio', '2', doc)
  await assert.rejects(first, /superseded provider "app-studio" version 1/)
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(doc.appended.length, 2)
  assert.equal(doc.appended[1].src, '/ui/providers/app-studio/main.js?v=2')
  const currentGeneration = doc.appended[1].dataset.farosProviderBootstrapGeneration
  assert.notEqual(currentGeneration, staleGeneration)
  assert.equal(doc.defaultView.__farosProviderBootstrapGenerationsV1['app-studio'], currentGeneration)

  // A late browser event from the detached v1 script cannot settle or replace
  // the current v2 record.
  staleOnload()
  doc.appended[1].onload()
  await next

  assert.strictEqual(loadProviderScript('app-studio', '2', doc), next)
  invalidateProviderScript('app-studio', '1', doc)
  assert.equal(doc.defaultView.__farosProviderBootstrapGenerationsV1['app-studio'], currentGeneration)
  assert.equal(doc.getElementById('faros-provider-script-app-studio'), doc.appended[1])
})

test('requires a page reload when a direct-registration provider catalog version changes', async () => {
  const doc = providerDocument()
  const first = loadProviderScript('quickstart', '1', doc)
  await Promise.resolve()
  assert.equal(doc.appended.length, 1)
  const staleOnload = doc.appended[0].onload

  // quickstart does not implement the generation-aware retained-wrapper
  // contract, so starting v2 before v1 settles could let either immutable
  // custom-element class win. The v2 consumer must receive an explicit reload
  // state rather than treating the stale v1 promise as a successful v2 load.
  const next = loadProviderScript('quickstart', '2', doc)
  await assert.rejects(next, (error) => {
    assert.ok(error instanceof ProviderPageReloadRequiredError)
    assert.equal(error.code, 'PROVIDER_PAGE_RELOAD_REQUIRED')
    assert.match(error.message, /version changed from 1 to 2; reload the page/)
    return true
  })
  assert.equal(doc.appended.length, 1)

  staleOnload()
  await first
  assert.strictEqual(loadProviderScript('quickstart', '1', doc), first)
  assert.equal(doc.appended.length, 1)
  assert.equal(doc.getElementById('faros-provider-script-quickstart'), doc.appended[0])
})

test('keeps bootstrap generation tokens unique across loader module re-evaluation', async () => {
  const doc = providerDocument()
  const first = loadProviderScript('app-studio', 'hmr-1', doc)
  await Promise.resolve()
  const staleScript = doc.appended[0]
  const staleOnload = staleScript.onload
  const staleGeneration = staleScript.dataset.farosProviderBootstrapGeneration

  // A Vite HMR update re-evaluates this module but retains the browser window
  // and already-prepared classic scripts. A window-owned counter must prevent
  // the fresh module instance from reissuing the old script's token, while the
  // shared load record lets it cancel the superseded request.
  const reloaded = await vite.ssrLoadModule('/src/providers/providerScriptLoader.ts?hmr-generation-test')
  const next = reloaded.loadProviderScript('app-studio', 'hmr-2', doc)
  await assert.rejects(first, /superseded provider "app-studio" version hmr-1/)
  await new Promise((resolve) => setImmediate(resolve))
  const currentScript = doc.appended[1]
  const currentGeneration = currentScript.dataset.farosProviderBootstrapGeneration

  assert.notEqual(currentGeneration, staleGeneration)
  assert.equal(doc.defaultView.__farosProviderBootstrapGenerationsV1['app-studio'], currentGeneration)

  currentScript.onload()
  await next
  staleOnload()
  assert.equal(doc.defaultView.__farosProviderBootstrapGenerationsV1['app-studio'], currentGeneration)
})

test('keeps direct-registration version safety across loader module re-evaluation', async () => {
  const doc = providerDocument()
  const first = loadProviderScript('quickstart', 'hmr-1', doc)
  await Promise.resolve()
  doc.appended[0].onload()
  await first

  // The custom-element class and script state survive HMR. The fresh module
  // must reuse the window-owned load record and require a document reload
  // rather than injecting a second immutable provider bootstrap.
  const reloaded = await vite.ssrLoadModule('/src/providers/providerScriptLoader.ts?hmr-direct-version-test')
  const next = reloaded.loadProviderScript('quickstart', 'hmr-2', doc)
  await assert.rejects(next, (error) => {
    assert.equal(error.code, 'PROVIDER_PAGE_RELOAD_REQUIRED')
    assert.match(error.message, /version changed from hmr-1 to hmr-2; reload the page/)
    return true
  })
  assert.equal(doc.appended.length, 1)
})

test('bounds a provider script request that never settles', async () => {
  const doc = providerDocument()
  const load = loadProviderScript('app-studio', 'stalled', doc, 1)
  await Promise.resolve()
  const failedGeneration = doc.appended[0].dataset.farosProviderBootstrapGeneration
  await assert.rejects(
    load,
    /timed out loading \/ui\/providers\/app-studio\/main\.js\?v=stalled/,
  )
  assert.equal(doc.getElementById('faros-provider-script-app-studio'), null)
  assert.notEqual(doc.defaultView.__farosProviderBootstrapGenerationsV1['app-studio'], failedGeneration)
})

test('keeps a timed-out direct-registration provider terminal until page reload', async () => {
  const doc = providerDocument()
  const first = loadProviderScript('quickstart', 'timed-out', doc, 1)
  await Promise.resolve()
  const lateOnload = doc.appended[0].onload
  await assert.rejects(
    first,
    /timed out loading \/ui\/providers\/quickstart\/main\.js\?v=timed-out/,
  )

  // Explicit invalidation cannot make it safe to inject another immutable
  // custom-element bootstrap: the detached timed-out script body may execute.
  invalidateProviderScript('quickstart', 'timed-out', doc)
  const retry = loadProviderScript('quickstart', 'retry', doc)
  await assert.rejects(retry, (error) => {
    assert.ok(error instanceof ProviderPageReloadRequiredError)
    assert.match(error.message, /version changed from timed-out to retry; reload the page/)
    return true
  })
  assert.equal(doc.appended.length, 1)

  // Model the browser reporting the detached request late. It cannot reopen
  // the loader or cause a second bootstrap to be appended.
  lateOnload()
  assert.equal(doc.appended.length, 1)
})

test('invalidation reinjects a loaded bootstrap at the same catalog version', async () => {
  const doc = providerDocument()
  const first = loadProviderScript('app-studio', '3', doc)
  await Promise.resolve()
  const invalidatedGeneration = doc.appended[0].dataset.farosProviderBootstrapGeneration
  doc.appended[0].onload()
  await first

  assert.strictEqual(loadProviderScript('app-studio', '3', doc), first)
  invalidateProviderScript('app-studio', '3', doc)
  assert.equal(doc.getElementById('faros-provider-script-app-studio'), null)
  assert.notEqual(doc.defaultView.__farosProviderBootstrapGenerationsV1['app-studio'], invalidatedGeneration)

  const retry = loadProviderScript('app-studio', '3', doc)
  await new Promise((resolve) => setImmediate(resolve))
  assert.notStrictEqual(retry, first)
  assert.equal(doc.appended.length, 2)
  doc.appended[1].onload()
  await retry
})

test('pins the bundle with subresource integrity when the catalog carries a hash', async () => {
  const doc = providerDocument()
  const integrity = 'sha384-OLBgp1GsljhM2TJ+sbHjaiH9txEUvgdDTAzHv2P24donTt6/529l+9Ua0vFImLlb'
  const load = loadProviderScript('quickstart', '7', doc, 15_000, { integrity })
  await Promise.resolve()
  assert.equal(doc.appended.length, 1)
  const script = doc.appended[0]
  // SRI hashes the response body, so the ?v= cache-buster in the URL does not
  // disturb the check.
  assert.equal(script.src, '/ui/providers/quickstart/main.js?v=7')
  assert.equal(script.integrity, integrity)
  // No crossorigin: the bundle is same-origin, so the response type is "basic"
  // and integrity is enforced without the attribute. Setting it would make the
  // load a CORS-mode request, and the hub's UI proxy sends no CORS headers.
  assert.equal(script.crossOrigin, undefined)
  script.onload()
  await load
})

test('loads an unpinned bundle with a warning when the catalog carries no hash', async () => {
  const doc = providerDocument()
  const warnings = []
  const originalWarn = console.warn
  console.warn = (message) => warnings.push(String(message))
  try {
    const load = loadProviderScript('quickstart', '8', doc)
    await Promise.resolve()
    const script = doc.appended[0]
    assert.equal(script.integrity, undefined)
    assert.equal(script.crossOrigin, undefined)
    assert.equal(warnings.length, 1)
    assert.match(warnings[0], /provider "quickstart" bundle without an integrity pin/)
    script.onload()
    await load
  } finally {
    console.warn = originalWarn
  }
})

test('generation fence rejects a stale tile completion after newer props win', async () => {
  const fence = createProviderLoadGeneration()
  const commits = []
  let releaseOld
  const oldLoad = new Promise((resolve) => { releaseOld = resolve })

  const oldGeneration = fence.begin()
  const oldCommit = oldLoad.then(() => {
    if (fence.isCurrent(oldGeneration)) commits.push('old')
  })

  const currentGeneration = fence.begin()
  if (fence.isCurrent(currentGeneration)) commits.push('current')
  releaseOld()
  await oldCommit

  assert.deepEqual(commits, ['current'])
})
