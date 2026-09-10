<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     ModelConnectionForm owns the common model connection controls. Provider
     forms supply state and event handlers; slots are reserved for provider
     specific controls such as Google credential mode/JSON and recommendations.
 -->
<script setup lang="ts">
import { Check, Loader2, RefreshCw } from 'lucide-vue-next'
import ModelIDSelector from './ModelIDSelector.vue'
import type { DiscoveredModel } from './modelIDSelection'

const props = defineProps<{
  name: string
  nameError?: string | null
  nameDisabled?: boolean
  namePattern?: string
  nameMaxLength?: number
  providerPreset: string
  providerOptions: readonly { value: string; label: string; disabled?: boolean }[]
  providerLabelId: string
  providerGuidance: string
  baseURL: string
  baseURLError?: string | null
  credential: string
  credentialLabel: string
  credentialPlaceholder: string
  credentialHint: string
  credentialError?: string | null
  credentialRequired?: boolean
  model: string
  modelError?: string | null
  modelHint: string
  discoveredModels: DiscoveredModel[]
  discoveryLoading?: boolean
  discoveryError?: string | null
  discoveryStatus?: string | null
  discoverDisabled?: boolean
  discoverDisabledReason?: string
  testing?: boolean
  testError?: string | null
  connectionTested?: boolean
  testDisabled?: boolean
  saveDisabled?: boolean
  busy?: boolean
  editing?: boolean
  wide?: boolean
  novalidate?: boolean
  formError?: string | null
}>()

const emit = defineEmits<{
  save: []
  cancel: []
  test: []
  discover: []
  'select-model': [model: DiscoveredModel]
  'update:name': [value: string]
  'update:provider': [value: string]
  'update:baseURL': [value: string]
  'update:credential': [value: string]
  'update:model': [value: string]
}>()

function isLocked(): boolean {
  return props.busy || props.testing || Boolean(props.discoveryLoading)
}
</script>

