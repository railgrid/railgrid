<script setup>
import { ref } from 'vue'
import ResourceBackLink from '../../provider-sdk/portalkit-vue/ResourceBackLink.vue'
import ResourcePage from '../../provider-sdk/portalkit-vue/ResourcePage.vue'
import StatusBadge from '../../provider-sdk/portalkit-vue/StatusBadge.vue'

const notice = ref('')
</script>

<template>
  <div class="spec-stack">
    <div>
      <h3>Detail page</h3>
      <p class="spec-note">A labeled link names the collection and sits above the resource heading.</p>
    </div>
    <div class="back-review__example">
      <ResourceBackLink href="#sample-instances" @back="notice = 'Return to Instances selected in the fixture.'">Instances</ResourceBackLink>
      <ResourcePage title="api-production" kind="Instance" :loaded="true">
        <template #status><StatusBadge status="Ready" /></template>
      </ResourcePage>
    </div>
    <div>
      <h3>Compact and busy states</h3>
      <p class="spec-note">The icon-only version follows App Studio’s conversation header, which returns to Projects. The disabled link represents an operation in progress.</p>
    </div>
    <div class="spec-row">
      <ResourceBackLink href="#sample-projects" icon-only aria-label="Back to projects" title="Back to projects" @back="notice = 'Return to projects selected in the fixture.'" />
      <ResourceBackLink href="#sample-instances" disabled>Instances</ResourceBackLink>
    </div>
    <p class="spec-note" role="status" aria-live="polite">{{ notice }}</p>
    <p class="spec-note">Ordinary activation reports a simulated route change. Modified clicks follow the links to these sample destinations.</p>
    <div id="sample-instances" class="spec-note">Sample destination: Instances</div>
    <div id="sample-projects" class="spec-note">Sample destination: Projects</div>
    <p class="spec-note">This is a synthetic detail-page composition using the shared component. Infrastructure instance and Edge details use the same link in the product.</p>
    <a href="https://vercel.com/design/guidelines" target="_blank" rel="noopener">Vercel web interface guidelines</a>
  </div>
</template>

<style scoped>
.back-review__example { display: grid; gap: 16px; padding: 16px; border: 1px solid var(--color-border-default); border-radius: 6px; }
.spec-stack > div > h3 { margin: 0 0 5px; font-size: 14px; }
.spec-stack > div > p { margin: 0; }
a:not(.k-btn) { color: var(--color-accent); font-size: 12px; text-decoration: underline; text-underline-offset: 3px; }
</style>
