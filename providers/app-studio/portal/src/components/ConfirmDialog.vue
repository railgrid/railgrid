<script setup lang="ts">
import { X, AlertTriangle } from 'lucide-vue-next'
import { useEscapeKey } from '@/composables/useEscapeKey'

defineProps<{
  title: string
  message: string
  confirmLabel?: string
  busy?: boolean
}>()

const emit = defineEmits<{
  confirm: []
  cancel: []
}>()

useEscapeKey(() => emit('cancel'))
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
              <AlertTriangle class="h-4 w-4 text-danger" :stroke-width="1.75" />
            </div>
            <div>
              <h2 class="text-[14px] font-semibold text-text-primary">{{ title }}</h2>
              <p class="mt-1.5 text-[12px] leading-relaxed text-text-secondary">{{ message }}</p>
            </div>
          </div>
          <button
            class="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg text-text-muted transition-all hover:bg-surface-hover hover:text-text-primary"
            :disabled="busy"
            @click="emit('cancel')"
          >
            <X class="h-4 w-4" :stroke-width="2" />
          </button>
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
            {{ busy ? 'Working...' : confirmLabel ?? 'Confirm' }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
