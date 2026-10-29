<script setup lang="ts">
// Metering in the admin area. Metering is a standalone deployment the hub
// discovers through kcp (pkg/hub/metering); this section reads its objects
// through the hub's read-only proxy, /api/admin/metering/{collection}, so the
// operator token never reaches the browser. Not installed renders the Helm
// hint instead of tables. Plans (quota templates) and entitlements (an
// organization's plan plus overrides) are edited here too, through the hub's
// write routes, which update the objects in the metering workspace.
import { computed, onMounted, ref } from 'vue'
import { Plus, RefreshCw } from 'lucide-vue-next'

import ResourceTable from '@/portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '@/portalkit/ResourceTableDeleteButton.vue'
import ResourceTableEditButton from '@/portalkit/ResourceTableEditButton.vue'
import StatusBadge from '@/portalkit/StatusBadge.vue'
import { confirmDialog } from '@/portalkit/confirm'
import { authFetch } from '@/auth/session'

import MeteringLimitsEditor, { type Limit } from './MeteringLimitsEditor.vue'

type Endpoints = { portalURL: string; webhookURL?: string; auditURL?: string; version?: string; workspace: string }
type Status = {
  installed: boolean; workspace: string; ready: boolean; reason?: string; collections: string[]; endpoints?: Endpoints
  writable?: boolean; defaultPlan?: string
}
type Item = { metadata: { name: string }; spec?: Record<string, unknown>; status?: Record<string, unknown> }

const status = ref<Status | null>(null)
const items = ref<Record<string, Item[]>>({})
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)

// Editors. One open at a time per table; `busy` names the object being written.
type PlanDraft = { name: string; isNew: boolean; displayName: string; limits: Limit[] }
type EntitlementDraft = { name: string; plan: string; overrides: Limit[] }
const planDraft = ref<PlanDraft | null>(null)
const entitlementDraft = ref<EntitlementDraft | null>(null)
const busy = ref<string | null>(null)
const actionError = ref<string | null>(null)

const writable = computed(() => !!status.value?.writable)
const planNames = computed(() => (items.value.plans ?? []).map((p) => p.metadata.name))

const collections = [
  { key: 'plans', label: 'Plans', columns: [
    { key: 'name', label: 'Name', primary: true },
    { key: 'displayName', label: 'Display name' },
    { key: 'limits', label: 'Limits' },
    { key: 'actions', label: '', ariaLabel: 'Actions' },
  ] },
  { key: 'entitlements', label: 'Entitlements', columns: [
    { key: 'name', label: 'Name', primary: true },
    { key: 'account', label: 'Account' },
    { key: 'plan', label: 'Plan' },
    { key: 'effectiveLimits', label: 'Effective limits' },
    { key: 'usage', label: 'Usage' },
    { key: 'actions', label: '', ariaLabel: 'Actions' },
  ] },
  { key: 'accounts', label: 'Accounts', columns: [
    { key: 'name', label: 'Name', primary: true },
    { key: 'displayName', label: 'Display name' },
    { key: 'memberCount', label: 'Members' },
    { key: 'usage', label: 'Usage' },
    { key: 'customerRef', label: 'Customer' },
  ] },
  { key: 'membershipreports', label: 'Membership reports', columns: [
    { key: 'name', label: 'Name', primary: true },
    { key: 'account', label: 'Account' },
    { key: 'reporter', label: 'Reporter' },
    { key: 'members', label: 'Members' },
  ] },
  { key: 'meteringconfigs', label: 'Metering configs', columns: [
    { key: 'name', label: 'Name', primary: true },
    { key: 'target', label: 'Target' },
    { key: 'meters', label: 'Meters' },
    { key: 'account', label: 'Account resolution' },
  ] },
] as const

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

function rowsFor(collection: string): Array<Record<string, unknown>> {
  return (items.value[collection] ?? []).map((item) => {
    const row: Record<string, unknown> = { name: item.metadata.name }
    for (const [k, v] of Object.entries(item.spec ?? {})) row[k] = cell(v)
    for (const [k, v] of Object.entries(item.status ?? {})) row[k] = cell(v)
    if (collection === 'membershipreports') row.members = cell((item.spec?.members as unknown[] | undefined)?.length ?? 0)
    return row
  })
}

