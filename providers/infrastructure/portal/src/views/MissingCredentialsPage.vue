<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ tenantPath?: string | null }>()
const emit = defineEmits<{ (e: 'navigate', view: string): void }>()

// Build a kubectl snippet templated against the caller's tenant
// path so the user can copy-paste rather than learn kcp's per-cluster
// flag dance.
const cmd = computed(() => {
  const wsPath = props.tenantPath || '<your-tenant-workspace-path>'
  return `kubectl --context kedge-${wsPath.split(':').pop()} \\
  create secret generic cloud-credentials \\
  --from-literal=aws_access_key_id=YOUR_KEY \\
  --from-literal=aws_secret_access_key=YOUR_SECRET \\
  --from-literal=aws_region=us-east-1`
})
</script>

<template>
  <section class="page">
    <button class="link back" @click="emit('navigate', 'catalog')">← Back to templates</button>
    <header class="page-head">
      <div>
        <h2 class="page-title">Cloud credentials missing</h2>
        <p class="page-meta">
          The provider needs a Secret named <code>cloud-credentials</code> in
          your workspace's <code>default</code> namespace before it can
          provision templates.
        </p>
      </div>
    </header>

    <section class="panel">
      <h3 class="panel-title">Create it</h3>
      <pre>{{ cmd }}</pre>
      <p class="page-meta">
        Per-cloud key conventions (AWS, GCP, Azure, k8s) are documented in
        the provider's <code>docs/credentials.md</code> — published RGD
        templates assume those exact key names.
      </p>
    </section>

    <section class="panel">
      <h3 class="panel-title">Why?</h3>
      <p>
        This provider is a broker: when you provision a template it creates the
        underlying resource on your behalf. The provisioner needs cloud credentials to
        actually talk to AWS / GCP / Azure. We resolve them per-request from your
        workspace so credentials never leak across tenants.
      </p>
    </section>
  </section>
</template>
