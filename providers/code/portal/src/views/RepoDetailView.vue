<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { AlertTriangle, ArrowLeft, ExternalLink } from 'lucide-vue-next'
import { api } from '../api'
import type { Collaborator, Connection, DeployKey, ErrorResponse, Package, RepositoryDetail } from '../types'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import { createLatestRefreshController, createOperationLocks, operationKey, type LatestRefreshController, type ResourceDeletions } from '../refresh'

const props = defineProps<{ name: string; deletions: ResourceDeletions }>()
const emit = defineEmits<{ (e: 'back'): void }>()
const repositoryScope = 'repository'
const keyScope = `deploy-key:${props.name}`
const collaboratorScope = `collaborator:${props.name}`

const repo = ref<RepositoryDetail | null>(null)
const repoLoading = ref(true)
const repoLoaded = ref(false)
const repoError = ref<string | null>(null)

const connections = ref<Connection[]>([])
const connectionsLoading = ref(true)
const connectionsLoaded = ref(false)
const connectionsError = ref<string | null>(null)
const selectedConn = ref('')
const changingConn = ref(false)
const connError = ref<string | null>(null)

const keys = ref<DeployKey[]>([])
const keysLoading = ref(true)
const keysLoaded = ref(false)
const keysError = ref<string | null>(null)
const keyTitle = ref('')
const keyPublic = ref('')
const keyReadOnly = ref(true)
const keySubmitting = ref(false)
const keyError = ref<string | null>(null)
const keyDeleteError = ref<string | null>(null)

const collabs = ref<Collaborator[]>([])
const collabsLoading = ref(true)
const collabsLoaded = ref(false)
const collabsError = ref<string | null>(null)
const collabUser = ref('')
const collabPerm = ref('push')
const collabSubmitting = ref(false)
const collabError = ref<string | null>(null)
const collabDeleteError = ref<string | null>(null)

const packages = ref<Package[]>([])
const packagesLoading = ref(true)
const packagesLoaded = ref(false)
const packagesError = ref<string | null>(null)

const operations = createOperationLocks()
const keyColumns = [
  { key: 'title', label: 'Deploy key' },
  { key: 'access', label: 'Access' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '' },
]
const collabColumns = [
  { key: 'username', label: 'Collaborator' },
  { key: 'permission', label: 'Permission' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '' },
]
const packageColumns = [
  { key: 'name', label: 'Package' },
  { key: 'type', label: 'Type' },
  { key: 'visibility', label: 'Visibility' },
  { key: 'versionCount', label: 'Versions' },
  { key: 'status', label: 'Status' },
  { key: 'url', label: '' },
]
function controllerCaughtUp(resource: { generation?: number; observedGeneration?: number }): boolean {
  return resource.generation === undefined ||
    (resource.observedGeneration !== undefined && resource.observedGeneration >= resource.generation)
}
function isDeleting(scope: string, resource: { name: string; uid?: string; deletionTimestamp?: string }): boolean {
  return !!resource.deletionTimestamp || props.deletions.has(scope, resource.name, resource.uid)
}
function isPackageDeleting(resource: Package): boolean {
  return !!resource.deletionTimestamp
}
const repositoryDeleting = computed(() => !!repo.value && isDeleting(repositoryScope, repo.value))
const keyRows = computed<Array<Record<string, unknown>>>(() => keys.value
  .map(key => {
    const deleting = isDeleting(keyScope, key)
    return { ...key, deleting, title: key.title || key.name, access: key.readOnly ? 'read-only' : 'read-write', status: deleting ? 'Deleting' : key.ready ? 'ready' : 'pending', actions: '' }
  }))
const collabRows = computed<Array<Record<string, unknown>>>(() => collabs.value
  .map(collab => ({
    ...collab,
    deleting: isDeleting(collaboratorScope, collab),
    status: isDeleting(collaboratorScope, collab) ? 'Deleting' : !controllerCaughtUp(collab) || collab.invitationPending ? 'pending' : collab.ready ? 'active' : 'unknown',
    actions: '',
  })))
