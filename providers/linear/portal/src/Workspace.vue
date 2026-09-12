<script setup lang="ts">
import { computed, nextTick, provide, reactive, ref, watch } from 'vue';
import { Plug, ListTodo, Activity, Radio } from 'lucide-vue-next';
import type { FarosContext } from './api';
import { canonicalPath, type Route } from './routes';
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
watch(() => props.route, async () => {
  await nextTick();
  root.value?.dispatchEvent(new CustomEvent('faros-route-ready', { bubbles: true }));
}, { flush: 'post', immediate: true });
const draft = reactive({ title: '', description: '', stateID: '', returnToIssue: false });
const selection = reactive({ connection: '', team: '', query: '' });
function scopedPath(path: string) { return canonicalPath(path); }
function navigate(path: string, replace = false) { root.value?.dispatchEvent(new CustomEvent('faros-navigate', { bubbles: true, detail: { path: scopedPath(path), ...(replace ? { replace: true } : {}) } })); }
function href(path: string) {
  const current = window.location.pathname;
  const prefix = current.match(/^(.*\/providers\/linear)(?:\/|$)/)?.[1] || '/ui/providers/linear';
  return `${prefix}/${scopedPath(path)}`;
}
provide(sessionKey, { context: props.context, get signal() { return props.authoritySignal; }, draft, selection, navigate, href });
const tabs = [{ id: 'connections', label: 'Connections', icon: Plug }, { id: 'issues', label: 'Issues', icon: ListTodo }, { id: 'operations', label: 'Operations', icon: Activity }, { id: 'events', label: 'Events', icon: Radio }];
const collection = computed(() => !props.route.name && !props.route.create && !props.route.invalid);

</script>
<template>
  <div ref="root" class="linear-app">
    <Tabs v-if="collection" :tabs="tabs" :active="route.page" aria-label="Linear provider sections" @select="navigate" />
    <ResourceBackLink v-if="!collection" :href="href(route.page)" @back="navigate(route.page)">Back to {{ route.page }}</ResourceBackLink>
    <p v-if="route.invalid" role="alert">This Linear page was not found.</p>
    <template v-else>
      <KeepAlive :max="4">
        <ConnectionsView v-if="collection && route.page === 'connections'" />
        <IssuesView v-else-if="collection && route.page === 'issues'" />
        <HistoryView v-else-if="collection" :key="route.page" :kind="route.page as 'operations' | 'events'" />
      </KeepAlive>
      <ConnectionCreateView v-if="route.create === 'connection'" :key="'connection-create'" />
      <IssueCreateView v-else-if="route.create === 'issue'" :key="'issue-create'" />
      <IssueDetailView v-else-if="route.page === 'issues' && route.name" :key="`${route.connection}:${route.name}`" :connection="route.connection!" :id="route.name" />
      <ResourceDetailView v-else-if="route.name" :key="`${route.page}:${route.name}`" :kind="route.page as 'connections' | 'operations' | 'events'" :name="route.name" />
    </template>
  </div>
</template>
