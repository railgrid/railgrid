<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { Check, Copy, Pause, Play, Plus } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import { hashFor, type Route } from '../router'
import type { AppStore } from '../store'
import {
  fmtTime,
  type Schedule,
  type ScheduleCreate,
  type SchedulePatch,
  type Trigger,
  type TriggerCreate,
  type TriggerPatch,
} from '../types'
import { confirmDialog } from '../portalkit/confirm'
import FormSelect from '../portalkit/FormSelect.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableActionButton from '../portalkit/ResourceTableActionButton.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import ResourceTableEditButton from '../portalkit/ResourceTableEditButton.vue'
import { toast } from '../ui/toast'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'

export type AutomationKind = 'schedule' | 'trigger'
type Automation = Schedule | Trigger

interface Meta {
  title: string
  one: string
  blurb: string
  empty: string
  whenHeader: string
  namePlaceholder: string
  taskPlaceholder: string
}

interface Draft {
  name: string
  type: string
  schedule: string
  runAt: string
  timeZone: string
  source: string
  connectionRef: string
  filter: string
  task: string
  channelRef: string
  suspend: boolean
}

const META: Record<AutomationKind, Meta> = {
  schedule: {
    title: 'Schedules',
    one: 'schedule',
    blurb: 'Recurring or one-shot tasks that run as this agent, in the background.',
    empty: 'No schedules yet.',
    whenHeader: 'When',
    namePlaceholder: 'daily-digest',
    taskPlaceholder: "Summarise today's open PRs and post to my channel.",
  },
  trigger: {
    title: 'Triggers',
    one: 'trigger',
    blurb: 'External events that wake this agent — a webhook POST or a GitHub event.',
    empty: 'No triggers yet.',
    whenHeader: 'Source',
    namePlaceholder: 'on-issue',
    taskPlaceholder: 'Triage the incoming event.',
  },
}

const EMPTY: Draft = {
  name: '',
  type: 'cron',
  schedule: '',
  runAt: '',
  timeZone: '',
  source: 'webhook',
  connectionRef: '',
  filter: '',
  task: '',
  channelRef: '',
  suspend: false,
}

const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  kind: AutomationKind
  agent: string
  createRoute?: boolean
  editName?: string
  authorityEpoch?: number
}>(), { createRoute: false, editName: '', authorityEpoch: 0 })

const emit = defineEmits<{ navigate: [route: Route] }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const editing = ref<string | null>(null)
const draft = reactive<Draft>({ ...EMPTY })
const nameError = ref('')
const filterError = ref('')
const copied = ref(false)
const formBusy = ref(false)
const actionBusy = ref('')

const meta = computed(() => META[props.kind])
const formRoute = computed(() => props.createRoute || !!props.editName)
const slice = computed(() => {
  void revision.value
  return { ...(props.kind === 'schedule' ? props.store.schedules : props.store.triggers) }
})
const rows = computed<Automation[]>(() => {
  void revision.value
  const all = props.kind === 'schedule' ? props.store.schedules.data : props.store.triggers.data
  return all.filter(row => row.spec.agentRef === props.agent)
})
const currentEdit = computed(() => rows.value.find(row => row.metadata.name === props.editName))
const tableRows = computed(() => rows.value.map(row => ({
  name: row.metadata.name,
  when: whenText(row),
  status: statusText(row),
  actions: '',
  resource: row,
})))
const columns = computed(() => [
  { key: 'name', label: 'Name', primary: true },
  { key: 'when', label: meta.value.whenHeader },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: 'Actions', ariaLabel: 'Actions', align: 'end' as const },
])
const channels = computed(() => {
  void revision.value
  return props.store.agent(props.agent)?.spec?.channels || []
})
const connections = computed(() => {
  void revision.value
  return props.store.connections.data
})
const typeOptions = [
  { value: 'cron', label: 'recurring (cron)' },
  { value: 'wakeup', label: 'one-shot (runAt)' },
]
const sourceOptions = [
  { value: 'webhook', label: 'webhook' },
  { value: 'github', label: 'github' },
]
const connectionOptions = computed(() => [
  { value: '', label: '— none —' },
  ...connections.value.map(connection => ({ value: connection.metadata.name, label: connection.metadata.name })),
])
const channelOptions = computed(() => [
  { value: '', label: '— primary channel —' },
  ...channels.value.map(channel => ({ value: channel.name, label: `${channel.name}${channel.primary ? ' (primary)' : ''}` })),
])
const wantsWebhook = computed(() => props.kind === 'trigger' && (draft.source === 'webhook' || draft.source === 'github'))
// The inbound URL is status.webhookPath (minted by the trigger reconciler,
// token included) on the hub the portal is served from — the same origin the
// Connections view hands the provider as publicBaseURL.
const webhookURL = computed(() => currentEdit.value ? webhookURLFor(currentEdit.value) : '')

