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
    // Default to (and recover to) a connection that actually exists — otherwise
    // a stale ref (e.g. a since-deleted connection) silently sticks and the
    // Repository fails to reconcile with "connection not found".
    if (c.length && !c.some(x => x.name === connectionRef.value)) {
      connectionRef.value = c[0].name
    }
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
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Repositories</h2>
        <p class="page-meta">Repositories the provider manages on the git host. Click one to manage deploy keys and collaborators.</p>
      </div>
      <button class="primary" :disabled="!connections.length" @click="showForm = !showForm">
        {{ showForm ? 'Cancel' : 'New repository' }}
      </button>
    </header>

    <p v-if="!connections.length" class="empty">Add a connection first, then create repositories under it.</p>

    <div v-if="showForm" class="panel">
      <h3 class="panel-title">New repository</h3>
      <form class="form" @submit.prevent="submit">
        <div class="field">
          <span class="field-label">Connection</span>
          <select v-model="connectionRef">
            <option v-for="c in connections" :key="c.name" :value="c.name">{{ c.name }} ({{ c.owner }})</option>
          </select>
        </div>
        <div class="field"><span class="field-label">Object name</span><input v-model="name" placeholder="my-service" autocomplete="off" /></div>
        <div class="field"><span class="field-label">Repo name (defaults to object name)</span><input v-model="repo" placeholder="my-service" autocomplete="off" /></div>
        <div class="field">
          <span class="field-label">Visibility</span>
          <select v-model="visibility">
            <option value="private">private</option>
            <option value="public">public</option>
            <option value="internal">internal</option>
          </select>
        </div>
        <div class="field"><span class="field-label">Description</span><input v-model="description" autocomplete="off" /></div>
        <label class="field field-check"><input v-model="autoInit" type="checkbox" /> Initialize with a README</label>
        <div class="actions">
          <button class="primary" type="submit" :disabled="submitting">{{ submitting ? 'Creating…' : 'Create' }}</button>
          <span v-if="formError" class="error">{{ formError }}</span>
        </div>
      </form>
    </div>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="loading && !repos.length" class="muted">Loading…</p>
    <p v-else-if="!repos.length" class="empty">No repositories yet.</p>

    <div v-else class="panel">
      <table class="table">
        <thead>
          <tr><th>Name</th><th>Connection</th><th>Visibility</th><th>URL</th><th>Status</th><th class="right"></th></tr>
        </thead>
        <tbody>
          <tr v-for="r in repos" :key="r.name">
            <td><button class="link" @click="emit('open', r.name)">{{ r.repo }}</button></td>
            <td>{{ r.connectionRef }}</td>
            <td>{{ r.visibility }}</td>
            <td><a v-if="r.htmlURL" :href="r.htmlURL" target="_blank" rel="noopener">open ↗</a><span v-else class="muted">—</span></td>
            <td>
              <span v-if="r.ready" class="badge ok">ready</span>
              <span v-else class="badge warn" :title="r.message">pending</span>
            </td>
            <td class="right"><button class="danger" @click="remove(r)">Delete</button></td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
