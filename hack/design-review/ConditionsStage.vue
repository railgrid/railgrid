<script setup>
import { computed, ref } from 'vue'
import ConditionsPanel from '../../provider-sdk/portalkit-vue/ConditionsPanel.vue'
import ResourceSectionCard from '../../provider-sdk/portalkit-vue/ResourceSectionCard.vue'

const snapshot = ref('current')
const faultSeverity = ref('default')
const semantics = computed(() => ({ StorageDegraded: faultSeverity.value === 'danger' ? { polarity: 'negative', severity: 'danger' } : { polarity: 'negative' } }))
const conditions = [
  { type: 'Ready', status: 'False', reason: 'ReplicasNotReady', message: 'Two of three replicas are ready.', lastTransitionTime: '2026-11-12T14:20:00Z' },
  { type: 'Available', status: 'True', reason: 'MinimumAvailable', message: 'The deployment is serving traffic.', lastTransitionTime: '2026-11-12T14:18:00Z' },
  { type: 'StorageDegraded', status: 'True', reason: 'DiskPressure', message: 'The node is low on free storage.', lastTransitionTime: '2026-11-12T14:19:00Z' },
  { type: 'Healthy', status: 'Unknown', reason: 'ProbePending', message: 'Waiting for the health probe.' },
]
</script>

<template>
  <div class="spec-stack">
    <label class="spec-field spec-fit">Condition snapshot
      <select v-model="snapshot" class="k-input"><option value="current">Current generation</option><option value="reconciling">Controller catching up</option><option value="empty">No conditions</option></select>
    </label>
    <label class="spec-field spec-fit">Storage fault severity
      <select v-model="faultSeverity" class="k-input"><option value="default">Default warning</option><option value="danger">Explicit danger</option></select>
    </label>
    <ResourceSectionCard id="conditions-example" title="Conditions" description="Controller observations for a deployment.">
      <ConditionsPanel :show-title="false" :conditions="snapshot === 'empty' ? [] : conditions" :generation="5" :observed-generation="snapshot === 'reconciling' ? 4 : 5" :semantics="semantics" />
    </ResourceSectionCard>
    <p class="spec-note">Synthetic observations show the shared panel; switch the storage fault severity to compare the default warning with an explicit danger tone. They do not represent a live deployment. Infrastructure instance details use the same neutral empty message.</p>
    <div class="spec-row">
      <a class="k-btn k-btn--ghost" href="http://127.0.0.1:5201/" target="_blank" rel="noopener">Open deployment detail fixture</a>
      <a href="https://vercel.com/docs/deployments/troubleshoot-a-build" target="_blank" rel="noopener">Vercel deployment diagnostics</a>
    </div>
  </div>
</template>
