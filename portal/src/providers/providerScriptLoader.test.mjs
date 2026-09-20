import assert from 'node:assert/strict'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: join(tmpdir(), 'railgrid-vite-provider-script-loader'),
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
  const staleGeneration = doc.appended[0].dataset.railgridProviderBootstrapGeneration
  const staleOnload = doc.appended[0].onload

  const next = loadProviderScript('app-studio', '2', doc)
  await assert.rejects(first, /superseded provider "app-studio" version 1/)
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(doc.appended.length, 2)
  assert.equal(doc.appended[1].src, '/ui/providers/app-studio/main.js?v=2')
  const currentGeneration = doc.appended[1].dataset.railgridProviderBootstrapGeneration
  assert.notEqual(currentGeneration, staleGeneration)
  assert.equal(doc.defaultView.__railgridProviderBootstrapGenerationsV1['app-studio'], currentGeneration)

  // A late browser event from the detached v1 script cannot settle or replace
  // the current v2 record.
  staleOnload()
  doc.appended[1].onload()
  await next

  assert.strictEqual(loadProviderScript('app-studio', '2', doc), next)
  invalidateProviderScript('app-studio', '1', doc)
  assert.equal(doc.defaultView.__railgridProviderBootstrapGenerationsV1['app-studio'], currentGeneration)
  assert.equal(doc.getElementById('railgrid-provider-script-app-studio'), doc.appended[1])
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
  assert.equal(doc.getElementById('railgrid-provider-script-quickstart'), doc.appended[0])
})