function webhookURLFor(row: Automation): string {
  const path = automationStatus(row)?.webhookPath
  return path && props.kind === 'trigger' ? `${location.origin}${path}` : ''
}

// spec.filter is edited as one key=value per line: the map is small, the keys
// are a fixed vocabulary (eventType, match, header.<name>) and a table editor
// would be heavier than the thing it edits.
function filterToText(filter: Record<string, string> | undefined): string {
  return Object.entries(filter ?? {}).map(([key, value]) => `${key}=${value}`).join('\n')
}

function parseFilterText(input: string): { filter: Record<string, string>; error: string } {
  const filter: Record<string, string> = {}
  for (const raw of input.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const at = line.indexOf('=')
    const key = at > 0 ? line.slice(0, at).trim() : ''
    const value = at > 0 ? line.slice(at + 1).trim() : ''
    if (!key || !value) return { filter, error: `Each filter line is key=value — “${line}” is not.` }
    filter[key] = value
  }
  return { filter, error: '' }
}

// filterPatch is the merge-patch fragment that turns `before` into `after`:
// changed and added keys carry their value, removed keys carry null, and an
// unchanged map yields undefined so the save does not touch spec.filter.
function filterPatch(before: Record<string, string> | undefined, after: Record<string, string>): Record<string, string | null> | undefined {
  const out: Record<string, string | null> = {}
  for (const [key, value] of Object.entries(after)) if (before?.[key] !== value) out[key] = value
  for (const key of Object.keys(before ?? {})) if (!(key in after)) out[key] = null
  return Object.keys(out).length > 0 ? out : undefined
}

async function copyWebhookURL(url: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(url)
    copied.value = true
    toast('ok', 'Webhook URL copied.')
  } catch {
    copied.value = false
    toast('error', 'Could not copy. Check clipboard permission and try again.')
  }
}

function automationSpec(row: Automation): Schedule['spec'] & Trigger['spec'] {
  return row.spec as Schedule['spec'] & Trigger['spec']
}

function automationStatus(row: Automation): (Schedule['status'] & Trigger['status']) | undefined {
  return row.status as (Schedule['status'] & Trigger['status']) | undefined
}

function whenText(row: Automation): string {
  const spec = automationSpec(row)
  if (props.kind === 'schedule') return `${spec.type === 'wakeup' ? spec.runAt || '' : spec.schedule || ''}${spec.timeZone ? ` ${spec.timeZone}` : ''}`
  return `${spec.source}${spec.connectionRef ? ` ${spec.connectionRef}` : ''}`
}

function statusText(row: Automation): string {
  const spec = automationSpec(row)
  const status = automationStatus(row)
  if (status?.disabledReason) return status.disabledReason
  if (spec.suspend) return 'paused'
  if (props.kind === 'schedule') return status?.nextRun ? `next ${fmtTime(status.nextRun)}` : 'armed'
  return status?.lastFired ? `last ${fmtTime(status.lastFired)}` : 'armed'
}

function resetDraft(values: Draft = EMPTY): void {
  Object.assign(draft, values)
}

function openCreate(): void {
	emit('navigate', { kind: 'automation', resource: props.kind, agent: props.agent, action: 'create' })
}

function openEdit(row: Automation): void {
	emit('navigate', { kind: 'automation', resource: props.kind, agent: props.agent, action: 'edit', name: row.metadata.name })
}

function hydrateEdit(row: Automation): void {
  const spec = automationSpec(row)
  resetDraft({
    name: row.metadata.name,
    type: spec.type || 'cron',
    schedule: spec.schedule || '',
    runAt: spec.runAt || '',
    timeZone: spec.timeZone || '',
    source: spec.source || 'webhook',
    connectionRef: spec.connectionRef || '',
    filter: filterToText(spec.filter),
    task: spec.task || spec.checklist || '',
    channelRef: spec.channelRef || '',
    suspend: !!spec.suspend,
  })
  nameError.value = ''
  filterError.value = ''
  copied.value = false
  editing.value = row.metadata.name
}

