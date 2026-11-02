<script setup lang="ts">
// Metering in the admin area. Metering is a standalone deployment the hub
// discovers through kcp (pkg/hub/metering); this section reads its objects
// through the hub's read proxy, /api/admin/metering/{collection}, so the
// operator token never reaches the browser. Not installed renders the Helm
// hint instead of tables. Plans (quota templates) and entitlements (an
// organization's plan plus overrides) are edited here too, through the hub's
// write routes. Organization assignments go through the Organization plan
// endpoint, so the reconciler preserves them. Overrides are edited separately.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { Plus, RefreshCw } from 'lucide-vue-next'

import ResourceTable from '@/portalkit/ResourceTable.vue'
import ResourcePage from '@/portalkit/ResourcePage.vue'
import ResourceBackLink from '@/portalkit/ResourceBackLink.vue'
import ResourceSectionCard from '@/portalkit/ResourceSectionCard.vue'
import type { ResourceRefreshMode } from '@/portalkit/page-state'
import ResourceTableEditButton from '@/portalkit/ResourceTableEditButton.vue'
import type { TableFilterDefinition } from '@/portalkit/table'
import ResourceTableDeleteButton from '@/portalkit/ResourceTableDeleteButton.vue'
import ConditionsPanel, { type ConditionInfo } from '@/portalkit/ConditionsPanel.vue'
import StatusBadge from '@/portalkit/StatusBadge.vue'
import { organizationReturn } from './admin-relations'
import { confirmDialog } from '@/portalkit/confirm'
import { authFetch } from '@/auth/session'

import MeteringDialog from './MeteringDialog.vue'
import { planApplication, usageFreshness } from './metering-diagnostics'
import { meterDefinition, meterLabel } from '@/components/metering-meters'
import { assignmentApplied, assignmentNeedsWrite, assignmentSelection, assignmentSource, effectiveLimits, indexEntitlements, limitsError, meteringDisplayNames, toLimits, type MeteringItem } from './metering-model'
import type { AdminOrg } from '@/stores/admin'
import MeteringLimitsEditor, { type Limit } from './MeteringLimitsEditor.vue'
import MeteringUsageList, { type LimitRow, type UsageWindow } from '@/components/MeteringUsageList.vue'

type Endpoints = { portalURL: string; webhookURL?: string; auditURL?: string; version?: string; workspace: string }
type Status = {
  installed: boolean; workspace: string; ready: boolean; reason?: string; collections: string[]; endpoints?: Endpoints
  writable?: boolean; defaultPlan?: string
}
type Item = MeteringItem

const status = ref<Status | null>(null)
const items = ref<Record<string, Item[]>>({})
const loading = ref(false)
const loaded = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const lastCheckedAt = ref('')
const diagnosticNow = ref(Date.now())
let diagnosticClock: ReturnType<typeof setInterval> | undefined
const foregroundLoading = computed(() => loading.value && refreshMode.value === 'foreground')
const error = ref<string | null>(null)
const organizationsLoaded = ref(false)
const organizationsLoading = ref(false)
const organizationError = ref<string | null>(null)
const organizationsCheckedAt = ref('')

// Editors. One open at a time per table; `busy` names the object being written.
type PlanDraft = { name: string; isNew: boolean; displayName: string; limits: Limit[] }
type EntitlementDraft = { name: string; plan: string; overrides: Limit[]; advanced: boolean }
const planDraft = ref<PlanDraft | null>(null)
const entitlementDraft = ref<EntitlementDraft | null>(null)
const busy = ref<string | null>(null)
const actionError = ref<string | null>(null)

const writable = computed(() => !!status.value?.writable)


// Collections show compact summaries; row activation opens the canonical
// resource page. Search includes friendly names, IDs, and limit summaries.
type Row = Record<string, unknown>
type Column = {
  key: string
  label: string
  primary?: boolean
  sortable?: boolean
  ariaLabel?: string
  maxWidth?: string
  fullValue?: (row: Row) => string
}
type Collection = {
  key: string
  label: string
  /** Singular noun for a row's accessible name, e.g. "Plan free". */
  noun: string
  searchPlaceholder: string
  searchKeys: string[]
  columns: Column[]
}
const summaryText = (row: Row) => String(row.summaryText ?? '')
const collections: Collection[] = [
  { key: 'plans', label: 'Plans', noun: 'Plan', searchPlaceholder: 'Search plans…',
    searchKeys: ['name', 'displayName', 'summaryText'], columns: [
      { key: 'displayName', label: 'Plan', primary: true, sortable: true },
      { key: 'name', label: 'Plan ID' },
      { key: 'organizationCount', label: 'Organizations', sortable: true },
      { key: 'limits', label: 'Limits', maxWidth: '28rem', fullValue: summaryText },
      { key: 'actions', label: '', ariaLabel: 'Actions' },
    ] },
  { key: 'entitlements', label: 'Entitlements', noun: 'Entitlement', searchPlaceholder: 'Search entitlements…',
    searchKeys: ['displayName', 'name', 'account', 'plan', 'stateText', 'summaryText'], columns: [
      { key: 'displayName', label: 'Organization', primary: true },
      { key: 'plan', label: 'Plan' },
      { key: 'state', label: 'State', fullValue: (row) => stateOf(row).status },
      { key: 'limits', label: 'Limits and usage', maxWidth: '28rem', fullValue: summaryText },
      { key: 'name', label: 'Entitlement ID' },
    ] },
  { key: 'accounts', label: 'Accounts', noun: 'Account', searchPlaceholder: 'Search accounts…',
    searchKeys: ['name', 'displayName', 'customerRef', 'summaryText'], columns: [
      { key: 'displayName', label: 'Account', primary: true },
      { key: 'memberCount', label: 'Workspaces' },
      { key: 'usage', label: 'Usage', maxWidth: '28rem', fullValue: summaryText },
      { key: 'customerRef', label: 'Customer' },
      { key: 'name', label: 'Account ID' },
    ] },
  { key: 'membershipreports', label: 'Membership reports', noun: 'Membership report', searchPlaceholder: 'Search membership reports…',
    searchKeys: ['displayName', 'name', 'account', 'reporter'], columns: [
      { key: 'displayName', label: 'Organization', primary: true },
      { key: 'reporter', label: 'Reporter' },
      { key: 'members', label: 'Workspaces' },
      { key: 'name', label: 'Report ID' },
    ] },
  { key: 'meteringconfigs', label: 'Metering configs', noun: 'Metering config', searchPlaceholder: 'Search metering configs…',
    searchKeys: ['name', 'target', 'meters', 'account'], columns: [
      { key: 'name', label: 'Name', primary: true },
      { key: 'target', label: 'Target' },
      { key: 'meters', label: 'Meters' },
      { key: 'account', label: 'Account resolution' },
    ] },
]

function cell(value: unknown): string {
  if (value === undefined || value === null) return ''
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return String(value)
  if (Array.isArray(value)) {
    if (value.every((v) => v && typeof v === 'object' && 'meter' in (v as object))) {
      return (value as Array<Record<string, unknown>>)
        .map((l) => `${l.meter}: ${l.enforce ?? ''} ${l.max ?? ''}`.trim())
        .join(', ')
    }
    return `${value.length} item${value.length === 1 ? '' : 's'}`
  }
  if (typeof value === 'object') {
    const o = value as Record<string, unknown>
    if ('group' in o && 'resource' in o) return `${o.resource}.${o.group}`
    if ('resolve' in o) return String(o.resolve)
    return Object.entries(o).map(([k, v]) => `${k}=${cell(v)}`).join(', ')
  }
  return JSON.stringify(value)
}

// The structured values the limits, usage and state cells render. They ride
// on the row under their own keys; everything else stays the text of cell().
function limitsOf(row: Record<string, unknown>): LimitRow[] {
  return Array.isArray(row.limits) ? (row.limits as LimitRow[]) : []
}
function usageOf(row: Record<string, unknown>): Record<string, unknown> | null {
  return row.usage && typeof row.usage === 'object' && !Array.isArray(row.usage) ? (row.usage as Record<string, unknown>) : null
}
function windowOf(row: Record<string, unknown>): UsageWindow | null {
  return row.window && typeof row.window === 'object' ? (row.window as UsageWindow) : null
}

type Condition = { type?: string; status?: string; reason?: string; message?: string }
type EntitlementState = { status: string; tone: 'success' | 'warning' | 'danger' | 'muted'; title: string }

// entitlementState reads the controller's conditions into one badge: a hard
// limit exceeded (creates denied) outranks a soft one (flagged), which
// outranks plain readiness.
function entitlementState(status: Record<string, unknown> | undefined): EntitlementState {
  const conditions = Array.isArray(status?.conditions) ? (status?.conditions as Condition[]) : []
  const find = (type: string) => conditions.find((c) => c.type === type)
  const hard = find('OverHardLimit')
  const soft = find('OverSoftLimit')
  const ready = find('Ready')
  if (hard?.status === 'True') return { status: 'over hard limit', tone: 'danger', title: 'A hard limit is exceeded: creates over it are denied' }
  if (soft?.status === 'True') return { status: 'over soft limit', tone: 'warning', title: 'A soft limit is exceeded: flagged, nothing is blocked' }
  if (ready?.status === 'True') return { status: 'ready', tone: 'success', title: 'Within every limit' }
  if (ready?.status === 'False') return { status: 'not ready', tone: 'danger', title: ready.message || ready.reason || 'Not ready' }
  return { status: 'pending', tone: 'warning', title: 'Not reconciled yet' }
}
function stateOf(row: Record<string, unknown>): EntitlementState {
  return (row.state as EntitlementState | undefined) ?? { status: 'pending', tone: 'warning', title: 'Not reconciled yet' }
}

// limitsText is the one-line text of a limits/usage cell, for truncation
// disclosure and anything else that needs it as a string.
function limitsText(limits: LimitRow[], usage: Record<string, unknown> | null): string {
  const parts = limits.map((l) => {
    const used = usage?.[String(l.meter)]
    const amount = used === undefined ? String(l.max ?? '') : `${used} of ${l.max ?? '∞'}`
    return `${l.meter}: ${l.enforce ?? 'Meter'} ${amount}`.trim()
  })
  const limited = new Set(limits.map((l) => String(l.meter)))
  for (const [meter, value] of Object.entries(usage ?? {}).sort(([a], [b]) => a.localeCompare(b))) {
    if (!limited.has(meter)) parts.push(`${meter}: ${value}`)
  }
  return parts.join('; ')
}

