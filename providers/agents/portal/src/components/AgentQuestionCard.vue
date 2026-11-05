<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { CircleHelp, LoaderCircle, Pencil } from 'lucide-vue-next'
import type { AgentQuestion, QuestionAnswers, QuestionReply } from '../types'
import { questionPresentation, skippedQuestionResponse } from '../question-presentation'

const props = withDefaults(defineProps<{
  questionId: string
  prompt?: string
  questions?: AgentQuestion[]
  blocking?: boolean
  busy?: boolean
  disabled?: boolean
  resolved?: 'answered' | 'dismissed'
}>(), { blocking: true })
const emit = defineEmits<{ answer: [response: QuestionReply] }>()
const id = useId()
const draft = ref('')
const choice = ref('')
const replies = ref<QuestionAnswers>({})
const customDrafts = ref<Record<string, string>>({})
const groupReady = computed(() => props.questions?.every(question =>
  Object.hasOwn(replies.value, question.id) &&
  (replies.value[question.id] === null || !!replies.value[question.id]?.trim()),
))
function setReply(questionID: string, value: string | null, custom = false): void {
  customDrafts.value = { ...customDrafts.value, [questionID]: custom ? value ?? '' : '' }
  replies.value = { ...replies.value, [questionID]: value }
}
function sendGroup(): void {
  if (groupReady.value && !props.disabled && !props.busy) emit('answer', replies.value)
}
const question = computed(() => questionPresentation(props.prompt))
const response = computed(() => choice.value || draft.value.trim())
const canSend = computed(() => !!response.value && !props.disabled && !props.busy && !props.resolved)
watch(() => props.questionId, () => { draft.value = ''; choice.value = ''; replies.value = {}; customDrafts.value = {} })

function select(label: string): void {
  choice.value = label
  draft.value = ''
}

function send(): void {
  if (canSend.value) emit('answer', response.value)
}
</script>

<template>
  <section class="agents-question-card k-card" :aria-labelledby="questions?.length ? `${id}-heading` : `${id}-question`" :aria-busy="busy || undefined">
    <div :id="`${id}-heading`" class="agents-question-card__heading"><CircleHelp :size="16" :stroke-width="1.75" aria-hidden="true" />
      {{ questions?.length ? `${questions.length} ${questions.length === 1 ? 'question' : 'questions'}` : 'Question' }}
      <span v-if="!resolved" class="muted">· {{ blocking === false ? 'Reply when ready' : 'Waiting for your answers' }}</span>
    </div>
    <template v-if="questions?.length">
      <p v-if="resolved" role="status">{{ resolved === 'answered' ? 'Answers sent' : 'Questions dismissed' }}</p>
      <form v-else class="agents-question-card__form" @submit.prevent="sendGroup">
        <div class="agents-question-card__questions">
          <fieldset v-for="(entry, index) in questions" :key="entry.id" class="agents-question-card__group"
            :disabled="disabled || busy">
            <legend>{{ entry.prompt }}</legend>
            <label v-for="option in entry.options" :key="option.label" class="agents-question-card__choice"
              :class="{ 'is-selected': !customDrafts[entry.id] && replies[entry.id] === option.label }">
              <input type="radio" :name="`${id}-${index}`" :checked="!customDrafts[entry.id] && replies[entry.id] === option.label"
                @change="setReply(entry.id, option.label)">
              <span><strong>{{ option.label }}</strong><span v-if="option.description && option.description !== option.label"> — {{ option.description }}</span></span>
            </label>
            <label class="agents-question-card__choice">
              <input type="radio" :name="`${id}-${index}`" :checked="replies[entry.id] === null"
                @change="setReply(entry.id, null)"> Skip this question
            </label>
            <input class="k-input agents-question-card__input" type="text" :aria-label="`Your answer to ${entry.prompt}`"
              :value="customDrafts[entry.id] ?? ''"
              :placeholder="entry.options?.length ? 'Or write your own response…' : 'Write your answer…'" maxlength="8192"
              @input="setReply(entry.id, ($event.target as HTMLInputElement).value, true)">
          </fieldset>
        </div>
        <div class="agents-question-card__actions">
          <button class="k-btn k-btn--primary" type="submit" :disabled="!groupReady || disabled || busy">
            {{ busy ? 'Sending…' : questions.length === 1 ? 'Send answer' : 'Send answers' }}
          </button>
        </div>
      </form>
    </template>
    <template v-else>
    <h3 :id="`${id}-question`" class="agents-question-card__prompt">{{ question.prompt }}</h3>
    <p v-if="resolved" class="agents-question-card__status" role="status">{{ resolved === 'answered' ? 'Answer sent' : 'Question dismissed' }}</p>
    <form v-else class="agents-question-card__form" @submit.prevent="send">
      <fieldset v-if="question.options.length" class="agents-question-card__choices" :disabled="disabled || busy" :aria-labelledby="`${id}-question`">
        <label v-for="(option, index) in question.options" :key="option.label" class="agents-question-card__choice" :class="{ 'is-selected': choice === option.label }">
          <input class="agents-question-card__radio" type="radio" :name="`${id}-choice`" :checked="choice === option.label" :value="option.label" @change="select(option.label)">
          <span class="agents-question-card__number" aria-hidden="true">{{ index + 1 }}</span>
          <span><strong>{{ option.label }}</strong><span v-if="option.description !== option.label" class="agents-question-card__description"> — {{ option.description }}</span></span>
        </label>
      </fieldset>
      <div class="agents-question-card__footer">
        <div class="agents-question-card__custom">
          <Pencil :size="16" :stroke-width="1.75" aria-hidden="true" />
          <input v-model="draft" class="k-input agents-question-card__input" type="text" aria-label="Your answer" :placeholder="question.options.length ? 'Or write your own response…' : 'Write your answer…'" :disabled="disabled || busy" maxlength="8192" @input="choice = ''">
        </div>
        <div class="agents-question-card__actions">
          <button class="k-btn k-btn--ghost secondary agents-question-card__action" type="button" :disabled="disabled || busy" title="Resume without an answer" @click="emit('answer', skippedQuestionResponse)">Skip</button>
          <button class="k-btn k-btn--primary agents-question-card__action agents-question-card__send" type="submit" :disabled="!canSend" aria-label="Send answer">
            <LoaderCircle v-if="busy" :size="16" class="k-spin" aria-hidden="true" /> {{ busy ? 'Sending…' : 'Send' }}
          </button>
        </div>
      </div>
    </form>
    </template>
  </section>
