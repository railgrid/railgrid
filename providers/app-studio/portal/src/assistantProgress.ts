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

interface AssistantWorkedDurationPersistedState extends AssistantWorkedDurationClockState {
  savedAtMs: number
}

interface AssistantWorkedDurationStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

export interface AssistantWorkedDurationClockOptions {
  /** Session-scoped storage; injectable for deterministic tests. */
  storage?: AssistantWorkedDurationStorage | null
  /** Storage namespace (tenant/project scope is supplied per observation). */
  namespace?: string
  /** Maximum persisted entries retained in one namespace bucket. */
  maxEntries?: number
  /** Maximum age for a persisted active segment. */
  maxAgeMs?: number
}

export interface AssistantWorkedDurationObservation {
  messageID: string
  /** Stable tenant/project scope; prevents state leaking between conversations. */
  scope?: string
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
  private readonly storage?: AssistantWorkedDurationStorage
  private readonly namespace: string
  private readonly maxEntries: number
  private readonly maxAgeMs: number

  constructor(options: AssistantWorkedDurationClockOptions = {}) {
    this.storage = options.storage === undefined ? defaultAssistantWorkedDurationStorage() : options.storage ?? undefined
    this.namespace = normalizeDurationScope(options.namespace || 'default')
    this.maxEntries = Number.isSafeInteger(options.maxEntries) && (options.maxEntries as number) > 0
      ? Math.min(options.maxEntries as number, 128)
      : 64
    this.maxAgeMs = Number.isFinite(options.maxAgeMs) && (options.maxAgeMs as number) > 0
      ? Math.min(options.maxAgeMs as number, MAX_DURATION_MS)
      : MAX_DURATION_MS
  }

  observe(observation: AssistantWorkedDurationObservation): number {
    const snapshotDurationMs = Math.max(0, observation.snapshotDurationMs)
    const nowMs = Number.isFinite(observation.nowMs) ? observation.nowMs : Date.now()
    const stateKey = this.stateKey(observation)
    if (observation.terminal) {
      this.states.delete(stateKey)
      this.removePersistedState(observation, stateKey)
      return snapshotDurationMs
    }

    let state = this.states.get(stateKey)
    if (!state) {
      state = this.restoreState(observation, stateKey, snapshotDurationMs, nowMs)
      this.states.set(stateKey, state)
      this.persistState(observation, stateKey, state, nowMs)
      return state.displayedDurationMs
    }

    const wasTicking = state.ticking
    if (wasTicking) {
      state.displayedDurationMs = state.segmentBaseMs + Math.max(0, nowMs - state.observedAtMs)
    }

    // Non-terminal server snapshots are monotonic hints. Preserve a local
    // estimate that is ahead of a stale/replayed snapshot so reloads and
    // reconnects cannot make the disclosure jump backwards.
    if (snapshotDurationMs > state.snapshotDurationMs) {
      state.snapshotDurationMs = snapshotDurationMs
      state.displayedDurationMs = Math.max(state.displayedDurationMs, snapshotDurationMs)
      state.segmentBaseMs = state.displayedDurationMs
      state.observedAtMs = nowMs
    }
    if (observation.ticking !== wasTicking) {
      state.segmentBaseMs = state.displayedDurationMs
      state.observedAtMs = nowMs
    }
    state.ticking = observation.ticking
    this.persistState(observation, stateKey, state, nowMs)
    return state.displayedDurationMs
  }

  clear(): void {
    this.states.clear()
  }

  private stateKey(observation: AssistantWorkedDurationObservation): string {
    return `${normalizeDurationScope(observation.scope || 'default')}:${normalizeDurationScope(observation.messageID)}`
  }

  private storageKey(scope: string): string {
    return `faros:app-studio:assistant-worked-duration:v1:${encodeDurationScope(this.namespace)}:${encodeDurationScope(scope || 'default')}`
  }

  private restoreState(
    observation: AssistantWorkedDurationObservation,
    stateKey: string,
    snapshotDurationMs: number,
    nowMs: number,
  ): AssistantWorkedDurationClockState {
    const persisted = this.readPersistedState(observation, stateKey, nowMs)
    if (!persisted) {
      return {
        snapshotDurationMs,
        displayedDurationMs: snapshotDurationMs,
        segmentBaseMs: snapshotDurationMs,
        observedAtMs: nowMs,
        ticking: observation.ticking,
      }
    }

    let displayedDurationMs = Math.max(snapshotDurationMs, persisted.displayedDurationMs)
    let segmentBaseMs = persisted.segmentBaseMs
    let observedAtMs = persisted.observedAtMs
    const persistedSnapshot = Math.max(0, persisted.snapshotDurationMs)
    const serverAdvanced = snapshotDurationMs > persistedSnapshot

    // A pending approval/input is a deliberate pause. If the page reloads
    // while paused, do not credit time since the last running observation.
    if (persisted.ticking && observation.ticking) {
      if (serverAdvanced) {
        displayedDurationMs = Math.max(displayedDurationMs, snapshotDurationMs)
        segmentBaseMs = displayedDurationMs
        observedAtMs = nowMs
      } else {
        displayedDurationMs = Math.max(
          displayedDurationMs,
          persisted.segmentBaseMs + Math.max(0, nowMs - persisted.observedAtMs),
        )
      }
    } else if (serverAdvanced || persisted.ticking !== observation.ticking) {
      segmentBaseMs = displayedDurationMs
      observedAtMs = nowMs
    }

    return {
      snapshotDurationMs: Math.max(persistedSnapshot, snapshotDurationMs),
      displayedDurationMs,
      segmentBaseMs,
      observedAtMs,
      ticking: observation.ticking,
    }
  }

