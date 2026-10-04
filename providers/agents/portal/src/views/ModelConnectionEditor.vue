<script setup lang="ts">
// The model connection editor writes either of the two families a
// ModelCredential can be, and WHICH ONE is the first question it asks.
//
// A CHAT ENDPOINT (openai, openai-compatible) is a two-step form, and the step
// boundary is a SAVE.
//
// Both probes — "Find models" and "Test connection" — are verbs on a saved
// ModelCredential, so the credential is written first and asked about second.
// That ordering is not a limitation to work around; it is what makes the
// first-run flow work at all. The probes used to hang off an arbitrary Agent,
// which meant the very first thing a person does in an empty workspace —
// connect a model — had nothing to run the probe as, and the form had to
// explain that it could not test what the person had just typed.
//
// So: name + endpoint + key → Save → the endpoint is asked what it serves →
// pick a model → Save. A credential with no model is a legitimate halfway
// state (its reconciler will still report whether the key works), and the
// editor stays open on it rather than pretending the job is done.
//
// A HARNESS IDENTITY (claude-code, codex) is one step, because there is
// nothing to ask anything: no endpoint, no /models, no chat model. Its whole
// content is which login it is and the login itself — and for this family the
// Secret KEY is the kind declaration, so the choice of key is what gets
// written, not spec.secretKey. It gets its own form (HarnessCredentialForm)
// rather than a mode of this one, so the controls that cannot mean anything
// are absent instead of disabled.
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { ApiClient } from '../api'
import {
  HARNESS_SECRET_KEY_CODEX_AUTH,
  HARNESS_SECRET_KEY_OAUTH_TOKEN,
  harnessSecretKeys,
  isJSONObject,
  type Credential,
  type CredentialWrite,
  type CredentialTestResult,
  type HarnessSecretKey,
  type ModelInfo,
} from '../types'
import { DEFAULT_PROVIDER_PRESET, PROVIDER_PRESETS, presetFor, providerPreset } from '../conn-defs'
import ModelConnectionForm from '../agentkit/ModelConnectionForm.vue'
import HarnessCredentialForm from '../components/HarnessCredentialForm.vue'
import { confirmDialog } from '../portalkit/confirm'

// catalog is the curated reference list the Models view already loaded. It is
// passed in rather than fetched again, and it is optional: without it every
// discovered model is simply "available" and the picker shows one flat group.
const props = defineProps<{ api: ApiClient; credential?: Credential; busy: boolean; error?: string | null; catalog?: ModelInfo[] }>()
const emit = defineEmits<{
  save: [body: CredentialWrite, result?: CredentialTestResult]
  cancel: []
  'family-change': [family: 'chat' | 'harness']
}>()
const name = ref(props.credential?.name || '')
const editorForm = ref<{ $el: HTMLFormElement } | null>(null)
// The preset IS the provider choice now: it carries spec.provider and the
// family, so picking one changes which form exists rather than only which URL
// is prefilled.
const preset = ref(presetFor(props.credential).id)
const current = computed(() => providerPreset(preset.value))
const provider = computed(() => current.value.provider)
const harness = computed(() => current.value.family === 'harness')
const baseURL = ref(props.credential?.baseURL || DEFAULT_PROVIDER_PRESET.baseURL)
const model = ref(props.credential?.model || '')
const apiKey = ref('')
// A stored credential keeps its family. Converting a chat endpoint into a
// harness identity would mean rewriting its Secret into a different shape and
// unsaying spec.secretKey, and the honest way to do that is a new credential —
// so an edit offers only the presets of the family it already is.
const storedFamily = props.credential ? presetFor(props.credential).family : null
const options = computed(() => PROVIDER_PRESETS
  .filter(item => !storedFamily || item.family === storedFamily)
  .map(item => ({ value: item.id, label: item.label })))
// A saved credential arrives with whatever its reconciler already discovered,
// so the picker is populated before anyone clicks anything.
const models = ref<string[]>([...(props.credential?.discovered || [])])
const discovering = ref(false)
const testing = ref(false)
const discoveryError = ref('')
const testError = ref('')
const nameError = ref('')
const endpointValidationError = ref('')
const credentialError = ref('')
const modelError = ref('')
const harnessSecretError = ref('')
const testedFingerprint = ref('')
const testResult = ref<CredentialTestResult>({ ok: false, latencyMS: 0 })
let generation = 0
let providerFocusGeneration = 0

// ---- harness identity ------------------------------------------------------
//
// The stored Secret is never read back (that is the point of keeping the key
// off the object), so an edit cannot know WHICH of claude-code's two keys is
// in there. It does not have to: a blank input writes nothing at all, and a
// typed one declares its own kind.
const harnessKeys = computed(() => harnessSecretKeys(provider.value))
const harnessKey = ref<HarnessSecretKey>(harnessSecretKeys(presetFor(props.credential).provider)[0] ?? HARNESS_SECRET_KEY_OAUTH_TOKEN)
const harnessSecret = ref('')
const codex = computed(() => harnessKey.value === HARNESS_SECRET_KEY_CODEX_AUTH)

