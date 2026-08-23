<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import { api } from '../api'
import type { ConnectionDetail, ErrorResponse } from '../types'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import { createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController, type ResourceDeletions } from '../refresh'

const props = defineProps<{ name: string; deletions: ResourceDeletions }>()
const emit = defineEmits<{ (e: 'back'): void }>()
const deletionScope = 'connection'

const conn = ref<ConnectionDetail | null>(null)
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const loading = ref(true)
const loaded = ref(false)
const operations = createOperationLocks()
let timer: number | undefined
let mounted = false
let refresh!: LatestRefreshController

const validated = computed(() => conn.value?.conditions.find(c => c.type === 'Validated'))
const deleting = computed(() => !!conn.value && (
  !!conn.value.deletionTimestamp || props.deletions.has(deletionScope, conn.value.name, conn.value.uid)
))
const reconciled = computed(() =>
  !!conn.value &&
  conn.value.observedGeneration !== undefined &&
  conn.value.generation !== undefined &&
  conn.value.observedGeneration >= conn.value.generation,
)
const hint = computed(() => {
  const c = conn.value
  if (!c || c.validated) return ''
  if (!c.conditions.length || !reconciled.value) {
    return 'Waiting for the connection controller to validate the credential. This usually takes a few seconds after creation.'
  }
  switch (validated.value?.reason) {
    case 'CredentialUnavailable':
      return `The credential Secret could not be read. Check that the Secret "${c.secretName}"` +
        (c.secretNamespace ? ` in namespace "${c.secretNamespace}"` : '') +
        ` exists and holds key "${c.secretKey || 'token'}".`
    case 'ValidationFailed':
      return 'The git host rejected the credential. The token may be expired, revoked, or missing the scopes needed for this owner.'
    case 'ProviderNotFound':
      return `No backend is registered for provider "${c.provider}". This is a provider configuration issue — contact a platform admin.`
    default:
      return validated.value?.message || 'The connection is not validated yet.'
  }
})

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function load() {
  refresh.request()
}

async function remove() {
  const connection = conn.value
  if (!connection || deleting.value) return
  const ok = await confirmDialog({
    title: `Delete connection "${connection.name}"?`,
    message: 'Repositories using it will stop reconciling.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok || !mounted) return
  const lock = operationKey('connection', connection.name)
  if (!operations.acquire(lock, 'deleting')) return
  mutationError.value = null
  try {
    await api.deleteConnection(connection.name)
    props.deletions.acknowledge(deletionScope, connection.name, connection.uid)
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
    if (!refresh.isCurrent(requestID)) return
    conn.value = next
    if (next.deletionTimestamp) props.deletions.acknowledge(deletionScope, next.name, next.uid)
    loaded.value = true
    error.value = null
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    if (err.reason === 'NotFound' && (deleting.value || props.deletions.has(deletionScope, props.name))) {
      conn.value = null
      emit('back')
      return
    }
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (refresh.isCurrent(requestID)) loading.value = false
  }
})

watch(() => props.name, () => {
  conn.value = null
  error.value = null
  mutationError.value = null
  loaded.value = false
  loading.value = true
  refresh.invalidate()
  load()
})

onMounted(() => {
  mounted = true
  load()
  timer = window.setInterval(load, 5000)
})
onUnmounted(() => {
  mounted = false
  window.clearInterval(timer)
  refresh.stop()
})
</script>

<template>
  <section class="page" :aria-busy="loading">
    <button class="k-btn k-btn--ghost code-back-action" type="button" @click="emit('back')"><ArrowLeft :size="14" aria-hidden="true" /> Connections</button>

    <header class="page-head">
      <div>
        <h2 class="page-title">{{ conn?.name || name }}</h2>
        <p class="page-meta">
          <span v-if="conn?.login">authenticated as <code>{{ conn.login }}</code></span>
          <span v-else-if="conn" class="muted">not validated yet</span>
          <span v-else-if="loading" class="muted">Loading connection details…</span>
          <span v-else class="muted">Connection details unavailable</span>
        </p>
      </div>
      <StatusBadge
        v-if="conn"
        :status="deleting ? 'Deleting' : conn.validated ? 'ready' : 'pending'"
        :tone="deleting ? 'warning' : null"
        :title="conn.message"
      />
    </header>

    <div v-if="error && !conn" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ error }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="load">Retry</button>
    </div>
    <div v-else-if="loading && !conn" class="detail-loading" role="status" aria-live="polite" aria-label="Loading connection details" aria-busy="true">
      <div v-for="i in 4" :key="i" class="shimmer detail-loading-line" />
    </div>
    <div v-if="error && conn" class="error read-error" role="alert" aria-live="assertive">
      <span>Showing cached connection data. {{ error }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="load">Retry</button>
    </div>
    <span v-else-if="loading && loaded" class="sr-only" role="status" aria-live="polite">Updating connection…</span>
    <p v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">{{ mutationError }}</p>

    <template v-if="conn">
      <div v-if="!deleting && !conn.validated && hint" class="panel k-card">
        <h3 class="panel-title">Status</h3>
        <p class="muted">{{ hint }}</p>
      </div>

      <div class="panel k-card">
        <h3 class="panel-title">Overview</h3>
        <dl class="props">
          <dt>Provider</dt><dd>{{ conn.provider }}</dd>
          <dt>Type</dt><dd>{{ conn.type }}</dd>
          <dt>Owner</dt><dd>{{ conn.owner }}</dd>
          <dt>Login</dt><dd>{{ conn.login || '—' }}</dd>
          <dt>Scopes</dt>
          <dd>
            <span v-if="conn.scopes.length"><code v-for="s in conn.scopes" :key="s" class="chip">{{ s }}</code></span>
            <span v-else class="muted">—</span>
          </dd>
          <dt>Secret</dt>
          <dd>
            <code>{{ conn.secretName }}</code>
            <span v-if="conn.secretNamespace" class="muted"> · ns <code>{{ conn.secretNamespace }}</code></span>
            <span v-if="conn.secretKey" class="muted"> · key <code>{{ conn.secretKey }}</code></span>
          </dd>
          <dt v-if="conn.baseURL">Base URL</dt><dd v-if="conn.baseURL"><code>{{ conn.baseURL }}</code></dd>
          <dt v-if="conn.observedGeneration !== undefined">Reconciled</dt>
          <dd v-if="conn.observedGeneration !== undefined">
            <span v-if="reconciled" class="muted">up to date (generation {{ conn.generation }})</span>
            <span v-else class="warn">controller has not caught up (spec {{ conn.generation }}, observed {{ conn.observedGeneration }})</span>
          </dd>
        </dl>
      </div>

      <ConditionsPanel
        :conditions="conn.conditions"
        :generation="conn.generation"
        :observed-generation="conn.observedGeneration"
        empty-text="No conditions yet — the controller has not reconciled this connection."
      />

      <div class="code-form-actions">
        <button class="k-btn k-btn--danger" type="button" :disabled="deleting || operations.isLocked(operationKey('connection', conn.name))" @click="remove">
          {{ deleting || operations.phase(operationKey('connection', conn.name)) === 'deleting' ? 'Deleting connection…' : 'Delete connection' }}
        </button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.chip { margin-right: 0.35rem; }
</style>
