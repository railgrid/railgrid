<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, ArrowRight, ChevronDown, ChevronUp, EyeOff, Hexagon, Maximize2, Minus, RefreshCw, ScanEye } from 'lucide-vue-next'
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
import { toastBottomOffsetPx } from '@/composables/useToastBottomOffset'
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import { authFetch } from '@/auth/session'
import { createProviderContext, type ProviderContext } from '@/providers/providerContext'
import { loadProviderScript } from '@/providers/providerScriptLoader'
import { resolveProviderBundle } from '@/providers/providerBundle'

interface OverlayElement extends HTMLElement {
  railgridContext: ProviderContext | null
  overlayState?: { agent: string; composerOnly?: boolean; pageContext?: string }
}
const overlay = useAgentOverlayStore()
const pageResource = usePageResourceStore()
const resourceHint = computed(() => pageResource.snapshot?.scope === JSON.stringify([overlay.preferenceKey, auth.clusterName, route.fullPath]) ? pageResource.snapshot.resource : undefined)
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
const threadsOpen = ref(false)
const expanded = ref(false)
function collapse(): void { expanded.value = false; panel.value?.focus() }
function engage(): void { expanded.value = true }
function toggleThreads(): void { element?.dispatchEvent(new Event('railgrid-overlay-toggle-threads')) }
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
// Navigation identity only: no DOM scraping, query parameters, credentials,
// forms, or page text. Providers can publish richer curated summaries later.
const pageContext = computed(() => JSON.stringify({
  resource: resourceHint.value,
  workspace: tenant.activeWorkspace?.displayName || tenant.workspaceUUID,
  provider: typeof route.params.name === 'string' ? route.params.name : null,
  path: route.path.slice(0, 1024),
  view: route.hash.startsWith('#/') ? route.hash.split('?')[0]!.slice(0, 512) : null,
}))
const contextLabel = computed(() => {
  const hash = route.hash.startsWith('#/') ? route.hash.slice(2).split('?')[0] : ''
  return hash || (Array.isArray(route.params.rest) ? route.params.rest.join('/') : route.params.rest) || route.params.name || 'Workspace page'
})

