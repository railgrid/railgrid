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
const connType = ref<'pat' | 'oauth'>('pat')
const submitting = ref(false)
const formError = ref<string | null>(null)

// GitHub OAuth ("Connect with GitHub") — enabled when the provider is configured.
const oauthEnabled = ref(false)
const oauthStartURL = ref('')
const oauthBusy = ref(false)
let oauthState = ''
let oauthOrigin = ''

let timer: number | undefined

function resetForm() {
  name.value = owner.value = token.value = baseURL.value = ''
  connType.value = 'pat'
}

function randomState(): string {
  const a = new Uint8Array(16)
  crypto.getRandomValues(a)
  return Array.from(a, b => b.toString(16).padStart(2, '0')).join('')
}

// connectGitHub opens the provider's OAuth start URL in a popup and waits for it
// to postMessage the access token back. On success it prefills + reveals the
// form (type=oauth) so the user can confirm the name/owner, then Create.
function connectGitHub() {
  if (!oauthStartURL.value) return
  oauthBusy.value = true
  formError.value = null
  oauthState = randomState()
  try {
    oauthOrigin = new URL(oauthStartURL.value).origin
  } catch {
    oauthOrigin = ''
  }
  const url = oauthStartURL.value + '?state=' + encodeURIComponent(oauthState)
  const popup = window.open(url, 'kedge-github-oauth', 'width=720,height=820')
  if (!popup) {
    oauthBusy.value = false
    formError.value = 'popup blocked — allow popups and retry'
  }
}

function onMessage(ev: MessageEvent) {
  if (!oauthBusy.value) return
  if (oauthOrigin && ev.origin !== oauthOrigin) return
  const d = ev.data as { type?: string; state?: string; token?: string; login?: string; error?: string }
  if (!d || d.type !== 'kedge-github-oauth') return
  oauthBusy.value = false
  if (d.state !== oauthState) {
    formError.value = 'oauth state mismatch — please retry'
    return
  }
  if (d.error || !d.token) {
    formError.value = d.error || 'no token returned from GitHub'
    return
  }
  // Prefill the form so the user confirms which org/account to create under.
  token.value = d.token
  owner.value = d.login || ''
  name.value = d.login ? 'github-' + d.login : 'github'
  connType.value = 'oauth'
  showForm.value = true
}

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
    await api.connect({ name: name.value, owner: owner.value, token: token.value, baseURL: baseURL.value || undefined, type: connType.value })
    resetForm()
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

onMounted(async () => {
  load()
  timer = window.setInterval(load, 5000)
  window.addEventListener('message', onMessage)
  const cfg = await api.oauthConfig()
  oauthEnabled.value = cfg.enabled
  oauthStartURL.value = cfg.startURL || ''
})
onUnmounted(() => {
  window.clearInterval(timer)
  window.removeEventListener('message', onMessage)
})
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Connections</h2>
        <p class="page-meta">A connection binds your workspace to a git account. Repositories are created under it.</p>
      </div>
      <div class="actions">
        <button v-if="oauthEnabled" class="primary" :disabled="oauthBusy" @click="connectGitHub">
          {{ oauthBusy ? 'Waiting for GitHub…' : 'Connect with GitHub' }}
        </button>
        <button :class="oauthEnabled ? 'secondary' : 'primary'" @click="showForm = !showForm">
          {{ showForm ? 'Cancel' : 'Add token manually' }}
        </button>
      </div>
    </header>

    <p v-if="!oauthEnabled" class="muted">
      Tip: a platform admin can enable one-click “Connect with GitHub” by configuring the provider’s GitHub OAuth app.
    </p>

    <div v-if="showForm" class="panel">
      <h3 class="panel-title">{{ connType === 'oauth' ? 'Confirm GitHub connection' : 'Connect with a token' }}</h3>
      <form class="form" @submit.prevent="submit">
        <p v-if="connType === 'oauth'" class="muted">
          Authorized via GitHub<span v-if="owner"> as <code>{{ owner }}</code></span>. Pick the org/account to create repositories under, then confirm.
        </p>
        <div class="field"><span class="field-label">Name</span><input v-model="name" placeholder="my-github" autocomplete="off" /></div>
        <div class="field"><span class="field-label">Owner (org or user)</span><input v-model="owner" placeholder="acme" autocomplete="off" /></div>
        <div v-if="connType === 'pat'" class="field"><span class="field-label">Personal access token</span><input v-model="token" type="password" placeholder="ghp_…" autocomplete="off" /></div>
        <div v-else class="field"><span class="field-label">Credential</span><input value="GitHub OAuth — authorized ✓" disabled /></div>
        <div v-if="connType === 'pat'" class="field"><span class="field-label">Base URL (GHES, optional)</span><input v-model="baseURL" placeholder="https://github.example.com/api/v3" autocomplete="off" /></div>
        <div class="actions">
          <button class="primary" type="submit" :disabled="submitting">{{ submitting ? 'Connecting…' : 'Create' }}</button>
          <button class="secondary" type="button" @click="() => { showForm = false; resetForm() }">Cancel</button>
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
