<!-- CANONICAL SOURCE — provider-sdk/agentkit-vue; sync with make sync-portalkit. -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = defineProps<{ text: string; active: boolean }>()
const viewport = ref<HTMLElement | null>(null)
const content = ref<HTMLElement | null>(null)
let observer: ResizeObserver | undefined
let motion: MediaQueryList | undefined
let animation: Animation | undefined

function refresh(): void {
  animation?.cancel()
  animation = undefined
  const outer = viewport.value
  const inner = content.value
  if (!outer || !inner) return
  outer.scrollLeft = 0
  const distance = inner.scrollWidth - outer.clientWidth
  if (!props.active || distance <= 1 || motion?.matches || !inner.animate) return
  // Pause at both ends and travel at a readable speed, only while engaged.
  const travel = distance / 30 * 1000
  const duration = travel + 1800
  animation = inner.animate([
    { transform: 'translateX(0)', offset: 0 },
    { transform: 'translateX(0)', offset: 900 / duration },
    { transform: `translateX(-${distance}px)`, offset: (900 + travel) / duration },
    { transform: `translateX(-${distance}px)`, offset: 1 },
  ], { duration, iterations: Infinity, direction: 'alternate', easing: 'linear' })
}
watch(() => [props.text, props.active], async () => { await nextTick(); refresh() })
onMounted(() => {
  motion = window.matchMedia?.('(prefers-reduced-motion: reduce)')
  motion?.addEventListener('change', refresh)
  if (typeof ResizeObserver !== 'undefined') {
    observer = new ResizeObserver(refresh)
    if (viewport.value) observer.observe(viewport.value)
    if (content.value) observer.observe(content.value)
  }
  refresh()
})
onBeforeUnmount(() => {
  observer?.disconnect()
  motion?.removeEventListener('change', refresh)
  animation?.cancel()
})
</script>

<template>
  <span ref="viewport" class="k-ai-overflow-title" :class="{ 'is-active': active }">
    <span ref="content" class="k-ai-overflow-title__text">{{ text }}</span>
  </span>
</template>

<style scoped>
.k-ai-overflow-title { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.k-ai-overflow-title__text { white-space: nowrap; }
.k-ai-overflow-title.is-active .k-ai-overflow-title__text { display: inline-block; }
.k-ai-overflow-title.is-active { text-overflow: clip; }
@media (prefers-reduced-motion: reduce) {
  .k-ai-overflow-title.is-active { overflow-x: auto; scrollbar-width: none; }
  .k-ai-overflow-title.is-active::-webkit-scrollbar { display: none; }
}
</style>
