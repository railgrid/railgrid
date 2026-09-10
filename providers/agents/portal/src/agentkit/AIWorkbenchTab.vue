<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIWorkbenchTab owns the shared wrapper/control frame for workbench tabs.
  Callers own tab collections, selection and keyboard behavior, while the
  optional leading, after-label, and trailing slots provide lifecycle chrome
  such as App Studio's drag and close controls without forcing those controls
  onto simpler fixed tab sets.
-->
<script setup lang="ts">
import { ensureAgentUIStyles } from '../agentkit/styles'

type DropPlacement = '' | 'before' | 'after'

withDefaults(defineProps<{
  id: string
  dataTabId?: string
  selected: boolean
  controls: string
  tabindex: number
  title?: string
  active?: boolean
  dragged?: boolean
  dragOver?: boolean
  dropPlacement?: DropPlacement
  draggable?: boolean
  disabled?: boolean
}>(), {
  dataTabId: undefined,
  title: undefined,
  active: false,
  dragged: false,
  dragOver: false,
  dropPlacement: '',
  draggable: false,
  disabled: false,
})

const emit = defineEmits<{
  click: [event: MouseEvent]
  keydown: [event: KeyboardEvent]
  dragstart: [event: DragEvent]
  dragover: [event: DragEvent]
  drop: [event: DragEvent]
  dragend: [event: DragEvent]
}>()

ensureAgentUIStyles()
</script>

<template>
  <div
    class="k-workbench-tab"
    :class="{
      'k-workbench-tab--active': active,
      'k-workbench-tab--dragged': dragged,
      'k-workbench-tab--drag-over': dragOver,
      'k-workbench-tab--drop-after': dropPlacement === 'after',
      'k-workbench-tab--drop-before': dropPlacement === 'before',
    }"
    :draggable="draggable ? 'true' : undefined"
    @dragstart="emit('dragstart', $event)"
    @dragover="emit('dragover', $event)"
    @drop="emit('drop', $event)"
    @dragend="emit('dragend', $event)"
  >
    <slot name="leading" />
    <button
      class="k-workbench-tab__button"
      :id="id"
      :data-k-tab-id="dataTabId"
      type="button"
      role="tab"
      :aria-selected="selected"
      :aria-controls="controls"
      :tabindex="tabindex"
      :title="title"
      :disabled="disabled"
      @click="emit('click', $event)"
      @keydown="emit('keydown', $event)"
    >
      <span class="k-workbench-tab__icon"><slot name="icon" /></span>
      <span class="k-workbench-tab__label"><slot /></span>
      <slot name="after-label" />
    </button>
    <slot name="trailing" />
  </div>
</template>
