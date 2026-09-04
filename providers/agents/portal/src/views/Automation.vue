<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { Pause, Play, Plus } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import type { Route } from '../router'
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
  task: '',
  channelRef: '',
  suspend: false,
}

const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  kind: AutomationKind
  agent: string
  authorityEpoch?: number
}>(), { authorityEpoch: 0 })

const emit = defineEmits<{ navigate: [route: Route] }>()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const editing = ref<string | null>(null)
const draft = reactive<Draft>({ ...EMPTY })
const nameError = ref('')
const formBusy = ref(false)
const actionBusy = ref('')

const meta = computed(() => META[props.kind])
const slice = computed(() => {
  void revision.value
  return props.kind === 'schedule' ? props.store.schedules : props.store.triggers
})
const rows = computed<Automation[]>(() => {
  void revision.value
  const all = props.kind === 'schedule' ? props.store.schedules.data : props.store.triggers.data
  return all.filter(row => row.spec.agentRef === props.agent)
})
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
  resetDraft()
  nameError.value = ''
  editing.value = ''
}

function openEdit(row: Automation): void {
  const spec = automationSpec(row)
  resetDraft({
    name: row.metadata.name,
    type: spec.type || 'cron',
    schedule: spec.schedule || '',
    runAt: spec.runAt || '',
    timeZone: spec.timeZone || '',
    source: spec.source || 'webhook',
    connectionRef: spec.connectionRef || '',
    task: spec.task || spec.checklist || '',
    channelRef: spec.channelRef || '',
    suspend: !!spec.suspend,
  })
  nameError.value = ''
  editing.value = row.metadata.name
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
  return {
    source: draft.source || 'webhook',
    connectionRef: draft.connectionRef,
    task: draft.task,
    suspend: draft.suspend,
    channelRef: draft.channelRef,
  }
}

async function save(): Promise<void> {
  if (formBusy.value) return
  const authority = captureAuthority()
  const kind = props.kind
  const agent = props.agent
  const editingName = editing.value
  const one = META[kind].one
  const bodyPatch = patch(kind)
  if (!editingName && !draft.name.trim()) {
    nameError.value = 'A name is required.'
    return
  }
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
    if (result && authorityIsCurrent(authority) && kind === props.kind && agent === props.agent) editing.value = null
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
  formBusy.value = false
  actionBusy.value = ''
  resetDraft()
})

const cap = (value: string): string => value.charAt(0).toUpperCase() + value.slice(1)
</script>

<template>
  <ResourceSectionCard class="agents-config-sec" :heading-id="`agent-${kind}-heading`" :title="meta.title" :description="meta.blurb">
    <template v-if="editing === null" #actions>
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
          <button v-if="automationStatus(row.resource as Automation)?.lastRunID" class="k-btn k-btn--ghost agents-linkbtn" type="button" @click="emit('navigate', { kind: 'run', id: automationStatus(row.resource as Automation)!.lastRunID! })">last run</button>
        </span>
      </template>
      <template #actions="{ row }">
        <ResourceTableActionButton :icon="Play" :label="`Run ${row.name} now`" :busy="actionBusy === `run:${row.name}`" :disabled="!!actionBusy" @click="runNow(String(row.name))" />
        <ResourceTableEditButton :label="`Edit ${row.name}`" :disabled="!!actionBusy" @click="openEdit(row.resource as Automation)" />
        <ResourceTableActionButton :icon="automationSpec(row.resource as Automation).suspend ? Play : Pause" :label="`${automationSpec(row.resource as Automation).suspend ? 'Resume' : 'Pause'} ${row.name}`" :busy="actionBusy === `toggle:${row.name}`" :disabled="!!actionBusy" @click="toggleSuspend(row.resource as Automation)" />
        <ResourceTableDeleteButton :label="`Delete ${row.name}`" :busy="actionBusy === `delete:${row.name}`" :disabled="!!actionBusy" @click="del(String(row.name))" />
      </template>
    </ResourceTable>
    </div>

    <form v-if="editing !== null" class="agents-obj-form k-card" @submit.prevent="save">
      <h3>{{ editing ? `Edit ${meta.one} ${editing}` : `New ${meta.one}` }}</h3>
      <label v-if="!editing">Name *<input v-model="draft.name" class="k-input" :placeholder="meta.namePlaceholder" autocomplete="off" :disabled="formBusy" :aria-invalid="nameError ? 'true' : undefined" :aria-describedby="nameError ? `automation-${kind}-name-error` : undefined" @input="nameError = ''" /><span v-if="nameError" :id="`automation-${kind}-name-error`" class="agents-fielderr" role="alert">{{ nameError }}</span></label>
      <template v-if="kind === 'schedule'">
        <div class="agents-grid2">
          <label><span :id="`automation-${kind}-type-label`">Type</span><FormSelect v-model="draft.type" :options="typeOptions" :disabled="formBusy" :labelledby="`automation-${kind}-type-label`" /></label>
          <label>Timezone<input v-model="draft.timeZone" class="k-input" placeholder="Europe/Vilnius" :disabled="formBusy" /></label>
        </div>
        <label v-if="draft.type === 'wakeup'">Run at (RFC3339)<input v-model="draft.runAt" class="k-input mono" placeholder="2026-01-01T09:00:00Z" :disabled="formBusy" /></label>
        <label v-else>Cron<input v-model="draft.schedule" class="k-input mono" placeholder="0 9 * * *" :disabled="formBusy" /><span class="agents-hint">5-field cron · crontab.guru</span></label>
      </template>
      <div v-else class="agents-grid2">
        <label><span :id="`automation-${kind}-source-label`">Source</span><FormSelect v-model="draft.source" :options="sourceOptions" :disabled="formBusy" :labelledby="`automation-${kind}-source-label`" /></label>
        <label><span :id="`automation-${kind}-connection-label`">Connection</span><FormSelect v-model="draft.connectionRef" :options="connectionOptions" :disabled="formBusy" :labelledby="`automation-${kind}-connection-label`" /></label>
      </div>
      <label>Task{{ kind === 'trigger' ? ' on fire' : '' }}<textarea v-model="draft.task" class="k-input" rows="3" :placeholder="meta.taskPlaceholder" :disabled="formBusy"></textarea></label>
      <label><span :id="`automation-${kind}-channel-label`">Channel</span><FormSelect v-model="draft.channelRef" :options="channelOptions" :disabled="formBusy" :labelledby="`automation-${kind}-channel-label`" /><span class="agents-hint">Where output is delivered</span></label>
      <label class="agents-check"><input v-model="draft.suspend" type="checkbox" :disabled="formBusy" /> Paused</label>
      <div class="agents-form-actions"><button class="k-btn k-btn--primary" type="submit" :disabled="formBusy">{{ formBusy ? 'Saving…' : editing ? 'Save' : `Create ${meta.one}` }}</button><button type="button" class="k-btn k-btn--ghost secondary" :disabled="formBusy" @click="editing = null">Cancel</button></div>
    </form>
  </ResourceSectionCard>
</template>