  private readPersistedState(
    observation: AssistantWorkedDurationObservation,
    stateKey: string,
    nowMs: number,
  ): AssistantWorkedDurationPersistedState | undefined {
    if (!this.storage) return undefined
    try {
      const raw = this.storage.getItem(this.storageKey(observation.scope || 'default'))
      if (!raw) return undefined
      const bucket = JSON.parse(raw) as { version?: unknown; entries?: Record<string, unknown> }
      if (bucket.version !== 1 || !bucket.entries || typeof bucket.entries !== 'object') return undefined
      const candidate = bucket.entries[stateKey]
      if (!candidate || typeof candidate !== 'object') return undefined
      const value = candidate as Record<string, unknown>
      const numeric = (key: string) => {
        const candidate = value[key]
        return typeof candidate === 'number' && Number.isFinite(candidate) ? candidate : undefined
      }
      const savedAtMs = numeric('savedAtMs')
      const snapshotDurationMs = numeric('snapshotDurationMs')
      const displayedDurationMs = numeric('displayedDurationMs')
      const segmentBaseMs = numeric('segmentBaseMs')
      const observedAtMs = numeric('observedAtMs')
      if (savedAtMs === undefined || snapshotDurationMs === undefined || displayedDurationMs === undefined ||
        segmentBaseMs === undefined || observedAtMs === undefined || typeof value.ticking !== 'boolean') return undefined
      if (savedAtMs < nowMs - this.maxAgeMs || snapshotDurationMs < 0 || displayedDurationMs < 0 ||
        segmentBaseMs < 0 || displayedDurationMs > MAX_DURATION_MS || segmentBaseMs > MAX_DURATION_MS) return undefined
      return { savedAtMs, snapshotDurationMs, displayedDurationMs, segmentBaseMs, observedAtMs, ticking: value.ticking }
    } catch {
      return undefined
    }
  }

  private persistState(
    observation: AssistantWorkedDurationObservation,
    stateKey: string,
    state: AssistantWorkedDurationClockState,
    nowMs: number,
  ): void {
    if (!this.storage) return
    try {
      const key = this.storageKey(observation.scope || 'default')
      const raw = this.storage.getItem(key)
      const decoded = raw ? JSON.parse(raw) as { version?: unknown; entries?: Record<string, AssistantWorkedDurationPersistedState> } : undefined
      const entries: Record<string, AssistantWorkedDurationPersistedState> = decoded?.version === 1 && decoded.entries && typeof decoded.entries === 'object'
        ? { ...decoded.entries }
        : {}
      entries[stateKey] = { ...state, savedAtMs: nowMs }
      const cutoff = nowMs - this.maxAgeMs
      const retained = Object.entries(entries)
        .filter(([, value]) => value && typeof value.savedAtMs === 'number' && value.savedAtMs >= cutoff)
        .sort(([, left], [, right]) => right.savedAtMs - left.savedAtMs)
        .slice(0, this.maxEntries)
      this.storage.setItem(key, JSON.stringify({ version: 1, entries: Object.fromEntries(retained) }))
    } catch {
      // Storage can be disabled, full, or unavailable in privacy mode. The
      // in-memory clock remains authoritative for the current page.
    }
  }

  private removePersistedState(observation: AssistantWorkedDurationObservation, stateKey: string): void {
    if (!this.storage) return
    try {
      const key = this.storageKey(observation.scope || 'default')
      const raw = this.storage.getItem(key)
      if (!raw) return
      const decoded = JSON.parse(raw) as { version?: unknown; entries?: Record<string, unknown> }
      if (decoded.version !== 1 || !decoded.entries || typeof decoded.entries !== 'object') return
      const entries = { ...decoded.entries }
      delete entries[stateKey]
      if (Object.keys(entries).length === 0) this.storage.removeItem(key)
      else this.storage.setItem(key, JSON.stringify({ version: 1, entries }))
    } catch {
      // Ignore storage cleanup failures; terminal rendering still uses the
      // server snapshot and the stale entry is bounded by the age limit.
    }
  }
}

function defaultAssistantWorkedDurationStorage(): AssistantWorkedDurationStorage | undefined {
  try {
    if (typeof window === 'undefined') return undefined
    return window.sessionStorage
  } catch {
    return undefined
  }
}

function normalizeDurationScope(value: string): string {
  return value.trim().slice(0, 256) || 'default'
}

function encodeDurationScope(value: string): string {
  return encodeURIComponent(normalizeDurationScope(value)).slice(0, 256)
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
