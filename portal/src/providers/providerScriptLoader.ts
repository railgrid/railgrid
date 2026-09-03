interface ProviderScriptLoad {
  version: string
  bootstrapGeneration: string
  promise: Promise<void>
  cancel: (reason?: Error) => void
}

const PROVIDER_SCRIPT_LOAD_TIMEOUT_MS = 15_000
const PROVIDER_SCRIPT_LOADS_KEY = '__farosProviderScriptLoadsV1'
const PROVIDER_BOOTSTRAP_GENERATIONS_KEY = '__farosProviderBootstrapGenerationsV1'
const PROVIDER_BOOTSTRAP_GENERATION_COUNTER_KEY = '__farosProviderBootstrapGenerationCounterV1'
// App Studio keeps stable custom-element wrappers and replaces only their lazy
// loaders, so its bootstrap can safely be superseded in-place. Other providers
// register immutable custom-element classes directly. Loading two versions of
// those bootstraps in one document creates an unfixable race: a detached older
// classic script may still execute and claim the tag before the newer bundle.
const HOT_RELOADABLE_PROVIDER_BOOTSTRAPS = new Set(['app-studio'])

export class ProviderPageReloadRequiredError extends Error {
  readonly code = 'PROVIDER_PAGE_RELOAD_REQUIRED'

  constructor(name: string, loadedVersion: string, requestedVersion: string) {
    super(
      `provider "${name}" version changed from ${loadedVersion} to ${requestedVersion}; reload the page to use the new version`,
    )
    this.name = 'ProviderPageReloadRequiredError'
  }
}

export function canReloadProviderScriptInDocument(name: string): boolean {
  return HOT_RELOADABLE_PROVIDER_BOOTSTRAPS.has(name)
}

interface ProviderScriptAttempt {
  promise: Promise<void>
  cancel: (reason?: Error) => void
}

type ProviderScriptStateRoot = typeof globalThis & {
  [PROVIDER_SCRIPT_LOADS_KEY]?: Map<string, ProviderScriptLoad>
  [PROVIDER_BOOTSTRAP_GENERATIONS_KEY]?: Record<string, string>
  [PROVIDER_BOOTSTRAP_GENERATION_COUNTER_KEY]?: string | number
}

function providerScriptStateRoot(doc: Document): ProviderScriptStateRoot {
  return (doc.defaultView ?? globalThis) as ProviderScriptStateRoot
}

function documentLoads(doc: Document): Map<string, ProviderScriptLoad> {
  const root = providerScriptStateRoot(doc)
  return root[PROVIDER_SCRIPT_LOADS_KEY] ??= new Map<string, ProviderScriptLoad>()
}

function nextProviderBootstrapGeneration(doc: Document): string {
  const root = providerScriptStateRoot(doc)
  const stored = root[PROVIDER_BOOTSTRAP_GENERATION_COUNTER_KEY]
  let current = 0n
  if (typeof stored === 'string' && /^\d+$/.test(stored)) {
    current = BigInt(stored)
  } else if (typeof stored === 'number' && Number.isSafeInteger(stored) && stored >= 0) {
    current = BigInt(stored)
  }
  const generation = String(current + 1n)
  root[PROVIDER_BOOTSTRAP_GENERATION_COUNTER_KEY] = generation
  return generation
}

function claimProviderBootstrapGeneration(doc: Document, name: string): string {
  const root = providerScriptStateRoot(doc)
  const generations = root[PROVIDER_BOOTSTRAP_GENERATIONS_KEY] ??= Object.create(null) as Record<string, string>
  const generation = nextProviderBootstrapGeneration(doc)
  generations[name] = generation
  return generation
}

function revokeProviderBootstrapGeneration(doc: Document, name: string, generation: string): void {
  const root = providerScriptStateRoot(doc)
  const generations = root[PROVIDER_BOOTSTRAP_GENERATIONS_KEY]
  if (generations?.[name] !== generation) return
  generations[name] = nextProviderBootstrapGeneration(doc)
}

