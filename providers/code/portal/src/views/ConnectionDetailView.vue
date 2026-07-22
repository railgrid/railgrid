<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import type { ConnectionDetail, ErrorResponse } from '../types'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import { confirmDialog } from '../portalkit/confirm'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const conn = ref<ConnectionDetail | null>(null)
const error = ref<string | null>(null)
const loading = ref(false)

let timer: number | undefined

// The Validated condition is the one that gates "pending" vs "validated".
const validated = computed(() => conn.value?.conditions.find(c => c.type === 'Validated'))

// reconciled is false when the controller has not yet observed the current spec
// generation — i.e. the connection is genuinely still being processed, as
// opposed to having failed validation.
const reconciled = computed(() =>
  !!conn.value &&
  conn.value.observedGeneration !== undefined &&
  conn.value.generation !== undefined &&
  conn.value.observedGeneration >= conn.value.generation,
)

// hint explains the current pending state in plain language so the user knows
// what to fix, keyed off the reason the controller recorded.
const hint = computed(() => {
  const c = conn.value
  if (!c) return ''
  if (c.validated) return ''
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

async function load() {
  loading.value = true
  error.value = null
  try {
    conn.value = await api.getConnection(props.name)
  } catch (e) {
    const err = e as ErrorResponse
    if (err.reason !== 'TenantMissing') error.value = `${err.reason}: ${err.message}`
  } finally {
    loading.value = false
  }
}

async function remove() {
  if (!conn.value) return
  const ok = await confirmDialog({
    title: `Delete connection "${conn.value.name}"?`,
    message: 'Repositories using it will stop reconciling.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  try {
    await api.deleteConnection(conn.value.name)
    emit('back')
  } catch (e) {
    error.value = (e as ErrorResponse).message
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
    <button class="link back" @click="emit('back')">← Connections</button>

    <header class="page-head">
      <div>
        <h2 class="page-title">{{ conn?.name || name }}</h2>
        <p class="page-meta">
          <span v-if="conn?.login">authenticated as <code>{{ conn.login }}</code></span>
          <span v-else class="muted">not validated yet</span>
        </p>
      </div>
      <span v-if="conn" :class="['badge', conn.validated ? 'ok' : 'warn']" :title="conn.message">
        {{ conn.validated ? 'validated' : 'pending' }}
      </span>
    </header>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="loading && !conn" class="muted">Loading…</p>

    <template v-else-if="conn">
      <!-- Why it's pending -->
      <div v-if="!conn.validated && hint" class="panel">
        <h3 class="panel-title">Status</h3>
        <p class="muted">{{ hint }}</p>
      </div>

      <!-- Overview -->
      <div class="panel">
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
            <span class="muted" v-if="conn.secretNamespace"> · ns <code>{{ conn.secretNamespace }}</code></span>
            <span class="muted" v-if="conn.secretKey"> · key <code>{{ conn.secretKey }}</code></span>
          </dd>
          <dt v-if="conn.baseURL">Base URL</dt><dd v-if="conn.baseURL"><code>{{ conn.baseURL }}</code></dd>
          <dt v-if="conn.observedGeneration !== undefined">Reconciled</dt>
          <dd v-if="conn.observedGeneration !== undefined">
            <span v-if="reconciled" class="muted">up to date (generation {{ conn.generation }})</span>
            <span v-else class="warn">controller has not caught up (spec {{ conn.generation }}, observed {{ conn.observedGeneration }})</span>
          </dd>
        </dl>
      </div>

      <!-- Conditions -->
      <ConditionsPanel
        :conditions="conn.conditions"
        :generation="conn.generation"
        :observed-generation="conn.observedGeneration"
        empty-text="No conditions yet — the controller has not reconciled this connection."
      />

      <div class="actions">
        <button class="danger" @click="remove">Delete connection</button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.chip { margin-right: 0.35rem; }
</style>
