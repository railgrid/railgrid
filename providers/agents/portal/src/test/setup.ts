// jsdom shims for browser APIs the components touch. Kept to the minimum —
// anything stubbed here is something a test can't assert on.

import { afterEach, vi } from 'vitest'

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

afterEach(() => {
  document.body.replaceChildren()
  localStorage.clear()
})
