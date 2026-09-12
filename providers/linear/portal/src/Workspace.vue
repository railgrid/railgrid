<script setup lang="ts">
import { computed, onBeforeUnmount, provide, reactive, ref, watch } from 'vue';
import { Plug, ListTodo, Activity, Radio } from 'lucide-vue-next';
import type { FarosContext } from './api';
import type { Route } from './routes';
import { sessionKey } from './state';
import Tabs from './portalkit/Tabs.vue';
import ResourceBackLink from './portalkit/ResourceBackLink.vue';
import ConnectionsView from './views/ConnectionsView.vue';
import ConnectionCreateView from './views/ConnectionCreateView.vue';
import ResourceDetailView from './views/ResourceDetailView.vue';
import IssuesView from './views/IssuesView.vue';
import IssueCreateView from './views/IssueCreateView.vue';
import IssueDetailView from './views/IssueDetailView.vue';
import HistoryView from './views/HistoryView.vue';
const props = defineProps<{ context: () => FarosContext; authoritySignal: AbortSignal; route: Route }>();
const root = ref<HTMLElement>();
const namespace = ref(props.route.namespace || 'default');
const namespaceDraft = ref(namespace.value);
const epoch = ref(0);
let controller = new AbortController();
const selection = reactive({ connection: '', team: '', query: '' });
function scopedPath(path: string) { return `namespaces/${encodeURIComponent(namespace.value)}/${path}`; }
function navigate(path: string, replace = false) { root.value?.dispatchEvent(new CustomEvent('faros-navigate', { bubbles: true, detail: { path: scopedPath(path), ...(replace ? { replace: true } : {}) } })); }
function href(path: string) {
  const current = window.location.pathname;
  const prefix = current.match(/^(.*\/providers\/linear)(?:\/|$)/)?.[1] || '/ui/providers/linear';
  return `${prefix}/${scopedPath(path)}`;
}
watch(namespace, () => {
  controller.abort(); controller = new AbortController(); epoch.value++;
  Object.assign(selection, { connection: '', team: '', query: '' });
}, { flush: 'sync' });
watch(() => props.route.namespace, value => { if (value) { namespace.value = value; namespaceDraft.value = value; } });
onBeforeUnmount(() => controller.abort());
provide(sessionKey, { context: props.context, get namespace() { return namespace.value; }, get signal() { return AbortSignal.any([controller.signal, props.authoritySignal]); }, selection, navigate, href });
const tabs = [{ id: 'connections', label: 'Connections', icon: Plug }, { id: 'issues', label: 'Issues', icon: ListTodo }, { id: 'operations', label: 'Operations', icon: Activity }, { id: 'events', label: 'Events', icon: Radio }];
const collection = computed(() => !props.route.name && !props.route.create && !props.route.invalid);
function changeNamespace() { namespace.value = namespaceDraft.value.trim() || 'default'; namespaceDraft.value = namespace.value; navigate(props.route.page, true); }
</script>
<template>
  <div ref="root" class="linear-app">
    <Tabs v-if="collection" :tabs="tabs" :active="route.page" aria-label="Linear provider sections" @select="navigate" />
    <form v-if="collection" class="linear-namespace" @submit.prevent="changeNamespace">
      <label for="linear-namespace">Namespace</label>
      <input id="linear-namespace" v-model="namespaceDraft" class="k-input linear-namespace-input" required pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?" maxlength="63">
      <button class="k-btn k-btn--ghost" :disabled="namespaceDraft === namespace">Apply</button>
    </form>
    <ResourceBackLink v-if="!collection" :href="href(route.page)" @back="navigate(route.page)">Back to {{ route.page }}</ResourceBackLink>
    <p v-if="route.invalid" role="alert">This Linear page was not found.</p>
    <template v-else>
      <KeepAlive :key="epoch" :max="4">
        <ConnectionsView v-if="collection && route.page === 'connections'" />
        <IssuesView v-else-if="collection && route.page === 'issues'" />
        <HistoryView v-else-if="collection" :key="route.page" :kind="route.page as 'operations' | 'events'" />
      </KeepAlive>
      <ConnectionCreateView v-if="route.create === 'connection'" :key="`${epoch}:connection-create`" />
      <IssueCreateView v-else-if="route.create === 'issue'" :key="`${epoch}:issue-create`" />
      <IssueDetailView v-else-if="route.page === 'issues' && route.name" :key="`${epoch}:${route.connection}:${route.name}`" :connection="route.connection!" :id="route.name" />
      <ResourceDetailView v-else-if="route.name" :key="`${epoch}:${route.page}:${route.name}`" :kind="route.page as 'connections' | 'operations' | 'events'" :name="route.name" />
    </template>
  </div>
</template>
