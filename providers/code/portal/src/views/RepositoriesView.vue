<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ExternalLink } from 'lucide-vue-next'
import { api, normalizeResourceName } from '../api'
import type { Connection, ErrorResponse, Repository } from '../types'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import { createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController, type ResourceDeletions } from '../refresh'

const props = defineProps<{ deletions: ResourceDeletions }>()
const emit = defineEmits<{ (e: 'open', name: string): void }>()
const deletionScope = 'repository'

const repos = ref<Repository[]>([])
const connections = ref<Connection[]>([])
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const loading = ref(false)
const loaded = ref(false)
const connectionsError = ref<string | null>(null)
const connectionsLoading = ref(false)
const connectionsLoaded = ref(false)
const operations = createOperationLocks()
const columns = [
  { key: 'name', label: 'Name' },
  { key: 'connectionRef', label: 'Connection' },
  { key: 'visibility', label: 'Visibility' },
  { key: 'url', label: 'URL' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '' },
]
const rows = computed<Array<Record<string, unknown>>>(() => repos.value
  .map(repository => {
    const deleting = isDeleting(repository)
    return { ...repository, deleting, url: repository.htmlURL || '', status: deleting ? 'Deleting' : repository.ready ? 'ready' : 'pending', actions: '' }
  }))

function isDeleting(repository: Pick<Repository, 'name' | 'uid' | 'deletionTimestamp'>): boolean {
  return !!repository.deletionTimestamp || props.deletions.has(deletionScope, repository.name, repository.uid)
}
const connectionChoices = computed(() => connections.value.filter(connection => (
  !connection.deletionTimestamp && !props.deletions.has('connection', connection.name, connection.uid)
)))

const showForm = ref(false)
const name = ref('')
const repo = ref('')
const connectionRef = ref('')
const visibility = ref('private')
const description = ref('')
const autoInit = ref(true)
const submitting = ref(false)
const formError = ref<string | null>(null)

let timer: number | undefined
let mounted = false
let repoRefresh!: LatestRefreshController
let connectionRefresh!: LatestRefreshController

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function loadRepositories() {
  repoRefresh.request()
}

function loadConnections() {
  connectionRefresh.request()
}

function load() {
  loadRepositories()
  loadConnections()
}

function openRepository(row: Record<string, unknown>) {
  const resourceName = String(row.name)
  if (!row.deleting && !operations.isLocked(operationKey('repository', resourceName))) emit('open', resourceName)
}

async function submit() {
  formError.value = null
  if (!loaded.value || !connectionsLoaded.value) {
    formError.value = 'Repository data is still loading. Retry failed reads before creating a repository.'
    return
  }
  if (!name.value || !connectionRef.value) {
    formError.value = 'name and connection are required'
    return
  }
  if (!connectionChoices.value.some(connection => connection.name === connectionRef.value)) {
    formError.value = 'Select an active connection before creating a repository.'
    return
  }
  const existing = repos.value.find(repository => repository.name === normalizeResourceName(name.value))
  if (existing && isDeleting(existing)) {
    formError.value = `Repository "${existing.name}" is still deleting. Wait for it to disappear before recreating it.`
    return
  }
  submitting.value = true
  try {
    const created = await api.createRepository({
      name: name.value,
      connectionRef: connectionRef.value,
      repo: repo.value || undefined,
      visibility: visibility.value,
      description: description.value || undefined,
      autoInit: autoInit.value,
    })
    repos.value = [...repos.value.filter(item => item.name !== created.name), created]
    loaded.value = true
    name.value = repo.value = description.value = ''
    showForm.value = false
    loadRepositories()
  } catch (e) {
    formError.value = errMessage(e)
  } finally {
    submitting.value = false
  }
}

