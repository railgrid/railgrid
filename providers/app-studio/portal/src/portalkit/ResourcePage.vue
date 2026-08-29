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
import { computed, ref, watch } from 'vue'
import { AlertCircle } from 'lucide-vue-next'
import { ensureFarosUIStyles } from '../portalkit/styles'
import type { ResourceReadState, ResourceRefreshMode } from '../portalkit/page-state'
import { useDelayedLoading } from './useDelayedLoading'

ensureFarosUIStyles()

const props = withDefaults(defineProps<{
  title: string
  kind?: string
  subtitle?: string
  loaded?: ResourceReadState['loaded']
  loading?: ResourceReadState['loading']
  refreshMode?: ResourceRefreshMode
  error?: ResourceReadState['error']
  stale?: ResourceReadState['stale']
  retryable?: ResourceReadState['retryable']
}>(), {
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
const retrying = computed(() => retryRequested.value || (Boolean(props.loading) && Boolean(props.error)))
const refreshAnnouncement = computed(() => {
  if (showInitialError.value && props.loading) return `Retrying ${props.title}…`
  if (explicitReadState.value && props.loaded === true && props.loading) {
    return props.refreshMode === 'foreground'
      ? `Refreshing ${props.title}…`
      : `Updating ${props.title}…`
  }
  return ''
})
const ariaBusy = computed(() => Boolean(props.loading) || initialReadPending.value)
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
watch(() => props.loading, (loading, wasLoading) => {
  if (wasLoading && !loading) retryRequested.value = false
})
watch(() => props.error, error => {
  if (!error) retryRequested.value = false
})
</script>

<template>
  <section class="k-resource-page" :aria-busy="ariaBusy">
    <!-- Keep read announcements out of layout while preserving the
      caller-owned body. Foreground refreshes, background updates, and initial
      retries use distinct, truthful copy in one polite live region. -->
    <span
      class="k-resource-page__live"
      role="status"
      aria-live="polite"
      aria-atomic="true"
      style="block-size: 1px; clip: rect(0 0 0 0); clip-path: inset(50%); inline-size: 1px; margin: -1px; overflow: hidden; padding: 0; position: absolute; white-space: nowrap;"
    >
      {{ refreshAnnouncement }}
    </span>
    <header class="k-resource-page__header">
      <div class="k-resource-page__heading">
        <h1 class="k-resource-page__title">{{ title }}</h1>
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
    </header>

    <div v-if="showInitialError" class="k-resource-page__read-error" role="alert" aria-live="assertive">
      <AlertCircle class="k-resource-page__read-icon" :size="16" :stroke-width="1.75" aria-hidden="true" />
      <span class="k-resource-page__read-message">{{ error }}</span>
      <button
        v-if="retryable"
        type="button"
        class="k-btn k-btn--ghost k-resource-page__retry"
        :disabled="retrying"
        :aria-busy="retrying || undefined"
        @click="requestRetry"
      >
        {{ retrying ? 'Retrying…' : 'Retry' }}
      </button>
    </div>

    <div
      v-else-if="initialReadPending"
      class="k-resource-page__loading k-delayed-loading"
      :role="showInitialLoading ? 'status' : undefined"
      :aria-live="showInitialLoading ? 'polite' : undefined"
      :aria-label="showInitialLoading ? `Loading ${title}` : undefined"
      :aria-hidden="showInitialLoading ? undefined : 'true'"
    >
      <!-- The shell owns the loading semantics; callers may replace only the
        visual content when a resource needs a more specific placeholder. -->
      <slot name="loading">
        <div class="shimmer k-resource-page__skeleton k-resource-page__skeleton--short" />
        <div class="shimmer k-resource-page__skeleton k-resource-page__skeleton--wide" />
        <div class="shimmer k-resource-page__skeleton k-resource-page__skeleton--medium" />
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
          :disabled="retrying"
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
