<script setup lang="ts">
import { Inbox, AlertCircle } from 'lucide-vue-next'

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
  <div class="k-table">
    <!-- Error -->
    <div v-if="error" class="flex items-center gap-2 p-4 text-[13px] text-danger">
      <AlertCircle class="h-4 w-4 shrink-0" :stroke-width="1.75" />
      {{ error }}
    </div>

    <!-- Shimmer -->
    <div v-else-if="loading" class="space-y-0">
      <div class="border-b border-border-subtle px-5 py-3">
        <div class="shimmer h-3 w-24 rounded" />
      </div>
      <div v-for="i in 5" :key="i" class="flex items-center gap-6 border-b border-border-subtle px-5 py-3.5 last:border-b-0">
        <div class="shimmer h-3 w-32 rounded" />
        <div class="shimmer h-3 w-20 rounded" />
        <div class="shimmer h-3 w-16 rounded" />
      </div>
    </div>

    <!-- Table -->
    <table v-else>
      <thead>
        <tr>
          <th v-for="col in columns" :key="col.key">{{ col.label }}</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="(row, i) in rows"
          :key="i"
          class="stagger-item"
          :class="interactive ? 'is-interactive' : ''"
          :style="{ animationDelay: `${i * 35}ms` }"
          @click="onRowClick(row)"
        >
          <td v-for="col in columns" :key="col.key">
            <slot :name="col.key" :value="row[col.key]" :row="row">
              {{ row[col.key] }}
            </slot>
          </td>
        </tr>
        <tr v-if="rows.length === 0">
          <td :colspan="columns.length" class="py-16 text-center">
            <Inbox class="mx-auto h-8 w-8 text-text-muted/20" :stroke-width="1.25" />
            <p class="mt-2 text-[12px] text-text-muted">{{ emptyText }}</p>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
