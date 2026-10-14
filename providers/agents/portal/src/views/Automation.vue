<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { Pause, Play, Plus, Power } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import { hashFor, type Route } from '../router'
import type { AppStore } from '../store'
import {
  CONDITION_VALIDATED,
  fmtTime,
  type KubeCondition,
  type Schedule,
  type ScheduleCreate,
  type SchedulePatch,
  type Trigger,
  type TriggerCreate,
  type TriggerPatch,
  type Webhook,
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
import { useAgentDOMID, useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import TriggerFilterTester from './TriggerFilterTester.vue'
import { deliveryOutcomeLabel } from './webhook-deliveries'

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
  /** spec.webhookRef: the workspace Webhook this trigger routes from. */
  webhookRef: string
  /** Which layout the filter fields currently hold: the GitHub flavour (the
   *  structured event/action/repository fields) or the generic lines. It
   *  follows the selected webhook's spec.source; see the flavour watch. */
  github: boolean
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
}

const domID = useAgentDOMID()

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
    blurb: 'Events that wake this agent. Each trigger routes from a workspace webhook and fires when its filter matches.',
    empty: 'No triggers yet.',
    whenHeader: 'Webhook',
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
  webhookRef: '',
  github: false,
  filter: '',
  ghEvents: [],
  ghOtherEvents: '',
  ghActions: '',
  ghRepos: '',
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
const webhookError = ref('')
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
const webhookSlice = computed(() => {
  void revision.value
  return { ...props.store.webhooks }
})
const webhooks = computed<Webhook[]>(() => webhookSlice.value.data)
const selectedWebhook = computed(() => webhooks.value.find(webhook => webhook.metadata.name === draft.webhookRef))
// wantsGitHub is the flavour the selected webhook asks for. Until the
// webhooks have loaded it is unknown, and the draft keeps the layout it has.
const wantsGitHub = computed<boolean | null>(() => selectedWebhook.value ? selectedWebhook.value.spec.source === 'github' : null)
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
const SOURCE_LABEL: Record<string, string> = { webhook: 'generic', github: 'GitHub' }
// The Webhook select names each webhook and its sender, since the sender is
// what decides which filter fields the form offers. A webhookRef that names no
// loaded webhook (deleted, or a typo through another writer) stays selectable
// so an edit does not silently retarget the trigger.
const webhookOptions = computed(() => {
  const options = webhooks.value.map(webhook => ({
    value: webhook.metadata.name,
    label: `${webhook.spec.displayName ? `${webhook.spec.displayName} (${webhook.metadata.name})` : webhook.metadata.name} · ${SOURCE_LABEL[webhook.spec.source] ?? webhook.spec.source}`,
  }))
  if (draft.webhookRef && !options.some(option => option.value === draft.webhookRef)) {
    options.unshift({ value: draft.webhookRef, label: `${draft.webhookRef} · not found` })
  }
  return options
})

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
// The filter keys the provider's matcher knows, besides header.<name>. Any
// other key is stored, flagged by the reconciler (UnknownFilterKey) and then
// ignored — so a typo would silently widen the filter. Refuse it here.
const KNOWN_FILTER_KEYS = ['eventType', 'action', 'repository', 'match']

function unknownFilterKey(filter: Record<string, string>): string | undefined {
  return Object.keys(filter).find(key => !KNOWN_FILTER_KEYS.includes(key) && !/^header\.[^\s.][^\s]*$/.test(key))
}

const channelOptions = computed(() => [
  { value: '', label: '— primary channel —' },
  ...channels.value.map(channel => ({ value: channel.name, label: `${channel.name}${channel.primary ? ' (primary)' : ''}` })),
])

// spec.filter is edited as one key=value per line: the map is small, the keys
// are a fixed vocabulary (see KNOWN_FILTER_KEYS) and a table editor
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
  if (parsed.error) return parsed
  const unknown = unknownFilterKey(parsed.filter)
  if (unknown) return { filter: parsed.filter, error: `“${unknown}” is not a filter key — use eventType, action, repository, match or header.<name>.` }
  if (!draft.github) return parsed
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

// switchFlavour changes the layout without losing the filter: going to GitHub
// lifts eventType/action/repository into the structured fields, going back
// folds them into the lines. A new trigger with no events yet gets the
// default selection; an existing one keeps exactly the filter it has.
function switchFlavour(github: boolean): void {
  if (github === draft.github) return
  if (github) {
    const parsed = parseFilterText(draft.filter)
    if (!parsed.error) {
      const split = splitGitHubFilter(parsed.filter)
      draft.ghEvents = split.events.length || split.otherEvents || editing.value ? split.events : [...DEFAULT_GITHUB_EVENTS]
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
  draft.github = github
}

// The flavour follows the selected webhook: picking another webhook, or the
// webhooks arriving after the form hydrated, re-lays the filter fields.
watch(wantsGitHub, (github) => {
  if (props.kind === 'trigger' && formRoute.value && github !== null) switchFlavour(github)
})

// filterPatch is the merge-patch fragment that turns `before` into `after`:
// changed and added keys carry their value, removed keys carry null, and an
// unchanged map yields undefined so the save does not touch spec.filter.
function filterPatch(before: Record<string, string> | undefined, after: Record<string, string>): Record<string, string | null> | undefined {
  const out: Record<string, string | null> = {}
  for (const [key, value] of Object.entries(after)) if (before?.[key] !== value) out[key] = value
  for (const key of Object.keys(before ?? {})) if (!(key in after)) out[key] = null
  return Object.keys(out).length > 0 ? out : undefined
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
  return spec.webhookRef || '—'
}

// invalidCondition is a trigger's Validated=False condition: a webhookRef that
// names no webhook (UnknownWebhookRef, MissingWebhookRef) or a filter key the
// matcher ignores (UnknownFilterKey). It is shown on the row because each one
// means the trigger is not doing what its form says.
function invalidCondition(row: Automation): KubeCondition | undefined {
  if (props.kind !== 'trigger') return undefined
  const condition = (automationStatus(row)?.conditions ?? []).find(c => c.type === CONDITION_VALIDATED)
  return condition?.status === 'False' ? condition : undefined
}

function conditionText(condition: KubeCondition): string {
  return condition.message || condition.reason || 'Validation failed'
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

function openWebhook(name: string): void {
  if (name) emit('navigate', { kind: 'edit', resource: 'webhook', name })
}

function createWebhook(): void {
  emit('navigate', { kind: 'create', resource: 'webhook' })
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

function hydrateFilter(github: boolean, filter: Record<string, string> | undefined): Pick<Draft, 'github' | 'filter' | 'ghEvents' | 'ghOtherEvents' | 'ghActions' | 'ghRepos'> {
  if (!github) return { github, filter: filterToText(filter), ghEvents: [], ghOtherEvents: '', ghActions: '', ghRepos: '' }
  const split = splitGitHubFilter(filter)
  return { github, filter: filterToText(split.rest), ghEvents: split.events, ghOtherEvents: split.otherEvents, ghActions: split.actions, ghRepos: split.repos }
}

function hydrateEdit(row: Automation): void {
  const spec = automationSpec(row)
  resetDraft({
    name: row.metadata.name,
    type: spec.type || 'cron',
    schedule: spec.schedule || '',
    runAt: spec.runAt || '',
    timeZone: spec.timeZone || '',
    webhookRef: spec.webhookRef || '',
    ...hydrateFilter(props.store.webhooks.data.find(webhook => webhook.metadata.name === spec.webhookRef)?.spec.source === 'github', spec.filter),
    task: spec.task || spec.checklist || '',
    channelRef: spec.channelRef || '',
    suspend: !!spec.suspend,
  })
  nameError.value = ''
  filterError.value = ''
  webhookError.value = ''
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
  // The URL, its token and the delivery log belong to the Webhook; a trigger
  // only names which webhook it routes from and what it fires on.
  const body: TriggerPatch = {
    webhookRef: draft.webhookRef,
    task: draft.task,
    suspend: draft.suspend,
    channelRef: draft.channelRef,
  }
  const filter = filterPatch(currentEdit.value ? automationSpec(currentEdit.value).filter : undefined, draftFilter().filter)
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
    if (!draft.webhookRef) {
      webhookError.value = 'Choose the webhook this trigger routes from.'
      return
    }
    const built = draftFilter()
    if (built.error) {
      filterError.value = built.error
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
  nameError.value = ''
  filterError.value = ''
  webhookError.value = ''
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
			webhookError.value = ''
			editing.value = ''
			// A workspace with exactly one webhook has only one answer; pick it
			// so the common case is one less step.
			if (props.kind === 'trigger' && webhooks.value.length === 1) draft.webhookRef = webhooks.value[0].metadata.name
			if (wantsGitHub.value !== null) switchFlavour(wantsGitHub.value)
		}
		return
	}
	if (currentEdit.value && editing.value !== props.editName) hydrateEdit(currentEdit.value)
}, { immediate: true })

const cap = (value: string): string => value.charAt(0).toUpperCase() + value.slice(1)
</script>

<template>
  <ResourceSectionCard v-if="!formRoute" class="agents-config-sec" :heading-id="domID(`agent-${kind}-heading`)" :title="meta.title" :description="meta.blurb">
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
        <template #when="{ row }">
          <a v-if="kind === 'trigger' && automationSpec(row.resource as Automation).webhookRef" class="mono k-dashboard-action" :href="hashFor({ kind: 'edit', resource: 'webhook', name: automationSpec(row.resource as Automation).webhookRef })" @click.prevent="openWebhook(automationSpec(row.resource as Automation).webhookRef)">{{ row.when }}</a>
          <span v-else class="mono">{{ row.when }}</span>
        </template>
        <template #status="{ row }">
          <span class="muted">
            <span v-if="automationStatus(row.resource as Automation)?.disabledReason" class="k-badge agents-badge k-badge--warning agents-badge-warn">{{ automationStatus(row.resource as Automation)?.disabledReason }}</span>
            <span v-else-if="invalidCondition(row.resource as Automation)" class="k-badge agents-badge k-badge--warning agents-badge-warn" :title="invalidCondition(row.resource as Automation)?.reason">{{ conditionText(invalidCondition(row.resource as Automation)!) }}</span>
            <span v-else-if="automationSpec(row.resource as Automation).suspend" class="k-badge agents-badge">paused</span>
            <template v-else>{{ statusText(row.resource as Automation) }}</template>
            <button v-if="automationStatus(row.resource as Automation)?.lastRunID" class="k-dashboard-action" type="button" @click="emit('navigate', { kind: 'run', id: automationStatus(row.resource as Automation)!.lastRunID! })">last run</button>
          </span>
        </template>
        <template #actions="{ row }">
          <ResourceTableActionButton v-if="automationStatus(row.resource as Automation)?.disabledReason" :icon="Power" tone="accent" :label="`Enable ${row.name}`" :busy-label="`Enabling ${row.name}…`" :busy="actionBusy === `enable:${row.name}`" :disabled="!!actionBusy" @click="enable(row.resource as Automation)" />
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

    <!-- The trigger form carries the GitHub event grid and the filter tester
         with its delivery picker, so it takes the wide surface; a schedule is
         a handful of fields and keeps the default width. -->
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
          <label v-if="webhooks.length || draft.webhookRef"><span :id="`automation-${kind}-webhook-label`">Webhook *</span><FormSelect v-model="draft.webhookRef" :options="webhookOptions" placeholder="Choose a webhook" :disabled="formBusy" required :invalid="!!webhookError" :labelledby="`automation-${kind}-webhook-label`" :describedby="webhookError ? `automation-${kind}-webhook-error` : `automation-${kind}-webhook-hint`" name="webhookRef" @update:model-value="webhookError = ''" /><span v-if="webhookError" :id="`automation-${kind}-webhook-error`" class="agents-fielderr" role="alert">{{ webhookError }}</span><span v-else :id="`automation-${kind}-webhook-hint`" class="agents-hint">{{ draft.github ? 'A GitHub webhook. Pick the events below; GitHub’s ping on setup is acknowledged and ignored.' : 'Deliveries to this webhook’s URL are offered to every trigger routing from it; this trigger fires on the ones its filter matches.' }}</span></label>
          <p v-if="webhooks.length || draft.webhookRef" class="agents-webhook-links">
            <span v-if="selectedWebhook" class="agents-hint">The URL to give the sender and the delivery log live on the webhook.</span>
            <a v-if="selectedWebhook" class="k-dashboard-action" :href="hashFor({ kind: 'edit', resource: 'webhook', name: draft.webhookRef })" @click.prevent="openWebhook(draft.webhookRef)">Open webhook {{ draft.webhookRef }}</a>
            <a class="k-dashboard-action" :href="hashFor({ kind: 'create', resource: 'webhook' })" @click.prevent="createWebhook">New webhook</a>
          </p>
          <div v-else class="agents-webhook-empty" role="status">
            <span id="automation-trigger-webhook-empty" class="agents-hint">{{ webhookSlice.hasSnapshot ? 'A trigger routes from a webhook, and this workspace has none yet. Create one — it gives you the URL to paste into GitHub or any sender — then come back.' : webhookSlice.error ? `Could not load webhooks. ${webhookSlice.error}` : 'Loading webhooks…' }}</span>
            <span v-if="webhookError" :id="`automation-${kind}-webhook-error`" class="agents-fielderr" role="alert">{{ webhookError }}</span>
            <button v-if="webhookSlice.hasSnapshot" type="button" class="k-btn k-btn--ghost secondary" :disabled="formBusy" aria-describedby="automation-trigger-webhook-empty" @click="createWebhook"><Plus :stroke-width="1.75" aria-hidden="true" /> Create a webhook</button>
            <button v-else-if="webhookSlice.error" type="button" class="k-btn k-btn--ghost secondary" :disabled="webhookSlice.loading" @click="store.load('webhooks')">{{ webhookSlice.loading ? 'Retrying…' : 'Retry' }}</button>
          </div>
          <template v-if="draft.github">
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
          <TriggerFilterTester v-if="editing && currentEdit" :api="api" :store="store" :trigger="currentEdit as Trigger" :filter="liveFilter.filter" :filter-error="liveFilter.error || filterError" :draft-webhook="draft.webhookRef" @navigate="emit('navigate', $event)" />
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
