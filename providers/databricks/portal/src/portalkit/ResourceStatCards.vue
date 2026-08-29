<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.

  ResourceStatCards provides the compact, icon-led summary used at the top of
  a resource detail page. The caller owns the facts, formatting, and icon
  choice; this component only supplies the responsive card geometry.
-->
<script setup lang="ts">
import type { Component } from 'vue'
import { computed } from 'vue'
import { ensureFarosUIStyles } from '../portalkit/styles'

ensureFarosUIStyles()

export type ResourceStatTone = 'default' | 'success' | 'warning' | 'danger'
export type ResourceStatDensity = 'default' | 'compact'

export interface ResourceStatCard {
  id: string
  label: string
  value: string | number
  detail?: string
  icon?: Component
  tone?: ResourceStatTone
  mono?: boolean
}

const props = withDefaults(defineProps<{
  cards: readonly ResourceStatCard[]
  ariaLabel?: string
  density?: ResourceStatDensity
}>(), {
  ariaLabel: 'Resource summary',
  density: 'default',
})

const cardLayout = computed(() => {
  const count = props.cards.length
  if (count <= 1) return 'count-1'
  if (count === 2) return 'count-2'
  if (count === 4) return 'count-4'
  return 'count-3-plus'
})
</script>

<template>
  <ul
    class="k-resource-stat-cards"
    :class="[
      `k-resource-stat-cards--${cardLayout}`,
      { 'k-resource-stat-cards--compact': props.density === 'compact' },
    ]"
    :aria-label="props.ariaLabel"
    :data-density="props.density"
    data-k-resource-stat-cards
  >
    <li
      v-for="card in props.cards"
      :key="card.id"
      class="k-resource-stat-card"
      :class="`k-resource-stat-card--${card.tone || 'default'}`"
      :data-k-resource-stat-card="card.id"
    >
      <span class="k-resource-stat-card__icon" aria-hidden="true">
        <component v-if="card.icon" :is="card.icon" :size="16" :stroke-width="1.75" />
        <slot v-else :name="`icon-${card.id}`" />
      </span>
      <span class="k-resource-stat-card__content">
        <span class="k-resource-stat-card__label">{{ card.label }}</span>
        <strong class="k-resource-stat-card__value" :class="{ mono: card.mono }">{{ card.value }}</strong>
        <span v-if="card.detail" class="k-resource-stat-card__detail">{{ card.detail }}</span>
      </span>
    </li>
  </ul>
</template>
