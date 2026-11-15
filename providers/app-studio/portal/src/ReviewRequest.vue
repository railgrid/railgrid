<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, Loader2, Send, X } from 'lucide-vue-next'
import AIInterrupt from './agentkit/AIInterrupt.vue'
import AssistantExecDetails from './AssistantExecDetails.vue'
import { assistantInterruptAllowsApproval, type ProjectAssistantInterruptView } from './assistantInterrupt'
import type { ProjectAssistantFollowUpQuestion } from './types'

const props = defineProps<{
  interrupt: ProjectAssistantInterruptView
  questions?: ProjectAssistantFollowUpQuestion[]
  answers?: Record<string, string>
  busy?: boolean | 'allow' | 'deny'
  error?: string
}>()

const emit = defineEmits<{
  answer: [interrupt: ProjectAssistantInterruptView, questionID: string, value: string]
  submit: [interrupt: ProjectAssistantInterruptView]
  decide: [interrupt: ProjectAssistantInterruptView, decision: 'allow' | 'deny']
}>()

const followUp = computed(() => props.interrupt.kind === 'follow_up')
const title = computed(() => followUp.value ? 'Clarification needed' : 'Approval required')
const description = computed(() => props.interrupt.description || (followUp.value
  ? 'App Studio needs a little more information before continuing.'
  : 'Review this action before it runs.'))
const errorMessage = computed(() => props.error || (!followUp.value && props.interrupt.execDisclosureInvalid
  ? 'Command details are unavailable, so allowing this request is disabled. Deny it and retry.'
  : ''))
const exec = computed(() => props.interrupt.action?.exec || props.interrupt.exec)
// Preserve the user's chosen input mode, even if custom text happens to match
// an offered label. Restored answers without a local mode infer an option.
const customAnswers = ref(new Set<string>())
watch(() => props.interrupt.interruptId, () => { customAnswers.value = new Set() })
function isSelected(question: ProjectAssistantFollowUpQuestion, label: string): boolean {
  return !customAnswers.value.has(question.id) && props.answers?.[question.id] === label
}
function otherAnswer(question: ProjectAssistantFollowUpQuestion): string {
  const answer = props.answers?.[question.id] || ''
  return !customAnswers.value.has(question.id) && question.options?.some(option => option.label === answer) ? '' : answer
}
function chooseOption(questionID: string, value: string): void {
  const next = new Set(customAnswers.value)
  next.delete(questionID)
  customAnswers.value = next
  emit('answer', props.interrupt, questionID, value)
}
function updateOther(questionID: string, event: Event): void {
  customAnswers.value = new Set(customAnswers.value).add(questionID)
  emit('answer', props.interrupt, questionID, (event.target as HTMLInputElement).value)
}
function submit(): void {
  if (!props.busy && props.interrupt.action) emit('submit', props.interrupt)
}
function decide(decision: 'allow' | 'deny'): void {
  if (props.busy || !props.interrupt.action || (decision === 'allow' && !assistantInterruptAllowsApproval(props.interrupt))) return
  emit('decide', props.interrupt, decision)
}
</script>

<template>
  <AIInterrupt
    :kind="followUp ? 'follow-up' : 'approval'"
    :busy="Boolean(busy)"
    :invalid="!followUp && interrupt.execDisclosureInvalid"
    :title="title"
    :description="description"
    :error="errorMessage"
    :aria-label="title"
  >
    <div v-if="followUp && questions?.length" class="grid gap-3">
      <fieldset v-for="question in questions" :key="question.id" class="min-w-0 border-0 border-t border-border-subtle p-0 pt-3">
        <legend class="sr-only">{{ question.question }}</legend>
        <div v-if="question.header" class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">{{ question.header }}</div>
        <div aria-hidden="true" class="mt-1 text-[12px] font-medium leading-5 text-text-primary">{{ question.question }}</div>
        <div v-if="question.options?.length" class="mt-2 grid gap-2">
          <button
            v-for="option in question.options"
            :key="option.label"
            type="button"
            class="app-studio-touch-target rounded-md border px-3 py-2 text-left transition"
            :aria-pressed="isSelected(question, option.label)"
            :class="isSelected(question, option.label) ? 'border-accent bg-accent-subtle' : 'border-border-subtle bg-surface-raised hover:border-accent/40 hover:bg-surface-hover'"
            :disabled="Boolean(busy)"
            @click="chooseOption(question.id, option.label)"
          >
            <div class="text-[12px] font-medium text-text-primary">{{ option.label }}</div>
            <div class="mt-0.5 text-[11px] leading-4 text-text-secondary">{{ option.description }}</div>
          </button>
        </div>
        <input
          v-if="question.isOther !== false"
          class="app-studio-touch-target k-input mt-2"
          :aria-label="`${question.question} Other answer`"
          placeholder="Other..."
          :value="otherAnswer(question)"
          :disabled="Boolean(busy)"
          @input="updateOther(question.id, $event)"
        />
      </fieldset>
    </div>
    <AssistantExecDetails v-else-if="!followUp && exec" :exec="exec" variant="approval" />
    <template #actions>
      <button
        v-if="followUp"
        type="button"
        class="k-btn k-btn--primary"
        :disabled="!interrupt.action"
        :aria-disabled="Boolean(busy) || undefined"
        :aria-busy="Boolean(busy) || undefined"
        @click="submit"
      >
        <Loader2 v-if="busy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" aria-hidden="true" />
        <Send v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
        Continue
      </button>
      <template v-else>
        <button
          type="button"
          class="k-btn k-btn--primary"
          :disabled="!assistantInterruptAllowsApproval(interrupt) || busy === 'deny'"
          :aria-disabled="(busy === true || busy === 'allow') || undefined"
          :aria-busy="(busy === true || busy === 'allow') || undefined"
          :title="interrupt.execDisclosureInvalid ? 'Command details are unavailable; deny this request.' : 'Allow'"
          @click="decide('allow')"
        >
          <Loader2 v-if="busy === 'allow'" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" aria-hidden="true" />
          <Check v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          Allow
        </button>
        <button type="button" class="k-btn k-btn--ghost" :disabled="!interrupt.action || busy === 'allow'" :aria-disabled="(busy === true || busy === 'deny') || undefined" :aria-busy="(busy === true || busy === 'deny') || undefined" @click="decide('deny')">
          <Loader2 v-if="busy === 'deny'" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" aria-hidden="true" />
          <X v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          Deny
        </button>
      </template>
    </template>
  </AIInterrupt>
</template>
