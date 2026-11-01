<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { resizeSidePanel } from '@/lib/overlayResize'
import { useRoute, useRouter } from 'vue-router'
import { ChevronUp, EyeOff, GripVertical, Hexagon, Minus, PanelRightClose, RefreshCw, ScanEye } from 'lucide-vue-next'
import ActionMenu from '@/portalkit/ActionMenu.vue'
import { usePageResourceStore } from '@/stores/pageResource'
import { useAgentOverlayStore } from '@/stores/agentOverlay'
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
  overlayState?: { agent: string; composerOnly?: boolean; pageContext?: string }
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
const retry = ref(0)
const expanded = ref(false)
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
  // Keep the exit frame and scrollbar suppression until the compact provider
  // and host layout have both rendered; never expose the full-height scroller.
  await nextTick()
  cancelClosingAnimation()
  element?.dispatchEvent(new Event('railgrid-overlay-focus-composer'))
}
function engage(): void { cancelClosingAnimation(); expanded.value = true }
let element: OverlayElement | null = null
let generation = 0

const entry = computed(() => providers.byName('agents'))
const allowed = computed(() => !!auth.token && !!auth.user && !!auth.clusterName &&
  tenant.activeWorkspaceUsable && routeContext.state === 'ready' &&
  !route.meta.public && !routeContext.blocksRoute(route.path, false) &&
  providers.loaded && !providers.error && providers.catalogOrgUUID === tenant.orgUUID &&
  !!entry.value?.ready && !!entry.value.serving?.ui &&
  providers.bindingsLoadState === 'ready' && !providers.bindingsStale &&
  providers.bindingsOrgUUID === tenant.orgUUID && providers.bindingsWorkspace === tenant.workspaceUUID &&
  providers.isEnabled('agents') && !providers.bindingsByProvider.agents?.terminating)
const authorityKey = computed(() => JSON.stringify([
  overlay.preferenceKey, auth.clusterName, entry.value?.scope, entry.value?.ownerOrg,
]))
const dedicatedChat = computed(() => route.params.name === 'agents' && route.hash.startsWith('#/agents/' + encodeURIComponent(overlay.selectedAgent || '') + '/'))
const visible = computed(() => allowed.value && !!overlay.selectedAgent && !dedicatedChat.value)
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
const contextPreviewItems = [{ id: 'context-preview', label: 'View page context' }]
const chatMenuItems = [
  { id: 'open-agents', label: 'Open in Agents' },
  { id: 'hide', label: 'Hide assistant' },
  ...contextPreviewItems,
  { id: 'settings', label: 'Assistant settings' },
  { id: 'remove', label: 'Turn off assistant' },
]

