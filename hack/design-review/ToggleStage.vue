<script setup>
import { ref } from 'vue'

const notifications = ref(false)
</script>

<template>
  <div class="spec-stack">
    <section class="spec-section spec-stack">
      <div class="spec-row">
        <span id="notifications-example-label">Deployment notifications</span>
        <button type="button" class="k-toggle" role="switch" :aria-checked="notifications" aria-labelledby="notifications-example-label" aria-describedby="notifications-example-help" @click="notifications = !notifications">
          <span class="k-toggle__knob" aria-hidden="true"></span>
        </button>
      </div>
      <p id="notifications-example-help" class="spec-note">Notify this workspace when a deployment finishes.</p>
    </section>
    <section class="spec-section spec-stack">
      <div class="spec-row">
        <span id="disabled-context-example-label">Share page context</span>
        <button type="button" class="k-toggle" role="switch" aria-checked="false" aria-labelledby="disabled-context-example-label" aria-describedby="disabled-context-example-help" disabled>
          <span class="k-toggle__knob" aria-hidden="true"></span>
        </button>
      </div>
      <p id="disabled-context-example-help" class="spec-note">Choose an agent to enable page context.</p>
    </section>
  </div>
</template>
