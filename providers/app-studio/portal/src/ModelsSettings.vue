<script setup lang="ts">
import { Check, ChevronRight, Cpu, KeyRound, Loader2, Pencil, Plus, Star, Trash2 } from 'lucide-vue-next'
import StatusBadge from './portalkit/StatusBadge.vue'
import type { ProjectLLMSettings } from './types'

type LLMCredentialMode = 'api-key' | 'service-account-json'

defineProps<{
  settings: ProjectLLMSettings | null
  loading: boolean
  loadError: string | null
  saving: boolean
  status: string | null
  actionError: string | null
  editorOpen: boolean
  editingModelID: string | null
  name: string
  provider: string
  credentialMode: LLMCredentialMode
  baseURL: string
  model: string
  apiKey: string
  baseURLError: string
  baseURLPlaceholder: string
  apiKeyPlaceholder: string
  apiKeyHint: string
  googleProvider: boolean
  googleServiceAccountMode: boolean
}>()

const emit = defineEmits<{
  retry: []
  openEditor: [modelID?: string]
  cancelEditor: []
  save: []
  delete: [modelID: string]
  setDefault: [modelID: string]
  selectProvider: [provider: string]
  'update:name': [value: string]
  'update:credentialMode': [mode: LLMCredentialMode]
  'update:baseURL': [value: string]
  'update:model': [value: string]
  'update:apiKey': [value: string]
}>()
</script>

