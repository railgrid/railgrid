<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import type { Connection, ErrorResponse } from '../types'

const connections = ref<Connection[]>([])
const error = ref<string | null>(null)
const loading = ref(false)

// connect form
const showForm = ref(false)
const name = ref('')
const owner = ref('')
const token = ref('')
const baseURL = ref('')
const submitting = ref(false)
const formError = ref<string | null>(null)

let timer: number | undefined

async function load() {
  loading.value = true
  error.value = null
  try {
    connections.value = await api.listConnections()
  } catch (e) {
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : `${err.reason}: ${err.message}`
  } finally {
    loading.value = false
  }
}

async function submit() {
  formError.value = null
  if (!name.value || !owner.value || !token.value) {
    formError.value = 'name, owner, and token are required'
    return
  }
  submitting.value = true
  try {
    await api.connect({ name: name.value, owner: owner.value, token: token.value, baseURL: baseURL.value || undefined })
    name.value = owner.value = token.value = baseURL.value = ''
    showForm.value = false
    await load()
  } catch (e) {
    const err = e as ErrorResponse
    formError.value = `${err.reason}: ${err.message}`
  } finally {
    submitting.value = false
  }
}

async function remove(c: Connection) {
  if (!confirm(`Delete connection "${c.name}"? Repositories using it will stop reconciling.`)) return
  try {
    await api.deleteConnection(c.name)
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
        <h2 class="page-title">Connections</h2>
        <p class="page-meta">A connection binds your workspace to a git account via a token. Repositories are created under it.</p>
      </div>
      <button class="primary" @click="showForm = !showForm">{{ showForm ? 'Cancel' : 'Connect GitHub' }}</button>
    </header>

    <div v-if="showForm" class="panel">
      <h3 class="panel-title">Connect GitHub</h3>
      <form class="form" @submit.prevent="submit">
        <div class="field"><span class="field-label">Name</span><input v-model="name" placeholder="my-github" autocomplete="off" /></div>
        <div class="field"><span class="field-label">Owner (org or user)</span><input v-model="owner" placeholder="acme" autocomplete="off" /></div>
        <div class="field"><span class="field-label">Personal access token</span><input v-model="token" type="password" placeholder="ghp_…" autocomplete="off" /></div>
        <div class="field"><span class="field-label">Base URL (GHES, optional)</span><input v-model="baseURL" placeholder="https://github.example.com/api/v3" autocomplete="off" /></div>
        <div class="actions">
          <button class="primary" type="submit" :disabled="submitting">{{ submitting ? 'Connecting…' : 'Create' }}</button>
          <span v-if="formError" class="error">{{ formError }}</span>
        </div>
        <p class="muted">The token is stored as a Secret in your workspace; the provider validates it and shows the login below.</p>
      </form>
    </div>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="loading && !connections.length" class="muted">Loading…</p>
    <p v-else-if="!connections.length" class="empty">No connections yet.</p>

    <div v-else class="panel">
      <table class="table">
        <thead>
          <tr><th>Name</th><th>Owner</th><th>Login</th><th>Status</th><th class="right"></th></tr>
        </thead>
        <tbody>
          <tr v-for="c in connections" :key="c.name">
            <td>{{ c.name }}</td>
            <td>{{ c.owner }}</td>
            <td>{{ c.login || '—' }}</td>
            <td>
              <span v-if="c.validated" class="badge ok">validated</span>
              <span v-else class="badge warn" :title="c.message">pending</span>
            </td>
            <td class="right"><button class="danger" @click="remove(c)">Delete</button></td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