function injectProviderScript(
  doc: Document,
  name: string,
  version: string,
  bootstrapGeneration: string,
  timeoutMs: number,
): ProviderScriptAttempt {
  const scriptID = `faros-provider-script-${name}`
  const current = doc.getElementById(scriptID) as HTMLScriptElement | null
  if (
    current?.dataset.farosProviderVersion === version &&
    current.dataset.farosProviderLoadState === 'loaded'
  ) {
    return { promise: Promise.resolve(), cancel: () => {} }
  }

  current?.remove()
  const src = `/ui/providers/${name}/main.js?v=${encodeURIComponent(version)}`
  let cancel = (_reason?: Error) => {}
  const promise = new Promise<void>((resolve, reject) => {
    const script = doc.createElement('script')
    let settled = false
    let timeoutID: ReturnType<typeof setTimeout> | undefined

    const finish = (error?: Error) => {
      if (settled) return
      settled = true
      if (timeoutID !== undefined) clearTimeout(timeoutID)
      script.onload = null
      script.onerror = null
      if (error) {
        revokeProviderBootstrapGeneration(doc, name, bootstrapGeneration)
        script.remove()
        reject(error)
      } else {
        script.dataset.farosProviderLoadState = 'loaded'
        resolve()
      }
    }

    script.id = scriptID
    script.src = src
    script.async = true
    script.dataset.farosProviderVersion = version
    // Provider bootstraps with mutable global side effects must verify this
    // host-issued generation before installing them. Removing a prepared
    // classic script does not guarantee its body will not execute later.
    script.dataset.farosProviderBootstrapGeneration = bootstrapGeneration
    script.dataset.farosProviderLoadState = 'loading'
    script.onload = () => finish()
    script.onerror = () => finish(new Error(`failed to load ${src}`))
    cancel = (reason = new Error(`cancelled loading ${src}`)) => finish(reason)
    timeoutID = setTimeout(
      () => finish(new Error(`timed out loading ${src}`)),
      timeoutMs,
    )
    doc.head.appendChild(script)
  })
  return { promise, cancel: (reason?: Error) => cancel(reason) }
}

/**
 * Load one provider bootstrap for both page and dashboard consumers.
 * Same-version consumers always share one promise. App Studio's generation-
 * aware retained wrapper may supersede an older deployment in place; direct-
 * registration providers keep their first bootstrap for the document because
 * their immutable custom-element classes require a page reload to upgrade.
 */
export function loadProviderScript(
  name: string,
  version: string | undefined,
  doc: Document = document,
  timeoutMs: number = PROVIDER_SCRIPT_LOAD_TIMEOUT_MS,
): Promise<void> {
  const requestedVersion = version ?? '0'
  const loads = documentLoads(doc)
  const current = loads.get(name)
  if (current?.version === requestedVersion) return current.promise
  if (current && !canReloadProviderScriptInDocument(name)) {
    // A direct-registration provider cannot replace its custom-element class
    // without reloading the document. Reject this different catalog version
    // explicitly: resolving with the existing promise would falsely report the
    // requested bundle as loaded and mount the stale element against new APIs.
    return Promise.reject(
      new ProviderPageReloadRequiredError(name, current.version, requestedVersion),
    )
  }

  // Only generation-aware providers reach this supersede path. Claim before
  // cancelling the old request so a prepared classic script that executes late
  // observes itself as stale before touching the retained loader registry.
  const bootstrapGeneration = claimProviderBootstrapGeneration(doc, name)
  // A catalog version supersedes the previous request. Cancelling it removes
  // the obsolete script and settles its promise immediately, so a stalled
  // network request cannot hold every newer version behind it.
  current?.cancel(new Error(`superseded provider "${name}" version ${current.version}`))
  const predecessor = current?.promise.catch(() => undefined) ?? Promise.resolve()
  let attempt: ProviderScriptAttempt | undefined
  let cancelled: Error | undefined
  const record: ProviderScriptLoad = {
    version: requestedVersion,
    bootstrapGeneration,
    promise: predecessor.then(() => {
      if (cancelled) throw cancelled
      attempt = injectProviderScript(doc, name, requestedVersion, bootstrapGeneration, timeoutMs)
      return attempt.promise
    }),
    cancel: (reason = new Error(`cancelled provider "${name}" version ${requestedVersion}`)) => {
      cancelled = reason
      attempt?.cancel(reason)
    },
  }
  loads.set(name, record)
  void record.promise.catch(() => {
    // A detached classic script may still execute after cancellation or
    // timeout. Only a generation-aware provider can safely start another
    // bootstrap in this document; direct-registration providers retain this
    // terminal attempt and recover by reloading the page.
    if (loads.get(name) === record && canReloadProviderScriptInDocument(name)) {
      loads.delete(name)
    }
  })
  return record.promise
}

/**
 * Forget a completed bootstrap after its consumer proves registration failed.
 * A successful network load is not sufficient evidence that bundle execution
 * registered the expected custom element, so retry must be able to reinject
 * the same catalog version.
 */
export function invalidateProviderScript(
  name: string,
  version: string | undefined,
  doc: Document = document,
): void {
  const requestedVersion = version ?? '0'
  const loads = documentLoads(doc)
  const current = loads.get(name)
  if (!canReloadProviderScriptInDocument(name)) return
  if (current?.version === requestedVersion) {
    current.cancel(new Error(`invalidated provider "${name}" version ${requestedVersion}`))
    revokeProviderBootstrapGeneration(doc, name, current.bootstrapGeneration)
    loads.delete(name)
  }

  const script = doc.getElementById(`faros-provider-script-${name}`) as HTMLScriptElement | null
  if (script?.dataset.farosProviderVersion === requestedVersion) {
    const generation = script.dataset.farosProviderBootstrapGeneration
    if (generation) revokeProviderBootstrapGeneration(doc, name, generation)
    script.remove()
  }
}
