<script setup lang="ts">
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import { api, isPreDispatchError, isRejectedConnectionWrite, normalizeResourceName } from '../api'
import { contextGenerationKey } from '../context'
import type { Connection, ErrorResponse } from '../types'
import type { ConnectionCreateMethod } from '../routes'
import type { ResourceDeletions } from '../refresh'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import { toast } from '../portalkit/toast'

const props = defineProps<{
  method: ConnectionCreateMethod
  deletions: ResourceDeletions
  recoveryScope: string | null
}>()
const emit = defineEmits<{
  (e: 'cancel'): void
  (e: 'created', name: string): void
  (e: 'use-token'): void
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
const recoveryStorage = (() => { try { return window.sessionStorage } catch { return null } })()
const recoveryKey = props.recoveryScope ? `railgrid:code:connection-attempt:${props.recoveryScope}` : null
let recoveryRecord: string | null = null
const recoveryUnavailable = ref(false)
const uncertainName = ref<string | null>(null)
try {
  if (!recoveryStorage || !recoveryKey) throw new Error('Storage unavailable')
  recoveryRecord = recoveryStorage.getItem(recoveryKey)
  if (recoveryRecord) {
    const record: unknown = JSON.parse(recoveryRecord)
    if (!record || typeof record !== 'object' || typeof (record as { name?: unknown }).name !== 'string' || typeof (record as { id?: unknown }).id !== 'string') throw new Error('Invalid recovery record')
    uncertainName.value = (record as { name: string }).name
  }
} catch { recoveryUnavailable.value = true }
function clearRecovery(): boolean {
  try {
    if (!recoveryStorage || !recoveryKey || !recoveryRecord) throw new Error('Recovery record unavailable')
    const current = recoveryStorage.getItem(recoveryKey)
    if (current !== null && current !== recoveryRecord) throw new Error('Recovery record changed')
    if (current !== null) recoveryStorage.removeItem(recoveryKey)
    recoveryRecord = null
    uncertainName.value = null
    return true
  } catch { recoveryUnavailable.value = true; return false }
}
const uncertainContext = ref<number | null>(uncertainName.value ? 0 : null)
const inspected = ref(false)
const inspection = ref<string | null>(null)
const inspecting = ref(false)
const READ_TIMEOUT_MS = 15_000

async function boundedRead<T>(request: Promise<T>): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([request, new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error('The request timed out.')), READ_TIMEOUT_MS)
    })])
  } finally {
    clearTimeout(timer)
  }
}
type ConnectionField = 'name' | 'owner' | 'token'
const fieldErrors = ref<Partial<Record<ConnectionField, string>>>({})
const nameInput = ref<HTMLInputElement | null>(null)
const ownerInput = ref<HTMLInputElement | null>(null)
const tokenInput = ref<HTMLInputElement | null>(null)
const contextGeneration = inject(contextGenerationKey, ref(0))
if (uncertainName.value) uncertainContext.value = contextGeneration.value

// GitHub OAuth is deliberately kept on the creation route. The collection
// only chooses the creation method; this page owns the popup and the final
// confirmation so an OAuth credential can never be lost during navigation.
const oauthEnabled = ref(false)
const oauthLoaded = ref(false)
const oauthStartURL = ref('')
const oauthConfigError = ref<string | null>(null)
const oauthBusy = ref(false)
const oauthAuthorized = ref(false)
const oauthLogin = ref('')
// Set when the OAuth App issues expiring tokens; the provider renews with it.
const oauthRefreshToken = ref('')
const oauthExpiry = ref('')
const oauthRetryButton = ref<HTMLButtonElement | null>(null)
const oauthTokenButton = ref<HTMLButtonElement | null>(null)
const oauthContinueButton = ref<HTMLButtonElement | null>(null)
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
    const next = await boundedRead(api.listConnections())
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
  oauthPopup = window.open(url.toString(), 'railgrid-github-oauth', 'width=720,height=820')
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
  const data = ev.data as { type?: string; state?: string; token?: string; refreshToken?: string; expiry?: string; login?: string; error?: string }
  if (!data || data.type !== 'railgrid-github-oauth') return
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
  oauthRefreshToken.value = typeof data.refreshToken === 'string' ? data.refreshToken : ''
  oauthExpiry.value = typeof data.expiry === 'string' ? data.expiry : ''
  oauthLogin.value = typeof data.login === 'string' ? data.login : ''
  owner.value = oauthLogin.value
  name.value = oauthLogin.value ? 'github-' + oauthLogin.value : 'github'
  fieldErrors.value = {}
  oauthAuthorized.value = true
}

