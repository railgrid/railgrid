<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue.

  AIConversationTurn only composes the neutral AIMessage frame. Providers
  retain transport, projection, ordering, sanitization, and lifecycle state;
  each presentation surface arrives through a named slot.
-->
<script setup lang="ts">
import { Comment, Fragment, Text, computed, useSlots, type VNode } from 'vue'
import AIMessage from './AIMessage.vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIConversationTurnRole } from './conversation'
import { safeConversationID } from './conversation'

const props = withDefaults(defineProps<{
  /** Stable provider-owned turn identity. */
  turnId?: string
  /** Compatibility alias for adapters that already call the identity `id`. */
  id?: string
  /** Compatibility alias for message-oriented projections. */
  messageId?: string
  role: AIConversationTurnRole
  bubble?: boolean
  ariaLabel?: string
}>(), {
  turnId: '',
  id: '',
  messageId: '',
  bubble: false,
  ariaLabel: '',
})

const generatedTurnID = safeConversationID('k-ai-conversation-turn')
const resolvedTurnID = computed(() => {
  const value = props.turnId || props.id || props.messageId
  return value ? safeConversationID('k-ai-conversation-turn', value) : generatedTurnID
})

const slots = useSlots()

/**
 * Vue keeps a declared conditional slot as a comment vnode while its
 * condition is false. Check the rendered vnode tree at render time so those
 * placeholders do not create empty flex regions in the message frame.
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

function outputSlotName(): 'output' | 'default' | undefined {
  if (hasRenderableSlot('output')) return 'output'
  if (hasRenderableSlot('default')) return 'default'
  return undefined
}

ensureAgentUIStyles()
</script>

<template>
  <AIMessage
    :id="resolvedTurnID"
    class="k-ai-conversation-turn"
    :role="role"
    :bubble="bubble"
    :aria-label="ariaLabel || undefined"
  >
    <template v-if="hasRenderableSlot('before') || hasRenderableSlot('progress') || hasRenderableSlot('trace')" #before>
      <div v-if="hasRenderableSlot('before')" class="k-ai-conversation-turn__before">
        <slot name="before" />
      </div>
      <div v-if="hasRenderableSlot('progress')" class="k-ai-conversation-turn__progress">
        <slot name="progress" />
      </div>
      <div v-if="hasRenderableSlot('trace')" class="k-ai-conversation-turn__trace">
        <slot name="trace" />
      </div>
    </template>

    <template v-if="outputSlotName()" #default>
      <template v-if="outputSlotName() === 'output'">
        <slot name="output" />
      </template>
      <template v-else>
        <slot />
      </template>
    </template>

    <template v-if="hasRenderableSlot('interrupt') || hasRenderableSlot('metadata') || hasRenderableSlot('after')" #after>
      <div v-if="hasRenderableSlot('interrupt')" class="k-ai-conversation-turn__interrupt">
        <slot name="interrupt" />
      </div>
      <div v-if="hasRenderableSlot('metadata')" class="k-ai-conversation-turn__metadata">
        <slot name="metadata" />
      </div>
      <div v-if="hasRenderableSlot('after')" class="k-ai-conversation-turn__after">
        <slot name="after" />
      </div>
    </template>
  </AIMessage>
</template>
