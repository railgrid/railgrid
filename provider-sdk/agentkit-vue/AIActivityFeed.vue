<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIActivityFeed owns disclosure geometry and keyed expansion. Providers own
  parsing, grouping, status interpretation, and the details slot projection.
-->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronDown, Circle } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIActivityGroup, AIActivityRow, AIActivitySummary } from '../agentkit/activity'
import AIActionRow from './AIActionRow.vue'
import AIActivityDisclosure from './AIActivityDisclosure.vue'

const props = withDefaults(defineProps<{
  panelId: string
  groups?: readonly AIActivityGroup[]
  summary?: AIActivitySummary | string
  count?: number | string
  label?: string
  expanded?: boolean
  busy?: boolean
  attention?: boolean
  error?: boolean
  /** Controlled group state. Omitted groups start expanded. */
  collapsedGroupKeys?: readonly string[]
  /** Controlled row state. Omitted rows start collapsed. */
  expandedRowKeys?: readonly string[]
}>(), {
  groups: () => [],
  summary: '',
  count: undefined,
  label: 'action',
  expanded: false,
  busy: false,
  attention: false,
  error: false,
  collapsedGroupKeys: undefined,
  expandedRowKeys: undefined,
})

const emit = defineEmits<{
  toggle: []
  toggleGroup: [key: string]
  toggleRow: [id: string]
}>()

const internalCollapsedGroups = ref(new Set<string>())
const internalExpandedRows = ref(new Set<string>())

ensureAgentUIStyles()

const activityCount = computed(() => {
  if (props.count !== undefined) return props.count
  return props.groups.reduce((count, group) => count + group.rows.length, 0)
})

const summaryView = computed<AIActivitySummary>(() => {
  if (typeof props.summary === 'object') {
    return { ...props.summary, count: props.summary.count ?? activityCount.value }
  }
  return {
    count: activityCount.value,
    ...(props.summary ? { text: props.summary } : {}),
  }
})

const summaryBusy = computed(() => props.busy || Boolean(summaryView.value.busy))
const summaryAttention = computed(() => props.attention || Boolean(summaryView.value.attention))
const summaryError = computed(() => props.error || Boolean(summaryView.value.error))

function isGroupExpanded(group: AIActivityGroup): boolean {
  if (props.collapsedGroupKeys !== undefined) return !props.collapsedGroupKeys.includes(group.key)
  return !internalCollapsedGroups.value.has(group.key)
}

function isRowExpanded(row: AIActivityRow): boolean {
  if (!row.expandable) return false
  if (props.expandedRowKeys !== undefined) return props.expandedRowKeys.includes(row.id)
  return row.expanded ?? internalExpandedRows.value.has(row.id)
}

function groupPanelID(group: AIActivityGroup): string {
  const key = group.key.replace(/[^a-zA-Z0-9_-]/g, '-') || 'group'
  return `${props.panelId}-${key}`
}

function toggleGroup(group: AIActivityGroup): void {
  if (props.collapsedGroupKeys === undefined) {
    const next = new Set(internalCollapsedGroups.value)
    if (next.has(group.key)) next.delete(group.key)
    else next.add(group.key)
    internalCollapsedGroups.value = next
  }
  emit('toggleGroup', group.key)
}

function toggleRow(row: AIActivityRow): void {
  if (props.expandedRowKeys === undefined) {
    const next = new Set(internalExpandedRows.value)
    if (next.has(row.id)) next.delete(row.id)
    else next.add(row.id)
    internalExpandedRows.value = next
  }
  emit('toggleRow', row.id)
}

function rowDetailsID(row: AIActivityRow): string | undefined {
  if (!row.expandable) return undefined
  return row.detailsId || `${props.panelId}-${row.id.replace(/[^a-zA-Z0-9_-]/g, '-')}-details`
}

function isGroupScrollable(group: AIActivityGroup): boolean {
  return group.scrollable ?? group.rows.length > 6
}
</script>

<template>
  <AIActivityDisclosure
    class="k-ai-activity-feed"
    :panel-id="panelId"
    :count="summaryView.count"
    :summary="summaryView.text || ''"
    :label="summaryView.label || label"
    :expanded="expanded"
    :busy="summaryBusy"
    :attention="summaryAttention"
    :error="summaryError"
    @toggle="emit('toggle')"
  >
    <div class="k-ai-activity-feed__groups">
      <section v-for="group in groups" :key="group.key" class="k-ai-activity-feed__group">
        <button
          v-if="group.label"
          type="button"
          class="k-ai-activity-feed__group-toggle"
          :aria-expanded="isGroupExpanded(group)"
          :aria-controls="groupPanelID(group)"
          @click="toggleGroup(group)"
        >
          <span class="k-ai-activity-feed__group-icon" :class="{ 'k-ai-activity-feed__group-icon--busy': group.busy }">
            <slot name="group-icon" :group="group">
              <Circle :stroke-width="1.75" aria-hidden="true" />
            </slot>
          </span>
          <span class="k-ai-activity-feed__group-label">{{ group.label }}</span>
          <ChevronDown
            class="k-ai-activity-feed__group-chevron"
            :class="{ 'is-collapsed': !isGroupExpanded(group) }"
            :stroke-width="1.75"
            aria-hidden="true"
          />
        </button>

        <div
          v-show="isGroupExpanded(group)"
          :id="groupPanelID(group)"
          class="k-ai-activity-feed__group-rows"
          :class="{
            'k-ai-activity-feed__group-rows--ungrouped': !group.label,
            'k-ai-activity-feed__group-rows--scrollable': isGroupScrollable(group),
          }"
        >
          <AIActionRow
            v-for="row in group.rows"
            :key="row.id"
            :id="row.id"
            :title="row.title"
            :status="row.status"
            :status-label="row.statusLabel"
            :target="row.target"
            :outcome="row.outcome"
            :busy="row.busy"
            :attention="row.attention"
            :error="row.error"
            :canceled="row.canceled"
            :expandable="row.expandable"
            :expanded="isRowExpanded(row)"
            :details-id="rowDetailsID(row)"
            @toggle="toggleRow(row)"
          >
            <template v-if="$slots['row-icon']" #icon>
              <slot name="row-icon" :row="row" :group="group" />
            </template>
            <template v-if="$slots.details" #details>
              <slot name="details" :row="row" :group="group" />
            </template>
          </AIActionRow>
        </div>
      </section>
      <div v-if="!groups.length" class="k-ai-activity-feed__empty">No activity</div>
    </div>
  </AIActivityDisclosure>
</template>
