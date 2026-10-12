// Presentation shared by the delivery log and the filter tester: how an
// outcome reads and which tone it takes, and the one-line "what was it".
import type { TriggerDelivery } from '../types'

export type DeliveryTone = 'success' | 'warning' | 'danger' | 'muted'

const OUTCOME_LABEL: Record<string, string> = {
  fired: 'Fired',
  filtered: 'Filtered',
  suspended: 'Paused',
  disabled: 'Disabled',
  noTask: 'No task',
  failed: 'Failed',
}

export function deliveryOutcomeLabel(outcome: string | undefined): string {
  return (outcome && OUTCOME_LABEL[outcome]) || outcome || 'Unknown'
}

export function deliveryOutcomeTone(outcome: string | undefined): DeliveryTone {
  switch (outcome) {
    case 'fired': return 'success'
    case 'filtered': return 'muted'
    case 'failed': return 'danger'
    default: return 'warning'
  }
}

/** deliveryWhat is the event as a filter would see it: eventType, then action. */
export function deliveryWhat(d: Pick<TriggerDelivery, 'eventType' | 'action' | 'contentType'>): string {
  if (d.eventType && d.action && d.eventType !== d.action) return `${d.eventType} · ${d.action}`
  return d.eventType || d.action || (d.contentType ? d.contentType.split(';')[0] : 'delivery')
}

/** deliveryOptionLabel names a recorded delivery in the tester's picker. */
export function deliveryOptionLabel(d: TriggerDelivery, when: (iso: string) => string): string {
  const what = deliveryWhat(d)
  const repo = d.repository ? ` · ${d.repository}` : ''
  return `${when(d.receivedAt)} · ${what}${repo} · ${deliveryOutcomeLabel(d.outcome).toLowerCase()}`
}

/**
 * parseHeaderLines turns "Name: value" lines into a header map, for the
 * tester's pasted sample. Lines without a colon are ignored rather than
 * refused: this is a scratchpad, not a form.
 */
export function parseHeaderLines(input: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of input.split('\n')) {
    const at = raw.indexOf(':')
    if (at <= 0) continue
    const key = raw.slice(0, at).trim()
    const value = raw.slice(at + 1).trim()
    if (key) out[key] = value
  }
  return out
}