const readyTone = computed(() => (status.value?.ready ? 'success' : 'danger'))

async function refresh(force = false): Promise<void> {
  loading.value = true
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
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
}

function toLimits(value: unknown): Limit[] {
  if (!Array.isArray(value)) return []
  return (value as Array<Record<string, unknown>>).map((l) => ({
    meter: String(l.meter ?? ''),
    shape: String(l.shape ?? 'Level'),
    enforce: String(l.enforce ?? 'Hard'),
    max: String(l.max ?? ''),
  }))
}

function editPlan(name: string): void {
  const plan = (items.value.plans ?? []).find((p) => p.metadata.name === name)
  actionError.value = null
  planDraft.value = {
    name,
    isNew: !plan,
    displayName: String(plan?.spec?.displayName ?? ''),
    limits: toLimits(plan?.spec?.limits),
  }
}

function editEntitlement(name: string): void {
  const ent = (items.value.entitlements ?? []).find((e) => e.metadata.name === name)
  if (!ent) return
  actionError.value = null
  entitlementDraft.value = { name, plan: String(ent.spec?.plan ?? ''), overrides: toLimits(ent.spec?.overrides) }
}

async function write(method: 'PUT' | 'DELETE', path: string, body?: unknown): Promise<void> {
  const resp = await authFetch(`/api/admin/metering/${path}`, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!resp.ok) {
    let message = `${resp.status}`
    try { message = ((await resp.json()) as { error?: string }).error ?? message } catch { /* non-JSON error */ }
    throw new Error(message)
  }
}

async function savePlan(): Promise<void> {
  const draft = planDraft.value
  if (!draft) return
  busy.value = draft.name
  actionError.value = null
  try {
    await write('PUT', `plans/${encodeURIComponent(draft.name.trim())}`, { displayName: draft.displayName.trim(), limits: draft.limits })
    planDraft.value = null
    await refresh()
  } catch (err) {
    actionError.value = `Plan ${draft.name}: ${(err as Error).message}`
  } finally {
    busy.value = null
  }
}

async function deletePlan(name: string): Promise<void> {
  const using = (items.value.entitlements ?? []).filter((e) => e.spec?.plan === name).length
  const ok = await confirmDialog({
    title: `Delete plan "${name}"?`,
    message: using
      ? `${using} entitlement${using === 1 ? ' is' : 's are'} on this plan; move them first.`
      : 'Organizations can no longer be placed on it. Existing usage data is kept.',
    danger: true,
    confirmLabel: 'Delete',
  })
  if (!ok) return
  busy.value = name
  actionError.value = null
  try {
    await write('DELETE', `plans/${encodeURIComponent(name)}`)
    await refresh()
  } catch (err) {
    actionError.value = `Plan ${name}: ${(err as Error).message}`
  } finally {
    busy.value = null
  }
}

async function saveEntitlement(): Promise<void> {
  const draft = entitlementDraft.value
  if (!draft) return
  busy.value = draft.name
  actionError.value = null
  try {
    await write('PUT', `entitlements/${encodeURIComponent(draft.name)}`, { plan: draft.plan, overrides: draft.overrides })
    entitlementDraft.value = null
    await refresh()
  } catch (err) {
    actionError.value = `Entitlement ${draft.name}: ${(err as Error).message}`
  } finally {
    busy.value = null
  }
}

onMounted(() => void refresh())
</script>

