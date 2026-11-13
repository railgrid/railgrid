<script setup>
import { computed, nextTick, ref } from 'vue'
import { Activity, Clock3, Layers } from 'lucide-vue-next'
import ResourcePage from '../../provider-sdk/portalkit-vue/ResourcePage.vue'
import ResourceSectionCard from '../../provider-sdk/portalkit-vue/ResourceSectionCard.vue'
import ResourceStatCards from '../../provider-sdk/portalkit-vue/ResourceStatCards.vue'
import ResourceTable from '../../provider-sdk/portalkit-vue/ResourceTable.vue'
import StatusBadge from '../../provider-sdk/portalkit-vue/StatusBadge.vue'
import ConditionsPanel from '../../provider-sdk/portalkit-vue/ConditionsPanel.vue'

const theme = ref('light')
const stale = ref(false)
const failureControl = ref(null)
const cards = [
  { id: 'revision', label: 'Revision', value: 'v42', detail: 'Previous revision v41', icon: Layers, mono: true },
  { id: 'replicas', label: 'Ready replicas', value: '2 / 3', detail: 'One replica awaiting readiness', icon: Activity },
  { id: 'updated', label: 'Last updated', value: '14:32 UTC', detail: '12 November 2026', icon: Clock3 },
]
const conditions = [
  { type: 'Available', status: 'True', reason: 'MinimumReplicasAvailable', message: 'Two replicas are serving traffic.', lastTransitionTime: '14:30 UTC' },
  { type: 'Progressing', status: 'False', reason: 'ReadinessTimeout', message: 'Revision v42 has one replica that is not ready.', lastTransitionTime: '14:32 UTC' },
]
const columns = [
  { key: 'time', label: 'Time' },
  { key: 'revision', label: 'Revision' },
  { key: 'event', label: 'Event', primary: true },
]
const events = [
  { time: '14:32 UTC', revision: 'v42', event: 'Readiness check timed out for replica api-v42-3' },
  { time: '14:30 UTC', revision: 'v42', event: 'Two of three replicas are ready' },
  { time: '14:27 UTC', revision: 'v42', event: 'Rollout started' },
  { time: '14:20 UTC', revision: 'v41', event: 'Previous revision healthy' },
]
const readError = computed(() => stale.value ? 'Could not refresh deployment details.' : null)
function toggleTheme() {
  theme.value = theme.value === 'light' ? 'dark' : 'light'
  document.documentElement.classList.toggle('light', theme.value === 'light')
}
async function retry() {
  stale.value = false
  await nextTick()
  failureControl.value?.focus()
}
</script>

<template>
  <main class="fixture">
    <div class="fixture-controls">
      <button ref="failureControl" class="k-btn k-btn--ghost" type="button" @click="stale = !stale">{{ stale ? 'Restore refresh' : 'Simulate failed refresh' }}</button>
      <button class="k-btn k-btn--ghost" type="button" @click="toggleTheme">Switch to {{ theme === 'light' ? 'dark' : 'light' }} theme</button>
    </div>
    <ResourcePage title="api-production" kind="Deployment" :loaded="true" :error="readError" :stale="stale" refresh-mode="background" :retryable="stale" @retry="retry">
      <template #meta><span>production · east cluster</span></template>
      <template #status><StatusBadge status="Needs attention" /></template>
      <template #summary><ResourceStatCards :cards="cards" density="compact" aria-label="Deployment summary" /></template>
      <template #body>
        <div class="content">
          <ResourceSectionCard id="rollout" title="Rollout needs attention" description="Revision v42 has not reached the desired replica count.">
            <p>Two replicas are serving traffic. Check the readiness diagnostic for the remaining replica before continuing the rollout.</p>
            <a class="k-btn k-btn--ghost inspect-link" href="#conditions">Review conditions</a>
          </ResourceSectionCard>
          <ResourceSectionCard id="conditions" tabindex="-1" eyebrow="Diagnostics" title="Conditions" description="Controller observations for revision v42.">
            <ConditionsPanel :conditions="conditions" :show-title="false" :generation="42" :observed-generation="42" />
          </ResourceSectionCard>
          <ResourceSectionCard id="events" eyebrow="Rollout" title="Recent events" description="Events for production, newest first.">
            <ResourceTable :columns="columns" :rows="events" aria-label="Deployment events" :interactive="false" variant="simple" />
          </ResourceSectionCard>
          <div class="detail-grid">
            <ResourceSectionCard id="metrics" title="Health signals" description="Production · last 15 minutes">
              <dl class="facts">
                <div><dt>Requests</dt><dd>148 / min</dd></div>
                <div><dt>Error rate</dt><dd>1.8%</dd></div>
                <div><dt>p95 latency</dt><dd>820 ms</dd></div>
              </dl>
            </ResourceSectionCard>
            <ResourceSectionCard id="logs" title="Recent logs" description="Replica api-v42-3 · 14:32 UTC">
              <pre class="logs">readiness probe failed
connection to 127.0.0.1:8080 timed out</pre>
            </ResourceSectionCard>
          </div>
        </div>
      </template>
    </ResourcePage>
  </main>
</template>
