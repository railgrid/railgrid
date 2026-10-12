// Presentation shared by the webhook delivery log, the Webhooks page and the
// trigger filter tester: how an outcome reads and which tone it takes, the
// one-line "what was it", and the per-route line under a delivery.
import type { Webhook, WebhookDelivery, WebhookDeliveryRoute } from '../types'

export type DeliveryTone = 'success' | 'warning' | 'danger' | 'muted'

// One vocabulary for both levels: a delivery's rollup (fired, filtered,
// unrouted, failed) and a single route's outcome (which adds the trigger-side
// reasons a route can stop at).
const OUTCOME_LABEL: Record<string, string> = {
  fired: 'Fired',
  filtered: 'Filtered',
  unrouted: 'Unrouted',
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
export function deliveryWhat(d: Pick<WebhookDelivery, 'eventType' | 'action' | 'contentType'>): string {
  if (d.eventType && d.action && d.eventType !== d.action) return `${d.eventType} · ${d.action}`
  return d.eventType || d.action || (d.contentType ? d.contentType.split(';')[0] : 'delivery')
}

/**
 * routeText is one route's line under a delivery, without the run link:
 * "on-pr filtered: eventType does not match pull_request".
 */
export function routeText(route: Pick<WebhookDeliveryRoute, 'trigger' | 'outcome' | 'reason'>): string {
  return `${route.trigger} ${deliveryOutcomeLabel(route.outcome).toLowerCase()}${route.reason ? `: ${route.reason}` : ''}`
}

/**
 * deliveryOptionLabel names a recorded delivery in the tester's picker. With
 * `trigger` set, the outcome shown is that trigger's route rather than the
 * rollup, because the tester is asking about one trigger.
 */
export function deliveryOptionLabel(d: WebhookDelivery, when: (iso: string) => string, trigger = ''): string {
  const what = deliveryWhat(d)
  const repo = d.repository ? ` · ${d.repository}` : ''
  const route = trigger ? d.routes.find(r => r.trigger === trigger) : undefined
  const outcome = trigger ? (route ? deliveryOutcomeLabel(route.outcome) : 'not routed here') : deliveryOutcomeLabel(d.outcome)
  return `${when(d.receivedAt)} · ${what}${repo} · ${outcome.toLowerCase()}`
}

/**
 * webhookURL is the inbound URL: status.webhookPath (minted by the Webhook
 * reconciler, token included) on the hub the portal is served from — the
 * same origin the Connections view hands the provider as publicBaseURL.
 */
export function webhookURL(webhook: Pick<Webhook, 'status'> | undefined): string {
  const path = webhook?.status?.webhookPath
  return path ? `${location.origin}${path}` : ''
}

/**
 * parseHeaderLines turns "Name: value" lines into a header map, for a pasted
 * sample. Lines without a colon are ignored rather than refused: this is a
 * scratchpad, not a form.
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
