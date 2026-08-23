<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import DynamicForm from '../components/DynamicForm.vue'
import { api, isContextChangedError } from '../api'
import type { Template, ErrorResponse } from '../types'
import { REASON_CLOUD_CREDENTIALS_MISSING, REASON_API_BINDING_MISSING, REASON_TENANT_MISSING } from '../types'

const props = defineProps<{ templateName: string }>()
const emit = defineEmits<{
  (e: 'navigate', view: string, payload?: unknown): void
  (e: 'provisioned', instanceName: string): void
}>()

const template = ref<Template | null>(null)
const values = ref<Record<string, unknown>>({})
const instanceName = ref('')
const loading = ref(true)
const loaded = ref(false)
const readError = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const submitting = ref(false)
let loadSerial = 0
let active = true

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
  if (!instanceName.value) {
    mutationError.value = 'instance name required'
    return
  }
  mutationError.value = null
  submitting.value = true
  try {
    const inst = await api.createInstance({
      templateName: currentTemplate.name,
      templateVersion: currentTemplate.version,
      name: instanceName.value,
      values: values.value,
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
      mutationError.value = 'No tenant identity on this request — the faros hub did not inject X-Faros-Tenant. (Phase-3 hub wiring required.)'
      return
    }
    mutationError.value = err.message || 'provision failed'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <section class="page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="submitting" @click="emit('navigate', 'catalog')"><ArrowLeft :size="14" aria-hidden="true" /> Back to templates</button>
    <div v-if="loading && !loaded" class="page-loading-shell" role="status" aria-live="polite" aria-busy="true">
      <span>Loading template…</span>
      <div class="shimmer page-loading-line page-loading-line-short" aria-hidden="true" />
      <div class="shimmer page-loading-panel" aria-hidden="true" />
    </div>
    <div v-else-if="!template && readError" class="read-error" role="alert" aria-live="assertive">
      <span>{{ readError }}</span>
      <button type="button" class="k-btn k-btn--ghost" @click="load">Retry</button>
    </div>
    <template v-else-if="template">
      <header class="page-head">
        <div>
          <h2 class="page-title">Provision {{ template.displayName }}</h2>
          <p class="page-meta">{{ template.description }}</p>
        </div>
      </header>
      <div v-if="readError" class="stale-banner" role="alert" aria-live="assertive">
        <span>Showing the last successful template. {{ readError }}</span>
        <button type="button" class="k-btn k-btn--ghost" @click="load">Retry</button>
      </div>
      <span v-if="loading" class="sr-only" role="status" aria-live="polite">Rechecking template…</span>
      <form class="form" :aria-busy="submitting || loading" @submit.prevent="submit">
        <div class="dynform-row">
          <label for="infrastructure-instance-name">
            <span class="dynform-label">Instance name<span class="required">*</span></span>
            <span class="dynform-desc">DNS-1123 subdomain. Lowercase alnum, '-', '.'.</span>
          </label>
          <input id="infrastructure-instance-name" v-model="instanceName" class="k-input" placeholder="my-instance" />
        </div>
        <DynamicForm :schema="template.inputsSchema" v-model:values="values" />
        <div v-if="mutationError" class="read-error" role="alert" aria-live="assertive">{{ mutationError }}</div>
        <span v-if="submitting" class="sr-only" role="status" aria-live="polite">Provisioning instance…</span>
        <div class="actions">
          <button type="submit" class="k-btn k-btn--primary" :disabled="submitting || loading">
            {{ submitting ? 'Provisioning…' : 'Provision' }}
          </button>
          <button type="button" class="k-btn k-btn--ghost" :disabled="submitting" @click="emit('navigate', 'catalog')">Cancel</button>
        </div>
      </form>
    </template>
  </section>
</template>
