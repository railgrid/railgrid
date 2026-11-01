<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, Loader2, RefreshCw } from 'lucide-vue-next'
import ModelIDSelector from '../agentkit/ModelIDSelector.vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
import { PROVIDER_PRESETS, presetFor, providerPreset } from '../conn-defs'
import { type Credential, type CredentialWrite, type CredentialTestResult } from '../types'
import type { ApiClient } from '../api'
import { presentModelProbeResult } from '../model-probe-result'
import { confirmDialog } from '../portalkit/confirm'

ensureAgentUIStyles()
const props = defineProps<{ api: ApiClient; credentials: Credential[]; credential?: Credential; selectConnection?: boolean; initialConnection?: string; discoveryFallback?: { connectionName: string; models: string[] } | null; busy: boolean; error?: string | null }>()
const emit = defineEmits<{ save: [body: CredentialWrite, result?: CredentialTestResult]; cancel: []; 'connect-provider': [] }>()
const newID = `connection-${crypto.randomUUID()}`
const connections = computed(() => props.credentials.filter(item => item.isConnection))
const originalConnection = props.credential?.connectionRef
  ? props.credentials.find(item => item.isConnection && item.name === props.credential!.connectionRef)
  : props.credential
const editingProviderConnection = Boolean(props.credential?.isConnection)
const managingConnection = Boolean(props.credential && !props.credential.connectionRef)
const selectedConnection = ref(originalConnection?.name || props.initialConnection || '')
const connection = computed(() => connections.value.find(item => item.name === selectedConnection.value) || (originalConnection?.name === selectedConnection.value ? originalConnection : undefined))
const standaloneConnection = computed(() => !props.credential && !props.initialConnection && !props.selectConnection)
const preset = ref(presetFor(originalConnection).id)
const current = computed(() => providerPreset(preset.value))
const providers = PROVIDER_PRESETS.filter(item => item.family === 'chat')
const baseURL = ref(originalConnection?.baseURL || current.value.baseURL)
const apiKey = ref('')
const workspaceID = ref(originalConnection?.anthropicWorkspaceID || '')
const connectionLabel = ref(originalConnection?.displayName || originalConnection?.name || '')
const suggestedName = computed(() => {
  const brand = preset.value === 'anthropic' ? 'Anthropic' : current.value.label
  const count = connections.value.filter(item => item.name !== newID && presetFor(item).id === preset.value).length
  return brand + (count ? ` ${count + 1}` : '')
})
const model = ref(props.credential?.model || '')
function connectionModelOptions(name: string, persisted: string[] | undefined): string[] {
  if (props.discoveryFallback?.connectionName === name) return props.discoveryFallback.models
  return persisted || []
}
const models = ref<string[]>(connectionModelOptions(originalConnection?.name || props.initialConnection || '', originalConnection?.discovered))
const modelStage = ref(Boolean(props.credential || props.initialConnection))
const alreadyConfigured = computed(() => !props.credential && Boolean(selectedConnection.value) && props.credentials.some(item => (item.connectionRef === selectedConnection.value || item.name === selectedConnection.value) && item.model === model.value.trim()))
const discovering = ref(false)
const testing = ref(false)
const discoveryError = ref('')
const testError = ref('')
const fieldError = ref('')
const operationNotice = ref('')
let activeOperation: AbortController | null = null
const verifiedFingerprint = ref('')
let generation = 0
const locked = computed(() => props.busy || testing.value || discovering.value)
const editingDetails = computed(() => (!props.selectConnection && !selectedConnection.value) || managingConnection)
const effectiveURL = computed(() => editingDetails.value ? baseURL.value.trim() : connection.value?.baseURL || '')
const effectiveProvider = computed(() => editingDetails.value ? originalConnection && preset.value === presetFor(originalConnection).id ? originalConnection.provider || current.value.provider : current.value.provider : connection.value?.provider)
const effectiveWorkspace = computed(() => editingDetails.value ? workspaceID.value.trim() : connection.value?.anthropicWorkspaceID || '')
const isAnthropic = computed(() => { try { return new URL(effectiveURL.value).hostname === 'api.anthropic.com' } catch { return false } })
const canReuse = computed(() => Boolean(connection.value && (!editingDetails.value || (effectiveURL.value === connection.value.baseURL && effectiveProvider.value === (connection.value.provider || current.value.provider) && effectiveWorkspace.value === (connection.value.anthropicWorkspaceID || '')))))
const canContinue = computed(() => Boolean(effectiveURL.value && (canReuse.value || apiKey.value.trim())))
const fingerprint = computed(() => JSON.stringify([selectedConnection.value, effectiveProvider.value, effectiveURL.value, effectiveWorkspace.value, apiKey.value, model.value]))
watch(fingerprint, () => {
  const interrupted = Boolean(activeOperation)
  invalidateOperation()
  verifiedFingerprint.value = ''; testError.value = ''; fieldError.value = ''
  operationNotice.value = interrupted ? 'Connection details changed. Verify again before saving.' : ''
})
watch(selectedConnection, () => {
  apiKey.value = ''; workspaceID.value = ''; model.value = ''; models.value = connectionModelOptions(selectedConnection.value, connection.value?.discovered)
  modelStage.value = Boolean(selectedConnection.value); discoveryError.value = ''
})
watch(preset, () => { apiKey.value = ''; workspaceID.value = ''; baseURL.value = current.value.baseURL; model.value = ''; models.value = []; modelStage.value = false; discoveryError.value = '' })
watch(baseURL, () => { apiKey.value = ''; workspaceID.value = ''; models.value = [] })
watch(apiKey, () => { if (editingDetails.value) models.value = [] })
onBeforeUnmount(() => { invalidateOperation(); apiKey.value = '' })

