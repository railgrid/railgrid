<script setup lang="ts">
import { Check, ChevronDown, ClipboardList, SearchCheck, Sparkles } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

export type AssistantResponseMode = 'default' | 'plan' | 'review'

const props = defineProps<{
  mode: AssistantResponseMode
  disabled?: boolean
}>()

const emit = defineEmits<{
  selectMode: [mode: AssistantResponseMode]
}>()

const root = ref<HTMLElement | null>(null)
const open = ref(false)
const modeLabel = computed(() => {
  if (props.mode === 'plan') return 'Plan'
  if (props.mode === 'review') return 'Review'
  return 'Default'
})

function chooseMode(mode: AssistantResponseMode) {
  if (props.disabled) return
  open.value = false
  emit('selectMode', mode)
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
  <div ref="root" class="relative min-w-0 max-w-52">
    <button
      type="button"
      class="inline-flex h-8 max-w-full items-center gap-1.5 rounded-lg px-2 text-[11px] font-medium text-text-muted transition hover:bg-surface-hover hover:text-text-secondary disabled:cursor-not-allowed disabled:opacity-60"
      :disabled="disabled"
      aria-haspopup="dialog"
      :aria-expanded="open"
      :aria-label="`Response mode: ${modeLabel}`"
      @click="open = !open"
    >
      <ClipboardList v-if="mode === 'plan'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <SearchCheck v-else-if="mode === 'review'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <Sparkles v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <span class="truncate">{{ modeLabel }}</span>
      <ChevronDown class="h-3 w-3 shrink-0 transition" :class="{ 'rotate-180': open }" :stroke-width="1.75" />
    </button>

    <div
      v-if="open"
      role="dialog"
      aria-modal="false"
      aria-label="How should App Studio respond?"
      class="fixed inset-x-2 bottom-2 z-50 max-h-[calc(100dvh-1rem)] overflow-y-auto rounded-xl border border-border-default bg-surface-overlay p-1.5 shadow-xl md:absolute md:inset-x-auto md:bottom-9 md:left-0 md:max-h-[min(28rem,calc(100dvh-7rem))] md:w-[360px]"
    >
      <div class="px-1.5 py-1 text-[11px] text-text-muted">How should App Studio respond?</div>
      <button
        type="button"
        :aria-pressed="mode === 'default'"
        class="flex w-full items-start gap-2 rounded-lg px-1.5 py-1.5 text-left transition hover:bg-surface-hover"
        @click="chooseMode('default')"
      >
        <Sparkles class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[12px] font-medium text-text-primary">Default</span>
          <span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Answer, inspect, or make requested changes using current evidence.</span>
        </span>
        <Check v-if="mode === 'default'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
      <button
        type="button"
        :aria-pressed="mode === 'plan'"
        class="flex w-full items-start gap-2 rounded-lg px-1.5 py-1.5 text-left transition hover:bg-surface-hover"
        @click="chooseMode('plan')"
      >
        <ClipboardList class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[12px] font-medium text-text-primary">Plan</span>
          <span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Investigate and produce a plan without changing the project.</span>
        </span>
        <Check v-if="mode === 'plan'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
      <button
        type="button"
        :aria-pressed="mode === 'review'"
        class="flex w-full items-start gap-2 rounded-lg px-1.5 py-1.5 text-left transition hover:bg-surface-hover"
        @click="chooseMode('review')"
      >
        <SearchCheck class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[12px] font-medium text-text-primary">Review</span>
          <span class="mt-0.5 block text-[11px] leading-4 text-text-muted">Inspect the current workspace and report prioritized findings without changing it.</span>
        </span>
        <Check v-if="mode === 'review'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
    </div>
  </div>
</template>
