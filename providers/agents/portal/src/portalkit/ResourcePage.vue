<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.

  ResourcePage owns page composition and read-state presentation only. It does
  not fetch, route, or decide what the resource body contains. Callers provide
  the snapshot and map `retry` to their own behavior. The resource kind,
  metadata, status, actions, summary, and body remain caller-owned.
-->
<script setup lang="ts">
import { computed, nextTick, ref, useAttrs, watch } from 'vue'
import { AlertCircle } from 'lucide-vue-next'
import { ensureRailgridUIStyles } from '../portalkit/styles'
import type { ResourceReadState, ResourceRefreshMode } from '../portalkit/page-state'
import { useDelayedLoading } from './useDelayedLoading'

ensureRailgridUIStyles()
const pageElement = ref<HTMLElement | null>(null)

const props = withDefaults(defineProps<{
  variant?: 'default' | 'collection'
  title: string
  fill?: boolean
  /** Hide only the title/action header while retaining read-state handling. */
  showHeader?: boolean
  /** Keep a single action or compact action menu beside the heading at narrow widths. */
  compactActions?: boolean
  kind?: string
  subtitle?: string
  loaded?: ResourceReadState['loaded']
  loading?: ResourceReadState['loading']
  refreshMode?: ResourceRefreshMode
  error?: ResourceReadState['error']
  stale?: ResourceReadState['stale']
  retryable?: ResourceReadState['retryable']
  /** Stable caller-owned identity; enables retry focus recovery within that identity. */
  focusScope?: string
}>(), {
  variant: 'default',
  fill: false,
  showHeader: true,
  compactActions: false,
  kind: '',
  subtitle: '',
  // A null sentinel preserves the distinction between an omitted read
  // contract and an explicit first read that has not completed.
  loaded: null,
  loading: false,
  refreshMode: 'foreground',
  error: null,
  stale: false,
  retryable: false,
})

const emit = defineEmits<{ retry: [] }>()
const attrs = useAttrs()
let invalidatedRetry: Element | null = null

function onFocusIn(event: FocusEvent) {
  // A fresh focus after a scope change belongs to the current scope.
  if (event.target === invalidatedRetry) invalidatedRetry = null
}

const explicitReadState = computed(() => props.loaded !== null)
const showInitialError = computed(() =>
  explicitReadState.value ? props.loaded === false && Boolean(props.error) : Boolean(props.error),
)
const initialReadPending = computed(() =>
  explicitReadState.value
    ? props.loaded === false && !props.error
    : Boolean(props.loading),
)
const showInitialLoading = useDelayedLoading(initialReadPending)
// A caller can leave an initial error visible while it retries. Keep the
// error useful, but expose that the request is in flight and prevent a second
// Retry event until the caller settles it. `loading` is the caller-owned
// source of truth; ResourcePage never starts or cancels a read itself.
const retryRequested = ref(false)
const retrying = computed(() => retryRequested.value || (Boolean(props.loading) && Boolean(props.error)
  && (showInitialError.value || props.refreshMode === 'foreground')))
const refreshAnnouncement = computed(() => {
  if (retrying.value) return `Retrying ${props.title}…`
  if (explicitReadState.value && props.loaded === true && props.loading) {
    return props.refreshMode === 'foreground'
      ? `Refreshing ${props.title}…`
      : `Updating ${props.title}…`
  }
  return ''
})
const ariaBusy = computed(() => Boolean(props.loading) || initialReadPending.value || retryRequested.value)
const staleMessageRole = computed(() => props.refreshMode === 'background' ? 'status' : 'alert')
const staleMessageLive = computed(() => props.refreshMode === 'background' ? 'polite' : 'assertive')

function requestRetry() {
  if (retrying.value) return
  // Latch synchronously so a double activation cannot emit twice before the
  // caller's loading prop has had a chance to render back down.
  retryRequested.value = true
  emit('retry')
}

