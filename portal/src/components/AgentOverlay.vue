<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, watchEffect } from 'vue'
import { resizeSidePanel } from '@/lib/overlayResize'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, SquarePen, EyeOff, GripVertical, Hexagon, PanelRightClose, RefreshCw, ScanEye } from 'lucide-vue-next'
import ActionMenu from '@/portalkit/ActionMenu.vue'
import { usePageResourceStore } from '@/stores/pageResource'
import { dashboardAgentReloadDraftKey, useAgentOverlayStore } from '@/stores/agentOverlay'
import { useAuthStore } from '@/stores/auth'
import { useTenantStore } from '@/stores/tenant'
import { useProvidersStore } from '@/stores/providers'
import { useRouteContextStore } from '@/stores/routeContext'
import { useThemeStore } from '@/stores/theme'
import { useTerminalSessionsStore } from '@/stores/terminalSessions'
import { useLayoutInsets } from '@/composables/useLayoutInsets'
import { gettingStartedFocused } from '@/composables/gettingStartedFocus'
import { toastBottomOffsetPx } from '@/composables/useToastBottomOffset'
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import { authFetch, notifySessionExpired } from '@/auth/session'
import { createProviderContext, type ProviderContext } from '@/providers/providerContext'
import { pageContextForOverlay, pageResourceForScope, providerAgentContextForScope } from '@/providers/agentOverlayContext'
import { loadProviderScript } from '@/providers/providerScriptLoader'
import { resolveProviderBundle } from '@/providers/providerBundle'

interface OverlayElement extends HTMLElement {
  railgridContext: ProviderContext | null
  overlayState?: { agent: string; composerOnly?: boolean; hidden?: boolean; pageContext?: string }
}
const overlay = useAgentOverlayStore()
const pageResource = usePageResourceStore()
const contextScope = computed(() => JSON.stringify([overlay.preferenceKey, auth.clusterName, route.fullPath]))
const resourceHint = computed(() => pageResourceForScope(pageResource.snapshot, contextScope.value))
const providerContextHint = computed(() => providerAgentContextForScope(pageResource.agentContextSnapshot, contextScope.value))
const auth = useAuthStore()
const tenant = useTenantStore()
const providers = useProvidersStore()
const routeContext = useRouteContextStore()
const theme = useThemeStore()
const terminal = useTerminalSessionsStore()
const insets = useLayoutInsets()
const { scopePath } = useScopedNavigation()
const route = useRoute()
const router = useRouter()
const mount = ref<HTMLDivElement | null>(null)
const panel = ref<HTMLElement | null>(null)
const launcher = ref<HTMLButtonElement | null>(null)
const error = ref('')
const loading = ref(false)
const expanded = ref(false)
const dashboard = computed(() => route.name === 'dashboard')
let dashboardOrigin: HTMLElement | null = null
const retainDedicatedMount = ref(false)
const viewportWidth = ref(window.innerWidth)
const mobileLayout = computed(() => viewportWidth.value < 768)
const chatsVisible = ref(false)
const conversationTitle = ref('')
const preparingDashboardChat = ref(true)
function showChats(): void { element?.dispatchEvent(new Event('railgrid-overlay-show-chats')) }
function newChat(): void { element?.dispatchEvent(new Event('railgrid-overlay-new-chat')) }
const recoveryPending = ref(false)
const launcherLabel = computed(() => recoveryPending.value ? 'Return to assistant' : `Open ${overlay.selectedAgent} chat`)
const closing = ref(false)
let closingAnimation: Animation | null = null
function cancelClosingAnimation(): void {
  closingAnimation?.cancel()
  closingAnimation = null
  closing.value = false
  overlay.setSidePanelClosing(false)
}
async function collapse(): Promise<void> {
  if (closingAnimation || !expanded.value) return
  closing.value = true
  overlay.setSidePanelClosing(true)
  if (panel.value && !window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
    const animation = panel.value.animate([
      { transform: 'translateX(0)', opacity: 1 },
      { transform: 'translateX(32px)', opacity: 0 },
    ], { duration: 160, easing: 'cubic-bezier(.4, 0, 1, 1)', fill: 'forwards' })
    closingAnimation = animation
    try { await animation.finished } catch { return }
  }
  expanded.value = false
  // Suppress the scrollbar until the drawer is hidden and the host has reflowed.
  await nextTick()
  if (dashboard.value) {
    const origin = dashboardOrigin?.isConnected ? dashboardOrigin : document.getElementById('dashboard-agent-composer')
    if (origin) origin.focus()
    else launcher.value?.focus()
  }
  else launcher.value?.focus()
  cancelClosingAnimation()
}
function engage(): void { cancelClosingAnimation(); expanded.value = true }
function reloadAgentBundle(): void {
  // Agents registers its custom element once per document. Recover a failed
  // bootstrap in a fresh document, just as the dedicated provider page does.
  const composer = document.getElementById('dashboard-agent-composer') as HTMLTextAreaElement | null
  if (dashboard.value && overlay.preferenceKey && composer?.value) {
    try {
      sessionStorage.setItem(dashboardAgentReloadDraftKey(overlay.preferenceKey), composer.value)
    } catch { /* Session storage can be unavailable. */ }
  }
  window.location.reload()
}
let element: OverlayElement | null = null
let generation = 0

