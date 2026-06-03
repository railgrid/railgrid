<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import StatusBadge from '../components/StatusBadge.vue'
import { api } from '../api'
import type { Instance } from '../types'

const props = defineProps<{ instanceName: string }>()
const emit = defineEmits<{ (e: 'navigate', view: string): void }>()

const inst = ref<Instance | null>(null)
const error = ref<string | null>(null)
let pollHandle: number | null = null

async function refresh() {
  try {
    inst.value = await api.getInstance(props.instanceName)
    error.value = null
  } catch (e: unknown) {
    error.value = (e as { message?: string }).message ?? 'failed to get instance'
  }
}
onMounted(() => {
  refresh()
  pollHandle = window.setInterval(refresh, 10000)
})
onUnmounted(() => {
  if (pollHandle !== null) window.clearInterval(pollHandle)
})
watch(() => props.instanceName, refresh)

async function del() {
  if (!confirm(`Delete instance ${props.instanceName}? This removes the underlying resource and its bridged credentials Secret.`)) {
    return
  }
  try {
    await api.deleteInstance(props.instanceName)
    emit('navigate', 'instances')
  } catch (e: unknown) {
    error.value = (e as { message?: string }).message ?? 'delete failed'
  }
}
</script>

<template>
  <section class="page">
    <button class="link back" @click="emit('navigate', 'instances')">← Back to instances</button>
    <div v-if="error" class="error">Error: {{ error }}</div>
    <div v-else-if="!inst" class="muted">Loading…</div>
    <template v-else>
      <header class="page-head">
        <div>
          <h2 class="page-title">{{ inst.name }}</h2>
          <p class="page-meta">{{ inst.template }} · namespace <code>{{ inst.namespace }}</code></p>
        </div>
        <StatusBadge :phase="inst.phase" />
      </header>
      <p v-if="inst.message" class="status-message">{{ inst.message }}</p>

      <section class="panel">
        <h3 class="panel-title">Values</h3>
        <pre>{{ JSON.stringify(inst.values, null, 2) }}</pre>
      </section>

      <section v-if="inst.conditions && inst.conditions.length" class="panel">
        <h3 class="panel-title">Conditions</h3>
        <table class="table">
          <thead><tr><th>Type</th><th>Status</th><th>Reason</th><th>Message</th></tr></thead>
          <tbody>
            <tr v-for="c in inst.conditions" :key="c.type">
              <td>{{ c.type }}</td>
              <td><StatusBadge :phase="c.status" /></td>
              <td>{{ c.reason }}</td>
              <td class="muted">{{ c.message }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <section v-if="inst.children && inst.children.length" class="panel">
        <h3 class="panel-title">Child resources</h3>
        <table class="table">
          <thead><tr><th>Kind</th><th>Name</th><th>Namespace</th><th>Phase</th></tr></thead>
          <tbody>
            <tr v-for="c in inst.children" :key="c.kind + '/' + c.name">
              <td>{{ c.kind }}</td>
              <td>{{ c.name }}</td>
              <td>{{ c.namespace }}</td>
              <td>{{ c.phase }}</td>
            </tr>
          </tbody>
        </table>
      </section>

      <div class="actions">
        <button class="danger" @click="del">Delete instance</button>
      </div>
    </template>
  </section>
</template>