function clear(): void {
  threadsOpen.value = false
  generation++
  // Revoke in the same stack, before Vue unmounts or a pending confirmation resolves.
  if (element) element.railgridContext = null
  element?.remove()
  element = null
  loading.value = false
  error.value = ''
}
function pushContext(): void {
  if (!element || !allowed.value || !overlay.selectedAgent) return
  const owner = generation
  const key = authorityKey.value
  element.overlayState = {
    agent: overlay.selectedAgent,
    composerOnly: !expanded.value,
    pageContext: overlay.followContext ? pageContext.value : undefined,
  }
  element.railgridContext = createProviderContext({
    user: auth.user, tenant: auth.clusterName, orgUUID: tenant.orgUUID, workspaceUUID: tenant.workspaceUUID,
    theme: theme.resolved, basePath: '/ui/providers/agents',
    navigationBasePath: '/ui' + scopePath('/providers/agents'),
  }, {
    providerName: 'agents',
    isCurrent: () => owner === generation && key === authorityKey.value && allowed.value,
    scope: () => ({ token: auth.token, orgUUID: tenant.orgUUID, workspaceUUID: tenant.workspaceUUID }),
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
    candidate.addEventListener('railgrid-overlay-threads-state', event => {
      if (current()) threadsOpen.value = !!(event as CustomEvent<{ open: boolean }>).detail?.open
    })
    candidate.addEventListener('railgrid-overlay-expand', () => { if (current()) openDedicated() })
    candidate.addEventListener('railgrid-overlay-navigate', event => {
      if (!current()) return
      const path = (event as CustomEvent<{ path?: string }>).detail?.path
      if (!path?.startsWith('#/')) return
      element?.dispatchEvent(new Event('railgrid-chat-handoff'))
      overlay.open = false
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
watch(expanded, () => { if (element) element.overlayState = { agent: overlay.selectedAgent!, composerOnly: !expanded.value, pageContext: overlay.followContext ? pageContext.value : undefined } })
watch([visible, () => overlay.open], ([show, open]) => { overlay.dockVisible = show && open }, { immediate: true })
watch(authorityKey, () => { expanded.value = false })
watch([() => auth.token, () => theme.resolved, () => overlay.followContext, pageContext], pushContext, { flush: 'sync' })

watch(() => overlay.open, async open => {
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
  if (id === 'remove') { overlay.select(null); return }
  overlay.open = false
  overlay.settingsOpen = true
  void router.push(scopePath('/providers/agents') + '#/models')
}
function openDedicated(): void {
  element?.dispatchEvent(new Event('railgrid-chat-handoff'))
  overlay.open = false
  void router.push(scopePath('/providers/agents') + '#/agents/' + encodeURIComponent(overlay.selectedAgent || '') + '/chat')
}
const size = ref({ width: 720, height: 560 })
function resize(event: PointerEvent): void {
  const handle = event.currentTarget as HTMLElement
  handle.setPointerCapture(event.pointerId)
  const start = { x: event.clientX, y: event.clientY, width: panel.value!.offsetWidth, height: panel.value!.offsetHeight }
  const move = (e: PointerEvent) => { size.value = { width: Math.max(320, start.width + 2 * (start.x - e.clientX)), height: Math.max(280, start.height + start.y - e.clientY) } }
  const stop = () => { handle.removeEventListener('pointermove', move); handle.removeEventListener('pointerup', stop); handle.removeEventListener('pointercancel', stop) }
  handle.addEventListener('pointermove', move)
  handle.addEventListener('pointerup', stop)
  handle.addEventListener('pointercancel', stop)
}
function resizeWithKeys(event: KeyboardEvent): void {
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return
  event.preventDefault()
  size.value = { width: Math.max(320, panel.value!.offsetWidth + (event.key === 'ArrowLeft' ? 24 : event.key === 'ArrowRight' ? -24 : 0)), height: Math.max(280, panel.value!.offsetHeight + (event.key === 'ArrowUp' ? 24 : event.key === 'ArrowDown' ? -24 : 0)) }
}
// Animate the existing panel rather than cloning or remounting the chat.
// The launcher keeps a measurable box even while visually hidden, so this
// follows the real destination across sidebar, terminal and mobile layouts.
let panelMotion: Animation | null = null
function cancelPanelMotion(): void {
  panelMotion?.cancel()
  panelMotion = null
}
function animatePanel(node: Element, done: () => void, opening: boolean): void {
  cancelPanelMotion()
  const surface = node as HTMLElement
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches || !surface.animate || !launcher.value) {
    done()
    return
  }
  const from = surface.getBoundingClientRect()
  const target = launcher.value.getBoundingClientRect()
  if (!from.width || !from.height) { done(); return }
  const buttonShape = {
    transform: `translate(${target.left - from.left}px, ${target.top - from.top}px) scale(${target.width / from.width}, ${target.height / from.height})`,
    borderRadius: '50%', opacity: 0,
  }
  const panelShape = { transform: 'translate(0, 0) scale(1, 1)', borderRadius: getComputedStyle(surface).borderRadius, opacity: 1 }
  const animation = surface.animate(opening ? [buttonShape, panelShape] : [panelShape, buttonShape], {
    duration: opening ? 320 : 240,
    easing: opening ? 'cubic-bezier(.16, 1, .3, 1)' : 'cubic-bezier(.4, 0, .6, 1)',
    fill: 'both',
  })
  panelMotion = animation
  void animation.finished.then(() => {
    if (panelMotion !== animation) return
    done()
    animation.cancel()
    panelMotion = null
  }).catch(() => { /* Vue owns the final visibility when interrupted. */ })
}
function enterPanel(node: Element, done: () => void): void { animatePanel(node, done, true) }
function leavePanel(node: Element, done: () => void): void { animatePanel(node, done, false) }
onBeforeUnmount(() => { overlay.dockVisible = false; cancelPanelMotion(); clear() })
</script>

<template>
  <div v-if="visible" class="agent-overlay" :class="{ 'agent-overlay--ready': !expanded, 'agent-overlay--minimized': !overlay.open }"
    :style="{ '--overlay-width': size.width + 'px', '--overlay-height': size.height + 'px', '--overlay-bottom': bottom, '--overlay-right': insets.right, '--overlay-left': insets.left }">
    <Transition :css="false" @enter="enterPanel" @leave="leavePanel" @enter-cancelled="cancelPanelMotion" @leave-cancelled="cancelPanelMotion">
    <section v-show="overlay.open" id="railgrid-agent-overlay" ref="panel" class="agent-overlay__panel" tabindex="-1"
      role="region" :inert="!overlay.open" :aria-hidden="!overlay.open" :aria-label="`${overlay.selectedAgent} agent chat`" @keydown.esc="expanded ? collapse() : minimize()">
      <button v-show="expanded" class="agent-overlay__resize" aria-label="Resize chat. Use arrow keys or drag." title="Resize chat" @pointerdown.prevent="resize" @keydown="resizeWithKeys" />
      <header v-show="expanded" class="agent-overlay__header">
        <button class="k-btn k-btn--text" type="button" :disabled="loading || !!error" :aria-label="threadsOpen ? 'Return to chat' : 'Back to threads'" :title="threadsOpen ? 'Return to chat' : 'Back to threads'" @click="toggleThreads"><ArrowRight v-if="threadsOpen" :size="16" aria-hidden="true" /><ArrowLeft v-else :size="16" aria-hidden="true" /></button>
        <Hexagon :size="16" aria-hidden="true" /><strong>{{ overlay.selectedAgent }}</strong>
        <button class="k-btn k-btn--text agent-overlay__context-toggle" :class="{ 'agent-overlay__context-toggle--on': overlay.followContext }" type="button" aria-label="Include page context" :aria-pressed="overlay.followContext"
          :title="overlay.followContext ? `Page context on: ${contextLabel}. Includes workspace, page location, and a resource reference when available. Click to pause.` : 'Page context paused. Click to include workspace, page location, and a resource reference when available.'"
          @click="overlay.setFollowContext(!overlay.followContext)">
          <ScanEye v-if="overlay.followContext" :size="16" aria-hidden="true" /><EyeOff v-else :size="16" aria-hidden="true" />
        </button>
        <button class="k-btn k-btn--text" type="button" aria-label="Open full chat in Agents" title="Open full chat in Agents" @click="openDedicated"><Maximize2 :size="16" aria-hidden="true" /></button>
        <ActionMenu label="Chat options" :items="[{ id: 'settings', label: 'Assistant settings' }, { id: 'remove', label: 'Turn off assistant' }]" @select="assistantAction" />
        <button class="k-btn k-btn--text" type="button" title="Collapse to assistant bar" aria-label="Collapse to assistant bar" @click="collapse"><ChevronDown :size="16" aria-hidden="true" /></button>
        <button class="k-btn k-btn--text" type="button" title="Minimize chat" aria-label="Minimize chat" @click="minimize"><Minus :size="16" aria-hidden="true" /></button>
      </header>
      <div v-if="loading" class="agent-overlay__state" role="status">Loading agent chat…</div>
      <div v-if="error" class="agent-overlay__state" role="alert"><p>{{ error }}</p><button class="k-btn k-btn--ghost" type="button" @click="retry++"><RefreshCw :size="14" aria-hidden="true" /> Retry</button></div>
      <Hexagon v-show="!expanded" class="agent-overlay__brand" :size="20" aria-hidden="true" />
      <div ref="mount" class="agent-overlay__mount" v-show="!error && !loading" />
      <div v-show="!expanded" class="agent-overlay__bar-actions">
        <button class="k-btn k-btn--text" type="button" aria-label="Include page context" :aria-pressed="overlay.followContext" :title="overlay.followContext ? 'Page context on. Click to pause.' : 'Page context paused. Click to include.'" :class="{ 'agent-overlay__context-toggle--on': overlay.followContext }" @click="overlay.setFollowContext(!overlay.followContext)"><ScanEye v-if="overlay.followContext" :size="16" aria-hidden="true" /><EyeOff v-else :size="16" aria-hidden="true" /></button>
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
.agent-overlay { position: fixed; left: var(--overlay-left); right: var(--overlay-right); display: flex; justify-content: center; padding: 0 16px; pointer-events: none; bottom: var(--overlay-bottom); z-index: calc(var(--k-layer-menu, 1000) - 5); }

.agent-overlay__launcher { position: absolute; right: 16px; bottom: 0; transition: opacity 140ms ease, transform 200ms ease; pointer-events: auto; width: 48px; height: 48px; padding: 0; border-radius: 50%; margin-left: auto; display: flex; }
.agent-overlay__launcher--hidden { visibility: hidden; opacity: 0; pointer-events: none; }
.agent-overlay__panel { transform-origin: top left; pointer-events: auto; display: flex; flex-direction: column; position: relative; width: min(var(--overlay-width), calc(100vw - var(--overlay-left) - var(--overlay-right) - 32px)); height: min(var(--overlay-height), calc(100dvh - var(--overlay-bottom) - 72px)); min-height: 180px; margin-bottom: 0; background: var(--color-surface-raised); color: var(--color-text-primary); border: 1px solid var(--color-border-default); border-radius: var(--radius-lg); overflow: hidden; }
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
.agent-overlay__resize { position: absolute; top: 0; left: 0; width: 16px; height: 16px; cursor: nwse-resize; touch-action: none; border: 0; background: linear-gradient(135deg, var(--color-text-secondary) 0 2px, transparent 2px 5px, var(--color-text-secondary) 5px 7px, transparent 7px); }
@media (max-width: 640px) {
  .agent-overlay { left: 0; right: 0; padding: 0 8px; }

  .agent-overlay__panel { width: calc(100vw - 16px); }

}
.agent-overlay--ready .agent-overlay__panel { flex-direction: row; align-items: center; gap: 8px; padding: 8px 12px; height: 64px; min-height: 64px; }
.agent-overlay--ready .agent-overlay__mount { height: 100%; }
.agent-overlay__brand { flex-shrink: 0; color: var(--color-accent); }
.agent-overlay__bar-actions { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
.agent-overlay__panel { transition: height 180ms cubic-bezier(.16, 1, .3, 1); }
@media (prefers-reduced-motion: reduce) { .agent-overlay__panel, .agent-overlay__launcher { transition: none; } }
@media (max-width: 640px) { .agent-overlay__launcher { right: 8px; } .agent-overlay--ready .agent-overlay__panel { gap: 4px; padding: 6px; } .agent-overlay__brand { width: 16px; } }
</style>
