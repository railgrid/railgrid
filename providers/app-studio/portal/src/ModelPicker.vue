<script setup lang="ts">
import { Check, ChevronDown, Cpu } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { ProjectLLMModelSettings } from './types'

const props = defineProps<{
  models: ProjectLLMModelSettings[]
  selectedID: string
  disabled?: boolean
}>()

const emit = defineEmits<{
  select: [modelID: string]
}>()

const root = ref<HTMLElement | null>(null)
const open = ref(false)
const selected = computed(() => props.models.find((model) => model.id === props.selectedID) ?? props.models[0])

function choose(modelID: string) {
  if (props.disabled) return
  open.value = false
  emit('select', modelID)
}

function closeFromOutside(event: PointerEvent) {
  if (root.value && !root.value.contains(event.target as Node)) open.value = false
}

function closeFromEscape(event: KeyboardEvent) {
  if (!open.value || event.key !== 'Escape') return
  open.value = false
  root.value?.querySelector<HTMLButtonElement>('[aria-haspopup="listbox"]')?.focus()
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
  <div ref="root" class="relative min-w-0 max-w-56">
    <button
      type="button"
      class="inline-flex h-8 max-w-full items-center gap-1.5 rounded-md px-2 text-[11px] font-medium text-text-muted transition hover:bg-surface-hover hover:text-text-secondary disabled:cursor-not-allowed disabled:opacity-60"
      :disabled="disabled || models.length === 0"
      aria-haspopup="listbox"
      :aria-expanded="open"
      :aria-label="`Model: ${selected?.name || 'Not configured'}`"
      @click="open = !open"
    >
      <Cpu class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <span class="truncate">{{ selected?.name || 'Configure model' }}</span>
      <ChevronDown class="h-3 w-3 shrink-0 transition" :class="{ 'rotate-180': open }" :stroke-width="2" />
    </button>

    <div
      v-if="open"
      role="listbox"
      aria-label="Choose model"
      class="fixed inset-x-2 bottom-2 z-50 max-h-[calc(100dvh-1rem)] overflow-y-auto rounded-lg border border-border-default bg-surface-overlay p-1.5 shadow-xl md:absolute md:inset-x-auto md:bottom-9 md:left-0 md:max-h-72 md:w-72"
    >
      <button
        v-for="model in models"
        :key="model.id"
        type="button"
        role="option"
        :aria-selected="model.id === selected?.id"
        class="flex w-full items-start gap-2 rounded-md px-2 py-2 text-left transition hover:bg-surface-hover"
        @click="choose(model.id)"
      >
        <Cpu class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block truncate text-[12px] font-medium text-text-primary">{{ model.name }}</span>
          <span class="mt-0.5 block truncate font-mono text-[10px] text-text-muted">{{ model.model }}</span>
        </span>
        <Check v-if="model.id === selected?.id" class="mt-0.5 h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" />
      </button>
    </div>
  </div>
</template>
