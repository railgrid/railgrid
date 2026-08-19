<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import type { ErrorResponse, PackageRow } from '../types'
import ResourceTable from '../portalkit/ResourceTable.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { createLatestRefreshController, type LatestRefreshController } from '../refresh'

const emit = defineEmits<{ (e: 'open', repositoryRef: string): void }>()

const packages = ref<PackageRow[]>([])
const error = ref<string | null>(null)
const loading = ref(false)
const loaded = ref(false)
const columns = [
  { key: 'repositoryRef', label: 'Repository' },
  { key: 'name', label: 'Package' },
  { key: 'type', label: 'Type' },
  { key: 'visibility', label: 'Visibility' },
  { key: 'versionCount', label: 'Versions' },
  { key: 'status', label: 'Status' },
  { key: 'url', label: '' },
]
function controllerCaughtUp(resource: { generation?: number; observedGeneration?: number }): boolean {
  return resource.generation === undefined ||
    (resource.observedGeneration !== undefined && resource.observedGeneration >= resource.generation)
}
const rows = computed<Array<Record<string, unknown>>>(() => {
  let previousRepository = ''
  return [...packages.value]
    .sort((a, b) => a.repositoryRef.localeCompare(b.repositoryRef) || a.type.localeCompare(b.type) || a.name.localeCompare(b.name))
    .map(item => {
      const showRepository = item.repositoryRef !== previousRepository
      previousRepository = item.repositoryRef
      const deleting = !!item.deletionTimestamp
      return {
        ...item,
        deleting,
        rowKey: `${item.repositoryRef}:${item.uid || `${item.type}/${item.name}`}`,
        showRepository,
        status: deleting ? 'Deleting' : !controllerCaughtUp(item) ? 'pending' : item.ready ? 'ready' : item.message ? 'failed' : 'pending',
        url: item.htmlURL || '',
      }
    })
})

let timer: number | undefined
let refresh!: LatestRefreshController

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function load() {
  refresh.request()
}

refresh = createLatestRefreshController(async requestID => {
  loading.value = true
  try {
    const next = await api.listAllPackages()
    if (!refresh.isCurrent(requestID)) return
    packages.value = next
    loaded.value = true
    error.value = null
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (refresh.isCurrent(requestID)) loading.value = false
  }
})

onMounted(() => {
  load()
  timer = window.setInterval(load, 5000)
})
onUnmounted(() => {
  window.clearInterval(timer)
  refresh.stop()
})
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Packages</h2>
        <p class="page-meta">Artifacts (container images, npm/maven packages, …) published under the workspace's repositories. Observed state — they appear automatically when artifacts are pushed.</p>
      </div>
    </header>

    <ResourceTable
      :columns="columns"
      :rows="rows"
      row-key="rowKey"
      :loaded="loaded"
      :loading="loading"
      :error="error"
      :stale="loaded && !!error"
      retryable
      empty-text="No packages published in this workspace yet."
      :interactive="false"
      @retry="load"
    >
      <template #repositoryRef="{ row }">
        <button v-if="row.showRepository && !row.deleting" class="link" type="button" @click="emit('open', String(row.repositoryRef))">{{ row.repositoryRef }}</button>
        <span v-else-if="row.showRepository">{{ row.repositoryRef }}</span>
        <span v-else class="muted">↳</span>
      </template>
      <template #name="{ row }"><strong><a v-if="row.htmlURL && !row.deleting" :href="String(row.htmlURL)" target="_blank" rel="noopener">{{ row.name }}</a><template v-else>{{ row.name }}</template></strong></template>
      <template #type="{ value }"><span class="badge muted">{{ value }}</span></template>
      <template #visibility="{ value }"><span class="muted">{{ value || '—' }}</span></template>
      <template #versionCount="{ value }"><span class="muted">{{ value || 0 }}</span></template>
      <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
      <template #url="{ row }"><a v-if="row.htmlURL && !row.deleting" class="link" :href="String(row.htmlURL)" target="_blank" rel="noopener">View ↗</a></template>
    </ResourceTable>
    <p class="muted">Packages appear automatically when artifacts are pushed (e.g. <code>docker push</code>, <code>npm publish</code>); the provider crawls each repository periodically.</p>
  </section>
</template>