function rowsFor(collection: string): Row[] {
  return (items.value[collection] ?? []).map((item) => {
    const row: Row = { name: item.metadata.name }
    for (const [k, v] of Object.entries(item.spec ?? {})) row[k] = cell(v)
    for (const [k, v] of Object.entries(item.status ?? {})) row[k] = cell(v)
    if (collection === 'plans') row.displayName = item.spec?.displayName || item.metadata.name
    if (['accounts', 'entitlements', 'membershipreports'].includes(collection)) {
      const account = collection === 'accounts' ? item.metadata.name : String(item.spec?.account || item.metadata.name)
      row.displayName = displayNames.value.get(account) || account
    }
    if (collection === 'membershipreports') row.members = cell((item.spec?.members as unknown[] | undefined)?.length ?? 0)
    if (collection === 'plans') {
      row.limits = item.spec?.limits ?? []
      row.organizationCount = organizationsLoaded.value && !organizationError.value ? planCount(item.metadata.name) : 'Unavailable'
    }
    if (collection === 'entitlements') {
      row.limits = item.status?.effectiveLimits ?? []
      row.usage = item.status?.usage ?? null
      row.window = item.status?.window ?? null
      row.state = entitlementState(item.status)
      row.stateText = (row.state as EntitlementState).status
    }
    if (collection === 'accounts') {
      row.usage = item.status?.usage ?? null
      row.window = item.status?.window ?? null
    }
    row.summaryText = limitsText(limitsOf(row), usageOf(row))
    return row
  })
}

// One row array per collection, rebuilt only when the items change: the table
// keeps its page and query across renders.
const tableRows = computed<Record<string, Row[]>>(() =>
  Object.fromEntries(collections.map((c) => [c.key, rowsFor(c.key)])))

function rowLabel(c: Collection, row: Row): string {
  return `${c.noun} ${String(row.displayName || row.name)}`
}

const readyTone = computed(() => (status.value?.ready ? 'success' : 'danger'))

async function refresh(force = false, mode: ResourceRefreshMode = 'foreground'): Promise<void> {
  if (loading.value) return
  loading.value = true
  void refreshOrganizations()
  refreshMode.value = mode
  error.value = null
  try {
    const resp = await authFetch(`/api/admin/metering${force ? '?refresh=true' : ''}`)
    if (!resp.ok) throw new Error(`metering status: ${resp.status}`)
    const next = (await resp.json()) as Status
    status.value = next
    if (next.installed) {
      const fetched = await Promise.all(next.collections.map(async (c) => {
        const r = await authFetch(`/api/admin/metering/${encodeURIComponent(c)}`)
        if (!r.ok) throw new Error(`${c}: ${r.status} ${(await r.text()).trim()}`)
        return [c, ((await r.json()) as { items: Item[] }).items ?? []] as const
      }))
      items.value = Object.fromEntries(fetched)
    } else {
      items.value = {}
    }
    loaded.value = true
    lastCheckedAt.value = new Date().toISOString()
    diagnosticNow.value = Date.now()
    checkPending()
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
    for (const p of pendingAssignments.value) if (Date.now() - p.started >= 90_000) p.timedOut = true
  }
}

// Organization naming/ownership is a separate read contract. Its failure must
// not discard successful Plan or raw metering reads.
async function refreshOrganizations(): Promise<void> {
  if (organizationsLoading.value) return
  organizationsLoading.value = true
  organizationError.value = null
  try {
    const organizations = await read<{ items: AdminOrg[] }>('/api/admin/organizations')
    orgs.value = organizations.items ?? []
    organizationsLoaded.value = true
    organizationsCheckedAt.value = new Date().toISOString()
  } catch {
    organizationError.value = 'Could not refresh organization details. Plans and raw metering resources remain available.'
  } finally {
    organizationsLoading.value = false
    checkPending()
  }
}

const route = useRoute()
const router = useRouter()
const orgs = ref<AdminOrg[]>([])
type MeteringView = 'organizations' | 'plans' | 'diagnostics'
const tab = computed<MeteringView>(() => route.query.view === 'plans' || route.query.view === 'diagnostics' ? route.query.view : 'organizations')
function selectTab(view: MeteringView): void {
  void router.push({ query: { ...route.query, view: view === 'organizations' ? undefined : view } })
}
const notice = ref('')
const assignment = ref<{ name: string; plan: string; initial: string } | null>(null)
const initialPlan = ref('')
const initialOverrides = ref('')
const initialEntitlement = ref('')
type PendingAssignment = { name: string; requested: string; target: string; started: number; timedOut: boolean }
const pending = ref<Record<string, PendingAssignment>>({})
const pendingAssignments = computed(() => Object.values(pending.value))
function assignmentPending(name: string): boolean { return !!pending.value[name] && !pending.value[name].timedOut }
let poll: ReturnType<typeof setTimeout> | undefined
let disposed = false
const organizationDetail = ref<HTMLElement | null>(null)
let organizationTrigger: HTMLElement | null = null

