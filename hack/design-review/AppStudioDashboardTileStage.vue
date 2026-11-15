<script setup>
import { ref } from 'vue'
import DashboardTile from '../../providers/app-studio/portal/src/DashboardTile.vue'

const service = ref('unavailable')
async function simulatedFetch() {
  await new Promise(resolve => setTimeout(resolve, 700))
  if (service.value === 'unavailable') return new Response(JSON.stringify({ message: 'The simulated App Studio service is unavailable.' }), { status: 503, headers: { 'Content-Type': 'application/json' } })
  return new Response(JSON.stringify({ items: [], metadata: {} }), { headers: { 'Content-Type': 'application/json' } })
}
const context = { tenant: 'review', orgUUID: 'review-org', workspaceUUID: 'review-workspace', fetch: simulatedFetch }
</script>

<template>
  <section class="spec-section spec-stack" aria-labelledby="studio-dashboard-title">
    <div><h3 id="studio-dashboard-title">App Studio dashboard tile</h3><p class="spec-note">This product tile uses a simulated service. Change the service to Available and retry the initial failure. No hub is contacted.</p></div>
    <label class="spec-field spec-fit">Simulated App Studio service
      <select v-model="service" class="k-input"><option value="unavailable">Unavailable</option><option value="available">Available</option></select>
    </label>
    <div class="k-card" style="padding: 16px; max-width: 360px">
      <h4>App Studio</h4>
      <DashboardTile :context="context" />
    </div>
  </section>
</template>