const entry = computed(() => providers.workspaceProvider('agents'))
const agentsBinding = computed(() => providers.bindingsByProvider.agents)
// Authority follows the bound copy. Bundle version and pin changes must
// leave the mounted conversation running until the next page load.
const authorityKey = computed(() => JSON.stringify([
  overlay.preferenceKey, auth.clusterName, entry.value?.scope, entry.value?.ownerOrg,
  entry.value?.export?.path, agentsBinding.value?.bindingName, agentsBinding.value?.exportName, agentsBinding.value?.exportCluster,
]))
const mountedAuthority = ref<string | null>(null)
const bindingsConfirmed = computed(() => providers.bindingsLoadState === 'ready' && !providers.bindingsStale)
// A refresh marks the current projection stale before setting loading, then
// clears stale before setting ready. Hold the existing element through both
// transitions and a transient read error, while the same binding still exists.
const holdBindings = computed(() => mountedAuthority.value === authorityKey.value && (
  providers.bindingsLoadState === 'loading' ||
  (providers.bindingsStale && (providers.bindingsLoadState === 'ready' || providers.bindingsLoadState === 'error'))
))
const allowed = computed(() => !!auth.token && !!auth.user && !!auth.clusterName &&
  tenant.activeWorkspaceUsable && routeContext.state === 'ready' &&
  !route.meta.public && !routeContext.blocksRoute(route.path, false) &&
  providers.loaded && !providers.error && providers.catalogOrgUUID === tenant.orgUUID &&
  !!entry.value?.ready && !!entry.value.serving?.ui &&
  providers.bindingsOrgUUID === tenant.orgUUID && providers.bindingsWorkspace === tenant.workspaceUUID &&
  providers.isEnabled('agents') && agentsBinding.value?.ready !== false && !agentsBinding.value?.terminating &&
  (bindingsConfirmed.value || holdBindings.value))
