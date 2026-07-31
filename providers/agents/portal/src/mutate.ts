// One mutation helper, replacing the 345-line actions.ts of near-identical
// try/notify/reload wrappers.
//
// It owns the whole write lifecycle: optimistic local edit → request → toast →
// slice refresh, rolling the optimistic edit back when the request fails. Views
// describe *what* they want changed; the mechanics live here.

import type { AppStore, SliceKey } from './store'
import { toast } from './ui/toast'

export interface MutateOptions<T> {
  // run performs the write. Its resolved value is returned to the caller.
  run: () => Promise<T>
  // success message; omit for silent success (e.g. a toggle that is its own
  // feedback).
  success?: string
  // failure prefixes the server's message, e.g. "Save failed".
  failure: string
  // optimistic applies the change locally before the request so the UI reacts
  // immediately; rollback undoes it if the request fails. Both must be
  // pure-local (they never call the API).
  optimistic?: () => void
  rollback?: () => void
  // reload names the store slices to refresh once the write lands, so the
  // authoritative server state replaces the optimistic guess.
  reload?: SliceKey[]
  // action attaches a follow-up button to the success toast (e.g. "View run").
  action?: { label: string; run: () => void }
}

// mutate returns the write's result, or undefined if it failed (the error is
// already surfaced as a toast — callers only need the happy path).
export async function mutate<T>(store: AppStore, opts: MutateOptions<T>): Promise<T | undefined> {
  opts.optimistic?.()
  if (opts.optimistic) store.dispatchEvent(new Event('change'))
  try {
    const res = await opts.run()
    if (opts.success) toast('ok', opts.success, opts.action)
    for (const key of opts.reload || []) void store.load(key)
    return res
  } catch (e) {
    opts.rollback?.()
    if (opts.rollback) store.dispatchEvent(new Event('change'))
    toast('error', `${opts.failure}: ${(e as Error).message}`)
    // Re-sync from the server: a rejected write may still have partially
    // applied (e.g. connection spec written, secret not).
    for (const key of opts.reload || []) void store.load(key)
    return undefined
  }
}
