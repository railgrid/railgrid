<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIWorkbenchTabs owns the shared tab strip, launcher, close affordances, and
  event forwarding used by AI workbenches. Callers own tab identity, active
  state, persistence, keyboard policy, and drag/reorder decisions.
-->
<script setup lang="ts">
import { ChevronLeft, ChevronRight, Plus, X } from 'lucide-vue-next'
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIWorkbenchTabView } from './ai'
import AIWorkbenchTab from './AIWorkbenchTab.vue'

const props = withDefaults(defineProps<{
  tabs: readonly AIWorkbenchTabView[]
  ariaLabel?: string
  launcher?: boolean
  launcherLabel?: string
  launcherTitle?: string
  launcherDisabled?: boolean
  launcherActive?: boolean
  scrollPreviousLabel?: string
  scrollNextLabel?: string
}>(), {
  ariaLabel: 'Workbench tabs',
  launcher: false,
  launcherLabel: 'New tab',
  launcherTitle: undefined,
  launcherDisabled: false,
  launcherActive: false,
  scrollPreviousLabel: 'Scroll tabs left',
  scrollNextLabel: 'Scroll tabs right',
})

const emit = defineEmits<{
  select: [id: string, event: MouseEvent]
  keydown: [id: string, event: KeyboardEvent]
  close: [id: string, event: MouseEvent]
  dragstart: [id: string, event: DragEvent]
  dragover: [id: string, event: DragEvent]
  drop: [id: string, event: DragEvent]
  dragend: [id: string, event: DragEvent]
  launch: [event: MouseEvent]
}>()

ensureAgentUIStyles()

const viewport = ref<HTMLDivElement | null>(null)
const hasOverflow = ref(false)
const canScrollPrevious = ref(false)
const canScrollNext = ref(false)
let resizeObserver: ResizeObserver | undefined

function updateScrollState(): void {
  const element = viewport.value
  if (!element) return

  const maxScrollLeft = Math.max(0, element.scrollWidth - element.clientWidth)
  hasOverflow.value = maxScrollLeft > 1
  canScrollPrevious.value = element.scrollLeft > 1
  canScrollNext.value = element.scrollLeft < maxScrollLeft - 1
}

function revealSelectedTab(): void {
  const element = viewport.value
  const selected = props.tabs.find((tab) => tab.selected)
  if (!element || !selected) return

  const tab = document.getElementById(selected.controlId)
  if (!tab || !element.contains(tab)) return

  const viewportBounds = element.getBoundingClientRect()
  const tabBounds = (tab.closest('.k-workbench-tab') || tab).getBoundingClientRect()
  const left = tabBounds.left < viewportBounds.left
    ? tabBounds.left - viewportBounds.left
    : tabBounds.right > viewportBounds.right
      ? tabBounds.right - viewportBounds.right
      : 0
  if (left) element.scrollBy({ left, behavior: 'auto' })
}

function refreshAfterLayout(): void {
  updateScrollState()
  revealSelectedTab()
}

function scrollByPage(direction: -1 | 1): void {
  const element = viewport.value
  if (!element) return

  const distance = Math.max(120, Math.floor(element.clientWidth * 0.75))
  element.scrollBy({ left: direction * distance, behavior: 'auto' })
}

watch(
  () => props.tabs.map((tab) => `${tab.id}\u0000${tab.controlId}\u0000${tab.title}\u0000${Boolean(tab.selected)}`).join('\u0001'),
  async () => {
    await nextTick()
    refreshAfterLayout()
  },
  { flush: 'post' },
)

onMounted(() => {
  const element = viewport.value
  if (!element) return

  if (typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(refreshAfterLayout)
    resizeObserver.observe(element)
    const tabList = element.querySelector<HTMLElement>('[role="tablist"]')
    if (tabList) resizeObserver.observe(tabList)
  }

  refreshAfterLayout()
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  resizeObserver = undefined
})

function closeLabel(tab: AIWorkbenchTabView): string {
  return `Close ${tab.title}`
}
</script>

<template>
  <div class="k-workbench-tabs__strip">
    <button
      v-if="hasOverflow"
      type="button"
      class="k-workbench-tabs__scroll-control"
      :aria-label="scrollPreviousLabel"
      :disabled="!canScrollPrevious"
      @click="scrollByPage(-1)"
    >
      <ChevronLeft :stroke-width="1.75" aria-hidden="true" />
    </button>

    <div ref="viewport" class="k-workbench-tabs__viewport" @scroll.passive="updateScrollState">
      <div class="k-workbench-tabs" role="tablist" :aria-label="ariaLabel">
        <AIWorkbenchTab
          v-for="tab in props.tabs"
          :id="tab.controlId"
          :key="tab.id"
          :data-tab-id="tab.dataTabId"
          :active="tab.active ?? tab.selected"
          :dragged="tab.dragged"
          :drag-over="tab.dragOver"
          :drop-placement="tab.dropPlacement"
          :draggable="tab.draggable"
          :selected="tab.selected"
          :controls="tab.controls"
          :tabindex="tab.tabindex"
          :title="tab.title"
          :disabled="tab.disabled"
          @dragstart="emit('dragstart', tab.id, $event)"
          @dragover="emit('dragover', tab.id, $event)"
          @drop="emit('drop', tab.id, $event)"
          @dragend="emit('dragend', tab.id, $event)"
          @click="emit('select', tab.id, $event)"
          @keydown="emit('keydown', tab.id, $event)"
        >
          <template #leading>
            <slot name="leading" :tab="tab" />
          </template>
          <template #icon>
            <slot name="icon" :tab="tab" />
          </template>
          <slot name="label" :tab="tab">{{ tab.title }}</slot>
          <template #after-label>
            <slot name="after-label" :tab="tab" />
          </template>
          <template #trailing>
            <slot name="trailing" :tab="tab" />
            <button
              v-if="tab.closeable"
              type="button"
              class="k-workbench-tab__close"
              :title="closeLabel(tab)"
              :aria-label="closeLabel(tab)"
              @click.stop="emit('close', tab.id, $event)"
            >
              <slot name="close-icon" :tab="tab">
                <X :stroke-width="1.75" aria-hidden="true" />
              </slot>
            </button>
          </template>
        </AIWorkbenchTab>
      </div>
    </div>

    <button
      v-if="hasOverflow"
      type="button"
      class="k-workbench-tabs__scroll-control"
      :aria-label="scrollNextLabel"
      :disabled="!canScrollNext"
      @click="scrollByPage(1)"
    >
      <ChevronRight :stroke-width="1.75" aria-hidden="true" />
    </button>

    <button
      v-if="launcher"
      type="button"
      class="k-workbench-tabs__launcher"
      :class="{ 'k-workbench-tabs__launcher--active': launcherActive }"
      :title="launcherTitle || launcherLabel"
      :aria-label="launcherLabel"
      :disabled="launcherDisabled"
      @click="emit('launch', $event)"
    >
      <slot name="launcher-icon">
        <Plus :stroke-width="1.75" aria-hidden="true" />
      </slot>
      <slot name="launcher-indicator" />
    </button>
  </div>
</template>
