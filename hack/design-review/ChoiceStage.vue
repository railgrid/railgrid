<script setup>
import { ref } from 'vue'

defineProps({ radio: Boolean })
const radioChoice = ref('agent')
const requiredRadio = ref('')
const radioSaved = ref(false)
const checkboxOptions = ['Charts', 'Deployments']
const checkboxChoices = ref(['Charts'])

function toggleAllCheckboxes(event) {
  checkboxChoices.value = event.target.checked ? [...checkboxOptions] : []
}
</script>

<template>
  <div class="spec-stack">
    <section v-if="radio" class="spec-section spec-stack" aria-labelledby="review-radio-heading">
      <div><h3 id="review-radio-heading">Radio states</h3><p class="spec-note">Anatomy: a group legend, one labeled input per option, and nearby help. Tab to a choice, then use arrow keys to move between options.</p></div>
      <fieldset class="spec-stack">
        <legend>Connection method</legend>
        <label class="k-checkbox-hit choice-example"><input v-model="radioChoice" class="k-checkbox" type="radio" name="review-radio-method" value="agent"> Agent</label>
        <label class="k-checkbox-hit choice-example"><input v-model="radioChoice" class="k-checkbox" type="radio" name="review-radio-method" value="manual"> Manual</label>
      </fieldset>
      <fieldset class="spec-stack" aria-describedby="review-radio-disabled-help">
        <legend>Disabled examples</legend>
        <label class="k-checkbox-hit choice-example"><input class="k-checkbox" type="radio" name="review-radio-disabled" checked disabled> Agent</label>
        <label class="k-checkbox-hit choice-example"><input class="k-checkbox" type="radio" name="review-radio-disabled" disabled> Manual</label>
        <p id="review-radio-disabled-help" class="spec-note">Connection method is managed by your organization.</p>
      </fieldset>
      <form class="spec-stack" @submit.prevent="radioSaved = true">
        <fieldset class="spec-stack">
          <legend>Environment · required</legend>
          <label class="k-checkbox-hit choice-example"><input v-model="requiredRadio" class="k-checkbox" type="radio" name="review-required-radio" value="staging" required @change="radioSaved = false"> Staging</label>
          <label class="k-checkbox-hit choice-example"><input v-model="requiredRadio" class="k-checkbox" type="radio" name="review-required-radio" value="production" required @change="radioSaved = false"> Production</label>
        </fieldset>
        <div><button class="k-btn k-btn--ghost" type="submit">Continue</button></div>
        <p v-if="radioSaved" class="spec-note" role="status">Environment selected in this fixture.</p>
      </form>
    </section>
    <section v-else class="spec-section spec-stack" aria-labelledby="review-checkbox-heading">
      <div><h3 id="review-checkbox-heading">Checkbox states</h3><p class="spec-note">Anatomy: a native control, its clickable label, and optional explanation. Compare selection, partial selection, disabled states, and keyboard focus.</p></div>
      <fieldset class="spec-stack">
        <legend>Choose tools · {{ checkboxChoices.length }} of {{ checkboxOptions.length }} selected</legend>
        <label class="k-checkbox-hit choice-example"><input class="k-checkbox" type="checkbox" :checked="checkboxChoices.length === checkboxOptions.length" :indeterminate.prop="checkboxChoices.length > 0 && checkboxChoices.length < checkboxOptions.length" @change="toggleAllCheckboxes"> All tools</label>
        <label v-for="option in checkboxOptions" :key="option" class="k-checkbox-hit choice-example"><input v-model="checkboxChoices" class="k-checkbox" type="checkbox" :value="option"> {{ option }}</label>
      </fieldset>
      <fieldset class="spec-stack" aria-describedby="review-checkbox-disabled-help">
        <legend>Disabled examples</legend>
        <label class="k-checkbox-hit choice-example"><input class="k-checkbox" type="checkbox" disabled> Unchecked</label>
        <label class="k-checkbox-hit choice-example"><input class="k-checkbox" type="checkbox" checked disabled> Checked</label>
        <label class="k-checkbox-hit choice-example"><input class="k-checkbox" type="checkbox" :indeterminate.prop="true" disabled> Partially selected</label>
        <p id="review-checkbox-disabled-help" class="spec-note">Unavailable while organization policy manages these options.</p>
      </fieldset>
    </section>
  </div>
</template>

<style scoped>
.choice-example {
  display: flex;
  align-items: center;
  gap: 8px;
}
</style>
