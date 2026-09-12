<script setup lang="ts">
import { computed, onActivated, ref } from 'vue';
import type { Resource } from '../api';
import { useSession, useTask } from '../state';
import { resourcePath } from '../routes';
import type { ResourceTableChange } from '../portalkit/table';
import ResourceTable from '../portalkit/ResourceTable.vue';
import StatusBadge from '../portalkit/StatusBadge.vue';
const props = defineProps<{ kind: 'operations' | 'events' }>();
const session = useSession(); const read = useTask(); const resources = ref<Resource[]>([]);
const page = ref(1); const pageSize = ref(10); const cursor = ref(''); const nextCursor = ref('');
const lookup = ref(''); const activeLookup = ref('');
const title = computed(() => props.kind === 'operations' ? 'Operations' : 'Events');
const rows = computed(() => resources.value.map(r => ({ name: r.metadata.name, connection: r.spec?.connection || '—', action: r.spec?.action || '—', status: r.status?.phase || (props.kind === 'operations' ? 'Pending' : r.spec?.type || '—'), message: r.status?.message || r.spec?.issueID || r.spec?.entityID || '—', received: r.spec?.receivedAt || r.metadata.creationTimestamp || '—' })));
const columns = [{ key: 'name', label: 'Summary', primary: true }, { key: 'connection', label: 'Connection' }, { key: 'action', label: 'Action' }, { key: 'received', label: 'Created' }];
function load(targetPage = page.value, targetSize = pageSize.value, targetCursor = cursor.value, name = activeLookup.value) {
  void read.run(async api => name ? { items: [await api.get(props.kind, name)], metadata: { continue: '' } } : api.listPage(props.kind, targetSize, targetCursor), result => {
    resources.value = result.items; page.value = targetPage; pageSize.value = targetSize; cursor.value = targetCursor;
    nextCursor.value = result.metadata?.continue || ''; activeLookup.value = name;
  });
}
function change(event: ResourceTableChange) { load(event.page, event.pageSize, event.cursor || ''); }
function find() { read.reset(); resources.value = []; load(1, pageSize.value, '', lookup.value.trim()); }
function clear() { lookup.value = ''; find(); }
function open(row: Record<string, unknown>) { session.navigate(resourcePath(props.kind, String(row.name))); }
onActivated(() => load());
</script>
<template>
  <section class="linear-page">
    <header class="linear-page-head"><div><h2 class="linear-page-title">{{ title }}</h2><p class="linear-page-meta">{{ kind === 'operations' ? 'Inspect pending or uncertain operations before repeating a write.' : 'Verified notifications received from Linear.' }}</p></div><button class="k-btn k-btn--ghost" :disabled="read.state.loading" @click="load(1, pageSize, '')">Refresh</button></header>
    <form class="linear-fields" @submit.prevent="find"><label for="history-name">Find by exact name<input id="history-name" v-model="lookup" class="k-input" placeholder="Resource name" :disabled="read.state.loading"></label><button class="k-btn k-btn--ghost" :disabled="read.state.loading || !lookup.trim()">Find</button><button v-if="activeLookup" type="button" class="k-btn k-btn--ghost" :disabled="read.state.loading" @click="clear">Show all</button></form>
    <p class="linear-page-meta">Browse one page at a time, or find an exact resource name across all history. Full-text history search is not available.</p>
    <ResourceTable :columns="columns" :rows="rows" row-key="name" pagination-mode="server" :page="page" :page-size="pageSize" :cursor="cursor || null" :page-info="{ hasNext: !!nextCursor, nextCursor: nextCursor || null }" :loaded="read.state.loaded" :loading="read.state.loading" :error="read.state.error" :stale="read.state.loaded && !!read.state.error" retryable :empty-text="`No ${kind} yet.`" :aria-label="title" :row-aria-label="row => `Open ${row.name}`" @change="change" @retry="load()" @row-click="open">
      <template #name="{ row }"><div class="linear-history-summary"><StatusBadge v-if="kind === 'operations'" :status="String(row.status)" /><span v-else>{{ row.status }}</span><button class="k-btn k-btn--ghost k-table-resource-link" @click.stop="open(row)">{{ row.name }}</button><span class="linear-history-message">{{ row.message }}</span></div></template>
    </ResourceTable>
  </section>
</template>
