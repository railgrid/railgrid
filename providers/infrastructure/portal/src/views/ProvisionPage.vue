<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import DynamicForm from '../components/DynamicForm.vue'
import { api, isContextChangedError } from '../api'
import { createWritableValues } from '../createValues'
import type { Template, ErrorResponse } from '../types'
import { useDelayedLoading } from '../portalkit/useDelayedLoading'
import { REASON_CLOUD_CREDENTIALS_MISSING, REASON_API_BINDING_MISSING, REASON_TENANT_MISSING } from '../types'

const props = defineProps<{ templateName: string }>()
const emit = defineEmits<{
  (e: 'navigate', view: string, payload?: unknown): void
  (e: 'provisioned', instanceName: string): void
}>()

const template = ref<Template | null>(null)
const values = ref<Record<string, unknown>>({})
const instanceName = ref('')
const provisionForm = ref<HTMLFormElement | null>(null)
const loading = ref(true)
const loaded = ref(false)
const initialReadPending = computed(() => loading.value && !loaded.value)
const showInitialLoading = useDelayedLoading(initialReadPending)
const readError = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const submitting = ref(false)
let loadSerial = 0
let active = true

// Templates conventionally expose spec.name because the runtime CR needs it,
// while the platform Instance also has metadata.name. Show one authoritative
// control and mirror it into values only at submission.
const inputSchema = computed(() => {
  const schema = template.value?.inputsSchema
  if (!schema?.properties?.name) return schema ?? {}
  const { name: _name, ...properties } = schema.properties
  return {
    ...schema,
    properties,
    required: schema.required?.filter(field => field !== 'name'),
  }
})

const schemaUsesName = computed(() => Boolean(template.value?.inputsSchema?.properties?.name))

async function load() {
  const serial = ++loadSerial
  loading.value = true
  readError.value = null
  mutationError.value = null
  const firstLoad = template.value === null || template.value.name !== props.templateName
  if (firstLoad) {
    loaded.value = false
    template.value = null
    values.value = {}
    instanceName.value = ''
  }
  try {
    const r = await api.getTemplate(props.templateName)
    if (serial !== loadSerial || !active) return
    template.value = r.template
    // Seed only the first read for this route. A readiness recheck or retry
    // must not overwrite values the user has already entered.
    if (firstLoad) {
      values.value = { ...(r.template.sampleValues || {}) }
      if (typeof values.value['name'] === 'string') {
        instanceName.value = values.value['name'] as string
      }
    }
    loaded.value = true
  } catch (e: unknown) {
    if (serial !== loadSerial || !active || isContextChangedError(e)) return
    readError.value = (e as { message?: string }).message ?? 'failed to load template'
  } finally {
    if (serial === loadSerial && active) loading.value = false
  }
}
watch(() => props.templateName, load, { immediate: true })
onUnmounted(() => {
  active = false
  loadSerial += 1
})

async function submit() {
  if (!template.value || !loaded.value || loading.value || submitting.value) return
  const currentTemplate = template.value
  if (!instanceName.value.trim()) {
    mutationError.value = 'Enter an instance name.'
    provisionForm.value?.querySelector<HTMLInputElement>('#infrastructure-instance-name')?.focus()
    return
  }
  mutationError.value = null
  submitting.value = true
  try {
    const writableValues = createWritableValues(currentTemplate.inputsSchema, values.value)
    if (schemaUsesName.value && !currentTemplate.inputsSchema?.properties?.name?.readOnly) {
      writableValues.name = instanceName.value.trim()
    }
    const inst = await api.createInstance({
      templateName: currentTemplate.name,
      templateVersion: currentTemplate.version,
      name: instanceName.value.trim(),
      values: writableValues,
    })
    if (active) emit('provisioned', inst.name)
  } catch (e: unknown) {
    if (!active || isContextChangedError(e)) return
    const err = e as ErrorResponse
    if (err.reason === REASON_CLOUD_CREDENTIALS_MISSING) {
      emit('navigate', 'missing-credentials')
      return
    }
    if (err.reason === REASON_API_BINDING_MISSING) {
      mutationError.value = 'This provider is not enabled in your workspace. Click Enable in the faros portal first.'
      return
    }
    if (err.reason === REASON_TENANT_MISSING) {
      mutationError.value = 'The selected workspace is no longer available. Choose a workspace in the sidebar, then try again.'
      return
    }
    mutationError.value = err.message || 'provision failed'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <section class="page k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="submitting" @click="emit('navigate', 'catalog')"><ArrowLeft :size="14" aria-hidden="true" /> Back to templates</button>
    <div
      v-if="initialReadPending"
      class="page-loading-shell k-delayed-loading"
      :role="showInitialLoading ? 'status' : undefined"
      :aria-live="showInitialLoading ? 'polite' : undefined"
      :aria-busy="showInitialLoading ? 'true' : undefined"
      :aria-hidden="showInitialLoading ? undefined : 'true'"
    >
      <span>Loading template…</span>
      <div class="shimmer page-loading-line page-loading-line-short" aria-hidden="true" />
      <div class="shimmer page-loading-panel" aria-hidden="true" />
    </div>
    <div v-else-if="!template && readError" class="read-error" role="alert" aria-live="assertive">
      <span>{{ readError }}</span>
      <button type="button" class="k-btn k-btn--ghost" @click="load">Retry</button>
    </div>
    <template v-else-if="template">
      <header class="k-create-header">
        <h2 class="k-create-title">Provision {{ template.displayName }}</h2>
        <p class="k-create-description">{{ template.description }}</p>
      </header>
      <div v-if="readError" class="stale-banner" role="alert" aria-live="assertive">
        <span>Showing the last successful template. {{ readError }}</span>
        <button type="button" class="k-btn k-btn--ghost" @click="load">Retry</button>
      </div>
      <span v-if="loading" class="sr-only" role="status" aria-live="polite">Rechecking template…</span>
      <form ref="provisionForm" class="k-create-surface k-create-surface--wide" :aria-busy="submitting || loading" @submit.prevent="submit">
        <div class="k-create-body">
          <div class="provision-identity">
            <div class="dynform-row">
              <label for="infrastructure-instance-name">
                <span class="dynform-label">Instance name<span class="required">*</span></span>
                <span class="dynform-desc">DNS-1123 subdomain. Lowercase alnum, '-', '.'.</span>
              </label>
              <input
                id="infrastructure-instance-name"
                v-model="instanceName"
                class="k-input"
                placeholder="my-instance"
                autocomplete="off"
                required
                aria-required="true"
                pattern="[a-z0-9]([-a-z0-9.]*[a-z0-9])?"
                maxlength="253"
                :aria-invalid="mutationError && !instanceName.trim() ? 'true' : undefined"
                :aria-describedby="mutationError ? 'infrastructure-provision-error' : undefined"
              />
            </div>
          </div>
          <DynamicForm :schema="inputSchema" v-model:values="values" />
          <div v-if="mutationError" id="infrastructure-provision-error" class="read-error" role="alert" aria-live="assertive">{{ mutationError }}</div>
          <span v-if="submitting" class="sr-only" role="status" aria-live="polite">Provisioning instance…</span>
        </div>
        <div class="k-create-actions">
          <button type="button" class="k-btn k-btn--ghost" :disabled="submitting" @click="emit('navigate', 'catalog')">Cancel</button>
          <button type="submit" class="k-btn k-btn--primary" :disabled="submitting || loading">
            {{ submitting ? 'Provisioning…' : 'Provision' }}
          </button>
        </div>
      </form>
    </template>
  </section>
</template>
