<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { CircleHelp, LoaderCircle, Pencil } from 'lucide-vue-next'
import { questionPresentation } from '../question-presentation'

const props = defineProps<{
  questionId: string
  prompt?: string
  busy?: boolean
  disabled?: boolean
  resolved?: 'answered' | 'dismissed'
  dismissible?: boolean
  dismissing?: boolean
}>()
const emit = defineEmits<{ answer: [response: string]; dismiss: [] }>()
const id = useId()
const draft = ref('')
const choice = ref('')
const question = computed(() => questionPresentation(props.prompt))
const response = computed(() => choice.value || draft.value.trim())
const canSend = computed(() => !!response.value && !props.disabled && !props.busy && !props.dismissing && !props.resolved)
watch(() => props.questionId, () => { draft.value = ''; choice.value = '' })

function select(label: string): void {
  choice.value = label
  draft.value = ''
}

function send(): void {
  if (canSend.value) emit('answer', response.value)
}
</script>

<template>
  <section class="agents-question-card k-card" :aria-labelledby="`${id}-question`" :aria-busy="busy || dismissing || undefined">
    <div class="agents-question-card__heading"><CircleHelp :size="16" :stroke-width="1.75" aria-hidden="true" /> Question</div>
    <h3 :id="`${id}-question`" class="agents-question-card__prompt">{{ question.prompt }}</h3>
    <p v-if="resolved" class="agents-question-card__status" role="status">{{ resolved === 'answered' ? 'Answer sent' : 'Question dismissed' }}</p>
    <form v-else class="agents-question-card__form" @submit.prevent="send">
      <fieldset v-if="question.options.length" class="agents-question-card__choices" :disabled="disabled || busy || dismissing" :aria-labelledby="`${id}-question`">
        <label v-for="(option, index) in question.options" :key="option.label" class="agents-question-card__choice" :class="{ 'is-selected': choice === option.label }">
          <input class="agents-question-card__radio" type="radio" :name="`${id}-choice`" :checked="choice === option.label" :value="option.label" @change="select(option.label)">
          <span class="agents-question-card__number" aria-hidden="true">{{ index + 1 }}</span>
          <span><strong>{{ option.label }}</strong><span v-if="option.description !== option.label" class="agents-question-card__description"> — {{ option.description }}</span></span>
        </label>
      </fieldset>
      <div class="agents-question-card__footer">
        <div class="agents-question-card__custom">
          <Pencil :size="16" :stroke-width="1.75" aria-hidden="true" />
          <input v-model="draft" class="k-input agents-question-card__input" type="text" aria-label="Your answer" :placeholder="question.options.length ? 'Or write your own response…' : 'Write your answer…'" :disabled="disabled || busy || dismissing" maxlength="8192" @input="choice = ''">
        </div>
        <div class="agents-question-card__actions">
          <button v-if="dismissible" class="k-btn k-btn--ghost secondary agents-question-card__action" type="button" :disabled="disabled || busy || dismissing" title="Dismiss and keep the run paused" @click="emit('dismiss')">{{ dismissing ? 'Dismissing…' : 'Dismiss' }}</button>
          <button class="k-btn agents-question-card__action agents-question-card__send" :class="canSend ? 'k-btn--primary' : 'k-btn--ghost secondary'" type="submit" :disabled="!canSend" aria-label="Send answer">
            <LoaderCircle v-if="busy" :size="16" class="k-spin" aria-hidden="true" /> {{ busy ? 'Sending…' : 'Send' }}
          </button>
        </div>
      </div>
    </form>
  </section>
</template>

<style scoped>
.agents-question-card { display: grid; gap: 18px; min-width: 0; padding: clamp(16px, 2.4vw, 24px); border-radius: 12px; background: var(--color-surface-overlay); }
.agents-question-card__heading { display: flex; align-items: center; gap: 9px; color: var(--color-text-secondary); font-size: 14px; }
.agents-question-card__prompt { max-width: 72ch; margin: 0; color: var(--color-text-primary); font-size: 17px; font-weight: 550; line-height: 1.45; white-space: pre-wrap; overflow-wrap: anywhere; }
.agents-question-card__form, .agents-question-card__choices { display: grid; gap: 6px; min-width: 0; }
.agents-question-card__choices { border: 0; padding: 0; margin: 0 0 10px; }
.agents-question-card__choice { position: relative; display: flex; align-items: baseline; gap: 12px; padding: 10px 6px; border: 1px solid transparent; border-radius: 8px; font-size: 15px; line-height: 1.45; cursor: pointer; overflow-wrap: anywhere; }
.agents-question-card__choice:hover, .agents-question-card__choice:focus-within { background: var(--color-surface-raised); }
.agents-question-card__choice:focus-within { outline: 2px solid var(--color-accent); outline-offset: 1px; }
.agents-question-card__choice.is-selected { border-color: var(--color-accent); background: var(--color-accent-subtle); }
.agents-question-card__choices:disabled .agents-question-card__choice { cursor: default; }
.agents-question-card__radio { position: absolute; width: 1px; height: 1px; opacity: 0; }
.agents-question-card__number { display: grid; place-items: center; flex: 0 0 26px; height: 26px; border: 1px solid var(--color-border-default); border-radius: 6px; background: var(--color-surface-raised); color: var(--color-text-secondary); font-size: 13px; font-variant-numeric: tabular-nums; }
.agents-question-card__description, .agents-question-card__status { color: var(--color-text-secondary); }
.agents-question-card__status { margin: 0; }
.agents-question-card__footer { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 10px 12px; }
.agents-question-card__custom { display: grid; grid-template-columns: auto minmax(0, 1fr); flex: 1 1 210px; align-items: center; gap: 10px; min-width: 0; padding-inline: 6px; border-radius: 8px; color: var(--color-text-secondary); }
.agents-question-card__custom:focus-within { outline: 2px solid var(--color-accent); outline-offset: 2px; }
.agents-question-card__input, .agents-question-card__input:focus { width: 100%; min-width: 0; padding-inline: 0; border-color: transparent; background: transparent; box-shadow: none; outline: none; font-size: 14px; }
.agents-question-card__actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
.agents-question-card__action { min-height: 36px; padding-inline: 15px; border-radius: 12px; }
.agents-question-card__send { min-width: 68px; }
.agents-question-card__send:disabled { border-color: var(--color-border-default); background: var(--color-surface-raised); color: var(--color-text-secondary); opacity: 1; }
</style>