function clear(): void {
  cancelClosingAnimation()
  contextPreviewOpen.value = false
  generation++
  // Revoke in the same stack, before Vue unmounts or a pending confirmation resolves.
  if (element) element.railgridContext = null
  element?.remove()
  element = null
  loading.value = false
  error.value = ''
}
function pushOverlayState(): void {
  if (!element || !allowed.value || !overlay.selectedAgent) return
  const pageContext = previewPayload.value
  element.overlayState = {
    agent: overlay.selectedAgent,
    composerOnly: !expanded.value,
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
  if (!visible.value || !entry.value || !mount.value) return
  const owner = generation
  const key = authorityKey.value
  const current = () => owner === generation && key === authorityKey.value && visible.value
  loading.value = true
  try {
    const provider = entry.value
    const bundle = await resolveProviderBundle(provider, authFetch)
    if (!current()) return
    await loadProviderScript('agents', provider.version, document, undefined, {
      ...bundle, refreshIntegrity: () => providers.refreshMainJSIntegrity('agents'),
    })
    if (!current() || !mount.value) return
    if (!customElements.get('railgrid-provider-agents')) throw new Error('The Agents interface did not load. Reload the page to try again.')
    const candidate = document.createElement('railgrid-provider-agents') as OverlayElement
    if (!('overlayState' in candidate)) throw new Error('This Agents version does not support overlay chat. Update the Agents provider and reload the page.')
    element = candidate
    pushContext()
    candidate.addEventListener('railgrid-overlay-engage', () => { if (current()) engage() })
    candidate.addEventListener('railgrid-overlay-expand', () => { if (current()) openDedicated() })
    candidate.addEventListener('railgrid-overlay-navigate', event => {
      if (!current()) return
      const path = (event as CustomEvent<{ path?: string }>).detail?.path
      if (!path?.startsWith('#/')) return
      element?.dispatchEvent(new Event('railgrid-chat-handoff'))
      overlay.closeForNavigation()
      void router.push(scopePath('/providers/agents') + path)
    })
    mount.value.appendChild(candidate)
  } catch (cause) {
    if (current()) error.value = cause instanceof Error ? cause.message : 'Could not load agent chat.'
  } finally { if (current()) loading.value = false }
}
// The persistent mount is intentionally independent of the current page and
// presentation. Only authority, selected agent, or explicit retry replaces it.
watch([allowed, dedicatedChat, authorityKey, () => overlay.selectedAgent], () => {
  clear()
}, { flush: 'sync' })
watch([visible, authorityKey, () => overlay.selectedAgent, mount, retry], () => { void mountAgent() }, { flush: 'post' })
// Navigation and context sharing must not replace the provider's fetch wrapper
// or reset its API/store watchers while a conversation is streaming.
watch([expanded, () => overlay.followContext, pageContext], pushOverlayState, { flush: 'sync' })
watch([visible, () => overlay.open], ([show, open]) => { overlay.setDockVisible(show && open) }, { immediate: true })
watch([authorityKey, () => overlay.selectedAgent], () => { expanded.value = false })
watch([() => auth.token, () => theme.resolved], pushContext, { flush: 'sync' })

watch(() => overlay.open, async open => {
  cancelClosingAnimation()
  if (open) {
    await nextTick()
    panel.value?.focus()
  }
})
function minimize(): void {
  overlay.setMinimized(true)
  void nextTick(() => launcher.value?.focus())
}
function assistantAction(id: string): void {
  if (id === 'open-agents') { openDedicated(); return }
  if (id === 'hide') { minimize(); return }
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
  void router.push(scopePath('/providers/agents') + '#/agents/' + encodeURIComponent(overlay.selectedAgent || '') + '/chat')
}
const viewportWidth = ref(window.innerWidth)
const width = ref(440)
const navigationWidth = computed(() => insets.navigationWidth ?? 0)
const panelWidth = computed(() => resizeSidePanel(width.value, 0, viewportWidth.value, navigationWidth.value))
function updateViewport(): void { viewportWidth.value = window.innerWidth }
onMounted(() => window.addEventListener('resize', updateViewport))
watch([visible, expanded, () => overlay.open, panelWidth, viewportWidth, gettingStartedFocused], () => {
  const showing = visible.value && expanded.value && overlay.open && !gettingStartedFocused.value
  overlay.setSidePanel(showing && viewportWidth.value >= 768 ? panelWidth.value : 0, showing && viewportWidth.value < 768)
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
  overlay.setDockVisible(false)
  overlay.setSidePanel(0)
  clear()
})
</script>

<template>
  <div v-if="visible" class="agent-overlay" :class="{ 'agent-overlay--ready': !expanded, 'agent-overlay--expanded': expanded && overlay.open, 'agent-overlay--minimized': !overlay.open, 'agent-overlay--resizing': resizing, 'agent-overlay--closing': closing }"
    :style="{ '--overlay-width': panelWidth + 'px', '--overlay-bottom': bottom, '--overlay-right': insets.right, '--overlay-left': insets.left, '--overlay-nav-top': insets.navigationTop, '--overlay-nav-right': insets.navigationRight, '--overlay-nav-bottom': insets.bottom }">
    <Transition name="agent-overlay-visibility">
    <section v-show="overlay.open" id="railgrid-agent-overlay" ref="panel" class="agent-overlay__panel" :class="{ 'agent-overlay__panel--context-preview': contextPreviewOpen }" tabindex="-1"
      role="region" :inert="!overlay.open" :aria-hidden="!overlay.open" :aria-label="`${overlay.selectedAgent} agent chat`" @keydown.esc="expanded ? collapse() : minimize()">
      <section v-if="contextPreviewOpen" class="agent-overlay__context-preview k-card" :class="{ 'agent-overlay__context-preview--compact': !expanded }" role="region" aria-labelledby="agent-overlay-context-preview-title"
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
      <button v-show="expanded" class="k-ai-pane-divider agent-overlay__resize" :class="{ 'k-ai-pane-divider--resizing': resizing }" aria-label="Resize chat panel. Use left and right arrow keys or drag." title="Resize chat panel" aria-controls="railgrid-agent-overlay" @pointerdown.prevent="resize" @keydown="resizeWithKeys"><GripVertical :stroke-width="1.75" aria-hidden="true" /></button>
      <header v-show="expanded" class="agent-overlay__header">
        <Hexagon :size="16" aria-hidden="true" /><strong>{{ overlay.selectedAgent }}</strong>
        <button class="k-btn k-btn--text agent-overlay__context-toggle" :class="{ 'agent-overlay__context-toggle--on': overlay.followContext }" type="button" aria-label="Include page context" :aria-pressed="overlay.followContext"
          :title="overlay.followContext ? `Page context on: ${contextLabel}. ${contextDescription} Click to pause.` : `Page context paused. Click to include. ${contextDescription}`"
          @click="overlay.setFollowContext(!overlay.followContext)">
          <ScanEye v-if="overlay.followContext" :size="16" aria-hidden="true" /><EyeOff v-else :size="16" aria-hidden="true" />
        </button>
        <ActionMenu label="Chat options" :items="chatMenuItems" @select="assistantAction" />
        <button class="k-btn k-btn--text" type="button" title="Close chat panel" aria-label="Close chat panel" @click="collapse"><PanelRightClose :size="16" aria-hidden="true" /></button>
      </header>
      <div v-if="loading" class="agent-overlay__state" role="status">Loading agent chat…</div>
      <div v-if="error" class="agent-overlay__state" role="alert"><p>{{ error }}</p><button class="k-btn k-btn--ghost" type="button" @click="retry++"><RefreshCw :size="14" aria-hidden="true" /> Retry</button></div>
      <Hexagon v-show="!expanded" class="agent-overlay__brand" :size="20" aria-hidden="true" />
      <div ref="mount" class="agent-overlay__mount" v-show="!error && !loading" />
      <div v-show="!expanded" class="agent-overlay__bar-actions">
        <button class="k-btn k-btn--text" type="button" aria-label="Include page context" :aria-pressed="overlay.followContext" :title="overlay.followContext ? `Page context on. ${contextDescription} Click to pause.` : `Page context paused. Click to include. ${contextDescription}`" :class="{ 'agent-overlay__context-toggle--on': overlay.followContext }" @click="overlay.setFollowContext(!overlay.followContext)"><ScanEye v-if="overlay.followContext" :size="16" aria-hidden="true" /><EyeOff v-else :size="16" aria-hidden="true" /></button>
        <ActionMenu label="Page context options" :items="contextPreviewItems" @select="assistantAction" />
        <button class="k-btn k-btn--text" type="button" aria-label="Expand conversation" title="Expand conversation" @click="engage"><ChevronUp :size="16" aria-hidden="true" /></button>
        <button class="k-btn k-btn--text" type="button" aria-label="Minimize chat" title="Minimize chat" @click="minimize"><Minus :size="16" aria-hidden="true" /></button>
      </div>
    </section>
    </Transition>
    <button :class="{ 'agent-overlay__launcher--hidden': overlay.open }" :aria-hidden="overlay.open" :tabindex="overlay.open ? -1 : 0" ref="launcher" type="button" class="k-btn k-btn--primary agent-overlay__launcher"
      :aria-label="`${overlay.open ? 'Minimize' : 'Open'} ${overlay.selectedAgent} chat`" :aria-expanded="overlay.open" aria-controls="railgrid-agent-overlay"
      @click="overlay.setMinimized(false)"><Hexagon :size="22" :stroke-width="2" aria-hidden="true" /></button>
  </div>
</template>

<style scoped>
/* Keep a usable panel when a tall terminal leaves too little space above it. */
.agent-overlay { --overlay-safe-bottom: min(var(--overlay-bottom), max(16px, calc(100dvh - 252px))); position: fixed; left: var(--overlay-left); right: var(--overlay-right); display: flex; justify-content: center; padding: 0 16px; pointer-events: none; bottom: var(--overlay-safe-bottom); z-index: calc(var(--k-layer-menu, 1000) - 5); }

.agent-overlay__launcher { position: absolute; right: 16px; bottom: 0; transition: opacity 140ms ease, transform 200ms ease; pointer-events: auto; width: 48px; height: 48px; padding: 0; border-radius: 50%; margin-left: auto; display: flex; }
.agent-overlay__launcher--hidden { visibility: hidden; opacity: 0; pointer-events: none; }
.agent-overlay__panel { transform-origin: top left; pointer-events: auto; display: flex; flex-direction: column; position: relative; width: min(720px, calc(100vw - var(--overlay-left) - var(--overlay-right) - 32px)); min-height: 64px; margin-bottom: 0; background: var(--color-surface-raised); color: var(--color-text-primary); border: 1px solid var(--color-border-default); border-radius: var(--radius-lg); overflow: hidden; }
.agent-overlay__panel--context-preview { overflow: visible; }
.agent-overlay__context-preview { position: absolute; top: 8px; right: 8px; z-index: 2; pointer-events: auto; width: min(520px, calc(100% - 16px)); max-height: min(60dvh, 34rem, calc(100% - 16px)); overflow: auto; padding: 12px; }
.agent-overlay__context-preview--compact { top: auto; bottom: calc(100% + 8px); max-height: min(60dvh, 34rem, max(0px, calc(100dvh - var(--overlay-safe-bottom) - 88px))); }
.agent-overlay__header { display: flex; align-items: center; gap: 4px; padding: 4px 8px; border-bottom: 1px solid var(--color-border-default); flex-shrink: 0; }
.agent-overlay__header strong { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; font-size: 13px; }
.agent-overlay__header > button, .agent-overlay__bar-actions > button { width: 28px; height: 28px; min-height: 28px; padding: 0; }
.agent-overlay__context-toggle { color: var(--color-text-secondary); }
.agent-overlay__context-toggle--on { color: var(--color-accent); background: var(--color-accent-subtle); }
@media (pointer: coarse) {
  .agent-overlay__header > button, .agent-overlay__bar-actions > button { width: 44px; height: 44px; }
}
.agent-overlay__mount { flex: 1; min-height: 0; min-width: 0; }
.agent-overlay__state { padding: 16px; font-size: 13px; }
.agent-overlay__resize { position: absolute; top: 0; left: 0; width: 6px; height: 100%; touch-action: none; border: 0; padding: 0; z-index: 3; }
@media (max-width: 640px) {
  .agent-overlay { left: 0; right: 0; padding: 0 8px; }

  .agent-overlay__panel { width: calc(100vw - 16px); }

}
.agent-overlay--ready .agent-overlay__panel { flex-direction: row; align-items: center; gap: 8px; padding: 8px 12px; height: 64px; min-height: 64px; }
.agent-overlay--ready .agent-overlay__mount { height: 100%; }
@media (pointer: coarse), (any-pointer: coarse) { .agent-overlay--ready .agent-overlay__panel { height: 80px; min-height: 80px; } }
.agent-overlay__brand { flex-shrink: 0; color: var(--color-accent); }
.agent-overlay__bar-actions { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
/* The panel is a sibling of the routed shell, never a provider overlay. */
.agent-overlay--expanded { left: auto; right: var(--overlay-nav-right, 0px); top: var(--overlay-nav-top, 0px); bottom: var(--overlay-nav-bottom, 0px); width: var(--overlay-width); padding: 0; overflow: clip; }
.agent-overlay--expanded .agent-overlay__panel { width: 100%; height: 100%; max-height: 100%; overflow: visible; border-radius: 0; border-width: 0 0 0 1px; animation: agent-side-panel-enter 240ms cubic-bezier(.16, 1, .3, 1); }
.agent-overlay--expanded .agent-overlay__header { min-height: 52px; padding: 8px 12px; }
@keyframes agent-side-panel-enter { from { transform: translateX(32px); opacity: 0; } to { transform: translateX(0); opacity: 1; } }
.agent-overlay-visibility-enter-active, .agent-overlay-visibility-leave-active { transition: opacity 160ms ease; }
.agent-overlay-visibility-enter-from, .agent-overlay-visibility-leave-to { opacity: 0; }
@media (max-width: 767px) { .agent-overlay--expanded { left: var(--overlay-left, 0px); width: auto; } .agent-overlay__resize { display: none; } }
/* Native overlay scrollbars can outlive an opacity animation. Suppress their
   chrome only during dismissal, retaining scroll positions and gutter space. */
.agent-overlay--closing .agent-overlay__mount :deep(*) { scrollbar-color: transparent transparent; }
.agent-overlay--closing .agent-overlay__mount :deep(*::-webkit-scrollbar-thumb),
.agent-overlay--closing .agent-overlay__mount :deep(*::-webkit-scrollbar-track) { background: transparent; }
.agent-overlay--resizing .agent-overlay__panel { transition: none; user-select: none; }
@media (prefers-reduced-motion: reduce) { .agent-overlay--expanded .agent-overlay__panel, .agent-overlay__panel, .agent-overlay__launcher { transition: none; animation: none; } }
@media (max-width: 640px) { .agent-overlay__launcher { right: 8px; } .agent-overlay--ready .agent-overlay__panel { gap: 4px; padding: 6px; } .agent-overlay__brand { width: 16px; } }
</style>
