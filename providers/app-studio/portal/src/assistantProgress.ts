export interface AssistantProgress {
  version: 1
  messages: string[]
  messageSequences?: number[]
  workedDurationMs: number
}

const MAX_MESSAGES = 32
const MAX_MESSAGE_BYTES = 600
const MAX_DURATION_MS = 7 * 24 * 60 * 60 * 1000

export function parseAssistantProgress(value: unknown): AssistantProgress | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const item = value as Record<string, unknown>
  if (Object.keys(item).some((key) => !['version', 'messages', 'messageSequences', 'workedDurationMs'].includes(key))) return undefined
  if (item.version !== 1 || !Array.isArray(item.messages)) return undefined
  if (item.messages.length === 0 || item.messages.length > MAX_MESSAGES) return undefined
  if (!Number.isInteger(item.workedDurationMs) || (item.workedDurationMs as number) < 0 || (item.workedDurationMs as number) > MAX_DURATION_MS) return undefined

  const messages: string[] = []
  for (const message of item.messages) {
    if (typeof message !== 'string' || !message || message !== message.trim()) return undefined
    if (new TextEncoder().encode(message).length > MAX_MESSAGE_BYTES || /[\u0000-\u001f\u007f-\u009f]/u.test(message)) return undefined
    messages.push(message)
  }
  let messageSequences: number[] | undefined
  if (item.messageSequences !== undefined) {
    if (!Array.isArray(item.messageSequences) || item.messageSequences.length !== messages.length) return undefined
    let previous = 0
    messageSequences = []
    for (const sequence of item.messageSequences) {
      if (!Number.isSafeInteger(sequence) || Number(sequence) < 1 || Number(sequence) > 10_000) return undefined
      if (Number(sequence) <= previous) return undefined
      previous = Number(sequence)
      messageSequences.push(Number(sequence))
    }
  }
  return {
    version: 1,
    messages,
    ...(messageSequences ? { messageSequences } : {}),
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
