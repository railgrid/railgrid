<script setup lang="ts">
import { TriangleAlert } from 'lucide-vue-next'
import { computed, ref, useId } from 'vue'
import AIActionRow from '../agentkit/AIActionRow.vue'
import TechnicalDetails from './TechnicalDetails.vue'
import {
  runFailurePresentation,
  sanitizeTechnicalDiagnostic,
  type FailureRecoveryTarget,
  type RunFailurePhase,
} from '../failure-presentation'

const props = defineProps<{
  phase: RunFailurePhase
  diagnostic?: string
  inspection?: boolean
}>()

const emit = defineEmits<{
  recovery: [target: FailureRecoveryTarget]
}>()

const expanded = ref(false)
const detailsId = useId()
const safeDiagnostic = computed(() => sanitizeTechnicalDiagnostic(props.diagnostic))
const presentation = computed(() => runFailurePresentation(props.phase, safeDiagnostic.value))
</script>

<template>
  <section v-if="inspection" class="agents-run-state agents-run-state--inspection" :class="`agents-run-state--${phase}`" :role="phase === 'failed' ? 'alert' : 'status'">
    <TriangleAlert class="agents-run-state__icon" :stroke-width="1.9" aria-hidden="true" />
    <div class="agents-run-state__body">
      <h3>{{ presentation.title }}</h3>
      <p>{{ presentation.summary }}</p>
      <button
        v-if="presentation.recovery"
        class="k-btn k-btn--primary agents-run-state__recovery"
        type="button"
        @click="emit('recovery', presentation.recovery.target)"
      >
        {{ presentation.recovery.label }}
      </button>
      <TechnicalDetails class="agents-run-state__details" :diagnostic="safeDiagnostic" compact copyable />
    </div>
  </section>

  <section v-else class="agents-run-state agents-run-state--inline" :class="`agents-run-state--${phase}`">
    <span class="sr-only" :role="phase === 'failed' ? 'alert' : 'status'">{{ presentation.title }}</span>
    <div class="agents-run-state__line">
      <AIActionRow
        :title="presentation.title"
        :error="phase === 'failed'"
        :canceled="phase === 'aborted'"
        :expanded="expanded"
        :details-id="detailsId"
        expandable
        @toggle="expanded = !expanded"
      />
      <slot name="actions" />
    </div>
    <div v-if="expanded" :id="detailsId" class="k-ai-action-row__details agents-run-state__panel">
      <p class="agents-run-state__summary">{{ presentation.summary }}</p>
      <button
        v-if="presentation.recovery"
        class="k-btn k-btn--ghost agents-run-state__recovery"
        type="button"
        @click="emit('recovery', presentation.recovery.target)"
      >
        {{ presentation.recovery.label }}
      </button>
      <TechnicalDetails :diagnostic="safeDiagnostic" embedded compact copyable />
    </div>
  </section>
</template>

<style scoped>
.agents-run-state--inspection {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  align-items: start;
  gap: 10px;
  min-width: 0;
}

.agents-run-state__icon {
  width: 18px;
  height: 18px;
  margin-block-start: 1px;
  color: var(--color-text-muted, #8587a1);
}

.agents-run-state--failed .agents-run-state__icon {
  color: var(--color-danger, #ff5d5d);
}

.agents-run-state__body { min-width: 0; }
.agents-run-state--inspection h3 {
  margin: 0;
  color: var(--color-text-primary, #e9e9f2);
  font-size: 14px;
  font-weight: 650;
  line-height: 1.35;
}

.agents-run-state--inspection p {
  margin: 5px 0 0;
  color: var(--color-text-secondary, #8a8ca6);
  font-size: 13px;
  line-height: 1.45;
  overflow-wrap: anywhere;
}

.agents-run-state__recovery {
  margin-block-start: 14px;
}

.agents-run-state__details { min-width: 0; margin-block-start: 12px; }

.agents-run-state__line {
  display: flex;
  align-items: center;
}

.agents-run-state__panel {
  display: grid;
  gap: 8px;
}

.agents-run-state__summary {
  margin: 0;
  color: var(--color-text-secondary);
  font-size: 12px;
  line-height: 1.55;
}

.agents-run-state--inline .agents-run-state__recovery {
  justify-self: start;
  margin: 0;
}

</style>