// saved is the step boundary: an unsaved credential has no object for a verb
// to be addressed at, so neither probe can run yet.
const saved = computed(() => Boolean(props.credential))
const SAVE_FIRST_NOTICE = 'Save this connection first. Finding models and testing run against the saved credential, so nothing has to hold your key in the browser.'
const PICK_MODEL_NOTICE = 'Connection saved. Find models to see which chat models this endpoint serves, pick one, test it, then save again.'
// A model the endpoint lists is not a model it will answer chat on: a picked
// id used to be exercised for the first time by the first agent run, which is
// where the provider's "use the responses endpoint instead" 404 turned up.
const TEST_MODEL_NOTICE = 'Test this model before saving. The endpoint listing a model is not a promise that it answers chat requests.'
const CODEX_JSON_NOTICE = 'This is not a JSON object. Paste the contents of the auth.json file created by codex login.'

const savedModel = computed(() => (props.credential?.model || '').trim())
// A model that differs from what is stored is an unproven choice. Rotating the
// key or changing nothing else is not: those were already verified for this
// model, and re-testing them is a charge for no new information.
const modelChanged = computed(() => saved.value && model.value.trim() !== savedModel.value)
const fingerprint = computed(() => JSON.stringify([
  provider.value, baseURL.value.trim(), model.value.trim(), apiKey.value.trim(),
  harnessKey.value, harnessSecret.value.trim(),
]))
const baseline = fingerprint.value
const verified = computed(() => testedFingerprint.value === fingerprint.value)
const baseURLError = computed(() => endpointValidationError.value || (preset.value === 'custom' && !baseURL.value.trim() ? 'Base URL is required.' : ''))
const locked = computed(() => props.busy || testing.value || discovering.value)
// status.models is already curated to the chat-capable subset by the provider
// (llm.FilterChatModels), so nothing here has to hide anything — what is left
// is telling the curated families apart from whatever else the gateway carries.
// catalogKnown mirrors the backend's LookupModel: exact id first, then the
// longest catalog id the discovered id starts with, both after stripping a
// provider prefix ("openai/gpt-4o", "models/gemini-2.5-pro").
function catalogKnown(id: string): boolean {
  const normalized = id.toLowerCase().trim().replace(/^.*\//, '')
  return (props.catalog || []).some(item => normalized === item.id || normalized.startsWith(item.id))
}
const discoveredModels = computed(() => models.value.map(id => ({
  id,
  name: id,
  compatibility: (catalogKnown(id) ? 'recommended' : 'available') as 'recommended' | 'available',
})))
// A changed endpoint invalidates the stored key: it was issued for the old one
// and must not be sent to a new one.
const credentialChanged = computed(() => !props.credential || baseURL.value.replace(/\/+$/, '') !== (props.credential.baseURL || DEFAULT_PROVIDER_PRESET.baseURL).replace(/\/+$/, ''))
const needsKey = computed(() => credentialChanged.value || props.credential?.secretResolved === false)
const providerGuidance = computed(() => current.value.guidance)

// A harness identity's secret is required when there is nothing stored to
// keep, or when what IS stored is a login for a different harness — switching
// claude-code to codex makes the old value the wrong kind, not a fallback.
const harnessProviderChanged = computed(() => !props.credential || provider.value !== (props.credential.provider || ''))
const needsHarnessSecret = computed(() => harnessProviderChanged.value || props.credential?.secretResolved === false)
const harnessHint = computed(() => {
  const storage = 'Your credential is stored as a workspace secret and never returned to the browser.'
  if (props.credential && !needsHarnessSecret.value) return `${storage} Leave blank to keep the current login, or enter a new value to replace it.`
  if (codex.value) return `${storage} Paste the auth.json contents created by codex login on a machine you control.`
  return `${storage} Enter the selected credential type for this Claude Code identity.`
})

function updateProvider(nextPreset: string): void {
  const activeSelect = document.activeElement instanceof HTMLSelectElement && document.activeElement.id === 'model-provider'
    ? document.activeElement
    : null
  const currentForm = editorForm.value?.$el
  const ownedSelectHadFocus = Boolean(activeSelect && currentForm?.contains(activeSelect))
  const serial = ++providerFocusGeneration
  const nextProvider = providerPreset(nextPreset)
  if (nextProvider.family === 'chat') baseURL.value = nextProvider.baseURL
  preset.value = nextPreset
  if (!ownedSelectHadFocus) return

  // Changing families replaces the whole form and its native select. Restore
  // focus only when that select owned focus and Vue's replacement left focus
  // on the document body; a later selection or user focus move wins.
  void nextTick(() => {
    if (serial !== providerFocusGeneration || preset.value !== nextPreset || document.activeElement !== document.body) return
    editorForm.value?.$el.querySelector<HTMLSelectElement>('#model-provider')?.focus()
  })
}

// The notice explains what the disabled buttons are waiting for, in the order
// a person meets it: save, then choose a model.
const notice = computed(() => {
  if (!saved.value) return SAVE_FIRST_NOTICE
  if (!model.value.trim()) return PICK_MODEL_NOTICE
  if (modelChanged.value && !verified.value) return TEST_MODEL_NOTICE
  return ''
})

// An unsaved form saves what it has; a saved one must carry a model, because
// that is the field an agent actually runs on. A harness identity has neither
// an endpoint nor a model to require.
const saveDisabled = computed(() => {
  if (!name.value.trim()) return true
  if (harness.value) return needsHarnessSecret.value && !harnessSecret.value.trim()
  return !baseURL.value.trim() ||
    (needsKey.value && !apiKey.value.trim()) ||
    (saved.value && !model.value.trim()) ||
    (modelChanged.value && !verified.value)
})

watch(fingerprint, () => {
  generation++
  testedFingerprint.value = ''
  testError.value = ''
  endpointValidationError.value = ''
  credentialError.value = ''
  modelError.value = ''
  harnessSecretError.value = ''
})
watch(name, () => { nameError.value = '' })
// A changed endpoint or key means the discovered list belongs to something
// else. A changed model does not: it was picked FROM that list.
watch([baseURL, apiKey], () => { models.value = []; discoveryError.value = '' })
// A secret is issued for ONE identity. Crossing between the families — an API
// key for an endpoint and a login for a harness are not the same kind of thing
// at all — must not carry the typed value across.
watch(() => current.value.family, family => {
  emit('family-change', family)
  apiKey.value = ''
  harnessSecret.value = ''
}, { immediate: true })
// Each harness provider accepts its own keys, so switching provider moves the
// selection to a key this one actually has.
watch(harnessKeys, keys => { if (keys.length && !keys.includes(harnessKey.value)) harnessKey.value = keys[0] })
// Switching claude-code's credential type is a different identity too, so the
// box starts empty rather than re-labelling what was typed for the other kind.
watch(harnessKey, () => { harnessSecret.value = '' })
onBeforeUnmount(() => { generation++; providerFocusGeneration++; apiKey.value = ''; harnessSecret.value = '' })

function valid(requireModel: boolean): boolean {
  endpointValidationError.value = ''
  credentialError.value = ''
  modelError.value = ''
  let ok = true
  try { const url = new URL(baseURL.value); if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw new Error() }
  catch { endpointValidationError.value = 'Enter a valid HTTP or HTTPS API endpoint.'; ok = false }
  if (!apiKey.value.trim() && needsKey.value) { credentialError.value = 'Enter an API key for this endpoint.'; ok = false }
  // No API key format is known here (any OpenAI-compatible gateway), but none
  // contains whitespace. A pasted sentence — a copied error banner, a line of
  // a config file — is saved verbatim otherwise and only fails later as a 401.
  else if (/\s/.test(apiKey.value.trim())) { credentialError.value = 'The API key contains spaces or line breaks; paste only the key.'; ok = false }
  if (requireModel && !model.value.trim()) { modelError.value = 'Choose or enter a model ID.'; ok = false }
  // The button is disabled for this too, but a form can also be submitted with
  // Enter, and an unproven model is exactly what this change exists to stop
  // from being written.
  else if (modelChanged.value && !verified.value) { modelError.value = TEST_MODEL_NOTICE; ok = false }
  return ok
}

// validHarness checks the SHAPE of a harness login before anything is sent —
// the same checks llm.ReadHarnessSecret makes on the other side, made here so
// a half-pasted auth.json is a message under the box rather than a condition
// on an object several minutes later.
function validHarness(): boolean {
  harnessSecretError.value = ''
  const value = harnessSecret.value.trim()
  if (!value) {
    if (needsHarnessSecret.value) {
      harnessSecretError.value = codex.value
        ? 'Paste the contents of the auth.json file created by codex login.'
        : 'Enter the selected credential for this identity.'
    }
    return !harnessSecretError.value
  }
  if (codex.value) {
    if (!isJSONObject(value)) harnessSecretError.value = CODEX_JSON_NOTICE
  } else if (/\s/.test(value)) {
    harnessSecretError.value = 'The credential contains spaces or line breaks; paste only the value.'
  }
  return !harnessSecretError.value
}

// probe runs one of the two verbs against the SAVED credential. Neither takes
// anything from this form: the object and its Secret are the whole input, so a
// probe can never be pointed at an endpoint the saved credential does not have.
async function probe(discover: boolean) {
  if (locked.value || !props.credential || harness.value) return
  if (!discover && !model.value.trim()) { modelError.value = 'Choose or enter a model ID.'; return }
  const serial = ++generation
  const snapshot = fingerprint.value
  if (discover) { discovering.value = true; discoveryError.value = '' } else { testing.value = true; testError.value = ''; testedFingerprint.value = '' }
  try {
    const target = props.credential.name
    // The probe carries the model currently PICKED, not the one saved on the
    // object — that is the whole point: the pick is proved before it is
    // written, not by the first run that uses it.
    const result = await (discover ? props.api.discoverCredential(target) : props.api.testCredential(target, model.value.trim()))
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

function namedWell(): boolean {
  if (props.credential || /^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(name.value.trim())) return true
  nameError.value = name.value.trim()
    ? 'Use lowercase letters and numbers separated by hyphens for the name.'
    : 'Enter a name for this credential.'
  return false
}

function submit() {
  if (locked.value) return
  if (harness.value) {
    const secretValid = validHarness()
    const nameValid = namedWell()
    if (!secretValid || !nameValid) return
    const value = harnessSecret.value.trim()
    // No baseURL and no model: the CRD's CEL rule asks for a URL only from a
    // chat provider, and there is no model to default. spec.secretKey is
    // absent on purpose — the Secret key below is the kind declaration.
    emit('save', {
      name: name.value.trim(),
      provider: provider.value,
      ...(value ? { harnessSecret: { key: harnessKey.value, value } } : {}),
    })
    return
  }
  const fieldsValid = valid(saved.value)
  const nameValid = namedWell()
  if (!fieldsValid || !nameValid) return
  emit('save', {
    name: name.value.trim(),
    provider: provider.value,
    baseURL: baseURL.value.trim(),
    model: model.value.trim(),
    ...(apiKey.value.trim() ? { apiKey: apiKey.value.trim() } : {}),
  }, verified.value ? testResult.value : undefined)
}

async function cancel() {
  if (locked.value) return
  const subject = harness.value ? 'harness identity' : 'model connection'
  const title = harness.value ? 'Discard harness identity changes?' : 'Discard model changes?'
  if ((fingerprint.value !== baseline || name.value !== (props.credential?.name || '')) && !await confirmDialog({ title, message: `Your unsaved ${subject} changes will be lost.`, confirmLabel: 'Discard changes' })) return
  emit('cancel')
}
defineExpose({ cancel, locked })
</script>
<template>
 <HarnessCredentialForm
 v-if="harness"
  ref="editorForm"
  class="agents-model-create"
  :name="name"
  :name-disabled="Boolean(credential)"
  name-pattern="[a-z0-9]+(-[a-z0-9]+)*"
  :provider-preset="preset"
  :provider-options="options"
  provider-label-id="agents-model-provider-label"
  :provider-guidance="providerGuidance"
  :origin="current.origin"
  :secret-keys="harnessKeys"
  :secret-key="harnessKey"
  :secret="harnessSecret"
  :secret-required="needsHarnessSecret"
  :secret-hint="harnessHint"
  :name-error="nameError"
  :secret-error="harnessSecretError"
  :form-error="error"
  :save-disabled="saveDisabled"
  :busy="busy"
  :editing="Boolean(credential)"
  wide
  @update:name="name = $event"
  @update:provider="updateProvider($event)"
  @update:secret-key="harnessKey = $event"
  @update:secret="harnessSecret = $event"
  @cancel="cancel"
  @save="submit"
 />
 <ModelConnectionForm
 v-else
  ref="editorForm"
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
  :name-error="nameError"
  :credential="apiKey"
  credential-label="API key"
  :credential-error="credentialError"
  :credential-required="needsKey"
  :credential-placeholder="credential ? 'API key (leave blank to keep current)' : 'API key'"
  :credential-hint="credential ? 'The saved key can only be reused with the same provider endpoint.' : 'Stored in this workspace as a Secret and never returned to the browser.'"
  :model="model"
  model-hint="Use the exact model identifier shown by your provider."
  :model-error="modelError"
  :discovered-models="discoveredModels"
  :discovery-loading="discovering"
  :discovery-error="discoveryError"
  :discover-disabled="!saved"
  discover-disabled-reason="Save this connection first — model discovery runs against the saved credential."
  :testing="testing"
  :test-error="testError"
  :test-notice="notice"
  :form-error="error"
  :connection-tested="verified"
  :test-disabled="!saved || !model.trim()"
  :save-disabled="saveDisabled"
  :busy="busy"
  :editing="Boolean(credential)"
  novalidate
  wide
  @update:name="name = $event"
  @update:provider="updateProvider($event)"
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
