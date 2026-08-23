<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api, normalizeResourceName } from '../api'
import type { Connection, ErrorResponse } from '../types'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import { createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController, type ResourceDeletions } from '../refresh'

const props = defineProps<{ deletions: ResourceDeletions }>()
const emit = defineEmits<{ (e: 'open', name: string): void }>()
const deletionScope = 'connection'

const connections = ref<Connection[]>([])
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const loading = ref(false)
const loaded = ref(false)
const operations = createOperationLocks()
const columns = [
  { key: 'name', label: 'Name' },
  { key: 'owner', label: 'Owner' },
  { key: 'login', label: 'Login' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '' },
]
const rows = computed<Array<Record<string, unknown>>>(() => connections.value
  .map(connection => {
    const deleting = isDeleting(connection)
    return { ...connection, deleting, status: deleting ? 'Deleting' : connection.validated ? 'ready' : 'pending', actions: '' }
  }))

function isDeleting(connection: Pick<Connection, 'name' | 'uid' | 'deletionTimestamp'>): boolean {
  return !!connection.deletionTimestamp || props.deletions.has(deletionScope, connection.name, connection.uid)
}

// connect form
const showForm = ref(false)
const name = ref('')
const owner = ref('')
const token = ref('')
const baseURL = ref('')
const connType = ref<'pat' | 'oauth'>('pat')
const submitting = ref(false)
const formError = ref<string | null>(null)

// GitHub OAuth ("Connect with GitHub") — enabled when the provider is configured.
const oauthEnabled = ref(false)
const oauthLoaded = ref(false)
const oauthStartURL = ref('')
const oauthBusy = ref(false)
let oauthState = ''
let oauthOrigin = ''
let oauthPopup: Window | null = null
let oauthPopupTimer: number | undefined
let mounted = false
let timer: number | undefined
let refresh!: LatestRefreshController

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function resetForm() {
  name.value = owner.value = token.value = baseURL.value = ''
  connType.value = 'pat'
}

function randomState(): string {
  const a = new Uint8Array(16)
  crypto.getRandomValues(a)
  return Array.from(a, b => b.toString(16).padStart(2, '0')).join('')
}

function clearOAuthWait(message?: string) {
  window.clearInterval(oauthPopupTimer)
  oauthPopupTimer = undefined
  oauthBusy.value = false
  oauthPopup = null
  oauthOrigin = ''
  oauthState = ''
  if (message) formError.value = message
}

function connectGitHub() {
  if (!oauthStartURL.value) return
  oauthBusy.value = true
  formError.value = null
  oauthState = randomState()
  let url: URL
  try {
    // The provider normally returns a same-origin relative path. Resolve it
    // before recording the trusted callback origin, and reject schemes that
    // could execute in the opener's page context.
    url = new URL(oauthStartURL.value, window.location.href)
    if (url.protocol !== 'https:' && url.protocol !== 'http:') throw new Error('unsupported OAuth URL scheme')
  } catch {
    clearOAuthWait('invalid GitHub OAuth start URL — contact a platform admin')
    return
  }
  oauthOrigin = url.origin
  url.searchParams.set('state', oauthState)
  oauthPopup = window.open(url.toString(), 'faros-github-oauth', 'width=720,height=820')
  if (!oauthPopup) {
    clearOAuthWait('popup blocked — allow popups and retry')
    return
  }
  oauthPopupTimer = window.setInterval(() => {
    if (oauthPopup?.closed) clearOAuthWait('GitHub sign-in window was closed — retry when ready')
  }, 500)
}

function onMessage(ev: MessageEvent) {
  if (!oauthBusy.value) return
  if (!oauthPopup || ev.source !== oauthPopup || ev.origin !== oauthOrigin) return
  const d = ev.data as { type?: string; state?: string; token?: string; login?: string; error?: string }
  if (!d || d.type !== 'faros-github-oauth') return
  if (d.state !== oauthState) {
    clearOAuthWait('oauth state mismatch — please retry')
    return
  }
  if (d.error || !d.token) {
    clearOAuthWait(d.error || 'no token returned from GitHub')
    return
  }
  clearOAuthWait()
  token.value = d.token
  owner.value = d.login || ''
  name.value = d.login ? 'github-' + d.login : 'github'
  connType.value = 'oauth'
  showForm.value = true
}

function load() {
  refresh.request()
}

function openConnection(row: Record<string, unknown>) {
  const resourceName = String(row.name)
  if (!row.deleting && !operations.isLocked(operationKey('connection', resourceName))) emit('open', resourceName)
}

async function submit() {
  formError.value = null
  if (!loaded.value) {
    formError.value = 'Connection list is still loading. Retry the read before creating a connection.'
    return
  }
  if (!name.value || !owner.value || !token.value) {
    formError.value = 'name, owner, and token are required'
    return
  }
  const existing = connections.value.find(connection => connection.name === normalizeResourceName(name.value))
  if (existing && isDeleting(existing)) {
    formError.value = `Connection "${existing.name}" is still deleting. Wait for it to disappear before reconnecting.`
    return
  }
  submitting.value = true
  try {
    const created = await api.connect({ name: name.value, owner: owner.value, token: token.value, baseURL: baseURL.value || undefined, type: connType.value })
    connections.value = [...connections.value.filter(item => item.name !== created.name), created]
    loaded.value = true
    resetForm()
    showForm.value = false
    load()
  } catch (e) {
    formError.value = errMessage(e)
  } finally {
    submitting.value = false
  }
}

