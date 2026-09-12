<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import type { Resource } from '../api';
import { useSession, useTask } from '../state';
import ResourcePage from '../portalkit/ResourcePage.vue';
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue';
import ConnectionPolicy from '../components/ConnectionPolicy.vue';
import StatusBadge from '../portalkit/StatusBadge.vue';
const props = defineProps<{ kind: 'connections' | 'operations' | 'events'; name: string }>();
const session = useSession(); const read = useTask(); const resource = ref<Resource>();
const policyMessage = ref('');
function policySaved(result: Resource) { resource.value = result; policyMessage.value = 'Team policy saved. Refresh the connection to check readiness.'; }
const facts = computed(() => {
  const r = resource.value; if (!r) return [];
  const spec = r.spec || {};
  if (props.kind === 'connections') return [
    ['Secret namespace', (spec.apiKeySecretRef as { namespace?: string })?.namespace || 'default'], ['Secret reference', (spec.apiKeySecretRef as { name?: string })?.name || '—'],
    ['Allowed teams', (spec.teams as { id: string }[] | undefined)?.map(t => t.id).join(', ') || 'All accessible teams'],
    ['Checked at', r.status?.checkedAt || '—'], ['Message', r.status?.message || '—'],
  ];
  const keys = props.kind === 'operations' ? ['connection', 'action', 'teamID', 'issueID', 'query', 'after', 'first', 'title', 'description', 'stateID', 'body', 'since'] : ['connection', 'type', 'action', 'deliveryID', 'entityID', 'issueID', 'teamID', 'receivedAt', 'expiresAt'];
  return [...keys.filter(k => spec[k] !== undefined && spec[k] !== '').map(k => [k.replace(/([A-Z])/g, ' $1').replace(/^./, c => c.toUpperCase()), String(spec[k])]), ['Message', r.status?.message || '—'], ...(props.kind === 'operations' ? [['Started', r.status?.startedAt || '—'], ['Completed', r.status?.completedAt || '—']] : [])];
});
function load() { void read.run(api => api.get(props.kind, props.name), result => { resource.value = result; }); }
function browse() { session.selection.connection = props.name; session.selection.team = ''; session.navigate('issues'); }
onMounted(load);
</script>
<template>
  <ResourcePage :title="name" :kind="kind === 'connections' ? 'Connection' : kind === 'operations' ? 'Operation' : 'Event'" :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" :stale="read.state.loaded && !!read.state.error" retryable @retry="load">
    <template #actions><button class="k-btn k-btn--ghost" :disabled="read.state.loading" @click="load">Refresh</button><button v-if="kind === 'connections' && resource" class="k-btn k-btn--primary" @click="browse">Browse issues</button></template>
    <template #status><StatusBadge v-if="resource && kind !== 'events'" :status="kind === 'connections' ? resource.status?.ready ? 'Ready' : 'Not ready' : resource.status?.phase || 'Pending'" /></template>
    <div class="linear-detail-content">
    <ResourceSectionCard title="Details"><dl class="linear-facts"><div v-for="[label, value] in facts" :key="label"><dt>{{ label }}</dt><dd>{{ value }}</dd></div></dl></ResourceSectionCard>
    <ResourceSectionCard v-if="kind === 'connections' && resource" title="Team access"><p v-if="policyMessage" class="linear-notice" role="status">{{ policyMessage }}</p><ConnectionPolicy :key="resource.metadata.resourceVersion" :resource="resource" @saved="policySaved" /></ResourceSectionCard>
    <button v-if="kind === 'connections' && session.draft.returnToIssue" class="k-btn k-btn--primary" @click="session.navigate('issues/create')">Return to issue draft</button>
    <ResourceSectionCard v-if="resource?.status?.result" title="Result"><pre class="linear-result">{{ JSON.stringify(resource.status.result, null, 2) }}</pre></ResourceSectionCard>
    </div>
  </ResourcePage>
</template>