const packageRows = computed<Array<Record<string, unknown>>>(() => packages.value.map(item => ({
  ...item,
  deleting: isPackageDeleting(item),
  rowKey: item.uid || `${item.type}/${item.name}`,
  status: isPackageDeleting(item) ? 'Deleting' : !controllerCaughtUp(item) ? 'pending' : item.ready ? 'ready' : item.message ? 'failed' : 'pending',
  url: item.htmlURL || '',
})))

const connectionChoices = computed(() => connections.value.filter(connection => !isDeleting('connection', connection)))
const currentConn = computed(() => connections.value.find(c => c.name === repo.value?.connectionRef))
const newConn = computed(() => connections.value.find(c => c.name === selectedConn.value))
const currentOwner = computed(() => repo.value?.owner || currentConn.value?.owner || '')
const newOwner = computed(() => repo.value?.owner || newConn.value?.owner || '')
const ownerWillChange = computed(() =>
  !!repo.value &&
  selectedConn.value !== repo.value.connectionRef &&
  !repo.value.owner &&
  !!newConn.value &&
  !!currentConn.value &&
  newConn.value.owner !== currentConn.value.owner,
)

let timer: number | undefined
let mounted = false
let repoRefresh!: LatestRefreshController
let connectionRefresh!: LatestRefreshController
let keyRefresh!: LatestRefreshController
let collabRefresh!: LatestRefreshController
let packageRefresh!: LatestRefreshController

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function loadRepository() { repoRefresh.request() }
function loadConnections() { connectionRefresh.request() }
function loadKeys() { keyRefresh.request() }
function loadCollaborators() { collabRefresh.request() }
function loadPackages() { packageRefresh.request() }
function loadAll() {
  loadRepository()
  loadConnections()
  loadKeys()
  loadCollaborators()
  loadPackages()
}

async function changeConnection() {
  const current = repo.value
  if (!current || repositoryDeleting.value || selectedConn.value === current.connectionRef) return
  const message = ownerWillChange.value
    ? `Its owner (${newOwner.value}) differs from the current (${currentOwner.value}).\n` +
      `The repository will be re-targeted to that account — a new repo may be created there, ` +
      `and the existing repo on ${currentOwner.value} is left untouched.`
    : 'Only the managing credential changes; the repository stays on the same account.'
  const ok = await confirmDialog({
    title: `Change connection to "${selectedConn.value}"?`,
    message,
    confirmLabel: 'Change',
    danger: ownerWillChange.value,
  })
  if (!ok || !mounted) return
  const lock = operationKey('repository', current.name)
  if (!operations.acquire(lock, 'saving')) return
  changingConn.value = true
  connError.value = null
  try {
    const updated = await api.updateRepositoryConnection(current.name, selectedConn.value)
    repo.value = { ...current, ...updated, conditions: current.conditions }
    loadRepository()
  } catch (e) {
    connError.value = errMessage(e)
  } finally {
    changingConn.value = false
    operations.release(lock)
  }
}

async function addKey() {
  keyError.value = null
  if (repositoryDeleting.value) return
  if (!keysLoaded.value) {
    keyError.value = 'Deploy keys are still loading. Retry the read before adding a key.'
    return
  }
  keySubmitting.value = true
  try {
    const created = await api.createDeployKey({
      repositoryRef: props.name,
      title: keyTitle.value || undefined,
      publicKey: keyPublic.value || undefined,
      readOnly: keyReadOnly.value,
    })
    keys.value = [...keys.value.filter(item => item.name !== created.name), created]
    keysLoaded.value = true
    keyTitle.value = keyPublic.value = ''
    loadKeys()
  } catch (e) {
    keyError.value = errMessage(e)
  } finally {
    keySubmitting.value = false
  }
}