<template>
  <section class="grid gap-4" aria-label="Models">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <h3 class="text-[14px] font-semibold text-text-primary">Models</h3>
        <p class="mt-1 max-w-2xl text-[12px] leading-5 text-text-muted">
          Add workspace model connections, choose the default for project creation, and select a model per chat turn.
        </p>
      </div>
      <button
        v-if="!editorOpen && !(loading && !settings)"
        type="button"
        class="inline-flex h-9 shrink-0 items-center gap-2 rounded-md border border-accent bg-accent px-3 text-[12px] font-semibold text-surface shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="(settings?.models.length ?? 0) >= 20"
        @click="emit('openEditor')"
      >
        <Plus class="h-4 w-4" :stroke-width="1.75" />
        New model
      </button>
    </div>

    <div v-if="loading && !settings" class="grid min-h-48 content-start gap-3 rounded-md border border-dashed border-border-subtle bg-surface p-4" role="status" aria-live="polite" aria-busy="true">
      <div class="shimmer h-4 w-36 rounded bg-surface-overlay" />
      <div class="shimmer h-24 w-full rounded bg-surface-overlay" />
      <div class="text-[12px] text-text-muted">Loading models…</div>
    </div>
    <div v-else-if="loadError && !settings" class="flex min-h-48 flex-col items-start justify-center gap-2 rounded-md border border-danger/30 bg-danger-subtle p-4 text-[12px] text-danger" role="alert">
      <div>{{ loadError }}</div>
      <button type="button" class="font-medium underline underline-offset-2" @click="emit('retry')">Retry</button>
    </div>

    <template v-else>
      <div v-if="loading" class="flex items-center gap-2 text-[11px] text-text-muted" role="status" aria-live="polite" aria-busy="true">
        <Loader2 class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="1.75" />
        Refreshing models…
      </div>
      <div v-if="loadError" class="flex flex-wrap items-center gap-2 rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger" role="alert">
        <span>{{ loadError }}</span>
        <button type="button" class="font-medium underline underline-offset-2" @click="emit('retry')">Retry</button>
      </div>
      <div v-if="actionError" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] text-danger" role="alert">{{ actionError }}</div>
      <div v-else-if="status" class="rounded-md border border-success/30 bg-success-subtle px-3 py-2 text-[12px] text-success" role="status" aria-live="polite">{{ status }}</div>

      <div v-if="settings?.models.length" class="grid gap-3 sm:grid-cols-2">
        <article
          v-for="saved in settings.models"
          :key="saved.id"
          class="grid gap-3 rounded-lg border bg-surface p-4 transition"
          :class="editingModelID === saved.id ? 'border-accent/50' : 'border-border-subtle'"
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
            <StatusBadge :status="saved.configured ? 'Configured' : 'Needs credential'" :tone="saved.configured ? 'success' : 'warning'" />
          </div>
          <dl class="border-y border-border-subtle py-2 text-[11px]">
            <dt class="text-[9px] font-semibold uppercase tracking-wide text-text-muted">Endpoint</dt>
            <dd class="mt-1 truncate font-mono text-text-secondary" :title="saved.baseURL">{{ saved.baseURL }}</dd>
          </dl>
          <div class="flex flex-wrap items-center gap-1">
            <button type="button" class="inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-[11px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary" @click="emit('openEditor', saved.id)">
              <Pencil class="h-3.5 w-3.5" :stroke-width="1.75" /> Edit
            </button>
            <button v-if="!saved.default" type="button" class="inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-[11px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:opacity-50" :disabled="saving || !saved.configured" @click="emit('setDefault', saved.id)">
              <Star class="h-3.5 w-3.5" :stroke-width="1.75" /> Make default
            </button>
            <button type="button" class="ml-auto inline-flex h-8 w-8 items-center justify-center rounded-md text-text-muted transition hover:bg-danger-subtle hover:text-danger disabled:opacity-50" :disabled="saving" :aria-label="`Delete ${saved.name}`" @click="emit('delete', saved.id)">
              <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
            </button>
          </div>
        </article>
      </div>

      <div v-else-if="!editorOpen" class="flex min-h-44 flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-border-subtle bg-surface px-5 py-8 text-center">
        <div class="flex h-10 w-10 items-center justify-center rounded-lg border border-border-subtle bg-surface-overlay text-text-muted"><Cpu class="h-5 w-5" :stroke-width="1.75" /></div>
        <div>
          <h4 class="text-[13px] font-semibold text-text-primary">No models configured</h4>
          <p class="mt-1 max-w-md text-[12px] leading-5 text-text-muted">Add a provider endpoint and credential before creating or chatting in projects.</p>
        </div>
        <button type="button" class="inline-flex h-9 items-center gap-2 rounded-md border border-accent bg-accent px-3 text-[12px] font-semibold text-surface shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover" @click="emit('openEditor')">
          <Plus class="h-4 w-4" :stroke-width="1.75" /> Add model
        </button>
      </div>

      <form v-if="editorOpen" class="grid gap-4 rounded-lg border border-border-subtle bg-surface-overlay/40 p-4" aria-label="Model configuration form" @submit.prevent="emit('save')">
        <div class="flex items-start gap-3">
          <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface text-text-muted"><KeyRound class="h-4 w-4" :stroke-width="1.75" /></div>
          <div class="min-w-0">
            <h4 class="text-[13px] font-semibold text-text-primary">{{ editingModelID ? 'Edit model' : 'New model' }}</h4>
            <p class="mt-0.5 text-[11px] leading-4 text-text-muted">Give this connection a recognizable name, then configure its endpoint and workspace credential.</p>
          </div>
        </div>

        <label class="grid gap-1.5 text-[11px] font-medium text-text-secondary">
          Display name
          <input :value="name" class="h-10 rounded-md border border-border-subtle bg-surface px-3 text-[13px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50" placeholder="e.g. GPT-5.6 High" :disabled="saving" @input="emit('update:name', ($event.target as HTMLInputElement).value)" />
        </label>

        <section class="grid gap-3 border-t border-border-subtle pt-4" aria-labelledby="model-provider-heading">
          <h5 id="model-provider-heading" class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Provider</h5>
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="grid min-w-0 gap-1.5 text-[11px] font-medium text-text-secondary">Provider preset
              <span class="relative block">
                <select :value="provider" class="h-10 w-full appearance-none rounded-md border border-border-subtle bg-surface px-3 pr-9 text-[13px] text-text-primary outline-none transition focus:border-accent/50" :disabled="saving" @change="emit('selectProvider', ($event.target as HTMLSelectElement).value)">
                  <option value="openai-compatible">OpenAI-compatible</option><option value="google-ai-studio">Google</option>
                </select>
                <ChevronRight class="pointer-events-none absolute right-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 rotate-90 text-text-muted" :stroke-width="1.75" />
              </span>
            </label>
            <label v-if="googleProvider" class="grid min-w-0 gap-1.5 text-[11px] font-medium text-text-secondary">Credential method
              <span class="relative block">
                <select :value="credentialMode" class="h-10 w-full appearance-none rounded-md border border-border-subtle bg-surface px-3 pr-9 text-[13px] text-text-primary outline-none transition focus:border-accent/50" :disabled="saving" @change="emit('update:credentialMode', ($event.target as HTMLSelectElement).value as LLMCredentialMode)">
                  <option value="api-key">Gemini API key</option><option value="service-account-json">Vertex AI service account</option>
                </select>
                <ChevronRight class="pointer-events-none absolute right-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 rotate-90 text-text-muted" :stroke-width="1.75" />
              </span>
            </label>
          </div>
        </section>

        <section class="grid gap-3 border-t border-border-subtle pt-4" aria-labelledby="model-endpoint-heading">
          <h5 id="model-endpoint-heading" class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Model endpoint</h5>
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="grid min-w-0 gap-1.5 text-[11px] font-medium text-text-secondary">Base URL
              <input :value="baseURL" class="h-10 min-w-0 rounded-md border bg-surface px-3 font-mono text-[12px] text-text-primary outline-none transition placeholder:text-text-muted" :class="baseURLError ? 'border-danger/50 focus:border-danger' : 'border-border-subtle focus:border-accent/50'" :placeholder="baseURLPlaceholder" :disabled="saving" :aria-invalid="Boolean(baseURLError)" type="url" @input="emit('update:baseURL', ($event.target as HTMLInputElement).value)" />
              <span class="text-[11px] font-normal leading-4" :class="baseURLError ? 'text-danger' : 'text-text-muted'">{{ baseURLError || (googleProvider ? 'Provider API base URL.' : 'Base URL only. App Studio adds /chat/completions.') }}</span>
            </label>
            <label class="grid min-w-0 content-start gap-1.5 text-[11px] font-medium text-text-secondary">Model ID
              <input :value="model" class="h-10 min-w-0 rounded-md border border-border-subtle bg-surface px-3 font-mono text-[12px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50" placeholder="Model ID" :disabled="saving" @input="emit('update:model', ($event.target as HTMLInputElement).value)" />
            </label>
          </div>
        </section>

        <section class="grid gap-2 border-t border-border-subtle pt-4" aria-labelledby="model-credential-heading">
          <h5 id="model-credential-heading" class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Credential</h5>
          <textarea v-if="googleServiceAccountMode" :value="apiKey" class="min-h-[140px] resize-y rounded-md border border-border-subtle bg-surface px-3 py-2.5 font-mono text-[12px] leading-5 text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50" :placeholder="apiKeyPlaceholder" autocomplete="off" :disabled="saving" @input="emit('update:apiKey', ($event.target as HTMLTextAreaElement).value)" />
          <input v-else :value="apiKey" class="h-10 rounded-md border border-border-subtle bg-surface px-3 text-[13px] text-text-primary outline-none transition placeholder:text-text-muted focus:border-accent/50" :placeholder="editingModelID ? `${apiKeyPlaceholder} (leave blank to keep current)` : apiKeyPlaceholder" type="password" autocomplete="off" :disabled="saving" @input="emit('update:apiKey', ($event.target as HTMLInputElement).value)" />
          <p class="text-[11px] leading-4 text-text-muted">{{ apiKeyHint || (editingModelID ? 'Leave blank to keep the current credential.' : 'A credential is required before this model can be selected in chat.') }}</p>
        </section>

        <footer class="flex flex-wrap items-center justify-end gap-2 border-t border-border-subtle pt-3">
          <button type="button" class="inline-flex h-9 items-center justify-center rounded-md border border-border-subtle px-3 text-[13px] font-medium text-text-secondary transition hover:bg-surface-hover" :disabled="saving" @click="emit('cancelEditor')">Cancel</button>
          <button class="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-accent bg-accent px-3 text-[13px] font-semibold text-surface shadow-[0_0_16px_var(--color-accent-glow)] transition hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-60 disabled:shadow-none" :disabled="saving || !name.trim() || !model.trim() || Boolean(baseURLError)">
            <Loader2 v-if="saving" class="h-4 w-4 animate-spin" :stroke-width="1.75" /><Check v-else class="h-4 w-4" :stroke-width="1.75" />
            {{ editingModelID ? 'Save changes' : 'Add model' }}
          </button>
        </footer>
      </form>
    </template>
  </section>
</template>
