// The provider attaches a verification receipt to every completed assistant
// message (metadata.assistantVerification). For a negative outcome it also
// carries the provider's own summary and the blockers it observed: a failed
// workspace sync, a workspace rebuilt from git after a replica change. This
// module turns that receipt into a banner the message renders regardless of
// what the model wrote, so a user is never left with prose as the only account
// of whether the development runtime is running their work.

export type AssistantVerificationTone = 'error' | 'warning'

export interface AssistantVerificationBanner {
  tone: AssistantVerificationTone
  title: string
  summary: string
  blockers: string[]
}

const MAX_BLOCKERS = 4
const MAX_BLOCKER_LENGTH = 320
const MAX_SUMMARY_LENGTH = 240

function boundedText(value: unknown, max: number): string {
  if (typeof value !== 'string') return ''
  const trimmed = value.trim()
  if (trimmed.length <= max) return trimmed
  return `${trimmed.slice(0, max - 1).trimEnd()}…`
}

function boundedBlockers(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  const out: string[] = []
  for (const entry of value) {
    const text = boundedText(entry, MAX_BLOCKER_LENGTH)
    if (!text) continue
    out.push(text)
    if (out.length === MAX_BLOCKERS) break
  }
  return out
}

/**
 * Returns the banner for a message's verification receipt, or null when the
 * receipt is missing, malformed, or positive. Only `failed` and `stale` earn a
 * banner: `not_verified` means no runtime evidence was gathered, which is the
 * normal state for answers and reviews.
 */
export function assistantVerificationBanner(metadata: unknown): AssistantVerificationBanner | null {
  if (!metadata || typeof metadata !== 'object') return null
  const receipt = (metadata as Record<string, unknown>).assistantVerification
  if (!receipt || typeof receipt !== 'object') return null
  const outcome = (receipt as Record<string, unknown>).outcome
  if (outcome !== 'failed' && outcome !== 'stale') return null
  const summary = boundedText((receipt as Record<string, unknown>).summary, MAX_SUMMARY_LENGTH)
  const blockers = boundedBlockers((receipt as Record<string, unknown>).blockers)
  if (outcome === 'stale') {
    return {
      tone: 'warning',
      title: 'Development runtime is behind this work',
      summary: summary || 'The development sandbox has not picked up the latest workspace changes.',
      blockers,
    }
  }
  return {
    tone: 'error',
    title: 'Development runtime is not running this work',
    summary: summary || 'Runtime verification failed for this turn, so the preview does not reflect the changes described above.',
    blockers,
  }
}
