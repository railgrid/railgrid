<script setup lang="ts">
import { Check, ChevronDown, Hand, ShieldCheck } from 'lucide-vue-next'
import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { ProjectAssistantApprovalMode } from './types'

const props = defineProps<{
  mode: ProjectAssistantApprovalMode
  disabled?: boolean
  busy?: boolean
}>()

const emit = defineEmits<{
  select: [mode: ProjectAssistantApprovalMode]
}>()

const root = ref<HTMLElement | null>(null)
const open = ref(false)

function choose(mode: ProjectAssistantApprovalMode) {
  if (props.disabled || props.busy || mode === props.mode) {
    open.value = false
    return
  }
  open.value = false
  emit('select', mode)
}

function closeFromOutside(event: PointerEvent) {
  if (root.value && !root.value.contains(event.target as Node)) open.value = false
}

function closeFromEscape(event: KeyboardEvent) {
  if (!open.value || event.key !== 'Escape') return
  open.value = false
  root.value?.querySelector<HTMLButtonElement>('[aria-haspopup="dialog"]')?.focus()
}

onMounted(() => {
  document.addEventListener('pointerdown', closeFromOutside)
  document.addEventListener('keydown', closeFromEscape)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', closeFromOutside)
  document.removeEventListener('keydown', closeFromEscape)
})
</script>

<template>
  <div ref="root" class="relative min-w-0 max-w-44">
    <button
      type="button"
      class="inline-flex h-8 max-w-full items-center gap-1.5 rounded-lg px-2 text-[11px] font-medium text-text-muted transition hover:bg-surface-hover hover:text-text-secondary disabled:cursor-not-allowed disabled:opacity-60"
      :disabled="disabled || busy"
      aria-haspopup="dialog"
      :aria-expanded="open"
      :aria-label="`Approval mode: ${mode === 'auto_approve' ? 'Auto-approve' : 'Always ask'}`"
      @click="open = !open"
    >
      <ShieldCheck v-if="mode === 'auto_approve'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <Hand v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <span class="truncate">{{ busy ? 'Saving…' : mode === 'auto_approve' ? 'Auto-approve' : 'Always ask' }}</span>
      <ChevronDown class="h-3 w-3 shrink-0 transition" :class="{ 'rotate-180': open }" :stroke-width="1.75" />
    </button>

    <div
      v-if="open"
      role="dialog"
      aria-modal="false"
      aria-label="How should App Studio actions be approved?"
      class="fixed inset-x-3 bottom-3 z-50 overflow-hidden rounded-2xl border border-border-default bg-surface-overlay p-2 shadow-2xl md:absolute md:inset-x-auto md:bottom-10 md:left-0 md:w-[430px]"
    >
      <div class="flex items-center justify-between gap-3 px-2 pb-1 pt-0.5">
        <span class="text-[11px] text-text-muted">How should App Studio actions be approved?</span>
      </div>
      <button
        type="button"
        :aria-pressed="mode === 'always_ask'"
        class="flex w-full items-start gap-3 rounded-xl px-2 py-2 text-left transition hover:bg-surface-hover"
        @click="choose('always_ask')"
      >
        <Hand class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[13px] font-medium text-text-primary">Always ask</span>
          <span class="mt-0.5 block text-[12px] leading-4 text-text-muted">Ask before actions that change the project or run external operations.</span>
        </span>
        <Check v-if="mode === 'always_ask'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
      <button
        type="button"
        :aria-pressed="mode === 'auto_approve'"
        class="flex w-full items-start gap-3 rounded-xl px-2 py-2 text-left transition hover:bg-surface-hover"
        @click="choose('auto_approve')"
      >
        <ShieldCheck class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[13px] font-medium text-text-primary">Auto-approve</span>
          <span class="mt-0.5 block text-[12px] leading-4 text-text-muted">Run valid App Studio actions without asking for approval.</span>
        </span>
        <Check v-if="mode === 'auto_approve'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
    </div>
  </div>
</template>
