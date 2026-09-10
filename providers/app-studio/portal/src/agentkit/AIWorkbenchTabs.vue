<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIWorkbenchTabs owns the shared tab strip, launcher, close affordances, and
  event forwarding used by AI workbenches. Callers own tab identity, active
  state, persistence, keyboard policy, and drag/reorder decisions.
-->
<script setup lang="ts">
import { Plus, X } from 'lucide-vue-next'
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
}>(), {
  ariaLabel: 'Workbench tabs',
  launcher: false,
  launcherLabel: 'New tab',
  launcherTitle: undefined,
  launcherDisabled: false,
  launcherActive: false,
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

function closeLabel(tab: AIWorkbenchTabView): string {
  return `Close ${tab.title}`
}
</script>

<template>
  <div class="k-workbench-tabs__strip">
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
