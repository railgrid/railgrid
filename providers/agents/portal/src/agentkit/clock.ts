// CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
// under providers/*/portal/src/agentkit/; edit here and run
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
 * Return the shared wall clock used by mounted presentation components.
 *
 * The clock is intentionally started from a lifecycle hook: importing or
 * rendering AgentKit during SSR never creates a browser timer. Each consumer
 * refreshes the value on mount, while all consumers share one interval.
 */
export function useReactiveNow(): Readonly<Ref<number>> {
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
