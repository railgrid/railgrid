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
  inspection?: boolean
}>()

const emit = defineEmits<{
  recovery: [target: FailureRecoveryTarget]
}>()

const safeDiagnostic = computed(() => sanitizeTechnicalDiagnostic(props.diagnostic))
const presentation = computed(() => runFailurePresentation(props.phase, safeDiagnostic.value))
</script>

<template>
  <section
    class="agents-run-state"
    :class="[inspection ? 'agents-run-state--inspection' : 'agents-run-state--inline', `agents-run-state--${phase}`]"
    :role="phase === 'failed' ? 'alert' : 'status'"
  >
    <TriangleAlert class="agents-run-state__icon" :stroke-width="1.9" aria-hidden="true" />
    <div class="agents-run-state__body">
      <div class="agents-run-state__copy">
        <h3>{{ presentation.title }}</h3>
        <span v-if="!inspection" class="agents-run-state__separator" aria-hidden="true"> · </span>
        <p>{{ presentation.summary }}</p>
      </div>
      <button
        v-if="inspection && presentation.recovery"
        class="k-btn k-btn--primary agents-run-state__recovery"
        type="button"
        @click="emit('recovery', presentation.recovery.target)"
      >
        {{ presentation.recovery.label }}
      </button>
      <TechnicalDetails class="agents-run-state__details" :diagnostic="safeDiagnostic" :inline="!inspection" compact copyable>
        <template v-if="!inspection && ($slots.actions || presentation.recovery)" #actions>
          <slot name="actions" />
          <button
            v-if="presentation.recovery"
            class="k-ai-message-metadata__button agents-run-state__recovery agents-run-state__action"
            type="button"
            @click="emit('recovery', presentation.recovery.target)"
          >
            {{ presentation.recovery.label }}
          </button>
        </template>
      </TechnicalDetails>
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
  font-size: 14px;
  font-weight: 650;
  line-height: 1.35;
}

.agents-run-state p {
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

.agents-run-state--inline {
  grid-template-columns: 16px minmax(0, 1fr);
  gap: 8px;
}

.agents-run-state--inline .agents-run-state__icon {
  width: 16px;
  height: 16px;
  margin-block-start: 2px;
}

.agents-run-state--inline .agents-run-state__copy {
  color: var(--color-text-secondary);
  font-size: 13px;
  line-height: 1.55;
}

.agents-run-state--inline h3,
.agents-run-state--inline p { display: inline; }
.agents-run-state--inline p { margin: 0; }
.agents-run-state__separator { margin-inline: 2px; }
.agents-run-state--inline .agents-run-state__details { margin-block-start: 2px; }

.agents-run-state__action {
  min-height: 28px;
  margin: 0;
  padding-inline: 0;
  color: var(--color-accent);
}

@media (hover: none), (any-pointer: coarse) {
  .agents-run-state__action { min-height: 44px; }
}
</style>
