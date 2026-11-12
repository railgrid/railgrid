import type { OrgBilling, OrgBillingLimit, OrgBillingPlan, OrgBillingPrice } from '@/stores/tenant'

const PREFIX = 'railgrid:billing:'
const MAX_AGE = 60 * 60 * 1000

export interface WorkspaceBillingDraft {
  name: string
  createdAt: number
}
export interface PendingCheckout {
  plan: string
  startedAt: number
}

function read<T>(kind: string, org: string, user: string): T | null {
  if (!org || !user) return null
  try {
    const item = JSON.parse(sessionStorage.getItem(`${PREFIX}${kind}:${user}:${org}`) || 'null')
    if (!item || typeof item.savedAt !== 'number' || Date.now() - item.savedAt > MAX_AGE || item.savedAt > Date.now()) return null
    return item.value as T
  } catch { return null }
}

function write(kind: string, org: string, user: string, value: unknown): boolean {
  if (!org || !user) return false
  try {
    sessionStorage.setItem(`${PREFIX}${kind}:${user}:${org}`, JSON.stringify({ savedAt: Date.now(), value }))
    return true
  } catch { return false }
}

export function clearBillingState(kind: 'draft' | 'checkout', org: string, user: string): void {
  try { sessionStorage.removeItem(`${PREFIX}${kind}:${user}:${org}`) } catch { /* Storage can be unavailable. */ }
}

export function saveWorkspaceBillingDraft(org: string, user: string, name: string): boolean {
  return write('draft', org, user, { name: name.trim(), createdAt: Date.now() })
}

export function workspaceBillingDraft(org: string, user: string): WorkspaceBillingDraft | null {
  const value = read<WorkspaceBillingDraft>('draft', org, user)
  return value && typeof value.name === 'string' && value.name.length <= 1000 && typeof value.createdAt === 'number' ? value : null
}

export function savePendingCheckout(org: string, user: string, plan: string): boolean {
  return write('checkout', org, user, { plan, startedAt: Date.now() })
}

export function pendingCheckout(org: string, user: string): PendingCheckout | null {
  const value = read<PendingCheckout>('checkout', org, user)
  return value && typeof value.plan === 'string' && value.plan.length > 0 && value.plan.length <= 253 && typeof value.startedAt === 'number' ? value : null
}

export function workspaceLimitError(message: string): boolean {
  return /workspaces?[^.\n]*(?:limit|allowance)[^.\n]*(?:exceed|reach|used)|workspaces? limit exceeded|limit exceeded[^.\n]*workspaces?/i.test(message)
}

export function billingSettingsLocation(org: string) {
  return { path: `/${encodeURIComponent(org)}/settings/organizations`, query: { panel: 'billing', review: '1', meter: 'workspaces' } }
}

export function priceAvailable(price?: OrgBillingPrice): price is OrgBillingPrice {
  return !!price && /^\d+(?:\.\d+)?$/.test(price.amount) && Number.isFinite(Number(price.amount)) &&
    /^[A-Z]{3}$/.test(price.currency) && ['day', 'week', 'month', 'year'].includes(price.interval) &&
    Number.isInteger(price.intervalCount) && price.intervalCount > 0 && !!price.id
}

export function planPrice(price?: OrgBillingPrice): string {
  if (!priceAvailable(price)) return 'Price unavailable'
  try {
    // Intl accepts exact decimal strings in current browsers; avoid losing
    // currency precision by first converting the provider amount to Number.
    const format = new Intl.NumberFormat(undefined, { style: 'currency', currency: price.currency }).format as unknown as (value: string) => string
    return `${format(price.amount)} ${price.currency}`
  } catch { return 'Price unavailable' }
}

export function priceInterval(price?: OrgBillingPrice): string {
  if (!priceAvailable(price)) return ''
  return price.intervalCount === 1 ? `per ${price.interval}` : `every ${price.intervalCount} ${price.interval}s`
}

export function subscriptionActivated(value: OrgBilling, expectedPlan: string, allowancesUpdating: boolean): boolean {
  return !!expectedPlan && value.plan === expectedPlan && value.managedBy === 'billing' &&
    value.subscription?.entitledPlan === expectedPlan && ['active', 'trialing'].includes(value.subscription.status || '') &&
    value.overrides !== undefined && !allowancesUpdating
}

// Compare what applies to this organization, including persistent overrides.
export function proposedLimits(plan: OrgBillingPlan, overrides?: OrgBillingLimit[]): OrgBillingLimit[] {
  const limits = new Map(plan.limits.map((limit) => [limit.meter, limit]))
  for (const limit of overrides ?? []) limits.set(limit.meter, limit)
  return [...limits.values()]
}
// The declaration changes immediately; metering applies it on its next sample.
// Keep the enforced snapshot visible, but do not infer upgrades from mixed versions.
export function allowancesPending(value: OrgBilling): boolean {
  const plan = value.plans.find((item) => item.current)
  if (!plan || value.overrides === undefined) return false
  const signature = (limits: OrgBillingLimit[]) => JSON.stringify(limits.map((limit) => ({
    meter: limit.meter, shape: limit.shape || 'Consumption', enforce: limit.enforce, max: limit.max ?? '',
  })).sort((a, b) => a.meter.localeCompare(b.meter)))
  return signature(proposedLimits(plan, value.overrides)) !== signature(value.limits)
}
