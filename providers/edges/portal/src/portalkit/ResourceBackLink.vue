<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.

  ResourceBackLink keeps the browser-safe href on detail-route navigation while
  allowing a Vue owner to retain its in-app route/state transition. The caller
  owns the href, disabled guard, and handling of the back event.
-->
<script setup lang="ts">
import { ArrowLeft } from 'lucide-vue-next'
import { ensureFarosUIStyles } from '../portalkit/styles'

ensureFarosUIStyles()

const props = withDefaults(defineProps<{
  /** The collection URL used when the Vue route handler is unavailable. */
  href: string
  /** Prevents the in-app event and native fallback while the resource is busy. */
  disabled?: boolean
}>(), {
  disabled: false,
})

const emit = defineEmits<{
  back: [event: MouseEvent]
}>()

function handleClick(event: MouseEvent): void {
  // A disabled anchor keeps its real href for inspection, but is removed from
  // the tab order and must not navigate or hand control to its route owner
  // for any kind of click.
  if (props.disabled) {
    event.preventDefault()
    return
  }

  // Preserve ordinary link affordances for modified and non-primary clicks:
  // Cmd/Ctrl-click opens the real href in another context, while middle- and
  // secondary-click retain the browser's native menu/tab behavior. Only an
  // unmodified primary activation belongs to the in-app back event.
  if (
    event.button !== 0 ||
    event.metaKey ||
    event.ctrlKey ||
    event.shiftKey ||
    event.altKey
  ) return

  // Keep the real href as a no-JavaScript/browser fallback, but let the Vue
  // route owner handle an active click in the mounted application.
  event.preventDefault()
  emit('back', event)
}

function handleAuxClick(event: MouseEvent): void {
  // Middle-button activation is dispatched as `auxclick`, not `click`, in
  // modern browsers. Keep native new-tab behavior for active links while
  // ensuring an aria-disabled back action cannot navigate around its guard.
  if (props.disabled) event.preventDefault()
}
</script>

<template>
  <a
    class="k-btn k-btn--ghost k-back-action"
    :href="href"
    :aria-disabled="disabled ? 'true' : undefined"
    :tabindex="disabled ? -1 : undefined"
    @click="handleClick"
    @auxclick="handleAuxClick"
  >
    <ArrowLeft :size="14" :stroke-width="1.75" aria-hidden="true" />
    <slot>Back</slot>
  </a>
</template>
