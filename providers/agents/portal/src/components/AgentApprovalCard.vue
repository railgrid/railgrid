<script setup lang="ts">
import { computed, useId } from 'vue'
import { LoaderCircle, ShieldCheck } from 'lucide-vue-next'
import ApprovalDisclosure from './ApprovalDisclosure.vue'
import { approvalDisclosure } from '../approval-disclosure'

const props = withDefaults(defineProps<{
  tool?: unknown
  args?: unknown
  busy?: 'approve' | 'deny'
  disabled?: boolean
  resolved?: 'approve' | 'deny'
  approveLabel?: string
  approveBusyLabel?: string
  headingLevel?: 3 | 4
}>(), { approveLabel: 'Approve', approveBusyLabel: 'Approving…', headingLevel: 3 })
const emit = defineEmits<{ decide: [decision: 'approve' | 'deny'] }>()
const id = useId()
const disclosure = computed(() => approvalDisclosure(props.tool, props.args))
const request = computed<Record<string, unknown>>(() => disclosure.value.valid ? JSON.parse(props.args as string) : {})
const command = computed(() => {
  const value = request.value.command
  if (typeof value === 'string') return value
  if (Array.isArray(value) && value.length && value.every(arg => typeof arg === 'string')) return value.map(arg => JSON.stringify(arg)).join(' ')
  return ''
})
const directory = computed(() => typeof request.value.cwd === 'string' ? request.value.cwd : '')
const reason = computed(() => typeof request.value.reason === 'string' ? request.value.reason : '')
function decide(decision: 'approve' | 'deny'): void {
  if (props.busy || props.disabled || props.resolved || (decision === 'approve' && !disclosure.value.valid)) return
  emit('decide', decision)
}
</script>

<template>
  <section class="agents-approval agents-permission-card k-card" :aria-labelledby="`${id}-heading`" :aria-busy="!!busy || undefined">
    <div :id="`${id}-heading`" class="agents-permission-card__heading">
      <ShieldCheck :size="16" :stroke-width="1.75" aria-hidden="true" />
      Permission required
      <span v-if="!resolved" class="muted">· Waiting for your approval</span>
    </div>
    <component :is="`h${headingLevel}`" class="agents-permission-card__prompt">Allow this action?</component>
    <template v-if="command">
      <div class="agents-permission-card__request">
        <span class="agents-permission-card__label">{{ disclosure.tool }} · Command</span>
        <pre class="agents-permission-card__command">{{ command }}</pre>
        <p v-if="directory" class="agents-permission-card__directory">Directory: <code>{{ directory }}</code></p>
        <p v-if="reason" class="agents-permission-card__reason">{{ reason }}</p>
      </div>
      <details class="agents-permission-card__details">
        <summary>Full request</summary>
        <ApprovalDisclosure :tool="tool" :args="args" details-only />
      </details>
    </template>
    <ApprovalDisclosure v-else :tool="tool" :args="args" details-only />
    <p v-if="resolved" class="agents-approval-done" role="status">{{ resolved === 'approve' ? 'Approved — the run is resuming.' : 'Denied — the agent was told no.' }}</p>
    <div v-else class="agents-approval-actions agents-permission-card__actions" role="group" aria-label="Permission decision">
      <button class="k-btn k-btn--ghost secondary" type="button" :disabled="disabled || !!busy" :aria-busy="busy === 'deny' || undefined" @click="decide('deny')">
        <LoaderCircle v-if="busy === 'deny'" :size="16" class="k-spin" aria-hidden="true" />{{ busy === 'deny' ? 'Denying…' : 'Deny' }}
      </button>
      <button class="k-btn k-btn--primary" type="button" :disabled="disabled || !!busy || !disclosure.valid" :aria-busy="busy === 'approve' || undefined" @click="decide('approve')">
        <LoaderCircle v-if="busy === 'approve'" :size="16" class="k-spin" aria-hidden="true" />{{ busy === 'approve' ? approveBusyLabel : approveLabel }}
      </button>
    </div>
  </section>
</template>

<style scoped>
.agents-permission-card { display: grid; gap: 12px; min-width: 0; padding: 14px 16px; background: var(--color-surface-overlay); }
.agents-permission-card__heading { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; color: var(--color-text-secondary); font-size: 13px; }
.agents-permission-card__prompt { max-width: 72ch; margin: 0; color: var(--color-text-primary); font-size: 16px; font-weight: 500; line-height: 1.4; }
.agents-permission-card__request { display: grid; gap: 6px; min-width: 0; }
.agents-permission-card__label, .agents-permission-card__details { color: var(--color-text-secondary); font-size: 13px; }
.agents-permission-card__command { max-block-size: 20dvh; overflow: auto; margin: 0; padding: 10px 12px; border-radius: 8px; background: var(--color-surface-raised); color: var(--color-text-primary); white-space: pre-wrap; overflow-wrap: anywhere; font-size: 13px; }
.agents-permission-card__directory, .agents-permission-card__reason { margin: 0; font-size: 13px; overflow-wrap: anywhere; }
.agents-permission-card__details { min-width: 0; }
.agents-permission-card__details summary { width: fit-content; cursor: pointer; }
.agents-permission-card :deep(.agents-approval-args) { max-block-size: 24dvh; overflow: auto; overflow-wrap: anywhere; margin-block-end: 0; }
.agents-permission-card__actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
.agents-permission-card__actions button { min-height: 34px; padding-inline: 12px; }
@media (pointer: coarse) { .agents-permission-card__actions button { min-height: 44px; } }
</style>