function returnToAutomation(): void {
	if (!formBusy.value) emit('navigate', { kind: 'agent', name: props.agent, tab: 'automation' })
}

function patch(kind: AutomationKind): SchedulePatch | TriggerPatch {
  if (kind === 'schedule') {
    const body: SchedulePatch = {
      type: draft.type,
      timeZone: draft.timeZone,
      task: draft.task,
      suspend: draft.suspend,
      channelRef: draft.channelRef,
    }
    if (draft.type === 'wakeup') body.runAt = draft.runAt
    else body.schedule = draft.schedule
    return body
  }
  const body: TriggerPatch = {
    source: draft.source || 'webhook',
    connectionRef: draft.connectionRef,
    task: draft.task,
    suspend: draft.suspend,
    channelRef: draft.channelRef,
  }
  const filter = filterPatch(currentEdit.value ? automationSpec(currentEdit.value).filter : undefined, parseFilterText(draft.filter).filter)
  if (filter) body.filter = filter
  return body
}

async function save(): Promise<void> {
  if (formBusy.value) return
  const authority = captureAuthority()
  const kind = props.kind
  const agent = props.agent
  const editingName = editing.value
  const one = META[kind].one
  if (!editingName && !draft.name.trim()) {
    nameError.value = 'A name is required.'
    return
  }
  if (kind === 'trigger') {
    const parsed = parseFilterText(draft.filter)
    if (parsed.error) {
      filterError.value = parsed.error
      return
    }
  }
  const bodyPatch = patch(kind)
  formBusy.value = true
  try {
    const name = draft.name.trim()
    const result = editingName
      ? await mutate(authority.store, {
          run: (): Promise<Automation> => kind === 'schedule'
            ? authority.api.patchSchedule(editingName, bodyPatch as SchedulePatch)
            : authority.api.patchTrigger(editingName, bodyPatch as TriggerPatch),
          success: `${cap(one)} saved.`,
          failure: 'Save failed',
          reload: [kind === 'schedule' ? 'schedules' : 'triggers'],
        })
      : await mutate(authority.store, {
          run: (): Promise<Automation> => kind === 'schedule'
            ? authority.api.createSchedule({ name, agentRef: agent, ...bodyPatch } as ScheduleCreate)
            : authority.api.createTrigger({ name, agentRef: agent, ...bodyPatch } as TriggerCreate),
          success: `${cap(one)} “${name}” created.`,
          failure: 'Create failed',
          reload: [kind === 'schedule' ? 'schedules' : 'triggers'],
        })
	    if (result && authorityIsCurrent(authority) && kind === props.kind && agent === props.agent) {
	      emit('navigate', { kind: 'agent', name: props.agent, tab: 'automation' })
    }
  } finally {
    formBusy.value = false
  }
}

async function toggleSuspend(row: Automation): Promise<void> {
  if (actionBusy.value) return
  const authority = captureAuthority()
  const kind = props.kind
  const name = row.metadata.name
  const next = !row.spec.suspend
  actionBusy.value = `toggle:${name}`
  try {
    await mutate(authority.store, {
      run: (): Promise<Automation> => kind === 'schedule'
        ? authority.api.patchSchedule(name, { suspend: next })
        : authority.api.patchTrigger(name, { suspend: next }),
      success: next ? `${cap(META[kind].one)} paused.` : `${cap(META[kind].one)} resumed.`,
      failure: 'Update failed',
      optimistic: () => { row.spec.suspend = next },
      rollback: () => { row.spec.suspend = !next },
      reload: [kind === 'schedule' ? 'schedules' : 'triggers'],
    })
  } finally {
    actionBusy.value = ''
  }
}

