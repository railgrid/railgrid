<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import type { Collaborator, Connection, DeployKey, ErrorResponse, Package, RepositoryDetail } from '../types'
import ConditionsPanel from '../components/ConditionsPanel.vue'
import { confirmDialog } from '../components/confirm'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const repo = ref<RepositoryDetail | null>(null)
const keys = ref<DeployKey[]>([])
const collabs = ref<Collaborator[]>([])
const error = ref<string | null>(null)

// connection switching
const connections = ref<Connection[]>([])
const selectedConn = ref('')
const changingConn = ref(false)
const connError = ref<string | null>(null)

// packages (read-only; crawled into Package CRs, read via GraphQL, best-effort)
const packages = ref<Package[]>([])
const packagesError = ref<string | null>(null)

const currentConn = computed(() => connections.value.find(c => c.name === repo.value?.connectionRef))
const newConn = computed(() => connections.value.find(c => c.name === selectedConn.value))
// Effective owner = the repo's per-repo override, else the connection's owner.
const currentOwner = computed(() => repo.value?.owner || currentConn.value?.owner || '')
const newOwner = computed(() => repo.value?.owner || newConn.value?.owner || '')
// Swapping only re-targets the host account when there is no spec.owner override
// and the two connections' owners differ.
const ownerWillChange = computed(() =>
  !!repo.value &&
  selectedConn.value !== repo.value.connectionRef &&
  !repo.value.owner &&
  !!newConn.value &&
  !!currentConn.value &&
  newConn.value.owner !== currentConn.value.owner,
)

// deploy key form
const keyTitle = ref('')
const keyPublic = ref('')
const keyReadOnly = ref(true)
const keySubmitting = ref(false)
const keyError = ref<string | null>(null)

// collaborator form
const collabUser = ref('')
const collabPerm = ref('push')
const collabSubmitting = ref(false)
const collabError = ref<string | null>(null)

let timer: number | undefined

async function load() {
  error.value = null
  try {
    const [r, k, c, conns] = await Promise.all([
      api.getRepository(props.name),
      api.listDeployKeys(props.name),
      api.listCollaborators(props.name),
      api.listConnections(),
    ])
    repo.value = r
    keys.value = k
    collabs.value = c
    connections.value = conns
    // Seed the selector once; don't clobber an in-progress user choice on the poll.
    if (selectedConn.value === '') selectedConn.value = r.connectionRef
  } catch (e) {
    const err = e as ErrorResponse
    if (err.reason !== 'TenantMissing') error.value = `${err.reason}: ${err.message}`
  }
  // Packages are best-effort: a failure here (gateway unreachable, not yet
  // crawled) must not blank the rest of the page.
  await loadPackages()
}

async function loadPackages() {
  packagesError.value = null
  try {
    packages.value = await api.listPackages(props.name)
  } catch (e) {
    const err = e as ErrorResponse
    if (err.reason !== 'TenantMissing') packagesError.value = `${err.reason}: ${err.message}`
  }
}

async function changeConnection() {
  if (!repo.value || selectedConn.value === repo.value.connectionRef) return
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
  if (!ok) return
  changingConn.value = true
  connError.value = null
  try {
    await api.updateRepositoryConnection(repo.value.name, selectedConn.value)
    await load()
  } catch (e) {
    const err = e as ErrorResponse
    connError.value = `${err.reason}: ${err.message}`
  } finally {
    changingConn.value = false
  }
}

async function addKey() {
  keyError.value = null
  keySubmitting.value = true
  try {
    await api.createDeployKey({
      repositoryRef: props.name,
      title: keyTitle.value || undefined,
      publicKey: keyPublic.value || undefined,
      readOnly: keyReadOnly.value,
    })
    keyTitle.value = keyPublic.value = ''
    await load()
  } catch (e) {
    const err = e as ErrorResponse
    keyError.value = `${err.reason}: ${err.message}`
  } finally {
    keySubmitting.value = false
  }
}

