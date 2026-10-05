<script setup lang="ts">
import { TriangleAlert } from 'lucide-vue-next'
import { computed } from 'vue'
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
}>()

const emit = defineEmits<{
  recovery: [target: FailureRecoveryTarget]
}>()

const safeDiagnostic = computed(() => sanitizeTechnicalDiagnostic(props.diagnostic))
const presentation = computed(() => runFailurePresentation(props.phase, safeDiagnostic.value))
</script>

<template>
  <section
    class="k-inline-notification agents-run-state"
    :class="[`agents-run-state--${phase}`, phase === 'failed' ? 'k-inline-notification--error' : 'k-inline-notification--info']"
    :role="phase === 'failed' ? 'alert' : 'status'"
  >
    <TriangleAlert class="agents-run-state__icon" :stroke-width="1.9" aria-hidden="true" />
    <div class="agents-run-state__body">
      <h3>{{ presentation.title }}</h3>
      <p>{{ presentation.summary }}</p>
      <button
        v-if="presentation.recovery"
        class="k-btn k-btn--ghost secondary agents-run-state__recovery"
        type="button"
        @click="emit('recovery', presentation.recovery.target)"
      >
        {{ presentation.recovery.label }}
      </button>
      <TechnicalDetails class="agents-run-state__details" :diagnostic="safeDiagnostic" />
    </div>
  </section>
</template>

<style scoped>
.agents-run-state {
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
.agents-run-state h3 {
  margin: 0;
  color: var(--color-text-primary, #e9e9f2);
  font-size: 13px;
  font-weight: 650;
  line-height: 1.35;
}

.agents-run-state p {
  margin: 3px 0 0;
  color: var(--color-text-secondary, #8a8ca6);
  font-size: 12px;
  line-height: 1.45;
  overflow-wrap: anywhere;
}

.agents-run-state__recovery {
  min-height: 44px;
  margin-block-start: 7px;
}

.agents-run-state__details { min-width: 0; margin-block-start: 5px; }
</style>
