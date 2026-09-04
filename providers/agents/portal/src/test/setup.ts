// jsdom shims for browser APIs the components touch. Kept to the minimum —
// anything stubbed here is something a test can't assert on.

import { afterEach, vi } from 'vitest'
import { unmountVueApps } from './vue-helper'

// Some Node 25 installations expose an incomplete experimental localStorage
// on both globalThis and jsdom's window. Tests need the browser contract, so
// supply a deterministic in-memory fallback whenever that object is unusable.
function memoryStorage(): Storage {
  const values = new Map<string, string>()
  return {
    get length() { return values.size },
    clear: () => values.clear(),
    getItem: key => values.get(key) ?? null,
    key: index => [...values.keys()][index] ?? null,
    removeItem: key => { values.delete(key) },
    setItem: (key, value) => { values.set(key, String(value)) },
  }
}
const browserStorage = typeof window.localStorage?.clear === 'function' ? window.localStorage : memoryStorage()
vi.stubGlobal('localStorage', browserStorage)
Object.defineProperty(window, 'localStorage', { value: browserStorage, configurable: true })

if (!globalThis.crypto) Object.defineProperty(globalThis, 'crypto', { value: {}, writable: true })
if (!globalThis.crypto.randomUUID) {
  Object.defineProperty(globalThis.crypto, 'randomUUID', {
    value: () => `test-${Math.random().toString(36).slice(2, 10)}-0000-0000-0000-000000000000`,
    writable: true,
  })
}

if (!navigator.clipboard) {
  Object.defineProperty(navigator, 'clipboard', { value: { writeText: () => Promise.resolve() }, writable: true })
}

// The chat batches deltas onto animation frames; running them as microtasks
// keeps tests deterministic without sprinkling raf waits everywhere.
vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
  const id = setTimeout(() => cb(performance.now()), 0)
  return id as unknown as number
})
vi.stubGlobal('cancelAnimationFrame', (id: number) => clearTimeout(id))

if (!HTMLElement.prototype.scrollIntoView) HTMLElement.prototype.scrollIntoView = () => undefined

afterEach(() => {
  unmountVueApps()
  document.body.replaceChildren()
  localStorage.clear()
})
