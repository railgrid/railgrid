<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, Loader2, RefreshCw } from 'lucide-vue-next'
import ModelIDSelector from '../agentkit/ModelIDSelector.vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
import { PROVIDER_PRESETS, presetFor, providerPreset } from '../conn-defs'
import { isHarnessProvider, type Credential, type CredentialWrite, type CredentialTestResult } from '../types'
import type { ApiClient } from '../api'
import { presentModelProbeResult } from '../model-probe-result'
import { confirmDialog } from '../portalkit/confirm'

ensureAgentUIStyles()
const props = defineProps<{ api: ApiClient; credentials: Credential[]; credential?: Credential; selectConnection?: boolean; initialConnection?: string; busy: boolean; error?: string | null }>()
const emit = defineEmits<{ save: [body: CredentialWrite, result?: CredentialTestResult]; cancel: []; 'connect-provider': [] }>()
const newID = `connection-${crypto.randomUUID()}`
const connections = computed(() => props.credentials.filter(item => !item.connectionRef && !isHarnessProvider(item.provider)))
const originalConnection = props.credential?.connectionRef
  ? props.credentials.find(item => item.name === props.credential!.connectionRef)
  : props.credential
const managingConnection = Boolean(props.credential && !props.credential.connectionRef)
const selectedConnection = ref(originalConnection?.name || props.initialConnection || '')
const connection = computed(() => connections.value.find(item => item.name === selectedConnection.value))
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
const models = ref<string[]>(originalConnection?.discovered || [])
const modelStage = ref(Boolean(props.credential || props.initialConnection))
const alreadyConfigured = computed(() => !props.credential && Boolean(selectedConnection.value) && props.credentials.some(item => (item.connectionRef === selectedConnection.value || item.name === selectedConnection.value) && item.model === model.value.trim()))
const discovering = ref(false)
const testing = ref(false)
const discoveryError = ref('')
const testError = ref('')
const fieldError = ref('')
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
watch(fingerprint, () => { generation++; verifiedFingerprint.value = ''; testError.value = ''; fieldError.value = '' })
watch(selectedConnection, () => {
  apiKey.value = ''; workspaceID.value = ''; model.value = ''; models.value = connection.value?.discovered || []
  modelStage.value = Boolean(selectedConnection.value); discoveryError.value = ''
})
watch(preset, () => { apiKey.value = ''; workspaceID.value = ''; baseURL.value = current.value.baseURL; model.value = ''; models.value = []; modelStage.value = false; discoveryError.value = '' })
watch(baseURL, () => { apiKey.value = ''; workspaceID.value = ''; models.value = [] })
watch(apiKey, () => { if (editingDetails.value) models.value = [] })
onBeforeUnmount(() => { generation++; apiKey.value = '' })

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
async function probe(discover: boolean, candidate = model.value.trim()): Promise<CredentialTestResult | undefined> {
  if (canReuse.value && !apiKey.value.trim()) return discover
    ? props.api.discoverCredential(connection.value!.name)
    : props.api.testCredential(connection.value!.name, candidate)
  return props.api.probeCredentialDraft({ ...draft(), model: candidate }, discover)
}
async function discover(): Promise<void> {
  if (locked.value || !valid()) return
  const serial = ++generation
  discovering.value = true; discoveryError.value = ''; modelStage.value = true
  try {
    const result = await probe(true)
    if (serial !== generation) return
    if (!result?.ok) { discoveryError.value = presentModelProbeResult(result || { ok: false, latencyMS: 0 }, 'discover').error || 'Models could not be loaded. Enter a model ID manually.'; return }
    models.value = result.models || []
  } catch (error) { if (serial === generation) discoveryError.value = (error as Error).message }
  finally { if (serial === generation) discovering.value = false }
}
function bindingName(connectionName: string, modelID: string): string {
  let hash = 2166136261
  for (const char of modelID) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619)
  const slug = modelID.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 70) || 'model'
  return `${connectionName}-${slug}-${(hash >>> 0).toString(36)}`.slice(0, 253)
}
async function connect(): Promise<void> {
  if (locked.value || !valid() || alreadyConfigured.value) return
  if (!model.value.trim()) { fieldError.value = 'Choose a model or enter its exact ID.'; return }
  const serial = ++generation
  const snapshot = fingerprint.value
  testing.value = true; testError.value = ''
  try {
    let result: CredentialTestResult = { ok: true, latencyMS: 0 }
    const candidates = managingConnection
      ? [...new Set([model.value.trim(), ...props.credentials.filter(item => item.connectionRef === selectedConnection.value).map(item => item.model || '').filter(Boolean)])]
      : [model.value.trim()]
    if (verifiedFingerprint.value !== snapshot) {
      for (const candidate of candidates) {
        const response = await probe(false, candidate)
        if (serial !== generation) return
        if (!response?.ok) { testError.value = `${candidate}: ${presentModelProbeResult(response || { ok: false, latencyMS: 0 }, 'test').error || 'The model did not respond. Review the connection and try again.'}`; return }
        result = response
      }
    }
    verifiedFingerprint.value = snapshot
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
  } catch (error) { if (serial === generation) testError.value = (error as Error).message }
  finally { if (serial === generation) testing.value = false }
}
async function requestProvider(event: MouseEvent): Promise<void> {
  if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button !== 0) return
  event.preventDefault()
  if (locked.value) return
  if (model.value && !await confirmDialog({ title: 'Discard model selection?', message: 'Your unsaved model selection will be lost.', confirmLabel: 'Discard changes' })) return
  emit('connect-provider')
}
async function cancel(): Promise<void> {
  if (locked.value) return
  if ((apiKey.value || model.value !== (props.credential?.model || '')) && !await confirmDialog({ title: 'Discard connection changes?', message: 'Your unsaved changes will be lost.', confirmLabel: 'Discard changes' })) return
  emit('cancel')
}
defineExpose({ cancel, locked })
</script>

