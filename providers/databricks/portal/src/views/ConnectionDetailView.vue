<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { Activity, Database, KeyRound, RefreshCw, Settings2 } from 'lucide-vue-next'
import { api } from '../api'
import ActionMenu, { type ActionMenuItem } from '../portalkit/ActionMenu.vue'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceStatCards, { type ResourceStatCard } from '../portalkit/ResourceStatCards.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import { toast } from '../portalkit/toast'
import { formatDatabricksError, isTenantMissingError } from '../errors'
import type { Connection } from '../types'
import {
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  createOperationLocks,
  FAST_REFRESH_MS,
  operationKey,
  STABLE_REFRESH_MS,
  type AdaptiveRefreshTimer,
  type LatestRefreshController,
  type ResourceRefreshMode,
} from '../refresh'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const conn = ref<Connection | null>(null)
const loading = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const loaded = ref(false)
const error = ref<string | null>(null)
const editing = ref(false)
const editHost = ref('')
const editToken = ref('')
const saving = ref(false)
const deleting = ref(false)
const saveError = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const editHostInput = ref<HTMLInputElement | null>(null)
const saveErrorRef = ref<HTMLElement | null>(null)
let poll!: AdaptiveRefreshTimer
let refresh!: LatestRefreshController
let mounted = false
const operations = createOperationLocks()

const validated = computed(() => conn.value?.conditions.find(c => c.type === 'Validated'))
const reconciled = computed(() =>
  !!conn.value &&
  conn.value.observedGeneration !== undefined &&
  conn.value.generation !== undefined &&
  conn.value.observedGeneration >= conn.value.generation,
)

const hint = computed(() => {
  const c = conn.value
  if (!c) return ''
  if (c.status === 'Ready') return ''
  if (!c.conditions.length || !reconciled.value) {
    return 'Waiting for the connection controller to validate the credential. This usually takes a few seconds after creation.'
  }
  switch (validated.value?.reason) {
    case 'CredentialUnavailable':
      return `The credential Secret could not be read. Check that "${c.secretNamespace}/${c.secretName}" contains key "${c.secretKey || 'token'}".`
    case 'ValidationFailed':
      return 'Databricks rejected the credential. The token may be expired, revoked, or missing access to the workspace.'
    default:
      return validated.value?.message || c.message || 'The connection is not validated yet.'
  }
})

type StatTone = 'default' | 'success' | 'warning' | 'danger'

function statTone(status: string | undefined): StatTone {
  if (!status) return 'default'
  if (status === 'Ready' || status === 'Validated') return 'success'
  if (/fail|error|attention|unavailable/i.test(status)) return 'danger'
  return 'warning'
}

const readState = computed<boolean | null>(() => {
  if (loaded.value) return true
  if (error.value) return false
  return loading.value ? false : null
})

const statCards = computed<ResourceStatCard[]>(() => [
  {
    id: 'status',
    label: 'Status',
    value: deleting.value ? 'Deleting' : conn.value?.status || '—',
    detail: deleting.value ? 'Deletion in progress' : hint.value || undefined,
    icon: Activity,
    tone: statTone(deleting.value ? 'Deleting' : conn.value?.status),
  },
  {
    id: 'auth',
    label: 'Authentication',
    value: conn.value?.authType || '—',
    icon: KeyRound,
    mono: true,
  },
  {
    id: 'workspace',
    label: 'Workspace ID',
    value: conn.value?.workspaceID || '—',
    icon: Database,
    mono: true,
  },
])

