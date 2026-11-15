<script setup>
import { computed, ref } from 'vue'
import DashboardTile from '../../providers/code/portal/src/DashboardTile.vue'

const service = ref('unavailable')
async function simulatedFetch() {
  await new Promise(resolve => setTimeout(resolve, 700))
  if (service.value === 'unavailable') return new Response(JSON.stringify({ message: 'The simulated Code service is unavailable.' }), { status: 503, headers: { 'Content-Type': 'application/json' } })
  return new Response(JSON.stringify({ items: [] }), { headers: { 'Content-Type': 'application/json' } })
}
const context = computed(() => ({ tenant: 'review', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: 'fixture', fetch: simulatedFetch }))
</script>

<template>
  <section class="spec-section spec-stack" aria-labelledby="code-dashboard-title">
    <div><h3 id="code-dashboard-title">Code dashboard tile</h3><p class="spec-note">This product tile uses a simulated service. Change the service to Available and retry, then change it back and wait for the background refresh (up to 30 seconds) to inspect the retained empty snapshot. No hub is contacted.</p></div>
    <label class="spec-field spec-fit">Simulated Code service
      <select v-model="service" class="k-input"><option value="unavailable">Unavailable</option><option value="available">Available</option></select>
    </label>
    <div class="k-card" style="padding: 16px; max-width: 360px">
      <h4>Code</h4>
      <DashboardTile :context="context" />
    </div>
  </section>
</template>
