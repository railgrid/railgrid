<script setup>
import { onUnmounted, ref } from 'vue'
import AgentTemplateListInput from '../../providers/agents/portal/src/components/AgentTemplateListInput.vue'
import agentsStyles from '../../providers/agents/portal/src/style.css?inline'

const style = document.createElement('style')
style.textContent = agentsStyles
document.head.appendChild(style)
onUnmounted(() => style.remove())
const topics = ref('platform reliability\nrelease notes')
const invalid = ref(false)
const disabled = ref(false)
function update(value) {
  topics.value = value
  invalid.value = false
}
function validate() { invalid.value = !topics.value.trim() }
</script>

<template>
  <component :is="'railgrid-provider-agents'" class="token-list-review">
    <div class="spec-stack">
      <p class="spec-note">Add an item with Enter or paste one per line. Remove both items and check the required state; changes stay in this specimen.</p>
      <div class="agents-template-parameter">
        <label id="token-list-review-label" for="token-list-review">Topics to follow *</label>
        <AgentTemplateListInput id="token-list-review" :model-value="topics" placeholder="Add a topic…" labelledby="token-list-review-label" :describedby="invalid ? 'token-list-review-hint token-list-review-error' : 'token-list-review-hint'" :disabled="disabled" :invalid="invalid" :required="true" @update:model-value="update" />
        <span id="token-list-review-hint" class="agents-hint">Press Enter to add an item, or paste one per line.</span>
        <span v-if="invalid" id="token-list-review-error" class="agents-fielderr" role="alert">Add at least one topic.</span>
      </div>
      <div class="spec-row"><button class="k-btn k-btn--ghost" type="button" :disabled="disabled" @click="validate">Check required</button><label><input v-model="disabled" type="checkbox" /> Disable field</label></div>
    </div>
  </component>
</template>

<style scoped>
.token-list-review { height: auto; }
</style>
