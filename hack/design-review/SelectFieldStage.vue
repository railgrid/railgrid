<script setup>
import { onMounted, ref } from 'vue'
import '../../provider-sdk/portalkit/form-select'
import FormSelect from '../../provider-sdk/portalkit-vue/FormSelect.vue'

const options = [
  { value: 'west', label: 'West' },
  { value: 'east', label: 'East' },
  { value: 'central', label: 'Central', disabled: true },
]
const region = ref('west')
const requiredRegion = ref('')
const submitted = ref(false)
const plain = ref(null)
onMounted(() => {
  Object.assign(plain.value, { options, value: 'west', labelledby: 'sample-plain-region-label' })
})
</script>

<template>
  <div class="spec-stack">
    <div class="spec-grid">
      <div class="spec-field">
        <span id="sample-region-label">Region</span>
        <FormSelect v-model="region" :options="options" labelledby="sample-region-label" />
      </div>
      <label class="spec-field">Native region
        <select class="k-input"><option>West</option><option>East</option><option disabled>Central</option></select>
      </label>
      <div class="spec-field">
        <span id="sample-plain-region-label">Framework-neutral region</span>
        <railgrid-form-select ref="plain" />
      </div>
    </div>
    <form class="spec-section spec-stack" @submit.prevent="submitted = true">
      <div class="spec-field">
        <span id="required-region-label">Target region</span>
        <FormSelect
          v-model="requiredRegion"
          :options="options"
          labelledby="required-region-label"
          placeholder="Select a region"
          :invalid="submitted && !requiredRegion"
          error-message="Choose a target region."
          required
        />
      </div>
      <button type="submit" class="k-btn k-btn--ghost spec-fit">Validate example</button>
    </form>
  </div>
</template>