// Retry is a caller-owned read transaction: once requested, keep the control
// latched through delayed acknowledgement and release it only when the caller
// reports a settled loading edge or clears the error.
function finishRetry() {
  retryRequested.value = false
  if (props.focusScope) return
  // The Retry button is about to leave the DOM. Move focus only if it is
  // still on this shell's button; do not pull a user back from another task.
  // A later background read can recover after an earlier retry failed.
  const focused = typeof document === 'undefined' ? null : document.activeElement
  const page = pageElement.value
  if (page && focused && focused.closest('.k-resource-page') === page
    && focused.classList.contains('k-resource-page__retry')) {
    const target = page.querySelector<HTMLElement>('.k-resource-page__title') ?? page
    target.focus({ preventScroll: true })
  }
}
watch(() => props.loading, (loading, wasLoading) => {
  if (wasLoading && !loading) {
    if (!props.error) finishRetry()
    else retryRequested.value = false
  }
})
watch(() => props.error, error => {
  if (!error) finishRetry()
})

// A retry control can disappear or move between the initial and stale error
// branches. Only restore focus while the caller's resource identity is stable.
watch(() => [props.error, props.loaded, props.retryable, props.focusScope] as const, async (current, previous, onCleanup) => {
  const root = pageElement.value
  if (!root) return
  const document = root.ownerDocument
  const focused = document.activeElement
  if (!(focused instanceof HTMLElement) || focused.closest('.k-resource-page') !== root || !focused.matches('.k-resource-page__retry')) return
  if (current[3] !== previous[3]) {
    invalidatedRetry = focused
    return
  }
  if (!current[3] || focused === invalidatedRetry) return
  let cancelled = false
  onCleanup(() => { cancelled = true })
  await nextTick()
  if (cancelled || props.focusScope !== current[3] || !root.isConnected || focused.isConnected || document.activeElement !== document.body) return
  const target = root.querySelector<HTMLElement>(':scope > .k-resource-page__read-error > .k-resource-page__retry, :scope > .k-resource-page__stale > .k-resource-page__retry')
    ?? root.querySelector<HTMLElement>(':scope > .k-resource-page__header .k-resource-page__title') ?? root
  if (!target.matches('.k-resource-page__retry')) target.setAttribute('tabindex', '-1')
  target.focus()
})
</script>

