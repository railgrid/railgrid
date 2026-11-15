<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Run make sync-portalkit after editing. -->
<script setup lang="ts">
import { nextTick, ref, type Component } from 'vue'
import { Grid2X2, List } from 'lucide-vue-next'
import { layoutModes, type LayoutMode } from './layoutPreference'

const props = withDefaults(defineProps<{
  modelValue: LayoutMode
  ariaLabel?: string
  gridLabel?: string
  gridIcon?: Component
}>(), { ariaLabel: 'Layout', gridLabel: 'Grid' })
const emit = defineEmits<{ 'update:modelValue': [mode: LayoutMode] }>()
const root = ref<HTMLElement | null>(null)

function choose(mode: LayoutMode): void {
  if (mode !== props.modelValue) emit('update:modelValue', mode)
}

async function navigate(event: KeyboardEvent): Promise<void> {
  if (event.isComposing || event.keyCode === 229) return
  // Modified arrows and Home/End belong to the browser and assistive technology.
  if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
  const index = layoutModes.indexOf(props.modelValue)
  let next: number
  if (event.key === 'ArrowRight' || event.key === 'ArrowDown') next = (index + 1) % layoutModes.length
  else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') next = (index + layoutModes.length - 1) % layoutModes.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = layoutModes.length - 1
  else return
  event.preventDefault()
  choose(layoutModes[next])
  await nextTick()
  root.value?.querySelectorAll<HTMLButtonElement>('[role="radio"]')[next]?.focus()
}
</script>

<template>
  <div ref="root" class="k-layout-selector" role="radiogroup" :aria-label="ariaLabel" @keydown="navigate">
    <button v-for="mode in layoutModes" :key="mode" type="button" role="radio" class="k-layout-selector__item"
      :class="{ 'is-selected': mode === modelValue }" :aria-checked="mode === modelValue"
      :tabindex="mode === modelValue ? 0 : -1" @click="choose(mode)">
      <component :is="gridIcon || Grid2X2" v-if="mode === 'grid'" class="k-layout-selector__icon" :stroke-width="1.75" aria-hidden="true" />
      <List v-else class="k-layout-selector__icon" :stroke-width="1.75" aria-hidden="true" />
      <span>{{ mode === 'grid' ? gridLabel : 'List' }}</span>
    </button>
  </div>
</template>
