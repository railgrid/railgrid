// CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies.

import { computed, shallowRef, watch } from 'vue'
import { useReactiveNow } from './clock'
import type { AITurnProgressStatus } from './conversation'

interface TurnWorkDuration {
  id: string
  status: AITurnProgressStatus
  measuredMs?: number
}

interface WorkEstimate {
  baseMs: number
  runningSinceMs?: number
}

function estimateAt(estimate: WorkEstimate, nowMs: number): number {
  return estimate.baseMs + Math.max(0, nowMs - (estimate.runningSinceMs ?? nowMs))
}

/** Estimate active work between measurements. Terminal durations come from the server. */
export function useTurnWorkDuration(readTurn: () => TurnWorkDuration | undefined) {
  const turn = computed(readTurn)
  const measured = computed(() => {
    const value = turn.value?.measuredMs
    return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : undefined
  })
  const now = useReactiveNow(() => turn.value?.status === 'running')
  const estimate = shallowRef<WorkEstimate>()

  watch([() => turn.value?.id, () => turn.value?.status, measured], ([id, status, measuredMs], previousValues) => {
    if (!id || (status !== 'running' && status !== 'waiting' && status !== 'stopping')) {
      estimate.value = undefined
      return
    }
    const nowMs = Date.now()
    const previous = id === previousValues?.[0] ? estimate.value : undefined
    // New measurements may include work already counted by the live estimate.
    estimate.value = {
      baseMs: Math.max(previous ? estimateAt(previous, nowMs) : 0, measuredMs ?? 0),
      runningSinceMs: status === 'running' ? nowMs : undefined,
    }
  }, { immediate: true })

  return computed(() => {
    const status = turn.value?.status
    switch (status) {
      case 'pending':
      case 'waiting':
        return undefined
      case 'running':
      case 'stopping': {
        if (!estimate.value) return measured.value
        const liveMs = status === 'running' ? estimateAt(estimate.value, now.value) : estimate.value.baseMs
        return measured.value === undefined && liveMs < 1_000 ? undefined : liveMs
      }
      default:
        return measured.value
    }
  })
}
