<!-- CANONICAL SOURCE — edit here and run make sync-portalkit. -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { BookOpen, ChevronRight, Folder, GitCommitHorizontal, Image, Loader2, Pencil, Plug, Search, TerminalSquare, TriangleAlert } from 'lucide-vue-next'
import { activityCategory, activitySummaryTitle, type AIActivityRow } from '../agentkit/activity'
import { useDisclosureScroll } from './useDisclosureScroll'
import { ensureAgentUIStyles } from '../agentkit/styles'
import AIActionRow from './AIActionRow.vue'

const props = withDefaults(defineProps<{
  panelId: string
  rows: readonly AIActivityRow[]
  expandedRowKeys?: readonly string[]
}>(), { expandedRowKeys: undefined })
const emit = defineEmits<{ toggleRow: [id: string] }>()
const manualOpen = ref<boolean>()
const list = ref<HTMLElement | null>(null)
const follow = ref(true)
const showAll = ref(false)
const expandedRows = ref(new Set<string>())
const single = computed(() => props.rows.length === 1)
const busy = computed(() => props.rows.some(row => row.busy))
const failures = computed(() => props.rows.filter(row => row.error || row.canceled).length)
const reviewCount = computed(() => props.rows.filter(row => row.needsReview).length)
const attention = computed(() => props.rows.some(row => row.attention))
const title = computed(() => activitySummaryTitle(props.rows))
const categories = computed(() => new Set(props.rows.map(activityCategory)))
const summaryIcon = computed(() => categories.value.has('edit') ? Pencil : categories.value.has('inspect') ? (props.rows.some(row => activityCategory(row) === 'inspect' && !['read', 'list', 'search'].includes(row.iconKey || '')) ? Search : Folder) : categories.value.has('run') ? TerminalSquare : Plug)
const open = computed(() => manualOpen.value ?? (busy.value || props.rows.some(isExpanded)))
const bounded = computed(() => props.rows.length > 8 && !showAll.value && !props.rows.some(isExpanded))
function isExpanded(row: AIActivityRow): boolean {
  return Boolean(row.expandable && (props.expandedRowKeys ? props.expandedRowKeys.includes(row.id) : expandedRows.value.has(row.id)))
}
function toggleRow(row: AIActivityRow): void {
  if (!props.expandedRowKeys) {
    const next = new Set(expandedRows.value)
    if (next.has(row.id)) next.delete(row.id)
    else next.add(row.id)
    expandedRows.value = next
  }
  emit('toggleRow', row.id)
}
function toggleGroup(): void { manualOpen.value = !open.value }
function onScroll(): void {
  const el = list.value
  if (el) follow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 36
}
// Keep newly started calls visible in a bounded list until the reader scrolls up.
watch([() => props.rows.length, () => props.rows.filter(row => row.busy).map(row => row.id).join('\0'), open, bounded], async () => {
  await nextTick()
  if (follow.value && open.value && busy.value && bounded.value && list.value) list.value.scrollTop = list.value.scrollHeight
})
function icon(row: AIActivityRow) {
  switch (row.iconKey) {
    case 'run': return TerminalSquare
    case 'list': return Folder
    case 'read': return BookOpen
    case 'search': case 'inspect': return Search
    case 'edit': return Pencil
    case 'commit': return GitCommitHorizontal
    case 'image': return Image
    default: return Plug
  }
}
const compensateDisclosure = useDisclosureScroll()

ensureAgentUIStyles()
</script>

<template>
  <section v-if="rows.length" @click.capture="compensateDisclosure" class="k-ai-transcript-activity" :class="{ 'k-ai-transcript-activity--single': single }" aria-label="Activity">
    <button v-if="!single" type="button" class="k-ai-transcript-activity__toggle" :aria-expanded="open" :aria-controls="panelId" @click="toggleGroup">
      <Loader2 v-if="busy" class="k-ai-transcript-activity__icon is-busy" aria-hidden="true" />
      <TriangleAlert v-else-if="failures || attention" class="k-ai-transcript-activity__icon is-attention" aria-hidden="true" />
      <component :is="summaryIcon" v-else class="k-ai-transcript-activity__icon" aria-hidden="true" />
      <span class="k-ai-transcript-activity__summary">{{ title }} · {{ rows.length }} calls<span v-if="failures"> · {{ failures }} failed or canceled</span><span v-if="reviewCount"> · {{ reviewCount }} to review</span><span v-else-if="attention && !failures"> · Needs attention</span></span>
      <ChevronRight class="k-ai-transcript-activity__chevron" :class="{ 'is-expanded': open }" aria-hidden="true" />
    </button>
    <Transition name="k-ai-disclosure">
      <div v-show="single || open" class="k-ai-disclosure">
        <div class="k-ai-disclosure__inner">
          <div ref="list" :id="panelId" @scroll="onScroll" class="k-ai-transcript-activity__list" :class="{ 'is-bounded': bounded, 'is-grouped': !single }">
            <AIActionRow v-for="row in rows" :key="row.id" :id="row.id" :title="row.title" :status="row.status" :status-label="row.statusLabel" :target="row.target" :outcome="row.outcome" :busy="row.busy" :attention="row.attention" :error="row.error" :canceled="row.canceled" :expandable="row.expandable" :expanded="isExpanded(row)" :details-id="row.detailsId || `${panelId}-${row.id}-details`" @toggle="toggleRow(row)">
              <template #icon><component :is="icon(row)" class="k-ai-action-row__icon" aria-hidden="true" /></template>
              <template v-if="$slots.details" #details><slot name="details" :row="row" /></template>
            </AIActionRow>
          </div>
          <button v-if="!single && open && rows.length > 8" type="button" class="k-ai-transcript-activity__more" :aria-expanded="showAll" @click="showAll = !showAll">{{ showAll ? 'Show fewer activities' : 'Show all activities' }}</button>
        </div>
      </div>
    </Transition>
  </section>
</template>
