<script setup lang="ts">
import { Check, ChevronDown, MessageCircle, Sparkles } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

export type AssistantResponseMode = 'auto' | 'ask'

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
  if (props.mode === 'ask') return 'Discuss'
  return 'Adaptive'
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
      <MessageCircle v-if="mode === 'ask'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <Sparkles v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <span class="truncate">{{ modeLabel }}</span>
      <ChevronDown class="h-3 w-3 shrink-0 transition" :class="{ 'rotate-180': open }" :stroke-width="1.75" />
    </button>

    <div
      v-if="open"
      role="dialog"
      aria-modal="false"
      aria-label="How should App Studio respond?"
      class="fixed inset-x-3 bottom-3 z-50 max-h-[calc(100dvh-1.5rem)] overflow-y-auto rounded-2xl border border-border-default bg-surface-overlay p-2 shadow-2xl md:absolute md:inset-x-auto md:bottom-10 md:left-0 md:max-h-[min(32rem,calc(100dvh-8rem))] md:w-[430px]"
    >
      <div class="px-2 pb-1 pt-0.5 text-[11px] text-text-muted">How should App Studio respond?</div>
      <button
        type="button"
        :aria-pressed="mode === 'auto'"
        class="flex w-full items-start gap-3 rounded-xl px-2 py-2 text-left transition hover:bg-surface-hover"
        @click="chooseMode('auto')"
      >
        <Sparkles class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[13px] font-medium text-text-primary">Adaptive</span>
          <span class="mt-0.5 block text-[12px] leading-4 text-text-muted">Answer directly, inspect safely, or propose work when needed.</span>
        </span>
        <Check v-if="mode === 'auto'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
      <button
        type="button"
        :aria-pressed="mode === 'ask'"
        class="flex w-full items-start gap-3 rounded-xl px-2 py-2 text-left transition hover:bg-surface-hover"
        @click="chooseMode('ask')"
      >
        <MessageCircle class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[13px] font-medium text-text-primary">Discuss</span>
          <span class="mt-0.5 block text-[12px] leading-4 text-text-muted">Discuss and inspect without changing project files.</span>
        </span>
        <Check v-if="mode === 'ask'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
    </div>
  </div>
</template>
