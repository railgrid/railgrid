<script setup>
import { computed, ref } from 'vue'
import { Activity, Clock3, Layers, Server } from 'lucide-vue-next'
import ResourceStatCards from '../../provider-sdk/portalkit-vue/ResourceStatCards.vue'

const count = ref(3)
const density = ref('compact')
const facts = [
  { id: 'revision', label: 'Revision', value: 'v42', detail: 'Previous revision v41', icon: Layers, mono: true },
  { id: 'replicas', label: 'Ready replicas', value: '2 / 3', detail: 'One replica awaiting readiness', icon: Activity },
  { id: 'updated', label: 'Last updated', value: '14:32 UTC', detail: '12 November 2026', icon: Clock3 },
  { id: 'cluster', label: 'Cluster', value: 'east', detail: 'Production environment', icon: Server },
]
const shown = computed(() => facts.slice(0, count.value))
</script>

<template>
  <div class="spec-stack">
    <p class="spec-note">Anatomy: a label, emphasized value, optional detail, and optional decorative icon. Change the count and density to inspect the grid.</p>
    <div class="spec-row">
      <label class="spec-field spec-fit">Number of facts<select v-model.number="count" class="k-input"><option v-for="n in 4" :key="n" :value="n">{{ n }}</option></select></label>
      <label class="spec-field spec-fit">Density<select v-model="density" class="k-input"><option value="compact">Compact</option><option value="default">Default</option></select></label>
    </div>
    <div class="k-resource-page"><ResourceStatCards :cards="shown" :density="density" aria-label="Deployment summary" /></div>
    <p class="spec-note">Synthetic deployment facts. The product supplies the labels, values, and icons.</p>
    <div class="spec-row"><a class="k-btn k-btn--ghost" href="http://127.0.0.1:5201/" target="_blank" rel="noopener">Open deployment detail fixture</a><a href="https://vercel.com/docs/deployments#resources-tab-and-deployment-summary" target="_blank" rel="noopener">Vercel deployment summary</a></div>
  </div>
</template>
