import { computed, shallowRef, watch, type WatchSource } from 'vue'
import type { ApiClient } from '../api'
import type { RunSummary } from '../types'

type RunActivity = Pick<RunSummary, 'id' | 'agent' | 'sessionID'> & { phase: string }

/** Conversation-wide activity survives selection changes; it never owns execution. */
export function useThreadActivity(api: () => ApiClient, agent: () => string, scope: WatchSource[]) {
  const runs = shallowRef(new Map<string, RunActivity>())
  let revision = 0
  const terminalRuns = new Set<string>()
  let refresh = () => {}
  function record(run: Partial<RunActivity>): void {
    if (!run.id || !run.phase || (run.agent && run.agent !== agent())) return
    if (terminalRuns.has(run.id)) return
    if (['succeeded', 'completed', 'failed', 'aborted', 'interrupted', 'cancelled', 'canceled'].includes(run.phase.toLowerCase())) terminalRuns.add(run.id)
    const previous = runs.value.get(run.id)
    if (!previous && (!run.sessionID || run.agent !== agent())) { refresh(); return }
    revision += 1
    runs.value = new Map(runs.value).set(run.id, { id: run.id, agent: agent(), sessionID: run.sessionID || previous?.sessionID, phase: run.phase })
  }
  watch([api, agent, ...scope], (_, __, onCleanup) => {
    // Capture typed getters rather than the heterogeneous watch tuple.
    const currentAPI = api()
    const currentAgent = agent()
    let active = true
    let reading = false
    let queued = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let cancelRead: (() => void) | undefined
    runs.value = new Map()
    terminalRuns.clear()
    revision += 1
    const poll = async () => {
      if (!active) return
      if (reading) { queued = true; return }
      clearTimeout(timer)
      reading = true
      const serial = revision
      let deadline: ReturnType<typeof setTimeout> | undefined
      try {
        const interrupted = new Promise<never>((_, reject) => {
          cancelRead = () => reject(new Error('Thread activity read cancelled'))
          deadline = setTimeout(() => reject(new Error('Thread activity read timed out')), 15000)
        })
        const page = await Promise.race([currentAPI.listRuns({ agent: currentAgent }), interrupted])
        if (active && revision === serial) {
          runs.value = new Map(page.items.filter(run => run.agent === currentAgent && run.sessionID && !terminalRuns.has(run.id)).map(run => [run.id, run]))
        }
      } catch {
        // Keep the last activity snapshot; the conversation owns error notices.
      } finally {
        clearTimeout(deadline)
        cancelRead = undefined
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
      cancelRead?.()
    })
  }, { immediate: true })
  const runningSessions = computed(() => new Set([...runs.value.values()]
    .filter(run => ['running', 'pending'].includes(run.phase.toLowerCase()))
    .map(run => run.sessionID)))
  return { runningSessions, record }
}
