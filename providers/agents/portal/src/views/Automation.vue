<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { Check, Copy, Inbox, Pause, Play, Plus, Power } from 'lucide-vue-next'
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
import TriggerDeliveryLog from './TriggerDeliveryLog.vue'
import TriggerFilterTester from './TriggerFilterTester.vue'
import { deliveryOutcomeLabel } from './trigger-deliveries'

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
  /** filter is the generic key=value editor; in GitHub mode it holds only the
   *  keys the structured fields below do not own (match, header.<name>). */
  filter: string
  /** The GitHub flavour's structured half of spec.filter — see GITHUB_EVENTS. */
  ghEvents: string[]
  ghOtherEvents: string
  ghActions: string
  ghRepos: string
  task: string
  channelRef: string
  suspend: boolean
  /** spec.deliveryRetention as typed ('' means the provider default); a
   *  number once the numeric input has cast it. */
  deliveryRetention: string | number
}

// The provider's own cap when a trigger does not set one, and the schema's
// ceiling; both are stated by the provider (DefaultTriggerDeliveryRetention).
const DEFAULT_DELIVERY_RETENTION = 200
const MAX_DELIVERY_RETENTION = 1000

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
    blurb: 'External events that wake this agent — any webhook POST, or a GitHub repository webhook.',
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
  filter: '',
  ghEvents: [],
  ghOtherEvents: '',
  ghActions: '',
  ghRepos: '',
  task: '',
  channelRef: '',
  suspend: false,
  deliveryRetention: '',
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
const retentionError = ref('')
const copied = ref(false)
// deliveriesFor is the trigger whose delivery log is open under the table;
// testDelivery is the delivery the edit form's tester starts on (from the
// log's "Test filter").
const deliveriesFor = ref<string | null>(null)
const testDelivery = ref('')
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
const deliveriesRow = computed(() => props.kind === 'trigger' ? rows.value.find(row => row.metadata.name === deliveriesFor.value) as Trigger | undefined : undefined)
// liveFilter is the filter the form currently describes, for the tester: it
// re-evaluates as the lines and the GitHub fields change.
const liveFilter = computed(() => draftFilter())
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
const typeOptions = [
  { value: 'cron', label: 'recurring (cron)' },
  { value: 'wakeup', label: 'one-shot (runAt)' },
]
// Both sources are the same inbound webhook endpoint on the provider; `github`
// only records that the sender is GitHub so the form can offer its event
// catalog instead of a raw key=value editor.
const sourceOptions = [
  { value: 'webhook', label: 'Generic webhook' },
  { value: 'github', label: 'GitHub webhook' },
]
const sourceLabel = (source: string): string => sourceOptions.find(option => option.value === source)?.label ?? source

// GITHUB_EVENTS is the catalog the GitHub flavour offers as checkboxes: the
// X-GitHub-Event names a repository or organization webhook sends that an
// agent is usually asked to react to. Anything else goes in "other events".
const GITHUB_EVENTS: readonly { name: string; label: string; hint: string }[] = [
  { name: 'pull_request', label: 'Pull requests', hint: 'opened, synchronize, closed, …' },
  { name: 'pull_request_review', label: 'PR reviews', hint: 'a review is submitted or dismissed' },
  { name: 'pull_request_review_comment', label: 'PR review comments', hint: 'a comment on the diff' },
  { name: 'issues', label: 'Issues', hint: 'opened, labeled, closed, …' },
  { name: 'issue_comment', label: 'Issue & PR comments', hint: 'a comment on an issue or PR thread' },
  { name: 'push', label: 'Pushes', hint: 'commits or tags pushed' },
  { name: 'release', label: 'Releases', hint: 'published, created, …' },
  { name: 'workflow_run', label: 'Workflow runs', hint: 'an Actions workflow finishes' },
  { name: 'check_suite', label: 'Check suites', hint: 'CI checks complete' },
  { name: 'discussion', label: 'Discussions', hint: 'created, answered, …' },
]
// A fresh GitHub trigger listens to pull requests: the review-on-PR agent is
// the case the flavour exists for, and an empty selection would fire on
// everything the webhook sends.
const DEFAULT_GITHUB_EVENTS: string[] = ['pull_request']
// The spec.filter keys the structured GitHub fields own. The "more filters"
// lines may not restate them, or the two would silently fight over one key.
const GITHUB_FILTER_KEYS = ['eventType', 'action', 'repository']

// sourceModel routes the Source select through switchSource so a change of
// flavour carries the filter across instead of dropping it.
const sourceModel = computed({ get: () => draft.source, set: (next: string) => switchSource(next) })
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

