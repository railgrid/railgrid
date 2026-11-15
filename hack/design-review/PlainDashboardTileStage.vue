<script setup>
import { onMounted, ref } from 'vue'
import { QuickstartDashboardTileElement } from '../../providers/quickstart/portal/src/tile'
import { KueryDashboardTile } from '../../providers/kuery/portal/src/dashboard-tile'

const quickHost = ref(null)
const kueryHost = ref(null)
const quickService = ref('unavailable')
const kueryService = ref('unavailable')
function transport(service) {
  return async () => {
    await new Promise(resolve => setTimeout(resolve, 700))
    if (service.value === 'unavailable') return new Response(JSON.stringify({ message: 'The simulated service is unavailable.' }), { status: 503, headers: { 'Content-Type': 'application/json' } })
    return new Response(JSON.stringify({ items: [], metadata: {} }), { headers: { 'Content-Type': 'application/json' } })
  }
}
onMounted(() => {
  const definitions = [
    ['review-quickstart-tile', QuickstartDashboardTileElement, quickHost.value, quickService, '/ui/providers/quickstart/'],
    ['review-kuery-tile', KueryDashboardTile, kueryHost.value, kueryService, '/ui/providers/kuery/'],
  ]
  for (const [tag, Tile, host, service, basePath] of definitions) {
    if (!customElements.get(tag)) customElements.define(tag, Tile)
    const tile = document.createElement(tag)
    tile.railgridContext = { tenant: 'review', orgUUID: 'review-org', workspaceUUID: 'review-workspace', basePath, fetch: transport(service) }
    host.appendChild(tile)
  }
})
</script>

<template>
  <section class="spec-section spec-stack" aria-labelledby="plain-dashboard-title">
    <div><h3 id="plain-dashboard-title">Quickstart and Kuery dashboard tiles</h3><p class="spec-note">Both tiles use simulated services. Choose Available and retry each initial failure; no hub is contacted.</p></div>
    <label class="spec-field spec-fit">Simulated Quickstart service
      <select v-model="quickService" class="k-input"><option value="unavailable">Unavailable</option><option value="available">Available</option></select>
    </label>
    <div ref="quickHost" class="k-card" style="padding: 16px; max-width: 360px" />
    <label class="spec-field spec-fit">Simulated Kuery service
      <select v-model="kueryService" class="k-input"><option value="unavailable">Unavailable</option><option value="available">Available</option></select>
    </label>
    <div ref="kueryHost" class="k-card" style="padding: 16px; max-width: 360px" />
  </section>
</template>
