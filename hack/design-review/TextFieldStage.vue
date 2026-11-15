<script setup>
defineProps({ multiline: Boolean })
</script>

<template>
  <div class="spec-grid">
    <template v-if="multiline">
      <label class="spec-field">Instructions<textarea class="k-input" rows="6" placeholder="Describe how this agent should help"></textarea></label>
      <label class="spec-field">Read-only instructions<textarea class="k-input" rows="3" readonly>Review deployment health.</textarea></label>
      <label class="spec-field">Managed instructions<textarea class="k-input" rows="3" disabled>Managed by your organization.</textarea></label>
      <div class="spec-field"><label for="review-rollout-instructions">Rollout instructions</label><textarea id="review-rollout-instructions" class="k-input" rows="3" aria-invalid="true" aria-describedby="review-instructions-error">Review</textarea><small id="review-instructions-error">Enter at least 20 characters.</small></div>
    </template>
    <template v-else>
      <label class="spec-field">Display name<input class="k-input" placeholder="Production edge"></label>
      <div class="spec-field"><label for="review-resource-name">Resource name</label><input id="review-resource-name" class="k-input" value="Production Edge" aria-invalid="true" aria-describedby="review-name-error"><small id="review-name-error">Use lowercase letters, numbers and hyphens.</small></div>
      <label class="spec-field">Managed hostname<input class="k-input" value="staging-west.example.test" disabled></label>
    </template>
  </div>
</template>