async function del(name: string): Promise<void> {
  if (actionBusy.value) return
  const authority = captureAuthority()
  const kind = props.kind
  const one = META[kind].one
  const confirmationLock = `confirm-delete:${name}`
  actionBusy.value = confirmationLock
  try {
    const ok = await confirmDialog({ title: `Delete ${one} “${name}”?`, danger: true, confirmLabel: 'Delete' })
    if (!ok || !authorityIsCurrent(authority) || kind !== props.kind) return
    actionBusy.value = `delete:${name}`
    await mutate(authority.store, {
      run: () => kind === 'schedule' ? authority.api.deleteSchedule(name) : authority.api.deleteTrigger(name),
      success: `${cap(one)} deleted.`,
      failure: 'Delete failed',
      reload: [kind === 'schedule' ? 'schedules' : 'triggers'],
    })
  } finally {
    if (actionBusy.value === confirmationLock || actionBusy.value === `delete:${name}`) actionBusy.value = ''
  }
}

async function runNow(name: string): Promise<void> {
  if (actionBusy.value) return
  const authority = captureAuthority()
  const kind = props.kind
  actionBusy.value = `run:${name}`
  try {
    const result = await mutate(authority.store, {
      run: (): Promise<{ runID: string }> => kind === 'schedule' ? authority.api.runSchedule(name) : authority.api.runTrigger(name),
      failure: 'Run failed',
    })
    if (!result?.runID || !authorityIsCurrent(authority) || kind !== props.kind) return
    const id = result.runID
    toast('ok', `${name} queued.`, { label: 'View run', run: () => emit('navigate', { kind: 'run', id }) })
  } finally {
    actionBusy.value = ''
  }
}

watch(() => [props.kind, props.agent] as const, () => {
  editing.value = null
  nameError.value = ''
  filterError.value = ''
  copied.value = false
  formBusy.value = false
  actionBusy.value = ''
  resetDraft()
})

watch([() => props.createRoute, () => props.editName, () => revision.value], () => {
	if (!formRoute.value) {
		editing.value = null
		return
	}
	if (props.createRoute) {
		if (editing.value !== '') {
			resetDraft()
			nameError.value = ''
			filterError.value = ''
			copied.value = false
			editing.value = ''
		}
		return
	}
	if (currentEdit.value && editing.value !== props.editName) hydrateEdit(currentEdit.value)
}, { immediate: true })

const cap = (value: string): string => value.charAt(0).toUpperCase() + value.slice(1)
</script>

