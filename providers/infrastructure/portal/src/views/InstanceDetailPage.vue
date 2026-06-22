<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import StatusBadge from '../components/StatusBadge.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import { api } from '../api'
import type { Instance } from '../types'

const props = defineProps<{ instanceName: string }>()
const emit = defineEmits<{ (e: 'navigate', view: string): void }>()

const inst = ref<Instance | null>(null)
const error = ref<string | null>(null)
let pollHandle: number | null = null

const showDelete = ref(false)
const deleting = ref(false)
const deleteError = ref<string | null>(null)

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

async function executeDelete() {
  deleting.value = true
  deleteError.value = null
  try {
    await api.deleteInstance(props.instanceName)
    emit('navigate', 'instances')
  } catch (e: unknown) {
    deleteError.value = (e as { message?: string }).message ?? 'delete failed'
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div>
    <button
      class="mb-4 inline-flex items-center gap-1.5 text-[12px] font-medium text-text-muted transition-colors hover:text-text-primary"
      @click="emit('navigate', 'instances')"
    >
      <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <line x1="19" y1="12" x2="5" y2="12" /><polyline points="12 19 5 12 12 5" />
      </svg>
      Back to instances
    </button>

    <div v-if="error" class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle p-4 text-[13px] text-danger">
      {{ error }}
    </div>
    <div v-else-if="!inst" class="p-6 text-[12px] text-text-muted">Loading…</div>

    <template v-else>
      <!-- Header -->
      <div class="mb-5 flex items-start gap-3 flex-wrap">
        <div>
          <div class="flex items-center gap-2.5">
            <h2 class="text-[16px] font-bold text-text-primary">{{ inst.name }}</h2>
            <StatusBadge :phase="inst.phase" />
          </div>
          <p class="mt-0.5 text-[12px] text-text-muted">{{ inst.template }}</p>
        </div>
        <div class="ml-auto">
          <button
            class="flex items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-overlay/80 px-3 py-1.5 text-[11px] font-medium text-text-secondary transition-all hover:border-danger/30 hover:bg-danger-subtle hover:text-danger"
            @click="showDelete = true"
          >
            <svg viewBox="0 0 24 24" class="h-3 w-3" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
              <line x1="10" y1="11" x2="10" y2="17" /><line x1="14" y1="11" x2="14" y2="17" />
            </svg>
            Delete
          </button>
        </div>
      </div>

      <div v-if="inst.message" class="mb-5 rounded-lg border border-border-subtle bg-surface-overlay px-3 py-2 text-[12px] text-text-secondary">
        {{ inst.message }}
      </div>

      <!-- Values -->
      <div class="mb-5 overflow-hidden rounded-2xl border border-border-subtle bg-surface-raised">
        <div class="border-b border-border-subtle px-4 py-2.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Values</div>
        <pre class="overflow-auto p-4 font-mono text-[12px] leading-relaxed text-text-secondary">{{ JSON.stringify(inst.values, null, 2) }}</pre>
      </div>

      <!-- Conditions -->
      <div v-if="inst.conditions && inst.conditions.length" class="mb-5 overflow-hidden rounded-2xl border border-border-subtle bg-surface-raised">
        <div class="border-b border-border-subtle px-4 py-2.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Conditions</div>
        <table class="w-full border-collapse text-left">
          <thead>
            <tr class="border-b border-border-subtle">
              <th class="px-4 py-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Type</th>
              <th class="px-4 py-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Status</th>
              <th class="px-4 py-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Reason</th>
              <th class="px-4 py-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Message</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in inst.conditions" :key="c.type" class="border-b border-border-subtle/60 last:border-0">
              <td class="px-4 py-2.5 text-[12px] text-text-primary">{{ c.type }}</td>
              <td class="px-4 py-2.5"><StatusBadge :phase="c.status" /></td>
              <td class="px-4 py-2.5 text-[12px] text-text-secondary">{{ c.reason }}</td>
              <td class="px-4 py-2.5 text-[12px] text-text-muted">{{ c.message }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Child resources -->
      <div v-if="inst.children && inst.children.length" class="mb-5 overflow-hidden rounded-2xl border border-border-subtle bg-surface-raised">
        <div class="border-b border-border-subtle px-4 py-2.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Child resources</div>
        <table class="w-full border-collapse text-left">
          <thead>
            <tr class="border-b border-border-subtle">
              <th class="px-4 py-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Kind</th>
              <th class="px-4 py-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Name</th>
              <th class="px-4 py-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Namespace</th>
              <th class="px-4 py-2 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Phase</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in inst.children" :key="c.kind + '/' + c.name" class="border-b border-border-subtle/60 last:border-0">
              <td class="px-4 py-2.5 text-[12px] text-text-primary">{{ c.kind }}</td>
              <td class="px-4 py-2.5 text-[12px] text-text-secondary">{{ c.name }}</td>
              <td class="px-4 py-2.5 text-[12px] text-text-muted">{{ c.namespace }}</td>
              <td class="px-4 py-2.5 text-[12px] text-text-secondary">{{ c.phase }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <ConfirmDialog
      v-if="showDelete"
      title="Delete instance?"
      :message="`This permanently deletes ${props.instanceName} and the resources (and bridged credentials Secret) it provisioned. This cannot be undone.`"
      confirm-label="Delete"
      :busy="deleting"
      :error="deleteError"
      @cancel="showDelete = false"
      @confirm="executeDelete"
    />
  </div>
</template>