const planItems = computed(() => items.value.plans ?? [])
function planFor(name: string): Item | undefined { return planItems.value.find(p => p.metadata.name === name) }
function planLabel(name: string): string { return String(planFor(name)?.spec?.displayName || name || 'Awaiting assignment') }
const entitlementsByOrg = computed(() => indexEntitlements(items.value.entitlements ?? []))
const displayNames = computed(() => meteringDisplayNames(orgs.value, items.value.accounts ?? []))
function entitlementFor(name: string): Item | undefined { return entitlementsByOrg.value.get(name) }
function orgLabel(name: string): string { return orgs.value.find(o => o.name === name)?.displayName || name }
function sourceFor(org: AdminOrg | undefined): string { return org ? assignmentSource(org.plan, entitlementFor(org.name), status.value?.defaultPlan) : 'Organization unavailable' }
function planReferenceCount(name: string): number { return (items.value.entitlements ?? []).filter(e => e.spec?.plan === name).length }
function planCount(name: string): number { return orgs.value.filter(org => org.plan === name || entitlementFor(org.name)?.spec?.plan === name).length }
function planHasReferences(name: string): boolean { return planReferenceCount(name) > 0 || orgs.value.some(org => org.plan === name) || pendingAssignments.value.some(p => p.target === name) }
const organizationColumns: Column[] = [
  { key: 'displayName', label: 'Organization', primary: true, sortable: true },
  { key: 'planName', label: 'Plan', sortable: true },
  { key: 'stateText', label: 'State', sortable: true },
  { key: 'limits', label: 'Limits and usage', maxWidth: '28rem', fullValue: summaryText },
]
const organizationFilters: TableFilterDefinition[] = [
  { key: 'planName', label: 'Plan', allLabel: 'All plans' },
  { key: 'stateText', label: 'State', allLabel: 'All states' },
]
const organizationRows = computed<Row[]>(() => orgs.value.map(org => {
  const entitlement = entitlementFor(org.name)
  const limits = toLimits(entitlement?.status?.effectiveLimits)
  const usage = entitlement?.status?.usage as Record<string, unknown> | undefined
  return {
    name: org.name, displayName: org.displayName || org.name,
    planName: planLabel(String(entitlement?.spec?.plan ?? '')),
    state: orgState(org), stateText: orgState(org).status,
    limits, usage, window: entitlement?.status?.window,
    summaryText: limitsText(limits, usage ?? null),
  }
}))
const selectedOrgId = computed(() => typeof route.query.organization === 'string' ? route.query.organization : '')
const selectedOrg = computed(() => orgs.value.find(o => o.name === selectedOrgId.value))
function openOrganization(row: Row): void {
  organizationTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  void router.push({ query: { ...route.query, view: undefined, collection: undefined, resource: undefined, organization: String(row.name) } })
}
const organizationListLocation = computed(() => {
  const origin = organizationReturn(route.query, selectedOrgId.value)
  if (origin) return origin
  const query = { ...route.query }
  delete query.organization
  return { path: route.path, query }
})
const organizationListHref = computed(() => router.resolve(organizationListLocation.value).href)
const organizationBackLabel = computed(() => organizationReturn(route.query, selectedOrgId.value) ? 'Organization' : route.query.collection === 'plans' && typeof route.query.resource === 'string' ? `${planLabel(route.query.resource)} plan` : route.query.view === 'diagnostics' ? 'Diagnostics' : 'Organizations')
function planOrganizationLocation(row: Row) { return { path: route.path, query: { ...route.query, organization: String(row.name) } } }
function rememberOrganizationTrigger(): void { organizationTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null }
function openPlanOrganization(row: Row): void {
  rememberOrganizationTrigger()
  void router.push(planOrganizationLocation(row))
}
const detailReadError = computed(() => {
  if (error.value) return 'Could not refresh organization metering. Try again.'
  if (organizationError.value) return organizationError.value
  if (status.value && !status.value.installed) return 'Metering is not installed on this hub.'
  if (organizationsLoaded.value && !selectedOrg.value) return 'This organization is no longer available. Return to the organization list to choose another.'
  return null
})
function closeOrganization(): void { void router.push(organizationListLocation.value) }
watch([selectedOrgId, loaded], async ([name]) => {
  await nextTick()
  if (name) {
    const heading = organizationDetail.value?.querySelector<HTMLElement>('.k-resource-page__title')
    heading?.setAttribute('tabindex', '-1')
    heading?.focus()
  } else if (organizationTrigger?.isConnected) organizationTrigger.focus()
  else if (route.query.resource) {
    const heading = resourceDetail.value?.querySelector<HTMLElement>('.k-resource-page__title')
    heading?.setAttribute('tabindex', '-1')
    heading?.focus()
  }
})
// Plans and diagnostic records use the same collection-to-instance contract
// as organizations. Query-backed selection supports reload and browser history;
// the mounted collection retains its search, page, and keyboard focus.
const selectedCollectionKey = computed(() => typeof route.query.collection === 'string' ? route.query.collection : '')
const selectedResourceId = computed(() => typeof route.query.resource === 'string' ? route.query.resource : '')
const resourceDetailOpen = computed(() => !!selectedResourceId.value)
const anyDetailOpen = computed(() => !!selectedOrgId.value || resourceDetailOpen.value)
const selectedCollection = computed(() => collections.find(c => c.key === selectedCollectionKey.value))
const selectedResource = computed(() => selectedCollection.value ? (items.value[selectedCollection.value.key] ?? []).find(i => i.metadata.name === selectedResourceId.value) : undefined)
const selectedResourceRow = computed(() => (tableRows.value[selectedCollectionKey.value] ?? []).find(r => r.name === selectedResourceId.value))
const resourceTitle = computed(() => String(selectedResourceRow.value?.displayName || selectedResource.value?.metadata.name || selectedCollection.value?.noun || 'Metering resource'))
const resourceDetail = ref<HTMLElement | null>(null)
let resourceTrigger: HTMLElement | null = null
const resourceListLocation = computed(() => {
  const query = { ...route.query }
  delete query.collection
  delete query.resource
  query.view = selectedCollectionKey.value === 'plans' ? 'plans' : 'diagnostics'
  if (query.view === 'diagnostics') query.advanced = 'true'
  if (query.view === 'diagnostics' && selectedCollection.value) query.diagnostic = selectedCollectionKey.value
  return { path: route.path, query }
})
const resourceListHref = computed(() => router.resolve(resourceListLocation.value).href)
const resourceListLabel = computed(() => selectedCollectionKey.value === 'plans' ? 'Plans' : 'Diagnostics')
function openResource(collection: string, row: Row): void {
  resourceTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  void router.push({ query: { ...route.query, view: collection === 'plans' ? 'plans' : 'diagnostics', organization: undefined, advanced: collection === 'plans' ? route.query.advanced : 'true', diagnostic: collection === 'plans' ? route.query.diagnostic : collection, collection, resource: String(row.name) } })
}
function closeResource(): void { void router.push(resourceListLocation.value) }
watch([selectedResourceId, selectedCollectionKey, loaded], async ([name]) => {
  await nextTick()
  if (name) {
    const heading = resourceDetail.value?.querySelector<HTMLElement>('.k-resource-page__title')
    heading?.setAttribute('tabindex', '-1')
    heading?.focus()
  } else if (resourceTrigger?.isConnected) resourceTrigger.focus()
})
const resourceReadError = computed(() => {
  if (error.value) return 'Could not refresh this metering resource. Try again.'
  if (status.value && !status.value.installed) return 'Metering is not installed on this hub.'
  if (loaded.value && !selectedResource.value) return 'This resource is no longer available. Return to the list to choose another.'
  return null
})
const resourceConditions = computed(() => (selectedResource.value?.status?.conditions ?? []) as ConditionInfo[])
const resourceLimits = computed(() => toLimits(selectedCollectionKey.value === 'plans' ? selectedResource.value?.spec?.limits : selectedResource.value?.status?.effectiveLimits))
const assignedOrganizationRows = computed(() => organizationRows.value.filter(row => entitlementFor(String(row.name))?.spec?.plan === selectedResourceId.value || orgs.value.some(org => org.name === row.name && org.plan === selectedResourceId.value)))
function isCanonicalEntitlement(ent: Item | undefined): boolean {
  return !!ent && (orgs.value.some(org => org.name === ent.metadata.name) || ent.metadata.labels?.['railgrid.ai/organization'] === ent.metadata.name)
}
const selectedEntitlementCanonical = computed(() => isCanonicalEntitlement(selectedResource.value))
const additionalEntitlementRows = computed(() => (tableRows.value.entitlements ?? []).filter(row => row.name !== selectedOrgId.value && row.account === selectedOrgId.value))
const otherPlanEntitlementRows = computed(() => (tableRows.value.entitlements ?? []).filter(row => row.plan === selectedResourceId.value && !orgs.value.some(org => org.name === row.name)))
const entitlementReferenceColumns: Column[] = [{ key: 'name', label: 'Entitlement', primary: true }, { key: 'plan', label: 'Plan' }]
function entitlementLocation(row: Row) { return { path: route.path, query: { view: 'diagnostics', advanced: 'true', diagnostic: 'entitlements', collection: 'entitlements', resource: String(row.name) } } }
const resourceMeters = computed<Row[]>(() => Array.isArray(selectedResource.value?.spec?.meters) ? selectedResource.value.spec.meters as Row[] : [])
const resourceMembers = computed<Row[]>(() => Array.isArray(selectedResource.value?.spec?.members) ? selectedResource.value.spec.members.map((member: unknown) => typeof member === 'string' ? { cluster: member, path: '' } : member as Row) : [])
const resourceAccountResolution = computed(() => {
  const account = selectedResource.value?.spec?.account
  return account && typeof account === 'object' ? Object.entries(account as Record<string, unknown>).map(([key, value]) => ({ key, label: ({ resolve: 'Method', labelKey: 'Label key', accountPrefix: 'Account prefix' } as Record<string, string>)[key] || key, value: cell(value) })) : []
})
const resourceFacts = computed(() => {
  const item = selectedResource.value
  if (!item) return []
  const labels: Record<string, string> = { account: 'Organization', plan: 'Plan', customerRef: 'Customer', memberCount: 'Workspaces', reporter: 'Reporter', target: 'Target', meters: 'Meters', members: 'Workspaces', observedGeneration: 'Observed generation' }
  const omitted = new Set(['meters', 'displayName', 'limits', 'effectiveLimits', 'overrides', 'usage', 'window', 'conditions', 'observedGeneration'])
  return Object.entries({ ...item.spec, ...item.status }).filter(([key]) => !omitted.has(key) && !(key === 'account' && selectedCollectionKey.value === 'meteringconfigs')).map(([key, value]) => ({
    label: labels[key] || key.replace(/([A-Z])/g, ' $1').replace(/^./, c => c.toUpperCase()),
    value: key === 'account' && selectedCollectionKey.value !== 'meteringconfigs' ? displayNames.value.get(String(value)) || cell(value)
      : key === 'plan' ? planLabel(String(value))
      : key === 'members' && Array.isArray(value) ? String(value.length) : cell(value) || '—',
  }))
})
const diagnosticCollections = computed(() => collections.filter(c => !['plans'].includes(c.key)))
const selectedDiagnostic = computed(() => diagnosticCollections.value.some(c => c.key === route.query.diagnostic) ? String(route.query.diagnostic) : 'entitlements')
function selectDiagnostic(key: string): void {
  void router.push({ query: { ...route.query, view: 'diagnostics', diagnostic: key } })
}
const meteringConditionSemantics = {
  OverHardLimit: { polarity: 'negative', severity: 'danger' },
  OverSoftLimit: { polarity: 'negative', severity: 'warning' },
} as const
const advancedDiagnosticsOpen = ref(route.query.advanced === 'true' || (!!route.query.collection && route.query.collection !== 'plans'))
watch(() => route.query.advanced, value => { if (value === 'true') advancedDiagnosticsOpen.value = true })
const diagnosticColumns: Column[] = [
  { key: 'displayName', label: 'Organization', primary: true, sortable: true },
  { key: 'planState', label: 'Plan application', sortable: true },
  { key: 'usageState', label: 'Usage snapshot', sortable: true },
  { key: 'reportedAtLabel', label: 'Last reported' },
]
const diagnosticFilters: TableFilterDefinition[] = [
  { key: 'planState', label: 'Plan application', allLabel: 'All plan states' },
  { key: 'usageState', label: 'Usage snapshot', allLabel: 'All usage states' },
]
const diagnosticOrganizationRows = computed(() => orgs.value.filter(org => !org.deletionRequestedAt).map(org => {
  const ent = entitlementFor(org.name)
  const assignedPlan = org.plan || String(ent?.spec?.plan || status.value?.defaultPlan || '')
  const plan = planApplication(org, ent, planFor(assignedPlan))
  const usage = usageFreshness(ent, diagnosticNow.value)
  return { name: org.name, displayName: org.displayName || org.name, planName: planLabel(assignedPlan),
    planState: plan.state, usageState: usage.state, planCheck: plan, usageCheck: usage,
    reportedAt: usage.reportedAt, reportedAtLabel: formatDiagnosticTime(usage.reportedAt),
    attention: plan.tone !== 'success' || usage.tone !== 'success',
  }
}).sort((a, b) => Number(b.attention) - Number(a.attention) || a.displayName.localeCompare(b.displayName)))
function diagnosticCheckRow(row: Row): (typeof diagnosticOrganizationRows.value)[number] { return row as (typeof diagnosticOrganizationRows.value)[number] }
const planAppliedCount = computed(() => diagnosticOrganizationRows.value.filter(row => row.planState === 'Applied').length)
const usageRecentCount = computed(() => diagnosticOrganizationRows.value.filter(row => row.usageState === 'Recent').length)
function formatDiagnosticTime(value: string): string {
  const time = Date.parse(value)
  return Number.isFinite(time) ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' }).format(time) : 'Not reported'
}
function diagnosticOrganizationLocation(row: Row) { return { path: route.path, query: { ...route.query, view: 'diagnostics', collection: undefined, resource: undefined, organization: String(row.name) } } }
function openDiagnosticOrganization(row: Row): void {
  rememberOrganizationTrigger()
  void router.push(diagnosticOrganizationLocation(row))
}
const selectedEntitlement = computed(() => assignment.value ? entitlementFor(assignment.value.name) : undefined)
const selectedTarget = computed(() => planFor(assignment.value?.plan || status.value?.defaultPlan || ''))
const previewLimits = computed(() => effectiveLimits(selectedTarget.value, selectedEntitlement.value?.spec?.overrides))
const comparisonLimits = computed(() => {
  const next = previewLimits.value.map(l => ({ ...l, removed: false }))
  for (const old of toLimits(selectedEntitlement.value?.status?.effectiveLimits)) {
    if (!next.some(l => l.meter === old.meter)) next.push({ ...old, max: '', enforce: 'Meter', removed: true })
  }
  return next
})
function amount(value: unknown): string {
  if (value === undefined || value === null || value === '') return '—'
  const text = String(value)
  const n = Number(text)
  return /^\d+$/.test(text) && Number.isSafeInteger(n) ? new Intl.NumberFormat().format(n) : text
}
function orgState(org: AdminOrg): EntitlementState {
  const ent = entitlementFor(org.name)
  if ((org.plan && ent?.spec?.plan !== org.plan) || (ent?.metadata.generation !== undefined && Number(ent.status?.observedGeneration ?? 0) < ent.metadata.generation)) return { status: 'applying', tone: 'warning', title: 'The requested plan or limits have not yet been applied' }
  return entitlementState(ent?.status)
}
const overTarget = computed(() => previewLimits.value.filter(l => l.enforce === 'Hard' && l.max !== '' && Number(selectedEntitlement.value?.status?.usage && (selectedEntitlement.value.status.usage as Record<string, unknown>)[l.meter]) > Number(l.max)))
const assignmentDirty = computed(() => !!assignment.value && assignment.value.plan !== assignment.value.initial)
const assignmentWritable = computed(() => !!assignment.value && assignmentNeedsWrite(orgs.value.find(o => o.name === assignment.value!.name)?.plan, selectedEntitlement.value, status.value?.defaultPlan, assignment.value.plan))
const planDirty = computed(() => !!planDraft.value && JSON.stringify(planDraft.value) !== initialPlan.value)
const overridesDirty = computed(() => !!entitlementDraft.value && (entitlementDraft.value.advanced ? JSON.stringify({ plan: entitlementDraft.value.plan, overrides: entitlementDraft.value.overrides }) !== initialEntitlement.value : JSON.stringify(entitlementDraft.value.overrides) !== initialOverrides.value))
const planValidation = computed(() => planDraft.value ? limitsError(planDraft.value.limits) : null)
const overrideValidation = computed(() => entitlementDraft.value ? limitsError(entitlementDraft.value.overrides) : null)

