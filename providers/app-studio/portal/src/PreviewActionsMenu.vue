<script setup lang="ts">
import { Check, ChevronRight, Ellipsis, GitBranch, LayoutTemplate, Loader2 } from 'lucide-vue-next'
import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { DevelopmentTemplate } from './types'

const props = defineProps<{
  templates: DevelopmentTemplate[]
  currentTemplate?: string
  templateBusy?: boolean
  hydrateBusy?: boolean
  hydrateDisabled?: boolean
  disabled?: boolean
}>()

const emit = defineEmits<{
  selectTemplate: [template: string]
  loadFromGit: []
}>()

const root = ref<HTMLElement | null>(null)
const open = ref(false)
const templatesOpen = ref(false)

function close() {
  open.value = false
  templatesOpen.value = false
}

function selectTemplate(template: string) {
  if (props.disabled || props.templateBusy || template === props.currentTemplate) return
  close()
  emit('selectTemplate', template)
}

function loadFromGit() {
  if (props.disabled || props.hydrateBusy || props.hydrateDisabled) return
  close()
  emit('loadFromGit')
}

function closeFromOutside(event: PointerEvent) {
  if (root.value && !root.value.contains(event.target as Node)) close()
}

function closeFromEscape(event: KeyboardEvent) {
  if (!open.value || event.key !== 'Escape') return
  close()
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
  <div ref="root" class="relative">
    <button
      type="button"
      class="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
      title="More preview actions"
      aria-label="More preview actions"
      aria-haspopup="dialog"
      :aria-expanded="open"
      :disabled="disabled"
      @click="open = !open; templatesOpen = false"
    >
      <Ellipsis class="h-4 w-4" :stroke-width="1.75" />
    </button>

    <div
      v-if="open"
      role="dialog"
      aria-modal="false"
      aria-label="Preview actions"
      class="absolute right-0 top-10 z-30 w-64 rounded-xl border border-border-default bg-surface-overlay p-1.5 shadow-2xl"
    >
      <button
        v-if="templates.length > 0"
        type="button"
        class="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="disabled || templateBusy"
        :aria-expanded="templatesOpen"
        @click="templatesOpen = !templatesOpen"
      >
        <Loader2 v-if="templateBusy" class="h-3.5 w-3.5 shrink-0 animate-spin" :stroke-width="1.75" />
        <LayoutTemplate v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
        <span class="min-w-0 flex-1 truncate">Switch template</span>
        <ChevronRight class="h-3.5 w-3.5 shrink-0 transition" :class="{ 'rotate-90': templatesOpen }" :stroke-width="1.75" />
      </button>

      <div v-if="templatesOpen" class="mb-1 ml-5 border-l border-border-subtle pl-1.5" role="group" aria-label="Development templates">
        <button
          v-for="template in templates"
          :key="template.name"
          type="button"
          class="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-[12px] transition hover:bg-surface-hover disabled:cursor-default"
          :class="template.name === currentTemplate ? 'text-text-primary' : 'text-text-secondary'"
          :disabled="disabled || templateBusy || template.name === currentTemplate"
          :aria-current="template.name === currentTemplate ? 'true' : undefined"
          @click="selectTemplate(template.name)"
        >
          <span class="min-w-0 flex-1 truncate">{{ template.displayName || template.name }}</span>
          <Check v-if="template.name === currentTemplate" class="h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="1.75" />
        </button>
      </div>

      <button
        type="button"
        class="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="disabled || hydrateDisabled || hydrateBusy"
        @click="loadFromGit"
      >
        <Loader2 v-if="hydrateBusy" class="h-3.5 w-3.5 shrink-0 animate-spin" :stroke-width="1.75" />
        <GitBranch v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
        <span>{{ hydrateBusy ? 'Loading from git…' : 'Load from git' }}</span>
      </button>
    </div>
  </div>
</template>