test('keeps bootstrap generation tokens unique across loader module re-evaluation', async () => {
  const doc = providerDocument()
  const first = loadProviderScript('app-studio', 'hmr-1', doc)
  await Promise.resolve()
  const staleScript = doc.appended[0]
  const staleOnload = staleScript.onload
  const staleGeneration = staleScript.dataset.railgridProviderBootstrapGeneration

  // A Vite HMR update re-evaluates this module but retains the browser window
  // and already-prepared classic scripts. A window-owned counter must prevent
  // the fresh module instance from reissuing the old script's token, while the
  // shared load record lets it cancel the superseded request.
  const reloaded = await vite.ssrLoadModule('/src/providers/providerScriptLoader.ts?hmr-generation-test')
  const next = reloaded.loadProviderScript('app-studio', 'hmr-2', doc)
  await assert.rejects(first, /superseded provider "app-studio" version hmr-1/)
  await new Promise((resolve) => setImmediate(resolve))
  const currentScript = doc.appended[1]
  const currentGeneration = currentScript.dataset.railgridProviderBootstrapGeneration

  assert.notEqual(currentGeneration, staleGeneration)
  assert.equal(doc.defaultView.__railgridProviderBootstrapGenerationsV1['app-studio'], currentGeneration)

  currentScript.onload()
  await next
  staleOnload()
  assert.equal(doc.defaultView.__railgridProviderBootstrapGenerationsV1['app-studio'], currentGeneration)
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
  const failedGeneration = doc.appended[0].dataset.railgridProviderBootstrapGeneration
  await assert.rejects(
    load,
    /timed out loading \/ui\/providers\/app-studio\/main\.js\?v=stalled/,
  )
  assert.equal(doc.getElementById('railgrid-provider-script-app-studio'), null)
  assert.notEqual(doc.defaultView.__railgridProviderBootstrapGenerationsV1['app-studio'], failedGeneration)
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
  const invalidatedGeneration = doc.appended[0].dataset.railgridProviderBootstrapGeneration
  doc.appended[0].onload()
  await first

  assert.strictEqual(loadProviderScript('app-studio', '3', doc), first)
  invalidateProviderScript('app-studio', '3', doc)
  assert.equal(doc.getElementById('railgrid-provider-script-app-studio'), null)
  assert.notEqual(doc.defaultView.__railgridProviderBootstrapGenerationsV1['app-studio'], invalidatedGeneration)

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

test('loads an org-owned bundle from the granted URL the hub returned', async () => {
  const doc = providerDocument()
  const integrity = 'sha384-OLBgp1GsljhM2TJ+sbHjaiH9txEUvgdDTAzHv2P24donTt6/529l+9Ua0vFImLlb'
  const src = '/ui/providers/infrastructure/main.js?v=v0.1.20&grant=fpui_sealed'
  const load = loadProviderScript('infrastructure', 'v0.1.20', doc, 15_000, { integrity, src })
  await Promise.resolve()
  assert.equal(doc.appended.length, 1)
  const script = doc.appended[0]
  // The grant rides in the URL; the loader must use it verbatim rather than
  // rebuilding the platform path from name and version.
  assert.equal(script.src, src)
  assert.equal(script.integrity, integrity)
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

// A provider bundle can be rebuilt without its catalog version changing (every
// Tilt rebuild), which leaves an open page holding a pin the browser refuses.
// The hub re-pins from the bundle it actually served, so one refresh-and-retry
// recovers the page without a reload — but exactly one, and only on a pin that
// actually changed.
test('retries once with the pin the hub corrected after a pinned load fails', async () => {
  const doc = providerDocument()
  const stale = 'sha384-OLBgp1GsljhM2TJ+sbHjaiH9txEUvgdDTAzHv2P24donTt6/529l+9Ua0vFImLlb'
  const corrected = 'sha384-VbxVaw3bZ6ZS8Z4JtxWYAC0Gau6EYGwPRC5rpGXSlbBhnUSHtlL0KAOMRiHSZ5gF'
  let refreshes = 0
  const load = loadProviderScript('quickstart', '9', doc, 15_000, {
    integrity: stale,
    refreshIntegrity: async () => {
      refreshes += 1
      return corrected
    },
  })
  await Promise.resolve()
  assert.equal(doc.appended.length, 1)
  assert.equal(doc.appended[0].integrity, stale)
  const staleGeneration = doc.appended[0].dataset.railgridProviderBootstrapGeneration

  // The browser refuses the bundle: "Failed to find a valid digest in the
  // 'integrity' attribute" surfaces as an ordinary load error.
  doc.appended[0].onerror()
  await new Promise((resolve) => setImmediate(resolve))

  assert.equal(refreshes, 1)
  assert.equal(doc.appended.length, 2)
  const retry = doc.appended[1]
  assert.equal(retry.integrity, corrected)
  assert.equal(retry.src, '/ui/providers/quickstart/main.js?v=9')
  assert.equal(retry.crossOrigin, undefined)
  // The retry must carry a live bootstrap generation; the failed attempt
  // revoked its own, and a generation-aware bundle checks it before installing.
  assert.notEqual(retry.dataset.railgridProviderBootstrapGeneration, staleGeneration)
  assert.equal(
    doc.defaultView.__railgridProviderBootstrapGenerationsV1.quickstart,
    retry.dataset.railgridProviderBootstrapGeneration,
  )

  retry.onload()
  await load

  // One retry only: a second failure is terminal.
  assert.equal(refreshes, 1)
  assert.equal(doc.appended.length, 2)
})

test('does not retry when the refreshed pin is unchanged', async () => {
  const doc = providerDocument()
  const integrity = 'sha384-OLBgp1GsljhM2TJ+sbHjaiH9txEUvgdDTAzHv2P24donTt6/529l+9Ua0vFImLlb'
  let refreshes = 0
  const load = loadProviderScript('quickstart', '10', doc, 15_000, {
    integrity,
    refreshIntegrity: async () => {
      refreshes += 1
      return integrity
    },
  })
  await Promise.resolve()
  doc.appended[0].onerror()

  // Reinjecting the same pin would fail identically; the original failure is
  // what the consumer must see.
  await assert.rejects(load, /failed to load \/ui\/providers\/quickstart\/main\.js\?v=10/)
  assert.equal(refreshes, 1)
  assert.equal(doc.appended.length, 1)
})

test('does not retry when the pin refresh fails or returns nothing', async () => {
  const integrity = 'sha384-OLBgp1GsljhM2TJ+sbHjaiH9txEUvgdDTAzHv2P24donTt6/529l+9Ua0vFImLlb'

  const failing = providerDocument()
  const failed = loadProviderScript('quickstart', '11', failing, 15_000, {
    integrity,
    refreshIntegrity: async () => {
      throw new Error('catalog unreachable')
    },
  })
  await Promise.resolve()
  failing.appended[0].onerror()
  // The catalog read failing tells us nothing about the bundle, so the load
  // stays as terminal as it was and reports its own error, not the refresh's.
  await assert.rejects(failed, /failed to load \/ui\/providers\/quickstart\/main\.js\?v=11/)
  assert.equal(failing.appended.length, 1)

  // An unpinned catalog entry is not a reason to reinject: loading whatever
  // the upstream now serves on no authority would be worse than failing.
  const unpinned = providerDocument()
  const nulled = loadProviderScript('quickstart', '12', unpinned, 15_000, {
    integrity,
    refreshIntegrity: async () => null,
  })
  await Promise.resolve()
  unpinned.appended[0].onerror()
  await assert.rejects(nulled, /failed to load \/ui\/providers\/quickstart\/main\.js\?v=12/)
  assert.equal(unpinned.appended.length, 1)
})

test('never retries an unpinned load', async () => {
  const doc = providerDocument()
  const originalWarn = console.warn
  console.warn = () => {}
  let refreshes = 0
  try {
    const load = loadProviderScript('quickstart', '13', doc, 15_000, {
      refreshIntegrity: async () => {
        refreshes += 1
        return 'sha384-OLBgp1GsljhM2TJ+sbHjaiH9txEUvgdDTAzHv2P24donTt6/529l+9Ua0vFImLlb'
      },
    })
    await Promise.resolve()
    doc.appended[0].onerror()
    // An unpinned script cannot have been refused over its pin, so a pin is
    // not the fix — and adopting one here would silently change what "retry"
    // means for a bundle nobody pinned.
    await assert.rejects(load, /failed to load \/ui\/providers\/quickstart\/main\.js\?v=13/)
  } finally {
    console.warn = originalWarn
  }
  assert.equal(refreshes, 0)
  assert.equal(doc.appended.length, 1)
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
