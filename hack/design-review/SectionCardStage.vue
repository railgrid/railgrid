<script setup>
import { ref } from 'vue'
import ResourceSectionCard from '../../provider-sdk/portalkit-vue/ResourceSectionCard.vue'

const notice = ref('')
</script>

<template>
  <div class="spec-stack">
    <p class="spec-note">Anatomy: an optional heading and description, caller-owned actions, and a content body. Compare a boxed card with a divided section; data is synthetic.</p>
    <div class="k-resource-page">
    <ResourceSectionCard id="section-card-status" title="Service status" description="The latest controller observation for this service.">
      <template #actions><button class="k-btn k-btn--ghost" type="button" @click="notice = 'Refresh selected in the fixture.'">Refresh</button></template>
      <dl class="section-review__facts"><div><dt>Endpoint</dt><dd>home-server:8123</dd></div><div><dt>Last checked</dt><dd>2 minutes ago</dd></div></dl>
    </ResourceSectionCard>
    <p class="spec-note">Divided section</p>
    <ResourceSectionCard variant="divided" title="Available tools" description="Tools exposed to agents in this workspace."><p class="section-review__body">This service makes its configured tools available to agents in the workspace.</p></ResourceSectionCard>
    </div>
    <p class="spec-note" role="status" aria-live="polite">{{ notice }}</p>
    <div class="spec-row"><a class="k-btn k-btn--ghost" href="http://127.0.0.1:5201/" target="_blank" rel="noopener">Open deployment detail fixture</a><a class="section-review__reference" href="https://vercel.com/geist/fieldset" target="_blank" rel="noopener">Vercel Fieldset reference</a></div>
  </div>
</template>

<style scoped>
.section-review__facts { display: grid; gap: 12px; margin: 0; }
.section-review__facts div { display: grid; grid-template-columns: minmax(7rem, 1fr) 2fr; gap: 12px; font-size: 13px; }
.section-review__facts dt { color: var(--color-text-secondary); }
.section-review__facts dd { margin: 0; overflow-wrap: anywhere; }
.section-review__body { margin: 0; font-size: 13px; }
.section-review__reference { color: var(--color-accent); font-size: 12px; text-decoration: underline; text-underline-offset: 3px; }
</style>
