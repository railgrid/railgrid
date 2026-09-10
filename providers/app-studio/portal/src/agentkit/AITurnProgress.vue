<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue.

  AITurnProgress owns the compact worked-state disclosure and its ARIA
  relationship. The provider supplies an already-formatted duration and the
  ordered trace details through a slot; this component never starts a clock.
-->
<script setup lang="ts">
import { Comment, Fragment, Text, computed, useSlots, type VNode } from 'vue'
import { ChevronRight, TriangleAlert } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AITurnProgressStatus } from './conversation'
import { aiTurnProgressLabel, isAITurnProgressRunning, safeConversationID } from './conversation'

const props = withDefaults(defineProps<{
  turnId?: string
  messageId?: string
  /** Optional explicit region identity for callers that need stable linking. */
  regionId?: string
  status?: AITurnProgressStatus
  /** A provider-formatted duration. No client-side elapsed time is inferred. */
  duration?: string | null
  interrupted?: boolean
  /** Expansion is controlled by the provider; this component emits `toggle`. */
  expanded?: boolean
  ariaLabel?: string
}>(), {
  turnId: '',
  messageId: '',
  regionId: '',
  status: 'pending',
  duration: '',
  interrupted: false,
  expanded: false,
  ariaLabel: '',
})

const emit = defineEmits<{
  toggle: [expanded: boolean]
}>()

const slots = useSlots()

/**
 * Vue keeps a declared conditional slot as a comment vnode while its
 * condition is false. Match AIConversationTurn's render-time inspection so
 * an empty details slot does not expose a disclosure or an empty region.
 */
function hasRenderableNodes(nodes: readonly unknown[]): boolean {
  return nodes.some((node) => {
    if (node === null || node === undefined || typeof node === 'boolean') return false
    if (typeof node === 'string') return node.trim().length > 0
    if (typeof node !== 'object') return true

    const vnode = node as VNode
    if (vnode.type === Comment) return false
    if (vnode.type === Fragment) {
      return Array.isArray(vnode.children) && hasRenderableNodes(vnode.children)
    }
    if (vnode.type === Text) {
      return typeof vnode.children === 'string'
        ? vnode.children.trim().length > 0
        : hasRenderableNodes(Array.isArray(vnode.children) ? vnode.children : [vnode.children])
    }
    return true
  })
}

function hasRenderableSlot(name: string): boolean {
  const slot = slots[name]
  return Boolean(slot && hasRenderableNodes(slot()))
}

function detailsSlotName(): 'details' | 'default' | undefined {
  if (hasRenderableSlot('details')) return 'details'
  if (hasRenderableSlot('default')) return 'default'
  return undefined
}

function hasDetails(): boolean {
  return detailsSlotName() !== undefined
}
const generatedProgressRegionID = safeConversationID('k-ai-turn-progress')
const progressRegionID = computed(() => {
  if (props.regionId.trim()) return props.regionId.trim()
  const value = props.turnId || props.messageId
  return value ? safeConversationID('k-ai-turn-progress', value) : generatedProgressRegionID
})
const statusClass = computed(() => sanitizeStatusClass(props.status))
const isRunning = computed(() => isAITurnProgressRunning(props.status))
const isInterrupted = computed(() => props.interrupted || props.status === 'interrupted')
const progressLabel = computed(() => aiTurnProgressLabel(props.status, props.duration))
function accessibleLabel(): string {
  if (props.ariaLabel.trim()) return props.ariaLabel.trim()
  const prefix = `${progressLabel.value}${isInterrupted.value ? '. Interrupted' : ''}`
  return hasDetails() ? `${prefix}. ${props.expanded ? 'Hide' : 'Show'} task details.` : prefix
}

function toggle(): void {
  emit('toggle', !props.expanded)
}

function sanitizeStatusClass(status: AITurnProgressStatus): string {
  return status.toLowerCase().replace(/[^a-z0-9_-]+/g, '-') || 'pending'
}

ensureAgentUIStyles()
</script>

<template>
  <section
    class="k-ai-turn-progress"
    :class="[`k-ai-turn-progress--${statusClass}`, { 'k-ai-turn-progress--interrupted': isInterrupted }]"
    :data-status="status"
  >
    <button
      v-if="hasDetails()"
      type="button"
      class="k-ai-turn-progress__trigger"
      :aria-expanded="expanded"
      :aria-controls="progressRegionID"
      :aria-label="accessibleLabel()"
      @click="toggle"
    >
      <span class="k-ai-turn-progress__label">{{ progressLabel }}</span>
      <span v-if="isInterrupted" class="k-ai-turn-progress__interrupted">
        <span aria-hidden="true">·</span>
        <TriangleAlert :stroke-width="2" aria-hidden="true" />
        <span>Interrupted</span>
      </span>
      <ChevronRight
        class="k-ai-turn-progress__chevron"
        :class="{ 'is-expanded': expanded }"
        :stroke-width="1.75"
        aria-hidden="true"
      />
    </button>
    <span v-else class="k-ai-turn-progress__status" :aria-label="accessibleLabel()">
      <span class="k-ai-turn-progress__label">{{ progressLabel }}</span>
      <span v-if="isInterrupted" class="k-ai-turn-progress__interrupted">
        <span aria-hidden="true">·</span>
        <TriangleAlert :stroke-width="2" aria-hidden="true" />
        <span>Interrupted</span>
      </span>
    </span>

    <div
      v-if="hasDetails()"
      v-show="expanded"
      :id="progressRegionID"
      class="k-ai-turn-progress__details"
      :role="isRunning ? 'log' : undefined"
      :aria-live="isRunning ? 'polite' : undefined"
      :aria-relevant="isRunning ? 'additions' : undefined"
      aria-atomic="false"
    >
      <slot v-if="detailsSlotName() === 'details'" name="details" />
      <slot v-else />
    </div>
  </section>
</template>
