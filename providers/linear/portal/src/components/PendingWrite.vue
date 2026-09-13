<script setup lang="ts">
defineProps<{ name: string; loading: boolean }>();
defineEmits<{ resume: []; separate: [] }>();
</script>
<template>
  <div v-if="name" class="linear-form">
    <p class="linear-notice" role="status">A write is awaiting confirmation. Leaving this page does not cancel it.</p>
    <div class="linear-actions">
      <button type="button" class="k-btn k-btn--ghost" :disabled="loading" @click="$emit('resume')">Check outcome</button>
    </div>
    <details v-if="!loading">
      <summary class="k-btn k-btn--ghost">Prepare a separate write</summary>
      <p class="linear-notice">First check the outcome and inspect Linear. A separate write can duplicate an issue or comment if this one already succeeded.</p>
      <button type="button" class="k-btn k-btn--ghost" @click="$emit('separate')">I checked Linear; prepare a separate write</button>
    </details>
  </div>
</template>
