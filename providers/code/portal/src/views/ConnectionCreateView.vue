<script setup lang="ts">
import { computed, inject, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import { api, normalizeResourceName } from '../api'
import { contextGenerationKey } from '../context'
import type { Connection, ErrorResponse } from '../types'
import type { ConnectionCreateMethod } from '../routes'
import type { ResourceDeletions } from '../refresh'

const props = defineProps<{
  method: ConnectionCreateMethod
  deletions: ResourceDeletions
}>()
const emit = defineEmits<{
  (e: 'cancel'): void
  (e: 'created', name: string): void
}>()

const connections = ref<Connection[]>([])
const loading = ref(false)
const loaded = ref(false)
const readError = ref<string | null>(null)

const name = ref('')
const owner = ref('')
const token = ref('')
const baseURL = ref('')
const submitting = ref(false)
const formError = ref<string | null>(null)
type ConnectionField = 'name' | 'owner' | 'token'
const fieldErrors = ref<Partial<Record<ConnectionField, string>>>({})
const nameInput = ref<HTMLInputElement | null>(null)
const ownerInput = ref<HTMLInputElement | null>(null)
const tokenInput = ref<HTMLInputElement | null>(null)
const contextGeneration = inject(contextGenerationKey, ref(0))

// GitHub OAuth is deliberately kept on the creation route. The collection
// only chooses the creation method; this page owns the popup and the final
// confirmation so an OAuth credential can never be lost during navigation.
const oauthEnabled = ref(false)
const oauthLoaded = ref(false)
const oauthStartURL = ref('')
const oauthConfigError = ref<string | null>(null)
const oauthBusy = ref(false)
const oauthAuthorized = ref(false)
let oauthState = ''
let oauthOrigin = ''
let oauthPopup: Window | null = null
let oauthPopupTimer: number | undefined
let mounted = false
let readGeneration = 0
let oauthGeneration = 0
let mutationGeneration = 0
let oauthContextGeneration = 0

const isGitHub = computed(() => props.method === 'github')
const pageTitle = computed(() => isGitHub.value ? 'Connect with GitHub' : 'Add a token connection')
const pageMeta = computed(() => isGitHub.value
  ? 'Authorize GitHub, then choose the account or organization where repositories should be created.'
  : 'Store a GitHub personal access token in this workspace and validate the connection.')

function isDeleting(connection: Pick<Connection, 'name' | 'uid' | 'deletionTimestamp'>): boolean {
  return !!connection.deletionTimestamp || props.deletions.has('connection', connection.name, connection.uid)
}

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function isCurrentRead(generation: number, expectedContext: number): boolean {
  return mounted && generation === readGeneration && contextGeneration.value === expectedContext
}

function isCurrentOAuthRead(generation: number, expectedContext: number): boolean {
  return mounted && generation === oauthGeneration && contextGeneration.value === expectedContext
}

function isCurrentMutation(generation: number, expectedContext: number): boolean {
  return mounted && generation === mutationGeneration && contextGeneration.value === expectedContext
}

function clearFieldError(field: ConnectionField): void {
  if (!fieldErrors.value[field]) return
  const next = { ...fieldErrors.value }
  delete next[field]
  fieldErrors.value = next
}

function focusField(field: ConnectionField): void {
  void nextTick(() => ({ name: nameInput, owner: ownerInput, token: tokenInput })[field].value?.focus?.())
}

function setFieldError(field: ConnectionField, message: string): void {
  fieldErrors.value = { ...fieldErrors.value, [field]: message }
  focusField(field)
}

function validateFields(): boolean {
  const errors: Partial<Record<ConnectionField, string>> = {}
  if (!name.value.trim()) errors.name = 'Enter a connection name.'
  if (!owner.value.trim()) errors.owner = 'Enter a GitHub account or organization.'
  if (!oauthAuthorized.value && !token.value) errors.token = 'Enter a personal access token.'
  fieldErrors.value = errors
  const first = (['name', 'owner', 'token'] as const).find(field => errors[field])
  if (first) focusField(first)
  return !first
}

async function loadConnections(): Promise<void> {
  const generation = ++readGeneration
  const expectedContext = contextGeneration.value
  loading.value = true
  try {
    const next = await api.listConnections()
    if (!isCurrentRead(generation, expectedContext)) return
    connections.value = next
    next
      .filter(item => item.deletionTimestamp)
      .forEach(item => props.deletions.acknowledge('connection', item.name, item.uid))
    props.deletions.reconcile('connection', next)
    loaded.value = true
    readError.value = null
  } catch (e) {
    if (!isCurrentRead(generation, expectedContext)) return
    const err = e as ErrorResponse
    readError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (isCurrentRead(generation, expectedContext)) loading.value = false
  }
}

function randomState(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
}

function clearOAuthWait(message?: string): void {
  const popup = oauthPopup
  window.clearInterval(oauthPopupTimer)
  oauthPopupTimer = undefined
  oauthBusy.value = false
  oauthPopup = null
  oauthOrigin = ''
  oauthState = ''
  oauthContextGeneration = 0
  if (popup && !popup.closed) popup.close()
  if (message) formError.value = message
}

function cancel(): void {
  if (submitting.value) return
  if (oauthBusy.value) clearOAuthWait()
  emit('cancel')
}

function connectGitHub(): void {
  if (!oauthStartURL.value) {
    formError.value = 'GitHub sign-in is unavailable — contact a platform admin'
    return
  }
  oauthBusy.value = true
  formError.value = null
  oauthContextGeneration = contextGeneration.value
  oauthState = randomState()
  let url: URL
  try {
    // The provider normally returns a same-origin relative path. Resolve it
    // before recording the trusted callback origin, and reject schemes that
    // could execute in the opener's page context.
    url = new URL(oauthStartURL.value, window.location.href)
    if (url.protocol !== 'https:' && url.protocol !== 'http:') throw new Error('unsupported OAuth URL scheme')
  } catch {
    clearOAuthWait('invalid GitHub OAuth start URL — contact a platform admin')
    return
  }
  oauthOrigin = url.origin
  url.searchParams.set('state', oauthState)
  oauthPopup = window.open(url.toString(), 'faros-github-oauth', 'width=720,height=820')
  if (!oauthPopup) {
    clearOAuthWait('popup blocked — allow popups and retry')
    return
  }
  oauthPopupTimer = window.setInterval(() => {
    if (contextGeneration.value !== oauthContextGeneration) clearOAuthWait()
    else if (oauthPopup?.closed) clearOAuthWait('GitHub sign-in window was closed — retry when ready')
  }, 500)
}

function onMessage(ev: MessageEvent): void {
  if (!oauthBusy.value) return
  if (!mounted || contextGeneration.value !== oauthContextGeneration) {
    clearOAuthWait()
    return
  }
  if (!oauthPopup || ev.source !== oauthPopup || ev.origin !== oauthOrigin) return
  const data = ev.data as { type?: string; state?: string; token?: string; login?: string; error?: string }
  if (!data || data.type !== 'faros-github-oauth') return
  if (data.state !== oauthState) {
    clearOAuthWait('oauth state mismatch — please retry')
    return
  }
  if (data.error || !data.token) {
    clearOAuthWait(data.error || 'no token returned from GitHub')
    return
  }
  clearOAuthWait()
  token.value = data.token
  owner.value = data.login || ''
  name.value = data.login ? 'github-' + data.login : 'github'
  fieldErrors.value = {}
  oauthAuthorized.value = true
}

async function submit(): Promise<void> {
  if (!mounted || submitting.value) return
  const generation = ++mutationGeneration
  const expectedContext = contextGeneration.value
  formError.value = null
  if (!loaded.value) {
    formError.value = 'Connection list is still loading. Retry the read before creating a connection.'
    return
  }
  if (!validateFields()) return
  const payload = {
    name: name.value,
    owner: owner.value,
    token: token.value,
    baseURL: baseURL.value || undefined,
    type: isGitHub.value ? 'oauth' as const : 'pat' as const,
  }
  const desiredName = normalizeResourceName(payload.name)
  submitting.value = true
  try {
    // The initial read only makes the form usable. Re-read the complete
    // collection immediately before applying so an active connection with the
    // normalized name can never be silently adopted and have its Secret
    // overwritten by api.connect.
    const currentConnections = await api.listConnections()
    if (!isCurrentMutation(generation, expectedContext)) return
    connections.value = currentConnections
    currentConnections
      .filter(item => item.deletionTimestamp)
      .forEach(item => props.deletions.acknowledge('connection', item.name, item.uid))
    props.deletions.reconcile('connection', currentConnections)
    const existing = currentConnections.find(connection => normalizeResourceName(connection.name) === desiredName)
    if (existing && isDeleting(existing)) {
      setFieldError('name', `Connection "${existing.name}" is still deleting. Wait for it to disappear before reconnecting.`)
      return
    }
    if (existing) {
      setFieldError('name', `Connection "${existing.name}" already exists.`)
      return
    }
    if (props.deletions.has('connection', desiredName)) {
      setFieldError('name', `Connection "${desiredName}" is still deleting. Wait for it to disappear before reconnecting.`)
      return
    }
    if (!isCurrentMutation(generation, expectedContext)) return
    const created = await api.connect(payload)
    if (!isCurrentMutation(generation, expectedContext)) return
    emit('created', created.name)
  } catch (e) {
    if (isCurrentMutation(generation, expectedContext)) formError.value = errMessage(e)
  } finally {
    if (isCurrentMutation(generation, expectedContext)) submitting.value = false
  }
}

async function loadOAuthConfig(): Promise<void> {
  const generation = ++oauthGeneration
  const expectedContext = contextGeneration.value
  oauthConfigError.value = null
  oauthLoaded.value = false
  try {
    const config = await api.oauthConfig()
    if (!isCurrentOAuthRead(generation, expectedContext)) return
    oauthEnabled.value = config.enabled
    oauthStartURL.value = config.startURL || ''
  } catch (e) {
    if (!isCurrentOAuthRead(generation, expectedContext)) return
    oauthEnabled.value = false
    oauthStartURL.value = ''
    oauthConfigError.value = errMessage(e)
  } finally {
    if (isCurrentOAuthRead(generation, expectedContext)) oauthLoaded.value = true
  }
}

onMounted(() => {
  mounted = true
  void loadConnections()
  if (isGitHub.value) void loadOAuthConfig()
})
onUnmounted(() => {
  mounted = false
  readGeneration += 1
  oauthGeneration += 1
  mutationGeneration += 1
  clearOAuthWait()
  window.removeEventListener('message', onMessage)
})

// Register the listener after setup so a popup callback cannot race a route
// change before the component has mounted.
onMounted(() => window.addEventListener('message', onMessage))
</script>

<template>
  <section class="page k-create-page">
    <button class="k-btn k-btn--ghost k-back-action" type="button" :disabled="submitting" @click="cancel">
      <ArrowLeft :size="14" aria-hidden="true" /> Connections
    </button>
    <header class="k-create-header">
      <h2 class="k-create-title">{{ pageTitle }}</h2>
      <p class="k-create-description">{{ pageMeta }}</p>
    </header>

    <div v-if="loading && !loaded" class="panel k-card" role="status" aria-live="polite">Loading connections…</div>
    <div v-if="readError" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ readError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="loadConnections">Retry connections</button>
    </div>

    <div v-if="isGitHub && !oauthAuthorized" class="k-create-surface">
      <div class="k-create-body">
      <p class="muted">A separate GitHub window will open. After authorization, review the connection details before creating it.</p>
      <span v-if="oauthLoaded && !oauthEnabled && !oauthConfigError" class="muted">GitHub OAuth is not configured. Use the token connection flow instead.</span>
      <span v-else-if="!oauthLoaded" class="muted" role="status" aria-live="polite">Checking GitHub sign-in…</span>
      <div v-if="oauthConfigError" class="error read-error" role="alert" aria-live="assertive">
        <span>GitHub sign-in configuration could not be loaded: {{ oauthConfigError }}</span>
        <button class="k-btn k-btn--ghost" type="button" @click="loadOAuthConfig">Retry GitHub sign-in</button>
      </div>
      <p v-if="formError" class="error" role="alert">{{ formError }}</p>
      </div>
      <div class="k-create-actions">
        <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="cancel">Cancel</button>
        <button v-if="oauthEnabled" class="k-btn k-btn--primary" :disabled="oauthBusy || !oauthLoaded" type="button" @click="connectGitHub">
          {{ oauthBusy ? 'Waiting for GitHub…' : 'Continue with GitHub' }}
        </button>
      </div>
    </div>

    <form v-if="method === 'token' || oauthAuthorized" class="k-create-surface" :aria-busy="submitting" novalidate @submit.prevent="submit">
      <div class="k-create-body">
        <p v-if="oauthAuthorized" class="muted">
          Authorized via GitHub<span v-if="owner"> as <code>{{ owner }}</code></span>. Pick the org/account to create repositories under, then confirm.
        </p>
        <label class="field" for="code-connection-name"><span class="field-label">Name</span><input id="code-connection-name" ref="nameInput" v-model="name" class="k-input" placeholder="my-github" autocomplete="off" required aria-required="true" :aria-invalid="fieldErrors.name ? 'true' : undefined" :aria-describedby="fieldErrors.name ? 'code-connection-name-error' : undefined" @input="clearFieldError('name')" /><span v-if="fieldErrors.name" id="code-connection-name-error" class="error" role="alert">{{ fieldErrors.name }}</span></label>
        <label class="field" for="code-connection-owner"><span class="field-label">Owner (org or user)</span><input id="code-connection-owner" ref="ownerInput" v-model="owner" class="k-input" placeholder="acme" autocomplete="off" required aria-required="true" :aria-invalid="fieldErrors.owner ? 'true' : undefined" :aria-describedby="fieldErrors.owner ? 'code-connection-owner-error' : undefined" @input="clearFieldError('owner')" /><span v-if="fieldErrors.owner" id="code-connection-owner-error" class="error" role="alert">{{ fieldErrors.owner }}</span></label>
        <label v-if="!oauthAuthorized" class="field" for="code-connection-token"><span class="field-label">Personal access token</span><input id="code-connection-token" ref="tokenInput" v-model="token" class="k-input" type="password" placeholder="ghp_…" autocomplete="new-password" required aria-required="true" :aria-invalid="fieldErrors.token ? 'true' : undefined" :aria-describedby="fieldErrors.token ? 'code-connection-token-error' : undefined" @input="clearFieldError('token')" /><span v-if="fieldErrors.token" id="code-connection-token-error" class="error" role="alert">{{ fieldErrors.token }}</span></label>
        <label v-else class="field"><span class="field-label">Credential</span><input class="k-input" value="GitHub OAuth — authorized" disabled /></label>
        <label v-if="!oauthAuthorized" class="field"><span class="field-label">Base URL (GHES, optional)</span><input v-model="baseURL" class="k-input" placeholder="https://github.example.com/api/v3" autocomplete="url" /></label>
        <p class="muted">The token is stored as a Secret in your workspace; the provider validates it and shows the login below.</p>
        <span v-if="formError" class="error" role="alert">{{ formError }}</span>
        <span v-if="submitting" class="sr-only" role="status" aria-live="polite">Creating connection…</span>
      </div>
      <div class="k-create-actions">
        <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="cancel">Cancel</button>
        <button class="k-btn k-btn--primary" type="submit" :disabled="submitting || !loaded">{{ submitting ? 'Connecting…' : 'Create connection' }}</button>
      </div>
    </form>
  </section>
</template>