<template>
  <section>
    <div class="mb-1 flex items-center gap-2">
      <h2 class="text-base font-semibold text-text-primary">Metering</h2>
      <StatusBadge v-if="status?.installed" :status="status.ready ? 'ready' : 'not ready'" :tone="readyTone" />
      <StatusBadge v-else-if="status" status="not installed" tone="muted" />
      <button
        type="button"
        class="k-btn ml-auto px-2 py-1 text-xs disabled:opacity-50"
        :disabled="loading"
        aria-label="Refresh metering"
        @click="refresh(true)"
      >
        <RefreshCw class="h-3.5 w-3.5" :class="{ 'animate-spin': loading }" :stroke-width="1.75" />
      </button>
    </div>
    <p class="mb-4 text-sm text-text-muted">
      Plans, entitlements, usage and limit enforcement. Metering is a standalone deployment the hub
      discovers through its workspace<template v-if="status"> (<span class="font-mono">{{ status.workspace }}</span>)</template>.
    </p>

    <p v-if="error" class="mb-3 text-sm text-danger" role="alert" aria-live="assertive">{{ error }}</p>

    <template v-if="status && !status.installed">
      <p class="mb-2 text-sm text-text-primary">Not installed.</p>
      <p class="mb-2 text-sm text-text-muted">
        Install contrib-metering with a kubeconfig that can create
        <span class="font-mono">{{ status.workspace }}</span>; its init publishes the endpoints the hub looks for.
      </p>
      <pre class="k-code mb-4 overflow-x-auto p-3 text-xs">helm install metering deploy/chart -n metering --create-namespace \
  --set workspacePath={{ status.workspace }} \
  --set admin.tokenSecretRef.name=metering-admin</pre>
    </template>

    <template v-else-if="status?.installed">
      <p v-if="!status.ready" class="mb-3 text-sm text-danger">{{ status.reason }}</p>
      <dl class="mb-4 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs text-text-muted">
        <dt>Version</dt><dd class="font-mono text-text-primary">{{ status.endpoints?.version || '—' }}</dd>
        <dt>Controller</dt><dd class="font-mono text-text-primary">{{ status.endpoints?.portalURL }}</dd>
        <dt v-if="status.endpoints?.webhookURL">Webhook</dt><dd v-if="status.endpoints?.webhookURL" class="font-mono text-text-primary">{{ status.endpoints?.webhookURL }}</dd>
        <dt v-if="status.endpoints?.auditURL">Audit sink</dt><dd v-if="status.endpoints?.auditURL" class="font-mono text-text-primary">{{ status.endpoints?.auditURL }}</dd>
      </dl>

      <p v-if="actionError" class="mb-3 text-sm text-danger" role="alert" aria-live="assertive">{{ actionError }}</p>

      <div v-for="c in collections" :key="c.key" class="mb-6">
        <div class="mb-2 flex items-center gap-2">
          <h3 class="text-sm font-semibold text-text-primary">{{ c.label }}</h3>
          <button
            v-if="c.key === 'plans' && writable"
            type="button"
            class="k-btn ml-auto px-2 py-1 text-xs disabled:opacity-50"
            :disabled="!!planDraft || !!busy"
            @click="editPlan('')"
          >
            <Plus class="h-3.5 w-3.5" :stroke-width="1.75" />
            New plan
          </button>
        </div>

        <form
          v-if="c.key === 'plans' && planDraft"
          class="k-card mb-3 p-4"
          :aria-label="planDraft.isNew ? 'New plan' : `Edit plan ${planDraft.name}`"
          @submit.prevent="savePlan"
        >
          <div class="mb-3 flex flex-wrap items-end gap-2">
            <div>
              <label for="metering-plan-name" class="block text-[11px] text-text-muted">Name</label>
              <input
                id="metering-plan-name"
                v-model="planDraft.name"
                class="k-input mt-1 w-40 font-mono text-sm"
                placeholder="e.g. team"
                :readonly="!planDraft.isNew"
                required
                pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?"
              />
            </div>
            <div>
              <label for="metering-plan-display-name" class="block text-[11px] text-text-muted">Display name (optional)</label>
              <input id="metering-plan-display-name" v-model="planDraft.displayName" class="k-input mt-1 w-56 text-sm" placeholder="e.g. Team" />
            </div>
          </div>
          <p class="mb-2 text-[11px] text-text-muted">
            Limits. A Hard limit denies creates over max; Soft only flags the entitlement; Meter measures without a cap.
          </p>
          <MeteringLimitsEditor v-model="planDraft.limits" row-label="Limit" id-prefix="metering-plan" :disabled="!!busy" />
          <div class="mt-3 flex items-center gap-2">
            <button type="submit" class="k-btn k-btn--primary px-3 py-1.5 text-sm disabled:opacity-50" :disabled="!!busy || !planDraft.name.trim()">
              {{ busy === planDraft.name ? 'Saving…' : planDraft.isNew ? 'Create plan' : 'Save plan' }}
            </button>
            <button type="button" class="k-btn px-3 py-1.5 text-sm" :disabled="!!busy" @click="planDraft = null">Cancel</button>
          </div>
        </form>

        <form
          v-if="c.key === 'entitlements' && entitlementDraft"
          class="k-card mb-3 p-4"
          :aria-label="`Edit entitlement ${entitlementDraft.name}`"
          @submit.prevent="saveEntitlement"
        >
          <div class="mb-3 flex flex-wrap items-end gap-2">
            <div>
              <span class="block text-[11px] text-text-muted">Entitlement</span>
              <span class="mt-1 block font-mono text-sm text-text-primary">{{ entitlementDraft.name }}</span>
            </div>
            <div>
              <label for="metering-entitlement-plan" class="block text-[11px] text-text-muted">Plan</label>
              <select id="metering-entitlement-plan" v-model="entitlementDraft.plan" class="k-input mt-1 w-48 text-sm" required>
                <option v-for="name in planNames" :key="name" :value="name">{{ name }}</option>
              </select>
            </div>
          </div>
          <p class="mb-2 text-[11px] text-text-muted">
            Overrides replace the plan's limit for the same meter on this organization only.
          </p>
          <MeteringLimitsEditor v-model="entitlementDraft.overrides" row-label="Override" id-prefix="metering-entitlement" :disabled="!!busy" />
          <div class="mt-3 flex items-center gap-2">
            <button type="submit" class="k-btn k-btn--primary px-3 py-1.5 text-sm disabled:opacity-50" :disabled="!!busy || !entitlementDraft.plan">
              {{ busy === entitlementDraft.name ? 'Saving…' : 'Save entitlement' }}
            </button>
            <button type="button" class="k-btn px-3 py-1.5 text-sm" :disabled="!!busy" @click="entitlementDraft = null">Cancel</button>
          </div>
        </form>

        <ResourceTable
          :columns="[...c.columns]"
          :rows="rowsFor(c.key)"
          :aria-label="c.label"
          row-key="name"
          :interactive="false"
          :loaded="loaded"
          :loading="loading"
          :error="error"
          :stale="loaded && !!error"
          retryable
          :empty-text="`No ${c.label.toLowerCase()}.`"
          @retry="refresh()"
        >
          <template v-if="c.key === 'plans'" #plan="{ value }">
            <span class="k-badge k-badge--muted">{{ value }}</span>
          </template>
          <template v-if="c.key === 'plans' && writable" #actions="{ row }">
            <div class="flex items-center justify-end gap-1">
              <span v-if="row.name === status.defaultPlan" class="text-[11px] text-text-muted">default</span>
              <ResourceTableEditButton :label="`Edit plan ${row.name}`" :disabled="!!busy" @click="editPlan(String(row.name))" />
              <ResourceTableDeleteButton
                :label="`Delete plan ${row.name}`"
                :busy-label="`Deleting plan ${row.name}…`"
                :busy="busy === row.name"
                :disabled="!!busy || row.name === status.defaultPlan"
                @click="deletePlan(String(row.name))"
              />
            </div>
          </template>
          <template v-if="c.key === 'entitlements' && writable" #actions="{ row }">
            <div class="flex items-center justify-end gap-1">
              <ResourceTableEditButton :label="`Change plan for ${row.name}`" :disabled="!!busy" @click="editEntitlement(String(row.name))" />
            </div>
          </template>
        </ResourceTable>
      </div>
    </template>
  </section>
</template>
