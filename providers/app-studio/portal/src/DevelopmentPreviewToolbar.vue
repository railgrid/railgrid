<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  AppWindow,
  Ellipsis,
  ExternalLink,
  Loader2,
  MessageSquare,
  Plus,
  RefreshCw,
} from 'lucide-vue-next'
import StatusBadge from './portalkit/StatusBadge.vue'
import { previewToolbarLayout } from './previewToolbarLayout'

const props = defineProps<{
  provider: string
  phase: string
  annotationMode: boolean
  annotationAvailable: boolean
  annotationDisabled: boolean
  syncBusy: boolean
  syncDisabled: boolean
  openDisabled: boolean
  openLabel: string
}>()

const emit = defineEmits<{
  annotate: []
  sync: []
  openBrowser: []
}>()

const root = ref<HTMLElement | null>(null)
const overflowTrigger = ref<HTMLButtonElement | null>(null)
const overflowMenu = ref<HTMLElement | null>(null)
const overflowOpen = ref(false)
const toolbarWidth = ref(0)
const layout = computed(() => previewToolbarLayout(toolbarWidth.value))
let toolbarResizeObserver: ResizeObserver | undefined

function syncToolbarWidth(width = root.value?.getBoundingClientRect().width ?? 0): void {
  toolbarWidth.value = width
}

function closeOverflow(restoreFocus = false): void {
  if (!overflowOpen.value) return
  overflowOpen.value = false
  if (restoreFocus) void nextTick(() => overflowTrigger.value?.focus())
}

function openOverflow(focusMenu = false): void {
  overflowOpen.value = true
  if (!focusMenu) return
  void nextTick(() => {
    overflowMenu.value
      ?.querySelector<HTMLButtonElement>('[role^="menuitem"]:not(:disabled)')
      ?.focus()
  })
}

function toggleOverflow(): void {
  if (overflowOpen.value) closeOverflow()
  else openOverflow()
}

function runAnnotation(): void {
  if (props.annotationDisabled) return
  emit('annotate')
  closeOverflow(true)
}

function runSync(): void {
  if (props.syncDisabled) return
  emit('sync')
  closeOverflow(true)
}

function runOpenBrowser(): void {
  if (props.openDisabled) return
  emit('openBrowser')
  closeOverflow(true)
}

function handleDocumentPointerDown(event: PointerEvent): void {
  if (!overflowOpen.value || root.value?.contains(event.target as Node)) return
  closeOverflow()
}

function handleFocusOut(event: FocusEvent): void {
  const next = event.relatedTarget
  if (next instanceof Node && root.value?.contains(next)) return
  closeOverflow()
}

function handleMenuKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    closeOverflow(true)
    return
  }
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const items = Array.from(
    overflowMenu.value?.querySelectorAll<HTMLButtonElement>('[role^="menuitem"]:not(:disabled)') ?? [],
  ).filter((item) => item.offsetParent !== null)
  if (!items.length) return
  const currentIndex = items.indexOf(document.activeElement as HTMLButtonElement)
  let nextIndex = 0
  if (event.key === 'End') nextIndex = items.length - 1
  else if (event.key === 'ArrowUp') nextIndex = currentIndex <= 0 ? items.length - 1 : currentIndex - 1
  else if (event.key === 'ArrowDown') nextIndex = currentIndex < 0 || currentIndex === items.length - 1 ? 0 : currentIndex + 1
  event.preventDefault()
  event.stopPropagation()
  items[nextIndex]?.focus()
}

onMounted(() => {
  document.addEventListener('pointerdown', handleDocumentPointerDown)
  syncToolbarWidth()
  if (typeof ResizeObserver !== 'undefined' && root.value) {
    toolbarResizeObserver = new ResizeObserver((entries) => {
      syncToolbarWidth(entries[0]?.contentRect.width)
      closeOverflow()
    })
    toolbarResizeObserver.observe(root.value)
  }
})

onBeforeUnmount(() => {
  toolbarResizeObserver?.disconnect()
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
})
</script>

