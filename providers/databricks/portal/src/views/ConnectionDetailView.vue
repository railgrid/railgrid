<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { api } from '../api'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import type { Connection, ErrorResponse } from '../types'
import { createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController } from '../refresh'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const conn = ref<Connection | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const editing = ref(false)
const editHost = ref('')
const editToken = ref('')
const saving = ref(false)
const saveError = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const editHostInput = ref<HTMLInputElement | null>(null)
const saveErrorRef = ref<HTMLElement | null>(null)
let timer: number | undefined
let refresh!: LatestRefreshController
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

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function load() {
  refresh.request()
}

function operationLocked(name: string): boolean {
  return operations.isLocked(operationKey('connection', name))
}

function operationPhase(name: string) {
  return operations.phase(operationKey('connection', name))
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
    await api.saveConnection({
      name: conn.value.name,
      host,
      secretName: conn.value.secretName,
      secretNamespace: conn.value.secretNamespace,
      secretKey: conn.value.secretKey,
      // An empty token intentionally preserves the existing Secret. Enter a
      // new token here only when rotating the credential.
      token: editToken.value || undefined,
    })
    editing.value = false
    editToken.value = ''
    load()
  } catch (e) {
    await focusSaveError(errMessage(e))
  } finally {
    saving.value = false
    operations.release(lock)
  }
}

async function remove() {
  if (!conn.value) return
  const ok = await confirmDialog({
    title: `Delete connection "${conn.value.name}"?`,
    message: 'Warehouses and tables that reference this connection will stop working.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  const lock = operationKey('connection', conn.value.name)
  if (!operations.acquire(lock, 'deleting')) {
    mutationError.value = `Connection "${conn.value.name}" already has an operation in progress.`
    return
  }
  mutationError.value = null
  try {
    await api.deleteConnection(conn.value)
    operations.tombstone(lock, conn.value.uid)
    emit('back')
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

refresh = createLatestRefreshController(async requestID => {
  loading.value = true
  try {
    const next = await api.getConnection(props.name)
    if (refresh.isCurrent(requestID)) {
      conn.value = next
      loaded.value = true
      error.value = null
    }
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (refresh.isCurrent(requestID)) loading.value = false
  }
})

watch(() => props.name, () => {
  conn.value = null
  loaded.value = false
  editing.value = false
  error.value = null
  mutationError.value = null
  refresh.invalidate()
  load()
})

onMounted(() => {
  load()
  timer = window.setInterval(load, 5000)
})
onUnmounted(() => {
  window.clearInterval(timer)
  refresh.stop()
})
</script>

<template>
  <section class="page">
    <button class="link back" type="button" :disabled="!!conn && operationLocked(conn.name)" @click="emit('back')">← Connections</button>

    <header class="page-head">
      <div>
        <h2 class="page-title">{{ conn?.name || name }}</h2>
        <p class="page-meta">
          <span v-if="conn?.status === 'Ready'">validated against <code>{{ conn.host }}</code></span>
          <span v-else-if="conn"><code>{{ conn.host }}</code></span>
          <span v-else class="muted">not validated yet</span>
        </p>
      </div>
      <StatusBadge v-if="conn" :status="conn.status" :title="conn.message" />
    </header>

    <div v-if="error && !conn" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ error }}</span>
      <button class="secondary" type="button" @click="load">Retry</button>
    </div>
    <p v-else-if="loading && !conn" class="muted" role="status" aria-live="polite">Loading…</p>
    <div v-if="error && conn" class="error read-error" role="alert" aria-live="assertive">
      <span>Showing cached connection data. {{ error }}</span>
      <button class="secondary" type="button" @click="load">Retry</button>
    </div>
    <span v-else-if="loading && conn" class="sr-only" role="status" aria-live="polite">Updating…</span>
    <div v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
      <span>{{ mutationError }}</span>
      <button class="secondary" type="button" @click="mutationError = null">Dismiss</button>
    </div>

    <template v-if="conn">
      <div v-if="hint" class="panel">
        <h3 class="panel-title">Status</h3>
        <p class="muted">{{ hint }}</p>
      </div>

      <div class="panel">
        <h3 class="panel-title">Overview</h3>
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
      </div>

      <div v-if="editing" class="panel">
        <h3 class="panel-title">Update connection</h3>
        <p class="field-hint">Change the workspace host or rotate the token. Leave the token blank to keep the current Secret.</p>
        <form class="form" @submit.prevent="saveEdit">
          <div class="field">
            <label class="field-label" for="connection-edit-host">Workspace host</label>
            <input id="connection-edit-host" ref="editHostInput" v-model="editHost" :disabled="saving" autocomplete="url" required aria-required="true" aria-describedby="connection-edit-host-hint connection-edit-error" :aria-invalid="!!saveError" />
            <span id="connection-edit-host-hint" class="field-hint">Use the HTTPS root URL from Databricks, with no path.</span>
          </div>
          <div class="field">
            <label class="field-label" for="connection-edit-token">New token <span class="muted">(optional)</span></label>
            <input id="connection-edit-token" v-model="editToken" :disabled="saving" type="password" autocomplete="new-password" placeholder="Leave blank to keep current token" aria-describedby="connection-edit-token-hint connection-edit-error" :aria-invalid="!!saveError" />
            <span id="connection-edit-token-hint" class="field-hint">Create a replacement personal access token in Databricks before pasting it here. The existing token remains in place when this is blank.</span>
          </div>
          <div class="actions">
            <button class="primary" type="submit" :disabled="saving || operationLocked(conn.name)">{{ saving ? 'Saving…' : 'Save changes' }}</button>
            <button class="secondary" type="button" :disabled="saving" @click="editing = false">Cancel</button>
            <span v-if="saveError" id="connection-edit-error" ref="saveErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ saveError }}</span>
          </div>
        </form>
      </div>

      <ConditionsPanel
        :conditions="conn.conditions"
        :generation="conn.generation"
        :observed-generation="conn.observedGeneration"
        empty-text="No conditions yet. Connection validation has not reported status for this resource."
      />

      <div class="actions">
        <button class="secondary" type="button" :disabled="operationLocked(conn.name)" @click="startEdit">Edit connection</button>
        <button class="danger resource-delete-button" type="button" :disabled="operationLocked(conn.name)" @click="remove">{{ operationPhase(conn.name) === 'deleting' ? 'Deleting connection…' : 'Delete connection' }}</button>
      </div>
    </template>
  </section>
</template>
