<script setup lang="ts">
import { ref } from 'vue'
import { useDelayedLoading } from './useDelayedLoading'

defineProps<{ label: string; tabs?: boolean; compact?: boolean }>()

const showLoading = useDelayedLoading(ref(true))
</script>

<template>
  <div>
    <span class="k-loading-skeleton__status" role="status">{{ showLoading ? label : '' }}</span>
    <div
      class="k-loading-skeleton k-delayed-loading"
      :class="{ 'k-loading-skeleton--compact': compact }"
      aria-hidden="true"
      data-loading-visual
      :data-pending-hidden="showLoading ? undefined : 'true'"
    >
      <slot>
        <div v-if="tabs" class="k-loading-skeleton__tabs">
          <div v-for="tab in 3" :key="tab" class="k-loading-shimmer" />
        </div>
        <div class="k-loading-skeleton__heading k-loading-shimmer" />
        <div class="k-loading-skeleton__panel">
          <div v-for="row in (compact ? 2 : 4)" :key="row" class="k-loading-shimmer" />
        </div>
      </slot>
    </div>
  </div>
</template>