<template>
  <header
    ref="root"
    class="preview-toolbar relative z-40 flex min-w-0 items-center justify-between gap-3"
    :data-preview-toolbar-layout="layout"
    @focusout="handleFocusOut"
  >
    <div class="preview-toolbar__identity flex min-w-0 items-center gap-2">
      <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
        <AppWindow class="h-4 w-4 text-accent" :stroke-width="1.75" aria-hidden="true" />
      </div>
      <div class="min-w-0">
        <div class="truncate text-[13px] font-semibold text-text-primary">Development</div>
        <div v-if="layout !== 'collapsed'" class="preview-toolbar__identity-meta truncate text-[12px] text-text-muted">{{ provider }}</div>
      </div>
      <StatusBadge class="shrink-0" :status="phase" />
    </div>

    <div class="ml-auto flex shrink-0 items-center gap-2">
      <button
        v-if="layout !== 'collapsed'"
        type="button"
        class="preview-toolbar__primary-action inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md border transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60"
        :class="annotationMode
          ? 'border-accent/40 bg-accent-subtle text-accent'
          : 'border-border-subtle bg-surface text-text-secondary hover:bg-surface-hover hover:text-text-primary'"
        :disabled="annotationDisabled"
        :aria-pressed="annotationMode"
        :aria-label="annotationMode ? 'Stop annotating' : 'Annotate preview'"
        :data-k-tip="annotationAvailable ? (annotationMode ? 'Stop annotating' : 'Annotate preview') : 'Annotation becomes available when the preview connects'"
        @click="runAnnotation"
      >
        <span class="relative h-3.5 w-3.5 shrink-0">
          <MessageSquare class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          <Plus class="absolute -right-1 -top-1 h-2.5 w-2.5 rounded-full bg-accent p-px text-on-accent" :stroke-width="2.5" aria-hidden="true" />
        </span>
      </button>

      <button
        v-if="layout === 'expanded'"
        type="button"
        class="preview-toolbar__secondary-action inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="syncDisabled"
        :aria-label="syncBusy ? 'Syncing preview' : 'Sync preview'"
        :data-k-tip="syncBusy ? 'Syncing preview…' : 'Sync preview'"
        @click="runSync"
      >
        <Loader2 v-if="syncBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" aria-hidden="true" />
        <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
      </button>

      <button
        v-if="layout === 'expanded'"
        type="button"
        class="preview-toolbar__secondary-action inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="openDisabled"
        :aria-label="openLabel"
        :data-k-tip="openLabel"
        @click="runOpenBrowser"
      >
        <ExternalLink class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
      </button>

      <div v-if="layout !== 'expanded'" class="preview-toolbar__overflow relative shrink-0">
        <button
          ref="overflowTrigger"
          type="button"
          class="flex h-7 w-7 items-center justify-center rounded-md border border-border-subtle bg-surface text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
          data-k-tip="More preview actions"
          aria-label="More preview actions"
          aria-haspopup="menu"
          :aria-expanded="overflowOpen"
          @click.stop="toggleOverflow"
          @keydown.down.stop.prevent="openOverflow(true)"
        >
          <Ellipsis class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" />
        </button>

        <div
          v-if="overflowOpen"
          ref="overflowMenu"
          class="absolute right-0 top-10 z-50 w-52 rounded-md border border-border-default bg-surface-overlay p-1 shadow-xl"
          role="menu"
          aria-label="Preview actions"
          @keydown="handleMenuKeydown"
        >
          <button
            v-if="layout === 'collapsed'"
            type="button"
            role="menuitemcheckbox"
            class="preview-toolbar__overflow-annotation flex w-full items-center gap-2 rounded-sm px-2.5 py-2 text-left text-[12px] text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus:bg-surface-hover focus:outline-none disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="annotationDisabled"
            :aria-checked="annotationMode"
            @click="runAnnotation"
          >
            <span class="relative h-3.5 w-3.5 shrink-0">
              <MessageSquare class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
              <Plus class="absolute -right-1 -top-1 h-2.5 w-2.5 rounded-full bg-accent p-px text-on-accent" :stroke-width="2.5" aria-hidden="true" />
            </span>
            {{ annotationMode ? 'Stop annotating' : 'Annotate preview' }}
          </button>
          <button
            type="button"
            role="menuitem"
            class="flex w-full items-center gap-2 rounded-sm px-2.5 py-2 text-left text-[12px] text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus:bg-surface-hover focus:outline-none disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="syncDisabled"
            @click="runSync"
          >
            <Loader2 v-if="syncBusy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" aria-hidden="true" />
            <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
            {{ syncBusy ? 'Syncing preview…' : 'Sync preview' }}
          </button>
          <button
            type="button"
            role="menuitem"
            class="flex w-full items-center gap-2 rounded-sm px-2.5 py-2 text-left text-[12px] text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus:bg-surface-hover focus:outline-none disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="openDisabled"
            @click="runOpenBrowser"
          >
            <ExternalLink class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
            {{ openLabel }}
          </button>
        </div>
      </div>
    </div>
  </header>
</template>