async function submit(): Promise<void> {
  if (!mounted || submitting.value || uncertainName.value || recoveryUnavailable.value) return
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
    refreshToken: oauthAuthorized.value ? oauthRefreshToken.value : '',
    expiry: oauthAuthorized.value ? oauthExpiry.value : '',
    baseURL: baseURL.value || undefined,
    type: isGitHub.value ? 'oauth' as const : 'pat' as const,
  }
  const desiredName = normalizeResourceName(payload.name)
  submitting.value = true
  let mutationStarted = false
  let attemptRecord: string | null = null
  try {
    // The initial read only makes the form usable. Re-read the complete
    // collection immediately before creating to catch known name collisions.
    // The create itself atomically rejects a concurrent name collision.
    const currentConnections = await boundedRead(api.listConnections())
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
    try {
      if (!recoveryStorage || !recoveryKey || recoveryStorage.getItem(recoveryKey)) throw new Error('Storage unavailable or another request is unresolved')
      const bytes = new Uint8Array(16)
      crypto.getRandomValues(bytes)
      recoveryRecord = JSON.stringify({ name: desiredName, id: Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('') })
      recoveryStorage.setItem(recoveryKey, recoveryRecord)
      if (recoveryStorage.getItem(recoveryKey) !== recoveryRecord) throw new Error('Storage unavailable')
    } catch {
      recoveryUnavailable.value = true
      formError.value = 'Cannot save connection recovery information. Enable session storage and reload before connecting.'
      return
    }
    attemptRecord = recoveryRecord
    mutationStarted = true
    // Retain uncertainty even if the caller context rotates while the write is
    // in flight: a stale response cannot prove that the server did not act.
    uncertainName.value = desiredName
    uncertainContext.value = expectedContext
    const created = await api.connect(payload)
    if (!isCurrentMutation(generation, expectedContext)) return
    if (!clearRecovery()) return
    uncertainContext.value = null
    toast('info', `Connection creation requested for ${created.name}.`)
    emit('created', created.name)
  } catch (e) {
    // A pre-dispatch error or a rejected first write proves setup did not start, even
    // when the workspace changed. Do not touch a later mutation's state.
    if (mutationStarted && (isPreDispatchError(e) || isRejectedConnectionWrite(e))) {
      if (mounted && generation === mutationGeneration) {
        if (clearRecovery()) uncertainContext.value = null
        if (contextGeneration.value === expectedContext) formError.value = errMessage(e)
      } else if (!mounted && recoveryStorage && recoveryKey && attemptRecord) {
        // This view is gone, but a definitive rejection settles its own stored
        // attempt. Never remove a record written by a later view.
        try {
          if (recoveryStorage.getItem(recoveryKey) === attemptRecord) recoveryStorage.removeItem(recoveryKey)
        } catch { /* The next view will retain the recovery prompt. */ }
      }
    } else if (isCurrentMutation(generation, expectedContext)) {
      if (mutationStarted) uncertainName.value = desiredName
      else formError.value = errMessage(e)
    }
  } finally {
    if (mounted && generation === mutationGeneration) submitting.value = false
  }
}

