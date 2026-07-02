<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import ConditionsPanel from '../components/ConditionsPanel.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { confirmDialog } from '../components/confirm'
import type { Connection, ErrorResponse } from '../types'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const conn = ref<Connection | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
let timer: number | undefined

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

async function load() {
  loading.value = true
  error.value = null
  try {
    conn.value = await api.getConnection(props.name)
  } catch (e) {
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    loading.value = false
  }
}

async function remove() {
  if (!conn.value) return
  const ok = await confirmDialog({
    title: `Delete connection "${conn.value.name}"?`,
    message: 'Warehouses and tables that reference this connection will stop working.',
    confirmLabel: 'Delete',
  })
  if (!ok) return
  try {
    await api.deleteConnection(conn.value)
    emit('back')
  } catch (e) {
    error.value = errMessage(e)
  }
}

onMounted(() => {
  load()
  timer = window.setInterval(load, 5000)
})
onUnmounted(() => window.clearInterval(timer))
</script>

<template>
  <section class="page">
    <button class="link back" type="button" @click="emit('back')">← Connections</button>

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

    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="loading && !conn" class="muted">Loading…</p>

    <template v-else-if="conn">
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

      <ConditionsPanel
        :conditions="conn.conditions"
        :generation="conn.generation"
        :observed-generation="conn.observedGeneration"
        empty-text="No conditions yet. Connection validation has not reported status for this resource."
      />

      <div class="actions">
        <button class="danger" type="button" @click="remove">Delete connection</button>
      </div>
    </template>
  </section>
</template>
