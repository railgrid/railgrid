<script setup lang="ts">
import { computed, markRaw, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { Bot, Cpu, Gauge, Plug, RefreshCw } from 'lucide-vue-next'
import Tabs, { type PortalTabItem } from './portalkit/Tabs.vue'
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import { resolveConfirm } from './portalkit/confirm'
import { ApiClient, type ContextAuthority } from './api'
import { AppStore } from './store'
import {
  DEFAULT_ROUTE,
  MENUS,
  activeMenu,
  hashFor,
  parseHash,
  syncHash,
  writeHash,
  type CreateSuccessDetail,
  type EditCancelDetail,
  type EditSuccessDetail,
  type MenuKey,
  type Route,
} from './router'
import { clearToasts } from './ui/toast'
import type { Agent, Connection, Credential, FarosContext, Toolset } from './types'
import { provideAgentsRuntime } from './vue/runtime'
import AgentsList from './views/AgentsList.vue'
import AgentCreate from './views/AgentCreate.vue'
import AgentDetail from './views/AgentDetail.vue'
import Activity from './views/Activity.vue'
import RunDetail from './views/RunDetail.vue'
import Connections from './views/Connections.vue'
import Toolsets from './views/Toolsets.vue'
import Models from './views/Models.vue'

const props = defineProps<{ ctx: FarosContext | null; host: HTMLElement }>()

const api = shallowRef(markRaw(new ApiClient()))
const store = shallowRef(markRaw(new AppStore(api.value)))
const route = ref<Route>(parseHash())
const authorityEpoch = ref(0)
const createSession = ref(0)
const storeRevision = ref(0)
const host = ref<HTMLElement | null>(props.host)
const root = ref<HTMLElement | null>(null)

let loadedTenant: string | null = null
let authority: ContextAuthority | null = null
let boundStore: AppStore | null = null
let focusCollectionAfterEdit: 'connections' | 'toolsets' | null = null
let focusRequest = 0

provideAgentsRuntime({ store, api, route, authorityEpoch, host })

const contextUsable = computed(() => {
  storeRevision.value
  return Boolean(authority?.usable)
})
const active = computed(() => activeMenu(route.value))
const menuCounts = computed<Record<MenuKey, number>>(() => {
  storeRevision.value
  return {
    agents: store.value.agents.data.length,
    activity: store.value.pendingInbox().length,
    connections: store.value.connections.data.length + store.value.toolsets.data.length,
    models: store.value.credentials.data.length,
  }
})
const live = computed(() => { storeRevision.value; return store.value.live })
const tabs = computed<PortalTabItem[]>(() => {
  const icons = { agents: Bot, activity: Gauge, connections: Plug, models: Cpu }
  const labels = { agents: 'Agents', activity: 'Activity', connections: 'Connections', models: 'Models' }
  return MENUS.map(menu => ({ id: menu, label: labels[menu], icon: icons[menu], count: menuCounts.value[menu] || undefined }))
})

const routeSurfaceKey = computed(() => {
  const current = route.value
  if (current.kind === 'agent') return `${authorityEpoch.value}:agent:${current.name}`
  if (current.kind === 'run') return `${authorityEpoch.value}:run:${current.id}`
  if (current.kind === 'create') return `${authorityEpoch.value}:create:${createSession.value}`
  if (current.kind === 'edit') return `${authorityEpoch.value}:edit:${current.name}`
  return `${authorityEpoch.value}:menu:${current.menu}`
})

function bindStore(next: AppStore): void {
  if (boundStore === next) return
  boundStore?.removeEventListener('change', onStoreChange)
  boundStore = next
  boundStore.addEventListener('change', onStoreChange)
  storeRevision.value += 1
}

function onStoreChange(): void { storeRevision.value += 1 }

function advanceCreateSession(previous: Route, next: Route): void {
  if (hashFor(previous) === hashFor(next)) return
  if (previous.kind === 'create' || next.kind === 'create') createSession.value += 1
}

function go(next: Route, mode?: 'push' | 'replace'): void {
  const historyMode = mode ?? (route.value.kind === 'create' && next.kind === 'create' ? 'replace' : 'push')
  advanceCreateSession(route.value, next)
  route.value = next
  // Let the host's Vue Router own browser history when this provider is
  // embedded. ProviderFrame acknowledges the event synchronously with
  // preventDefault(); the standalone portal falls back to its hash router.
  const navigation = new CustomEvent('faros-navigate', {
    detail: { path: hashFor(next), replace: historyMode === 'replace' },
    bubbles: true,
    composed: true,
    cancelable: true,
  })
  if (props.host.dispatchEvent(navigation)) writeHash(next, historyMode)
  if (!focusCollectionAfterEdit) scheduleRouteFocus(next)
}

function restoreRoute(): void {
  const next = parseHash()
  if (route.value.kind === 'edit' && next.kind === 'menu' && next.menu === 'connections') {
    focusCollectionAfterEdit = route.value.resource === 'toolset' ? 'toolsets' : 'connections'
  }
  advanceCreateSession(route.value, next)
  route.value = next
  syncHash(next)
  if (focusCollectionAfterEdit) scheduleCollectionFocus()
  else scheduleRouteFocus(next)
}

function scheduleCollectionFocus(): void {
  if (!focusCollectionAfterEdit || route.value.kind !== 'menu' || route.value.menu !== 'connections') return
  const target = focusCollectionAfterEdit
  focusCollectionAfterEdit = null
  const request = ++focusRequest
  void nextTick(() => requestAnimationFrame(() => {
    if (request === focusRequest) root.value?.querySelector<HTMLElement>(`[data-${target}-heading]`)?.focus()
  }))
}

function scheduleRouteFocus(next: Route): void {
  const request = ++focusRequest
  void nextTick(() => requestAnimationFrame(() => {
    if (request !== focusRequest || hashFor(route.value) !== hashFor(next)) return
    const target = root.value?.querySelector<HTMLElement>('.k-resource-page__title, .k-create-title, .agents-panel-head > h3, .agents-detail-title > h2')
    if (!target) return
    if (!target.hasAttribute('tabindex')) target.setAttribute('tabindex', '-1')
    target.focus()
  }))
}

function maybeLoad(): void {
  const currentAuthority = authority || api.value.contextAuthority()
  if (!currentAuthority.usable) return
  const key = currentAuthority.tenantKey
  if (key === loadedTenant) return
  if (loadedTenant !== null) {
    clearToasts()
    go(DEFAULT_ROUTE, 'replace')
  }
  loadedTenant = key
  store.value.retire()
  const nextStore = markRaw(new AppStore(api.value))
  store.value = nextStore
  bindStore(nextStore)
  nextStore.loadAll()
  nextStore.connect()
}

function shouldResetRoute(previous: ContextAuthority | null, next: ContextAuthority): boolean {
  // The host mounts the custom element before it can push farosContext. That
  // first null -> usable hydration must keep a dashboard/deep-link hash. A
  // real authority withdrawal already resets the route when it becomes
  // unusable, so restoring authority does not need to reset it a second time.
  if (!previous?.usable) return false
  if (previous.tenantKey !== next.tenantKey) return true
  return !(previous.userKey && next.userKey && previous.userKey === next.userKey)
}

function rotateContext(context: FarosContext | null, resetRoute: boolean): void {
  boundStore?.removeEventListener('change', onStoreChange)
  boundStore = null
  store.value.retire()
  authorityEpoch.value += 1
  createSession.value += 1
  resolveConfirm(false)
  clearToasts()

  const nextApi = markRaw(new ApiClient())
  nextApi.setContext(context)
  api.value = nextApi
  const nextStore = markRaw(new AppStore(nextApi))
  store.value = nextStore
  bindStore(nextStore)
  loadedTenant = null
  authority = nextApi.contextAuthority()
  if (resetRoute) go(DEFAULT_ROUTE, 'replace')
  maybeLoad()
}

function applyContext(context: FarosContext | null): void {
  const previous = authority
  const next = api.value.contextAuthority(context)
  const changed = previous !== null && (
    previous.usable !== next.usable ||
    previous.tenantKey !== next.tenantKey ||
    previous.userKey !== next.userKey ||
    previous.token !== next.token
  )

  if (!next.usable) {
    if (changed || loadedTenant !== null || store.value.live) rotateContext(context, true)
    else {
      api.value.setContext(context)
      authority = next
      storeRevision.value += 1
    }
    return
  }
  if (changed) {
    rotateContext(context, shouldResetRoute(previous, next))
    return
  }
  api.value.setContext(context)
  authority = next
  maybeLoad()
  storeRevision.value += 1
}

watch(() => props.ctx, applyContext, { immediate: true, flush: 'sync' })

type SourceDetail = Pick<CreateSuccessDetail, 'store' | 'authorityEpoch' | 'createSession'>

function sourceIsCurrent(detail: SourceDetail, needsCreateSession = false): boolean {
  if (detail.store !== store.value) return false
  if (detail.authorityEpoch !== authorityEpoch.value) return false
  if (needsCreateSession && detail.createSession !== createSession.value) return false
  return true
}

function createSuccessRoute(detail: CreateSuccessDetail): Route {
  switch (detail.resource) {
    case 'agent': return { kind: 'agent', name: detail.name || '', tab: 'config' }
    case 'model': return { kind: 'menu', menu: 'models' }
    case 'connection':
    case 'toolset': return { kind: 'menu', menu: 'connections' }
  }
}

function createOwnerRoute(current: Extract<Route, { kind: 'create' }>): Route {
  if (current.resource === 'model') return { kind: 'menu', menu: 'models' }
  if (current.resource === 'agent') return { kind: 'menu', menu: 'agents' }
  return { kind: 'menu', menu: 'connections' }
}

function adoptCreateResult(detail: CreateSuccessDetail): void {
  if (!detail.item) return
  if (detail.resource === 'agent') {
    const item = detail.item as Agent
    if (item.metadata?.name) store.value.adopt('agents', item)
  } else if (detail.resource === 'connection') {
    const item = detail.item as Connection
    if (item.metadata?.name) store.value.adopt('connections', item)
  } else if (detail.resource === 'toolset') {
    const item = detail.item as Toolset
    if (item.metadata?.name) store.value.adopt('toolsets', item)
  } else {
    const item = detail.item as Credential
    if (item.name) store.value.adopt('credentials', item)
  }
}

function onCreateSuccess(detail: CreateSuccessDetail): void {
  if (!sourceIsCurrent(detail, true) || route.value.kind !== 'create' || detail.resource !== route.value.resource) return
  adoptCreateResult(detail)
  go(detail.destination || createSuccessRoute(detail), 'replace')
}

function onCreateCancel(detail: Pick<CreateSuccessDetail, 'store' | 'authorityEpoch' | 'createSession'>): void {
  if (sourceIsCurrent(detail, true) && route.value.kind === 'create') go(createOwnerRoute(route.value), 'replace')
}

function onEditCancel(detail: EditCancelDetail): void {
  if (!sourceIsCurrent(detail) || route.value.kind !== 'edit' || detail.resource !== route.value.resource || detail.name !== route.value.name) return
  focusCollectionAfterEdit = detail.resource === 'toolset' ? 'toolsets' : 'connections'
  go({ kind: 'menu', menu: 'connections' }, 'replace')
  scheduleCollectionFocus()
}

function onEditSuccess(detail: EditSuccessDetail): void {
  if (!sourceIsCurrent(detail) || route.value.kind !== 'edit' || detail.resource !== route.value.resource || detail.name !== route.value.name) return
  if (detail.resource === 'connection') {
    const item = detail.item as Connection | undefined
    if (item?.metadata?.name) store.value.adopt('connections', item)
  } else {
    const item = detail.item as Toolset | undefined
    if (item?.metadata?.name) store.value.adopt('toolsets', item)
  }
  focusCollectionAfterEdit = detail.resource === 'toolset' ? 'toolsets' : 'connections'
  go({ kind: 'menu', menu: 'connections' }, 'replace')
  scheduleCollectionFocus()
}

function selectMenu(id: string): void {
  if ((MENUS as string[]).includes(id)) go({ kind: 'menu', menu: id as MenuKey })
}

onMounted(() => {
  bindStore(store.value)
  syncHash(route.value)
  window.addEventListener('hashchange', restoreRoute)
  window.addEventListener('popstate', restoreRoute)
  scheduleRouteFocus(route.value)
})

onBeforeUnmount(() => {
  window.removeEventListener('hashchange', restoreRoute)
  window.removeEventListener('popstate', restoreRoute)
  boundStore?.removeEventListener('change', onStoreChange)
  boundStore = null
  store.value.retire()
  resolveConfirm(false)
})

defineExpose({ api, store, route, authorityEpoch, createSession, applyContext })
</script>

<template>
  <div v-if="!ctx" class="k-card agents-empty k-loading-reveal"><p class="muted" role="status">Connecting…</p></div>
  <div v-else-if="!contextUsable" class="k-card agents-empty">
    <p class="muted" role="status">Select an organization and workspace in the sidebar to use your agents.</p>
  </div>
  <div v-else ref="root" class="agents-app">
    <div v-if="route.kind === 'menu'" class="agents-nav-wrap">
      <Tabs class="agents-nav" :tabs="tabs" :active="active" aria-label="Agents provider sections" @select="selectMenu" />
      <span v-if="!live" class="agents-offline" title="Live updates are reconnecting; falling back to polling.">
        <RefreshCw aria-hidden="true" /> reconnecting
      </span>
    </div>

    <div class="agents-view">
      <AgentsList
        v-if="route.kind === 'menu' && route.menu === 'agents'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        @navigate="go"
      />
      <Activity
        v-else-if="route.kind === 'menu' && route.menu === 'activity'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :authority-epoch="authorityEpoch"
        @navigate="go"
      />
      <Connections
        v-else-if="route.kind === 'menu' && route.menu === 'connections'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :route-owned="true"
        :authority-epoch="authorityEpoch"
        :create-session="createSession"
        @navigate="go"
        @create-success="onCreateSuccess"
        @create-cancel="onCreateCancel"
        @edit-success="onEditSuccess"
        @edit-cancel="onEditCancel"
      />
      <Models
        v-else-if="route.kind === 'menu' && route.menu === 'models'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :route-owned="true"
        :authority-epoch="authorityEpoch"
        :create-session="createSession"
        @navigate="go"
        @create-success="onCreateSuccess"
        @create-cancel="onCreateCancel"
      />
      <AgentDetail
        v-else-if="route.kind === 'agent'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :name="route.name"
        :tab="route.tab"
        :authority-epoch="authorityEpoch"
        @navigate="go"
      />
      <RunDetail
        v-else-if="route.kind === 'run'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :run-id="route.id"
        :authority-epoch="authorityEpoch"
        @navigate="go"
      />
      <AgentCreate
        v-else-if="route.kind === 'create' && route.resource === 'agent'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :authority-epoch="authorityEpoch"
        :create-session="createSession"
        @navigate="go"
        @create-success="onCreateSuccess"
        @create-cancel="onCreateCancel"
      />
      <Connections
        v-else-if="route.kind === 'create' && route.resource === 'connection'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :route-owned="true"
        :create-route="true"
        :create-type="route.type || ''"
        :authority-epoch="authorityEpoch"
        :create-session="createSession"
        @navigate="go"
        @create-success="onCreateSuccess"
        @create-cancel="onCreateCancel"
      />
      <Toolsets
        v-else-if="route.kind === 'create' && route.resource === 'toolset'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :route-owned="true"
        :create-route="true"
        :authority-epoch="authorityEpoch"
        :create-session="createSession"
        @navigate="go"
        @create-success="onCreateSuccess"
        @create-cancel="onCreateCancel"
      />
      <Models
        v-else-if="route.kind === 'create' && route.resource === 'model'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :route-owned="true"
        :create-route="true"
        :authority-epoch="authorityEpoch"
        :create-session="createSession"
        @navigate="go"
        @create-success="onCreateSuccess"
        @create-cancel="onCreateCancel"
      />
      <Connections
        v-else-if="route.kind === 'edit' && route.resource === 'connection'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :route-owned="true"
        :edit-route="true"
        :edit-name="route.name"
        :authority-epoch="authorityEpoch"
        :create-session="createSession"
        @navigate="go"
        @edit-success="onEditSuccess"
        @edit-cancel="onEditCancel"
      />
      <Toolsets
        v-else-if="route.kind === 'edit' && route.resource === 'toolset'"
        :key="routeSurfaceKey"
        :store="store"
        :api="api"
        :route-owned="true"
        :edit-route="true"
        :edit-name="route.name"
        :authority-epoch="authorityEpoch"
        :create-session="createSession"
        @navigate="go"
        @edit-success="onEditSuccess"
        @edit-cancel="onEditCancel"
      />
    </div>
    <ConfirmDialog />
  </div>
</template>
