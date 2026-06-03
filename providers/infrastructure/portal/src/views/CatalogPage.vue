<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import TemplateCard from '../components/TemplateCard.vue'
import { api } from '../api'
import type { Template } from '../types'

const emit = defineEmits<{
  (e: 'select', name: string): void
  (e: 'navigate', view: string): void
}>()

const loading = ref(true)
const error = ref<string | null>(null)
const templates = ref<Template[]>([])
const category = ref('')
const cloud = ref('')

const categories = computed(() => uniq(templates.value.map(t => t.category || 'Other')))
const clouds = computed(() => uniq(templates.value.flatMap(t => t.cloud ? [t.cloud] : [])))

function uniq(xs: string[]): string[] {
  return Array.from(new Set(xs)).sort()
}

const filtered = computed(() => templates.value.filter(t => {
  if (category.value && (t.category || 'Other') !== category.value) return false
  if (cloud.value && t.cloud !== cloud.value) return false
  return true
}))

async function load() {
  loading.value = true
  error.value = null
  try {
    const r = await api.listTemplates()
    templates.value = r.items || []
  } catch (e: unknown) {
    error.value = (e as { message?: string }).message ?? 'failed to load templates'
  } finally {
    loading.value = false
  }
}
onMounted(load)
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Templates</h2>
        <p class="page-meta">Pick a template to provision into your tenant scope.</p>
      </div>
      <button class="link" @click="emit('navigate', 'instances')">My instances →</button>
    </header>

    <div v-if="categories.length > 1 || clouds.length > 0" class="filters">
      <select v-model="category">
        <option value="">All categories</option>
        <option v-for="c in categories" :key="c" :value="c">{{ c }}</option>
      </select>
      <select v-if="clouds.length > 0" v-model="cloud">
        <option value="">All clouds</option>
        <option v-for="c in clouds" :key="c" :value="c">{{ c }}</option>
      </select>
    </div>

    <div v-if="loading" class="muted">Loading templates…</div>
    <div v-else-if="error" class="error">Error: {{ error }}</div>
    <div v-else-if="filtered.length === 0" class="muted">No templates match the current filters.</div>
    <div v-else class="grid">
      <TemplateCard
        v-for="t in filtered"
        :key="t.name"
        :template="t"
        @select="emit('select', $event)"
      />
    </div>
  </section>
</template>