const splitList = (value: string | undefined): string[] => (value ?? '').split(',').map(entry => entry.trim()).filter(Boolean)

// splitGitHubFilter lifts the keys the structured GitHub fields own out of a
// spec.filter map; whatever is left is what the "more filters" lines show.
function splitGitHubFilter(filter: Record<string, string> | undefined): { events: string[]; otherEvents: string; actions: string; repos: string; rest: Record<string, string> } {
  const rest: Record<string, string> = {}
  for (const [key, value] of Object.entries(filter ?? {})) if (!GITHUB_FILTER_KEYS.includes(key)) rest[key] = value
  const events: string[] = []
  const other: string[] = []
  for (const name of splitList(filter?.eventType)) (GITHUB_EVENTS.some(event => event.name === name) ? events : other).push(name)
  return {
    events,
    otherEvents: other.join(', '),
    actions: splitList(filter?.action).join(', '),
    repos: splitList(filter?.repository).join(', '),
    rest,
  }
}

// draftFilter assembles the spec.filter the form currently describes: the
// key=value lines, plus — in GitHub mode — the structured fields. Each list
// key is written comma-separated, which the provider matches as any-of.
function draftFilter(): { filter: Record<string, string>; error: string } {
  const parsed = parseFilterText(draft.filter)
  if (parsed.error || draft.source !== 'github') return parsed
  const owned = Object.keys(parsed.filter).find(key => GITHUB_FILTER_KEYS.includes(key))
  if (owned) return { filter: parsed.filter, error: `${owned} is set by the fields above — remove that line.` }
  const filter = { ...parsed.filter }
  const picked = GITHUB_EVENTS.filter(event => draft.ghEvents.includes(event.name)).map(event => event.name)
  const events = [...picked, ...splitList(draft.ghOtherEvents).filter(name => !picked.includes(name))]
  if (events.length) filter.eventType = events.join(',')
  const actions = splitList(draft.ghActions)
  if (actions.length) filter.action = actions.join(',')
  const repos = splitList(draft.ghRepos)
  if (repos.length) filter.repository = repos.join(',')
  return { filter, error: '' }
}

// switchSource changes the flavour without losing the filter: going to GitHub
// lifts eventType/action/repository into the structured fields (defaulting
// the events when there were none), going back folds them into the lines.
function switchSource(next: string): void {
  if (next === draft.source) return
  if (next === 'github') {
    const parsed = parseFilterText(draft.filter)
    if (!parsed.error) {
      const split = splitGitHubFilter(parsed.filter)
      draft.ghEvents = split.events.length || split.otherEvents ? split.events : [...DEFAULT_GITHUB_EVENTS]
      draft.ghOtherEvents = split.otherEvents
      draft.ghActions = split.actions
      draft.ghRepos = split.repos
      draft.filter = filterToText(split.rest)
      filterError.value = ''
    }
  } else {
    const built = draftFilter()
    if (!built.error) {
      draft.filter = filterToText(built.filter)
      filterError.value = ''
    }
  }
  draft.source = next
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
  return sourceLabel(spec.source)
}

function statusText(row: Automation): string {
  const spec = automationSpec(row)
  const status = automationStatus(row)
  if (status?.disabledReason) return status.disabledReason
  if (spec.suspend) return 'paused'
  if (props.kind === 'schedule') return status?.nextRun ? `next ${fmtTime(status.nextRun)}` : 'armed'
  const fired = status?.lastFired ? `last fired ${fmtTime(status.lastFired)}` : 'armed'
  // A delivery that arrived and did not fire is the thing status used to
  // hide: say so on the row, so "armed" is never read as "nothing arrived".
  const last = status?.lastDelivery
  if (last && last.outcome !== 'fired') {
    const when = status?.lastReceived || last.time
    return `${fired} · last delivery ${fmtTime(when)} ${deliveryOutcomeLabel(last.outcome).toLowerCase()}${last.reason ? `: ${last.reason}` : ''}`
  }
  return fired
}

function toggleDeliveries(name: string): void {
  deliveriesFor.value = deliveriesFor.value === name ? null : name
}

// From the log's "Test filter": open the trigger's edit form with the tester
// pointed at that delivery.
function openEditWithDelivery(deliveryID: string): void {
  const name = deliveriesFor.value
  if (!name) return
  testDelivery.value = deliveryID
  emit('navigate', { kind: 'automation', resource: props.kind, agent: props.agent, action: 'edit', name })
}