function invalidateOperation(): void {
  generation++
  activeOperation?.abort()
  activeOperation = null
  testing.value = false
  discovering.value = false
}
function startOperation() {
  const serial = ++generation
  const controller = new AbortController()
  activeOperation = controller
  operationNotice.value = ''
  const timeout = setTimeout(() => controller.abort(new Error('Verification timed out. Check the provider connection and try again.')), 60_000)
  return { serial, signal: controller.signal, finish: () => {
    clearTimeout(timeout)
    if (activeOperation === controller) activeOperation = null
  } }
}
async function awaitProbe(request: () => Promise<CredentialTestResult | undefined>, signal: AbortSignal): Promise<CredentialTestResult | undefined> {
  signal.throwIfAborted()
  let abort!: () => void
  const interrupted = new Promise<never>((_resolve, reject) => {
    abort = () => reject(signal.reason)
    signal.addEventListener('abort', abort, { once: true })
  })
  try { return await Promise.race([request(), interrupted]) }
  finally { signal.removeEventListener('abort', abort) }
}

function draft(): CredentialWrite {
  return { name: originalConnection?.name || newID, displayName: connectionLabel.value.trim() || suggestedName.value, provider: effectiveProvider.value, baseURL: effectiveURL.value, ...(apiKey.value.trim() ? { apiKey: apiKey.value.trim() } : {}), anthropicWorkspaceID: effectiveWorkspace.value, model: model.value.trim() }
}
function valid(): boolean {
  try {
    const url = new URL(effectiveURL.value)
    if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw Error()
  } catch { fieldError.value = 'Enter a valid HTTP or HTTPS API endpoint.'; return false }
  if (!canContinue.value) { fieldError.value = 'Add an API key or choose an existing provider connection.'; return false }
  if (/\s/.test(apiKey.value.trim())) { fieldError.value = 'Paste only the API key, without spaces or line breaks.'; return false }
  if (isAnthropic.value && effectiveWorkspace.value && !/^wrkspc_[A-Za-z0-9]+$/.test(effectiveWorkspace.value)) { fieldError.value = 'Enter a workspace ID beginning with wrkspc_, or leave it blank for a workspace-scoped key.'; return false }
  return true
}
async function probe(discover: boolean, signal: AbortSignal, candidate = model.value.trim()): Promise<CredentialTestResult | undefined> {
  return awaitProbe(() => probeRequest(discover, signal, candidate), signal)
}
async function probeRequest(discover: boolean, signal: AbortSignal, candidate: string): Promise<CredentialTestResult | undefined> {
  if (canReuse.value && !apiKey.value.trim()) return discover
    ? (connection.value?.isConnection ? props.api.discoverProviderConnection(connection.value.name, signal) : props.api.discoverCredential(connection.value!.name, signal))
    : (connection.value?.isConnection ? props.api.testProviderConnection(connection.value.name, candidate, signal) : props.api.testCredential(connection.value!.name, candidate, signal))
  return props.api.probeCredentialDraft({ ...draft(), model: candidate }, discover, signal)
}
async function discover(): Promise<void> {
  if (locked.value || !valid()) return
  const operation = startOperation()
  const { serial, signal } = operation
  discovering.value = true; discoveryError.value = ''; modelStage.value = true
  try {
    const result = await probe(true, signal)
    if (serial !== generation) return
    if (!result?.ok) {
      verifiedFingerprint.value = ''
      discoveryError.value = presentModelProbeResult(result || { ok: false, latencyMS: 0 }, 'discover').error || 'Models could not be loaded.'
      return
    }
    models.value = result.models || []
    if (standaloneConnection.value) verifiedFingerprint.value = fingerprint.value
  } catch (error) { if (serial === generation) { verifiedFingerprint.value = ''; discoveryError.value = (error as Error).message } }
  finally { operation.finish(); if (serial === generation) discovering.value = false }
}
function bindingName(connectionName: string, modelID: string): string {
  let hash = 2166136261
  for (const char of modelID) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619)
  const slug = modelID.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 70) || 'model'
  return `${connectionName}-${slug}-${(hash >>> 0).toString(36)}`.slice(0, 253)
}
async function connect(): Promise<void> {
  if (locked.value || !valid() || alreadyConfigured.value) return
  if (!standaloneConnection.value && !editingProviderConnection && !model.value.trim()) { fieldError.value = 'Choose a model or enter its exact ID.'; return }
  const operation = startOperation()
  const { serial, signal } = operation
  const snapshot = fingerprint.value
  testing.value = true; testError.value = ''
  try {
    let result: CredentialTestResult = { ok: true, latencyMS: 0 }
    const candidates = managingConnection
      ? [...new Set([model.value.trim(), ...props.credentials.filter(item => item.connectionRef === selectedConnection.value).map(item => item.model || '')].filter(Boolean))]
      : (standaloneConnection.value && !model.value.trim() ? [] : [model.value.trim()])
    if (verifiedFingerprint.value !== snapshot) {
      if ((editingProviderConnection || standaloneConnection.value) && !candidates.length) {
        const response = await probe(true, signal)
        if (serial !== generation) return
        if (!response?.ok) {
          verifiedFingerprint.value = ''
          const message = presentModelProbeResult(response || { ok: false, latencyMS: 0 }, 'discover').error || 'Provider access could not be verified.'
          if (standaloneConnection.value) {
            discoveryError.value = `${message} Retry discovery or enter a verification model ID.`
            modelStage.value = true
          } else testError.value = `${message} Choose a model to test instead.`
          return
        }
        result = response
      }
      for (const candidate of candidates) {
        const response = await probe(false, signal, candidate)
        if (serial !== generation) return
        if (!response?.ok) { testError.value = `${candidate}: ${presentModelProbeResult(response || { ok: false, latencyMS: 0 }, 'test').error || 'The model did not respond. Review the connection and try again.'}`; return }
        result = response
      }
    }
    verifiedFingerprint.value = snapshot
    if (standaloneConnection.value) { emit('save', { ...draft(), model: '' }, result); return }
    if (editingProviderConnection) { emit('save', { ...draft(), model: '' }, result); return }
    if (props.credential && !props.credential.connectionRef && props.credential.model) {
      emit('save', draft(), result)
      return
    }
    const parentName = selectedConnection.value || newID
    const existing = props.credentials.find(item => item.connectionRef === parentName && item.model === model.value.trim())
    emit('save', {
      name: props.credential?.connectionRef ? props.credential.name : existing?.name || bindingName(parentName, model.value.trim()),
      connectionRef: parentName, model: model.value.trim(), displayName: model.value.trim(),
      ...(!selectedConnection.value || managingConnection ? { newConnection: { ...draft(), name: parentName, model: '' } } : {}),
    }, result)
  } catch (error) {
    if (serial === generation) {
      if (standaloneConnection.value && !model.value.trim()) {
        discoveryError.value = (error as Error).message
        modelStage.value = true
      } else testError.value = (error as Error).message
    }
  }
  finally { operation.finish(); if (serial === generation) testing.value = false }
}
async function requestProvider(event: MouseEvent): Promise<void> {
  if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button !== 0) return
  event.preventDefault()
  if (locked.value) return
  if (model.value && !await confirmDialog({ title: 'Discard model selection?', message: 'Your unsaved model selection will be lost.', confirmLabel: 'Discard changes' })) return
  emit('connect-provider')
}
async function cancel(): Promise<void> {
  if (props.busy) return
  if (testing.value || discovering.value) {
    const wasTesting = testing.value
    invalidateOperation()
    operationNotice.value = wasTesting ? 'Verification stopped. Your changes have not been saved.' : 'Model loading stopped. You can try again or enter a model ID manually.'
    return
  }
  if ((apiKey.value || model.value !== (props.credential?.model || '')) && !await confirmDialog({ title: 'Discard connection changes?', message: 'Your unsaved changes will be lost.', confirmLabel: 'Discard changes' })) return
  emit('cancel')
}
defineExpose({ cancel, locked })
</script>

