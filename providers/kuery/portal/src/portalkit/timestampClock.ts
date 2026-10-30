// CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
// under providers/*/portal/src/portalkit/; edit here and run
// `make sync-portalkit`.

import { onBeforeUnmount, onMounted, readonly, ref, type Ref } from 'vue'

const CLOCK_INTERVAL_MS = 1_000
const now = ref(Date.now())
const readonlyNow: Readonly<Ref<number>> = readonly(now)

let subscribers = 0
let intervalHandle: ReturnType<typeof setInterval> | undefined

function refresh(): void {
  now.value = Date.now()
}

function start(): void {
  if (intervalHandle !== undefined || typeof globalThis.setInterval !== 'function') return
  intervalHandle = globalThis.setInterval(refresh, CLOCK_INTERVAL_MS)
}

function stop(): void {
  if (intervalHandle === undefined) return
  globalThis.clearInterval(intervalHandle)
  intervalHandle = undefined
}

/**
 * Return the shared wall clock used by mounted timestamp components. Importing
 * or rendering during SSR starts no timer; Vue mount hooks own its lifecycle.
 */
export function useReactiveTimestampNow(): Readonly<Ref<number>> {
  let subscribed = false

  onMounted(() => {
    if (subscribed) return
    subscribed = true
    subscribers += 1
    refresh()
    if (subscribers === 1) start()
  })

  onBeforeUnmount(() => {
    if (!subscribed) return
    subscribed = false
    subscribers -= 1
    if (subscribers === 0) stop()
  })

  return readonlyNow
}
