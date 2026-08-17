<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { MessageSquare, X } from 'lucide-vue-next'
import type { ProjectAssistantAnnotation } from './types'

const props = withDefaults(defineProps<{
  annotations: ProjectAssistantAnnotation[]
  /** Current authenticated preview document. Empty means staleness is unknown. */
  currentDocumentId?: string
  currentPagePath?: string
  unresolvedAnnotationIds?: string[]
  rebindAcrossDocuments?: boolean
  /** Composer mode adds a clear-all control to the annotation pill. */
  clearable?: boolean
  /** Stable relationship IDs supplied by the owning message/composer. */
  disclosureID?: string
}>(), {
  currentDocumentId: '',
  currentPagePath: '',
  unresolvedAnnotationIds: () => [],
  rebindAcrossDocuments: false,
  clearable: false,
  disclosureID: '',
})

const emit = defineEmits<{
  'remove-all': []
}>()

const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)

const relationshipID = computed(() => {
  const supplied = props.disclosureID.trim()
  if (supplied) return supplied
  const identity = props.annotations[0]?.id.replace(/[^A-Za-z0-9_-]/g, '').slice(0, 32) || 'default'
  return `assistant-annotations-${identity}`
})
const panelID = computed(() => `${relationshipID.value}-panel`)
const staleCount = computed(() => props.annotations.filter((annotation) => isStale(annotation)).length)

function isStale(annotation: ProjectAssistantAnnotation): boolean {
  if (props.rebindAcrossDocuments) {
    return annotation.pagePath === props.currentPagePath && props.unresolvedAnnotationIds.includes(annotation.id)
  }
  const current = props.currentDocumentId.trim()
  return Boolean(current && annotation.documentID !== current)
}

function targetKind(annotation: ProjectAssistantAnnotation): string {
  return annotation.target.tag || annotation.target.role || 'element'
}

function targetExcerpt(annotation: ProjectAssistantAnnotation): string {
  const target = annotation.target
  return target.text || target.name || target.locator || target.role || target.tag || 'Preview element'
}

function show() {
  open.value = true
}

function hide() {
  open.value = false
}

function handleFocusOut(event: FocusEvent) {
  const next = event.relatedTarget
  if (next instanceof Node && rootRef.value?.contains(next)) return
  hide()
}

function removeAll() {
  hide()
  emit('remove-all')
}

watch(() => props.annotations.length, (count) => {
  if (!count) hide()
})
</script>

<template>
  <div
    v-if="annotations.length"
    ref="rootRef"
    class="group relative inline-flex max-w-full"
    @mouseenter="show"
    @mouseleave="hide"
    @focusin="show"
    @focusout="handleFocusOut"
    @keydown.esc.stop.prevent="hide"
  >
    <div class="inline-flex h-8 max-w-full items-center rounded-md border border-border-default bg-surface-raised p-0.5 shadow-sm transition hover:bg-surface-hover focus-within:ring-2 focus-within:ring-accent/40">
      <button
        type="button"
        class="inline-flex min-w-0 items-center gap-1.5 px-2 text-[12px] font-medium text-text-primary focus-visible:outline-none"
        :aria-label="`Preview ${annotations.length} ${annotations.length === 1 ? 'annotation' : 'annotations'}${staleCount ? `; ${staleCount} stale` : ''}`"
        :aria-describedby="panelID"
        @click="show"
      >
        <MessageSquare class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
        <span class="truncate">{{ annotations.length }} {{ annotations.length === 1 ? 'annotation' : 'annotations' }}</span>
      </button>
      <button
        v-if="clearable"
        type="button"
        class="pointer-events-none flex h-6 w-0 shrink-0 scale-75 items-center justify-center overflow-hidden rounded-full border border-transparent bg-surface-hover text-text-muted opacity-0 transition-[width,margin,opacity,transform,border-color] group-hover:ml-0.5 group-hover:w-6 group-hover:scale-100 group-hover:border-border-default group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:ml-0.5 group-focus-within:w-6 group-focus-within:scale-100 group-focus-within:border-border-default group-focus-within:pointer-events-auto group-focus-within:opacity-100 hover:bg-surface-overlay hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
        aria-label="Clear annotations"
        @click.stop="removeAll"
      >
        <X class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
      </button>
    </div>

    <div
      :id="panelID"
      v-show="open"
      role="tooltip"
      class="absolute bottom-full left-0 z-40 mb-2 max-h-80 w-[min(32rem,calc(100vw-3rem))] overflow-auto rounded-lg border border-border-default bg-surface-overlay text-left shadow-2xl"
    >
      <div
        v-for="(annotation, index) in annotations"
        :key="annotation.id"
        class="grid gap-2 px-3 py-3"
        :class="index ? 'border-t border-border-subtle' : ''"
      >
        <div class="flex min-w-0 items-center gap-2 text-[12px] leading-4">
          <span class="shrink-0 rounded-sm border border-border-default bg-surface-hover px-1.5 py-0.5 font-mono text-[11px] text-text-secondary">{{ targetKind(annotation) }}</span>
          <span class="min-w-0 truncate text-text-muted">{{ targetExcerpt(annotation) }}</span>
          <span v-if="isStale(annotation)" class="ml-auto shrink-0 font-mono text-[10px] uppercase tracking-wide text-warning">Stale preview</span>
        </div>
        <div class="whitespace-pre-wrap text-[13px] leading-5 text-text-primary">{{ annotation.comment }}</div>
      </div>
    </div>
  </div>
</template>
