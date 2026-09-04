<script setup lang="ts">
import { computed, inject, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import { api, normalizeResourceName } from '../api'
import { contextGenerationKey } from '../context'
import type { Connection, ErrorResponse, Repository } from '../types'
import type { ResourceDeletions } from '../refresh'
import { createFullListReadCoordinator } from '../hybridPagination'
import { createOperationLocks, operationKey } from '../refresh'

const props = defineProps<{ deletions: ResourceDeletions }>()
const emit = defineEmits<{
  (e: 'cancel'): void
  (e: 'created', name: string): void
}>()

const repositories = ref<Repository[]>([])
const connections = ref<Connection[]>([])
const repositoryLoaded = ref(false)
const connectionsLoaded = ref(false)
const repositoryLoading = ref(false)
const connectionsLoading = ref(false)
const repositoryError = ref<string | null>(null)
const connectionsError = ref<string | null>(null)

const name = ref('')
const repo = ref('')
const connectionRef = ref('')
const visibility = ref('private')
const description = ref('')
const autoInit = ref(true)
const submitting = ref(false)
const formError = ref<string | null>(null)
type RepositoryField = 'connection' | 'name'
const fieldErrors = ref<Partial<Record<RepositoryField, string>>>({})
const connectionInput = ref<HTMLSelectElement | null>(null)
const nameInput = ref<HTMLInputElement | null>(null)
const contextGeneration = inject(contextGenerationKey, ref(0))

const repositoryFullRead = createFullListReadCoordinator(() => api.listRepositories())
const operations = createOperationLocks()
let mounted = false
let repositoryReadGeneration = 0
let connectionReadGeneration = 0
let mutationGeneration = 0

const connectionChoices = computed(() => connections.value.filter(connection => (
  !connection.deletionTimestamp && !props.deletions.has('connection', connection.name, connection.uid)
)))

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function isCurrentRepositoryRead(generation: number, expectedContext: number): boolean {
  return mounted && generation === repositoryReadGeneration && contextGeneration.value === expectedContext
}

function isCurrentConnectionRead(generation: number, expectedContext: number): boolean {
  return mounted && generation === connectionReadGeneration && contextGeneration.value === expectedContext
}

function isCurrentMutation(generation: number, expectedContext: number): boolean {
  return mounted && generation === mutationGeneration && contextGeneration.value === expectedContext
}

function clearFieldError(field: RepositoryField): void {
  if (!fieldErrors.value[field]) return
  const next = { ...fieldErrors.value }
  delete next[field]
  fieldErrors.value = next
}

function focusField(field: RepositoryField): void {
  void nextTick(() => (field === 'connection' ? connectionInput.value : nameInput.value)?.focus?.())
}

function setFieldError(field: RepositoryField, message: string): void {
  fieldErrors.value = { ...fieldErrors.value, [field]: message }
  focusField(field)
}

function validateFields(): boolean {
  const errors: Partial<Record<RepositoryField, string>> = {}
  if (!connectionRef.value) errors.connection = 'Select an active connection.'
  if (!name.value.trim()) errors.name = 'Enter an object name.'
  fieldErrors.value = errors
  const first = (['connection', 'name'] as const).find(field => errors[field])
  if (first) focusField(first)
  return !first
}

function cancel(): void {
  if (submitting.value) return
  emit('cancel')
}

function reconcileConnections(next: Connection[]): void {
  next
    .filter(item => item.deletionTimestamp)
    .forEach(item => props.deletions.acknowledge('connection', item.name, item.uid))
  props.deletions.reconcile('connection', next)
  const available = next.filter(item => !item.deletionTimestamp && !props.deletions.has('connection', item.name, item.uid))
  if (!available.some(item => item.name === connectionRef.value)) connectionRef.value = available[0]?.name || ''
}

function reconcileRepositories(next: Repository[]): void {
  next
    .filter(item => item.deletionTimestamp)
    .forEach(item => props.deletions.acknowledge('repository', item.name, item.uid))
  props.deletions.reconcile('repository', next)
}

async function loadConnections(): Promise<void> {
  const generation = ++connectionReadGeneration
  const expectedContext = contextGeneration.value
  connectionsLoading.value = true
  try {
    const next = await api.listConnections()
    if (!isCurrentConnectionRead(generation, expectedContext)) return
    connections.value = next
    reconcileConnections(next)
    connectionsLoaded.value = true
    connectionsError.value = null
  } catch (e) {
    if (!isCurrentConnectionRead(generation, expectedContext)) return
    const err = e as ErrorResponse
    connectionsError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (isCurrentConnectionRead(generation, expectedContext)) connectionsLoading.value = false
  }
}

async function loadRepositories(force = false): Promise<void> {
  const generation = ++repositoryReadGeneration
  const expectedContext = contextGeneration.value
  repositoryLoading.value = true
  try {
    const next = await repositoryFullRead.read(force)
    if (!isCurrentRepositoryRead(generation, expectedContext)) return
    repositories.value = next
    reconcileRepositories(next)
    repositoryLoaded.value = true
    repositoryError.value = null
  } catch (e) {
    if (!isCurrentRepositoryRead(generation, expectedContext)) return
    const err = e as ErrorResponse
    repositoryError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (isCurrentRepositoryRead(generation, expectedContext)) repositoryLoading.value = false
  }
}

async function submit(): Promise<void> {
  if (!mounted || submitting.value) return
  const generation = ++mutationGeneration
  const expectedContext = contextGeneration.value
  formError.value = null
  if (!repositoryLoaded.value || !connectionsLoaded.value) {
    formError.value = 'Repository data is still loading. Retry failed reads before creating a repository.'
    return
  }
  if (!validateFields()) return
  if (!connectionChoices.value.some(connection => connection.name === connectionRef.value)) {
    setFieldError('connection', 'Select an active connection before creating a repository.')
    return
  }
  const payload = {
    name: name.value,
    connectionRef: connectionRef.value,
    repo: repo.value || undefined,
    visibility: visibility.value,
    description: description.value || undefined,
    autoInit: autoInit.value,
  }
  const desiredName = normalizeResourceName(payload.name)
  const lock = operationKey('repository', desiredName)
  if (!operations.acquire(lock, 'creating')) {
    setFieldError('name', `Repository "${desiredName}" already has an operation in progress.`)
    return
  }
  submitting.value = true
  try {
    // The initial read populates the page, but creation still performs a fresh
    // complete walk so duplicate and terminating-name checks use current
    // workspace authority rather than a possibly stale snapshot.
    const [allRepositories, currentConnections] = await Promise.all([
      repositoryFullRead.read(true),
      api.listConnections(),
    ])
    if (!isCurrentMutation(generation, expectedContext)) return
    repositories.value = allRepositories
    reconcileRepositories(allRepositories)
    connections.value = currentConnections
    reconcileConnections(currentConnections)
    connectionsLoaded.value = true
    connectionsError.value = null
    const existing = allRepositories.find(repository => normalizeResourceName(repository.name) === desiredName)
    if (existing && (existing.deletionTimestamp || props.deletions.has('repository', existing.name, existing.uid))) {
      setFieldError('name', `Repository "${existing.name}" is still deleting. Wait for it to disappear before recreating it.`)
      return
    }
    if (existing) {
      setFieldError('name', `Repository "${existing.name}" already exists.`)
      return
    }
    if (props.deletions.has('repository', desiredName)) {
      setFieldError('name', `Repository "${desiredName}" is still deleting. Wait for it to disappear before recreating it.`)
      return
    }
    const currentConnection = currentConnections.find(connection => connection.name === payload.connectionRef)
    if (!currentConnection || currentConnection.deletionTimestamp || props.deletions.has('connection', currentConnection.name, currentConnection.uid)) {
      setFieldError('connection', `Connection "${payload.connectionRef}" is no longer active. Select another connection and retry.`)
      return
    }
    if (!isCurrentMutation(generation, expectedContext)) return
    const created = await api.createRepository(payload)
    if (!isCurrentMutation(generation, expectedContext)) return
    emit('created', created.name)
  } catch (e) {
    if (isCurrentMutation(generation, expectedContext)) formError.value = errMessage(e)
  } finally {
    operations.release(lock)
    if (isCurrentMutation(generation, expectedContext)) submitting.value = false
  }
}

onMounted(() => {
  mounted = true
  void loadRepositories()
  void loadConnections()
})
onUnmounted(() => {
  mounted = false
  repositoryReadGeneration += 1
  connectionReadGeneration += 1
  mutationGeneration += 1
})
</script>

<template>
  <section class="page k-create-page">
    <button class="k-btn k-btn--ghost k-back-action" type="button" :disabled="submitting" @click="cancel">
      <ArrowLeft :size="14" aria-hidden="true" /> Repositories
    </button>
    <header class="k-create-header">
      <h2 class="k-create-title">Create repository</h2>
      <p class="k-create-description">Create a repository through one of your ready GitHub connections. Deploy keys and collaborators stay managed on the repository detail page.</p>
    </header>

    <div v-if="(repositoryLoading && !repositoryLoaded) || (connectionsLoading && !connectionsLoaded)" class="panel k-card" role="status" aria-live="polite">
      Loading repository creation data…
    </div>
    <div v-if="repositoryError" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ repositoryError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="loadRepositories(true)">Retry repositories</button>
    </div>
    <div v-if="connectionsError" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ connectionsLoaded ? 'Showing cached connection choices. ' : '' }}{{ connectionsError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="loadConnections">Retry connections</button>
    </div>
    <p v-if="connectionsLoaded && !connectionChoices.length" class="empty">Add a ready connection first, then create repositories under it.</p>

    <form class="k-create-surface" :aria-busy="submitting" novalidate @submit.prevent="submit">
      <div class="k-create-body">
        <label class="field" for="code-repository-connection">
          <span class="field-label">Connection</span>
          <select id="code-repository-connection" ref="connectionInput" v-model="connectionRef" class="k-input" :disabled="connectionsLoading && !connectionsLoaded" required aria-required="true" :aria-invalid="fieldErrors.connection ? 'true' : undefined" :aria-describedby="fieldErrors.connection ? 'code-repository-connection-error' : undefined" @change="clearFieldError('connection')">
            <option v-for="connection in connectionChoices" :key="connection.name" :value="connection.name">{{ connection.name }} ({{ connection.owner }})</option>
          </select>
          <span v-if="fieldErrors.connection" id="code-repository-connection-error" class="error" role="alert">{{ fieldErrors.connection }}</span>
        </label>
        <label class="field" for="code-repository-name"><span class="field-label">Object name</span><input id="code-repository-name" ref="nameInput" v-model="name" class="k-input" placeholder="my-service" autocomplete="off" required aria-required="true" :aria-invalid="fieldErrors.name ? 'true' : undefined" :aria-describedby="fieldErrors.name ? 'code-repository-name-error' : undefined" @input="clearFieldError('name')" /><span v-if="fieldErrors.name" id="code-repository-name-error" class="error" role="alert">{{ fieldErrors.name }}</span></label>
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
        <span v-if="formError" class="error" role="alert">{{ formError }}</span>
        <span v-if="submitting" class="sr-only" role="status" aria-live="polite">Creating repository…</span>
      </div>
      <div class="k-create-actions">
        <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="cancel">Cancel</button>
        <button class="k-btn k-btn--primary" type="submit" :disabled="submitting">{{ submitting ? 'Creating…' : 'Create repository' }}</button>
      </div>
    </form>
  </section>
</template>
