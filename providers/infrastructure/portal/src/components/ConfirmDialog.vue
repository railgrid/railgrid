<script setup lang="ts">
// Mirrors the host portal's @/components/ConfirmDialog (and the edges providers'
// delete UX) so the infrastructure provider's confirmations look identical.
// Dependency-free: inline SVGs instead of lucide (this bundle ships no icon lib).
import { onMounted, onUnmounted } from 'vue'

defineProps<{
  title: string
  message: string
  confirmLabel?: string
  busy?: boolean
  error?: string | null
}>()

const emit = defineEmits<{ confirm: []; cancel: [] }>()

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('cancel')
}
onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <Teleport to="body">
    <div
      class="fixed inset-0 z-[100] flex items-center justify-center bg-black/50 backdrop-blur-sm"
      @click.self="emit('cancel')"
    >
      <div class="w-full max-w-md rounded-xl border border-border-subtle bg-surface-raised p-6 shadow-2xl">
        <div class="flex items-start justify-between gap-4">
          <div class="flex items-start gap-3">
            <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl border border-danger/20 bg-danger-subtle">
              <svg viewBox="0 0 24 24" class="h-4 w-4 text-danger" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">
                <path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
                <line x1="12" y1="9" x2="12" y2="13" />
                <line x1="12" y1="17" x2="12.01" y2="17" />
              </svg>
            </div>
            <div>
              <h2 class="text-[14px] font-semibold text-text-primary">{{ title }}</h2>
              <p class="mt-1.5 text-[12px] leading-relaxed text-text-secondary">{{ message }}</p>
            </div>
          </div>
          <button
            class="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg text-text-muted transition-all hover:bg-surface-hover hover:text-text-primary disabled:opacity-50"
            :disabled="busy"
            @click="emit('cancel')"
          >
            <svg viewBox="0 0 24 24" class="h-4 w-4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <line x1="18" y1="6" x2="6" y2="18" />
              <line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>

        <div v-if="error" class="mt-4 rounded-lg border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger">
          {{ error }}
        </div>

        <div class="mt-6 flex items-center justify-end gap-2">
          <button
            class="rounded-lg border border-border-subtle px-4 py-2 text-[12px] font-medium text-text-secondary transition-all hover:bg-surface-hover disabled:opacity-50"
            :disabled="busy"
            @click="emit('cancel')"
          >
            Cancel
          </button>
          <button
            class="rounded-lg bg-danger px-4 py-2 text-[12px] font-medium text-white transition-all hover:bg-danger/80 disabled:opacity-50"
            :disabled="busy"
            @click="emit('confirm')"
          >
            {{ busy ? 'Working…' : confirmLabel ?? 'Confirm' }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