async function inspectConnection(): Promise<void> {
  const target = uncertainName.value
  if (!target || inspecting.value || uncertainContext.value !== contextGeneration.value) return
  const generation = mutationGeneration
  const expectedContext = contextGeneration.value
  inspecting.value = true
  try {
    const result = await boundedRead(api.getConnection(target))
    if (!isCurrentMutation(generation, expectedContext) || uncertainName.value !== target) return
    inspected.value = true
    inspection.value = `A connection named “${result.name}” is present for owner “${result.owner}”. ${result.validated ? 'The controller reports it validated.' : 'It is not currently reported as validated.'} Its name does not establish which request created or changed it.`
  } catch {
    try {
      const list = await boundedRead(api.listConnections())
      if (isCurrentMutation(generation, expectedContext) && uncertainName.value === target) {
        inspected.value = true
        inspection.value = list.some(item => item.name === target)
          ? 'Could not inspect this connection. It appears in the list, but its setup remains unconfirmed.'
          : 'Could not inspect this connection. It was not found in the list; the request may still complete later.'
      }
    } catch {
      if (isCurrentMutation(generation, expectedContext) && uncertainName.value === target) inspection.value = 'Could not inspect this connection. Its presence and setup remain unconfirmed.'
    }
  } finally {
    if (isCurrentMutation(generation, expectedContext)) inspecting.value = false
  }
}

function acknowledgeRecovery(): void {
  if (!inspected.value || uncertainContext.value !== contextGeneration.value) return
  if (clearRecovery()) { uncertainContext.value = null; inspected.value = false; inspection.value = null }
}

async function loadOAuthConfig(): Promise<void> {
  const generation = ++oauthGeneration
  const expectedContext = contextGeneration.value
  const focusedRetry = typeof document !== 'undefined' && document.activeElement === oauthRetryButton.value ? oauthRetryButton.value : null
  const focusedContinue = typeof document !== 'undefined' && document.activeElement === oauthContinueButton.value ? oauthContinueButton.value : null
  oauthConfigError.value = null
  oauthLoaded.value = false
  oauthEnabled.value = false
  oauthStartURL.value = ''
  if (focusedRetry) void handoffOAuthFocus(focusedRetry, generation, expectedContext)
  if (focusedContinue) void handoffOAuthFocus(focusedContinue, generation, expectedContext)
  try {
    const config = await boundedRead(api.oauthConfig())
    if (!isCurrentOAuthRead(generation, expectedContext)) return
    const focusedFallback = typeof document !== 'undefined' && document.activeElement === oauthTokenButton.value ? oauthTokenButton.value : null
    oauthEnabled.value = config.enabled
    oauthStartURL.value = config.startURL || ''
    if (focusedFallback && config.enabled) void handoffOAuthFocus(focusedFallback, generation, expectedContext)
  } catch (e) {
    if (!isCurrentOAuthRead(generation, expectedContext)) return
    oauthEnabled.value = false
    oauthStartURL.value = ''
    oauthConfigError.value = errMessage(e)
  } finally {
    if (isCurrentOAuthRead(generation, expectedContext)) oauthLoaded.value = true
  }
}

