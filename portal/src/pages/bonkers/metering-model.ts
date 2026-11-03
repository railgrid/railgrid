import type { Limit } from './MeteringLimitsEditor.vue'

export type MeteringItem = {
  metadata: { name: string; generation?: number; labels?: Record<string, string>; annotations?: Record<string, string> }
  spec?: Record<string, unknown>
  status?: Record<string, unknown>
}

const archivedPlanAnnotation = 'metering.railgrid.ai/archived'

export function isPlanArchived(plan: MeteringItem | undefined | null): boolean {
  return plan?.metadata.annotations?.[archivedPlanAnnotation] === 'true'
}

// Archived plans stay visible when they are already selected so an operator
// can understand the current assignment and move away from it. Every archived
// alternative is excluded from new assignments and entitlement edits.
export function planSelectionOptions(plans: readonly MeteringItem[], currentPlan = ''): MeteringItem[] {
  return plans.filter(plan => !isPlanArchived(plan) || plan.metadata.name === currentPlan)
}

// Metering records reference accounts by ID. Prefer the organization's current
// name, then the account's stored name; callers retain IDs for unknown accounts.
export function meteringDisplayNames(organizations: readonly { name: string; displayName?: string }[], accounts: readonly MeteringItem[]): Map<string, string> {
  const names = new Map<string, string>()
  for (const account of accounts) {
    const name = account.spec?.displayName
    if (typeof name === 'string' && name.trim()) names.set(account.metadata.name, name)
  }
  for (const organization of organizations) {
    if (organization.displayName?.trim()) names.set(organization.name, organization.displayName)
  }
  return names
}

// Hub-owned organization entitlements are named by organization UUID. Account
// references are not identities: multiple entitlements may share one account.
export function indexEntitlements(items: readonly MeteringItem[]): Map<string, MeteringItem> {
  return new Map(items.map(item => [item.metadata.name, item]))
}

export function toLimits(value: unknown): Limit[] {
  if (!Array.isArray(value)) return []
  return value.map((l: Record<string, unknown>) => ({
    meter: String(l.meter ?? ''), shape: String(l.shape ?? 'Level'),
    enforce: String(l.enforce ?? 'Hard'), max: String(l.max ?? ''),
  }))
}

export function effectiveLimits(plan: MeteringItem | undefined, overrides: unknown): Limit[] {
  const limits = new Map(toLimits(plan?.spec?.limits).map(l => [l.meter, l]))
  for (const limit of toLimits(overrides)) limits.set(limit.meter, limit)
  return [...limits.values()]
}

export function assignmentSource(assigned: string | undefined, entitlement: MeteringItem | undefined, defaultPlan: string | undefined): string {
  if (assigned) return 'Assigned by platform admin'
  const source = entitlement?.metadata.labels?.['metering.railgrid.ai/plan-source']
  if (source === 'operator') return 'Assigned by platform admin'
  if (source === 'billing') return 'Subscription'
  if (entitlement?.spec?.plan === defaultPlan) return 'Platform default'
  return entitlement ? 'Entitlement assignment' : 'Awaiting metering'
}

export function assignmentSelection(assigned: string | undefined, entitlement: MeteringItem | undefined, defaultPlan: string | undefined): string {
  if (assigned) return assigned
  const effective = String(entitlement?.spec?.plan ?? '')
  return effective === defaultPlan ? '' : effective
}

export function assignmentNeedsWrite(assigned: string | undefined, entitlement: MeteringItem | undefined, defaultPlan: string | undefined, selected: string): boolean {
  if ((assigned ?? '') !== selected) return true
  // Reset is meaningful even when spec.plan was already empty: a direct
  // entitlement edit or subscription can still own its effective plan.
  return selected === '' && !!entitlement && (
    entitlement.spec?.plan !== defaultPlan
    || !!entitlement.metadata.labels?.['metering.railgrid.ai/plan-source']
    || entitlement.metadata.labels?.['railgrid.ai/plan-from-spec'] === 'true'
  )
}

// A successful assignment request is not proof of application. Wait for the
// requested org assignment, the entitlement, and controller-observed limits.
export function assignmentApplied(assigned: string | undefined, requested: string, target: MeteringItem | undefined, entitlement: MeteringItem | undefined): boolean {
  if ((assigned ?? '') !== requested || !target || !entitlement) return false
  if (entitlement.spec?.plan !== target.metadata.name) return false
  const generation = entitlement.metadata.generation
  const observed = entitlement.status?.observedGeneration
  if (generation === undefined || typeof observed !== 'number' || observed < generation) return false
  const expected = effectiveLimits(target, entitlement.spec?.overrides)
  const actual = toLimits(entitlement.status?.effectiveLimits)
  return expected.length === actual.length && expected.every(l => actual.some(a =>
    a.meter === l.meter && a.shape === l.shape && a.enforce === l.enforce && a.max === l.max))
}

export function limitsError(limits: Limit[]): string | null {
  const seen = new Set<string>()
  for (const l of limits) {
    if (!/^[a-z0-9][a-z0-9_.-]*$/.test(l.meter)) return 'Use lowercase meter names, with numbers, underscores, dots, or hyphens.'
    if (seen.has(l.meter)) return `The meter “${l.meter}” is listed twice. Keep one limit per meter.`
    seen.add(l.meter)
    if ((l.enforce !== 'Meter' || l.max !== '') && !/^(?:\d+(?:\.\d*)?|\.\d+)$/.test(l.max)) return `Enter a non-negative decimal maximum for ${l.meter}.`
  }
  return null
}
