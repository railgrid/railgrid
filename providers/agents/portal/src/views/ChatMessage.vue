<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, onUpdated, ref } from 'vue'
import { Check, ChevronRight, LoaderCircle, X } from 'lucide-vue-next'
import { attachCodeCopy, sanitizedMarkdown } from '../vue/chat'
import { fmtDuration, fmtTokens, fmtUSD, prettyJSON, type ChatMessage, type ToolCall } from '../types'
import { approvalDisclosureAvailable } from '../approval-disclosure'
import ApprovalDisclosure from '../components/ApprovalDisclosure.vue'

const props = withDefaults(defineProps<{
  message: ChatMessage
  announce?: boolean
  approvalBusy?: 'approve' | 'deny'
}>(), { announce: false, approvalBusy: undefined })
const emit = defineEmits<{ approval: [detail: { inboxID: string; decision: 'approve' | 'deny' }] }>()

const root = ref<HTMLElement | null>(null)
const expanded = ref(new Set<string>())
const assistantHTML = computed(() => {
  if (props.message.role !== 'assistant') return ''
  const caret = props.message.streaming ? '<span class="agents-caret" aria-hidden="true"></span>' : ''
  return sanitizedMarkdown(props.message.content) + caret
})
let mounted = false

function toggle(id: string): void {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

function stateFor(tool: ToolCall): 'pending' | 'err' | 'ok' {
  return tool.pending ? 'pending' : tool.error ? 'err' : 'ok'
}

function wireCopyActions(): void {
  void nextTick(() => {
    if (mounted && root.value) attachCodeCopy(root.value)
  })
}

onMounted(() => {
  mounted = true
  wireCopyActions()
})
onUpdated(wireCopyActions)
onBeforeUnmount(() => { mounted = false })
</script>

<template>
  <div ref="root" class="agents-msg" :class="message.role" :aria-live="announce ? 'polite' : undefined" :aria-atomic="announce ? 'false' : undefined">
    <div class="agents-role">{{ message.role }}</div>

    <div v-if="message.tools.length" class="agents-toolcards">
      <div
        v-for="tool in message.tools"
        :key="tool.id"
        class="agents-toolcard"
        :class="`is-${stateFor(tool)}`"
      >
        <button
          class="k-btn k-btn--ghost agents-toolcard-head"
          type="button"
          :aria-expanded="expanded.has(tool.id)"
          @click="toggle(tool.id)"
        >
          <span class="agents-toolcard-ic">
            <LoaderCircle v-if="tool.pending" class="agents-spinner k-spin" aria-hidden="true" />
            <X v-else-if="tool.error" aria-hidden="true" />
            <Check v-else aria-hidden="true" />
          </span>
          <span class="agents-toolcard-name mono">{{ tool.name }}</span>
          <span class="agents-toolcard-meta">
            {{ tool.pending ? 'running…' : tool.error ? 'failed' : 'ok' }}<template v-if="tool.durationMS"> · {{ fmtDuration(tool.durationMS) }}</template>
          </span>
          <span class="agents-toolcard-chev" :class="{ open: expanded.has(tool.id) }">
            <ChevronRight aria-hidden="true" />
          </span>
        </button>
        <div v-if="expanded.has(tool.id)" class="agents-toolcard-body">
          <div v-if="tool.args" class="agents-kv"><span>args</span><pre>{{ prettyJSON(tool.args) }}</pre></div>
          <div v-if="tool.error" class="agents-kv"><span>error</span><pre class="err">{{ tool.error }}</pre></div>
          <div v-else-if="tool.result" class="agents-kv"><span>result</span><pre>{{ prettyJSON(tool.result) }}</pre></div>
        </div>
      </div>
    </div>

    <div
      v-if="(message.content || !message.streaming) && message.role === 'assistant'"
      class="agents-body"
      v-html="assistantHTML"
    ></div>
    <div v-else-if="message.content || !message.streaming" class="agents-body">{{ message.content }}</div>
    <div v-else class="agents-body agents-thinking"><span class="agents-dots" aria-hidden="true"></span> thinking…</div>

    <div v-if="message.approval" class="agents-approval" role="group" aria-label="Tool approval required">
      <ApprovalDisclosure :tool="message.approval.tool" :args="message.approval.args" />
      <div v-if="message.approval.resolved" class="agents-approval-done">
        {{ message.approval.resolved === 'approve' ? 'Approved — the run is resuming.' : 'Denied — the agent was told no.' }}
      </div>
      <div v-else class="agents-approval-actions">
        <button
          class="k-btn k-btn--primary"
          type="button"
          :disabled="!!approvalBusy || !approvalDisclosureAvailable(message.approval.tool, message.approval.args)"
          :aria-busy="approvalBusy ? 'true' : undefined"
          @click="emit('approval', { inboxID: message.approval!.inboxID, decision: 'approve' })"
        >
          <LoaderCircle v-if="approvalBusy === 'approve'" class="agents-spinner k-spin" aria-hidden="true" />
          <Check v-else aria-hidden="true" /> {{ approvalBusy === 'approve' ? 'Approving…' : 'Approve' }}
        </button>
        <button
          class="k-btn k-btn--ghost secondary"
          type="button"
          :disabled="!!approvalBusy"
          :aria-busy="approvalBusy ? 'true' : undefined"
          @click="emit('approval', { inboxID: message.approval!.inboxID, decision: 'deny' })"
        >
          <LoaderCircle v-if="approvalBusy === 'deny'" class="agents-spinner k-spin" aria-hidden="true" />
          <X v-else aria-hidden="true" /> {{ approvalBusy === 'deny' ? 'Denying…' : 'Deny' }}
        </button>
      </div>
    </div>

    <div v-if="message.error" class="agents-err" role="alert">{{ message.error }}</div>
    <div v-if="message.usage" class="agents-turn-usage">
      {{ fmtTokens(message.usage.inputTokens) }} in · {{ fmtTokens(message.usage.outputTokens) }} out · {{ fmtUSD(message.usage.usdMicros) }}
    </div>
  </div>
</template>
