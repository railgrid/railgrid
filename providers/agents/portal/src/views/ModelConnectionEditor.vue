<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { ApiClient } from '../api'
import type { Credential, CredentialWrite, CredentialTestResult } from '../types'
import { PROVIDER_PRESETS } from '../conn-defs'
import ModelConnectionForm from '../agentkit/ModelConnectionForm.vue'
import { confirmDialog } from '../portalkit/confirm'

const props = defineProps<{ api: ApiClient; credential?: Credential; busy: boolean; error?: string | null }>()
const emit = defineEmits<{ save: [body: CredentialWrite, result: CredentialTestResult]; cancel: [] }>()
const name = ref(props.credential?.name || '')
const provider = props.credential?.provider || 'openai-compatible'
const baseURL = ref(props.credential?.baseURL || PROVIDER_PRESETS[0].baseURL)
const model = ref(props.credential?.model || '')
const apiKey = ref('')
const preset = ref(PROVIDER_PRESETS.find(item => item.baseURL === baseURL.value)?.id || 'custom')
const options = PROVIDER_PRESETS.map(item => ({ value: item.id, label: item.label }))
const models = ref<string[]>([])
const discovering = ref(false)
const testing = ref(false)
const discoveryError = ref('')
const testError = ref('')
const validationError = ref('')
const testedFingerprint = ref('')
const testResult = ref<CredentialTestResult>({ ok: false, latencyMS: 0 })
let generation = 0
const fingerprint = computed(() => JSON.stringify([provider, baseURL.value.trim(), model.value.trim(), apiKey.value.trim()]))
const baseline = fingerprint.value
const verified = computed(() => testedFingerprint.value === fingerprint.value)
const baseURLError = computed(() => preset.value === 'custom' && !baseURL.value.trim() ? 'Base URL is required.' : '')
const locked = computed(() => props.busy || testing.value || discovering.value)
const discoveredModels = computed(() => models.value.map(id => ({ id, name: id, compatibility: 'available' as const })))
const credentialChanged = computed(() => !props.credential || baseURL.value.replace(/\/+$/, '') !== (props.credential.baseURL || PROVIDER_PRESETS[0].baseURL).replace(/\/+$/, ''))
const providerGuidance = computed(() => preset.value === 'openai'
  ? 'Uses OpenAI’s standard API endpoint.'
  : 'Use a provider or gateway that implements OpenAI Chat Completions and GET /models.')
watch(fingerprint, () => { generation++; testedFingerprint.value = ''; testError.value = ''; validationError.value = '' })
watch([baseURL, apiKey], () => { models.value = []; discoveryError.value = '' })
onBeforeUnmount(() => { generation++; apiKey.value = '' })
function draft() { return { provider, baseURL: baseURL.value.trim(), model: model.value.trim(), apiKey: apiKey.value.trim(), ...(props.credential ? { existingName: props.credential.name } : {}) } }
function valid(requireModel: boolean): boolean {
 validationError.value = ''
 try { const url = new URL(baseURL.value); if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw new Error() }
 catch { validationError.value = 'Enter a valid HTTP or HTTPS API endpoint.' }
 if (!apiKey.value.trim() && (credentialChanged.value || props.credential?.hasAPIKey === false)) validationError.value = 'Enter an API key for this endpoint.'
 if (requireModel && !model.value.trim()) validationError.value = 'Choose or enter a model ID.'
 return !validationError.value
}
async function probe(discover: boolean) {
 if (locked.value || !valid(!discover)) return
 const serial = ++generation
 const snapshot = fingerprint.value
 if (discover) { discovering.value = true; discoveryError.value = '' } else { testing.value = true; testError.value = ''; testedFingerprint.value = '' }
 try {
  const result = await (discover ? props.api.discoverCredentialDraft(draft()) : props.api.testCredentialDraft(draft()))
  if (serial !== generation || snapshot !== fingerprint.value) return
  if (!result.ok) throw new Error(result.error || 'The provider did not confirm this connection.')
  if (discover) models.value = result.models || []
  else { testedFingerprint.value = snapshot; testResult.value = result }
 } catch (error) {
  if (serial !== generation) return
  if (discover) discoveryError.value = (error as Error).message
  else testError.value = (error as Error).message
 } finally { if (serial === generation) { discovering.value = false; testing.value = false } }
}
function submit() {
 if (locked.value || !verified.value || !valid(true)) return
 if (!props.credential && !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(name.value.trim())) { validationError.value = 'Use lowercase letters and numbers separated by hyphens for the name.'; return }
 emit('save', { name: name.value.trim(), provider, baseURL: baseURL.value.trim(), model: model.value.trim(), ...(apiKey.value.trim() ? { apiKey: apiKey.value.trim() } : {}) }, testResult.value)
}
async function cancel() {
 if (locked.value) return
 if ((fingerprint.value !== baseline || name.value !== (props.credential?.name || '')) && !await confirmDialog({ title: 'Discard model changes?', message: 'Your unsaved model connection changes will be lost.', confirmLabel: 'Discard changes' })) return
 emit('cancel')
}
defineExpose({ cancel, locked })
</script>
<template>
 <ModelConnectionForm
  class="agents-model-create"
  :name="name"
  :name-disabled="Boolean(credential)"
  name-pattern="[a-z0-9]+(-[a-z0-9]+)*"
  :provider-preset="preset"
  :provider-options="options"
  provider-label-id="agents-model-provider-label"
  :provider-guidance="providerGuidance"
  :base-u-r-l="baseURL"
  :base-u-r-l-error="baseURLError"
  :credential="apiKey"
  credential-label="API key"
  :credential-required="credentialChanged"
  :credential-placeholder="credential ? 'API key (leave blank to keep current)' : 'API key'"
  :credential-hint="credential ? 'The saved key can only be reused with the same provider endpoint.' : 'Stored for this workspace and never returned to the browser.'"
  :model="model"
  model-hint="Use the exact model identifier shown by your provider."
  :discovered-models="discoveredModels"
  :discovery-loading="discovering"
  :discovery-error="discoveryError"
  :discover-disabled="!apiKey.trim() && (credentialChanged || credential?.hasAPIKey === false)"
  discover-disabled-reason="Enter an API key before finding models."
  :testing="testing"
  :test-error="testError"
  :form-error="validationError || error"
  :connection-tested="verified"
  :test-disabled="!model.trim()"
  :save-disabled="!verified"
  :busy="busy"
  :editing="Boolean(credential)"
  wide
  @update:name="name = $event"
  @update:provider="preset = $event; baseURL = PROVIDER_PRESETS.find(item => item.id === $event)?.baseURL ?? baseURL"
  @update:base-u-r-l="baseURL = $event"
  @update:credential="apiKey = $event"
  @update:model="model = $event"
  @select-model="model = $event.id"
  @discover="probe(true)"
  @test="probe(false)"
  @cancel="cancel"
  @save="submit"
 />
</template>
