<script setup lang="ts">
import { onMounted, ref, shallowRef } from 'vue'
import { useDelayedLoading } from './useDelayedLoading'

defineProps<{ label: string; tabs?: boolean; compact?: boolean }>()

const showLoading = useDelayedLoading(ref(true))
const root = ref<HTMLElement | null>(null)
const announcementTarget = shallowRef<string | Element | null>(null)

onMounted(() => {
  const dialog = root.value?.closest('dialog, [aria-modal="true"]')
  // Keep announcements in the active modal when its root is outside the busy region.
  announcementTarget.value = dialog && !dialog.closest('[aria-busy="true"]') ? dialog : 'body'
})
</script>

<template>
  <div ref="root">
    <Teleport v-if="announcementTarget" :to="announcementTarget">
      <span class="k-loading-skeleton__status" role="status">{{ showLoading ? label : '' }}</span>
    </Teleport>
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
