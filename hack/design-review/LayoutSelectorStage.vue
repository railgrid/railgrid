<script setup>
import { ref } from 'vue'
import LayoutSelector from '../../provider-sdk/portalkit-vue/LayoutSelector.vue'
import ResourceTable from '../../provider-sdk/portalkit-vue/ResourceTable.vue'

const layout = ref('grid')
const projects = [
  { name: 'Customer portal', environment: 'Production' },
  { name: 'Documentation', environment: 'Preview' },
  { name: 'Status dashboard', environment: 'Production' },
]
const columns = [{ key: 'name', label: 'Project', primary: true }, { key: 'environment', label: 'Environment' }]
</script>

<template>
  <div class="layout-review">
    <div class="layout-review__toolbar">
      <div><h3>Projects</h3><p>Choose how to view this collection.</p></div>
      <LayoutSelector v-model="layout" aria-label="Layout" />
    </div>
    <div v-if="layout === 'grid'" class="layout-review__grid">
      <article v-for="project in projects" :key="project.name" class="k-card layout-review__card">
        <strong>{{ project.name }}</strong><span>{{ project.environment }}</span>
      </article>
    </div>
    <ResourceTable v-else :columns="columns" :rows="projects" :loaded="true" variant="simple" aria-label="Example projects" />
    <p class="spec-note">Sample data. The selector uses the same shared component as Railgrid's collection pages.</p>
    <div class="spec-row">
      <a class="k-btn k-btn--ghost" href="http://127.0.0.1:5198/?provider=agents&amp;scenario=layout&amp;route=%23%2Fagents" target="_blank" rel="noopener">Open Agents product fixture</a>
      <a href="https://vercel.com/geist/switch" target="_blank" rel="noopener">Vercel Switch reference</a>
    </div>
  </div>
</template>

<style scoped>
.layout-review { display: grid; gap: 18px; min-width: 0; }
.layout-review__toolbar { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px; }
.layout-review__toolbar h3 { margin: 0 0 4px; font-size: 14px; font-weight: 650; }
.layout-review__toolbar p { margin: 0; color: var(--color-text-secondary); font-size: 12px; }
.layout-review__grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 180px), 1fr)); gap: 10px; }
.layout-review__card { display: grid; align-content: start; gap: 8px; min-height: 90px; padding: 14px; font-size: 13px; }
.layout-review__card span { color: var(--color-text-secondary); font-size: 12px; }
.layout-review a:not(.k-btn) { color: var(--color-accent); font-size: 12px; text-decoration: underline; text-underline-offset: 3px; }
</style>
