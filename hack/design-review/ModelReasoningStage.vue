<script setup>
import { computed, ref } from 'vue'
import AIModelReasoningPicker from '../../provider-sdk/agentkit-vue/AIModelReasoningPicker.vue'

const models = [
  { id: 'scout', name: 'Scout', model: 'scout-v2', default: true },
  { id: 'builder', name: 'Builder', model: 'builder-v1' },
]
const options = [{ value: 'low', label: 'Low' }, { value: 'medium', label: 'Medium' }, { value: 'high', label: 'High' }]
const selected = ref('scout')
const effort = ref('')
const availableOptions = computed(() => selected.value === 'scout' ? options : [])
const invalidEffort = ref('legacy')
const saveError = ref('Could not save this preference.')
</script>

<template>
  <div class="spec-stack">
    <p class="spec-note">These examples update local sample data only; no model is contacted and no preference is saved.</p>
    <p class="spec-note"><a href="http://127.0.0.1:5198/?provider=app&amp;scenario=app-picker&amp;theme=light&amp;route=rollout-console" target="_blank" rel="noopener">Open App Studio project fixture</a> to review the picker in its composer. The project and models are sample data; requests are local to the fixture.</p>
    <section class="spec-section spec-stack" aria-labelledby="reasoning-normal">
      <div><h3 id="reasoning-normal">Model and effort</h3><p class="spec-note">Open Scout to choose a reasoning effort. Choose Builder to see a model without effort options.</p></div>
      <AIModelReasoningPicker :models="models" :selected-i-d="selected" :effort="effort" :effort-options="availableOptions" default-effort-label="Medium" @select="selected = $event; effort = ''" @update:effort="effort = $event" />
      <p class="spec-note" role="status">{{ selected === 'scout' ? 'Scout' : 'Builder' }} · {{ effort || 'model default' }}</p>
    </section>
    <section class="spec-section spec-stack" aria-labelledby="reasoning-recovery">
      <div><h3 id="reasoning-recovery">Unsupported saved effort</h3><p class="spec-note">Reset the stale value to the model default.</p></div>
      <AIModelReasoningPicker :models="models" selected-i-d="scout" :effort="invalidEffort" :effort-options="options" :reset-required="!!invalidEffort" @update:effort="invalidEffort = $event" />
    </section>
    <section class="spec-section spec-stack" aria-labelledby="reasoning-save-error">
      <div><h3 id="reasoning-save-error">Save failure</h3><p class="spec-note">Retry clears this simulated error.</p></div>
      <AIModelReasoningPicker :models="models" selected-i-d="scout" effort="medium" :effort-options="options" :save-error="saveError" @retry="saveError = ''" />
    </section>
  </div>
</template>