// parseRetention reads the "keep last N deliveries" field: '' is the provider
// default, otherwise an integer within the schema's bounds.
function parseRetention(input: string | number): { value: number | undefined; error: string } {
  // v-model on a type="number" input hands back a number, or '' when cleared.
  const trimmed = String(input ?? '').trim()
  if (!trimmed) return { value: undefined, error: '' }
  const n = Number(trimmed)
  if (!Number.isInteger(n) || n < 0 || n > MAX_DELIVERY_RETENTION) {
    return { value: undefined, error: `Keep between 0 and ${MAX_DELIVERY_RETENTION} deliveries, or leave empty for the default (${DEFAULT_DELIVERY_RETENTION}).` }
  }
  return { value: n, error: '' }
}

function resetDraft(values: Draft = EMPTY): void {
  Object.assign(draft, values, { ghEvents: [...values.ghEvents] })
}

function openCreate(): void {
	emit('navigate', { kind: 'automation', resource: props.kind, agent: props.agent, action: 'create' })
}

function openEdit(row: Automation): void {
	emit('navigate', { kind: 'automation', resource: props.kind, agent: props.agent, action: 'edit', name: row.metadata.name })
}

function hydrateFilter(source: string | undefined, filter: Record<string, string> | undefined): Pick<Draft, 'filter' | 'ghEvents' | 'ghOtherEvents' | 'ghActions' | 'ghRepos'> {
  if (source !== 'github') return { filter: filterToText(filter), ghEvents: [], ghOtherEvents: '', ghActions: '', ghRepos: '' }
  const split = splitGitHubFilter(filter)
  return { filter: filterToText(split.rest), ghEvents: split.events, ghOtherEvents: split.otherEvents, ghActions: split.actions, ghRepos: split.repos }
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
    ...hydrateFilter(spec.source, spec.filter),
    task: spec.task || spec.checklist || '',
    channelRef: spec.channelRef || '',
    suspend: !!spec.suspend,
    deliveryRetention: spec.deliveryRetention === undefined ? '' : String(spec.deliveryRetention),
  })
  nameError.value = ''
  filterError.value = ''
  retentionError.value = ''
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
  // No connectionRef: neither flavour reads one, the token in the URL is the
  // whole handshake, and a stale value on an older trigger is left untouched.
  const body: TriggerPatch = {
    source: draft.source || 'webhook',
    task: draft.task,
    suspend: draft.suspend,
    channelRef: draft.channelRef,
  }
  const filter = filterPatch(currentEdit.value ? automationSpec(currentEdit.value).filter : undefined, draftFilter().filter)
  if (filter) body.filter = filter
  // deliveryRetention: a number sets it, '' on an edit that had one removes it
  // (null in the merge patch) so the provider default is back in force.
  const retention = parseRetention(draft.deliveryRetention).value
  const before = currentEdit.value ? automationSpec(currentEdit.value).deliveryRetention : undefined
  if (retention !== before) body.deliveryRetention = retention === undefined ? (currentEdit.value ? null : undefined) : retention
  if (body.deliveryRetention === undefined) delete body.deliveryRetention
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
    const built = draftFilter()
    if (built.error) {
      filterError.value = built.error
      return
    }
    const retention = parseRetention(draft.deliveryRetention)
    if (retention.error) {
      retentionError.value = retention.error
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

// The provider disables a schedule or trigger after repeated run failures and
// says why in status.disabledReason. Once the cause is fixed, this clears the
// disabling and the failure count so it fires again; spec.suspend (the user's
// own pause) is a different switch and is not touched.
async function enable(row: Automation): Promise<void> {
  if (actionBusy.value) return
  const authority = captureAuthority()
  const kind = props.kind
  const name = row.metadata.name
  const previous = row.status?.disabledReason
  actionBusy.value = `enable:${name}`
  try {
    await mutate(authority.store, {
      run: (): Promise<Automation> => kind === 'schedule'
        ? authority.api.enableSchedule(name)
        : authority.api.enableTrigger(name),
      success: `${cap(META[kind].one)} enabled.`,
      failure: 'Enable failed',
      optimistic: () => { if (row.status) row.status.disabledReason = undefined },
      rollback: () => { if (row.status) row.status.disabledReason = previous },
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
  deliveriesFor.value = null
  testDelivery.value = ''
  nameError.value = ''
  filterError.value = ''
  retentionError.value = ''
  copied.value = false
  formBusy.value = false
  actionBusy.value = ''
  resetDraft()
})

watch([() => props.createRoute, () => props.editName, () => revision.value], () => {
	if (!formRoute.value) {
		editing.value = null
		testDelivery.value = ''
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
          <ResourceTableActionButton v-if="kind === 'trigger'" :icon="Inbox" :tone="deliveriesFor === row.name ? 'accent' : undefined" :label="`${deliveriesFor === row.name ? 'Hide' : 'Show'} deliveries for ${row.name}`" :disabled="!!actionBusy" @click="toggleDeliveries(String(row.name))" />
          <ResourceTableActionButton v-if="automationStatus(row.resource as Automation)?.disabledReason" :icon="Power" tone="accent" :label="`Enable ${row.name}`" :busy-label="`Enabling ${row.name}…`" :busy="actionBusy === `enable:${row.name}`" :disabled="!!actionBusy" @click="enable(row.resource as Automation)" />
          <ResourceTableActionButton :icon="Play" :label="`Run ${row.name} now`" :busy="actionBusy === `run:${row.name}`" :disabled="!!actionBusy" @click="runNow(String(row.name))" />
          <ResourceTableEditButton :label="`Edit ${row.name}`" :disabled="!!actionBusy" @click="openEdit(row.resource as Automation)" />
          <ResourceTableActionButton :icon="automationSpec(row.resource as Automation).suspend ? Play : Pause" :label="`${automationSpec(row.resource as Automation).suspend ? 'Resume' : 'Pause'} ${row.name}`" :busy="actionBusy === `toggle:${row.name}`" :disabled="!!actionBusy" @click="toggleSuspend(row.resource as Automation)" />
          <ResourceTableDeleteButton :label="`Delete ${row.name}`" :busy="actionBusy === `delete:${row.name}`" :disabled="!!actionBusy" @click="del(String(row.name))" />
        </template>
      </ResourceTable>
    </div>
    <TriggerDeliveryLog v-if="deliveriesRow" :api="api" :store="store" :trigger="deliveriesRow" @navigate="emit('navigate', $event)" @test="openEditWithDelivery" />
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

    <!-- The trigger form carries the GitHub event grid, the filter tester and
         the delivery picker, so it takes the wide surface; a schedule is a
         handful of fields and keeps the default width. -->
    <form v-else class="agents-obj-form agents-guided-form k-create-surface" :class="{ 'k-create-surface--wide': kind === 'trigger' }" novalidate :aria-busy="formBusy" @submit.prevent="save">
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
          <label><span :id="`automation-${kind}-source-label`">Source</span><FormSelect v-model="sourceModel" :options="sourceOptions" :disabled="formBusy" :labelledby="`automation-${kind}-source-label`" :describedby="`automation-${kind}-source-hint`" /><span :id="`automation-${kind}-source-hint`" class="agents-hint">{{ draft.source === 'github' ? 'A repository or organization webhook on GitHub. Pick the events below; GitHub’s ping on setup is acknowledged and ignored.' : 'Any sender that can POST JSON to the webhook URL below.' }}</span></label>
          <template v-if="draft.source === 'github'">
            <fieldset class="agents-fieldset" :aria-describedby="`automation-${kind}-events-hint`">
              <legend class="agents-fieldset-legend">Events</legend>
              <div class="agents-checkgrid">
                <label v-for="event in GITHUB_EVENTS" :key="event.name" class="agents-check k-checkbox-hit"><input v-model="draft.ghEvents" type="checkbox" name="ghEvents" :value="event.name" :disabled="formBusy" /><span class="agents-check-copy"><span>{{ event.label }}</span><span class="agents-hint">{{ event.hint }}</span></span></label>
              </div>
              <span :id="`automation-${kind}-events-hint`" class="agents-hint">The event names this trigger fires on. Nothing selected fires on every event the webhook sends.</span>
            </fieldset>
            <div class="agents-grid2">
              <label>Other events<input v-model="draft.ghOtherEvents" class="k-input mono" name="ghOtherEvents" placeholder="deployment_status, create" :aria-describedby="`automation-${kind}-other-events-hint`" :disabled="formBusy" /><span :id="`automation-${kind}-other-events-hint`" class="agents-hint">Comma-separated event names not listed above.</span></label>
              <label>Actions<input v-model="draft.ghActions" class="k-input mono" name="ghActions" placeholder="opened, synchronize, reopened" :aria-describedby="`automation-${kind}-actions-hint`" :disabled="formBusy" /><span :id="`automation-${kind}-actions-hint`" class="agents-hint">Comma-separated payload actions. Empty fires on every action.</span></label>
            </div>
            <label>Repositories<input v-model="draft.ghRepos" class="k-input mono" name="ghRepos" placeholder="octo-org/api, octo-org/web" :aria-describedby="`automation-${kind}-repos-hint`" :disabled="formBusy" /><span :id="`automation-${kind}-repos-hint`" class="agents-hint">Comma-separated owner/repo. Empty accepts every repository that delivers here — the usual choice for an organization webhook.</span></label>
            <label>More filters<textarea v-model="draft.filter" class="k-input mono" name="filter" rows="2" :aria-describedby="filterError ? `automation-${kind}-filter-error` : `automation-${kind}-filter-hint`" :aria-invalid="filterError ? 'true' : undefined" placeholder="match=needs-review" :disabled="formBusy" @input="filterError = ''"></textarea><span v-if="filterError" :id="`automation-${kind}-filter-error`" class="agents-fielderr" role="alert">{{ filterError }}</span><span v-else :id="`automation-${kind}-filter-hint`" class="agents-hint">Optional, one key=value per line: match is a substring of the payload, header.&lt;name&gt; matches a request header.</span></label>
          </template>
          <label v-else>Filter<textarea v-model="draft.filter" class="k-input mono" name="filter" rows="2" :aria-describedby="filterError ? `automation-${kind}-filter-error` : `automation-${kind}-filter-hint`" :aria-invalid="filterError ? 'true' : undefined" placeholder="eventType=deployment.created" :disabled="formBusy" @input="filterError = ''"></textarea><span v-if="filterError" :id="`automation-${kind}-filter-error`" class="agents-fielderr" role="alert">{{ filterError }}</span><span v-else :id="`automation-${kind}-filter-hint`" class="agents-hint">One key=value per line. eventType matches the X-Event-Type header or the body’s type/action, action matches the body’s action, match is a substring of the payload, header.&lt;name&gt; matches a request header. eventType and action take a comma-separated list. Empty fires on every delivery.</span></label>
          <section v-if="editing && webhookURL" class="k-card agents-secret-handoff agents-webhook-url" aria-label="Webhook URL">
            <div>
              <strong>Webhook URL</strong>
              <code data-testid="automation-webhook-url">{{ webhookURL }}</code>
              <span class="agents-hint">{{ draft.source === 'github' ? 'On GitHub, under Settings → Webhooks → Add webhook: Payload URL is this URL, Content type is application/json, Secret stays empty (the URL carries the token), and under “Let me select individual events” tick the events chosen above. ' : 'POST JSON deliveries here. ' }}The URL embeds this trigger’s secret token; treat it like a password.</span>
            </div>
            <div class="agents-secret-handoff-actions">
              <button type="button" class="k-btn k-btn--ghost" :disabled="formBusy" @click="copyWebhookURL(webhookURL)"><Check v-if="copied" aria-hidden="true" /><Copy v-else aria-hidden="true" /> {{ copied ? 'Copied' : 'Copy webhook URL' }}</button>
            </div>
            <span class="sr-only" role="status" aria-live="polite">{{ copied ? 'Webhook URL copied.' : '' }}</span>
          </section>
          <p v-else-if="editing && wantsWebhook" class="agents-hint" role="status">The webhook URL has not been minted yet — the provider stamps it shortly after the trigger is created. Reload to pick it up.</p>
          <p v-else-if="!editing && wantsWebhook" class="agents-hint">The inbound webhook URL appears here, and on the trigger row, once the trigger is created.</p>
          <TriggerFilterTester v-if="editing && currentEdit" :api="api" :store="store" :trigger="currentEdit as Trigger" :filter="liveFilter.filter" :filter-error="liveFilter.error || filterError" :preselect="testDelivery" @navigate="emit('navigate', $event)" />
          <label :for="`automation-${kind}-retention`">Keep last N deliveries
            <input :id="`automation-${kind}-retention`" v-model="draft.deliveryRetention" class="k-input" name="deliveryRetention" type="number" inputmode="numeric" min="0" :max="MAX_DELIVERY_RETENTION" step="1" :placeholder="String(DEFAULT_DELIVERY_RETENTION)" :disabled="formBusy" :aria-invalid="retentionError ? 'true' : undefined" :aria-describedby="retentionError ? `automation-${kind}-retention-error` : `automation-${kind}-retention-hint`" @input="retentionError = ''" />
            <span v-if="retentionError" :id="`automation-${kind}-retention-error`" class="agents-fielderr" role="alert">{{ retentionError }}</span>
            <span v-else :id="`automation-${kind}-retention-hint`" class="agents-hint">How many recent deliveries the provider keeps for the log and the tester above — fired, filtered or refused. Empty uses the provider default ({{ DEFAULT_DELIVERY_RETENTION }}); 0 keeps none.</span>
          </label>
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
