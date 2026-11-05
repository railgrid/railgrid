<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import { ChevronLeft, ChevronRight, CircleHelp, LoaderCircle, Pencil } from 'lucide-vue-next'
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
const activeIndex = ref(0)
const questionHeading = ref<HTMLElement>()
const activeQuestion = computed(() => props.questions?.[activeIndex.value])
const lastQuestion = computed(() => activeIndex.value === (props.questions?.length ?? 0) - 1)
const firstUnanswered = computed(() => props.questions?.findIndex(entry => !hasReply(entry.id)) ?? -1)

function hasReply(questionID: string): boolean {
  const reply = replies.value[questionID]
  return reply === null || (typeof reply === 'string' && !!reply.trim())
}

function goTo(index: number): void {
  if (!props.questions?.[index] || index === activeIndex.value) return
  activeIndex.value = index
  void nextTick(() => questionHeading.value?.focus())
}

function setReply(questionID: string, value: string | null, custom = false): void {
  customDrafts.value = { ...customDrafts.value, [questionID]: custom ? value ?? '' : '' }
  replies.value = { ...replies.value, [questionID]: value }
}
function advanceGroup(): void {
  const entry = activeQuestion.value
  if (!entry || !hasReply(entry.id) || props.disabled || props.busy) return
  if (!lastQuestion.value) goTo(activeIndex.value + 1)
  else if (firstUnanswered.value !== -1) goTo(firstUnanswered.value)
  else emit('answer', replies.value)
}

function skipQuestion(): void {
  const entry = activeQuestion.value
  if (!entry) return
  setReply(entry.id, null)
  if (!lastQuestion.value) goTo(activeIndex.value + 1)
}
const question = computed(() => questionPresentation(props.prompt))
const response = computed(() => choice.value || draft.value.trim())
const canSend = computed(() => !!response.value && !props.disabled && !props.busy && !props.resolved)
watch(() => props.questionId, () => { draft.value = ''; choice.value = ''; replies.value = {}; customDrafts.value = {}; activeIndex.value = 0 })

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
      Question
      <span v-if="!resolved" class="muted">· {{ blocking === false ? 'Reply when ready' : 'Waiting for your answers' }}</span>
      <div v-if="questions && questions.length > 1 && !resolved" class="agents-question-card__navigation" role="group" aria-label="Question navigation">
        <button class="k-btn k-btn--ghost" type="button" aria-label="Previous question" title="Previous question"
          :disabled="activeIndex === 0 || disabled || busy" @click="goTo(activeIndex - 1)"><ChevronLeft :size="16" aria-hidden="true" /></button>
        <span aria-live="polite">{{ activeIndex + 1 }} of {{ questions.length }}</span>
        <button class="k-btn k-btn--ghost" type="button" aria-label="Next question" title="Next question"
          :disabled="lastQuestion || disabled || busy" @click="goTo(activeIndex + 1)"><ChevronRight :size="16" aria-hidden="true" /></button>
      </div>
    </div>
    <template v-if="questions?.length">
      <p v-if="resolved" role="status">{{ resolved === 'answered' ? 'Answers sent' : 'Questions dismissed' }}</p>
      <form v-else-if="activeQuestion" class="agents-question-card__form" @submit.prevent="advanceGroup">
        <div class="agents-question-card__questions">
          <fieldset :key="activeQuestion.id" class="agents-question-card__group"
            :disabled="disabled || busy">
            <legend ref="questionHeading" tabindex="-1">{{ activeQuestion.prompt }}</legend>
            <label v-for="option in activeQuestion.options" :key="option.label" class="agents-question-card__choice"
              :class="{ 'is-selected': !customDrafts[activeQuestion.id] && replies[activeQuestion.id] === option.label }">
              <input type="radio" :name="`${id}-${activeIndex}`" :checked="!customDrafts[activeQuestion.id] && replies[activeQuestion.id] === option.label"
                @change="setReply(activeQuestion.id, option.label)">
              <span><strong>{{ option.label }}</strong><span v-if="option.description && option.description !== option.label"> — {{ option.description }}</span></span>
            </label>
            <input class="k-input agents-question-card__input" type="text" :aria-label="`Your answer to ${activeQuestion.prompt}`"
              :value="customDrafts[activeQuestion.id] ?? ''"
              :placeholder="activeQuestion.options?.length ? 'Or write your own response…' : 'Write your answer…'" maxlength="8192"
              @input="setReply(activeQuestion.id, ($event.target as HTMLInputElement).value, true)">
          </fieldset>
        </div>
        <div class="agents-question-card__actions">
          <span v-if="replies[activeQuestion.id] === null" class="agents-question-card__skipped" role="status">Skipped</span>
          <button class="k-btn k-btn--ghost secondary" type="button" :disabled="disabled || busy" @click="skipQuestion">Skip</button>
          <button class="k-btn k-btn--primary" type="submit" :disabled="!hasReply(activeQuestion.id) || disabled || busy">
            {{ busy ? 'Sending…' : !lastQuestion ? 'Next' : firstUnanswered !== -1 ? 'Review unanswered' : questions.length === 1 ? 'Send answer' : 'Send answers' }}
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
.agents-question-card__navigation { display: flex; align-items: center; gap: 4px; margin-inline-start: auto; white-space: nowrap; font-variant-numeric: tabular-nums; }
.agents-question-card__navigation .k-btn { display: grid; place-items: center; min-width: 32px; min-height: 32px; padding: 0; }
.agents-question-card__skipped { align-self: center; color: var(--color-text-secondary); font-size: 13px; }
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
.agents-question-card__heading { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; color: var(--color-text-secondary); font-size: 13px; }
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
