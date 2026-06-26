<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import StatusBadge from '../components/StatusBadge.vue'
import ViewValue from '../components/ViewValue.vue'
import { api } from '../api'
import { resolve } from '../view'
import type { Instance, TemplateView, ViewColumn } from '../types'

const emit = defineEmits<{
  (e: 'navigate', view: string): void
  (e: 'select', name: string): void
}>()

const items = ref<Instance[]>([])
const error = ref<string | null>(null)
const loading = ref(true)
let pollHandle: number | null = null

// Per-template view metadata, fetched once: drives the extra table columns each
// template defines. Templates change rarely, so we don't refresh this on poll.
const viewByTemplate = ref<Map<string, TemplateView>>(new Map())

// The extra columns to render, as the ordered union of every present template's
// column headers (templates can differ; a row only fills headers its own
// template defines). Empty when no template defines columns → table unchanged.
const extraColumns = computed<string[]>(() => {
  const headers: string[] = []
  for (const inst of items.value) {
    const cols = viewByTemplate.value.get(inst.template)?.columns ?? []
    for (const c of cols) if (!headers.includes(c.header)) headers.push(c.header)
  }
  return headers
})

// columnFor finds the column definition a given instance's template provides
// for a header (templates may define different columns under the same header).
function columnFor(inst: Instance, header: string): ViewColumn | undefined {
  return viewByTemplate.value.get(inst.template)?.columns?.find(c => c.header === header)
}

async function loadViews() {
  try {
    const { items: templates } = await api.listTemplates()
    const m = new Map<string, TemplateView>()
    for (const t of templates) if (t.view) m.set(t.name, t.view)
    viewByTemplate.value = m
  } catch {
    // Non-fatal: without views the table just shows the default columns.
  }
}

// Delete confirmation state (mirrors the edges providers' table UX).
const deleteConfirm = ref<Instance | null>(null)
const deleting = ref(false)
const deleteError = ref<string | null>(null)

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

function confirmDelete(inst: Instance) {
  deleteConfirm.value = inst
  deleteError.value = null
}

async function executeDelete() {
  if (!deleteConfirm.value) return
  deleting.value = true
  deleteError.value = null
  try {
    await api.deleteInstance(deleteConfirm.value.name)
    deleteConfirm.value = null
    await refresh()
  } catch (e: unknown) {
    deleteError.value = (e as { message?: string }).message ?? 'delete failed'
  } finally {
    deleting.value = false
  }
}

