<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.

  Tabs owns presentation and selection notification only. It deliberately does
  not know about routes: callers map `select` to their own navigation state.
  The shared faros-ui.css recipe is injected once because standalone provider
  portals render this component in light DOM without a Vite CSS asset.
-->
<script setup lang="ts">
import type { Component } from 'vue'
import { ensureFarosUIStyles } from '../portalkit/styles'

export interface PortalTabItem {
  id: string
  label: string
  icon?: Component
  count?: number | string
  active?: boolean
  disabled?: boolean
}

const props = defineProps<{
  tabs: readonly PortalTabItem[]
  /** Optional active id for callers that keep selection outside tab objects. */
  active?: string
  ariaLabel?: string
}>()

const emit = defineEmits<{
  select: [id: string]
}>()

ensureFarosUIStyles()

function isActive(tab: PortalTabItem): boolean {
  return tab.active ?? props.active === tab.id
}

function select(tab: PortalTabItem): void {
  if (!tab.disabled) emit('select', tab.id)
}
</script>

<template>
  <nav class="k-tabs" :aria-label="ariaLabel || 'Sections'">
    <button
      v-for="tab in tabs"
      :key="tab.id"
      class="k-tab"
      :class="{ 'k-tab--active': isActive(tab) }"
      type="button"
      :data-k-tab-id="tab.id"
      :aria-current="isActive(tab) ? 'page' : undefined"
      :disabled="tab.disabled || undefined"
      @click="select(tab)"
    >
      <span v-if="$slots.icon || tab.icon" class="k-tab__icon">
        <slot name="icon" :tab="tab">
          <component :is="tab.icon" aria-hidden="true" />
        </slot>
      </span>
      <span class="k-tab__label">{{ tab.label }}</span>
      <span v-if="tab.count !== undefined" class="k-tab__count">{{ tab.count }}</span>
    </button>
  </nav>
</template>
