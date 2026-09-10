<!-- Canonical shared model connection summary. Credentials and actions remain caller-owned. -->
<script setup lang="ts">
import { Cpu } from 'lucide-vue-next'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
ensureAgentUIStyles()
defineProps<{ name: string; model: string; endpoint?: string; configured: boolean; isDefault?: boolean; testState?: string; testTone?: 'success' | 'danger' | 'muted'; busy?: boolean }>()
</script>
<template>
  <article class="k-model-connection" :aria-label="`Model ${name}`" :aria-busy="busy">
    <div class="k-model-connection__heading">
      <span class="k-model-connection__icon"><Cpu :stroke-width="1.75" /></span>
      <div class="k-model-connection__identity"><h3>{{ name }}</h3><p :title="model">{{ model }}</p></div>
      <StatusBadge v-if="isDefault" status="Default" tone="muted" />
    </div>
    <dl class="k-model-connection__endpoint"><dt>Endpoint</dt><dd :title="endpoint">{{ endpoint || 'Provider default' }}</dd></dl>
    <div class="k-model-connection__state">
      <StatusBadge :status="configured ? 'Credential stored' : 'Needs credential'" :tone="configured ? 'muted' : 'warning'" />
      <StatusBadge :status="testState || 'Not tested'" :tone="testTone || 'muted'" />
    </div>
    <slot />
    <footer class="k-model-connection__actions"><slot name="actions" /></footer>
  </article>
</template>
