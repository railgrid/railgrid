import { assignmentApplied, type MeteringItem } from './metering-model'

export const usageFreshnessMs = 5 * 60_000
export type DiagnosticCheck = { state: string; tone: 'success' | 'warning' | 'danger' | 'muted'; detail: string }
type Organization = { name: string; plan?: string }

export function planApplication(org: Organization, entitlement: MeteringItem | undefined, plan: MeteringItem | undefined): DiagnosticCheck {
  if (!entitlement) return { state: 'Awaiting metering', tone: 'warning', detail: 'No entitlement has been created for this organization yet.' }
  if (!plan) return { state: 'Plan unavailable', tone: 'danger', detail: 'The requested plan could not be found. Check the organization assignment and available plans.' }
  const conditions = entitlement.status?.conditions as { type?: string; status?: string; message?: string; reason?: string }[] | undefined
  const ready = conditions?.find(c => c.type === 'Ready')
  if ((org.plan && org.plan !== plan.metadata.name) || !assignmentApplied(org.plan, org.plan || '', plan, entitlement)) return { state: 'Applying', tone: 'warning', detail: 'The requested plan, controller generation, or effective limits have not converged. Refresh to check progress.' }
  if (ready?.status === 'False') return { state: 'Needs attention', tone: 'danger', detail: ready.message || ready.reason || 'The entitlement controller reports that the plan is not ready.' }
  if (ready?.status !== 'True') return { state: 'Unconfirmed', tone: 'muted', detail: 'The plan and limits match, but readiness has not been confirmed.' }
  return { state: 'Applied', tone: 'success', detail: 'The assigned plan and override-aware limits match the controller’s observed state.' }
}

// window.end is when the entitlement controller published its usage snapshot.
// A recent snapshot is not proof that every upstream meter has delivered data.
export function usageFreshness(entitlement: MeteringItem | undefined, now: number): DiagnosticCheck & { reportedAt: string } {
  const usage = entitlement?.status?.usage as Record<string, unknown> | undefined
  const window = entitlement?.status?.window as { end?: string } | undefined
  const reportedAt = typeof window?.end === 'string' ? window.end : ''
  const timestamp = Date.parse(reportedAt)
  const limits = entitlement?.status?.effectiveLimits as { meter?: string }[] | undefined
  const missing = !usage || !Object.keys(usage).length || Object.values(usage).some(v => v === undefined || v === null || v === '')
    || limits?.some(limit => !limit.meter || usage[limit.meter] === undefined || usage[limit.meter] === null || usage[limit.meter] === '')
  if (missing) return { state: 'Unavailable', tone: 'muted', detail: 'Usage values are missing. Inspect this organization’s entitlement and metering configuration.', reportedAt }
  if (!Number.isFinite(timestamp)) return { state: 'Unknown', tone: 'muted', detail: 'The usage snapshot has no valid reporting time.', reportedAt: '' }
  if (timestamp - now > 60_000) return { state: 'Unknown', tone: 'muted', detail: 'The reporting time is in the future. Check clock synchronization.', reportedAt }
  if (now - timestamp > usageFreshnessMs) return { state: 'Stale', tone: 'warning', detail: 'The last usage snapshot is more than five minutes old. Refresh, then check the controller and raw resources if it persists.', reportedAt }
  return { state: 'Recent', tone: 'success', detail: 'The controller reported a usage snapshot within the last five minutes.', reportedAt }
}
