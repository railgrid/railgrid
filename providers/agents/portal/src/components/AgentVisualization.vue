<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Result } from 'vega-embed'
import { visualizationSpec, type VisualizationChart } from '../visualization'
const props = defineProps<{ chart: VisualizationChart }>()
const root = ref<HTMLElement | null>(null)
const canvas = ref<HTMLElement | null>(null)
const loading = ref(true)
const failed = ref(false)
const theme = ref(0)
const fields = computed(() => [...new Set(props.chart.data.flatMap(row => Object.keys(row)))])
let result: Result | undefined
let generation = 0
let resize: ResizeObserver | undefined
let observer: MutationObserver | undefined
async function render(): Promise<void> {
  const ticket = ++generation
  loading.value = true
  failed.value = false
  if (!root.value || !canvas.value) return
  const styles = getComputedStyle(root.value)
  const token = (name: string) => styles.getPropertyValue(`--color-${name}`).trim()
  const colors = { text: token('text-primary'), grid: token('border'), accent: token('accent'), palette: ['accent', 'success', 'warning', 'danger', 'text-secondary'].map(token) }
  const host = document.createElement('div')
  const width = canvas.value.clientWidth || 300
  try {
    const [{ default: embed }, { loader }] = await Promise.all([import('vega-embed'), import('vega')])
    if (ticket !== generation) return
    const denyLoader = loader()
    denyLoader.load = async () => { throw new Error('External chart resources are disabled') }
    denyLoader.sanitize = async () => { throw new Error('External chart resources are disabled') }
    const next = await embed(host, visualizationSpec(props.chart, width, colors), {
      mode: 'vega-lite', renderer: 'svg', ast: true, actions: false,
      defaultStyle: false, tooltip: false, loader: denyLoader,
    })
    if (ticket !== generation || !canvas.value) { next.finalize(); return }
    result?.finalize()
    result = next
    canvas.value.replaceChildren(host)
  } catch {
    if (ticket === generation) { result?.finalize(); result = undefined; canvas.value?.replaceChildren(); failed.value = true }
  } finally { if (ticket === generation) loading.value = false }
}
async function download(): Promise<void> {
  if (!result) return
  try {
    const svg = await result.view.toSVG()
    const url = URL.createObjectURL(new Blob([svg], { type: 'image/svg+xml' }))
    const link = document.createElement('a')
    link.href = url
    link.download = `${props.chart.title.replace(/[^a-zA-Z0-9_-]+/g, '-').slice(0, 80) || 'chart'}.svg`
    link.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch { failed.value = true }
}
onMounted(() => {
  observer = new MutationObserver(() => { theme.value++ })
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'style', 'data-theme'] })
  resize = new ResizeObserver(() => {
    const current = result
    if (canvas.value && current) void current.view.width(Math.max(120, canvas.value.clientWidth)).resize().runAsync().catch(() => { if (result === current) failed.value = true })
  })
  if (canvas.value) resize.observe(canvas.value)
  void render()
})
watch([() => JSON.stringify(props.chart), theme], () => { void render() })
onBeforeUnmount(() => { generation++; resize?.disconnect(); observer?.disconnect(); result?.finalize() })
</script>
<template>
  <figure ref="root" class="agents-visualization">
    <figcaption>
      <h3 class="agents-visualization__title">{{ chart.title }}</h3>
      <p v-if="chart.description">{{ chart.description }}</p>
    </figcaption>
    <p v-if="loading" role="status">Preparing chart…</p>
    <p v-if="failed" role="status">The chart could not be displayed or exported. View the data below.</p>
    <div ref="canvas" class="agents-visualization__canvas" :aria-label="chart.title" />
    <p v-if="chart.source" class="agents-visualization__note">Source: {{ chart.source }}</p>
    <p v-if="chart.xType === 'temporal'" class="agents-visualization__note">Times shown in UTC.</p>
    <button type="button" class="k-btn k-btn--ghost" :disabled="loading || failed" @click="download">Download SVG</button>
    <details class="agents-visualization__data">
      <summary>View data ({{ chart.data.length }} rows)</summary>
      <div class="agents-visualization__table" tabindex="0" role="region" :aria-label="`${chart.title} data`">
        <table class="k-table">
          <caption>{{ chart.title }} — data</caption>
          <thead><tr><th v-for="field in fields" :key="field" scope="col">{{ field }}</th></tr></thead>
          <tbody><tr v-for="(row, index) in chart.data" :key="index"><td v-for="field in fields" :key="field">{{ row[field] ?? '—' }}</td></tr></tbody>
        </table>
      </div>
    </details>
  </figure>
</template>