async function read<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await authFetch(path, init)
  if (!response.ok) {
    let message = `Request failed (${response.status}). Retry when the service is available.`
    try { message = ((await response.json()) as { error?: string }).error || message } catch { /* gateway response */ }
    throw new Error(message)
  }
  return response.status === 204 ? undefined as T : await response.json() as T
}

function editPlan(name: string): void {
  const plan = planFor(name)
  actionError.value = null
  planDraft.value = { name, isNew: !plan, displayName: String(plan?.spec?.displayName ?? ''), limits: toLimits(plan?.spec?.limits) }
  initialPlan.value = JSON.stringify(planDraft.value)
}
function editEntitlement(name: string, advanced = false): void {
  const ent = entitlementFor(name)
  if (!ent) return
  actionError.value = null
  entitlementDraft.value = { name: ent.metadata.name, plan: String(ent.spec?.plan ?? ''), overrides: toLimits(ent.spec?.overrides), advanced: advanced && !isCanonicalEntitlement(ent) }
  initialOverrides.value = JSON.stringify(entitlementDraft.value.overrides)
  initialEntitlement.value = JSON.stringify({ plan: entitlementDraft.value.plan, overrides: entitlementDraft.value.overrides })
}
function changePlan(name: string): void {
  const org = orgs.value.find(o => o.name === name)
  if (!org || org.deletionRequestedAt || !writable.value || assignmentPending(name) || busy.value) return
  actionError.value = null
  const selected = assignmentSelection(org.plan, entitlementFor(name), status.value?.defaultPlan)
  assignment.value = { name, plan: selected, initial: selected }
}

function checkPending(): void {
  for (const p of pendingAssignments.value) {
    const org = orgs.value.find(o => o.name === p.name)
    if (!organizationError.value && org && assignmentApplied(org.plan, p.requested, planFor(p.target), entitlementFor(p.name))) {
      notice.value = `${planLabel(p.target)} plan applied to ${orgLabel(p.name)}. Organization overrides were preserved.`
      delete pending.value[p.name]
    } else if (Date.now() - p.started >= 90_000) p.timedOut = true
  }
  if (!pendingAssignments.value.length && poll) clearTimeout(poll)
}
function schedulePoll(): void {
  if (poll) clearTimeout(poll)
  if (disposed || !pendingAssignments.value.some(p => !p.timedOut)) return
  poll = setTimeout(async () => { await refresh(false, 'background'); schedulePoll() }, 3000)
}
function retryPending(name: string): void {
  const p = pending.value[name]
  if (p) { p.started = Date.now(); p.timedOut = false }
  void refresh().then(schedulePoll)
}
function stopWatching(name: string): void {
  delete pending.value[name]
  if (!pendingAssignments.value.length && poll) clearTimeout(poll)
  notice.value = `Stopped watching ${orgLabel(name)}. The saved plan assignment remains in effect.`
}