const bundleRevision = computed(() => JSON.stringify([entry.value?.version, entry.value?.serving?.ui?.mainJSIntegrity]))
const agentChat = computed(() => {
  if (route.params.name === 'agents') {
    const hashStart = route.fullPath.indexOf('#')
    if (hashStart === -1) return null
    const hash = route.fullPath.slice(hashStart + 1)
    const parts = hash.split('?')[0].split('/').filter(Boolean)
    const [section, agent, tab, item, action] = parts
    if ((section !== 'agents' && section !== 'agent') || !agent) return null
    // Match Agents' hash router, which falls back to chat for unknown tabs.
    if (['config', 'tools', 'automation', 'runs', 'skills', 'flow', 'wiring', 'settings'].includes(tab)) return null
    const automationEditor = (tab === 'schedules' || tab === 'triggers') &&
      ((parts.length === 4 && item === 'create') || (parts.length === 5 && action === 'edit'))
    if (automationEditor) return null
    try { return decodeURIComponent(agent) } catch { return agent }
  }
  return null
})
const chatPage = computed(() => {
  if (agentChat.value) return true
  if (route.params.name === 'app-studio') {
    const rest = route.params.rest
    const path = (Array.isArray(rest) ? rest.join('/') : rest ?? '').split('/').filter(Boolean).join('/')
    return !!path && path !== 'create/model' && path.split('/')[0] !== '~models'
  }
  return false
})
const enabled = computed(() => allowed.value && !overlay.preferencesLoading && !!overlay.selectedAgent)
const visible = computed(() => enabled.value && !gettingStartedFocused.value && (!chatPage.value || !mobileLayout.value))
const panelVisible = computed(() => visible.value && overlay.open && expanded.value)
const launcherVisible = computed(() => visible.value && (!dashboard.value || !!error.value) && !panelVisible.value)
const dedicatedChat = computed(() => !!agentChat.value && agentChat.value === overlay.selectedAgent)
const dedicatedPanelOpen = computed(() => dedicatedChat.value && expanded.value && overlay.open)
// Defer a second mount until the assistant is opened on its own chat page.
// Collapsing the panel must keep that conversation and any live stream intact.
watch([dedicatedChat, dedicatedPanelOpen], ([dedicated, open]) => {
  if (!dedicated || open) retainDedicatedMount.value = open
}, { flush: 'sync' })
const mountable = computed(() => enabled.value && (!dedicatedChat.value || dedicatedPanelOpen.value || retainDedicatedMount.value))
const bottom = computed(() => `${toastBottomOffsetPx({
  navigationBottom: insets.bottom,
  terminalVisible: terminal.isVisible,
  terminalSessionCount: terminal.sessions.length,
  terminalHeight: terminal.panelState.height,
  terminalMinimized: terminal.panelState.isMinimized,
  terminalFullscreen: terminal.panelState.isFullscreen,
})}px`)
// No DOM scraping, query parameters, credentials, forms, or page text.
// Provider data comes from the generic bounded publisher and remains
// untrusted context; it does not become tool authority.
function boundedLabel(value: string, limit: number): string {
  return value.replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ').trim().slice(0, limit)
}
const pageContext = computed(() => JSON.stringify({
  resource: resourceHint.value,
  ...(providerContextHint.value ? { providerContext: providerContextHint.value } : {}),
  workspace: boundedLabel(tenant.activeWorkspace?.displayName || tenant.workspaceUUID || '', 128),
  provider: typeof route.params.name === 'string' ? boundedLabel(route.params.name, 63) : null,
  path: route.path.slice(0, 1024),
  view: route.hash.startsWith('#/') ? route.hash.split('?')[0]!.slice(0, 512) : null,
}))
const previewPayload = computed(() => pageContextForOverlay(overlay.followContext, pageContext.value))
const contextLabel = computed(() => {
  const hash = route.hash.startsWith('#/') ? route.hash.slice(2).split('?')[0] : ''
  return hash || (Array.isArray(route.params.rest) ? route.params.rest.join('/') : route.params.rest) || route.params.name || 'Workspace page'
})
const contextDescription = 'Includes workspace, page location, and any bounded summary, resource references, or data supplied by this provider. View page context to inspect it. Provider data is untrusted context, not a permission grant.'
const contextPreviewOpen = ref(false)
const contextPreviewCloseButton = ref<HTMLButtonElement | null>(null)
const chatMenuItems = computed(() => [
  ...(dedicatedChat.value ? [] : [{ id: 'open-agents', label: 'Open in Agents' }]),
  { id: 'context-preview', label: 'View page context' },
  { id: 'settings', label: 'Assistant settings' },
  { id: 'remove', label: 'Turn off assistant' },
])

