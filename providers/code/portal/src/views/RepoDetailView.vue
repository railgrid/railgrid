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
  <section class="code-panel">
    <header class="code-row code-head">
      <div>
        <button class="code-link" @click="emit('back')">← Repositories</button>
        <h2>{{ repo?.repo || name }}</h2>
        <p class="code-muted">
          <a v-if="repo?.htmlURL" :href="repo.htmlURL" target="_blank" rel="noopener">{{ repo.htmlURL }}</a>
          <span v-else>not created yet</span>
        </p>
        <p v-if="repo?.sshURL" class="code-muted small">SSH: <code>{{ repo.sshURL }}</code></p>
      </div>
    </header>

    <p v-if="error" class="code-error">{{ error }}</p>

    <div class="code-cols">
      <!-- Deploy keys -->
      <div class="code-col">
        <h3>Deploy keys</h3>
        <form class="code-form" @submit.prevent="addKey">
          <label>Title <input v-model="keyTitle" placeholder="ci-deploy" autocomplete="off" /></label>
          <label>Public key (leave empty to generate)
            <textarea v-model="keyPublic" rows="2" placeholder="ssh-ed25519 AAAA…" />
          </label>
          <label class="code-check"><input v-model="keyReadOnly" type="checkbox" /> read-only</label>
          <div class="code-form-actions">
            <button class="code-btn primary" type="submit" :disabled="keySubmitting">{{ keySubmitting ? 'Adding…' : 'Add deploy key' }}</button>
            <span v-if="keyError" class="code-error">{{ keyError }}</span>
          </div>
          <p class="code-muted small">A generated key's private half is written to a Secret in your workspace.</p>
        </form>
        <p v-if="!keys.length" class="code-muted">No deploy keys.</p>
        <ul v-else class="code-list">
          <li v-for="k in keys" :key="k.name">
            <span>
              <strong>{{ k.title || k.name }}</strong>
              <span class="code-badge" :class="k.readOnly ? 'muted' : 'warn'">{{ k.readOnly ? 'read-only' : 'read-write' }}</span>
              <span v-if="k.generated && k.secretName" class="code-badge muted" :title="k.secretName">secret: {{ k.secretName }}</span>
              <span v-if="k.ready" class="code-badge ok">ready</span>
              <span v-else class="code-badge warn" :title="k.message">pending</span>
            </span>
            <button class="code-btn ghost" @click="removeKey(k)">Delete</button>
          </li>
        </ul>
      </div>

      <!-- Collaborators -->
      <div class="code-col">
        <h3>Collaborators</h3>
        <form class="code-form" @submit.prevent="addCollab">
          <label>Username <input v-model="collabUser" placeholder="octocat" autocomplete="off" /></label>
          <label>Permission
            <select v-model="collabPerm">
              <option value="pull">pull</option>
              <option value="push">push</option>
              <option value="admin">admin</option>
            </select>
          </label>
          <div class="code-form-actions">
            <button class="code-btn primary" type="submit" :disabled="collabSubmitting">{{ collabSubmitting ? 'Adding…' : 'Add collaborator' }}</button>
            <span v-if="collabError" class="code-error">{{ collabError }}</span>
          </div>
        </form>
        <p v-if="!collabs.length" class="code-muted">No collaborators.</p>
        <ul v-else class="code-list">
          <li v-for="c in collabs" :key="c.name">
            <span>
              <strong>{{ c.username }}</strong>
              <span class="code-badge muted">{{ c.permission }}</span>
              <span v-if="c.invitationPending" class="code-badge warn">invited</span>
              <span v-else-if="c.ready" class="code-badge ok">active</span>
            </span>
            <button class="code-btn ghost" @click="removeCollab(c)">Remove</button>
          </li>
        </ul>
      </div>
    </div>
  </section>
</template>
