<script setup>
import { reactive } from 'vue'
import AIActivityDisclosure from '../../provider-sdk/agentkit-vue/AIActivityDisclosure.vue'

const expanded = reactive({ busy: false, error: false, attention: false, mixed: false, complete: false })
const examples = [
  { key: 'busy', label: 'Busy', props: { busy: true } },
  { key: 'error', label: 'Error', props: { error: true } },
  { key: 'attention', label: 'Attention', props: { attention: true } },
  { key: 'mixed', label: 'In progress with an error', props: { busy: true, error: true } },
  { key: 'complete', label: 'Completed', props: {} },
]
</script>

<template>
  <section class="spec-section spec-stack" aria-labelledby="activity-disclosure-heading">
    <div>
      <h3 id="activity-disclosure-heading">Collapsed activity states</h3>
      <p class="spec-note">Synthetic examples using the shared disclosure. Expand any example to inspect its panel; no actions run.</p>
    </div>
    <div v-for="example in examples" :key="example.key">
      <p class="spec-note">{{ example.label }}</p>
      <AIActivityDisclosure
        :panel-id="`review-activity-${example.key}`"
        :count="2"
        :expanded="expanded[example.key]"
        v-bind="example.props"
        @toggle="expanded[example.key] = !expanded[example.key]"
      >
        <p class="spec-note">Example activity details.</p>
      </AIActivityDisclosure>
    </div>
  </section>
</template>
