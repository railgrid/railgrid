<script setup>
import { nextTick, onMounted, ref } from 'vue'
import DashboardTile from '../../providers/agents/portal/src/views/DashboardTile.vue'
import '../../providers/agents/portal/src/style.css'

const tile = ref(null)
const context = ref(null)
const service = ref('unavailable')
onMounted(async () => {
  Object.assign(tile.value.api, {
    listAgents: async () => {
      await new Promise(resolve => setTimeout(resolve, 700))
      if (service.value === 'unavailable') throw new Error('The simulated service is unavailable.')
      if (service.value === 'unbound') throw Object.assign(new Error('The simulated binding is unavailable.'), { reason: 'APIBindingMissing' })
      if (service.value === 'forbidden') throw Object.assign(new Error('no workspace selected; private diagnostic'), { status: 403 })
      return [{}]
    },
    listRuns: async () => ({ items: [] }),
    listSchedules: async () => [],
  })
  await nextTick()
  context.value = { tenant: 'review', orgUUID: 'review-org', workspaceUUID: 'review-workspace', basePath: '/ui/providers/agents/' }
})
</script>

<template>
  <section class="spec-section spec-stack" aria-labelledby="dashboard-tile-title">
    <div><h3 id="dashboard-tile-title">Agents dashboard tile</h3><p class="spec-note">This mounts the product tile with a simulated service. Select Available and retry, then switch to Binding missing or Access denied and refresh to review recovery guidance with a retained snapshot. No hub is contacted.</p></div>
    <label class="spec-field spec-fit">Simulated service
      <select v-model="service" class="k-input"><option value="unavailable">Unavailable</option><option value="available">Available</option><option value="unbound">Binding missing</option><option value="forbidden">Access denied</option></select>
    </label>
    <div><button class="k-btn k-btn--secondary" type="button" @click="tile.load()">Refresh fixture</button></div>
    <div class="k-card" style="padding: 16px; max-width: 360px">
      <h4>Agents</h4>
      <railgrid-dashboard-tile-agents><DashboardTile ref="tile" :context="context" /></railgrid-dashboard-tile-agents>
    </div>
  </section>
</template>
