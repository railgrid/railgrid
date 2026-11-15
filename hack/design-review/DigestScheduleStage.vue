<script setup>
import { onUnmounted, ref } from 'vue'
import AgentDigestSchedule from '../../providers/agents/portal/src/components/AgentDigestSchedule.vue'
import agentsStyles from '../../providers/agents/portal/src/style.css?inline'

const style = document.createElement('style')
style.textContent = agentsStyles
document.head.appendChild(style)
onUnmounted(() => style.remove())
const expression = ref('0 8 * * 1-5')
const timeMissing = ref(false)
const disabled = ref(false)
const invalid = ref(false)
</script>

<template>
  <component :is="'railgrid-provider-agents'" class="digest-review">
    <div class="spec-stack">
      <p class="spec-note">Change the time or weekdays, then switch to cron and back. These sample changes stay in the review.</p>
      <AgentDigestSchedule v-model="expression" v-model:time-missing="timeMissing" default-value="0 8 * * 1-5" :disabled="disabled" :error="invalid ? 'Enter a valid schedule.' : undefined" />
      <div class="spec-row"><label><input v-model="disabled" type="checkbox"> Disable schedule</label><label><input v-model="invalid" type="checkbox"> Show error</label></div>
    </div>
  </component>
</template>

<style scoped>
.digest-review { height: auto; }
</style>