async function handoffOAuthFocus(previous: HTMLButtonElement, generation: number, expectedContext: number): Promise<void> {
  await nextTick()
  if (!isCurrentOAuthRead(generation, expectedContext)) return
  if (document.activeElement !== previous && document.activeElement !== document.body) return
  ;(oauthContinueButton.value || oauthTokenButton.value)?.focus()
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

watch(contextGeneration, () => {
  if (!mounted) return
  const generation = contextGeneration.value
  inspecting.value = false
  inspection.value = null
  inspected.value = false
  // Scope changes remount this view. Wait for that render before rebinding
  // recovery after a credential or transport refresh in the same scope.
  void nextTick(() => {
    if (mounted && contextGeneration.value === generation && uncertainName.value) uncertainContext.value = generation
  })
  void loadConnections()
  if (isGitHub.value) void loadOAuthConfig()
}, { flush: 'sync' })
</script>

<template>
  <section class="page k-create-page">
    <button class="k-btn k-btn--ghost k-back-action" type="button" :disabled="submitting" @click="cancel">
      <ArrowLeft :size="14" aria-hidden="true" /> Connections
    </button>
    <header class="k-create-header">
      <h2 class="k-create-title">{{ pageTitle }}</h2>
      <p class="k-create-description">Manage GitHub repositories from this workspace.</p>
    </header>

    <div v-if="uncertainName && !submitting" class="error" role="alert">
      <p>The connection request for {{ uncertainName }} could not be confirmed. Setup may be incomplete, and the request may still complete later.</p>
      <p>Inspect the connection before deciding what to do. Credential repair is not available on the connection page; contact your workspace administrator for help.</p>
      <p v-if="uncertainContext !== contextGeneration">The workspace or authentication context changed. Open the original workspace to inspect this request.</p>
      <button class="k-btn k-btn--ghost" type="button" :disabled="inspecting || uncertainContext !== contextGeneration" @click="inspectConnection">{{ inspecting ? 'Checking…' : 'Inspect connection' }}</button>
      <p v-if="inspection" role="status">{{ inspection }}</p>
      <button v-if="inspected" class="k-btn k-btn--ghost" type="button" @click="acknowledgeRecovery">I checked, allow another request</button>
    </div>
    <div v-if="recoveryUnavailable" class="error" role="alert">Connection recovery storage is unavailable or changed. Enable session storage and reload before connecting.</div>
    <div v-if="loading && !loaded" class="panel k-card" role="status" aria-live="polite">Loading connections…</div>
    <div v-if="readError" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ readError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="loadConnections">Retry connections</button>
    </div>

    <div v-if="isGitHub && !oauthAuthorized" class="k-create-surface k-create-surface--guided">
      <div class="k-create-body k-create-body--guided">
        <div class="k-create-fields">
          <span v-if="!oauthLoaded" class="muted" role="status" aria-live="polite">Checking GitHub sign-in…</span>
          <div v-else-if="oauthConfigError" class="error read-error" role="alert" aria-live="assertive">
            <span>GitHub sign-in configuration could not be loaded: {{ oauthConfigError }}</span>
            <button ref="oauthRetryButton" class="k-btn k-btn--ghost" type="button" @click="loadOAuthConfig">Retry GitHub sign-in</button>
          </div>
          <p v-else-if="!oauthEnabled" class="muted">GitHub sign-in is unavailable. You can connect with a personal access token.</p>
          <p v-else class="muted">GitHub opens in a new window. Authorize Railgrid there.</p>
          <p v-if="formError" class="error" role="alert">{{ formError }}</p>
        </div>
        <CreateGuidance
          v-if="oauthLoaded && oauthEnabled"
          title="After authorization"
          description="Review the GitHub owner, then create the connection."
        />
      </div>
      <div class="k-create-actions">
        <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="cancel">Cancel</button>
        <button v-if="!oauthLoaded || oauthConfigError || !oauthEnabled || (!!formError && !oauthBusy)" ref="oauthTokenButton" class="k-btn" :class="oauthEnabled ? 'k-btn--ghost' : 'k-btn--primary'" type="button" @click="emit('use-token')">Use a personal access token</button>
        <button v-if="oauthEnabled" ref="oauthContinueButton" class="k-btn k-btn--primary" :disabled="oauthBusy || !oauthLoaded" type="button" @click="connectGitHub">
          {{ oauthBusy ? 'Waiting for GitHub…' : 'Continue with GitHub' }}
        </button>
      </div>
    </div>

    <form v-if="method === 'token' || oauthAuthorized" class="k-create-surface k-create-surface--guided" :aria-busy="submitting" novalidate @submit.prevent="submit">
      <div class="k-create-body k-create-body--guided">
        <div class="k-create-fields">
          <p v-if="oauthAuthorized" class="muted" role="status">
            GitHub authorized<span v-if="oauthLogin"> as <code>{{ oauthLogin }}</code></span>.
          </p>
          <label class="field" for="code-connection-name"><span class="field-label">Name in Railgrid</span><input id="code-connection-name" ref="nameInput" v-model="name" :readonly="submitting" class="k-input" placeholder="my-github" autocomplete="off" required aria-required="true" :aria-invalid="fieldErrors.name ? 'true' : undefined" :aria-describedby="fieldErrors.name ? 'code-connection-name-error' : undefined" @input="clearFieldError('name')" /><span v-if="fieldErrors.name" id="code-connection-name-error" class="error" role="alert">{{ fieldErrors.name }}</span></label>
          <label class="field" for="code-connection-owner"><span class="field-label">GitHub owner (account or organization)</span><input id="code-connection-owner" ref="ownerInput" v-model="owner" :readonly="submitting" class="k-input" placeholder="acme" autocomplete="off" required aria-required="true" :aria-invalid="fieldErrors.owner ? 'true' : undefined" :aria-describedby="fieldErrors.owner ? 'code-connection-owner-error' : undefined" @input="clearFieldError('owner')" /><span v-if="fieldErrors.owner" id="code-connection-owner-error" class="error" role="alert">{{ fieldErrors.owner }}</span></label>
          <label v-if="!oauthAuthorized" class="field" for="code-connection-token"><span class="field-label">Personal access token</span><input id="code-connection-token" ref="tokenInput" v-model="token" :readonly="submitting" class="k-input" type="password" placeholder="ghp_…" autocomplete="new-password" required aria-required="true" :aria-invalid="fieldErrors.token ? 'true' : undefined" :aria-describedby="fieldErrors.token ? 'code-connection-token-error code-connection-token-hint' : 'code-connection-token-hint'" @input="clearFieldError('token')" /><span v-if="fieldErrors.token" id="code-connection-token-error" class="error" role="alert">{{ fieldErrors.token }}</span></label>
          <div v-if="!oauthAuthorized">
            <p id="code-connection-token-hint" class="muted">Use a token with access to the owner and repositories you want to manage.</p>
            <details class="muted">
              <summary>Token permissions</summary>
              <p>For a fine-grained token, select the resource owner and repositories. Grant <strong>Contents: read and write</strong> for code changes, <strong>Administration: read and write</strong> for repository management, and <strong>Workflows: read and write</strong> to edit GitHub Actions workflows.</p>
              <p>For a classic token, grant <code>repo</code>. Add <code>workflow</code> for GitHub Actions and App Studio builds or <code>delete_repo</code> to delete repositories. Viewing packages requires a classic token with <code>read:packages</code>.</p>
              <p>Check your organization’s token policy for approval and SSO requirements. <a href="https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens" target="_blank" rel="noopener noreferrer">GitHub token setup guide (opens in a new tab)</a></p>
            </details>
          </div>
          <label v-if="!oauthAuthorized" class="field"><span class="field-label">GitHub Enterprise Server API URL (optional)</span><input v-model="baseURL" :readonly="submitting" class="k-input" placeholder="https://github.example.com/api/v3" autocomplete="url" /></label>
          <span v-if="formError" class="error" role="alert">{{ formError }}</span>
          <span v-if="submitting" class="sr-only" role="status" aria-live="polite">Creating connection…</span>
        </div>
        <CreateGuidance
          title="After you connect"
          description="Railgrid stores the credential as a workspace Secret. Check the connection page for validation status."
        />
      </div>
      <div class="k-create-actions">
        <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="cancel">Cancel</button>
        <button class="k-btn k-btn--primary" type="submit" :disabled="submitting || !loaded || !!uncertainName || recoveryUnavailable">{{ submitting ? 'Connecting…' : 'Create connection' }}</button>
      </div>
    </form>
  </section>
</template>