async function saveAssignment(): Promise<void> {
  const draft = assignment.value
  if (!draft || !assignmentWritable.value || !selectedTarget.value) return
  const target = selectedTarget.value.metadata.name
  busy.value = draft.name
  actionError.value = null
  try {
    await read(`/api/admin/organizations/${encodeURIComponent(draft.name)}/plan`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ plan: draft.plan }),
    })
    const acknowledgedOrg = orgs.value.find(org => org.name === draft.name)
    if (acknowledgedOrg) acknowledgedOrg.plan = draft.plan || undefined
    pending.value[draft.name] = { name: draft.name, requested: draft.plan, target, started: Date.now(), timedOut: false }
    notice.value = ''
    assignment.value = null
    await refresh()
    schedulePoll()
  } catch (err) { actionError.value = (err as Error).message }
  finally { busy.value = null }
}
async function write(method: 'PUT' | 'DELETE', path: string, body?: unknown): Promise<void> {
  await read(`/api/admin/metering/${path}`, { method, headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
}
async function savePlan(): Promise<void> {
  const draft = planDraft.value
  if (!draft || planValidation.value) return
  busy.value = draft.name || 'new-plan'
  actionError.value = null
  try {
    await write('PUT', `plans/${encodeURIComponent(draft.name.trim())}`, { displayName: draft.displayName.trim(), limits: draft.limits })
    notice.value = `Plan ${draft.displayName || draft.name} saved. Metering will update effective limits; organization overrides are preserved.`
    planDraft.value = null
    await refresh()
  } catch (err) { actionError.value = (err as Error).message }
  finally { busy.value = null }
}
async function deletePlan(name: string): Promise<void> {
  const using = planReferenceCount(name)
  if (using || planHasReferences(name) || name === status.value?.defaultPlan) return
  if (!await confirmDialog({ title: `Delete plan “${planLabel(name)}”?`, message: 'Organizations can no longer be placed on it. Existing usage data is kept.', danger: true, confirmLabel: 'Delete plan' })) return
  busy.value = name
  actionError.value = null
  try {
    await write('DELETE', `plans/${encodeURIComponent(name)}`)
    notice.value = `Plan ${name} deleted.`
    if (selectedCollectionKey.value === 'plans' && selectedResourceId.value === name) closeResource()
    await refresh()
  }
  catch (err) { actionError.value = (err as Error).message }
  finally { busy.value = null }
}
async function saveEntitlement(): Promise<void> {
  const draft = entitlementDraft.value
  if (!draft || overrideValidation.value) return
  busy.value = draft.name
  actionError.value = null
  try {
    // Read the latest plan so saving overrides never restores an old plan.
    const latest = await read<{ items: Item[] }>('/api/admin/metering/entitlements')
    const ent = latest.items.find(e => e.metadata.name === draft.name)
    if (!ent) throw new Error('This entitlement no longer exists. Close the editor and refresh.')
    if (JSON.stringify(toLimits(ent.spec?.overrides)) !== initialOverrides.value) throw new Error('Organization overrides changed while editing. Close the editor and refresh before trying again.')
    if (draft.advanced && JSON.stringify({ plan: String(ent.spec?.plan ?? ''), overrides: toLimits(ent.spec?.overrides) }) !== initialEntitlement.value) throw new Error('This entitlement changed while editing. Close and reopen it before saving.')
    if (draft.advanced && isCanonicalEntitlement(ent)) throw new Error('This is now an organization entitlement. Use organization plan assignment to change its plan.')
    await write('PUT', `entitlements/${encodeURIComponent(draft.name)}`, { plan: draft.advanced ? draft.plan : String(ent.spec?.plan ?? ''), overrides: draft.overrides })
    notice.value = draft.advanced ? `Entitlement ${draft.name} saved. Metering will update effective limits.` : 'Organization overrides saved. Metering will update effective limits.'
    entitlementDraft.value = null
    await refresh()
  } catch (err) { actionError.value = (err as Error).message }
  finally { busy.value = null }
}
onMounted(() => {
  void refresh()
  diagnosticClock = setInterval(() => { diagnosticNow.value = Date.now() }, 30_000)
})
onBeforeUnmount(() => { if (diagnosticClock) clearInterval(diagnosticClock); disposed = true; if (poll) clearTimeout(poll) })
</script>

<template>
  <section>
    <div v-show="!anyDetailOpen">
    <div class="mb-1 flex items-center gap-2">
      <h2 class="text-xl font-semibold text-text-primary">Metering</h2>
      <StatusBadge v-if="status?.installed" :status="status.ready ? 'ready' : 'not ready'" :tone="readyTone" />
      <StatusBadge v-else-if="status" status="not installed" tone="muted" />
      <button type="button" class="k-btn k-btn--ghost ml-auto" :disabled="foregroundLoading || !!busy" :aria-busy="foregroundLoading || undefined" @click="refresh(true)">
        <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': foregroundLoading }" :stroke-width="1.75" aria-hidden="true" />{{ foregroundLoading ? 'Refreshing…' : 'Refresh' }}
      </button>
    </div>
    <p class="mb-5 text-sm text-text-muted">Manage organization plans, usage, and limits.</p>
    <div v-if="status?.installed && organizationError && tab === 'plans'" class="metering-message mb-4" role="alert"><p>{{ organizationError }} {{ organizationsLoaded ? 'Showing the last loaded organizations.' : 'Organization checks are unavailable.' }}</p><button type="button" class="k-btn mt-3" :disabled="organizationsLoading" @click="refreshOrganizations">Retry organizations</button></div>

    <div v-if="error" class="metering-message mb-4" role="alert">
      <p>Could not refresh metering. {{ loaded ? 'Showing the last loaded data.' : 'Retry to load organization plans and usage.' }}</p>
      <details class="mt-2"><summary>Technical details</summary><p class="mt-2 break-words font-mono text-xs">{{ error }}</p></details>
      <button type="button" class="k-btn mt-3" :disabled="loading" @click="refresh(true)">Retry</button>
    </div>
    <p v-if="actionError && !planDraft && !assignment && !entitlementDraft" class="mb-4 text-sm text-danger" role="alert">{{ actionError }}</p>
    <p v-if="!status && loading" class="text-sm text-text-muted" role="status">Loading metering…</p>
    <div v-if="status && !status.installed" class="k-card p-5">
      <h3 class="mb-2 font-semibold">Metering is not installed</h3>
      <p class="text-sm text-text-secondary">Install contrib-metering in <code>{{ status.workspace }}</code> to manage organization plans.</p>
      <pre class="k-code mt-4 overflow-x-auto p-3 text-xs">helm install metering deploy/chart -n metering --create-namespace \
  --set workspacePath={{ status.workspace }} \
  --set admin.tokenSecretRef.name=metering-admin</pre>
    </div>

    <template v-if="status?.installed">
      <div v-if="!status.ready" class="metering-message mb-4" role="status">
        <p>Metering is not ready. Effective limits and usage may be out of date.</p>
        <details class="mt-2"><summary>Service details</summary><p class="mt-2 break-words font-mono text-xs">{{ status.reason }}</p></details>
      </div>
      <nav class="k-tabs mb-5" aria-label="Metering views">
        <button v-for="t in (['organizations', 'plans', 'diagnostics'] as const)" :key="t" type="button" class="k-tab capitalize" :class="{ 'k-tab--active': tab === t }" :aria-current="tab === t ? 'page' : undefined" @click="selectTab(t)">{{ t }}</button>
      </nav>
      <p v-if="notice" class="metering-message metering-message--success mb-4" role="status" aria-live="polite">{{ notice }}</p>
      <div v-for="p in pendingAssignments" :key="p.name" class="metering-message mb-4" role="status" aria-live="polite">
        <p>{{ p.timedOut ? 'Plan assignment saved; application is not yet confirmed.' : 'Plan assignment saved. Waiting for metering to apply it…' }}</p>
        <p class="mt-1 text-xs">{{ orgLabel(p.name) }} → {{ planLabel(p.target) }}. Organization overrides are preserved.</p>
        <div class="mt-3 flex flex-wrap gap-2"><button v-if="p.timedOut" type="button" class="k-btn" :disabled="loading" @click="retryPending(p.name)">Check application again</button><button type="button" class="k-btn k-btn--ghost" @click="stopWatching(p.name)">Stop watching</button></div>
        <p class="mt-2 text-xs">Stopping this check does not undo the saved assignment. Other organizations remain editable.</p>
      </div>

      <div v-show="tab === 'organizations'">
        <div v-show="!selectedOrgId">
          <ResourceTable
            :columns="organizationColumns" :rows="organizationRows" aria-label="Organizations" row-key="name"
            searchable search-placeholder="Search organizations…" :search-keys="['displayName', 'name', 'planName', 'stateText', 'summaryText']"
            :filters="organizationFilters" :default-sort="{ key: 'displayName', direction: 'asc' }" paginated :page-size="10" :loaded="organizationsLoaded" :loading="loading || organizationsLoading" :refresh-mode="refreshMode" :error="organizationError" :stale="organizationsLoaded && !!organizationError" retryable @retry="refreshOrganizations"
            :interactive="true" :row-aria-label="(row: Row) => `View metering for ${row.displayName}`"
            empty-text="No organizations found." @row-click="openOrganization"
          >
            <template #displayName="{ value, row }"><button type="button" class="k-btn k-btn--ghost k-table-resource-link" :aria-label="`Open metering for ${value}`" @click.stop="openOrganization(row)" @keydown.space.prevent.stop="openOrganization(row)">{{ value }}</button></template>
            <template #planName="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
            <template #stateText="{ row }"><StatusBadge :status="stateOf(row).status" :tone="stateOf(row).tone" :title="stateOf(row).title" /></template>
            <template #limits="{ row }"><MeteringUsageList variant="summary" :limits="limitsOf(row)" :usage="usageOf(row)" :window="windowOf(row)" /></template>
          </ResourceTable>
        </div>

      </div>

      <div v-show="tab === 'plans'">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <p class="text-sm text-text-secondary">Shared limits applied to organizations on each plan.</p>
          <button v-if="writable" type="button" class="k-btn k-btn--primary" :disabled="!!busy || foregroundLoading || !!error" @click="editPlan('')"><Plus class="h-4 w-4" />New plan</button>
        </div>
        <ResourceTable :columns="collections[0]!.columns" :rows="tableRows.plans ?? []" aria-label="Plans" row-key="name" searchable search-placeholder="Search plans…" :search-keys="collections[0]!.searchKeys" paginated :loaded="loaded" :loading="loading" :refresh-mode="refreshMode" empty-text="No plans yet. Create a plan to assign organization limits." :interactive="true" :row-aria-label="(row: Row) => `View Plan ${row.displayName}`" :default-sort="{ key: 'displayName', direction: 'asc' }" @row-click="(row: Row) => openResource('plans', row)">
          <template #displayName="{ row }"><button type="button" class="k-btn k-btn--ghost k-table-resource-link" :aria-label="`Open plan ${row.displayName}`" @click.stop="openResource('plans', row)" @keydown.space.prevent.stop="openResource('plans', row)">{{ row.displayName }}</button><span v-if="row.name === status.defaultPlan" class="ml-2 text-xs text-text-muted">default</span></template>
          <template #limits="{ row }"><MeteringUsageList variant="summary" :limits="limitsOf(row)" /></template>
          <template #actions="{ row }">
            <div v-if="writable" class="flex items-center justify-end gap-2">
              <ResourceTableEditButton :label="`Edit plan ${row.name}`" :disabled="!!busy || foregroundLoading || !!error" @click="editPlan(String(row.name))" />
              <ResourceTableDeleteButton :label="`Delete plan ${row.name}`" :busy-label="`Deleting plan ${row.name}…`" :busy="busy === row.name" :disabled="!!busy || !!error || foregroundLoading || row.name === status.defaultPlan || planHasReferences(String(row.name))" @click="deletePlan(String(row.name))" />
            </div>
          </template>
        </ResourceTable>
      </div>

      <div v-show="tab === 'diagnostics'">
        <div v-if="loaded" class="mb-5 space-y-4">
          <ResourceSectionCard id="metering-diagnostic-checks" title="Metering checks" description="Confirm plans have applied and usage is being reported before inspecting raw resources.">
            <dl class="space-y-5 text-sm">
              <div class="flex flex-wrap items-start justify-between gap-2"><div><dt class="font-medium text-text-primary">Is metering healthy?</dt><dd class="mt-1 text-text-secondary">{{ status.ready ? 'The controller readiness probe passed.' : 'The controller is not ready. Refresh to retry, then inspect service details below.' }}</dd><dd class="mt-1 text-xs text-text-muted">This check covers the controller; webhook and audit delivery are not probed here.</dd></div><StatusBadge :status="status.ready ? 'Controller ready' : 'Controller not ready'" :tone="status.ready ? 'success' : 'danger'" /></div>
              <div class="flex flex-wrap items-start justify-between gap-2"><div><dt class="font-medium text-text-primary">Have plans applied?</dt><dd class="mt-1 text-text-secondary">{{ organizationError ? 'Organization data is unavailable. Retry the organization check.' : organizationsLoading ? 'Refreshing organization checks…' : !organizationsLoaded ? 'Loading organization checks…' : diagnosticOrganizationRows.length ? `${planAppliedCount} of ${diagnosticOrganizationRows.length} active organizations have matching assignments and reconciled limits.` : 'There are no active organizations to check.' }}</dd></div><StatusBadge :status="organizationError ? 'Unavailable' : organizationsLoading ? 'Checking' : !organizationsLoaded ? 'Unavailable' : !diagnosticOrganizationRows.length ? 'No organizations' : planAppliedCount === diagnosticOrganizationRows.length ? 'All applied' : 'Needs review'" :tone="organizationError || organizationsLoading || !organizationsLoaded || !diagnosticOrganizationRows.length ? 'muted' : planAppliedCount === diagnosticOrganizationRows.length ? 'success' : 'warning'" /></div>
              <div class="flex flex-wrap items-start justify-between gap-2"><div><dt class="font-medium text-text-primary">Is usage current?</dt><dd class="mt-1 text-text-secondary">{{ organizationError ? 'Organization data is unavailable. Retry the organization check.' : organizationsLoading ? 'Refreshing organization checks…' : !organizationsLoaded ? 'Loading organization checks…' : diagnosticOrganizationRows.length ? `${usageRecentCount} of ${diagnosticOrganizationRows.length} active organizations have usage snapshots from the last five minutes.` : 'Usage reporting will appear once an organization is metered.' }}</dd><dd class="mt-1 text-xs text-text-muted">Snapshot freshness does not confirm delivery from every upstream meter.</dd></div><StatusBadge :status="organizationError ? 'Unavailable' : organizationsLoading ? 'Checking' : !organizationsLoaded ? 'Unavailable' : !diagnosticOrganizationRows.length ? 'No snapshots' : usageRecentCount === diagnosticOrganizationRows.length ? 'Recent snapshots' : 'Needs review'" :tone="organizationError || organizationsLoading || !organizationsLoaded || !diagnosticOrganizationRows.length ? 'muted' : usageRecentCount === diagnosticOrganizationRows.length ? 'success' : 'warning'" /></div>
            </dl>
            <p v-if="lastCheckedAt" class="mt-5 text-xs text-text-muted">Metering data checked: <time :datetime="lastCheckedAt">{{ formatDiagnosticTime(lastCheckedAt) }}</time><span v-if="organizationsCheckedAt"> · Organizations checked: <time :datetime="organizationsCheckedAt">{{ formatDiagnosticTime(organizationsCheckedAt) }}</time></span>{{ error ? ' · Showing the last loaded results.' : '' }}</p>
          </ResourceSectionCard>
          <ResourceSectionCard id="metering-organization-checks" title="Organization checks" description="Organizations needing review appear first. Open an organization to inspect its plan, usage, and overrides.">
            <ResourceTable :columns="diagnosticColumns" :rows="diagnosticOrganizationRows" aria-label="Organization metering checks" row-key="name" searchable search-placeholder="Search organization checks…" :search-keys="['displayName', 'name', 'planName', 'planState', 'usageState']" :filters="diagnosticFilters" paginated :page-size="10" :loaded="organizationsLoaded" :loading="loading || organizationsLoading" :refresh-mode="refreshMode" :error="organizationError" :stale="organizationsLoaded && !!organizationError" retryable @retry="refreshOrganizations" :interactive="true" :row-aria-label="(row: Row) => `Inspect metering for ${row.displayName}`" empty-text="No active organizations to check." @row-click="openDiagnosticOrganization">
              <template #displayName="{ value, row }"><RouterLink :to="diagnosticOrganizationLocation(row)" class="k-btn k-btn--ghost k-table-resource-link" @click="rememberOrganizationTrigger">{{ value }}</RouterLink></template>
              <template #planState="{ row }"><StatusBadge :status="String(row.planState)" :tone="diagnosticCheckRow(row).planCheck.tone" /><p class="mt-1 text-xs text-text-muted">{{ row.planName }}</p><p v-if="row.planState !== 'Applied'" class="mt-1 whitespace-normal text-xs text-text-secondary">{{ diagnosticCheckRow(row).planCheck.detail }}</p></template>
              <template #usageState="{ row }"><StatusBadge :status="String(row.usageState)" :tone="diagnosticCheckRow(row).usageCheck.tone" /><p v-if="row.usageState !== 'Recent'" class="mt-1 whitespace-normal text-xs text-text-secondary">{{ diagnosticCheckRow(row).usageCheck.detail }}</p></template>
              <template #reportedAtLabel="{ row }"><time v-if="row.reportedAt" :datetime="String(row.reportedAt)">{{ row.reportedAtLabel }}</time><span v-else>Not reported</span></template>
            </ResourceTable>
          </ResourceSectionCard>
        </div>
        <p v-else-if="loading" class="mb-5 text-sm text-text-muted" role="status">Checking plan application, usage reporting, and controller readiness…</p>
        <details :open="advancedDiagnosticsOpen" class="text-sm" @toggle="advancedDiagnosticsOpen = ($event.target as HTMLDetailsElement).open">
          <summary class="font-medium text-text-primary">Advanced details</summary>
          <p class="mb-4 mt-3 text-sm text-text-secondary">Inspect metering resources and service endpoints when the checks above need investigation.</p>
        <div class="mb-4">
          <label for="metering-diagnostic-collection" class="mb-2 block text-sm font-medium">Diagnostic collection</label>
          <select id="metering-diagnostic-collection" class="k-input w-full p-2.5 text-base sm:text-sm" :value="selectedDiagnostic" @change="selectDiagnostic(($event.target as HTMLSelectElement).value)">
            <option v-for="c in diagnosticCollections" :key="c.key" :value="c.key">{{ c.label }} ({{ tableRows[c.key]?.length || 0 }})</option>
          </select>
        </div>
        <details class="mb-4 text-sm text-text-muted"><summary>Metering service details</summary>
        <ResourceSectionCard id="metering-service" title="Metering service" description="Deployment endpoints and version for troubleshooting.">
        <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm text-text-secondary">
          <dt>Workspace</dt><dd class="break-all font-mono">{{ status.workspace }}</dd><dt>Version</dt><dd class="font-mono">{{ status.endpoints?.version || '—' }}</dd>
          <dt>Controller</dt><dd class="break-all font-mono">{{ status.endpoints?.portalURL }}</dd><dt>Webhook</dt><dd class="break-all font-mono">{{ status.endpoints?.webhookURL || '—' }}</dd><dt>Audit sink</dt><dd class="break-all font-mono">{{ status.endpoints?.auditURL || '—' }}</dd>
        </dl>
        </ResourceSectionCard>
        </details>
        <div v-for="c in diagnosticCollections" v-show="selectedDiagnostic === c.key" :key="c.key" class="mt-4">
          <h3 class="mb-2 text-sm font-semibold">{{ c.label }}</h3>
          <ResourceTable :columns="c.columns" :rows="tableRows[c.key] ?? []" :aria-label="c.label" row-key="name" :interactive="true" :row-aria-label="(row: Row) => `View ${rowLabel(c, row)}`" searchable :search-placeholder="c.searchPlaceholder" :search-keys="c.searchKeys" paginated :loaded="loaded" :loading="loading" :refresh-mode="refreshMode" :empty-text="`No ${c.label.toLowerCase()}.`" @row-click="(row: Row) => openResource(c.key, row)">
            <template v-if="c.key !== 'meteringconfigs'" #displayName="{ value, row }"><button type="button" class="k-btn k-btn--ghost k-table-resource-link" :aria-label="`Open ${rowLabel(c, row)}`" @click.stop="openResource(c.key, row)" @keydown.space.prevent.stop="openResource(c.key, row)">{{ value }}</button></template>
            <template v-if="c.key === 'meteringconfigs'" #name="{ value, row }"><button type="button" class="k-btn k-btn--ghost k-table-resource-link" :aria-label="`Open ${rowLabel(c, row)}`" @click.stop="openResource(c.key, row)" @keydown.space.prevent.stop="openResource(c.key, row)">{{ value }}</button></template>
            <template v-if="c.key === 'entitlements'" #plan="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
            <template v-if="c.key === 'entitlements'" #limits="{ row }"><MeteringUsageList variant="summary" :limits="limitsOf(row)" :usage="usageOf(row)" :window="windowOf(row)" /></template>
            <template v-if="c.key === 'entitlements'" #state="{ row }"><StatusBadge :status="stateOf(row).status" :tone="stateOf(row).tone" :title="stateOf(row).title" /></template>
            <template v-if="c.key === 'accounts'" #usage="{ row }"><MeteringUsageList variant="summary" :usage="usageOf(row)" :window="windowOf(row)" /></template>
          </ResourceTable>
        </div>
        </details>
      </div>
    </template>

    </div>

    <div v-if="selectedOrgId" ref="organizationDetail">
      <ResourceBackLink :href="organizationListHref" :aria-label="`Back to ${organizationBackLabel === 'Organization' ? 'organization' : organizationBackLabel === 'Organizations' ? 'organizations' : organizationBackLabel === 'Diagnostics' ? 'diagnostics' : organizationBackLabel}`" @back="closeOrganization">{{ organizationBackLabel }}</ResourceBackLink>
      <ResourcePage
        :title="selectedOrg?.displayName || selectedOrg?.name || 'Organization'" kind="Organization"
        :loaded="!!selectedOrg" :loading="loading || organizationsLoading" :refresh-mode="refreshMode" :error="detailReadError"
        :stale="!!selectedOrg && !!detailReadError" retryable @retry="refresh(true)"
      >
        <template #meta>
          <span>Metering</span>
          <template v-if="selectedOrg"><span aria-hidden="true">·</span><span>{{ planLabel(String(entitlementFor(selectedOrg.name)?.spec?.plan ?? '')) }}</span><span aria-hidden="true">·</span><span>{{ sourceFor(selectedOrg) }}</span></template>
        </template>
        <template v-if="selectedOrg" #status><StatusBadge :status="orgState(selectedOrg).status" :tone="orgState(selectedOrg).tone" :title="orgState(selectedOrg).title" /></template>
        <template #actions>
          <div class="flex flex-wrap items-center gap-2" role="group" aria-label="Organization metering actions">
            <button type="button" class="k-btn k-btn--primary" :disabled="!selectedOrg || !writable || !!busy || foregroundLoading || !!detailReadError || assignmentPending(selectedOrg?.name || '') || !!selectedOrg?.deletionRequestedAt || !planItems.length" :aria-label="`Change plan for ${selectedOrg?.displayName || selectedOrg?.name || 'organization'}`" @click="selectedOrg && changePlan(selectedOrg.name)">Change plan</button>
            <button type="button" class="k-btn k-btn--ghost" :disabled="foregroundLoading || !!busy" :aria-busy="foregroundLoading || undefined" @click="refresh(true)"><RefreshCw class="h-4 w-4" :class="{ 'animate-spin': foregroundLoading }" aria-hidden="true" />{{ foregroundLoading ? 'Refreshing…' : 'Refresh' }}</button>
          </div>
        </template>
        <template v-if="selectedOrg">
          <p v-if="notice" class="metering-message metering-message--success mb-4" role="status" aria-live="polite">{{ notice }}</p>
          <p v-if="actionError && !planDraft && !assignment && !entitlementDraft" class="mb-4 text-sm text-danger" role="alert">{{ actionError }}</p>
          <div v-for="p in pendingAssignments.filter(p => p.name === selectedOrgId)" :key="p.name" class="metering-message mb-4" role="status" aria-live="polite">
            <p>{{ p.timedOut ? 'Plan assignment saved; application is not yet confirmed.' : 'Plan assignment saved. Waiting for metering to apply it…' }}</p>
            <p class="mt-1 text-xs">{{ orgLabel(p.name) }} → {{ planLabel(p.target) }}. Organization overrides are preserved.</p>
            <div class="mt-3 flex flex-wrap gap-2"><button v-if="p.timedOut" type="button" class="k-btn" :disabled="loading" @click="retryPending(p.name)">Check application again</button><button type="button" class="k-btn k-btn--ghost" @click="stopWatching(p.name)">Stop watching</button></div>
            <p class="mt-2 text-xs">Stopping this check does not undo the saved assignment. You can choose a different plan after a timeout.</p>
          </div>
          <p v-if="status?.installed && !status.ready" class="metering-message mb-4" role="status">Metering is not ready. Effective limits and usage may be out of date.</p>
          <div class="space-y-4">
            <ResourceSectionCard id="organization-metering-usage" title="Usage and limits" description="Current usage against this organization's effective limits.">
              <MeteringUsageList v-if="entitlementFor(selectedOrg.name)" variant="grid" :limits="toLimits(entitlementFor(selectedOrg.name)?.status?.effectiveLimits)" :usage="(entitlementFor(selectedOrg.name)?.status?.usage as Record<string, unknown> | undefined)" :window="(entitlementFor(selectedOrg.name)?.status?.window as UsageWindow | undefined)" />
              <p v-else class="text-sm text-text-muted">Metering is preparing this organization's entitlement.</p>
            </ResourceSectionCard>
            <ResourceSectionCard id="organization-metering-overrides" title="Organization overrides" description="Overrides replace the plan limit for the same meter on this organization only.">
              <template v-if="writable" #actions><button type="button" class="k-btn k-btn--ghost" :disabled="!entitlementFor(selectedOrg.name) || !!busy || foregroundLoading || !!detailReadError || assignmentPending(selectedOrg.name)" @click="editEntitlement(selectedOrg.name)">Edit overrides</button></template>
              <MeteringUsageList v-if="toLimits(entitlementFor(selectedOrg.name)?.spec?.overrides).length" variant="grid" :limits="toLimits(entitlementFor(selectedOrg.name)?.spec?.overrides)" />
              <p v-else class="text-sm text-text-muted">No organization overrides. Plan limits apply.</p>
            </ResourceSectionCard>
            <ResourceSectionCard v-if="additionalEntitlementRows.length" id="organization-additional-entitlements" title="Additional entitlements" description="These resources share this organization's account. They are managed separately from its primary plan.">
              <ResourceTable :columns="entitlementReferenceColumns" :rows="additionalEntitlementRows" aria-label="Additional entitlements" row-key="name" paginated :page-size="10"><template #name="{ row, value }"><RouterLink :to="entitlementLocation(row)" class="k-btn k-btn--ghost k-table-resource-link">{{ value }}</RouterLink></template></ResourceTable>
            </ResourceSectionCard>
            <details class="text-xs text-text-muted">
              <summary>Technical details</summary>
              <dl class="mt-3 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2"><dt>Organization ID</dt><dd class="break-all font-mono">{{ selectedOrg.name }}</dd><dt>Workspace</dt><dd class="break-all font-mono">{{ selectedOrg.workspacePath || '—' }}</dd></dl>
              <p v-if="error || (status?.installed && !status.ready)" class="mt-3 break-words font-mono">{{ error || status?.reason }}</p>
            </details>
          </div>
        </template>
      </ResourcePage>
    </div>

    <div v-if="resourceDetailOpen" v-show="!selectedOrgId" ref="resourceDetail">
      <ResourceBackLink :href="resourceListHref" :aria-label="`Back to ${resourceListLabel.toLowerCase()}`" @back="closeResource">{{ resourceListLabel }}</ResourceBackLink>
      <ResourcePage :title="resourceTitle" :kind="selectedCollection?.noun || 'Metering resource'"
        :loaded="!!selectedResource" :loading="loading" :refresh-mode="refreshMode" :error="resourceReadError"
        :stale="!!selectedResource && !!resourceReadError" retryable @retry="refresh(true)">
        <template #meta><span>Metering</span><template v-if="selectedCollectionKey === 'plans' && selectedResource"><span aria-hidden="true">·</span><span>{{ organizationsLoaded && !organizationError ? `${planCount(selectedResourceId)} ${planCount(selectedResourceId) === 1 ? 'organization' : 'organizations'}` : 'Organization count unavailable' }} · {{ planReferenceCount(selectedResourceId) }} {{ planReferenceCount(selectedResourceId) === 1 ? 'entitlement' : 'entitlements' }}</span><span v-if="selectedResourceId === status?.defaultPlan" class="k-badge k-badge--muted">Platform default</span></template></template>
        <template v-if="selectedCollectionKey === 'entitlements' && selectedResourceRow" #status><StatusBadge :status="stateOf(selectedResourceRow).status" :tone="stateOf(selectedResourceRow).tone" :title="stateOf(selectedResourceRow).title" /></template>
        <template #actions>
          <div class="flex flex-wrap items-center gap-2" role="group" aria-label="Metering resource actions">
            <button v-if="selectedCollectionKey === 'entitlements' && selectedResource && writable" type="button" class="k-btn k-btn--primary" :disabled="!!busy || foregroundLoading || !!resourceReadError || assignmentPending(selectedResourceId)" @click="editEntitlement(selectedResourceId, true)">{{ selectedEntitlementCanonical ? 'Edit overrides' : 'Edit entitlement' }}</button>
            <template v-if="selectedCollectionKey === 'plans' && writable">
              <button type="button" class="k-btn k-btn--primary" :disabled="!selectedResource || !!busy || foregroundLoading || !!resourceReadError" @click="editPlan(selectedResourceId)">Edit plan</button>
              <button type="button" class="k-btn k-btn--ghost" :disabled="!selectedResource || !!busy || foregroundLoading || !!resourceReadError || selectedResourceId === status?.defaultPlan || planHasReferences(selectedResourceId)" @click="deletePlan(selectedResourceId)">Delete plan</button>
            </template>
            <button type="button" class="k-btn k-btn--ghost" :disabled="foregroundLoading || !!busy" :aria-busy="foregroundLoading || undefined" @click="refresh(true)"><RefreshCw class="h-4 w-4" :class="{ 'animate-spin': foregroundLoading }" aria-hidden="true" />{{ foregroundLoading ? 'Refreshing…' : 'Refresh' }}</button>
          </div>
        </template>
        <div v-if="selectedResource" class="space-y-4">
          <p v-if="notice" class="metering-message metering-message--success" role="status" aria-live="polite">{{ notice }}</p>
          <p v-if="actionError && !planDraft" class="text-sm text-danger" role="alert">{{ actionError }}</p>
          <p v-if="status?.installed && !status.ready" class="metering-message" role="status">Metering is not ready. Effective limits and usage may be out of date.</p>
          <ResourceSectionCard v-if="selectedCollectionKey === 'plans' || selectedCollectionKey === 'entitlements' || selectedCollectionKey === 'accounts'" id="metering-resource-usage" :title="selectedCollectionKey === 'plans' ? 'Plan limits' : 'Usage and limits'" :description="selectedCollectionKey === 'plans' ? 'Shared limits applied to organizations on this plan. Organization overrides take precedence.' : 'Reported usage and effective limits for this account.'">
            <MeteringUsageList variant="grid" :limits="resourceLimits" :usage="usageOf(selectedResourceRow || {})" :window="windowOf(selectedResourceRow || {})" />
          </ResourceSectionCard>
          <ResourceSectionCard v-if="selectedCollectionKey === 'plans'" id="metering-plan-organizations" title="Organizations" description="Organizations using or assigned to this plan, including assignments still being applied.">
            <ResourceTable :columns="organizationColumns" :rows="assignedOrganizationRows" aria-label="Plan organizations" row-key="name" searchable search-placeholder="Search assigned organizations…" :search-keys="['displayName', 'name', 'stateText']" paginated :page-size="10" :loaded="organizationsLoaded" :loading="organizationsLoading" :error="organizationError" :stale="organizationsLoaded && !!organizationError" retryable @retry="refreshOrganizations" empty-text="No organizations are assigned to this plan." :interactive="true" :row-aria-label="(row: Row) => `View metering for ${row.displayName}`" @row-click="openPlanOrganization">
              <template #displayName="{ value, row }"><RouterLink :to="planOrganizationLocation(row)" class="k-btn k-btn--ghost k-table-resource-link" @click="rememberOrganizationTrigger">{{ value }}</RouterLink></template>
              <template #stateText="{ row }"><StatusBadge :status="stateOf(row).status" :tone="stateOf(row).tone" /></template>
              <template #limits="{ row }"><MeteringUsageList variant="summary" :limits="limitsOf(row)" :usage="usageOf(row)" :window="windowOf(row)" /></template>
            </ResourceTable>
            <p v-if="selectedResourceId === status?.defaultPlan || planHasReferences(selectedResourceId)" class="mt-3 text-sm text-text-muted">{{ selectedResourceId === status?.defaultPlan ? 'The platform default plan cannot be deleted.' : 'Reassign all organization assignments and entitlement references before deleting this plan.' }}</p>
          </ResourceSectionCard>
          <ResourceSectionCard v-if="selectedCollectionKey === 'plans' && otherPlanEntitlementRows.length" id="metering-plan-other-entitlements" title="Other entitlement references" description="Inspect and reassign these exact resources before deleting this plan.">
            <ResourceTable :columns="entitlementReferenceColumns" :rows="otherPlanEntitlementRows" aria-label="Other plan entitlements" row-key="name" searchable :search-keys="['name', 'account', 'plan']" paginated :page-size="10"><template #name="{ row, value }"><RouterLink :to="entitlementLocation(row)" class="k-btn k-btn--ghost k-table-resource-link">{{ value }}</RouterLink></template></ResourceTable>
          </ResourceSectionCard>
          <ResourceSectionCard v-if="selectedCollectionKey === 'meteringconfigs'" id="metering-config-meters" title="Meter definitions" description="Meters recorded for each matching target resource.">
            <ResourceTable :columns="[{ key: 'name', label: 'Meter', primary: true }, { key: 'type', label: 'Type' }, { key: 'quantity', label: 'Quantity expression' }]" :rows="resourceMeters" aria-label="Meter definitions" row-key="name" empty-text="No meters configured." />
          </ResourceSectionCard>
          <ResourceSectionCard v-if="selectedCollectionKey === 'meteringconfigs'" id="metering-config-account" title="Account resolution" description="How usage is attributed to an account.">
            <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-3 text-sm"><template v-for="fact in resourceAccountResolution" :key="fact.key"><dt class="text-text-muted">{{ fact.label }}</dt><dd class="min-w-0 break-words">{{ fact.value }}</dd></template></dl>
          </ResourceSectionCard>
          <ResourceSectionCard v-if="selectedCollectionKey === 'membershipreports'" id="metering-report-members" title="Reported members" description="Logical clusters included in this account by the reporter.">
            <ResourceTable :columns="[{ key: 'cluster', label: 'Cluster', primary: true }, { key: 'path', label: 'Workspace path' }]" :rows="resourceMembers" aria-label="Reported members" row-key="cluster" searchable search-placeholder="Search reported members…" :search-keys="['cluster', 'path']" paginated :page-size="10" empty-text="No workspaces reported." />
          </ResourceSectionCard>
          <ResourceSectionCard v-if="resourceFacts.length" id="metering-resource-details" title="Details">
            <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-3 text-sm"><template v-for="fact in resourceFacts" :key="fact.label"><dt class="text-text-muted">{{ fact.label }}</dt><dd class="min-w-0 break-words text-text-primary">{{ fact.value }}</dd></template></dl>
          </ResourceSectionCard>
          <ResourceSectionCard v-if="selectedCollectionKey === 'entitlements'" id="metering-entitlement-overrides" title="Organization overrides">
            <MeteringUsageList v-if="toLimits(selectedResource.spec?.overrides).length" variant="grid" :limits="toLimits(selectedResource.spec?.overrides)" />
            <p v-else class="text-sm text-text-muted">No organization overrides. Plan limits apply.</p>
          </ResourceSectionCard>
          <ResourceSectionCard v-if="resourceConditions.length" id="metering-resource-conditions" title="Conditions">
            <ConditionsPanel :conditions="resourceConditions" :semantics="meteringConditionSemantics" :generation="selectedResource.metadata.generation" :observed-generation="selectedResource.status?.observedGeneration as number | undefined" :show-title="false" />
          </ResourceSectionCard>
          <details class="text-xs text-text-muted"><summary>Technical details</summary><dl class="mt-3 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2"><dt>Resource ID</dt><dd class="break-all font-mono">{{ selectedResourceId }}</dd><dt>Workspace</dt><dd class="break-all font-mono">{{ status?.workspace }}</dd></dl><pre class="k-code mt-3 max-w-full overflow-x-auto p-3">{{ JSON.stringify(selectedResource, null, 2) }}</pre></details>
        </div>
      </ResourcePage>
    </div>

    <MeteringDialog v-if="assignment" title="Change organization plan" :busy="!!busy" :dirty="assignmentDirty" @close="assignment = null">
      <template #context><p class="mt-1 break-words text-sm font-medium">{{ orgLabel(assignment.name) }}</p><p class="mt-1 break-all font-mono text-[11px] text-text-muted">{{ assignment.name }}</p></template>
      <form id="metering-assignment-form" @submit.prevent="saveAssignment">
        <p v-if="actionError" class="mb-4 text-sm text-danger" role="alert">{{ actionError }}</p>
        <div class="mb-5 flex flex-wrap items-center gap-3 text-xs"><span class="text-text-muted">Current plan</span><span class="k-badge k-badge--muted">{{ planLabel(String(selectedEntitlement?.spec?.plan ?? '')) }}</span><span class="text-text-muted">{{ sourceFor(orgs.find(o => o.name === assignment?.name)) }}</span></div>
        <p v-if="selectedEntitlement?.metadata.labels?.['metering.railgrid.ai/plan-source'] === 'billing'" class="metering-message mb-4">Assigning a plan here replaces the subscription-managed plan with a platform-admin assignment.</p>
        <label for="metering-assignment-plan" class="mb-2 block text-xs font-medium">New plan</label>
        <select id="metering-assignment-plan" v-model="assignment.plan" data-initial-focus class="k-input w-full p-2.5 text-base sm:text-sm" :disabled="!!busy">
          <option value="" :disabled="!status?.defaultPlan">Use platform default ({{ planLabel(status?.defaultPlan || '') }})</option>
          <option v-for="p in planItems" :key="p.metadata.name" :value="p.metadata.name">{{ planLabel(p.metadata.name) }}</option>
        </select>
        <p v-if="!selectedTarget" class="mt-2 text-sm text-danger" role="alert">The selected plan is unavailable. Choose another plan or close and refresh.</p>
        <h3 class="mb-3 mt-6 text-sm font-semibold">Effective limits after change</h3>
        <div class="overflow-x-auto"><table class="metering-comparison"><thead><tr><th>Meter</th><th>Used</th><th>Current</th><th>New</th></tr></thead><tbody>
          <tr v-for="limit in comparisonLimits" :key="limit.meter"><td>{{ meterLabel(limit.meter) }}<span v-if="meterDefinition(limit.meter)" class="mt-1 block text-xs text-text-muted">{{ meterDefinition(limit.meter)?.unit }}</span></td><td>{{ amount((selectedEntitlement?.status?.usage as Record<string, unknown> | undefined)?.[limit.meter]) }}</td><td>{{ amount(toLimits(selectedEntitlement?.status?.effectiveLimits).find(l => l.meter === limit.meter)?.max) }}</td><td><strong>{{ limit.enforce === 'Meter' ? 'Uncapped' : amount(limit.max) }}</strong><span class="mt-1 block text-[11px] text-text-muted">{{ limit.removed ? 'Limit removed' : limit.enforce === 'Meter' ? 'Measure only' : `${limit.enforce} limit` }}</span><span v-if="toLimits(selectedEntitlement?.spec?.overrides).some(l => l.meter === limit.meter)" class="mt-1 block text-[11px] text-text-muted">Override retained</span></td></tr>
          <tr v-if="selectedTarget && !comparisonLimits.length"><td colspan="4" class="text-text-muted">This plan declares no limits.</td></tr>
        </tbody></table></div>
        <p v-if="overTarget.length" class="metering-message mt-4" role="status">Usage exceeds a proposed hard limit for {{ overTarget.map(l => l.meter).join(', ') }}. New creates above that limit may be denied.</p>
        <details class="mt-5 border-t border-border-subtle pt-4 text-xs"><summary>Advanced · {{ toLimits(selectedEntitlement?.spec?.overrides).length }} organization override(s)</summary><div class="mt-3"><MeteringUsageList :limits="toLimits(selectedEntitlement?.spec?.overrides)" /><p class="mt-3 text-text-muted">Organization overrides are preserved and replace the plan limit for the same meter. Edit them separately from the organization view.</p></div></details>
      </form>
      <template #submit><button type="submit" form="metering-assignment-form" class="k-btn k-btn--primary" :disabled="!!busy || !assignmentWritable || !selectedTarget">{{ busy ? 'Saving assignment…' : 'Change plan' }}</button></template>
    </MeteringDialog>

    <MeteringDialog v-if="planDraft" :title="planDraft.isNew ? 'New plan' : `Edit ${planDraft.displayName || planDraft.name} plan`" :busy="!!busy" :dirty="planDirty" @close="planDraft = null">
      <template #context><p v-if="!planDraft.isNew" class="mt-1 font-mono text-xs text-text-muted">Plan ID: {{ planDraft.name }}</p></template>
      <form id="metering-plan-form" @submit.prevent="savePlan">
        <p v-if="actionError" class="mb-4 text-sm text-danger" role="alert">{{ actionError }}</p>
        <p class="metering-message mb-5 text-xs">{{ planDraft.isNew ? 'Organizations can be assigned to this plan after it is created.' : `Changes apply to ${planReferenceCount(planDraft.name)} entitlement(s) on this plan. Organization overrides are preserved.` }}</p>
        <label v-if="planDraft.isNew" class="mb-4 block text-xs">Plan ID<input v-model="planDraft.name" data-initial-focus class="k-input mt-2 w-full p-2 text-base sm:text-sm" placeholder="e.g. team" required pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?" :disabled="!!busy" /></label>
        <label class="mb-5 block text-xs">Display name (optional)<input v-model="planDraft.displayName" :data-initial-focus="!planDraft.isNew ? '' : undefined" class="k-input mt-2 w-full p-2 text-base sm:text-sm" placeholder="e.g. Team" :disabled="!!busy" /></label>
        <h3 class="mb-2 text-sm font-semibold">Plan limits</h3>
        <p class="mb-4 text-xs text-text-muted">Hard limits deny creates above the maximum. Soft limits flag usage. Meter measures without a cap.</p>
        <MeteringLimitsEditor v-model="planDraft.limits" row-label="Limit" id-prefix="metering-plan" :disabled="!!busy" />
        <p v-if="planValidation" class="mt-3 text-sm text-danger" role="status">{{ planValidation }}</p>
      </form>
      <template #submit><button type="submit" form="metering-plan-form" class="k-btn k-btn--primary" :disabled="!!busy || !!planValidation || !planDraft.name.trim() || (!planDraft.isNew && !planDirty)">{{ busy ? 'Saving…' : planDraft.isNew ? 'Create plan' : 'Save plan' }}</button></template>
    </MeteringDialog>

    <MeteringDialog v-if="entitlementDraft" :title="entitlementDraft.advanced ? 'Edit entitlement' : 'Edit organization overrides'" :busy="!!busy" :dirty="overridesDirty" @close="entitlementDraft = null">
      <template #context><p class="mt-1 break-all text-sm text-text-secondary">{{ orgLabel(entitlementDraft.name) }}</p><p v-if="entitlementDraft.advanced" class="mt-1 font-mono text-xs text-text-muted">Entitlement ID: {{ entitlementDraft.name }}</p></template>
      <form id="metering-overrides-form" @submit.prevent="saveEntitlement">
        <p v-if="actionError" class="mb-4 text-sm text-danger" role="alert">{{ actionError }}</p>
        <div v-if="entitlementDraft.advanced" class="mb-5">
          <p class="metering-message mb-4">Advanced recovery edits apply only to this entitlement. Organization plan assignments use the organization view.</p>
          <label for="metering-recovery-plan" class="mb-2 block text-sm font-medium">Plan</label>
          <select id="metering-recovery-plan" v-model="entitlementDraft.plan" data-initial-focus class="k-input w-full p-2.5 text-base sm:text-sm" :disabled="!!busy"><option v-if="!planFor(entitlementDraft.plan)" :value="entitlementDraft.plan" disabled>Unavailable: {{ entitlementDraft.plan || 'No plan' }}</option><option v-for="plan in planItems" :key="plan.metadata.name" :value="plan.metadata.name">{{ planLabel(plan.metadata.name) }}</option></select>
        </div>
        <p class="mb-5 text-sm text-text-secondary">{{ entitlementDraft.advanced ? "An override replaces the plan limit for the same meter on this entitlement only. Removing it restores the plan limit." : "An override replaces the plan limit for the same meter on this organization only. Removing it restores that meter's plan limit." }}</p>
        <MeteringLimitsEditor v-model="entitlementDraft.overrides" row-label="Override" id-prefix="metering-overrides" :disabled="!!busy" />
        <p v-if="overrideValidation" class="mt-3 text-sm text-danger" role="status">{{ overrideValidation }}</p>
      </form>
      <template #submit><button type="submit" form="metering-overrides-form" class="k-btn k-btn--primary" :disabled="!!busy || !!overrideValidation || !overridesDirty || (entitlementDraft.advanced && !planFor(entitlementDraft.plan))">{{ busy ? 'Saving…' : entitlementDraft.advanced ? 'Save entitlement' : 'Save overrides' }}</button></template>
    </MeteringDialog>
  </section>
</template>

<style scoped>
.metering-message { border-radius: 4px; background: var(--color-accent-subtle); padding: 12px 14px; color: var(--color-text-primary); font-size: 13px; }
.metering-message--success { background: var(--color-success-subtle); color: var(--color-success); }
.metering-comparison { width: 100%; border-collapse: collapse; font-size: 12px; }
.metering-comparison th { text-align: left; font-weight: 500; color: var(--color-text-secondary); padding: 8px 8px 8px 0; }
.metering-comparison td { border-top: 1px solid var(--color-border-subtle); padding: 13px 8px 13px 0; vertical-align: top; font-variant-numeric: tabular-nums; }
.metering-comparison td:first-child { overflow-wrap: anywhere; }
.metering-comparison :is(th, td):last-child { text-align: right; padding-right: 0; }
.metering-comparison strong { color: var(--color-accent); }
</style>
