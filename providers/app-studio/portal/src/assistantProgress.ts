export interface AssistantProgress {
  version: 1
  messages: string[]
  messageSequences: number[]
  workedDurationMs: number
}

interface AssistantWorkedDurationClockState {
  snapshotDurationMs: number
  displayedDurationMs: number
  segmentBaseMs: number
  observedAtMs: number
  ticking: boolean
}

export interface AssistantWorkedDurationObservation {
  messageID: string
  snapshotDurationMs: number
  nowMs: number
  ticking: boolean
  terminal: boolean
}

/**
 * Advances a persisted worked-duration snapshot between server updates without
 * counting permission/input pauses or carrying client estimates into the
 * terminal value. A changed snapshot is authoritative and starts a new local
 * timing segment, which prevents reconnects from double-counting elapsed time.
 */
export class AssistantWorkedDurationClock {
  private readonly states = new Map<string, AssistantWorkedDurationClockState>()

  observe(observation: AssistantWorkedDurationObservation): number {
    const snapshotDurationMs = Math.max(0, observation.snapshotDurationMs)
    const nowMs = Number.isFinite(observation.nowMs) ? observation.nowMs : Date.now()
    if (observation.terminal) {
      this.states.delete(observation.messageID)
      return snapshotDurationMs
    }

    let state = this.states.get(observation.messageID)
    if (!state) {
      state = {
        snapshotDurationMs,
        displayedDurationMs: snapshotDurationMs,
        segmentBaseMs: snapshotDurationMs,
        observedAtMs: nowMs,
        ticking: observation.ticking,
      }
      this.states.set(observation.messageID, state)
      return state.displayedDurationMs
    }

    if (state.ticking) {
      state.displayedDurationMs = state.segmentBaseMs + Math.max(0, nowMs - state.observedAtMs)
    }
    if (state.snapshotDurationMs !== snapshotDurationMs) {
      state.snapshotDurationMs = snapshotDurationMs
      state.displayedDurationMs = snapshotDurationMs
      state.segmentBaseMs = snapshotDurationMs
      state.observedAtMs = nowMs
    } else if (observation.ticking !== state.ticking) {
      state.segmentBaseMs = state.displayedDurationMs
      state.observedAtMs = nowMs
    }
    state.ticking = observation.ticking
    return state.displayedDurationMs
  }

  clear(): void {
    this.states.clear()
  }
}

const MAX_MESSAGES = 32
const MAX_MESSAGE_BYTES = 600
const MAX_DURATION_MS = 7 * 24 * 60 * 60 * 1000

export function parseAssistantProgress(value: unknown): AssistantProgress | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const item = value as Record<string, unknown>
  if (Object.keys(item).some((key) => !['version', 'messages', 'messageSequences', 'workedDurationMs'].includes(key))) return undefined
  const rawMessages = item.messages === null ? [] : item.messages
  if (item.version !== 1 || !Array.isArray(rawMessages)) return undefined
  if (rawMessages.length > MAX_MESSAGES) return undefined
  if (!Number.isInteger(item.workedDurationMs) || (item.workedDurationMs as number) < 0 || (item.workedDurationMs as number) > MAX_DURATION_MS) return undefined

  const messages: string[] = []
  for (const message of rawMessages) {
    if (typeof message !== 'string' || !message || message !== message.trim()) return undefined
    if (new TextEncoder().encode(message).length > MAX_MESSAGE_BYTES || /[\u0000-\u001f\u007f-\u009f]/u.test(message)) return undefined
    messages.push(message)
  }
  if (!Array.isArray(item.messageSequences) || item.messageSequences.length !== messages.length) return undefined
  let previous = 0
  const messageSequences: number[] = []
  for (const sequence of item.messageSequences) {
    if (!Number.isSafeInteger(sequence) || Number(sequence) < 1 || Number(sequence) > 10_000) return undefined
    if (Number(sequence) <= previous) return undefined
    previous = Number(sequence)
    messageSequences.push(Number(sequence))
  }
  return {
    version: 1,
    messages,
    messageSequences,
    workedDurationMs: item.workedDurationMs as number,
  }
}

export function formatAssistantWorkedDuration(durationMs: number): string {
  const totalSeconds = Math.max(1, Math.round(durationMs / 1000))
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return `${hours}h ${minutes}m`
  if (minutes > 0) return `${minutes}m ${seconds}s`
  return `${seconds}s`
}
