<script setup lang="ts">
import { computed } from 'vue'
import { Check, Cpu, KeyRound, Loader2, Pencil, Plus, Star, Trash2 } from 'lucide-vue-next'
import ModelConnectionCard from './portalkit/ModelConnectionCard.vue'
import ModelConnectionForm from './portalkit/ModelConnectionForm.vue'
import ModelUsageSection from './portalkit/ModelUsageSection.vue'
import type { LLMProviderPreset } from './llmDiscovery'
import type { ProjectLLMDiscoveredModel, ProjectLLMSettings } from './types'

type LLMCredentialMode = 'api-key' | 'service-account-json'

const providerOptions = [
  { value: 'openai', label: 'OpenAI' },
  { value: 'google', label: 'Google AI Studio' },
  { value: 'custom', label: 'Custom OpenAI-compatible' },
] as const

const props = defineProps<{
  routePage?: boolean
  modelTests?: Record<string, { state: string; tone: 'success' | 'danger' | 'muted'; error?: string }>
  settings: ProjectLLMSettings | null
  loading: boolean
  loadError: string | null
  saving: boolean
  status: string | null
  actionError: string | null
  editorOpen: boolean
  creationRoute: boolean
  editingModelID: string | null
  name: string
  providerPreset: LLMProviderPreset
  credentialMode: LLMCredentialMode
  baseURL: string
  model: string
  apiKey: string
  nameError: string
  baseURLError: string
  modelError: string
  credentialError: string
  credentialRequired: boolean
  apiKeyPlaceholder: string
  apiKeyHint: string
  providerGuidance: string
  modelHint: string
  googleProvider: boolean
  googleServiceAccountMode: boolean
  discoveredModels: ProjectLLMDiscoveredModel[]
  discoveryLoading: boolean
  discoveryError: string | null
  discoveryStatus: string | null
  canDiscover: boolean
  testing: boolean
  testStatus: string | null
  testError: string | null
  requireConnectionTest: boolean
  connectionTested: boolean
}>()

const recommendedDiscoveredModels = computed(() => props.discoveredModels.filter((model) => model.compatibility === 'recommended').slice(0, 4))

const emit = defineEmits<{
  retry: []
  openEditor: [modelID?: string]
  cancelEditor: []
  save: []
  test: []
  testSaved: [modelID: string]
  delete: [modelID: string]
  setDefault: [modelID: string]
  selectProvider: [provider: LLMProviderPreset]
  discover: []
  selectDiscoveredModel: [model: ProjectLLMDiscoveredModel]
  'update:name': [value: string]
  'update:credentialMode': [mode: LLMCredentialMode]
  'update:baseURL': [value: string]
  'update:model': [value: string]
  'update:apiKey': [value: string]
}>()
</script>