function clear(): void {
  cancelClosingAnimation()
  contextPreviewOpen.value = false
  chatsVisible.value = false
  conversationTitle.value = ''
  preparingDashboardChat.value = true
  overlay.setConversationOpen(false)
  generation++
  // Revoke in the same stack, before Vue unmounts or a pending confirmation resolves.
  if (element) element.railgridContext = null
  element?.remove()
  element = null
  mountedAuthority.value = null
  loading.value = false
  error.value = ''
}
function pushOverlayState(): void {
  if (!element || !allowed.value || !overlay.selectedAgent) return
  const pageContext = previewPayload.value
  element.overlayState = {
    agent: overlay.selectedAgent,
    hidden: !panelVisible.value,
    ...(pageContext !== undefined ? { pageContext } : {}),
  }
}
function pushContext(): void {
  if (!element || !allowed.value || !overlay.selectedAgent) return
  const owner = generation
  const key = authorityKey.value
  pushOverlayState()
  element.railgridContext = createProviderContext({
    user: auth.user, tenant: auth.clusterName, orgUUID: tenant.orgUUID, workspaceUUID: tenant.workspaceUUID,
    theme: theme.resolved, basePath: '/ui/providers/agents',
    navigationBasePath: '/ui' + scopePath('/providers/agents'),
  }, {
    providerName: 'agents',
    isCurrent: () => owner === generation && key === authorityKey.value && allowed.value,
    scope: () => ({ token: auth.token, orgUUID: tenant.orgUUID, workspaceUUID: tenant.workspaceUUID }),
    getToken: () => auth.getValidToken(),
    onUnauthorized: () => notifySessionExpired(),
  })
}
async function mountAgent(): Promise<void> {
  clear()
  if (!mountable.value || !entry.value || !mount.value) return
  const owner = generation
  const key = authorityKey.value
  const current = () => owner === generation && key === authorityKey.value && mountable.value
  loading.value = true
  try {
    const provider = entry.value
    const bundle = await resolveProviderBundle(provider, authFetch)
    if (!current()) return
    await loadProviderScript('agents', provider.version, document, undefined, {
      ...bundle, refreshIntegrity: () => providers.refreshWorkspaceMainJSIntegrity('agents'),
      acceptLoadedVersion: true,
    })
    if (!current() || !mount.value) return
    if (!customElements.get('railgrid-provider-agents')) throw new Error('The Agents interface did not load. Reload the page to try again.')
    const candidate = document.createElement('railgrid-provider-agents') as OverlayElement
    if (!('overlayState' in candidate)) throw new Error('This Agents version does not support overlay chat. Update the Agents provider and reload the page.')
    element = candidate
    pushContext()
    candidate.addEventListener('railgrid-overlay-view', event => {
      if (!current()) return
      const detail = (event as CustomEvent<{ chats: boolean; title: string; preparingDashboardChat?: boolean }>).detail
      preparingDashboardChat.value = detail.preparingDashboardChat === true
      chatsVisible.value = detail.chats === true
      conversationTitle.value = typeof detail.title === 'string' ? detail.title : ''
      overlay.setConversationOpen(conversationTitle.value !== '')
    })
    candidate.addEventListener('railgrid-overlay-expand', () => { if (current()) openDedicated() })
    candidate.addEventListener('railgrid-overlay-navigate', event => {
      if (!current()) return
      const detail = (event as CustomEvent<{ path?: string; recovery?: boolean }>).detail
      const path = detail?.path
      if (!path?.startsWith('#/')) return
      if (!detail?.recovery) element?.dispatchEvent(new Event('railgrid-chat-handoff'))
      recoveryPending.value = detail?.recovery === true
      overlay.closeForNavigation()
      void router.push(scopePath('/providers/agents') + path)
    })
    mountedAuthority.value = key
    mount.value.appendChild(candidate)
  } catch (cause) {
    if (current()) error.value = cause instanceof Error ? cause.message : 'Could not load agent chat.'
  } finally { if (current()) loading.value = false }
}
// Preserve the conversation across presentation changes. A dedicated chat
// reached through a handoff owns the conversation until the assistant opens.
watch([mountable, authorityKey, () => overlay.selectedAgent], () => {
  clear()
}, { flush: 'sync' })
watch([mountable, authorityKey, () => overlay.selectedAgent, mount], () => { void mountAgent() }, { flush: 'post' })
// Only an assistant that is not running is remounted for a new bundle.
watch(bundleRevision, () => { if (!element && !loading.value) void mountAgent() }, { flush: 'post' })
// Navigation and context sharing must not replace the provider's fetch wrapper
// or reset its API/store watchers while a conversation is streaming.
watch([panelVisible, () => overlay.followContext, pageContext], pushOverlayState, { flush: 'sync' })
watch(panelVisible, show => {
  if (!show) contextPreviewOpen.value = false
}, { immediate: true })
watch(launcherVisible, show => overlay.setChatLauncherVisible(show), { immediate: true })
watch([authorityKey, () => overlay.selectedAgent], () => {
  expanded.value = false
  retainDedicatedMount.value = false
  recoveryPending.value = false
})
watch([() => auth.token, () => theme.resolved], pushContext, { flush: 'sync' })

