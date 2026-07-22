<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies under providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`. -->
<script setup lang="ts">
import { AlertCircle, Inbox } from 'lucide-vue-next'

const props = withDefaults(defineProps<{
  columns: Array<{ key: string; label: string }>
  rows: Array<Record<string, unknown>>
  loading?: boolean
  error?: string | null
  emptyText?: string
  interactive?: boolean
}>(), {
  emptyText: 'No data',
  interactive: true,
})

const emit = defineEmits<{
  rowClick: [row: Record<string, unknown>]
}>()

function onRowClick(row: Record<string, unknown>) {
  if (props.interactive) emit('rowClick', row)
}
</script>

<template>
  <div class="resource-table">
    <div v-if="error" class="resource-table-error">
      <AlertCircle class="resource-table-error-icon" :stroke-width="1.75" />
      {{ error }}
    </div>

    <div v-else-if="loading">
      <div class="resource-table-loading-head">
        <div class="shimmer resource-table-skeleton resource-table-skeleton-short" />
      </div>
      <div v-for="i in 5" :key="i" class="resource-table-loading-row">
        <div class="shimmer resource-table-skeleton resource-table-skeleton-wide" />
        <div class="shimmer resource-table-skeleton resource-table-skeleton-mid" />
        <div class="shimmer resource-table-skeleton resource-table-skeleton-small" />
      </div>
    </div>

    <table v-else class="resource-table-table">
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
          :key="i"
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
  </div>
</template>