<template>
  <section class="grid gap-4" :aria-label="creationRoute ? 'Connect model' : 'Models'">
    <header v-if="!creationRoute && !editorOpen" class="flex flex-wrap items-center justify-between gap-3">
      <h2 v-if="routePage" class="m-0 text-[15px] font-semibold text-text-primary">Models</h2>
      <span
        v-if="!(loading && !settings) && (settings?.models.length ?? 0) > 0"
        class="ml-auto inline-flex"
        :title="(settings?.models.length ?? 0) >= 20 ? 'This workspace already has the maximum of 20 models.' : undefined"
      >
        <button
          type="button"
          class="k-btn k-btn--primary shrink-0"
          :disabled="(settings?.models.length ?? 0) >= 20"
          @click="emit('openEditor')"
        >
          <Plus class="h-4 w-4" :stroke-width="1.75" />
          Connect model
        </button>
      </span>
    </header>
    <p v-if="routePage && !creationRoute && !editorOpen" class="text-[13px] text-text-muted">Configure the model credentials App Studio uses when creating and chatting in projects.</p>

    <div v-if="loading && !settings && !creationRoute" class="grid min-h-48 content-start gap-3 rounded-md border border-dashed border-border-subtle bg-surface p-4" role="status" aria-live="polite" aria-busy="true">
      <div class="shimmer h-4 w-36 rounded bg-surface-overlay" />
      <div class="shimmer h-24 w-full rounded bg-surface-overlay" />
      <div class="text-[12px] text-text-muted">Loading models…</div>
    </div>
    <div v-else-if="loadError && !settings && !creationRoute" class="flex min-h-48 flex-col items-start justify-center gap-2 rounded-md border border-danger/30 bg-danger-subtle p-4 text-[12px] text-danger" role="alert">
      <div>{{ loadError }}</div>
      <button type="button" class="k-btn k-btn--ghost" @click="emit('retry')">Retry</button>
    </div>

    <template v-else>
      <div v-if="loading" class="flex items-center gap-2 text-[11px] text-text-muted" role="status" aria-live="polite" aria-busy="true">
        <Loader2 class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="1.75" />
        Refreshing models…
      </div>
      <div v-if="loadError" class="k-inline-notification k-inline-notification--error" role="alert">
        <span>{{ loadError }}</span>
        <button type="button" class="k-btn k-btn--ghost" @click="emit('retry')">Retry</button>
      </div>
      <div v-if="actionError" class="k-inline-notification k-inline-notification--error" role="alert">{{ actionError }}</div>
      <div v-else-if="status" class="k-inline-notification k-inline-notification--success" role="status" aria-live="polite">{{ status }}</div>
      <div v-if="testError && !editorOpen && !creationRoute" class="k-inline-notification k-inline-notification--error" role="alert">{{ testError }}</div>
      <div v-else-if="testStatus && !editorOpen && !creationRoute" class="k-inline-notification k-inline-notification--success" role="status" aria-live="polite"><Check class="h-3.5 w-3.5" :stroke-width="2" />{{ testStatus }}</div>

      <div v-if="settings?.models.length && !creationRoute && !editorOpen" class="k-model-grid">
        <ModelConnectionCard v-for="saved in settings.models" :key="saved.id" :name="saved.name" :model="saved.model" :endpoint="saved.baseURL" :configured="saved.configured" :is-default="saved.default" :busy="saving"
          :test-state="modelTests?.[saved.id]?.state" :test-tone="modelTests?.[saved.id]?.tone">
          <p v-if="saved.catalog" class="text-[11px] text-text-secondary">${{ saved.catalog.inputPer1M }} input · ${{ saved.catalog.outputPer1M }} output<br />USD per 1M tokens · catalog estimate</p><p v-else class="text-[11px] text-text-secondary">Pricing unavailable</p>
          <p class="text-[11px] text-text-secondary">{{ saved.default ? 'Default for new projects' : 'Available in project model pickers' }}</p>
          <p v-if="modelTests?.[saved.id]?.error" class="k-inline-notification k-inline-notification--error" role="alert">{{ modelTests[saved.id].error }}</p>
          <template #actions>

            <button type="button" class="app-studio-touch-target k-btn k-btn--ghost" :disabled="saving" @click="emit('openEditor', saved.id)">
              <Pencil class="h-3.5 w-3.5" :stroke-width="1.75" /> Edit
            </button>
            <button type="button" class="app-studio-touch-target k-btn k-btn--ghost" :disabled="saving || !saved.configured || modelTests?.[saved.id]?.state === 'Testing…'" @click="emit('testSaved', saved.id)">Test connection</button>
            <span
              v-if="!saved.default"
              class="inline-flex"
              :title="!saved.configured ? 'Add a credential before making this model the default.' : undefined"
            >
              <button
                type="button"
                class="app-studio-touch-target k-btn k-btn--ghost"
                :disabled="saving || !saved.configured"
                :aria-label="!saved.configured ? `Make ${saved.name} default unavailable: add a credential first` : `Make ${saved.name} default`"
                @click="emit('setDefault', saved.id)"
              >
                <Star class="h-3.5 w-3.5" :stroke-width="1.75" /> Make default
              </button>
            </span>
            <button type="button" class="app-studio-touch-target k-icon-action" :disabled="saving" :aria-label="`Delete ${saved.name}`" @click="emit('delete', saved.id)">
              <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
            </button>
          </template>
        </ModelConnectionCard>
      </div>

      <div v-else-if="!editorOpen && !creationRoute" class="flex min-h-44 flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-border-subtle bg-surface px-5 py-8 text-center">
        <div class="flex h-10 w-10 items-center justify-center rounded-lg border border-border-subtle bg-surface-overlay text-text-muted"><Cpu class="h-5 w-5" :stroke-width="1.75" /></div>
        <div>
          <h4 class="text-[13px] font-semibold text-text-primary">No models configured</h4>
          <p class="mt-1 max-w-md text-[12px] leading-5 text-text-muted">Connect a provider endpoint and credential before creating or chatting in projects.</p>
        </div>
        <button type="button" class="k-btn k-btn--primary" @click="emit('openEditor')">
          <Plus class="h-4 w-4" :stroke-width="1.75" /> Connect model
        </button>
      </div>

      <ModelUsageSection v-if="!editorOpen && !creationRoute && settings?.models.length" provider="App Studio" />

      <ModelConnectionForm
        v-if="editorOpen || creationRoute"
        :name="name"
        :name-error="nameError"
        :name-max-length="80"
        :provider-preset="providerPreset"
        :provider-options="providerOptions"
        provider-label-id="model-provider-label"
        :provider-guidance="providerGuidance"
        :base-u-r-l="baseURL"
        :base-u-r-l-error="baseURLError"
        :credential="apiKey"
        :credential-label="googleServiceAccountMode ? 'Service account JSON' : 'API key'"
        :credential-placeholder="editingModelID && !credentialRequired ? apiKeyPlaceholder + ' (leave blank to keep current)' : apiKeyPlaceholder"
        :credential-hint="apiKeyHint"
        :credential-error="credentialError"
        :credential-required="credentialRequired"
        :model="model"
        :model-error="modelError"
        :model-hint="modelHint"
        :discovered-models="discoveredModels"
        :discovery-loading="discoveryLoading"
        :discovery-error="discoveryError"
        :discovery-status="discoveryStatus"
        :discover-disabled="!canDiscover"
        :discover-disabled-reason="googleServiceAccountMode ? 'Vertex AI model discovery is not available yet.' : 'Enter a credential before finding models.'"
        :testing="testing"
        :test-error="testError"
        :connection-tested="connectionTested"
        :test-disabled="!model.trim() || (credentialRequired && !apiKey.trim()) || Boolean(baseURLError)"
        :save-disabled="testing || (requireConnectionTest && !connectionTested)"
        :busy="saving"
        :editing="Boolean(editingModelID)"
        :wide="routePage || creationRoute"
        novalidate
        @update:name="emit('update:name', $event)"
        @update:provider="emit('selectProvider', $event as LLMProviderPreset)"
        @update:base-u-r-l="emit('update:baseURL', $event)"
        @update:credential="emit('update:apiKey', $event)"
        @update:model="emit('update:model', $event)"
        @select-model="emit('selectDiscoveredModel', $event)"
        @discover="emit('discover')"
        @test="emit('test')"
        @cancel="emit('cancelEditor')"
        @save="emit('save')"
      >
        <template v-if="!routePage && !creationRoute" #before-name>
          <div class="flex flex-wrap items-start gap-3">
            <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface text-text-muted"><KeyRound class="h-4 w-4" :stroke-width="1.75" /></div>
            <div class="min-w-0">
              <h4 class="text-[13px] font-semibold text-text-primary">{{ editingModelID ? 'Edit model' : 'Connect model' }}</h4>
              <p class="mt-0.5 text-[11px] leading-4 text-text-muted">Give this connection a recognizable name, then configure its endpoint and workspace credential.</p>
            </div>
          </div>
        </template>

        <template v-if="googleProvider" #connection-extra="{ disabled }">
          <label for="model-credential-method" class="k-model-form-field">Credential method
            <select id="model-credential-method" :value="credentialMode" class="k-input" :disabled="disabled" @change="emit('update:credentialMode', ($event.target as HTMLSelectElement).value as LLMCredentialMode)"><option value="api-key">Gemini API key</option><option value="service-account-json">Vertex AI service account</option></select>
          </label>
        </template>

        <template v-if="googleServiceAccountMode" #credential-control="{ disabled, describedby }">
          <textarea id="model-credential" name="apiKey" :value="apiKey" class="k-input min-h-[140px] resize-y font-mono text-[12px] leading-5" :class="credentialError ? 'border-danger' : ''" :placeholder="apiKeyPlaceholder" autocomplete="off" :disabled="disabled" :required="credentialRequired" :aria-invalid="Boolean(credentialError)" :aria-required="credentialRequired" :aria-describedby="describedby" @input="emit('update:apiKey', ($event.target as HTMLTextAreaElement).value)" />
        </template>

        <template #model-extra>
          <div v-if="recommendedDiscoveredModels.length" class="grid gap-2" aria-label="Recommended models">
            <span class="text-[9px] font-semibold uppercase tracking-wide text-text-muted">Recommended for App Studio</span>
            <div class="flex flex-wrap gap-2">
              <button v-for="available in recommendedDiscoveredModels" :key="available.id" type="button" class="app-studio-touch-target k-btn k-btn--ghost" :disabled="saving" @click="emit('selectDiscoveredModel', available)">
                {{ available.name }}
              </button>
            </div>
          </div>
        </template>
      </ModelConnectionForm>
    </template>
  </section>
</template>
