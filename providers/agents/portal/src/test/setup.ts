// jsdom shims for browser APIs the components touch. Kept to the minimum —
// anything stubbed here is something a test can't assert on.

import { afterEach, vi } from 'vitest'
import { nextTick } from 'vue'
import { clearToasts } from '../ui/toast'
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

// Track frame timers so Vue transition callbacks cannot outlive the jsdom
// environment. Unmounting alone does not cancel Vue's double-frame callbacks.
const pendingFrames = new Set<ReturnType<typeof setTimeout>>()
vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
  const id = setTimeout(() => {
    pendingFrames.delete(id)
    cb(performance.now())
  }, 0)
  pendingFrames.add(id)
  return id as unknown as number
})
vi.stubGlobal('cancelAnimationFrame', (id: number) => {
  clearTimeout(id)
  pendingFrames.delete(id as unknown as ReturnType<typeof setTimeout>)
})

if (!HTMLElement.prototype.scrollIntoView) HTMLElement.prototype.scrollIntoView = () => undefined

afterEach(async () => {
  unmountVueApps()
  // Toasts own real auto-dismiss timers. Clear them before the jsdom document
  // is replaced so a callback cannot run after the test environment is torn
  // down and dereference an unavailable document.
  clearToasts()
  await nextTick()
  for (const id of pendingFrames) clearTimeout(id)
  pendingFrames.clear()
  document.body.replaceChildren()
  localStorage.clear()
})