const boundDefaultAgent = computed(() => providers.bindingsByProvider.agents?.defaultAgent)
watch([allowed, authorityKey, () => overlay.personalAgent, boundDefaultAgent, () => overlay.preferencesLoading], ([canUse, key, preference, defaultAgent, loadingPreference], _, onCleanup) => {
  overlay.setDefaultAgent(null)
  overlay.defaultAgentLoading = false
  if (!canUse || loadingPreference || preference !== undefined || !defaultAgent) return
  const agentName = defaultAgent
  overlay.defaultAgentLoading = true
  const controller = new AbortController()
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  onCleanup(() => {
    controller.abort()
    if (retryTimer !== undefined) clearTimeout(retryTimer)
  })
  const cluster = encodeURIComponent(auth.clusterName!)
  const resource = `/clusters/${cluster}/apis/agents.railgrid.ai/v1alpha1/agents/${encodeURIComponent(agentName)}`
  const current = () => !controller.signal.aborted && key === authorityKey.value && allowed.value

  async function discover(retries: number) {
    try {
      const response = await authFetch(resource, { signal: controller.signal })
      if (!current()) return
      if (response.status === 404 && retries > 0 && providers.bindingsByProvider.agents?.defaultAgentPending) {
        retryTimer = setTimeout(() => { void discover(retries - 1) }, 2000)
        return
      }
      if (response.ok) {
        const agent = await response.json()
        if (current() && agent.metadata?.annotations?.['agents.railgrid.ai/workspace-default'] === 'true' &&
          !agent.metadata?.deletionTimestamp && agent.spec?.suspend !== true) {
          overlay.setDefaultAgent(agentName)
        }
      }
    } catch { /* Manual setup remains available after a failed discovery. */ }
    if (current()) overlay.defaultAgentLoading = false
  }
  // A newly Bound Agents export can precede creation of the starter agent.
  void discover(10)
}, { immediate: true, flush: 'sync' })

function openFromDashboard(prompt: string, origin: HTMLElement | null): boolean {
  if (!dashboard.value || !visible.value || !element) return false
  if (prompt && element.dispatchEvent(new CustomEvent('railgrid-overlay-dashboard-send', { cancelable: true, detail: { prompt } }))) {
    return false
  }
  dashboardOrigin = origin
  engage()
  overlay.setMinimized(false)
  void nextTick(() => panel.value?.focus())
  return true
}

watchEffect(() => {
  overlay.dashboardExpanded = dashboard.value && panelVisible.value
  overlay.dashboardPreparing = dashboard.value && visible.value && !error.value && (loading.value || preparingDashboardChat.value)
  overlay.dashboardSubmit = dashboard.value && visible.value && !overlay.dashboardPreparing && !error.value
    ? openFromDashboard : null
})


watch(() => overlay.open, open => {
  if (!open) {
    expanded.value = false
    cancelClosingAnimation()
  }
})
function returnToAssistant(): void {
  engage()
  if (recoveryPending.value) {
    recoveryPending.value = false
    element?.dispatchEvent(new Event('railgrid-overlay-resume'))
  }
  overlay.setMinimized(false)
  void nextTick(() => { if (panelVisible.value) panel.value?.focus() })
}