<template>
  <form class="k-create-surface k-create-surface--wide k-model-form k-provider-connect agents-model-create" aria-label="Model configuration form" novalidate :aria-busy="locked" @submit.prevent="modelStage ? connect() : discover()">
    <div class="k-create-body">
      <section class="k-provider-connect__section" aria-labelledby="provider-connect-heading">
        <h2 id="provider-connect-heading" class="k-model-form-heading">Provider connection</h2>
        <template v-if="selectConnection && !credential">
          <label v-if="connections.length" for="saved-provider-connection" class="k-model-form-field">Saved provider connection
            <select id="saved-provider-connection" v-model="selectedConnection" class="k-input" :disabled="locked"><option value="" disabled>Select a provider connection</option><option v-for="item in connections" :key="item.name" :value="item.name">{{ item.displayName || item.name }} · {{ presetFor(item).label }}</option></select>
          </label>
          <p v-else class="k-model-form-hint">No saved provider connections yet. Connect a provider to add your first model.</p>
          <p class="k-model-form-hint">Need another provider? <a class="k-text-link" href="#/create/model" :aria-disabled="locked || undefined" @click="requestProvider">Connect provider</a></p>
        </template>
        <template v-if="editingDetails">
          <label class="k-model-form-field"><span id="agents-model-provider-label">Provider</span>
            <select id="model-provider" aria-labelledby="agents-model-provider-label" v-model="preset" class="k-input" :disabled="locked"><option v-for="item in providers" :key="item.id" :value="item.id">{{ item.id === 'anthropic' ? 'Anthropic' : item.label }}</option></select>
          </label>
          <label v-if="preset === 'custom'" class="k-model-form-field">API endpoint
            <input id="model-base-url" v-model="baseURL" class="k-input" name="baseURL" type="url" placeholder="https://your-provider.example/v1" :disabled="locked" :aria-invalid="Boolean(fieldError && fieldError.includes('endpoint'))" :aria-describedby="fieldError ? 'provider-url-help provider-connection-error' : 'provider-url-help'">
            <span id="provider-url-help" class="k-model-form-hint">Use an OpenAI-compatible API endpoint. You can enter a model ID if this provider does not list models.</span>
          </label>
          <details v-else class="k-provider-connect__details"><summary>Connection details</summary><p class="k-model-form-hint">API endpoint: <span class="font-mono">{{ baseURL }}</span></p></details>
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
        <div class="k-model-form-row"><h2 id="provider-model-heading" class="k-model-form-heading">Choose a model</h2><button type="button" class="k-btn k-btn--ghost" :disabled="locked || !canContinue" aria-describedby="provider-model-help" @click="discover"><RefreshCw :size="14" />{{ discovering ? 'Loading…' : 'Refresh models' }}</button></div>
        <label for="model-id" class="k-model-form-field">Model
          <ModelIDSelector :model-value="model" :invalid="Boolean(fieldError && fieldError.includes('model'))" :models="models.map(id => ({ id, name: id, compatibility: 'available' as const }))" :disabled="locked" :described-by="fieldError ? 'provider-model-help provider-connection-error' : 'provider-model-help'" @update:model-value="model = $event" />
        </label>
        <p id="provider-model-help" class="k-model-form-hint">Choose an available model or enter its exact ID. You can add more models to this provider connection later.</p>
        <p v-if="discoveryError" class="k-inline-notification k-inline-notification--warning" role="status">{{ discoveryError }} You can still enter a model ID manually.</p>
        <label v-if="editingDetails" class="k-model-form-field">Connection name (optional)
          <input v-model="connectionLabel" class="k-input" name="connectionName" :placeholder="suggestedName" :disabled="locked" maxlength="100">
          <span class="k-model-form-hint">For example, “OpenAI production”. This identifies the saved key, not a model.</span>
        </label>
        <p v-if="model" class="k-model-form-hint">{{ model }} will use {{ connection?.displayName || connectionLabel || suggestedName }}.</p>
        <p v-if="alreadyConfigured" class="k-model-form-hint" role="status">This model is already configured for this connection. Choose another model to add.</p>
        <p v-if="testError" class="k-inline-notification k-inline-notification--error" role="alert">{{ testError }}</p>
        <p v-if="verifiedFingerprint" class="k-model-form-hint" role="status"><Check :size="14" class="inline" /> Model verified. Finish connecting to save your changes.</p>
      </section>
      <p v-if="error" class="k-inline-notification k-inline-notification--error" role="alert">{{ error }}</p>
    </div>
    <footer class="k-create-actions">
      <p v-if="modelStage" id="provider-verify-help" class="k-model-form-hint k-provider-connect__charge">{{ managingConnection ? 'Verification tests each configured model on this connection.' : 'Verification sends a short request using this key.' }} The provider may charge the account linked to the key.</p>
      <button type="button" class="k-btn k-btn--ghost" :disabled="locked" @click="cancel">Cancel</button>
      <button v-if="!modelStage" type="submit" class="k-btn k-btn--primary" :disabled="locked || !canContinue"><Loader2 v-if="discovering" :size="14" class="animate-spin motion-reduce:animate-none" />{{ discovering ? 'Loading models…' : 'Continue' }}</button>
      <button v-else type="submit" class="k-btn k-btn--primary" :disabled="locked || !canContinue || !model.trim() || alreadyConfigured" aria-describedby="provider-verify-help"><Loader2 v-if="locked" :size="14" class="animate-spin motion-reduce:animate-none" />{{ testing ? 'Verifying…' : busy ? 'Connecting…' : verifiedFingerprint ? 'Finish connecting' : 'Verify & connect' }}</button>
    </footer>
  </form>
</template>

<style scoped>
.k-provider-connect .k-text-link{color:var(--color-accent);text-decoration:underline;text-underline-offset:3px;}
.k-provider-connect .k-text-link[aria-disabled="true"]{opacity:.6;cursor:default;}
.k-provider-connect__section{display:grid;gap:12px;min-width:0;}
.k-provider-connect__details{font-size:12px;color:var(--color-text-secondary);}
.k-provider-connect__details summary{cursor:pointer;}
.k-provider-connect__details p{margin-top:8px;overflow-wrap:anywhere;}
.k-provider-connect .k-btn--primary:disabled{color:var(--color-text-secondary);background:var(--color-surface-overlay);box-shadow:none;opacity:1;}
.k-provider-connect__charge{flex:1 1 100%;max-width:75ch;}
@media(max-width:620px){.k-provider-connect .k-create-actions{position:static;}}
</style>