function requestRefresh(mode: ResourceRefreshMode): void {
  if (mode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  refresh.request(mode)
}

function load(): void {
  requestRefresh('foreground')
}

function pollCadence(): number {
  const stable = loaded.value && !error.value && !deleting.value && !!conn.value &&
    conn.value.status === 'Ready' && operationPhase(conn.value.name) !== 'deleting'
  return stable ? STABLE_REFRESH_MS : FAST_REFRESH_MS
}

poll = createAdaptiveRefreshTimer(() => requestRefresh('background'), pollCadence)

function operationLocked(name: string): boolean {
  return operations.isLocked(operationKey('connection', name))
}

function operationPhase(name: string) {
  return operations.phase(operationKey('connection', name))
}

const connectionActionBusy = computed(() =>
  !conn.value ||
  loading.value ||
  deleting.value ||
  operationLocked(conn.value?.name || props.name),
)
const connectionDeletePending = computed(() =>
  deleting.value || operationPhase(conn.value?.name || props.name) === 'deleting',
)
const actionItems = computed<ActionMenuItem[]>(() => [{
  id: 'delete',
  label: connectionDeletePending.value ? 'Deleting connection…' : 'Delete connection',
  tone: 'danger',
  disabled: connectionActionBusy.value,
  busy: connectionDeletePending.value,
}])

function selectAction(action: string): void {
  if (action === 'delete') void remove()
}

function goBack() {
  if (deleting.value || (conn.value && operationLocked(conn.value.name))) return
  emit('back')
}

function startEdit() {
  if (!conn.value) return
  if (operationLocked(conn.value.name)) return
  editHost.value = conn.value.host
  editToken.value = ''
  saveError.value = null
  editing.value = true
  void nextTick(() => editHostInput.value?.focus())
}

async function focusSaveError(message: string) {
  saveError.value = message
  await nextTick()
  saveErrorRef.value?.focus()
}

async function saveEdit() {
  if (!conn.value) return
  const host = editHost.value.trim()
  if (!host) {
    await focusSaveError('Workspace host is required.')
    return
  }
  const lock = operationKey('connection', conn.value.name)
  if (!operations.acquire(lock, 'saving')) {
    await focusSaveError(`Connection "${conn.value.name}" already has an operation in progress.`)
    return
  }
  saving.value = true
  saveError.value = null
  mutationError.value = null
  try {
    const current = conn.value
    await api.saveConnection({
      name: current.name,
      host,
      secretName: current.secretName,
      secretNamespace: current.secretNamespace,
      secretKey: current.secretKey,
      // An empty token intentionally preserves the existing Secret. Enter a
      // new token here only when rotating the credential.
      token: editToken.value || undefined,
    })
    if (!mounted || props.name !== current.name) return
    editing.value = false
    editToken.value = ''
    toast('ok', `Connection ${current.name} saved.`)
    load()
  } catch (e) {
    await focusSaveError(formatDatabricksError(e))
  } finally {
    saving.value = false
    operations.release(lock)
  }
}

async function remove() {
  const current = conn.value
  if (!current || deleting.value) return
  const ok = await confirmDialog({
    title: `Delete connection "${current.name}"?`,
    message: 'Warehouses and tables that reference this connection will stop working.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  const lock = operationKey('connection', current.name)
  if (!operations.acquire(lock, 'deleting')) {
    mutationError.value = `Connection "${current.name}" already has an operation in progress.`
    return
  }
  deleting.value = true
  mutationError.value = null
  try {
    await api.deleteConnection(current)
    if (!mounted || props.name !== current.name) return
    operations.tombstone(lock, current.uid)
    toast('info', `Connection deletion requested for ${current.name}.`)
    emit('back')
  } catch (e) {
    mutationError.value = formatDatabricksError(e)
  } finally {
    deleting.value = false
    operations.release(lock)
  }
}

refresh = createLatestRefreshController(async (requestID, mode) => {
  refreshMode.value = mode
  if (mode === 'foreground') loading.value = true
  try {
    const next = await api.getConnection(props.name)
    if (refresh.isCurrent(requestID)) {
      conn.value = next
      loaded.value = true
      error.value = null
    }
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    error.value = isTenantMissingError(e) ? null : formatDatabricksError(e)
  } finally {
    if (refresh.isCurrent(requestID)) {
      if (mode === 'foreground') loading.value = false
      poll.schedule()
    }
  }
})

watch(() => props.name, () => {
  conn.value = null
  loaded.value = false
  deleting.value = false
  editing.value = false
  error.value = null
  mutationError.value = null
  refresh.invalidate()
  load()
})

onMounted(() => {
  mounted = true
  load()
})
onUnmounted(() => {
  mounted = false
  poll.stop()
  refresh.stop()
})
</script>

<template>
  <section class="databricks-resource-detail">
    <ResourceBackLink
      href="/ui/providers/databricks/connections"
      :disabled="deleting || (!!conn && operationLocked(conn.name))"
      @back="goBack"
    >
      Connections
    </ResourceBackLink>

    <ResourcePage
      :title="conn?.name || name"
      kind="Connection"
      :loaded="readState"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      @retry="load"
    >
      <template #meta>
        <span>Databricks</span>
      </template>
      <template v-if="conn" #status><StatusBadge :status="deleting ? 'Deleting' : conn.status" :tone="deleting ? 'warning' : null" :title="conn.message" /></template>
      <template #actions>
        <div class="databricks-resource-actions" role="group" aria-label="Connection actions">
          <button class="k-btn k-btn--ghost icon-text" type="button" :disabled="loading || deleting || (!!conn && operationLocked(conn.name))" :aria-busy="loading || undefined" @click="load">
            <RefreshCw :size="14" :class="{ spin: loading }" aria-hidden="true" />
            {{ loading ? 'Refreshing…' : 'Refresh' }}
          </button>
          <ActionMenu
            label="More connection actions"
            :items="actionItems"
            :disabled="connectionActionBusy"
            @select="selectAction"
          />
        </div>
      </template>
      <template #summary><ResourceStatCards :cards="statCards" density="compact" aria-label="Connection summary" /></template>
      <template #body>
        <p v-if="deleting" class="warning deletion-progress" role="status" aria-live="polite">Deleting this connection. The last successful snapshot remains visible until the hub confirms removal.</p>
        <p v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
          <span>{{ mutationError }}</span>
          <button class="k-btn k-btn--ghost" type="button" @click="mutationError = null">Dismiss</button>
        </p>

        <div v-if="conn" class="databricks-resource-sections" :class="['databricks-resource-sections--connection', { 'databricks-resource-sections--editing': editing }]">
          <ResourceSectionCard id="connection-overview" eyebrow="Connection" title="Overview" description="Workspace endpoint, authentication, and reconciliation details.">
            <template #actions>
              <button v-if="!editing" class="k-btn k-btn--ghost icon-text" type="button" :disabled="operationLocked(conn.name)" @click="startEdit"><Settings2 :size="14" aria-hidden="true" /> Edit connection</button>
            </template>
            <dl class="props">
          <dt>Workspace host</dt><dd><code>{{ conn.host }}</code></dd>
          <dt>Type</dt><dd>{{ conn.authType }}</dd>
          <dt>Secret</dt>
          <dd>
            <code>{{ conn.secretName }}</code>
            <span class="muted"> · ns <code>{{ conn.secretNamespace }}</code></span>
            <span class="muted"> · key <code>{{ conn.secretKey }}</code></span>
          </dd>
          <dt v-if="conn.workspaceID">Workspace ID</dt><dd v-if="conn.workspaceID"><code>{{ conn.workspaceID }}</code></dd>
          <dt v-if="conn.creationTimestamp">Created</dt><dd v-if="conn.creationTimestamp">{{ conn.creationTimestamp }}</dd>
          <dt v-if="conn.observedGeneration !== undefined">Reconciled</dt>
          <dd v-if="conn.observedGeneration !== undefined">
            <span v-if="reconciled" class="muted">up to date (generation {{ conn.generation }})</span>
            <span v-else class="warning">controller has not caught up (spec {{ conn.generation }}, observed {{ conn.observedGeneration }})</span>
          </dd>
            </dl>
          </ResourceSectionCard>

          <ResourceSectionCard v-if="editing" id="connection-edit" eyebrow="Configuration" title="Update connection" description="Change the workspace host or rotate the token without exposing the existing credential.">
        <p class="field-hint">Change the workspace host or rotate the token. Leave the token blank to keep the current Secret.</p>
        <form class="form" @submit.prevent="saveEdit">
          <div class="field">
            <label class="field-label" for="connection-edit-host">Workspace host</label>
            <input id="connection-edit-host" class="k-input" ref="editHostInput" v-model="editHost" :disabled="saving" autocomplete="url" required aria-required="true" aria-describedby="connection-edit-host-hint connection-edit-error" :aria-invalid="!!saveError" />
            <span id="connection-edit-host-hint" class="field-hint">Use the HTTPS root URL from Databricks, with no path.</span>
          </div>
          <div class="field">
            <label class="field-label" for="connection-edit-token">New token <span class="muted">(optional)</span></label>
            <input id="connection-edit-token" class="k-input" v-model="editToken" :disabled="saving" type="password" autocomplete="new-password" placeholder="Leave blank to keep current token" aria-describedby="connection-edit-token-hint connection-edit-error" :aria-invalid="!!saveError" />
            <span id="connection-edit-token-hint" class="field-hint">Create a replacement personal access token in Databricks before pasting it here. The existing token remains in place when this is blank.</span>
          </div>
          <div class="actions">
            <button class="k-btn k-btn--primary" type="submit" :disabled="saving || operationLocked(conn.name)">{{ saving ? 'Saving…' : 'Save changes' }}</button>
            <button class="k-btn k-btn--ghost" type="button" :disabled="saving" @click="editing = false">Cancel</button>
            <span v-if="saveError" id="connection-edit-error" ref="saveErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ saveError }}</span>
          </div>
        </form>
          </ResourceSectionCard>

          <ResourceSectionCard id="connection-conditions" eyebrow="Diagnostics" title="Conditions" description="Controller conditions and observed generation for this connection.">
            <ConditionsPanel
              :conditions="conn.conditions"
              :generation="conn.generation"
              :observed-generation="conn.observedGeneration"
              empty-text="No conditions yet. Connection validation has not reported status for this resource."
            />
          </ResourceSectionCard>
        </div>
      </template>
    </ResourcePage>
  </section>
</template>