<template>
  <form class="k-create-surface k-create-surface--wide k-model-form agents-provider-connect agents-model-create" :aria-label="standaloneConnection ? 'Provider connection form' : 'Model configuration form'" novalidate :aria-busy="locked" @submit.prevent="modelStage || standaloneConnection ? connect() : discover()">
    <div class="k-create-body">
      <section class="agents-provider-connect__section" aria-labelledby="provider-connect-heading">
        <h2 id="provider-connect-heading" class="k-model-form-heading">Provider connection</h2>
        <template v-if="selectConnection && !credential">
          <label v-if="connections.length" for="saved-provider-connection" class="k-model-form-field">Saved provider connection
            <select id="saved-provider-connection" v-model="selectedConnection" class="k-input" :disabled="locked"><option value="" disabled>Select a provider connection</option><option v-for="item in connections" :key="item.name" :value="item.name">{{ item.displayName || item.name }} · {{ presetFor(item).label }}</option></select>
          </label>
          <p v-else class="k-model-form-hint">No saved provider connections yet. Connect a provider to add your first model.</p>
          <p class="k-model-form-hint">Need another provider? <a class="agents-provider-connect__link" href="#/create/model" :aria-disabled="locked || undefined" @click="requestProvider">Connect provider</a></p>
        </template>
        <template v-if="editingDetails">
          <label class="k-model-form-field"><span id="agents-model-provider-label">Provider</span>
            <select id="model-provider" aria-labelledby="agents-model-provider-label" v-model="preset" class="k-input" :disabled="locked"><option v-for="item in providers" :key="item.id" :value="item.id">{{ item.id === 'anthropic' ? 'Anthropic' : item.label }}</option></select>
          </label>
          <label v-if="preset === 'custom'" class="k-model-form-field">API endpoint
            <input id="model-base-url" v-model="baseURL" class="k-input" name="baseURL" type="url" placeholder="https://your-provider.example/v1" :disabled="locked" :aria-invalid="Boolean(fieldError && fieldError.includes('endpoint'))" :aria-describedby="fieldError ? 'provider-url-help provider-connection-error' : 'provider-url-help'">
            <span id="provider-url-help" class="k-model-form-hint">Use an OpenAI-compatible API endpoint. You can enter a model ID if this provider does not list models.</span>
          </label>
          <details v-else class="agents-provider-connect__details"><summary>Connection details</summary><p class="k-model-form-hint">API endpoint: <span class="font-mono">{{ baseURL }}</span></p></details>
        </template>
        <p v-else-if="connection" class="k-model-form-hint">{{ presetFor(connection).label }} · {{ connection?.baseURL }}<br>Uses the key stored with {{ connection?.displayName || connection?.name }}.</p>
      </section>
      <section v-if="editingDetails" class="k-model-form-section" aria-labelledby="provider-key-heading">
        <h2 id="provider-key-heading" class="k-model-form-heading">API key</h2>
        <label for="model-credential" class="sr-only">API key</label>
        <input id="model-credential" v-model="apiKey" class="k-input" type="password" name="apiKey" autocomplete="new-password" :placeholder="canReuse ? 'Leave blank to keep the saved key' : 'Paste your API key'" :disabled="locked" :aria-required="!canReuse" :aria-invalid="Boolean(fieldError && fieldError.includes('key') && !fieldError.includes('workspace'))" :aria-describedby="fieldError ? 'provider-key-help provider-connection-error' : 'provider-key-help'">
        <p id="provider-key-help" class="k-model-form-hint">Stored in this workspace and never returned to the browser. Models on this connection reuse this key. Changing the provider or endpoint clears a typed key.</p>
        <label v-if="isAnthropic" class="k-model-form-field">Anthropic workspace ID (if required by your key)
          <input id="anthropic-workspace-id" v-model="workspaceID" name="anthropicWorkspaceID" class="k-input" placeholder="wrkspc_…" :disabled="locked" :aria-invalid="Boolean(fieldError && fieldError.includes('workspace'))" :aria-describedby="fieldError ? 'provider-workspace-help provider-connection-error' : 'provider-workspace-help'">
          <span id="provider-workspace-help" class="k-model-form-hint">Leave blank for a workspace-scoped key. Otherwise, enter the workspace ID from Claude Console. Re-enter the key when changing its workspace.</span>
        </label>
      </section>
      <p v-if="fieldError" id="provider-connection-error" class="k-inline-notification k-inline-notification--error" role="alert">{{ fieldError }}</p>
      <section v-if="modelStage" class="k-model-form-section" aria-labelledby="provider-model-heading">
        <div class="k-model-form-row"><h2 id="provider-model-heading" class="k-model-form-heading">{{ editingProviderConnection || standaloneConnection ? 'Verify provider access' : 'Choose a model' }}</h2><button type="button" class="k-btn k-btn--ghost" :disabled="locked || !canContinue" aria-describedby="provider-model-help" @click="discover"><RefreshCw :size="14" />{{ discovering ? 'Loading…' : 'Refresh models' }}</button></div>
        <label for="model-id" class="k-model-form-field">{{ editingProviderConnection || standaloneConnection ? 'Verification model (optional)' : 'Model' }}
          <ModelIDSelector :model-value="model" :invalid="Boolean(fieldError && fieldError.includes('model'))" :models="models.map(id => ({ id, name: id, compatibility: 'available' as const }))" :disabled="locked" :described-by="fieldError ? 'provider-model-help provider-connection-error' : 'provider-model-help'" @update:model-value="model = $event" />
        </label>
        <p id="provider-model-help" class="k-model-form-hint">{{ editingProviderConnection ? 'Configured models are verified automatically. An optional test model is used only for verification; no models will be added or changed.' : standaloneConnection ? 'Model discovery checks provider access without adding a model. If listing is unavailable, enter one model ID to verify access; it will not be saved.' : 'Choose an available model or enter its exact ID. You can add more models to this provider connection later.' }}</p>
        <p v-if="discoveryError" class="k-inline-notification k-inline-notification--warning" role="status">{{ discoveryError }}{{ standaloneConnection ? '' : ' You can still enter a model ID manually.' }}</p>
        <label v-if="editingDetails" class="k-model-form-field">Connection name (optional)
          <input v-model="connectionLabel" class="k-input" name="connectionName" :placeholder="suggestedName" :disabled="locked" maxlength="100">
          <span class="k-model-form-hint">For example, “OpenAI production”. This identifies the saved key, not a model.</span>
        </label>
        <p v-if="model && !editingProviderConnection" class="k-model-form-hint">{{ model }} will use {{ connection?.displayName || connectionLabel || suggestedName }}.</p>
        <p v-if="alreadyConfigured" class="k-model-form-hint" role="status">This model is already configured for this connection. Choose another model to add.</p>
        <p v-if="testError" class="k-inline-notification k-inline-notification--error" role="alert">{{ testError }}</p>
        <p v-if="verifiedFingerprint" class="k-model-form-hint" role="status"><Check :size="14" class="inline" /> {{ standaloneConnection ? 'Provider access verified. Save the connection without adding a model.' : editingProviderConnection ? 'Provider access verified. Save the connection changes.' : 'Model verified. Finish connecting to save your changes.' }}</p>
      </section>
      <p v-if="operationNotice" class="k-model-form-hint" role="status">{{ operationNotice }}</p>
      <p v-if="error" class="k-inline-notification k-inline-notification--error" role="alert">{{ error }}</p>
    </div>
    <footer class="k-create-actions agents-provider-connect__actions">
      <p v-if="standaloneConnection && !modelStage" id="provider-verify-help" class="k-model-form-hint agents-provider-connect__charge">Provider discovery checks access without sending a model request. No model will be added.</p>
      <p v-else-if="modelStage" id="provider-verify-help" class="k-model-form-hint agents-provider-connect__charge">{{ standaloneConnection && !model ? 'Verification checks provider access without sending a model request. No model will be added.' : editingProviderConnection && !model && !credentials.some(item => item.connectionRef === selectedConnection && item.model) ? 'Verification checks provider access without sending a model request.' : managingConnection ? 'Verification tests each configured model on this connection. The provider may charge the account linked to the key.' : 'Verification sends a short request using this key. The provider may charge the account linked to the key.' }}</p>
      <button type="button" class="k-btn k-btn--ghost" :disabled="busy" @click="cancel">{{ testing ? 'Stop verification' : discovering ? 'Stop loading' : 'Cancel' }}</button>
      <button v-if="!modelStage" type="submit" class="k-btn k-btn--primary" :disabled="locked || !canContinue"><Loader2 v-if="locked" :size="14" class="animate-spin motion-reduce:animate-none" />{{ testing ? 'Checking provider…' : busy ? 'Saving…' : standaloneConnection ? 'Verify & connect' : 'Continue' }}</button>
      <button v-else type="submit" class="k-btn k-btn--primary" :disabled="locked || !canContinue || (!standaloneConnection && !editingProviderConnection && !model.trim()) || alreadyConfigured" aria-describedby="provider-verify-help"><Loader2 v-if="locked" :size="14" class="animate-spin motion-reduce:animate-none" />{{ testing ? (standaloneConnection && !model.trim() ? 'Checking provider…' : 'Verifying…') : busy ? (standaloneConnection ? 'Saving…' : editingProviderConnection ? 'Saving…' : 'Connecting…') : standaloneConnection ? (verifiedFingerprint ? 'Save provider connection' : model.trim() ? 'Verify & connect' : 'Retry provider check') : editingProviderConnection ? (verifiedFingerprint ? 'Save changes' : 'Verify & save') : verifiedFingerprint ? 'Finish connecting' : 'Verify & connect' }}</button>
    </footer>
  </form>
</template>

<style scoped>
.agents-provider-connect .agents-provider-connect__link{color:var(--color-accent);text-decoration:underline;text-underline-offset:3px;}
.agents-provider-connect .agents-provider-connect__link[aria-disabled="true"]{opacity:.6;cursor:default;}
.agents-provider-connect__section{display:grid;gap:12px;min-width:0;}
.agents-provider-connect__details{font-size:12px;color:var(--color-text-secondary);}
.agents-provider-connect__details summary{cursor:pointer;}
.agents-provider-connect__details p{margin-top:8px;overflow-wrap:anywhere;}
.agents-provider-connect__charge{flex:1 1 100%;max-width:75ch;}
@media(max-width:620px){.agents-provider-connect__actions{position:static;}}
</style>
