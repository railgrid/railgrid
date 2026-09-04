<script setup lang="ts">
import { computed } from 'vue'
import { Check, ChevronRight, Cpu, KeyRound, Loader2, Pencil, Plus, RefreshCw, Star, Trash2 } from 'lucide-vue-next'
import ModelIDSelector from './ModelIDSelector.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import type { LLMProviderPreset } from './llmDiscovery'
import type { ProjectLLMDiscoveredModel, ProjectLLMSettings } from './types'

type LLMCredentialMode = 'api-key' | 'service-account-json'

const props = defineProps<{
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
  provider: string
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
  baseURLPlaceholder: string
  apiKeyPlaceholder: string
  apiKeyHint: string
  providerGuidance: string
  modelHint: string
  googleProvider: boolean
  googleServiceAccountMode: boolean
  customProvider: boolean
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
    <div v-if="!creationRoute && !editorOpen && !(loading && !settings) && (settings?.models.length ?? 0) > 0" class="flex justify-end">
      <span
        class="inline-flex"
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
    </div>

    <div v-if="loading && !settings && !creationRoute" class="grid min-h-48 content-start gap-3 rounded-md border border-dashed border-border-subtle bg-surface p-4" role="status" aria-live="polite" aria-busy="true">
      <div class="shimmer h-4 w-36 rounded bg-surface-overlay" />
      <div class="shimmer h-24 w-full rounded bg-surface-overlay" />
      <div class="text-[12px] text-text-muted">Loading models…</div>
    </div>
    <div v-else-if="loadError && !settings && !creationRoute" class="flex min-h-48 flex-col items-start justify-center gap-2 rounded-md border border-danger/30 bg-danger-subtle p-4 text-[12px] text-danger" role="alert">
      <div>{{ loadError }}</div>
      <button type="button" class="rounded-sm font-medium underline underline-offset-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-danger" @click="emit('retry')">Retry</button>
    </div>

    <template v-else>
      <div v-if="loading" class="flex items-center gap-2 text-[11px] text-text-muted" role="status" aria-live="polite" aria-busy="true">
        <Loader2 class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="1.75" />
        Refreshing models…
      </div>
      <div v-if="loadError" class="flex flex-wrap items-center gap-2 rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger" role="alert">
        <span>{{ loadError }}</span>
        <button type="button" class="rounded-sm font-medium underline underline-offset-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-danger" @click="emit('retry')">Retry</button>
      </div>
      <div v-if="actionError" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger" role="alert">{{ actionError }}</div>
      <div v-else-if="status" class="rounded-md border border-success/30 bg-success-subtle px-3 py-2 text-[12px] text-success" role="status" aria-live="polite">{{ status }}</div>
      <div v-if="testError" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger" role="alert">{{ testError }}</div>
      <div v-else-if="testStatus" class="flex items-center gap-2 rounded-md border border-success/30 bg-success-subtle px-3 py-2 text-[12px] text-success" role="status" aria-live="polite"><Check class="h-3.5 w-3.5" :stroke-width="2" />{{ testStatus }}</div>

      <div v-if="settings?.models.length && !creationRoute && !editorOpen" class="grid grid-cols-[repeat(auto-fill,minmax(min(100%,280px),360px))] justify-start gap-3">
        <article
          v-for="saved in settings.models"
          :key="saved.id"
          class="grid gap-3 rounded-lg border border-border-subtle bg-surface p-4 transition-colors hover:border-border-default"
          :aria-label="`Model ${saved.name}`"
        >
          <div class="flex min-w-0 items-start gap-3">
            <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-accent/20 bg-accent-subtle text-accent">
              <Cpu class="h-4 w-4" :stroke-width="1.75" />
            </div>
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <h4 class="truncate text-[13px] font-semibold text-text-primary">{{ saved.name }}</h4>
                <span v-if="saved.default" class="rounded-sm border border-accent/30 bg-accent-subtle px-1.5 py-0.5 font-mono text-[9px] font-semibold uppercase tracking-wide text-accent">Default</span>
              </div>
              <p class="mt-0.5 truncate font-mono text-[11px] text-text-muted" :title="saved.model">{{ saved.model }}</p>
            </div>
            <StatusBadge :status="saved.configured ? 'Credential saved' : 'Needs credential'" :tone="saved.configured ? 'success' : 'warning'" />
          </div>
          <dl class="border-y border-border-subtle py-2 text-[11px]">
            <dt class="text-[9px] font-semibold uppercase tracking-wide text-text-muted">Endpoint</dt>
            <dd class="mt-1 truncate font-mono text-text-secondary" :title="saved.baseURL">{{ saved.baseURL }}</dd>
          </dl>
          <div class="flex flex-wrap items-center gap-1">
            <button type="button" class="app-studio-touch-target inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-[11px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:opacity-50" :disabled="saving" @click="emit('openEditor', saved.id)">
              <Pencil class="h-3.5 w-3.5" :stroke-width="1.75" /> Edit
            </button>
            <span
              v-if="!saved.default"
              class="inline-flex"
              :title="!saved.configured ? 'Add a credential before making this model the default.' : undefined"
            >
              <button
                type="button"
                class="inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-[11px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:opacity-50"
                :disabled="saving || !saved.configured"
                :aria-label="!saved.configured ? `Make ${saved.name} default unavailable: add a credential first` : `Make ${saved.name} default`"
                @click="emit('setDefault', saved.id)"
              >
                <Star class="h-3.5 w-3.5" :stroke-width="1.75" /> Make default
              </button>
            </span>
            <button type="button" class="app-studio-touch-target ml-auto inline-flex h-8 w-8 items-center justify-center rounded-md text-text-muted transition hover:bg-danger-subtle hover:text-danger focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-danger disabled:opacity-50" :disabled="saving" :aria-label="`Delete ${saved.name}`" @click="emit('delete', saved.id)">
              <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
            </button>
          </div>
        </article>
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

      <form v-if="editorOpen || creationRoute" class="k-create-surface" :class="{ 'k-create-surface--wide': creationRoute }" aria-label="Model configuration form" :aria-busy="saving" novalidate @submit.prevent="emit('save')">
        <div class="k-create-body">
          <div v-if="!creationRoute" class="flex flex-wrap items-start gap-3">
            <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface text-text-muted"><KeyRound class="h-4 w-4" :stroke-width="1.75" /></div>
            <div class="min-w-0">
              <h4 class="text-[13px] font-semibold text-text-primary">{{ editingModelID ? 'Edit model' : 'New model' }}</h4>
              <p class="mt-0.5 text-[11px] leading-4 text-text-muted">Give this connection a recognizable name, then configure its endpoint and workspace credential.</p>
            </div>
          </div>

          <label for="model-display-name" class="grid gap-1.5 text-[11px] font-medium text-text-secondary">
            Display name
            <input
              id="model-display-name"
              :value="name"
              class="k-input h-10"
              :class="nameError ? 'border-danger/50 focus:border-danger focus:shadow-[0_0_0_3px_var(--color-danger-subtle)]' : ''"
              placeholder="e.g. GPT-5.6 High"
              maxlength="80"
              :disabled="saving"
              :aria-invalid="Boolean(nameError)"
              aria-required="true"
              :aria-describedby="nameError ? 'model-display-name-error' : 'model-display-name-hint'"
              @input="emit('update:name', ($event.target as HTMLInputElement).value)"
            />
            <span v-if="nameError" id="model-display-name-error" class="text-[11px] font-normal leading-4 text-danger" role="alert">{{ nameError }}</span>
            <span v-else id="model-display-name-hint" class="text-[11px] font-normal leading-4 text-text-muted">Use a name people can recognize in project and chat model pickers.</span>
          </label>

          <section class="grid gap-3 border-t border-border-subtle pt-4" aria-labelledby="model-provider-heading">
            <h5 id="model-provider-heading" class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Connection</h5>
            <div class="grid gap-3 sm:grid-cols-2">
              <label for="model-provider" class="grid min-w-0 gap-1.5 text-[11px] font-medium text-text-secondary">Provider
                <span class="relative block">
                  <select id="model-provider" :value="providerPreset" class="k-input h-10 appearance-none pr-9" :disabled="saving" aria-describedby="model-provider-hint" @change="emit('selectProvider', ($event.target as HTMLSelectElement).value as LLMProviderPreset)">
                    <option value="openai">OpenAI</option>
                    <option value="google">Google AI Studio</option>
                    <option value="custom">Custom OpenAI-compatible</option>
                  </select>
                  <ChevronRight class="pointer-events-none absolute right-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 rotate-90 text-text-muted" :stroke-width="1.75" />
                </span>
                <span id="model-provider-hint" class="text-[11px] font-normal leading-4 text-text-muted">{{ providerGuidance }}</span>
              </label>
              <label v-if="googleProvider" for="model-credential-method" class="grid min-w-0 content-start gap-1.5 text-[11px] font-medium text-text-secondary">Credential method
                <span class="relative block">
                  <select id="model-credential-method" :value="credentialMode" class="k-input h-10 appearance-none pr-9" :disabled="saving" @change="emit('update:credentialMode', ($event.target as HTMLSelectElement).value as LLMCredentialMode)">
                    <option value="api-key">Gemini API key</option>
                    <option value="service-account-json">Vertex AI service account</option>
                  </select>
                  <ChevronRight class="pointer-events-none absolute right-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 rotate-90 text-text-muted" :stroke-width="1.75" />
                </span>
              </label>
              <label v-else-if="customProvider" for="model-base-url" class="grid min-w-0 gap-1.5 text-[11px] font-medium text-text-secondary">Base URL
                <input id="model-base-url" :value="baseURL" class="k-input h-10 min-w-0 font-mono text-[12px]" :class="baseURLError ? 'border-danger/50 focus:border-danger focus:shadow-[0_0_0_3px_var(--color-danger-subtle)]' : ''" :placeholder="baseURLPlaceholder" :disabled="saving" :aria-invalid="Boolean(baseURLError)" aria-required="true" aria-describedby="model-base-url-help" type="url" @input="emit('update:baseURL', ($event.target as HTMLInputElement).value)" />
                <span id="model-base-url-help" class="text-[11px] font-normal leading-4" :class="baseURLError ? 'text-danger' : 'text-text-muted'" :role="baseURLError ? 'alert' : undefined">{{ baseURLError || 'App Studio adds /chat/completions and queries /models.' }}</span>
              </label>
              <div v-else class="grid min-w-0 content-start gap-1.5 text-[11px] font-medium text-text-secondary">
                API endpoint
                <div class="flex h-10 min-w-0 items-center rounded-md border border-border-subtle bg-surface px-3 font-mono text-[11px] text-text-muted">
                  <span class="truncate" :title="baseURL">{{ baseURL }}</span>
                </div>
              </div>
            </div>
          </section>

          <section class="grid gap-2 border-t border-border-subtle pt-4" aria-labelledby="model-credential-heading">
            <h5 id="model-credential-heading" class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Credential</h5>
            <label for="model-credential" class="sr-only">{{ googleServiceAccountMode ? 'Service account JSON' : 'API key' }}</label>
            <textarea v-if="googleServiceAccountMode" id="model-credential" :value="apiKey" class="k-input min-h-[140px] resize-y font-mono text-[12px] leading-5" :class="credentialError ? 'border-danger/50 focus:border-danger focus:shadow-[0_0_0_3px_var(--color-danger-subtle)]' : ''" :placeholder="apiKeyPlaceholder" autocomplete="off" :disabled="saving" :aria-invalid="Boolean(credentialError)" :aria-required="credentialRequired" aria-describedby="model-credential-help" @input="emit('update:apiKey', ($event.target as HTMLTextAreaElement).value)" />
            <input v-else id="model-credential" :value="apiKey" class="k-input h-10" :class="credentialError ? 'border-danger/50 focus:border-danger focus:shadow-[0_0_0_3px_var(--color-danger-subtle)]' : ''" :placeholder="editingModelID && !credentialRequired ? `${apiKeyPlaceholder} (leave blank to keep current)` : apiKeyPlaceholder" type="password" autocomplete="new-password" :disabled="saving" :aria-invalid="Boolean(credentialError)" :aria-required="credentialRequired" aria-describedby="model-credential-help" @input="emit('update:apiKey', ($event.target as HTMLInputElement).value)" />
            <p id="model-credential-help" class="text-[11px] leading-4" :class="credentialError ? 'text-danger' : 'text-text-muted'" :role="credentialError ? 'alert' : undefined">{{ credentialError || apiKeyHint }}</p>
          </section>

          <section class="grid gap-3 border-t border-border-subtle pt-4" aria-labelledby="model-selection-heading">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <h5 id="model-selection-heading" class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Model</h5>
              <span class="inline-flex" :title="!canDiscover ? (googleServiceAccountMode ? 'Vertex AI model discovery is not available yet.' : 'Enter a credential before finding models.') : undefined">
                <button type="button" class="app-studio-touch-target k-btn k-btn--ghost h-8 px-2.5 text-[11px]" :disabled="saving || discoveryLoading || !canDiscover" @click="emit('discover')">
                  <Loader2 v-if="discoveryLoading" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                  <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
                  {{ discoveryLoading ? 'Finding models…' : 'Find models' }}
                </button>
              </span>
            </div>
            <label for="model-id" class="grid min-w-0 content-start gap-1.5 text-[11px] font-medium text-text-secondary">Model ID
              <ModelIDSelector
                :model-value="model"
                :models="discoveredModels"
                :disabled="saving"
                :invalid="Boolean(modelError)"
                :described-by="modelError ? 'model-id-error' : 'model-id-hint'"
                @update:model-value="emit('update:model', $event)"
                @select="emit('selectDiscoveredModel', $event)"
              />
              <span v-if="modelError" id="model-id-error" class="text-[11px] font-normal leading-4 text-danger" role="alert">{{ modelError }}</span>
              <span v-else id="model-id-hint" class="text-[11px] font-normal leading-4 text-text-muted">{{ modelHint }} Find models to load the full catalog, then search or enter an ID.</span>
            </label>
            <div v-if="recommendedDiscoveredModels.length" class="grid gap-2" aria-label="Recommended models">
              <span class="text-[9px] font-semibold uppercase tracking-wide text-text-muted">Recommended for App Studio</span>
              <div class="flex flex-wrap gap-2">
                <button v-for="available in recommendedDiscoveredModels" :key="available.id" type="button" class="app-studio-touch-target rounded-sm border border-accent/25 bg-accent-subtle px-2.5 py-1.5 font-mono text-[10px] font-medium text-accent transition hover:border-accent/50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent" :disabled="saving" @click="emit('selectDiscoveredModel', available)">
                  {{ available.name }}
                </button>
              </div>
            </div>
            <p v-if="discoveryError" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[11px] leading-4 text-danger" role="alert">{{ discoveryError }} You can still enter a model ID manually.</p>
            <p v-else-if="discoveryStatus" class="text-[11px] leading-4 text-text-muted" role="status" aria-live="polite">{{ discoveryStatus }}</p>
          </section>

        </div>
        <footer class="k-create-actions">
          <button type="button" class="k-btn k-btn--ghost" :disabled="saving || testing" @click="emit('cancelEditor')">Cancel</button>
          <button type="button" class="k-btn k-btn--ghost" :disabled="saving || testing || !model.trim() || !apiKey.trim() || Boolean(baseURLError)" @click="emit('test')">
            <Loader2 v-if="testing" class="h-4 w-4 animate-spin motion-reduce:animate-none" :stroke-width="1.75" />
            <Check v-else-if="connectionTested" class="h-4 w-4 text-success" :stroke-width="2" />
            <RefreshCw v-else class="h-4 w-4" :stroke-width="1.75" />
            {{ testing ? 'Testing…' : connectionTested ? 'Connection verified' : 'Test connection' }}
          </button>
          <button class="k-btn k-btn--primary" :disabled="saving || testing || (requireConnectionTest && !connectionTested)">
            <Loader2 v-if="saving" class="h-4 w-4 animate-spin" :stroke-width="1.75" /><Check v-else class="h-4 w-4" :stroke-width="1.75" />
            {{ saving ? (editingModelID && !creationRoute ? 'Saving changes…' : 'Connecting model…') : requireConnectionTest ? 'Save and finish' : (editingModelID && !creationRoute ? 'Save changes' : 'Connect model') }}
          </button>
        </footer>
      </form>
    </template>
  </section>
</template>
