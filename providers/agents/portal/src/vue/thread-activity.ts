import { computed, shallowRef, watch, type WatchSource } from 'vue'
import type { ApiClient } from '../api'
import type { RunDetail, RunSummary } from '../types'

type RunActivity = Pick<RunSummary, 'id' | 'agent' | 'sessionID'> & { phase: string; pending?: RunDetail['pending'] }
const isTerminal = (phase: string) => ['succeeded', 'completed', 'failed', 'aborted', 'interrupted', 'cancelled', 'canceled'].includes(phase.toLowerCase())
const activityPhase = (run: RunActivity) => !isTerminal(run.phase) && run.pending ? 'PendingApproval' : run.phase

/** Conversation-wide activity survives selection changes; it never owns execution. */
export function useThreadActivity(api: () => ApiClient, agent: () => string, scope: WatchSource[]) {
  const runs = shallowRef(new Map<string, RunActivity>())
  const revisions = new Map<string, number>()
  // Events and recovered details are stronger than the lagging run projection.
  const observed = new Map<string, RunActivity>()
  const terminalRuns = new Set<string>()
  let refresh = () => {}
  function record(run: Partial<RunActivity>): void {
    if (!run.id || !run.phase || (run.agent && run.agent !== agent()) || terminalRuns.has(run.id)) return
    revisions.set(run.id, (revisions.get(run.id) || 0) + 1)
    if (isTerminal(run.phase)) {
      terminalRuns.add(run.id)
      observed.delete(run.id)
      const next = new Map(runs.value)
      next.delete(run.id)
      runs.value = next
      return
    }
    const previous = runs.value.get(run.id)
    if (!previous && (!run.sessionID || run.agent !== agent())) { refresh(); return }
    const activity = { id: run.id, agent: agent(), sessionID: run.sessionID || previous?.sessionID, phase: activityPhase(run as RunActivity) }
    observed.set(run.id, activity)
    runs.value = new Map(runs.value).set(run.id, activity)
  }
  watch([api, agent, ...scope], (_, __, onCleanup) => {
    const currentAPI = api()
    const currentAgent = agent()
    let active = true
    let reading = false
    let queued = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const cancelReads = new Set<() => void>()
    runs.value = new Map()
    terminalRuns.clear()
    observed.clear()
    revisions.clear()
    // Bound both collection and conflict-confirmation reads; a failed trace
    // preserves the known state rather than treating missing access as resume.
    async function bounded<T>(request: () => Promise<T>): Promise<T> {
      let deadline: ReturnType<typeof setTimeout> | undefined
      let cancel!: () => void
      try {
        const interrupted = new Promise<never>((_, reject) => {
          cancel = () => reject(new Error('Thread activity read cancelled'))
          cancelReads.add(cancel)
          deadline = setTimeout(cancel, 15000)
        })
        return await Promise.race([request(), interrupted])
      } finally {
        clearTimeout(deadline)
        cancelReads.delete(cancel)
      }
    }
    const poll = async () => {
      if (!active) return
      if (reading) { queued = true; return }
      clearTimeout(timer)
      reading = true
      const serials = new Map(revisions)
      const unchanged = (id: string) => active && revisions.get(id) === serials.get(id)
      try {
        const page = await bounded(() => currentAPI.listRuns({ agent: currentAgent }))
        if (!active) return
        const snapshot = new Map(page.items.filter(run => run.agent === currentAgent && run.sessionID).map(run => [run.id, run]))
        const next = new Map(runs.value)
        for (const id of next.keys()) {
          if (unchanged(id) && !observed.has(id) && !snapshot.has(id)) next.delete(id)
        }
        for (const [id, run] of snapshot) {
          if (!unchanged(id) || terminalRuns.has(id)) continue
          if (isTerminal(run.phase)) {
            terminalRuns.add(id)
            observed.delete(id)
            next.delete(id)
          } else if (!observed.has(id)) next.set(id, run)
        }
        runs.value = next
        // A collection has no approval checkpoint or ordering token. Refresh
        // observed runs through the trace, even when the projection agrees:
        // a checkpoint can change before its projected phase or event arrives.
        await Promise.all([...observed].map(async ([id, known]) => {
          if (!unchanged(id)) return
          try {
            const detail = await bounded(() => currentAPI.getRun(id, { requireTrace: true }))
            if (!unchanged(id) || detail.id !== id || detail.agent !== currentAgent || detail.sessionID !== known.sessionID) return
            // The object phase can still lag an accepted resume. With no
            // checkpoint, PendingApproval cannot undo known Running activity.
            if (detail.phase === 'PendingApproval' && !detail.pending && known.phase === 'Running') return
            record(detail)
          } catch { /* Retain the last confirmed activity. */ }
        }))
      } catch {
        // Keep the last activity snapshot; the conversation owns error notices.
      } finally {
        reading = false
        if (active) {
          const delay = queued ? 0 : 10000
          queued = false
          timer = setTimeout(poll, delay)
        }
      }
    }
    refresh = () => { void poll() }
    void poll()
    onCleanup(() => {
      active = false
      clearTimeout(timer)
      for (const cancel of cancelReads) cancel()
    })
  }, { immediate: true })
  const runningSessions = computed(() => new Set([...runs.value.values()]
    .filter(run => ['running', 'pending'].includes(run.phase.toLowerCase()))
    .map(run => run.sessionID)))
  return { runningSessions, record }
}
