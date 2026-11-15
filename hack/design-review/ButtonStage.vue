<script setup>
import { ref } from 'vue'

const busy = ref(false)
</script>

<template>
  <div class="spec-stack">
    <div class="spec-row">
      <button type="button" class="k-btn k-btn--primary">Create edge</button>
      <button type="button" class="k-btn k-btn--ghost">Refresh</button>
      <button type="button" class="k-btn k-btn--text">Show details</button>
      <button type="button" class="k-btn k-btn--danger">Remove</button>
      <button type="button" class="k-btn k-btn--ghost" disabled>Deploy</button>
    </div>
    <div class="spec-row">
      <button type="button" class="k-btn k-btn--ghost" :aria-busy="busy || undefined" :aria-disabled="busy || undefined" @click="busy = true">
        {{ busy ? 'Saving…' : 'Save changes' }}
      </button>
      <button v-if="busy" type="button" class="k-btn k-btn--text" @click="busy = false">Complete example</button>
    </div>
    <p class="spec-note">Deploy requires a target edge. The Save example keeps focus while busy.</p>
  </div>
</template>