function assistantAction(id: string): void {
  if (id === 'open-agents') { openDedicated(); return }
  if (id === 'context-preview') {
    contextPreviewOpen.value = !contextPreviewOpen.value
    if (contextPreviewOpen.value) void nextTick(() => contextPreviewCloseButton.value?.focus())
    return
  }
  if (id === 'remove') { overlay.select(null); return }
  overlay.closeForNavigation({ settings: true })
  void router.push(scopePath('/providers/agents') + '#/models')
}
function closeContextPreview(): void {
  contextPreviewOpen.value = false
  void nextTick(() => panel.value?.focus())
}
function openDedicated(): void {
  element?.dispatchEvent(new Event('railgrid-chat-handoff'))
  overlay.closeForNavigation()
  retainDedicatedMount.value = false
  void router.push(scopePath('/providers/agents') + '#/agents/' + encodeURIComponent(overlay.selectedAgent || '') + '/chat')
}
const width = ref(440)
const navigationWidth = computed(() => insets.navigationWidth ?? 0)
const panelWidth = computed(() => resizeSidePanel(width.value, 0, viewportWidth.value, navigationWidth.value))
function updateViewport(): void { viewportWidth.value = window.innerWidth }
onMounted(() => window.addEventListener('resize', updateViewport))
watch([panelVisible, panelWidth, mobileLayout], () => {
  const showing = panelVisible.value
  overlay.setSidePanel(showing && !mobileLayout.value ? panelWidth.value : 0, showing && mobileLayout.value)
}, { immediate: true, flush: 'sync' })
const resizing = ref(false)
let stopResize: (() => void) | undefined
function resize(event: PointerEvent): void {
  if (event.button !== 0 || !panel.value) return
  stopResize?.()
  const handle = event.currentTarget as HTMLElement
  const pointerID = event.pointerId
  let x = event.clientX
  let pending = panel.value.offsetWidth
  let frame = 0
  width.value = pending
  resizing.value = true
  const flush = () => {
    frame = 0
    pending = resizeSidePanel(pending, 0, viewportWidth.value, navigationWidth.value)
    width.value = pending
  }
  const move = (e: PointerEvent) => {
    if (e.pointerId !== pointerID) return
    pending = resizeSidePanel(pending, e.clientX - x, viewportWidth.value, navigationWidth.value)
    x = e.clientX
    if (!frame) frame = requestAnimationFrame(flush)
  }
  const stop = () => {
    if (frame) cancelAnimationFrame(frame)
    flush()
    resizing.value = false
    handle.removeEventListener('pointermove', move)
    handle.removeEventListener('pointerup', finish)
    handle.removeEventListener('pointercancel', finish)
    handle.removeEventListener('lostpointercapture', finish)
    if (handle.hasPointerCapture(pointerID)) handle.releasePointerCapture(pointerID)
    stopResize = undefined
  }
  const finish = (e: PointerEvent) => {
    if (e.pointerId !== pointerID) return
    if (e.type === 'pointerup') move(e)
    stop()
  }
  stopResize = stop
  handle.addEventListener('pointermove', move)
  handle.addEventListener('pointerup', finish)
  handle.addEventListener('pointercancel', finish)
  handle.addEventListener('lostpointercapture', finish)
  handle.setPointerCapture(pointerID)
}
function resizeWithKeys(event: KeyboardEvent): void {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const delta = event.key === 'Home' ? 10000 : event.key === 'End' ? -10000 : event.key === 'ArrowLeft' ? -16 : 16
  width.value = resizeSidePanel(panelWidth.value, delta, viewportWidth.value, navigationWidth.value)
}
watch([expanded, () => overlay.open, visible], () => { stopResize?.() })
onBeforeUnmount(() => {
  stopResize?.()
  window.removeEventListener('resize', updateViewport)
  overlay.setChatLauncherVisible(false)
  overlay.dashboardSubmit = null
  overlay.dashboardExpanded = false
  overlay.dashboardPreparing = false
  overlay.setSidePanel(0)
  clear()
})
</script>