async function remove(row: Record<string, unknown>) {
  const repository = row as unknown as Repository
  if (isDeleting(repository)) return
  const ok = await confirmDialog({
    title: `Delete repository "${repository.repo}"?`,
    message: 'This removes the repository on the git host. This cannot be undone.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok || !mounted) return
  const lock = operationKey('repository', repository.name)
  if (!operations.acquire(lock, 'deleting')) return
  mutationError.value = null
  try {
    await api.deleteRepository(repository.name)
    props.deletions.acknowledge(deletionScope, repository.name, repository.uid)
    loadRepositories()
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

repoRefresh = createLatestRefreshController(async requestID => {
  loading.value = true
  try {
    const next = await api.listRepositories()
    if (!repoRefresh.isCurrent(requestID)) return
    repos.value = next
    next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge(deletionScope, item.name, item.uid))
    props.deletions.reconcile(deletionScope, next)
    loaded.value = true
    error.value = null
  } catch (e) {
    if (!repoRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (repoRefresh.isCurrent(requestID)) loading.value = false
  }
})

connectionRefresh = createLatestRefreshController(async requestID => {
  connectionsLoading.value = true
  try {
    const next = await api.listConnections()
    if (!connectionRefresh.isCurrent(requestID)) return
    connections.value = next
    next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge('connection', item.name, item.uid))
    props.deletions.reconcile('connection', next)
    connectionsLoaded.value = true
    connectionsError.value = null
    const available = next.filter(item => !item.deletionTimestamp && !props.deletions.has('connection', item.name, item.uid))
    if (!available.some(item => item.name === connectionRef.value)) connectionRef.value = available[0]?.name || ''
  } catch (e) {
    if (!connectionRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    connectionsError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (connectionRefresh.isCurrent(requestID)) connectionsLoading.value = false
  }
})

onMounted(() => {
  mounted = true
  load()
  timer = window.setInterval(load, 5000)
})
onUnmounted(() => {
  mounted = false
  window.clearInterval(timer)
  repoRefresh.stop()
  connectionRefresh.stop()
})
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Repositories</h2>
        <p class="page-meta">Repositories the provider manages on the git host. Click one to manage deploy keys and collaborators.</p>
      </div>
      <button class="k-btn k-btn--primary" :disabled="!loaded || !connectionsLoaded || !connectionChoices.length" @click="showForm = !showForm">
        {{ showForm ? 'Cancel' : 'New repository' }}
      </button>
    </header>

    <span v-if="connectionsLoading && !connectionsLoaded" class="sr-only" role="status" aria-live="polite">Loading connections…</span>
    <div v-if="connectionsError" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ connectionsLoaded ? 'Showing cached connection choices. ' : '' }}{{ connectionsError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="loadConnections">Retry connections</button>
    </div>
    <span v-else-if="connectionsLoading && connectionsLoaded" class="sr-only" role="status" aria-live="polite">Updating connections…</span>
    <p v-if="connectionsLoaded && !connectionChoices.length" class="empty">Add a ready connection first, then create repositories under it.</p>

    <div v-if="showForm" class="panel k-card">
      <h3 class="panel-title">New repository</h3>
      <form class="form" @submit.prevent="submit">
        <label class="field">
          <span class="field-label">Connection</span>
          <select v-model="connectionRef" class="k-input" :disabled="connectionsLoading && !connectionsLoaded">
            <option v-for="c in connectionChoices" :key="c.name" :value="c.name">{{ c.name }} ({{ c.owner }})</option>
          </select>
        </label>
        <label class="field"><span class="field-label">Object name</span><input v-model="name" class="k-input" placeholder="my-service" autocomplete="off" /></label>
        <label class="field"><span class="field-label">Repo name (defaults to object name)</span><input v-model="repo" class="k-input" placeholder="my-service" autocomplete="off" /></label>
        <label class="field">
          <span class="field-label">Visibility</span>
          <select v-model="visibility" class="k-input">
            <option value="private">private</option>
            <option value="public">public</option>
            <option value="internal">internal</option>
          </select>
        </label>
        <label class="field"><span class="field-label">Description</span><input v-model="description" class="k-input" autocomplete="off" /></label>
        <label class="field field-check"><input v-model="autoInit" type="checkbox" /> Initialize with a README</label>
        <div class="code-form-actions">
          <button class="k-btn k-btn--primary" type="submit" :disabled="submitting">{{ submitting ? 'Creating…' : 'Create' }}</button>
          <span v-if="formError" class="error" role="alert">{{ formError }}</span>
        </div>
      </form>
    </div>

    <p v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">{{ mutationError }}</p>
    <ResourceTable
      :columns="columns"
      :rows="rows"
      row-key="name"
      :loaded="loaded"
      :loading="loading"
      :error="error"
      :stale="loaded && !!error"
      retryable
      empty-text="No repositories yet."
      :row-aria-label="(row) => `Open repository ${String(row.repo || row.name)}`"
      @retry="loadRepositories"
      @row-click="openRepository"
    >
      <template #name="{ value, row }"><span v-if="row.deleting">{{ row.repo || value }}</span><button v-else class="k-btn k-btn--ghost code-inline-action" type="button" @click.stop="openRepository(row)">{{ row.repo || value }}</button></template>
      <template #connectionRef="{ value }">{{ value }}</template>
      <template #visibility="{ value }">{{ value }}</template>
      <template #url="{ row }"><a v-if="row.htmlURL && !row.deleting" :href="String(row.htmlURL)" target="_blank" rel="noopener" @click.stop>open <ExternalLink :size="12" aria-hidden="true" /></a><span v-else class="muted">—</span></template>
      <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
      <template #actions="{ row }">
        <div class="code-row-actions">
          <ResourceTableDeleteButton
            :label="`Delete repository ${String(row.repo || row.name)}`"
            :busy-label="`Deleting repository ${String(row.repo || row.name)}…`"
            :busy="Boolean(row.deleting) || operations.phase(operationKey('repository', String(row.name))) === 'deleting'"
            :disabled="Boolean(row.deleting) || operations.isLocked(operationKey('repository', String(row.name)))"
            @click="remove(row)"
          />
        </div>
      </template>
    </ResourceTable>
  </section>
</template>