async function removeKey(k: DeployKey) {
  const ok = await confirmDialog({
    title: `Delete deploy key "${k.title || k.name}"?`,
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  try {
    await api.deleteDeployKey(k.name)
    await load()
  } catch (e) {
    error.value = (e as ErrorResponse).message
  }
}

async function addCollab() {
  collabError.value = null
  if (!collabUser.value) {
    collabError.value = 'username is required'
    return
  }
  collabSubmitting.value = true
  try {
    await api.createCollaborator({ repositoryRef: props.name, username: collabUser.value, permission: collabPerm.value })
    collabUser.value = ''
    await load()
  } catch (e) {
    const err = e as ErrorResponse
    collabError.value = `${err.reason}: ${err.message}`
  } finally {
    collabSubmitting.value = false
  }
}

async function removeCollab(c: Collaborator) {
  const ok = await confirmDialog({
    title: `Remove collaborator "${c.username}"?`,
    confirmLabel: 'Remove',
    danger: true,
  })
  if (!ok) return
  try {
    await api.deleteCollaborator(c.name)
    await load()
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
    <button class="link back" @click="emit('back')">← Repositories</button>

    <header class="page-head">
      <div>
        <h2 class="page-title">{{ repo?.repo || name }}</h2>
        <p class="page-meta">
          <a v-if="repo?.htmlURL" :href="repo.htmlURL" target="_blank" rel="noopener">{{ repo.htmlURL }}</a>
          <span v-else class="muted">not created yet</span>
        </p>
      </div>
      <span v-if="repo" :class="['badge', repo.ready ? 'ok' : 'warn']" :title="repo.message">{{ repo.ready ? 'ready' : 'pending' }}</span>
    </header>

    <p v-if="error" class="error">{{ error }}</p>

    <!-- Overview -->
    <div v-if="repo" class="panel">
      <h3 class="panel-title">Overview</h3>
      <dl class="props">
        <dt>Connection</dt>
        <dd>
          <div class="conn-edit">
            <select v-model="selectedConn" :disabled="changingConn">
              <option v-for="c in connections" :key="c.name" :value="c.name">{{ c.name }} ({{ c.owner }})</option>
            </select>
            <button
              class="primary"
              :disabled="changingConn || selectedConn === repo.connectionRef"
              @click="changeConnection"
            >{{ changingConn ? 'Changing…' : 'Change' }}</button>
          </div>
          <p v-if="ownerWillChange" class="conn-warn">
            ⚠ Owner <code>{{ newOwner }}</code> differs from current <code>{{ currentOwner }}</code> —
            this re-targets the repo to a different account and may create a new repo there.
          </p>
          <p v-else-if="selectedConn !== repo.connectionRef" class="muted">Same owner — only the managing credential changes.</p>
          <span v-if="connError" class="error">{{ connError }}</span>
        </dd>
        <dt>Visibility</dt><dd>{{ repo.visibility }}</dd>
        <dt v-if="repo.cloneURL">Clone URL</dt><dd v-if="repo.cloneURL"><code>{{ repo.cloneURL }}</code></dd>
        <dt v-if="repo.sshURL">SSH URL</dt><dd v-if="repo.sshURL"><code>{{ repo.sshURL }}</code></dd>
      </dl>
    </div>

    <!-- Conditions -->
    <ConditionsPanel
      v-if="repo"
      :conditions="repo.conditions"
      :generation="repo.generation"
      :observed-generation="repo.observedGeneration"
      empty-text="No conditions yet — the controller has not reconciled this repository."
    />

    <div class="grid-2">
      <!-- Deploy keys -->
      <div class="panel">
        <div class="panel-head">
          <h3 class="panel-title">Deploy keys</h3>
          <span class="muted">{{ keys.length }}</span>
        </div>
        <form class="form" @submit.prevent="addKey">
          <div class="field"><span class="field-label">Title</span><input v-model="keyTitle" placeholder="ci-deploy" autocomplete="off" /></div>
          <div class="field">
            <span class="field-label">Public key (leave empty to generate)</span>
            <textarea v-model="keyPublic" rows="2" placeholder="ssh-ed25519 AAAA…" />
          </div>
          <label class="field field-check"><input v-model="keyReadOnly" type="checkbox" /> read-only</label>
          <div class="actions">
            <button class="primary" type="submit" :disabled="keySubmitting">{{ keySubmitting ? 'Adding…' : 'Add deploy key' }}</button>
            <span v-if="keyError" class="error">{{ keyError }}</span>
          </div>
          <p class="muted">A generated key's private half is written to a Secret in your workspace.</p>
        </form>
        <p v-if="!keys.length" class="empty">No deploy keys.</p>
        <table v-else class="table">
          <tbody>
            <tr v-for="k in keys" :key="k.name">
              <td>
                <strong>{{ k.title || k.name }}</strong>
                <div v-if="k.generated && k.secretName" class="muted">secret: <code>{{ k.secretName }}</code></div>
              </td>
              <td><span class="badge muted">{{ k.readOnly ? 'read-only' : 'read-write' }}</span></td>
              <td>
                <span v-if="k.ready" class="badge ok">ready</span>
                <span v-else class="badge warn" :title="k.message">pending</span>
              </td>
              <td class="right"><button class="danger" @click="removeKey(k)">Delete</button></td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Collaborators -->
      <div class="panel">
        <div class="panel-head">
          <h3 class="panel-title">Collaborators</h3>
          <span class="muted">{{ collabs.length }}</span>
        </div>
        <form class="form" @submit.prevent="addCollab">
          <div class="field"><span class="field-label">Username</span><input v-model="collabUser" placeholder="octocat" autocomplete="off" /></div>
          <div class="field">
            <span class="field-label">Permission</span>
            <select v-model="collabPerm">
              <option value="pull">pull</option>
              <option value="push">push</option>
              <option value="admin">admin</option>
            </select>
          </div>
          <div class="actions">
            <button class="primary" type="submit" :disabled="collabSubmitting">{{ collabSubmitting ? 'Adding…' : 'Add collaborator' }}</button>
            <span v-if="collabError" class="error">{{ collabError }}</span>
          </div>
        </form>
        <p v-if="!collabs.length" class="empty">No collaborators.</p>
        <table v-else class="table">
          <tbody>
            <tr v-for="c in collabs" :key="c.name">
              <td><strong>{{ c.username }}</strong></td>
              <td><span class="badge muted">{{ c.permission }}</span></td>
              <td>
                <span v-if="c.invitationPending" class="badge warn">invited</span>
                <span v-else-if="c.ready" class="badge ok">active</span>
              </td>
              <td class="right"><button class="danger" @click="removeCollab(c)">Remove</button></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Packages (read-only) -->
    <div class="panel">
      <div class="panel-head">
        <h3 class="panel-title">Packages</h3>
        <span class="muted">{{ packages.length }}</span>
      </div>
      <p v-if="packagesError" class="error">{{ packagesError }}</p>
      <p v-else-if="!packages.length" class="empty">No packages published to this repository yet.</p>
      <table v-else class="table">
        <thead>
          <tr><th>Package</th><th>Type</th><th>Visibility</th><th>Versions</th><th>Status</th><th></th></tr>
        </thead>
        <tbody>
          <tr v-for="p in packages" :key="p.type + '/' + p.name">
            <td>
              <strong v-if="p.htmlURL"><a :href="p.htmlURL" target="_blank" rel="noopener">{{ p.name }}</a></strong>
              <strong v-else>{{ p.name }}</strong>
            </td>
            <td><span class="badge muted">{{ p.type }}</span></td>
            <td><span class="muted">{{ p.visibility || '—' }}</span></td>
            <td><span class="muted">{{ p.versionCount || 0 }}</span></td>
            <td>
              <span v-if="p.ready" class="badge ok">synced</span>
              <span v-else class="badge warn" :title="p.message">{{ p.message ? 'error' : 'pending' }}</span>
            </td>
            <td class="right">
              <a v-if="p.htmlURL" class="link" :href="p.htmlURL" target="_blank" rel="noopener">View ↗</a>
            </td>
          </tr>
        </tbody>
      </table>
      <p class="muted">Packages appear automatically when artifacts are pushed (e.g. <code>docker push</code>, <code>npm publish</code>).</p>
    </div>
  </section>
</template>
