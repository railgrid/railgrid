<script setup lang="ts">
import { REMOTE_REPOSITORY_PATTERN, REPOSITORY_NAME_PATTERN } from '../repositoryIdentity'
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import { api, normalizeResourceName } from '../api'
import { contextGenerationKey } from '../context'
import type { Connection, ErrorResponse, Repository } from '../types'
import type { ResourceDeletions } from '../refresh'
import { createFullListReadCoordinator } from '../hybridPagination'
import { createOperationLocks, operationKey } from '../refresh'
import FormSelect from '../portalkit/FormSelect.vue'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import { toast } from '../portalkit/toast'

const props = defineProps<{ deletions: ResourceDeletions; mode?: 'create' | 'import' }>()
const emit = defineEmits<{
  (e: 'cancel'): void
  (e: 'created', name: string): void
  (e: 'create-connection'): void
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
const mode = computed(() => props.mode || 'create')
const importing = computed(() => mode.value === 'import')
const connectionRef = ref('')
const visibility = ref('private')
const description = ref('')
const autoInit = ref(true)
const submitting = ref(false)
const formError = ref<string | null>(null)
type RepositoryField = 'connection' | 'name' | 'repo'
const fieldErrors = ref<Partial<Record<RepositoryField, string>>>({})
const connectionInput = ref<{ focus: () => void } | null>(null)
const repoInput = ref<HTMLInputElement | null>(null)
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
const guidanceValues = computed(() => {
  const objectName = name.value.trim() ? normalizeResourceName(name.value) : ''
  const owner = connectionChoices.value.find(connection => connection.name === connectionRef.value)?.owner
  const remoteName = repo.value.trim() || objectName
  const remoteRepository = importing.value
    ? repo.value.trim()
    : owner && remoteName ? `${owner}/${remoteName}` : ''
  return [
    { label: 'Name in Railgrid', value: objectName || 'Not entered yet', technical: true },
    { label: 'GitHub repository', value: remoteRepository || 'Not entered yet', technical: true },
    { label: 'Connection', value: connectionRef.value || 'Not selected yet', technical: true },
    ...(!importing.value ? [{ label: 'Visibility', value: visibility.value, technical: true },
    { label: 'Initial content', value: autoInit.value ? 'README' : 'Empty repository' }] : []),
  ]
})
const guidancePrerequisites = computed(() => importing.value ? [
  'A GitHub connection with access to the repository you want to import.',
] : [
  'A GitHub connection in this workspace.',
  'Permission to create repositories under the connection’s GitHub account or organization.',
])
const guidanceNextSteps = computed(() => importing.value ? [
  'Check the repository detail page for import status and any access errors.',
  'Removing the imported repository from Railgrid keeps it on GitHub.',
] : [
  'Railgrid asks GitHub to create the repository under the connection’s account or organization.',
  'Check the repository detail page for creation status and any errors.',
  'Once the repository is ready, you can add deploy keys and collaborators.',
])

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
  void nextTick(() => (field === 'connection' ? connectionInput.value : field === 'repo' ? repoInput.value : nameInput.value)?.focus?.())
}

function setFieldError(field: RepositoryField, message: string): void {
  fieldErrors.value = { ...fieldErrors.value, [field]: message }
  focusField(field)
}

function validateFields(): boolean {
  const errors: Partial<Record<RepositoryField, string>> = {}
  if (!connectionRef.value) errors.connection = 'Select a GitHub connection.'
  if (!name.value.trim()) errors.name = 'Enter a name for this repository in Railgrid.'
  if (importing.value && !REMOTE_REPOSITORY_PATTERN.test(repo.value.trim())) errors.repo = 'Enter the owner and repository, for example acme/my-service.'
  if (!importing.value && repo.value.includes('/')) errors.repo = 'Enter only the repository name. To add an existing repository, go back and choose Import repository.'
  if (!importing.value && !errors.repo && !REPOSITORY_NAME_PATTERN.test(repo.value.trim() || normalizeResourceName(name.value))) errors.repo = 'Use 1–100 letters, numbers, periods, underscores, or hyphens for the GitHub name.'
  fieldErrors.value = errors
  const first = (['connection', 'name', 'repo'] as const).find(field => errors[field])
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
    formError.value = 'Wait for repositories and connections to load. If loading failed, use the retry buttons above.'
    return
  }
  if (!validateFields()) return
  if (!connectionChoices.value.some(connection => connection.name === connectionRef.value)) {
    setFieldError('connection', 'Select an available GitHub connection to continue.')
    return
  }
  const payload = {
    name: name.value,
    connectionRef: connectionRef.value,
    repo: repo.value.trim() || undefined,
    mode: mode.value as 'create' | 'import',
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
      setFieldError('name', `Repository "${existing.name}" is still deleting. Wait for deletion to finish before using this name again.`)
      return
    }
    if (existing) {
      setFieldError('name', `The name "${existing.name}" is already used in this workspace. Choose a different name in Railgrid.`)
      return
    }
    if (props.deletions.has('repository', desiredName)) {
      setFieldError('name', `Repository "${desiredName}" is still deleting. Wait for deletion to finish before using this name again.`)
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
    toast('info', `Repository ${payload.mode === 'import' ? 'import' : 'creation'} requested for ${created.name}.`)
    emit('created', created.name)
  } catch (e) {
    if (isCurrentMutation(generation, expectedContext)) formError.value = errMessage(e)
  } finally {
    operations.release(lock)
    if (mounted && generation === mutationGeneration) submitting.value = false
  }
}

