<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import type { Connection, ErrorResponse, Repository } from '../types'

const emit = defineEmits<{ (e: 'open', name: string): void }>()

const repos = ref<Repository[]>([])
const connections = ref<Connection[]>([])
const error = ref<string | null>(null)
const loading = ref(false)

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

async function load() {
  loading.value = true
  error.value = null
  try {
    const [r, c] = await Promise.all([api.listRepositories(), api.listConnections()])
    repos.value = r
    connections.value = c
    if (!connectionRef.value && c.length) connectionRef.value = c[0].name
  } catch (e) {
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : `${err.reason}: ${err.message}`
  } finally {
    loading.value = false
  }
}

async function submit() {
  formError.value = null
  if (!name.value || !connectionRef.value) {
    formError.value = 'name and connection are required'
    return
  }
  submitting.value = true
  try {
    await api.createRepository({
      name: name.value,
      connectionRef: connectionRef.value,
      repo: repo.value || undefined,
      visibility: visibility.value,
      description: description.value || undefined,
      autoInit: autoInit.value,
    })
    name.value = repo.value = description.value = ''
    showForm.value = false
    await load()
  } catch (e) {
    const err = e as ErrorResponse
    formError.value = `${err.reason}: ${err.message}`
  } finally {
    submitting.value = false
  }
}

async function remove(r: Repository) {
  if (!confirm(`Delete repository "${r.repo}"? This removes it on the git host.`)) return
  try {
    await api.deleteRepository(r.name)
    await load()
  } catch (e) {
    const err = e as ErrorResponse
    error.value = `${err.reason}: ${err.message}`
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
        <h2>Repositories</h2>
        <p class="code-muted">Repositories the provider manages on the git host. Click one to manage deploy keys and collaborators.</p>
      </div>
      <button class="code-btn primary" :disabled="!connections.length" @click="showForm = !showForm">
        {{ showForm ? 'Cancel' : 'New repository' }}
      </button>
    </header>

    <p v-if="!connections.length" class="code-muted">Add a connection first, then create repositories under it.</p>

    <form v-if="showForm" class="code-form" @submit.prevent="submit">
      <label>Connection
        <select v-model="connectionRef">
          <option v-for="c in connections" :key="c.name" :value="c.name">{{ c.name }} ({{ c.owner }})</option>
        </select>
      </label>
      <label>Object name <input v-model="name" placeholder="my-service" autocomplete="off" /></label>
      <label>Repo name (defaults to object name) <input v-model="repo" placeholder="my-service" autocomplete="off" /></label>
      <label>Visibility
        <select v-model="visibility">
          <option value="private">private</option>
          <option value="public">public</option>
          <option value="internal">internal</option>
        </select>
      </label>
      <label>Description <input v-model="description" autocomplete="off" /></label>
      <label class="code-check"><input v-model="autoInit" type="checkbox" /> Initialize with a README</label>
      <div class="code-form-actions">
        <button class="code-btn primary" type="submit" :disabled="submitting">{{ submitting ? 'Creating…' : 'Create' }}</button>
        <span v-if="formError" class="code-error">{{ formError }}</span>
      </div>
    </form>

    <p v-if="error" class="code-error">{{ error }}</p>
    <p v-else-if="loading && !repos.length" class="code-muted">Loading…</p>
    <p v-else-if="!repos.length" class="code-muted">No repositories yet.</p>

    <table v-else class="code-table">
      <thead>
        <tr><th>Name</th><th>Connection</th><th>Visibility</th><th>URL</th><th>Status</th><th></th></tr>
      </thead>
      <tbody>
        <tr v-for="r in repos" :key="r.name">
          <td><button class="code-link" @click="emit('open', r.name)">{{ r.repo }}</button></td>
          <td>{{ r.connectionRef }}</td>
          <td>{{ r.visibility }}</td>
          <td><a v-if="r.htmlURL" :href="r.htmlURL" target="_blank" rel="noopener">open ↗</a><span v-else>—</span></td>
          <td>
            <span v-if="r.ready" class="code-badge ok">ready</span>
            <span v-else class="code-badge warn" :title="r.message">pending</span>
          </td>
          <td class="code-right"><button class="code-btn ghost" @click="remove(r)">Delete</button></td>
        </tr>
      </tbody>
    </table>
  </section>
</template>