<template>
  <form
    class="k-create-surface k-model-form"
    :class="{ 'k-create-surface--wide': wide }"
    aria-label="Model configuration form"
    :aria-busy="isLocked()"
    :novalidate="novalidate || undefined"
    @submit.prevent="emit('save')"
  >
    <div class="k-create-body">
      <slot name="before-name" />

      <label for="model-display-name" class="k-model-form-field">
        Name
        <input
          id="model-display-name"
          :value="name"
          class="k-input"
          :class="nameError ? 'border-danger' : ''"
          placeholder="e.g. everyday"
          :maxlength="nameMaxLength"
          :pattern="namePattern"
          name="name"
          required
          :disabled="isLocked() || nameDisabled"
          :aria-invalid="Boolean(nameError)"
          aria-required="true"
          :aria-describedby="nameError ? 'model-display-name-error' : 'model-display-name-hint'"
          @input="emit('update:name', ($event.target as HTMLInputElement).value)"
        >
        <span v-if="nameError" id="model-display-name-error" class="k-model-form-hint text-danger" role="alert">{{ nameError }}</span>
        <span v-else id="model-display-name-hint" class="k-model-form-hint">Choose a recognizable lowercase name for model pickers.</span>
      </label>

      <section class="k-model-form-section" aria-labelledby="model-provider-heading">
        <h5 id="model-provider-heading" class="k-model-form-heading">Connection</h5>
        <div class="k-model-form-columns">
          <label class="k-model-form-field">
            <span :id="providerLabelId">Provider</span>
            <select
              id="model-provider"
              :value="providerPreset"
              class="k-input k-form-select__trigger"
              :disabled="isLocked()"
              :aria-labelledby="providerLabelId"
              :aria-describedby="`${providerLabelId}-hint`"
              required
              @change="emit('update:provider', ($event.target as HTMLSelectElement).value)"
            >
              <option v-for="option in providerOptions" :key="option.value" :value="option.value" :disabled="option.disabled">{{ option.label }}</option>
            </select>
            <span :id="`${providerLabelId}-hint`" class="k-model-form-hint">{{ providerGuidance }}</span>
          </label>

          <slot name="connection-extra" :disabled="isLocked()" />

          <label v-if="providerPreset !== 'google'" class="k-model-form-field">
            {{ providerPreset === 'custom' ? 'Base URL' : 'API endpoint' }}
            <input
              v-if="providerPreset === 'custom'"
              id="model-base-url"
              name="baseURL"
              :value="baseURL"
              class="k-input min-w-0 font-mono text-[12px]"
              :class="baseURLError ? 'border-danger' : ''"
              placeholder="Base URL"
              :disabled="isLocked()"
              :aria-invalid="Boolean(baseURLError)"
              aria-required="true"
              aria-describedby="model-base-url-help"
              type="url"
              @input="emit('update:baseURL', ($event.target as HTMLInputElement).value)"
            >
            <div v-else class="k-input k-model-form-endpoint font-mono text-[12px] text-text-muted">
              <span class="truncate" :title="baseURL">{{ baseURL }}</span>
            </div>
            <input
              v-if="providerPreset !== 'custom'"
              type="hidden"
              name="baseURL"
              :value="baseURL"
              :disabled="isLocked()"
              @input="emit('update:baseURL', ($event.target as HTMLInputElement).value)"
            >
            <span
              v-if="providerPreset === 'custom' || baseURLError"
              id="model-base-url-help"
              class="k-model-form-hint"
              :class="baseURLError ? 'text-danger' : ''"
              :role="baseURLError ? 'alert' : undefined"
            >{{ baseURLError || 'Adds /chat/completions and queries /models.' }}</span>
          </label>
        </div>
      </section>

      <section class="k-model-form-section" aria-labelledby="model-credential-heading">
        <h5 id="model-credential-heading" class="k-model-form-heading">Credential</h5>
        <label for="model-credential" class="sr-only">{{ credentialLabel }}</label>
        <slot
          name="credential-control"
          :disabled="isLocked()"
          input-id="model-credential"
          describedby="model-credential-help"
        >
          <input
            id="model-credential"
            :value="credential"
            class="k-input h-10"
            :class="credentialError ? 'border-danger' : ''"
            name="apiKey"
            :placeholder="credentialPlaceholder"
            type="password"
            autocomplete="new-password"
            :required="credentialRequired"
            :disabled="isLocked()"
            :aria-invalid="Boolean(credentialError)"
            :aria-required="credentialRequired"
            aria-describedby="model-credential-help"
            @input="emit('update:credential', ($event.target as HTMLInputElement).value)"
          >
        </slot>
        <p
          id="model-credential-help"
          class="k-model-form-hint"
          :class="credentialError ? 'text-danger' : ''"
          :role="credentialError ? 'alert' : undefined"
        >{{ credentialError || credentialHint }}</p>
      </section>

      <section class="k-model-form-section" aria-labelledby="model-selection-heading">
        <div class="k-model-form-row">
          <h5 id="model-selection-heading" class="k-model-form-heading">Model</h5>
          <span class="inline-flex" :title="discoverDisabled ? discoverDisabledReason || undefined : undefined">
            <button
              type="button"
              class="k-btn k-btn--ghost"
              :disabled="isLocked() || discoverDisabled"
              @click="emit('discover')"
            >
              <Loader2 v-if="discoveryLoading" :size="14" class="animate-spin motion-reduce:animate-none" :stroke-width="1.75" />
              <RefreshCw v-else :size="14" :stroke-width="1.75" />
              {{ discoveryLoading ? 'Finding models…' : 'Find models' }}
            </button>
          </span>
        </div>
        <label for="model-id" class="k-model-form-field">
          Model ID
          <ModelIDSelector
            :model-value="model"
            :models="discoveredModels"
            :disabled="isLocked()"
            :invalid="Boolean(modelError)"
            :described-by="modelError ? 'model-id-error' : 'model-id-hint'"
            @update:model-value="emit('update:model', $event)"
            @select="emit('select-model', $event)"
          />
          <span v-if="modelError" id="model-id-error" class="k-model-form-hint text-danger" role="alert">{{ modelError }}</span>
          <span v-else id="model-id-hint" class="k-model-form-hint">{{ modelHint }} Find models to load the full catalog, then search or enter an ID.</span>
        </label>
        <slot name="model-extra" />
        <p v-if="discoveryError" class="k-inline-notification k-inline-notification--error" role="alert">{{ discoveryError }} You can still enter a model ID manually.</p>
        <p v-else-if="discoveryStatus" class="k-model-form-hint" role="status" aria-live="polite">{{ discoveryStatus }}</p>
      </section>

      <p v-if="formError" class="k-inline-notification k-inline-notification--error" role="alert">{{ formError }}</p>
      <p v-if="testError" class="k-inline-notification k-inline-notification--error" role="alert">{{ testError }}</p>
      <p v-else-if="connectionTested" class="k-inline-notification k-inline-notification--success" role="status" aria-live="polite">
        <Check :size="14" :stroke-width="2" />Connection verified. The model responded successfully.
      </p>
    </div>

    <p class="k-model-form-test-hint">Testing sends a small model request and may incur a charge.</p>
    <footer class="k-create-actions">
      <button type="button" class="k-btn k-btn--ghost" :disabled="isLocked()" @click="emit('cancel')">Cancel</button>
      <button type="button" class="k-btn k-btn--ghost" :disabled="isLocked() || testDisabled" @click="emit('test')">
        <Loader2 v-if="testing" :size="14" class="animate-spin motion-reduce:animate-none" :stroke-width="1.75" />
        <Check v-else-if="connectionTested" :size="14" class="text-success" :stroke-width="2" />
        <RefreshCw v-else :size="14" :stroke-width="1.75" />
        {{ testing ? 'Testing…' : connectionTested ? 'Connection verified' : 'Test connection' }}
      </button>
      <button type="submit" class="k-btn k-btn--primary" :disabled="isLocked() || saveDisabled">
        <Loader2 v-if="busy" :size="14" class="animate-spin motion-reduce:animate-none" :stroke-width="1.75" />
        <Check v-else :size="14" :stroke-width="1.75" />
        {{ busy ? (editing ? 'Saving changes…' : 'Connecting model…') : editing ? 'Save changes' : 'Connect model' }}
      </button>
    </footer>
  </form>
</template>