<template>
  <div v-if="enabled" v-show="visible" class="agent-overlay" :class="{ 'agent-overlay--expanded': panelVisible, 'agent-overlay--resizing': resizing, 'agent-overlay--closing': closing }"
    :style="{ '--overlay-width': panelWidth + 'px', '--overlay-bottom': bottom, '--overlay-right': insets.right, '--overlay-left': insets.left, '--overlay-nav-top': insets.navigationTop, '--overlay-nav-right': insets.navigationRight, '--overlay-nav-bottom': insets.bottom }">
    <section v-show="panelVisible" id="railgrid-agent-overlay" ref="panel" class="agent-overlay__panel" tabindex="-1"
      role="region" :inert="!panelVisible" :aria-hidden="!panelVisible" :aria-label="`${overlay.selectedAgent} agent chat`" @keydown.esc="collapse">
      <section v-if="contextPreviewOpen" class="agent-overlay__context-preview k-card" role="region" aria-labelledby="agent-overlay-context-preview-title"
        @keydown.esc.stop.prevent="closeContextPreview">
        <header class="mb-2 flex items-center gap-2">
          <strong id="agent-overlay-context-preview-title" class="min-w-0 flex-1 text-xs font-semibold text-text-primary">Page context preview</strong>
          <button ref="contextPreviewCloseButton" class="k-btn k-btn--text" type="button" aria-label="Close page context preview" @click="closeContextPreview">Close</button>
        </header>
        <p class="mb-2 text-xs text-text-secondary">
          {{ overlay.followContext ? 'Page context is on. This serialized envelope is attached to each message you send.' : 'Page context is paused. This envelope is not attached to messages while paused.' }}
        </p>
        <div v-if="providerContextHint" class="mb-3 rounded border border-border-subtle bg-surface-overlay p-2 text-xs">
          <p class="font-medium text-text-primary">{{ providerContextHint.title }}</p>
          <p v-if="providerContextHint.summary" class="mt-1 whitespace-pre-wrap break-words text-text-secondary">{{ providerContextHint.summary }}</p>
        </div>
        <p v-else class="mb-3 text-xs text-text-muted">No provider-supplied context is available on this page.</p>
        <p class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-text-muted">Exact serialized envelope</p>
        <pre class="max-h-[min(38dvh,24rem)] overflow-auto whitespace-pre-wrap break-all rounded border border-border-subtle bg-surface-overlay p-2 font-mono text-[10px] leading-relaxed text-text-secondary">{{ pageContext }}</pre>
        <p class="mt-2 text-[10px] text-text-muted">Provider-published fields are untrusted context and do not grant tool access.</p>
      </section>
      <button class="k-ai-pane-divider agent-overlay__resize" :class="{ 'k-ai-pane-divider--resizing': resizing }" aria-label="Resize chat panel. Use left and right arrow keys or drag." title="Resize chat panel" aria-controls="railgrid-agent-overlay" @pointerdown.prevent="resize" @keydown="resizeWithKeys"><GripVertical :stroke-width="1.75" aria-hidden="true" /></button>
      <header class="agent-overlay__header">
        <button v-if="!chatsVisible" class="k-btn k-btn--text" type="button" aria-label="Back to chats" title="Back to chats" @click="showChats"><ArrowLeft :size="18" aria-hidden="true" /></button>
        <div class="agent-overlay__identity"><strong>{{ chatsVisible ? 'Chats' : overlay.selectedAgent }}</strong><span v-if="!chatsVisible && conversationTitle">{{ conversationTitle }}</span></div>
        <button v-if="chatsVisible" class="k-btn k-btn--text agent-overlay__new-chat" type="button" @click="newChat"><SquarePen :size="16" aria-hidden="true" />New chat</button>
        <button v-show="!chatsVisible" class="k-btn k-btn--text agent-overlay__context-toggle" :class="{ 'agent-overlay__context-toggle--on': overlay.followContext }" type="button" aria-label="Include page context" :aria-pressed="overlay.followContext"
          :title="overlay.followContext ? `Page context on: ${contextLabel}. ${contextDescription} Click to pause.` : `Page context paused. Click to include. ${contextDescription}`"
          @click="overlay.setFollowContext(!overlay.followContext)">
          <ScanEye v-if="overlay.followContext" :size="16" aria-hidden="true" /><EyeOff v-else :size="16" aria-hidden="true" />
        </button>
        <ActionMenu v-if="!chatsVisible" label="Chat options" :items="chatMenuItems" :disabled="!panelVisible" @select="assistantAction" />
        <button class="k-btn k-btn--text" type="button" title="Close chat panel" aria-label="Close chat panel" @click="collapse"><PanelRightClose :size="16" aria-hidden="true" /></button>
      </header>
      <div v-if="loading" class="agent-overlay__state" role="status">Loading agent chat…</div>
      <div v-if="error" class="agent-overlay__state" role="alert"><p>{{ error }}</p><button class="k-btn k-btn--ghost" type="button" @click="reloadAgentBundle"><RefreshCw :size="14" aria-hidden="true" /> Reload page</button></div>
      <div ref="mount" class="agent-overlay__mount" v-show="!error && !loading" />
    </section>
    <button v-show="launcherVisible" ref="launcher" type="button" class="k-btn k-btn--primary agent-overlay__launcher"
      :aria-label="launcherLabel" :title="launcherLabel" :aria-expanded="panelVisible" aria-controls="railgrid-agent-overlay"
      @click="returnToAssistant"><Hexagon :size="22" :stroke-width="2" aria-hidden="true" /></button>
  </div>