// Compact relative age from a creationTimestamp, matching the edges tables.
function formatAge(ts?: string): string {
  if (!ts) return '-'
  const then = new Date(ts).getTime()
  if (Number.isNaN(then)) return '-'
  const s = Math.max(0, Math.floor((Date.now() - then) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  return `${Math.floor(h / 24)}d`
}

onMounted(() => {
  loadViews()
  refresh()
  // Poll every 10s so status transitions show without a manual refresh.
  pollHandle = window.setInterval(refresh, 10000)
})
onUnmounted(() => {
  if (pollHandle !== null) window.clearInterval(pollHandle)
})
</script>

<template>
  <div>
    <!-- Header -->
    <div class="mb-5 flex items-center gap-3 flex-wrap">
      <div>
        <h2 class="text-[15px] font-bold text-text-primary">My instances</h2>
        <p class="text-[12px] text-text-muted">Provisioned into the active workspace.</p>
      </div>
      <div class="ml-auto flex items-center gap-3">
        <div class="flex items-center gap-1.5">
          <div class="h-1.5 w-1.5 rounded-full bg-success" />
          <span class="font-mono text-[10px] text-text-muted">auto-refresh 10s</span>
        </div>
        <button
          class="flex items-center gap-2 rounded-xl border border-accent/30 bg-accent/10 px-3.5 py-2 text-[12px] font-medium text-accent transition-all hover:bg-accent/20"
          @click="emit('navigate', 'catalog')"
        >
          Browse templates
        </button>
      </div>
    </div>

    <!-- Table -->
    <div class="overflow-hidden rounded-2xl border border-border-subtle bg-surface-raised">
      <div v-if="loading" class="p-6 text-[12px] text-text-muted">Loading…</div>
      <div v-else-if="error" class="m-4 rounded-lg border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger">
        {{ error }}
      </div>
      <div v-else-if="items.length === 0" class="p-8 text-center">
        <p class="text-[13px] text-text-secondary">No instances in this workspace yet.</p>
        <p class="mt-1 text-[12px] text-text-muted">
          Each workspace has its own instances.
          <a href="#" class="text-accent hover:underline" @click.prevent="emit('navigate', 'catalog')">Browse templates</a>
          to provision one.
        </p>
      </div>
      <table v-else class="w-full border-collapse text-left">
        <thead>
          <tr class="border-b border-border-subtle">
            <th class="px-4 py-2.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Name</th>
            <th class="px-4 py-2.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Template</th>
            <th
              v-for="header in extraColumns"
              :key="header"
              class="px-4 py-2.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted"
            >
              {{ header }}
            </th>
            <th class="px-4 py-2.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Status</th>
            <th class="px-4 py-2.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-text-muted">Age</th>
            <th class="px-4 py-2.5"></th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="i in items"
            :key="i.name"
            class="group cursor-pointer border-b border-border-subtle/60 transition-colors last:border-0 hover:bg-surface-hover"
            @click="emit('select', i.name)"
          >
            <td class="px-4 py-3">
              <span class="text-[13px] font-medium text-text-primary">{{ i.name }}</span>
            </td>
            <td class="px-4 py-3">
              <span class="rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 text-[11px] text-text-secondary">{{ i.template }}</span>
            </td>
            <td v-for="header in extraColumns" :key="header" class="px-4 py-3">
              <ViewValue
                v-if="columnFor(i, header)"
                :value="resolve(columnFor(i, header)!, i)"
              />
              <span v-else class="text-[12px] text-text-muted/50">—</span>
            </td>
            <td class="px-4 py-3"><StatusBadge :phase="i.phase" /></td>
            <td class="px-4 py-3 font-mono text-[12px] text-text-muted">{{ formatAge(i.createdAt) }}</td>
            <td class="px-4 py-3 text-right">
              <button
                class="inline-flex h-7 w-7 items-center justify-center rounded-lg text-text-muted/40 opacity-0 transition-all group-hover:opacity-100 hover:bg-danger-subtle hover:text-danger"
                title="Delete instance"
                @click.stop="confirmDelete(i)"
              >
                <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">
                  <path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6" />
                  <line x1="10" y1="11" x2="10" y2="17" />
                  <line x1="14" y1="11" x2="14" y2="17" />
                </svg>
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Delete confirmation -->
    <Teleport to="body">
      <div
        v-if="deleteConfirm"
        class="fixed inset-0 z-[100] flex items-center justify-center bg-black/50 backdrop-blur-sm"
        @click.self="deleting ? null : (deleteConfirm = null)"
      >
        <div class="w-full max-w-md rounded-2xl border border-border-subtle bg-surface-raised p-6 shadow-2xl">
          <h3 class="text-[14px] font-bold text-text-primary">Delete instance?</h3>
          <p class="mt-2 text-[12px] text-text-muted">
            This permanently deletes
            <span class="font-mono font-medium text-text-secondary">{{ deleteConfirm.name }}</span>
            ({{ deleteConfirm.template }}) and the resources it provisioned.
          </p>
          <div v-if="deleteError" class="mt-3 rounded-lg border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger">
            {{ deleteError }}
          </div>
          <div class="mt-5 flex items-center justify-end gap-3">
            <button
              class="rounded-lg border border-border-subtle px-4 py-2 text-[12px] font-medium text-text-secondary transition-all hover:bg-surface-hover disabled:opacity-50"
              :disabled="deleting"
              @click="deleteConfirm = null"
            >
              Cancel
            </button>
            <button
              class="rounded-lg bg-danger px-4 py-2 text-[12px] font-medium text-white transition-all hover:bg-danger/80 disabled:opacity-50"
              :disabled="deleting"
              @click="executeDelete"
            >
              {{ deleting ? 'Deleting…' : 'Delete' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>
