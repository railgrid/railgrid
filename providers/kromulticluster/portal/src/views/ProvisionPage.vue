<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import DynamicForm from '../components/DynamicForm.vue'
import { api } from '../api'
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
const error = ref<string | null>(null)
const submitting = ref(false)

async function load() {
  try {
    const r = await api.getTemplate(props.templateName)
    template.value = r.template
    // Seed form values from sampleValues when provided.
    values.value = { ...(r.template.sampleValues || {}) }
    if (typeof values.value['name'] === 'string') {
      instanceName.value = values.value['name'] as string
    }
  } catch (e: unknown) {
    error.value = (e as { message?: string }).message ?? 'failed to load template'
  }
}
onMounted(load)
watch(() => props.templateName, load)

async function submit() {
  if (!template.value) return
  if (!instanceName.value) {
    error.value = 'instance name required'
    return
  }
  error.value = null
  submitting.value = true
  try {
    const inst = await api.createInstance({
      templateName: template.value.name,
      templateVersion: template.value.version,
      name: instanceName.value,
      values: values.value,
    })
    emit('provisioned', inst.name)
  } catch (e: unknown) {
    const err = e as ErrorResponse
    if (err.reason === REASON_CLOUD_CREDENTIALS_MISSING) {
      emit('navigate', 'missing-credentials')
      return
    }
    if (err.reason === REASON_API_BINDING_MISSING) {
      error.value = 'This provider is not enabled in your workspace. Click Enable in the kedge portal first.'
      return
    }
    if (err.reason === REASON_TENANT_MISSING) {
      error.value = 'No tenant identity on this request — the kedge hub did not inject X-Kedge-Tenant. (Phase-3 hub wiring required.)'
      return
    }
    error.value = err.message || 'provision failed'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <section class="page">
    <button class="link back" @click="emit('navigate', 'catalog')">← Back to templates</button>
    <div v-if="!template" class="muted">Loading template…</div>
    <template v-else>
      <header class="page-head">
        <div>
          <h2 class="page-title">Provision {{ template.displayName }}</h2>
          <p class="page-meta">{{ template.description }}</p>
        </div>
      </header>
      <form class="form" @submit.prevent="submit">
        <div class="dynform-row">
          <label>
            <span class="dynform-label">Instance name<span class="required">*</span></span>
            <span class="dynform-desc">DNS-1123 subdomain. Lowercase alnum, '-', '.'.</span>
          </label>
          <input v-model="instanceName" placeholder="my-instance" />
        </div>
        <DynamicForm :schema="template.inputsSchema" v-model:values="values" />
        <div v-if="error" class="error">Error: {{ error }}</div>
        <div class="actions">
          <button type="submit" class="primary" :disabled="submitting">
            {{ submitting ? 'Provisioning…' : 'Provision' }}
          </button>
          <button type="button" class="link" @click="emit('navigate', 'catalog')">Cancel</button>
        </div>
      </form>
    </template>
  </section>
</template>