</template>

<style scoped>
.agents-question-card__questions { display: grid; gap: 8px; min-width: 0; }
.agents-question-dock .agents-question-card__questions { max-block-size: 42dvh; overflow: auto; padding: 3px; }
.agents-question-card__group {
  display: grid;
  gap: 0.5rem;
  min-width: 0;
  margin: 0;
  padding: 0.75rem 0 0;
  border: 0;
}
.agents-question-card__group legend { font-weight: 600; overflow-wrap: anywhere; }
.agents-question-card__group .agents-question-card__input:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: -2px;
}

.agents-question-card {
  display: grid;
  gap: 12px;
  min-width: 0;
  padding: 14px 16px;
  background: var(--color-surface-overlay);
}
.agents-question-card__heading { display: flex; align-items: center; gap: 8px; color: var(--color-text-secondary); font-size: 13px; }
.agents-question-card__prompt { max-width: 72ch; margin: 0; color: var(--color-text-primary); font-size: 16px; font-weight: 500; line-height: 1.4; white-space: pre-wrap; overflow-wrap: anywhere; }
.agents-question-card__form, .agents-question-card__choices { display: grid; gap: 4px; min-width: 0; }
.agents-question-card__choices { border: 0; padding: 0; margin: 0; }
.agents-question-card label.agents-question-card__choice { position: relative; display: grid; grid-template-columns: 24px minmax(0, 1fr); align-items: center; gap: 10px; box-sizing: border-box; min-height: 34px; padding: 4px 0; border: 1px solid transparent; border-radius: 8px; font-size: 14px; line-height: 1.4; cursor: pointer; overflow-wrap: anywhere; }
.agents-question-card__choice:hover, .agents-question-card__choice:focus-within { background: var(--color-surface-raised); }
.agents-question-card__choice:focus-within { outline: 2px solid var(--color-accent); outline-offset: 1px; }
.agents-question-card__choice.is-selected { border-color: var(--color-accent); background: var(--color-accent-subtle); }
.agents-question-card__choices:disabled .agents-question-card__choice { cursor: default; }
.agents-question-card__radio { position: absolute; width: 1px; height: 1px; opacity: 0; }
.agents-question-card__number { display: grid; place-items: center; box-sizing: border-box; width: 24px; height: 24px; border: 1px solid var(--color-border-default); border-radius: 6px; background: var(--color-surface-raised); color: var(--color-text-secondary); font-size: 13px; font-variant-numeric: tabular-nums; }
.agents-question-card__choice strong { font-weight: 550; }
.agents-question-card__description, .agents-question-card__status { color: var(--color-text-secondary); }
.agents-question-card__status { margin: 0; }
.agents-question-card__footer { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 4px 10px; }
.agents-question-card__custom { display: grid; grid-template-columns: auto minmax(0, 1fr); flex: 1 1 175px; align-items: center; gap: 8px; min-width: 0; padding-inline: 2px; border-radius: 8px; color: var(--color-text-secondary); }
.agents-question-card__custom:focus-within { outline: 2px solid var(--color-accent); outline-offset: 2px; }
.agents-question-card__input, .agents-question-card__input:focus { width: 100%; min-width: 0; padding-inline: 0; border-color: transparent; background: transparent; box-shadow: none; outline: none; font-size: 14px; }
.agents-question-card__actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
.agents-question-card__action { min-height: 34px; padding-inline: 12px; }
.agents-question-card__send { min-width: 60px; }
.agents-question-card__send:disabled {
  border-color: transparent;
  background: color-mix(in srgb, var(--color-text-primary) 46%, var(--color-surface-raised));
  color: var(--color-surface-overlay);
  box-shadow: none;
  opacity: 1;
}
@media (pointer: coarse) {
  .agents-question-card label.agents-question-card__choice,
  .agents-question-card__action { min-height: 44px; }
}
</style>
