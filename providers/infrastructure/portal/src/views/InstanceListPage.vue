<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import StatusBadge from '../components/StatusBadge.vue'
import { api } from '../api'
import type { Instance } from '../types'

const emit = defineEmits<{
  (e: 'navigate', view: string): void
  (e: 'select', name: string): void
}>()

const items = ref<Instance[]>([])
const error = ref<string | null>(null)
const loading = ref(true)
let pollHandle: number | null = null

async function refresh() {
  try {
    const r = await api.listInstances()
    items.value = r.items || []
    error.value = null
  } catch (e: unknown) {
    error.value = (e as { message?: string }).message ?? 'failed to list instances'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  refresh()
  // Poll every 10s while the page is mounted so the user sees status
  // transitions without a manual refresh. The window.setInterval cast
  // returns number in browsers; explicit type lets TS keep the
  // clearInterval signature happy.
  pollHandle = window.setInterval(refresh, 10000)
})
onUnmounted(() => {
  if (pollHandle !== null) window.clearInterval(pollHandle)
})
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">My instances</h2>
        <p class="page-meta">Updates automatically.</p>
      </div>
      <button class="link" @click="emit('navigate', 'catalog')">← Browse templates</button>
    </header>
    <div v-if="loading" class="muted">Loading…</div>
    <div v-else-if="error" class="error">Error: {{ error }}</div>
    <div v-else-if="items.length === 0" class="muted">
      <p>No instances in this workspace yet.</p>
      <p style="margin-top: 0.5rem; font-size: 0.85em;">
        Each workspace has its own instances — switching the active workspace
        in the portal sidebar changes what you see here.
        <a href="#" @click.prevent="emit('navigate', 'catalog')">Browse templates</a>
        to provision one.
      </p>
    </div>
    <table v-else class="table">
      <thead>
        <tr>
          <th>Name</th>
          <th>Template</th>
          <th>Phase</th>
          <th>Created</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="i in items" :key="i.name">
          <td><button class="link" @click="emit('select', i.name)">{{ i.name }}</button></td>
          <td>{{ i.template }}</td>
          <td><StatusBadge :phase="i.phase" /></td>
          <td class="muted">{{ i.createdAt }}</td>
          <td><button class="link" @click="emit('select', i.name)">Details →</button></td>
        </tr>
      </tbody>
    </table>
  </section>
</template>