async function remove(row: Record<string, unknown>) {
  const connection = row as unknown as Connection
  if (isDeleting(connection)) return
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
    load()
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

refresh = createLatestRefreshController(async requestID => {
  loading.value = true
  try {
    const next = await api.listConnections()
    if (!refresh.isCurrent(requestID)) return
    connections.value = next
    next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge(deletionScope, item.name, item.uid))
    props.deletions.reconcile(deletionScope, next)
    loaded.value = true
    error.value = null
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (refresh.isCurrent(requestID)) loading.value = false
  }
})

onMounted(async () => {
  mounted = true
  load()
  timer = window.setInterval(load, 5000)
  window.addEventListener('message', onMessage)
  const cfg = await api.oauthConfig()
  oauthEnabled.value = cfg.enabled
  oauthStartURL.value = cfg.startURL || ''
  oauthLoaded.value = true
})
onUnmounted(() => {
  mounted = false
  clearOAuthWait()
  window.clearInterval(timer)
  window.removeEventListener('message', onMessage)
  refresh.stop()
})
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Connections</h2>
        <p class="page-meta">A connection binds your workspace to a git account. Repositories are created under it.</p>
      </div>
      <div class="code-form-actions">
        <button v-if="oauthEnabled" class="k-btn k-btn--primary" :disabled="oauthBusy || !loaded" @click="connectGitHub">
          {{ oauthBusy ? 'Waiting for GitHub…' : 'Connect with GitHub' }}
        </button>
        <button class="k-btn" :class="oauthEnabled ? 'k-btn--ghost' : 'k-btn--primary'" :disabled="!loaded" @click="showForm = !showForm">
          {{ showForm ? 'Cancel' : 'Add token manually' }}
        </button>
      </div>
    </header>

    <p v-if="oauthLoaded && !oauthEnabled" class="muted">
      Tip: a platform admin can enable one-click “Connect with GitHub” by configuring the provider’s GitHub OAuth app.
    </p>

    <div v-if="showForm" class="panel k-card">
      <h3 class="panel-title">{{ connType === 'oauth' ? 'Confirm GitHub connection' : 'Connect with a token' }}</h3>
      <form class="form" @submit.prevent="submit">
        <p v-if="connType === 'oauth'" class="muted">
          Authorized via GitHub<span v-if="owner"> as <code>{{ owner }}</code></span>. Pick the org/account to create repositories under, then confirm.
        </p>
        <label class="field"><span class="field-label">Name</span><input v-model="name" class="k-input" placeholder="my-github" autocomplete="off" /></label>
        <label class="field"><span class="field-label">Owner (org or user)</span><input v-model="owner" class="k-input" placeholder="acme" autocomplete="off" /></label>
        <label v-if="connType === 'pat'" class="field"><span class="field-label">Personal access token</span><input v-model="token" class="k-input" type="password" placeholder="ghp_…" autocomplete="off" /></label>
        <label v-else class="field"><span class="field-label">Credential</span><input class="k-input" value="GitHub OAuth — authorized" disabled /></label>
        <label v-if="connType === 'pat'" class="field"><span class="field-label">Base URL (GHES, optional)</span><input v-model="baseURL" class="k-input" placeholder="https://github.example.com/api/v3" autocomplete="off" /></label>
        <div class="code-form-actions">
          <button class="k-btn k-btn--primary" type="submit" :disabled="submitting">{{ submitting ? 'Connecting…' : 'Create' }}</button>
          <button class="k-btn k-btn--ghost" type="button" @click="() => { showForm = false; resetForm() }">Cancel</button>
          <span v-if="formError" class="error" role="alert">{{ formError }}</span>
        </div>
        <p class="muted">The token is stored as a Secret in your workspace; the provider validates it and shows the login below.</p>
      </form>
    </div>

    <p v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">{{ mutationError }}</p>
    <ResourceTable
      :columns="columns"
      :rows="rows"
      searchable
      search-placeholder="Search connections…"
      :filters="[{ key: 'owner', label: 'Owner' }, { key: 'status', label: 'Status', allLabel: 'Any status' }]"
      paginated
      :page-size="10"
      row-key="name"
      :loaded="loaded"
      :loading="loading"
      :error="error"
      :stale="loaded && !!error"
      retryable
      empty-text="No connections yet."
      :row-aria-label="(row) => `Open connection ${String(row.name)}`"
      @retry="load"
      @row-click="openConnection"
    >
      <template #name="{ value, row }"><span v-if="row.deleting">{{ value }}</span><button v-else class="k-btn k-btn--ghost code-inline-action" type="button" @click.stop="openConnection(row)">{{ value }}</button></template>
      <template #owner="{ value }">{{ value }}</template>
      <template #login="{ value }">{{ value || '—' }}</template>
      <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
      <template #actions="{ row }">
        <div class="code-row-actions">
          <ResourceTableDeleteButton
            :label="`Delete connection ${String(row.name)}`"
            :busy-label="`Deleting connection ${String(row.name)}…`"
            :busy="Boolean(row.deleting) || operations.phase(operationKey('connection', String(row.name))) === 'deleting'"
            :disabled="Boolean(row.deleting) || operations.isLocked(operationKey('connection', String(row.name)))"
            @click="remove(row)"
          />
        </div>
      </template>
    </ResourceTable>
  </section>
</template>