</template>

<style scoped>
/* Keep a usable panel when a tall terminal leaves too little space above it. */
.agent-overlay { --overlay-safe-bottom: min(var(--overlay-bottom), max(16px, calc(100dvh - 252px))); position: fixed; left: var(--overlay-left); right: var(--overlay-right); pointer-events: none; bottom: var(--overlay-safe-bottom); z-index: calc(var(--k-layer-menu, 1000) - 5); }

.agent-overlay__launcher { position: absolute; right: 16px; bottom: 0; transition: opacity 140ms ease, transform 200ms ease; pointer-events: auto; width: 48px; height: 48px; padding: 0; border-radius: 50%; margin-left: auto; display: flex; }
.agent-overlay__panel { transform-origin: top left; pointer-events: auto; display: flex; flex-direction: column; position: relative; width: 100%; height: 100%; background: var(--color-surface-raised); color: var(--color-text-primary); border-left: 1px solid var(--color-border-default); }
.agent-overlay__context-preview { position: absolute; top: 8px; right: 8px; z-index: 2; pointer-events: auto; width: min(520px, calc(100% - 16px)); max-height: min(60dvh, 34rem, calc(100% - 16px)); overflow: auto; padding: 12px; }
.agent-overlay__header { display: flex; align-items: center; gap: 4px; padding: 4px 8px; border-bottom: 1px solid var(--color-border-default); flex-shrink: 0; }
.agent-overlay__identity { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.agent-overlay__identity span { font-size: 11px; color: var(--color-text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.agent-overlay__header > .agent-overlay__new-chat { width: auto; height: 32px; min-height: 32px; padding: 0 10px; gap: 6px; white-space: nowrap; color: var(--color-accent); font-size: 12px; font-weight: 500; }
.agent-overlay__header strong { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; font-size: 13px; }
.agent-overlay__header > button { width: 28px; height: 28px; min-height: 28px; padding: 0; }
.agent-overlay__context-toggle { color: var(--color-text-secondary); }
.agent-overlay__context-toggle--on { color: var(--color-accent); background: var(--color-accent-subtle); }
@media (pointer: coarse) {
  .agent-overlay__header > button { width: 44px; height: 44px; }
}
.agent-overlay__mount { flex: 1; min-height: 0; min-width: 0; }
.agent-overlay__state { padding: 16px; font-size: 13px; }
.agent-overlay__resize { position: absolute; top: 0; left: 0; width: 6px; height: 100%; touch-action: none; border: 0; padding: 0; z-index: 3; }
/* The panel is a sibling of the routed shell, never a provider overlay. */
.agent-overlay--expanded { left: auto; right: var(--overlay-nav-right, 0px); top: var(--overlay-nav-top, 0px); bottom: var(--overlay-nav-bottom, 0px); width: var(--overlay-width); overflow: clip; }
.agent-overlay--expanded .agent-overlay__panel { animation: agent-side-panel-enter 240ms cubic-bezier(.16, 1, .3, 1); }
.agent-overlay--expanded .agent-overlay__header { min-height: 52px; padding: 8px 12px; }
@keyframes agent-side-panel-enter { from { transform: translateX(32px); opacity: 0; } to { transform: translateX(0); opacity: 1; } }
@media (max-width: 767px) { .agent-overlay--expanded { left: var(--overlay-left, 0px); width: auto; } .agent-overlay__resize { display: none; } }
/* Native overlay scrollbars can outlive an opacity animation. Suppress their
   chrome only during dismissal, retaining scroll positions and gutter space. */
.agent-overlay--closing .agent-overlay__mount :deep(*) { scrollbar-color: transparent transparent; }
.agent-overlay--closing .agent-overlay__mount :deep(*::-webkit-scrollbar-thumb),
.agent-overlay--closing .agent-overlay__mount :deep(*::-webkit-scrollbar-track) { background: transparent; }
.agent-overlay--resizing .agent-overlay__panel { transition: none; user-select: none; }
@media (prefers-reduced-motion: reduce) { .agent-overlay--expanded .agent-overlay__panel, .agent-overlay__panel, .agent-overlay__launcher { transition: none; animation: none; } }
@media (max-width: 640px) { .agent-overlay__launcher { right: 8px; } }
</style>