<template>
  <section
    ref="pageElement"
    class="k-resource-page"
    tabindex="-1"
    :class="{ 'k-resource-page--fill': fill, 'k-resource-page--collection': variant === 'collection' }"
    :aria-busy="ariaBusy"
    :aria-label="typeof attrs['aria-label'] === 'string' ? attrs['aria-label'] : ((!showHeader || $slots.header) && !attrs['aria-labelledby'] ? title : undefined)"
    @focusin="onFocusIn"
  >
    <!-- Keep read announcements out of layout while preserving the
      caller-owned body. Foreground refreshes, background updates, and initial
      retries use distinct, truthful copy in one polite live region. -->
    <Teleport :to="pageElement?.closest('dialog, [aria-modal=true]') ?? 'body'">
      <span
        class="k-resource-page__live"
        role="status"
        aria-live="polite"
        aria-atomic="true"
        style="block-size: 1px; clip: rect(0 0 0 0); clip-path: inset(50%); inline-size: 1px; margin: -1px; overflow: hidden; padding: 0; position: absolute; white-space: nowrap;"
      >
        {{ showInitialLoading && initialReadPending ? `Loading ${title}` : refreshAnnouncement }}
      </span>
    </Teleport>
    <!-- A narrow header slot replaces only this inner heading/action content;
      the outer page header and all read-state surfaces remain authoritative. -->
    <header v-if="showHeader" class="k-resource-page__header" :class="{ 'k-resource-page__header--compact-actions': compactActions }">
      <slot name="header">
        <div class="k-resource-page__heading">
          <h1 class="k-resource-page__title" tabindex="-1">{{ title }}</h1>
          <div v-if="kind || $slots.meta || $slots.status" class="k-resource-page__meta">
            <span v-if="kind" class="k-resource-page__kind">{{ kind }}</span>
            <span v-if="kind && ($slots.meta || $slots.status)" class="k-resource-page__separator" aria-hidden="true">·</span>
            <template v-if="$slots.meta"><slot name="meta" /></template>
            <span v-if="$slots.meta && $slots.status" class="k-resource-page__separator" aria-hidden="true">·</span>
            <span v-if="$slots.status" class="k-resource-page__status"><slot name="status" /></span>
          </div>
          <p v-if="subtitle" class="k-resource-page__subtitle">{{ subtitle }}</p>
        </div>
        <div v-if="$slots.actions" class="k-resource-page__header-side">
          <div v-if="$slots.actions" class="k-resource-page__actions"><slot name="actions" /></div>
        </div>
      </slot>
    </header>

    <div v-if="showInitialError" class="k-resource-page__read-error" role="alert" aria-live="assertive">
      <AlertCircle class="k-resource-page__read-icon" :size="16" :stroke-width="1.75" aria-hidden="true" />
      <span class="k-resource-page__read-message">{{ error }}</span>
      <button
        v-if="retryable"
        type="button"
        class="k-btn k-btn--ghost k-resource-page__retry"
        :aria-disabled="retrying || undefined"
        :aria-busy="retrying || undefined"
        @click="requestRetry"
      >
        {{ retrying ? 'Retrying…' : 'Retry' }}
      </button>
    </div>

    <div
      v-else-if="initialReadPending"
      class="k-resource-page__loading k-delayed-loading"
      aria-hidden="true"
      data-loading-visual
      :data-pending-hidden="showInitialLoading ? undefined : 'true'"
    >
      <!-- The shell owns loading semantics; callers can replace the visual
        placeholder when their page has a more specific layout. -->
      <slot name="loading">
        <div v-if="variant !== 'collection' && $slots.summary" class="k-resource-page__loading-summary" aria-hidden="true">
          <div v-for="n in 3" :key="n" class="k-resource-page__loading-stat"><span class="k-loading-shimmer k-resource-page__skeleton k-resource-page__skeleton--short k-resource-page__skeleton--small" /><span class="k-loading-shimmer k-resource-page__skeleton k-resource-page__skeleton--medium" /><span class="k-loading-shimmer k-resource-page__skeleton k-resource-page__skeleton--wide k-resource-page__skeleton--small" /></div>
        </div>
        <div v-if="variant === 'collection'" class="k-resource-page__loading-list" aria-hidden="true">
          <div v-for="n in 4" :key="n" class="k-resource-page__loading-row"><span class="k-loading-shimmer k-resource-page__skeleton k-resource-page__skeleton--medium" /><span class="k-loading-shimmer k-resource-page__skeleton k-resource-page__skeleton--short" /></div>
        </div>
        <div v-else class="k-resource-page__loading-section" aria-hidden="true">
          <div class="k-loading-shimmer k-resource-page__skeleton k-resource-page__skeleton--short" />
          <div class="k-loading-shimmer k-resource-page__skeleton k-resource-page__skeleton--wide" />
          <div class="k-loading-shimmer k-resource-page__skeleton k-resource-page__skeleton--medium" />
        </div>
      </slot>
    </div>

    <template v-else>
      <div
        v-if="explicitReadState && error"
        class="k-resource-page__stale"
        :role="staleMessageRole"
        :aria-live="staleMessageLive"
      >
        <AlertCircle class="k-resource-page__read-icon" :size="16" :stroke-width="1.75" aria-hidden="true" />
        <span class="k-resource-page__read-message">
          {{ stale ? 'Showing the last successful result. ' : '' }}{{ error }}
        </span>
        <button
          v-if="retryable"
          type="button"
          class="k-btn k-btn--ghost k-resource-page__retry"
          :aria-disabled="retrying || undefined"
          :aria-busy="retrying || undefined"
          @click="requestRetry"
        >
          {{ retrying ? 'Retrying…' : 'Retry' }}
        </button>
      </div>
      <div v-if="$slots.summary" class="k-resource-page__summary">
        <slot name="summary" />
      </div>
      <div class="k-resource-page__body">
        <slot name="body"><slot /></slot>
      </div>
    </template>
  </section>
</template>
