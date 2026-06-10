<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import type { Collaborator, DeployKey, ErrorResponse, Repository } from '../types'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const repo = ref<Repository | null>(null)
const keys = ref<DeployKey[]>([])
const collabs = ref<Collaborator[]>([])
const error = ref<string | null>(null)

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
    const [r, k, c] = await Promise.all([
      api.getRepository(props.name),
      api.listDeployKeys(props.name),
      api.listCollaborators(props.name),
    ])
    repo.value = r
    keys.value = k
    collabs.value = c
  } catch (e) {
    const err = e as ErrorResponse
    if (err.reason !== 'TenantMissing') error.value = `${err.reason}: ${err.message}`
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
  if (!confirm(`Delete deploy key "${k.title || k.name}"?`)) return
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
  if (!confirm(`Remove collaborator "${c.username}"?`)) return
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
        <dt>Connection</dt><dd>{{ repo.connectionRef }}</dd>
        <dt>Visibility</dt><dd>{{ repo.visibility }}</dd>
        <dt v-if="repo.cloneURL">Clone URL</dt><dd v-if="repo.cloneURL"><code>{{ repo.cloneURL }}</code></dd>
        <dt v-if="repo.sshURL">SSH URL</dt><dd v-if="repo.sshURL"><code>{{ repo.sshURL }}</code></dd>
      </dl>
    </div>

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
  </section>
</template>
