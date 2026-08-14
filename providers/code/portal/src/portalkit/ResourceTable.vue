<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies under providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`. -->
<script setup lang="ts">
import { computed } from 'vue'
import { AlertCircle, Inbox } from 'lucide-vue-next'

const props = withDefaults(defineProps<{
  columns: Array<{ key: string; label: string }>
  rows: Array<Record<string, unknown>>
  /** Stable row identity. Resource names/ids are used when omitted. */
  rowKey?: string | ((row: Record<string, unknown>, index: number) => string | number)
  /** True after the first authoritative read has completed successfully. */
  loaded?: boolean | null
  /** True while a read is in flight. Cached rows remain visible after load. */
  loading?: boolean
  error?: string | null
  /** Marks cached rows as stale when the latest read failed. */
  stale?: boolean
  /** Shows the built-in Retry action. Callers must handle the retry event. */
  retryable?: boolean
  emptyText?: string
  interactive?: boolean
}>(), {
  // Vue casts an omitted Boolean prop to false in child components. A null
  // sentinel preserves omission so legacy callers retain loading -> content
  // behavior while explicit false still means the first read is incomplete.
  loaded: null,
  stale: false,
  retryable: false,
  emptyText: 'No data',
  interactive: true,
})

// `loaded` is intentionally optional. Existing PortalKit consumers predate
// the first-read contract and keep the original error -> loading -> content
// precedence. Callers that pass `loaded` opt into cached/stale refresh states.
const explicitReadState = computed(() => props.loaded !== null)
const showInitialError = computed(() =>
  explicitReadState.value ? props.loaded === false && !!props.error : !!props.error,
)
const showInitialLoading = computed(() =>
  explicitReadState.value ? props.loaded === false : !!props.loading,
)
const ariaBusy = computed(() =>
  explicitReadState.value
    ? (!!props.loading && !(props.loaded === false && !!props.error)) || (props.loaded === false && !props.error)
    : !!props.loading,
)

const emit = defineEmits<{
  rowClick: [row: Record<string, unknown>]
  retry: []
}>()

function onRowClick(row: Record<string, unknown>) {
  if (props.interactive) emit('rowClick', row)
}

function rowIdentity(row: Record<string, unknown>, index: number): string | number {
  if (typeof props.rowKey === 'function') return props.rowKey(row, index)
  if (typeof props.rowKey === 'string') {
    const value = row[props.rowKey]
    if (typeof value === 'string' || typeof value === 'number') return value
  }
  for (const key of ['name', 'id', 'uid']) {
    const value = row[key]
    if (typeof value === 'string' || typeof value === 'number') return value
  }
  return index
}
</script>

<template>
  <div
    class="resource-table"
    :aria-busy="ariaBusy"
  >
    <!-- Keep the live region outside layout so background reads cannot move the table. -->
    <span
      class="resource-table-live"
      role="status"
      aria-live="polite"
      aria-atomic="true"
      style="block-size: 1px; clip: rect(0 0 0 0); clip-path: inset(50%); inline-size: 1px; margin: -1px; overflow: hidden; padding: 0; position: absolute; white-space: nowrap;"
    >
      {{ explicitReadState && loading && loaded ? 'Updating…' : '' }}
    </span>
    <div v-if="showInitialError" class="resource-table-error" role="alert" aria-live="assertive">
      <AlertCircle class="resource-table-error-icon" :stroke-width="1.75" />
      <span class="resource-table-error-message">{{ error }}</span>
      <button v-if="retryable" class="resource-table-retry" type="button" @click="emit('retry')">Retry</button>
    </div>

    <div v-else-if="showInitialLoading" class="resource-table-loading" role="status" aria-live="polite" aria-label="Loading resources">
      <div class="resource-table-loading-head">
        <div class="shimmer resource-table-skeleton resource-table-skeleton-short" />
      </div>
      <div v-for="i in 5" :key="i" class="resource-table-loading-row">
        <div class="shimmer resource-table-skeleton resource-table-skeleton-wide" />
        <div class="shimmer resource-table-skeleton resource-table-skeleton-mid" />
        <div class="shimmer resource-table-skeleton resource-table-skeleton-small" />
      </div>
    </div>

    <template v-else>
      <div v-if="explicitReadState && error" class="resource-table-stale" role="alert" aria-live="assertive">
        <AlertCircle class="resource-table-error-icon" :stroke-width="1.75" />
        <span class="resource-table-error-message">
          {{ stale ? 'Showing the last successful result. ' : '' }}{{ error }}
        </span>
        <button v-if="retryable" class="resource-table-retry" type="button" @click="emit('retry')">Retry</button>
      </div>
      <table class="resource-table-table">
        <thead>
          <tr class="resource-table-head-row">
            <th
              v-for="col in columns"
              :key="col.key"
              class="resource-table-heading"
            >
              {{ col.label }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(row, i) in rows"
            :key="rowIdentity(row, i)"
            class="stagger-item resource-table-row"
            :class="{ 'is-interactive': interactive }"
            :style="{ animationDelay: `${i * 35}ms` }"
            @click="onRowClick(row)"
          >
            <td
              v-for="col in columns"
              :key="col.key"
              class="resource-table-cell"
            >
              <slot :name="col.key" :value="row[col.key]" :row="row">
                {{ row[col.key] }}
              </slot>
            </td>
          </tr>
          <tr v-if="rows.length === 0">
            <td :colspan="columns.length" class="resource-table-empty-cell">
              <Inbox class="resource-table-empty-icon" :stroke-width="1.25" />
              <p class="resource-table-empty-label">{{ emptyText }}</p>
            </td>
          </tr>
        </tbody>
      </table>
    </template>
  </div>
</template>
