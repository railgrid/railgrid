// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Framework-neutral read-state shape for resource pages. The Vue adapter
// consumes this contract directly; the plain-TS adapter can use it without
// importing Vue.

/** A resource read may either replace an empty body with visible progress or keep it stable while refreshing. */
export type ResourceRefreshMode = 'foreground' | 'background'

/**
 * The state of an authoritative resource read.
 *
 * `loaded` is nullable so older callers can omit the explicit first-read
 * contract. Once supplied, false always means that no authoritative snapshot
 * exists yet; true means cached content may remain visible during refresh.
 */
export interface ResourceReadState {
  loaded?: boolean | null
  loading?: boolean
  refreshMode?: ResourceRefreshMode
  error?: string | null
  stale?: boolean
  retryable?: boolean
}