onMounted(() => {
  mounted = true
  void loadRepositories()
  void loadConnections()
})
watch(contextGeneration, () => {
  if (!mounted) return
  void loadRepositories(true)
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
      <h2 class="k-create-title">{{ importing ? 'Import repository' : 'Create repository' }}</h2>
      <p class="k-create-description">{{ importing ? 'Add an existing GitHub repository to this workspace. Importing leaves its code and settings unchanged.' : 'Create a GitHub repository under the account or organization linked to your connection.' }}</p>
    </header>

    <div v-if="(repositoryLoading && !repositoryLoaded) || (connectionsLoading && !connectionsLoaded)" class="panel k-card" role="status" aria-live="polite">
      Loading repositories and connections…
    </div>
    <div v-if="repositoryError" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ repositoryError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="loadRepositories(true)">Retry repositories</button>
    </div>
    <div v-if="connectionsError" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ connectionsLoaded ? 'Showing the last loaded connections. ' : '' }}{{ connectionsError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="loadConnections">Retry connections</button>
    </div>
    <div v-if="connectionsLoaded && !connectionChoices.length" class="panel k-card">
      <p class="empty">Add a GitHub connection before creating or importing a repository.</p>
      <button class="k-btn k-btn--ghost" type="button" @click="emit('create-connection')">Add connection</button>
    </div>

    <form class="k-create-surface k-create-surface--guided" :aria-busy="submitting" novalidate @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
        <div class="k-create-fields">
          <div class="field">
            <label id="code-repository-connection-label" class="field-label" for="code-repository-connection">Connection</label>
            <FormSelect
              id="code-repository-connection"
              ref="connectionInput"
              v-model="connectionRef"
              :options="connectionChoices.map(connection => ({ value: connection.name, label: `${connection.name} (${connection.owner})` }))"
              :disabled="connectionsLoading && !connectionsLoaded"
              required
              :invalid="!!fieldErrors.connection"
              labelledby="code-repository-connection-label"
              :describedby="fieldErrors.connection ? 'code-repository-connection-error' : undefined"
              @update:model-value="clearFieldError('connection')"
            />
            <span v-if="fieldErrors.connection" id="code-repository-connection-error" class="error" role="alert">{{ fieldErrors.connection }}</span>
          </div>
          <label class="field" for="code-repository-name"><span class="field-label">Name in Railgrid</span><input id="code-repository-name" ref="nameInput" v-model="name" class="k-input" placeholder="my-service" autocomplete="off" required aria-required="true" :aria-invalid="fieldErrors.name ? 'true' : undefined" :aria-describedby="fieldErrors.name ? 'code-repository-name-error code-repository-name-hint' : 'code-repository-name-hint'" @input="clearFieldError('name')" /><span v-if="fieldErrors.name" id="code-repository-name-error" class="error" role="alert">{{ fieldErrors.name }}</span><span id="code-repository-name-hint" class="k-create-description">A unique name for this repository in this workspace. It can differ from the GitHub name.</span></label>
          <label class="field" for="code-repository-remote"><span class="field-label">{{ importing ? 'GitHub repository' : 'GitHub repository name (optional)' }}</span><input id="code-repository-remote" ref="repoInput" v-model="repo" class="k-input" :placeholder="importing ? 'owner/repository' : 'my-service'" autocomplete="off" :required="importing" :aria-required="importing || undefined" :aria-invalid="fieldErrors.repo ? 'true' : undefined" :aria-describedby="fieldErrors.repo ? 'code-repository-remote-error' : 'code-repository-remote-hint'" @input="clearFieldError('repo')" /><span v-if="fieldErrors.repo" id="code-repository-remote-error" class="error" role="alert">{{ fieldErrors.repo }}</span><span v-else id="code-repository-remote-hint" class="k-create-description">{{ importing ? 'Enter owner/repository, for example acme/my-service. Use the repository’s owner, even if the connection belongs to another account.' : 'Leave blank to use the name in Railgrid. Enter only the repository name; the connection supplies the owner.' }}</span></label>
          <div v-if="!importing" class="field">
            <label id="code-repository-visibility-label" class="field-label" for="code-repository-visibility">Visibility</label>
            <FormSelect
              id="code-repository-visibility"
              v-model="visibility"
              :options="[{ value: 'private', label: 'Private' }, { value: 'public', label: 'Public' }, { value: 'internal', label: 'Internal' }]"
              labelledby="code-repository-visibility-label"
            />
          </div>
          <label v-if="!importing" class="field"><span class="field-label">Description (optional)</span><input v-model="description" class="k-input" autocomplete="off" /></label>
          <label v-if="!importing" class="field field-check k-checkbox-hit"><input v-model="autoInit" class="k-checkbox" type="checkbox" /> Add a README</label>
          <span v-if="formError" class="error" role="alert">{{ formError }}</span>
          <span v-if="submitting" class="sr-only" role="status" aria-live="polite">{{ importing ? 'Requesting repository import…' : 'Requesting repository creation…' }}</span>
        </div>
        <CreateGuidance
          title="Review repository details"
          :description="importing ? 'Confirm the GitHub repository and connection before importing.' : 'Confirm where the repository will be created and its initial settings.'"
          :values-heading="importing ? 'What Railgrid will register' : 'What Railgrid will create'"
          :prerequisites="guidancePrerequisites"
          :values="guidanceValues"
          :next-steps="guidanceNextSteps"
        />
      </div>
      <div class="k-create-actions">
        <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="cancel">Cancel</button>
        <button class="k-btn k-btn--primary" type="submit" :disabled="submitting || !connectionChoices.length">{{ submitting ? (importing ? 'Importing…' : 'Creating…') : (importing ? 'Import repository' : 'Create repository') }}</button>
      </div>
    </form>
  </section>
</template>
