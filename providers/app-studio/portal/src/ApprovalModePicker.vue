<script setup lang="ts">
import { Check, ChevronDown, Hand, ShieldCheck, ShieldX } from 'lucide-vue-next'
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

const labels: Record<ProjectAssistantApprovalMode, string> = {
  on_request: 'Ask when needed',
  always_ask: 'Always ask',
  never: 'Never allow',
}

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
      :aria-label="`Approval mode: ${labels[mode]}`"
      @click="open = !open"
    >
      <ShieldCheck v-if="mode === 'on_request'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <Hand v-else-if="mode === 'always_ask'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <ShieldX v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <span class="truncate">{{ busy ? 'Saving…' : labels[mode] }}</span>
      <ChevronDown class="h-3 w-3 shrink-0 transition" :class="{ 'rotate-180': open }" :stroke-width="1.75" />
    </button>

    <div
      v-if="open"
      role="dialog"
      aria-modal="false"
      aria-label="How should App Studio actions be approved?"
      class="fixed inset-x-2 bottom-2 z-50 max-h-[calc(100dvh-1rem)] overflow-y-auto rounded-xl border border-border-default bg-surface-overlay p-1.5 shadow-xl md:absolute md:inset-x-auto md:bottom-9 md:left-0 md:max-h-[min(28rem,calc(100dvh-7rem))] md:w-[360px]"
    >
      <div class="flex items-center justify-between gap-2 px-1.5 py-1">
        <span class="text-[11px] text-text-muted">How should App Studio actions be approved?</span>
      </div>
      <button
        type="button"
        :aria-pressed="mode === 'on_request'"
        class="flex w-full items-start gap-2 rounded-lg px-1.5 py-1.5 text-left transition hover:bg-surface-hover"
        @click="choose('on_request')"
      >
        <ShieldCheck class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[12px] font-medium text-text-primary">Ask when needed</span>
          <span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Run routine workspace, build, test, and lint actions automatically. Ask before consequential external effects.</span>
        </span>
        <Check v-if="mode === 'on_request'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
      <button
        type="button"
        :aria-pressed="mode === 'always_ask'"
        class="flex w-full items-start gap-2 rounded-lg px-1.5 py-1.5 text-left transition hover:bg-surface-hover"
        @click="choose('always_ask')"
      >
        <Hand class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[12px] font-medium text-text-primary">Always ask</span>
          <span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Ask before actions that change state or invoke external operations.</span>
        </span>
        <Check v-if="mode === 'always_ask'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
      <button
        type="button"
        :aria-pressed="mode === 'never'"
        class="flex w-full items-start gap-2 rounded-lg px-1.5 py-1.5 text-left transition hover:bg-surface-hover"
        @click="choose('never')"
      >
        <ShieldX class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[12px] font-medium text-text-primary">Never allow</span>
          <span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Keep the assistant read-only and reject actions requiring approval.</span>
        </span>
        <Check v-if="mode === 'never'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
    </div>
  </div>
</template>