<template>
  <ResourceSectionCard v-if="!formRoute" class="agents-config-sec" :heading-id="`agent-${kind}-heading`" :title="meta.title" :description="meta.blurb">
    <template #actions>
      <button class="k-btn k-btn--ghost secondary" type="button" @click="openCreate"><Plus :stroke-width="1.75" aria-hidden="true" /> New {{ meta.one }}</button>
    </template>

    <div class="agents-tablewrap">
      <ResourceTable
        :columns="columns"
        :rows="tableRows"
        :aria-label="meta.title"
        variant="simple"
        :interactive="false"
        row-key="name"
        :loaded="slice.hasSnapshot"
        :loading="slice.loading"
        :error="slice.error"
        :stale="slice.hasSnapshot && !!slice.error"
        retryable
        :empty-text="meta.empty"
        @retry="store.load(kind === 'schedule' ? 'schedules' : 'triggers')"
      >
        <template #name="{ row }"><strong>{{ row.name }}</strong></template>
        <template #when="{ row }"><span class="mono">{{ row.when }}</span></template>
        <template #status="{ row }">
          <span class="muted">
            <span v-if="automationStatus(row.resource as Automation)?.disabledReason" class="k-badge agents-badge k-badge--warning agents-badge-warn">{{ automationStatus(row.resource as Automation)?.disabledReason }}</span>
            <span v-else-if="automationSpec(row.resource as Automation).suspend" class="k-badge agents-badge">paused</span>
            <template v-else>{{ statusText(row.resource as Automation) }}</template>
            <button v-if="automationStatus(row.resource as Automation)?.lastRunID" class="k-dashboard-action" type="button" @click="emit('navigate', { kind: 'run', id: automationStatus(row.resource as Automation)!.lastRunID! })">last run</button>
          </span>
        </template>
        <template #actions="{ row }">
          <ResourceTableActionButton v-if="webhookURLFor(row.resource as Automation)" :icon="Copy" :label="`Copy webhook URL for ${row.name}`" :disabled="!!actionBusy" @click="copyWebhookURL(webhookURLFor(row.resource as Automation))" />
          <ResourceTableActionButton :icon="Play" :label="`Run ${row.name} now`" :busy="actionBusy === `run:${row.name}`" :disabled="!!actionBusy" @click="runNow(String(row.name))" />
          <ResourceTableEditButton :label="`Edit ${row.name}`" :disabled="!!actionBusy" @click="openEdit(row.resource as Automation)" />
          <ResourceTableActionButton :icon="automationSpec(row.resource as Automation).suspend ? Play : Pause" :label="`${automationSpec(row.resource as Automation).suspend ? 'Resume' : 'Pause'} ${row.name}`" :busy="actionBusy === `toggle:${row.name}`" :disabled="!!actionBusy" @click="toggleSuspend(row.resource as Automation)" />
          <ResourceTableDeleteButton :label="`Delete ${row.name}`" :busy="actionBusy === `delete:${row.name}`" :disabled="!!actionBusy" @click="del(String(row.name))" />
        </template>
      </ResourceTable>
    </div>
  </ResourceSectionCard>

  <div v-else class="k-create-page">
    <ResourceBackLink :href="hashFor({ kind: 'agent', name: agent, tab: 'automation' })" :disabled="formBusy" @back="returnToAutomation">Agent schedules &amp; triggers</ResourceBackLink>
    <header class="k-create-header">
      <h1 class="k-create-title">{{ createRoute ? `New ${meta.one}` : `Edit ${meta.one} ${editName}` }}</h1>
      <p class="k-create-description">{{ meta.blurb }}</p>
    </header>

    <div v-if="!createRoute && slice.error && slice.hasSnapshot" class="k-stale" role="status">
      Could not refresh {{ meta.title.toLowerCase() }}. Showing the last loaded data. {{ slice.error }}
      <button class="k-dashboard-action" type="button" :disabled="slice.loading" @click="store.load(kind === 'schedule' ? 'schedules' : 'triggers')">{{ slice.loading ? 'Retrying…' : 'Retry' }}</button>
    </div>

    <div v-if="!createRoute && !editing" class="k-card agents-state" :class="slice.hasSnapshot ? 'agents-state-empty' : slice.error ? 'agents-state-error' : 'k-loading-reveal'" :role="slice.error && !slice.hasSnapshot ? 'alert' : 'status'">
      <p class="muted">{{ slice.hasSnapshot ? (slice.error ? `The last loaded data did not include ${meta.one} “${editName}”.` : `No ${meta.one} named “${editName}” belongs to this agent.`) : slice.error ? `Could not load this ${meta.one}. ${slice.error}` : `Loading ${meta.one}…` }}</p>
      <button v-if="slice.error && !slice.hasSnapshot" class="k-btn k-btn--ghost secondary" type="button" :disabled="slice.loading" @click="store.load(kind === 'schedule' ? 'schedules' : 'triggers')">{{ slice.loading ? 'Retrying…' : 'Retry' }}</button>
    </div>

    <form v-else class="agents-obj-form agents-guided-form k-create-surface" novalidate :aria-busy="formBusy" @submit.prevent="save">
      <div class="k-create-body">
        <label v-if="!editing" :for="`automation-${kind}-name`">Name *
          <input :id="`automation-${kind}-name`" v-model="draft.name" class="k-input" name="name" aria-label="Name" required aria-required="true" :placeholder="meta.namePlaceholder" autocomplete="off" :disabled="formBusy" :aria-invalid="nameError ? 'true' : undefined" :aria-describedby="nameError ? `automation-${kind}-name-error` : undefined" @input="nameError = ''" />
          <span v-if="nameError" :id="`automation-${kind}-name-error`" class="agents-fielderr" role="alert">{{ nameError }}</span>
        </label>
        <template v-if="kind === 'schedule'">
          <div class="agents-grid2">
            <label><span :id="`automation-${kind}-type-label`">Type</span><FormSelect v-model="draft.type" :options="typeOptions" :disabled="formBusy" :labelledby="`automation-${kind}-type-label`" /></label>
            <label>Timezone<input v-model="draft.timeZone" class="k-input" name="timeZone" placeholder="Europe/Vilnius" :disabled="formBusy" /></label>
          </div>
          <label v-if="draft.type === 'wakeup'">Run at (RFC3339)<input v-model="draft.runAt" class="k-input mono" name="runAt" placeholder="2026-01-01T09:00:00Z" :disabled="formBusy" /></label>
          <label v-else>Cron<input v-model="draft.schedule" class="k-input mono" name="schedule" aria-label="Cron" :aria-describedby="`automation-${kind}-cron-hint`" placeholder="0 9 * * *" :disabled="formBusy" /><span :id="`automation-${kind}-cron-hint`" class="agents-hint">5-field cron · crontab.guru</span></label>
        </template>
        <template v-else>
          <div class="agents-grid2">
            <label><span :id="`automation-${kind}-source-label`">Source</span><FormSelect v-model="draft.source" :options="sourceOptions" :disabled="formBusy" :labelledby="`automation-${kind}-source-label`" :describedby="`automation-${kind}-source-hint`" /><span :id="`automation-${kind}-source-hint`" class="agents-hint">{{ draft.source === 'github' ? 'GitHub delivers to the webhook URL below; filter on the X-GitHub-Event name.' : 'Any sender that can POST JSON to the webhook URL below.' }}</span></label>
            <label><span :id="`automation-${kind}-connection-label`">Connection</span><FormSelect v-model="draft.connectionRef" :options="connectionOptions" :disabled="formBusy" :labelledby="`automation-${kind}-connection-label`" /></label>
          </div>
          <label>Filter<textarea v-model="draft.filter" class="k-input mono" name="filter" rows="2" :aria-describedby="filterError ? `automation-${kind}-filter-error` : `automation-${kind}-filter-hint`" :aria-invalid="filterError ? 'true' : undefined" placeholder="eventType=pull_request" :disabled="formBusy" @input="filterError = ''"></textarea><span v-if="filterError" :id="`automation-${kind}-filter-error`" class="agents-fielderr" role="alert">{{ filterError }}</span><span v-else :id="`automation-${kind}-filter-hint`" class="agents-hint">One key=value per line. eventType matches the event header (X-GitHub-Event), match is a substring of the payload, header.&lt;name&gt; matches a request header. Empty fires on every delivery.</span></label>
          <section v-if="editing && webhookURL" class="k-card agents-secret-handoff agents-webhook-url" aria-label="Webhook URL">
            <div>
              <strong>Webhook URL</strong>
              <code data-testid="automation-webhook-url">{{ webhookURL }}</code>
              <span class="agents-hint">{{ draft.source === 'github' ? 'Paste as the Payload URL under the repository’s Settings → Webhooks, content type application/json. ' : 'POST JSON deliveries here. ' }}The URL embeds this trigger’s secret token; treat it like a password.</span>
            </div>
            <div class="agents-secret-handoff-actions">
              <button type="button" class="k-btn k-btn--ghost" :disabled="formBusy" @click="copyWebhookURL(webhookURL)"><Check v-if="copied" aria-hidden="true" /><Copy v-else aria-hidden="true" /> {{ copied ? 'Copied' : 'Copy webhook URL' }}</button>
            </div>
            <span class="sr-only" role="status" aria-live="polite">{{ copied ? 'Webhook URL copied.' : '' }}</span>
          </section>
          <p v-else-if="editing && wantsWebhook" class="agents-hint" role="status">The webhook URL has not been minted yet — the provider stamps it shortly after the trigger is created. Reload to pick it up.</p>
          <p v-else-if="!editing && wantsWebhook" class="agents-hint">The inbound webhook URL appears here, and on the trigger row, once the trigger is created.</p>
        </template>
        <label>Task{{ kind === 'trigger' ? ' on fire' : '' }}<textarea v-model="draft.task" class="k-input" name="task" rows="3" :placeholder="meta.taskPlaceholder" :disabled="formBusy"></textarea></label>
        <label><span :id="`automation-${kind}-channel-label`">Channel</span><FormSelect v-model="draft.channelRef" :options="channelOptions" :disabled="formBusy" :labelledby="`automation-${kind}-channel-label`" :describedby="`automation-${kind}-channel-hint`" /><span :id="`automation-${kind}-channel-hint`" class="agents-hint">Where output is delivered</span></label>
        <label class="agents-check k-checkbox-hit"><input v-model="draft.suspend" type="checkbox" name="suspend" :disabled="formBusy" /> Paused</label>
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost secondary" :disabled="formBusy" @click="returnToAutomation">Cancel</button>
        <button class="k-btn k-btn--primary" type="submit" :disabled="formBusy">{{ formBusy ? (editing ? 'Saving…' : 'Creating…') : editing ? 'Save' : `Create ${meta.one}` }}</button>
      </div>
    </form>
  </div>
</template>
