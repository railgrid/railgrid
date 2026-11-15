<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Activity, Clock3, Layers } from 'lucide-vue-next'
import ResourcePage from '../../provider-sdk/portalkit-vue/ResourcePage.vue'
import ResourceSectionCard from '../../provider-sdk/portalkit-vue/ResourceSectionCard.vue'
import ResourceStatCards from '../../provider-sdk/portalkit-vue/ResourceStatCards.vue'
import ResourceTable from '../../provider-sdk/portalkit-vue/ResourceTable.vue'
import StatusBadge from '../../provider-sdk/portalkit-vue/StatusBadge.vue'

defineProps({ entry: Object })
const surface = ref('detail')
const state = ref('ready')
const retryPending = ref(false)
let retryTimer
function cancelRetry() {
  clearTimeout(retryTimer)
  retryPending.value = false
}
function retry() {
  retryPending.value = true
  retryTimer = setTimeout(() => {
    retryPending.value = false
    state.value = 'ready'
  }, 1500)
}
watch(state, cancelRetry)
onBeforeUnmount(cancelRetry)
const loaded = computed(() => !['loading', 'error'].includes(state.value))
const loading = computed(() => retryPending.value || ['loading', 'refresh'].includes(state.value))
const error = computed(() => ['error', 'stale'].includes(state.value) ? 'Could not load deployment details.' : null)
const facts = [
  { id: 'revision', label: 'Revision', value: 'v42', detail: 'Previous revision v41', icon: Layers, mono: true },
  { id: 'replicas', label: 'Ready replicas', value: '2 / 3', detail: 'One replica awaiting readiness', icon: Activity },
  { id: 'updated', label: 'Last updated', value: '14:32 UTC', detail: '12 November 2026', icon: Clock3 },
]
const columns = [{ key: 'name', label: 'Deployment', primary: true }, { key: 'status', label: 'Status' }]
const rows = [{ name: 'api-production', status: 'Needs attention' }, { name: 'web-production', status: 'Ready' }]
</script>

<template>
  <div class="spec-stack">
    <p v-if="entry?.id === 'design.components.loading-placeholder'" class="spec-note">Anatomy: a stable page shell, decorative shapes that suggest the final layout, and a separate loading status. Compare first load with the retained-data states.</p>
    <div class="spec-row">
      <label class="spec-field spec-fit">Page<select v-model="surface" class="k-input"><option value="detail">Detail</option><option value="collection">Collection</option></select></label>
      <label class="spec-field spec-fit">Read state<select v-model="state" class="k-input"><option value="ready">Loaded</option><option value="loading">First read loading</option><option value="error">First read failed</option><option value="refresh">Background refresh</option><option value="stale">Refresh failed</option></select></label>
    </div>
    <ResourcePage v-if="surface === 'detail'" title="api-production" kind="Deployment" :loaded="loaded" :loading="loading" :error="error" :stale="state === 'stale'" :refresh-mode="state === 'refresh' || state === 'stale' ? 'background' : 'foreground'" retryable @retry="retry">
      <template v-if="loaded" #meta>production · east cluster</template>
      <template v-if="loaded" #status><StatusBadge status="Needs attention" /></template>
      <template #summary><ResourceStatCards :cards="facts" density="compact" aria-label="Deployment summary" /></template>
      <template #body><ResourceSectionCard title="Rollout needs attention" description="One replica in revision v42 is not ready."><p class="spec-note">Two replicas are serving traffic. Review the readiness check before continuing.</p></ResourceSectionCard></template>
    </ResourcePage>
    <ResourcePage v-else title="Deployments" subtitle="Manage deployments in this workspace." variant="collection" :loaded="loaded" :loading="loading" :error="error" :stale="state === 'stale'" :refresh-mode="state === 'refresh' || state === 'stale' ? 'background' : 'foreground'" retryable @retry="retry">
      <template #actions><button type="button" class="k-btn k-btn--ghost" :disabled="!loaded || loading" @click="state = 'refresh'">Refresh</button></template>
      <ResourceTable :columns="columns" :rows="rows" variant="simple" aria-label="Example deployments" />
    </ResourcePage>
    <section v-if="entry?.id === 'design.components.loading-placeholder'" class="spec-section spec-stack" aria-labelledby="table-loading-example">
      <div><h3 id="table-loading-example">Table read state</h3><p class="spec-note">The same read-state selector drives a table directly, so its own column and search placeholders can be compared with the page shell above.</p></div>
      <ResourceTable :columns="columns" :rows="loaded ? rows : []" :loaded="loaded" :loading="loading" :error="error" :stale="state === 'stale'" :refresh-mode="state === 'refresh' || state === 'stale' ? 'background' : 'foreground'" retryable searchable aria-label="Example deployments" @retry="retry" />
    </section>
    <p class="spec-note">Synthetic data; the fixture controls simulate reads and do not contact a backend.</p>
    <div class="spec-row"><a class="k-btn k-btn--ghost" href="http://127.0.0.1:5201/" target="_blank" rel="noopener">Open deployment detail fixture</a><a class="k-btn k-btn--ghost" href="http://127.0.0.1:5199/" target="_blank" rel="noopener">Open Edge Services fixture</a></div>
  </div>
</template>
