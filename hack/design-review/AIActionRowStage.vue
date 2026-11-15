<script setup>
import { ref } from 'vue'
import AIActionRow from '../../provider-sdk/agentkit-vue/AIActionRow.vue'

const expanded = ref(false)
</script>

<template>
  <div class="spec-stack">
    <p class="spec-note">Anatomy: a state cue, action title, optional target and outcome, and expandable evidence. The provider supplies the sample state and details; no operations run.</p>
    <section class="spec-section spec-stack" aria-labelledby="activity-states-heading">
      <h3 id="activity-states-heading">Activity states</h3>
      <AIActionRow title="Read deployment status" status="succeeded" outcome="Completed" />
      <AIActionRow title="Check service health" status="running" busy outcome="Running" />
      <AIActionRow title="Apply rollout change" status="waiting" attention outcome="Needs approval" />
      <AIActionRow title="Check endpoint" status="failed" error outcome="Failed" />
      <AIActionRow title="Read recent logs" status="canceled" canceled outcome="Canceled" />
    </section>
    <section class="spec-section spec-stack" aria-labelledby="activity-details-heading">
      <div><h3 id="activity-details-heading">Expandable evidence</h3><p class="spec-note">Open and close the row with a pointer or keyboard. The failure and its consequence remain visible in the row.</p></div>
      <AIActionRow title="Check service health" target="edge/staging-west" status="failed" error outcome="Timed out" expandable :expanded="expanded" details-id="review-action-details" @toggle="expanded = !expanded">
        <template #details><p class="spec-note">The health check did not respond within 10 seconds. Retry after checking the edge connection.</p></template>
      </AIActionRow>
    </section>
    <p class="spec-note"><a href="http://127.0.0.1:5200/" target="_blank" rel="noopener">Open the Agents run fixture</a> to compare activity rows with a failed run and its details.</p>
  </div>
</template>
