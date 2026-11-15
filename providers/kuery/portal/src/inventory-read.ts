/** Coalesce pending reads; after the abort grace, an unresponsive read can overlap its replacement. */
export function createInventoryReadCoordinator(
  task: (isCurrent: () => boolean) => Promise<void>,
  setBusy: (busy: boolean) => void,
  abortActive: () => void,
  abortGraceMs = 1000,
) {
  let generation = 0
  let active = false
  let queued = false
  let stopped = false
  let releaseInvalidated: (() => void) | null = null
  let abortTimer: ReturnType<typeof setTimeout> | null = null

  function abort(): void {
    if (!active) return
    abortActive()
    // A transport may ignore AbortSignal. Give it time to settle before
    // allowing a fenced replacement to proceed.
    if (!abortTimer && releaseInvalidated) {
      abortTimer = setTimeout(() => { abortTimer = null; releaseInvalidated?.() }, abortGraceMs)
    }
  }

  function clearAbortTimer(): void {
    if (abortTimer) clearTimeout(abortTimer)
    abortTimer = null
  }

  async function drain(): Promise<void> {
    if (active || stopped) return
    active = true
    try {
      while (queued && !stopped) {
        queued = false
        const current = generation
        const invalidated = new Promise<void>(resolve => { releaseInvalidated = resolve })
        try {
          await Promise.race([task(() => !stopped && generation === current), invalidated])
        } catch {
          // The task owns its user-facing error state; keep the queue usable.
        } finally {
          clearAbortTimer()
          releaseInvalidated = null
        }
      }
    } finally {
      active = false
      if (!stopped) setBusy(false)
    }
  }

  return {
    request(invalidate = false): void {
      if (stopped) return
      if (invalidate) {
        generation += 1
        abort()
      }
      queued = true
      setBusy(true)
      void drain()
    },
    invalidate(): void {
      generation += 1
      queued = false
      abort()
      if (!active) setBusy(false)
    },
    stop(): void {
      stopped = true
      generation += 1
      queued = false
      abort()
      clearAbortTimer()
      releaseInvalidated?.()
    },
  }
}
