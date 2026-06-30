<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import type { ErrorResponse, PackageRow } from '../types'

const emit = defineEmits<{ (e: 'open', repositoryRef: string): void }>()

const packages = ref<PackageRow[]>([])
const error = ref<string | null>(null)
const loading = ref(false)

// Group by repository so the table reads "repo → its artifacts" rather than a
// flat list; repositories sort alphabetically, packages by type then name.
const grouped = computed(() => {
  const byRepo = new Map<string, PackageRow[]>()
  for (const p of packages.value) {
    const list = byRepo.get(p.repositoryRef) ?? []
    list.push(p)
    byRepo.set(p.repositoryRef, list)
  }
  return [...byRepo.entries()]
    .map(([repositoryRef, items]) => ({
      repositoryRef,
      items: items.slice().sort((a, b) => a.type.localeCompare(b.type) || a.name.localeCompare(b.name)),
    }))
    .sort((a, b) => a.repositoryRef.localeCompare(b.repositoryRef))
})

async function load() {
  loading.value = true
  error.value = null
  try {
    packages.value = await api.listAllPackages()
  } catch (e) {
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : `${err.reason}: ${err.message}`
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  load()
  timer = window.setInterval(load, 5000)
})
let timer: number | undefined
onUnmounted(() => window.clearInterval(timer))
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Packages</h2>
        <p class="page-meta">Artifacts (container images, npm/maven packages, …) published under the workspace's repositories. Observed state — they appear automatically when artifacts are pushed.</p>
      </div>
    </header>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-else-if="loading && !packages.length" class="muted">Loading…</p>
    <p v-else-if="!packages.length" class="empty">No packages published in this workspace yet.</p>

    <div v-else class="panel">
      <table class="table">
        <thead>
          <tr><th>Repository</th><th>Package</th><th>Type</th><th>Visibility</th><th>Versions</th><th>Status</th><th class="right"></th></tr>
        </thead>
        <tbody>
          <template v-for="g in grouped" :key="g.repositoryRef">
            <tr v-for="(p, i) in g.items" :key="p.type + '/' + p.name">
              <td>
                <button v-if="i === 0" class="link" @click="emit('open', g.repositoryRef)">{{ g.repositoryRef }}</button>
                <span v-else class="muted">↳</span>
              </td>
              <td>
                <strong v-if="p.htmlURL"><a :href="p.htmlURL" target="_blank" rel="noopener">{{ p.name }}</a></strong>
                <strong v-else>{{ p.name }}</strong>
              </td>
              <td><span class="badge muted">{{ p.type }}</span></td>
              <td><span class="muted">{{ p.visibility || '—' }}</span></td>
              <td><span class="muted">{{ p.versionCount || 0 }}</span></td>
              <td>
                <span v-if="p.ready" class="badge ok">synced</span>
                <span v-else class="badge warn" :title="p.message">{{ p.message ? 'error' : 'pending' }}</span>
              </td>
              <td class="right">
                <a v-if="p.htmlURL" class="link" :href="p.htmlURL" target="_blank" rel="noopener">View ↗</a>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
      <p class="muted">Packages appear automatically when artifacts are pushed (e.g. <code>docker push</code>, <code>npm publish</code>); the provider crawls each repository periodically.</p>
    </div>
  </section>
</template>