async function removeKey(row: Record<string, unknown>) {
  const key = row as unknown as DeployKey
  if (repositoryDeleting.value || isDeleting(keyScope, key)) return
  const ok = await confirmDialog({ title: `Delete deploy key "${key.title || key.name}"?`, confirmLabel: 'Delete', danger: true })
  if (!ok || !mounted) return
  const lock = operationKey('deploy-key', key.name)
  if (!operations.acquire(lock, 'deleting')) return
  keyDeleteError.value = null
  try {
    await api.deleteDeployKey(key.name)
    props.deletions.acknowledge(keyScope, key.name, key.uid)
    loadKeys()
  } catch (e) {
    keyDeleteError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

async function addCollab() {
  collabError.value = null
  if (repositoryDeleting.value) return
  if (!collabsLoaded.value) {
    collabError.value = 'Collaborators are still loading. Retry the read before adding one.'
    return
  }
  if (!collabUser.value) {
    collabError.value = 'username is required'
    return
  }
  collabSubmitting.value = true
  try {
    const created = await api.createCollaborator({ repositoryRef: props.name, username: collabUser.value, permission: collabPerm.value })
    collabs.value = [...collabs.value.filter(item => item.name !== created.name), created]
    collabsLoaded.value = true
    collabUser.value = ''
    loadCollaborators()
  } catch (e) {
    collabError.value = errMessage(e)
  } finally {
    collabSubmitting.value = false
  }
}

async function removeCollab(row: Record<string, unknown>) {
  const collab = row as unknown as Collaborator
  if (repositoryDeleting.value || isDeleting(collaboratorScope, collab)) return
  const ok = await confirmDialog({ title: `Remove collaborator "${collab.username}"?`, confirmLabel: 'Remove', danger: true })
  if (!ok || !mounted) return
  const lock = operationKey('collaborator', collab.name)
  if (!operations.acquire(lock, 'deleting')) return
  collabDeleteError.value = null
  try {
    await api.deleteCollaborator(collab.name)
    props.deletions.acknowledge(collaboratorScope, collab.name, collab.uid)
    loadCollaborators()
  } catch (e) {
    collabDeleteError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

repoRefresh = createLatestRefreshController(async requestID => {
  repoLoading.value = true
  try {
    const next = await api.getRepository(props.name)
    if (!repoRefresh.isCurrent(requestID)) return
    repo.value = next
    if (next.deletionTimestamp) props.deletions.acknowledge(repositoryScope, next.name, next.uid)
    repoLoaded.value = true
    repoError.value = null
    if (selectedConn.value === '') selectedConn.value = next.connectionRef
  } catch (e) {
    if (!repoRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    if (err.reason === 'NotFound' && (repositoryDeleting.value || props.deletions.has(repositoryScope, props.name))) {
      repo.value = null
      emit('back')
      return
    }
    repoError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (repoRefresh.isCurrent(requestID)) repoLoading.value = false
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
    const available = next.filter(item => !isDeleting('connection', item))
    if (repo.value && (selectedConn.value === '' || !available.some(item => item.name === selectedConn.value))) {
      selectedConn.value = available.some(item => item.name === repo.value?.connectionRef) ? repo.value.connectionRef : available[0]?.name || ''
    }
  } catch (e) {
    if (!connectionRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    connectionsError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (connectionRefresh.isCurrent(requestID)) connectionsLoading.value = false
  }
})

keyRefresh = createLatestRefreshController(async requestID => {
  keysLoading.value = true
  try {
    const next = await api.listDeployKeys(props.name)
    if (!keyRefresh.isCurrent(requestID)) return
    keys.value = next
    next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge(keyScope, item.name, item.uid))
    props.deletions.reconcile(keyScope, next)
    keysLoaded.value = true
    keysError.value = null
  } catch (e) {
    if (!keyRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    keysError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (keyRefresh.isCurrent(requestID)) keysLoading.value = false
  }
})

collabRefresh = createLatestRefreshController(async requestID => {
  collabsLoading.value = true
  try {
    const next = await api.listCollaborators(props.name)
    if (!collabRefresh.isCurrent(requestID)) return
    collabs.value = next
    next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge(collaboratorScope, item.name, item.uid))
    props.deletions.reconcile(collaboratorScope, next)
    collabsLoaded.value = true
    collabsError.value = null
  } catch (e) {
    if (!collabRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    collabsError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (collabRefresh.isCurrent(requestID)) collabsLoading.value = false
  }
})

packageRefresh = createLatestRefreshController(async requestID => {
  packagesLoading.value = true
  try {
    const next = await api.listPackages(props.name)
    if (!packageRefresh.isCurrent(requestID)) return
    packages.value = next
    packagesLoaded.value = true
    packagesError.value = null
  } catch (e) {
    if (!packageRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    packagesError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (packageRefresh.isCurrent(requestID)) packagesLoading.value = false
  }
})

onMounted(() => {
  mounted = true
  loadAll()
  timer = window.setInterval(loadAll, 5000)
})
onUnmounted(() => {
  mounted = false
  window.clearInterval(timer)
  repoRefresh.stop()
  connectionRefresh.stop()
  keyRefresh.stop()
  collabRefresh.stop()
  packageRefresh.stop()
})
</script>

<template>
  <section class="page" :aria-busy="repoLoading">
    <button class="k-btn k-btn--ghost code-back-action" type="button" @click="emit('back')"><ArrowLeft :size="14" aria-hidden="true" /> Repositories</button>

    <header class="page-head">
      <div>
        <h2 class="page-title">{{ repo?.repo || name }}</h2>
        <p class="page-meta">
          <a v-if="repo?.htmlURL && !repositoryDeleting" :href="repo.htmlURL" target="_blank" rel="noopener">{{ repo.htmlURL }}</a>
          <span v-else-if="repo?.htmlURL" class="muted">{{ repo.htmlURL }}</span>
          <span v-else-if="repo" class="muted">not created yet</span>
          <span v-else-if="repoLoading" class="muted">Loading repository details…</span>
          <span v-else class="muted">Repository details unavailable</span>
        </p>
      </div>
      <StatusBadge v-if="repo" :status="repositoryDeleting ? 'Deleting' : repo.ready ? 'ready' : 'pending'" :tone="repositoryDeleting ? 'warning' : null" :title="repo.message" />
    </header>

    <div v-if="repoError && !repo" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ repoError }}</span><button class="k-btn k-btn--ghost" type="button" @click="loadRepository">Retry</button>
    </div>
    <div v-else-if="repoLoading && !repo" class="detail-loading" role="status" aria-live="polite" aria-label="Loading repository details" aria-busy="true">
      <div v-for="i in 5" :key="i" class="shimmer detail-loading-line" />
    </div>
    <div v-if="repoError && repo" class="error read-error" role="alert" aria-live="assertive">
      <span>Showing cached repository data. {{ repoError }}</span><button class="k-btn k-btn--ghost" type="button" @click="loadRepository">Retry</button>
    </div>
    <span v-else-if="repoLoading && repoLoaded" class="sr-only" role="status" aria-live="polite">Updating repository…</span>

    <template v-if="repo">
      <div class="panel k-card">
        <h3 class="panel-title">Overview</h3>
        <dl class="props">
          <dt>Connection</dt>
          <dd>
            <div class="conn-edit" :aria-busy="connectionsLoading">
              <select v-model="selectedConn" class="k-input" :disabled="repositoryDeleting || changingConn || !connectionsLoaded">
                <option v-for="c in connectionChoices" :key="c.name" :value="c.name">{{ c.name }} ({{ c.owner }})</option>
              </select>
              <button class="k-btn k-btn--primary" type="button" :disabled="repositoryDeleting || changingConn || !connectionsLoaded || selectedConn === repo.connectionRef" @click="changeConnection">{{ changingConn ? 'Changing…' : 'Change' }}</button>
            </div>
            <span v-if="connectionsLoading && !connectionsLoaded" class="muted" role="status" aria-live="polite">Loading connections…</span>
            <div v-if="connectionsError" class="error read-error" role="alert" aria-live="assertive">
              <span>{{ connectionsLoaded ? 'Showing cached connection choices. ' : '' }}{{ connectionsError }}</span><button class="k-btn k-btn--ghost" type="button" @click="loadConnections">Retry</button>
            </div>
            <span v-else-if="connectionsLoading && connectionsLoaded" class="sr-only" role="status" aria-live="polite">Updating connections…</span>
            <p v-if="ownerWillChange" class="conn-warn"><AlertTriangle :size="15" class="warn-ic" /> Owner <code>{{ newOwner }}</code> differs from current <code>{{ currentOwner }}</code> — this re-targets the repo to a different account and may create a new repo there.</p>
            <p v-else-if="selectedConn !== repo.connectionRef" class="muted">Same owner — only the managing credential changes.</p>
            <span v-if="connError" class="error" role="alert">{{ connError }}</span>
          </dd>
          <dt>Visibility</dt><dd>{{ repo.visibility }}</dd>
          <dt v-if="repo.cloneURL">Clone URL</dt><dd v-if="repo.cloneURL"><code>{{ repo.cloneURL }}</code></dd>
          <dt v-if="repo.sshURL">SSH URL</dt><dd v-if="repo.sshURL"><code>{{ repo.sshURL }}</code></dd>
        </dl>
      </div>

      <ConditionsPanel :conditions="repo.conditions" :generation="repo.generation" :observed-generation="repo.observedGeneration" empty-text="No conditions yet — the controller has not reconciled this repository." />

      <div class="grid-2">
        <div class="panel section-panel k-card">
          <div class="panel-head"><h3 class="panel-title">Deploy keys</h3><span v-if="keysLoaded" class="muted">{{ keyRows.length }}</span></div>
          <form class="form" @submit.prevent="addKey">
            <label class="field"><span class="field-label">Title</span><input v-model="keyTitle" class="k-input" :disabled="repositoryDeleting" placeholder="ci-deploy" autocomplete="off" /></label>
            <label class="field"><span class="field-label">Public key (leave empty to generate)</span><textarea v-model="keyPublic" class="k-input" :disabled="repositoryDeleting" rows="2" placeholder="ssh-ed25519 AAAA…" /></label>
            <label class="field field-check"><input v-model="keyReadOnly" type="checkbox" :disabled="repositoryDeleting" /> read-only</label>
            <div class="code-form-actions"><button class="k-btn k-btn--primary" type="submit" :disabled="repositoryDeleting || keySubmitting || !keysLoaded">{{ keySubmitting ? 'Adding…' : 'Add deploy key' }}</button><span v-if="keyError" class="error" role="alert">{{ keyError }}</span></div>
            <p class="muted">A generated key's private half is written to a Secret in your workspace.</p>
          </form>
          <p v-if="keyDeleteError" class="error mutation-error" role="alert" aria-live="assertive">{{ keyDeleteError }}</p>
          <ResourceTable :columns="keyColumns" :rows="keyRows" row-key="name" :loaded="keysLoaded" :loading="keysLoading" :error="keysError" :stale="keysLoaded && !!keysError" retryable empty-text="No deploy keys." :interactive="false" @retry="loadKeys">
            <template #title="{ row }"><strong>{{ row.title }}</strong><div v-if="row.generated && row.secretName" class="muted">secret: <code>{{ row.secretName }}</code></div></template>
            <template #access="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
            <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
            <template #actions="{ row }"><div class="code-row-actions"><ResourceTableDeleteButton :label="`Delete deploy key ${String(row.title)}`" :busy-label="`Deleting deploy key ${String(row.title)}…`" :busy="Boolean(row.deleting) || operations.phase(operationKey('deploy-key', String(row.name))) === 'deleting'" :disabled="repositoryDeleting || Boolean(row.deleting) || operations.isLocked(operationKey('deploy-key', String(row.name)))" @click="removeKey(row)" /></div></template>
          </ResourceTable>
        </div>

        <div class="panel section-panel k-card">
          <div class="panel-head"><h3 class="panel-title">Collaborators</h3><span v-if="collabsLoaded" class="muted">{{ collabRows.length }}</span></div>
          <form class="form" @submit.prevent="addCollab">
            <label class="field"><span class="field-label">Username</span><input v-model="collabUser" class="k-input" :disabled="repositoryDeleting" placeholder="octocat" autocomplete="off" /></label>
            <label class="field"><span class="field-label">Permission</span><select v-model="collabPerm" class="k-input" :disabled="repositoryDeleting"><option value="pull">pull</option><option value="push">push</option><option value="admin">admin</option></select></label>
            <div class="code-form-actions"><button class="k-btn k-btn--primary" type="submit" :disabled="repositoryDeleting || collabSubmitting || !collabsLoaded">{{ collabSubmitting ? 'Adding…' : 'Add collaborator' }}</button><span v-if="collabError" class="error" role="alert">{{ collabError }}</span></div>
          </form>
          <p v-if="collabDeleteError" class="error mutation-error" role="alert" aria-live="assertive">{{ collabDeleteError }}</p>
          <ResourceTable :columns="collabColumns" :rows="collabRows" row-key="name" :loaded="collabsLoaded" :loading="collabsLoading" :error="collabsError" :stale="collabsLoaded && !!collabsError" retryable empty-text="No collaborators." :interactive="false" @retry="loadCollaborators">
            <template #username="{ value }"><strong>{{ value }}</strong></template>
            <template #permission="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
            <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
            <template #actions="{ row }"><div class="code-row-actions"><ResourceTableDeleteButton :label="`Remove collaborator ${String(row.username)}`" :busy-label="`Removing collaborator ${String(row.username)}…`" :busy="Boolean(row.deleting) || operations.phase(operationKey('collaborator', String(row.name))) === 'deleting'" :disabled="repositoryDeleting || Boolean(row.deleting) || operations.isLocked(operationKey('collaborator', String(row.name)))" @click="removeCollab(row)" /></div></template>
          </ResourceTable>
        </div>
      </div>

      <div class="panel section-panel k-card">
        <div class="panel-head"><h3 class="panel-title">Packages</h3><span v-if="packagesLoaded" class="muted">{{ packageRows.length }}</span></div>
        <ResourceTable :columns="packageColumns" :rows="packageRows" row-key="rowKey" :loaded="packagesLoaded" :loading="packagesLoading" :error="packagesError" :stale="packagesLoaded && !!packagesError" retryable empty-text="No packages published to this repository yet." :interactive="false" @retry="loadPackages">
          <template #name="{ row }"><strong><a v-if="row.htmlURL && !repositoryDeleting && !row.deleting" :href="String(row.htmlURL)" target="_blank" rel="noopener">{{ row.name }}</a><template v-else>{{ row.name }}</template></strong></template>
          <template #type="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
          <template #visibility="{ value }"><span class="muted">{{ value || '—' }}</span></template>
          <template #versionCount="{ value }"><span class="muted">{{ value || 0 }}</span></template>
          <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
          <template #url="{ row }"><a v-if="row.htmlURL && !repositoryDeleting && !row.deleting" :href="String(row.htmlURL)" target="_blank" rel="noopener">View <ExternalLink :size="12" aria-hidden="true" /></a></template>
        </ResourceTable>
        <p class="muted">Packages appear automatically when artifacts are pushed (e.g. <code>docker push</code>, <code>npm publish</code>).</p>
      </div>
    </template>
  </section>
</template>
