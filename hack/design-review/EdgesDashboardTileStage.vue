<script setup>
import { computed, ref } from 'vue'
import DashboardTile from '../../providers/edges/portal/src/DashboardTile.vue'

const service = ref('unavailable')
const version = ref(0)
async function simulatedFetch() {
  await new Promise(resolve => setTimeout(resolve, 700))
  if (service.value === 'unavailable') return new Response(JSON.stringify({ message: 'The simulated edge service is unavailable.' }), { status: 503, headers: { 'Content-Type': 'application/json' } })
  return new Response(JSON.stringify({ items: [] }), { headers: { 'Content-Type': 'application/json' } })
}
const context = computed(() => ({ tenant: 'review', orgUUID: 'review-org', workspaceUUID: 'review-workspace', token: `fixture-${version.value}`, fetch: simulatedFetch }))
</script>

<template>
  <section class="spec-section spec-stack" aria-labelledby="edges-dashboard-title">
    <div><h3 id="edges-dashboard-title">Edges dashboard tile</h3><p class="spec-note">This product tile uses a simulated service. Change the service to Available and retry, then change it back and refresh to inspect the retained empty snapshot. No hub is contacted.</p></div>
    <label class="spec-field spec-fit">Simulated edge service
      <select v-model="service" class="k-input"><option value="unavailable">Unavailable</option><option value="available">Available</option></select>
    </label>
    <div><button class="k-btn k-btn--secondary" type="button" @click="version++">Refresh edge fixture</button></div>
    <div class="k-card" style="padding: 16px; max-width: 360px">
      <h4>Edges</h4>
      <DashboardTile :context="context" />
    </div>
  </section>
</template>
