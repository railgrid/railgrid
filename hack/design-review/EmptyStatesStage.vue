<script setup>
import { nextTick, ref } from 'vue'
import { Cpu, Server } from 'lucide-vue-next'
import FirstRunGuide from '../../provider-sdk/portalkit-vue/FirstRunGuide.vue'
import ResourceTable from '../../provider-sdk/portalkit-vue/ResourceTable.vue'

const notice = ref('')
const query = ref('database')
const failed = ref(true)
const failureTitle = ref(null)
async function retry() {
  failed.value = false
  await nextTick()
  failureTitle.value?.focus()
}
const columns = [{ key: 'name', label: 'Service', primary: true }, { key: 'edge', label: 'Edge' }]
const rows = [{ name: 'home-assistant', edge: 'home-server' }, { name: 'sonarr', edge: 'media-server' }]
const steps = [
  { label: 'Edge', description: 'Connect the cluster or server that can reach the service.' },
  { label: 'Service endpoint', description: 'Choose its type, address, protocol, and port.' },
  { label: 'Credentials and Ready', description: 'Add credentials when required and let the controller verify it.' },
]
</script>

<template>
  <div class="spec-stack empty-review">
    <p class="spec-note">First-use anatomy: an optional icon, title and explanation, clear primary action, and an optional prerequisite journey. These examples use synthetic data; no resources are created.</p>
    <div class="spec-row">
      <a class="review-source-link" href="http://127.0.0.1:5198/?provider=app&amp;route=~models">App Studio Models</a>
      <a class="review-source-link" href="http://127.0.0.1:5198/?provider=agents&amp;scenario=first-model&amp;route=%23%2Fmodels">Agents Models</a>
      <a class="review-source-link" href="http://127.0.0.1:5199/empty-product.html">Edge Services with an edge</a>
      <a class="review-source-link" href="http://127.0.0.1:5199/empty-product.html?scenario=no-edge">Edge Services without an edge</a>
    </div>

    <section class="empty-review__case" aria-labelledby="first-use-title">
      <div><h3 id="first-use-title">First use</h3><p class="spec-note">A successful empty read with one immediate action.</p></div>
      <FirstRunGuide title="Add a model" description="Choose a model from a connected provider to create projects and chat in App Studio." primary-label="Add model" :steps="[]" @primary="notice = 'Add model selected in the fixture.'"><template #icon><Cpu :stroke-width="1.75" /></template></FirstRunGuide>
    </section>

    <section class="empty-review__case" aria-labelledby="prerequisite-title">
      <div><h3 id="prerequisite-title">Prerequisite needed</h3><p class="spec-note">A guided path when another step must happen first.</p></div>
      <FirstRunGuide title="Connect an edge first" description="A Service must run beside an edge before Railgrid can expose its endpoint and tools." primary-label="Connect edge" :steps="steps" journey-label="Service setup path" @primary="notice = 'Connect edge selected in the fixture.'"><template #icon><Server aria-hidden="true" /></template></FirstRunGuide>
    </section>

    <section class="empty-review__case" aria-labelledby="no-results-title">
      <div class="spec-line"><div><h3 id="no-results-title">No results</h3><p class="spec-note">A search has no matches; clear it to return to the sample rows.</p></div><button v-if="query !== 'database'" class="k-btn k-btn--ghost" type="button" @click="query = 'database'">Reset search example</button></div>
      <ResourceTable v-model:query="query" :columns="columns" :rows="rows" aria-label="Example services" :loaded="true" searchable search-placeholder="Search services…" :search-keys="['name', 'edge']" search-empty-text="No services match your search." />
    </section>

    <section class="empty-review__case" aria-labelledby="load-failure-title">
      <div class="spec-line"><div><h3 id="load-failure-title" ref="failureTitle" tabindex="-1">Load failure</h3><p class="spec-note">The initial read failed; retry restores the sample rows.</p></div><button v-if="!failed" class="k-btn k-btn--ghost" type="button" @click="failed = true">Reset failure example</button></div>
      <ResourceTable :columns="columns" :rows="failed ? [] : rows" aria-label="Services after retry" :loaded="!failed" :error="failed ? 'Could not load services.' : null" retryable @retry="retry" />
    </section>
    <p class="spec-note" role="status" aria-live="polite">{{ notice }}</p>
  </div>
</template>

<style scoped>
.empty-review { gap: 28px; }
.empty-review__case { display: grid; gap: 12px; min-width: 0; }
.empty-review__case h3 { font-size: 14px; font-weight: 650; margin: 0 0 4px; }
.empty-review__case p { margin: 0; }
</style>
