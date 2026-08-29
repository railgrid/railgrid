<!--
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch, type CSSProperties } from 'vue'
import { useRoute } from 'vue-router'
import {
  Building2,
  ChevronDown,
  Code2,
  LogOut,
  Monitor,
  Moon,
  Pin,
  Plug,
  Settings,
  ShieldAlert,
  Sun,
  Terminal,
  UserRound,
} from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useTenantStore } from '@/stores/tenant'
import { useThemeStore, type ThemeMode } from '@/stores/theme'

interface Props {
  expanded?: boolean
  showPlatformAdmin?: boolean
  showUndock?: boolean
  undockLabel?: string
}

const props = withDefaults(defineProps<Props>(), {
  expanded: false,
  showPlatformAdmin: false,
  showUndock: false,
  undockLabel: 'Undock',
})

const emit = defineEmits<{
  cli: []
  undock: []
  logout: []
}>()

const auth = useAuthStore()
const tenant = useTenantStore()
const theme = useThemeStore()
const route = useRoute()

const isOpen = ref(false)
const triggerRef = ref<HTMLButtonElement | null>(null)
const panelRef = ref<HTMLElement | null>(null)
const positionReady = ref(false)
const position = ref({ top: 0, left: 0 })

let positionFrame: number | null = null
let disposed = false
let resizeObserver: ResizeObserver | null = null
let deferredTabClose: ReturnType<typeof setTimeout> | undefined
const panelId = useId()

const email = computed(() => auth.user?.email?.trim() || 'Authenticated user')
const identityLabel = computed(() => auth.user?.email?.trim() ? 'Email' : 'Account')
const mcpActive = computed(() => route.path === '/mcp' || route.path.startsWith('/mcp/'))
const settingsActive = computed(() => route.path === '/settings' || route.path.startsWith('/settings/'))
const adminActive = computed(() => route.path === '/bonkers' || route.path.startsWith('/bonkers/'))
const organizationsActive = computed(() => route.path === '/organizations' || route.path.startsWith('/organizations/'))
const contextRouteActive = computed(() => mcpActive.value || settingsActive.value || adminActive.value || organizationsActive.value)
const orgLabel = computed(() => {
  const displayName = tenant.activeOrg?.displayName?.trim()
  if (displayName) return displayName
  if (tenant.orgUUID) return `Organization ${tenant.orgUUID.slice(0, 8)}`
  return 'Choose organization'
})
const orgDetail = computed(() => {
  const org = tenant.activeOrg
  if (tenant.orgLoadState === 'error') return 'Authority context is unverified'
  if (!org && tenant.orgUUID && tenant.orgLoadState !== 'ready') return 'Loading authority context…'
  if (!org && tenant.orgUUID) return 'Organization is no longer available'
  if (!org) return 'No authority context selected'
  if (tenant.orgs.length > 1) return 'Switch organization'
  if (org.personal) return 'Personal organization'
  return org.role === 'admin' ? 'Organization admin' : 'Organization member'
})
const organizationDestination = computed(() => tenant.orgs.length > 1
  ? { path: '/organizations', query: { from: route.fullPath } }
  : { path: '/settings/organizations' },
)
const initials = computed(() => {
  const value = email.value === 'Authenticated user' ? '' : email.value
  const parts = value.split(/[@.\s_-]+/).filter(Boolean)
  if (parts.length >= 2) return `${parts[0][0]}${parts[1][0]}`.toUpperCase()
  return (parts[0]?.slice(0, 2) || '?').toUpperCase()
})

type DeveloperWorkspaceState = 'loading' | 'error' | 'organization' | 'pending' | 'ready'

const workspaceReadError = computed(() => tenant.orgUUID
  ? tenant.workspaceErrorByOrg[tenant.orgUUID] ?? null
  : null)
const developerWorkspaceState = computed<DeveloperWorkspaceState>(() => {
  if (!tenant.orgUUID) return 'organization'
  if (tenant.orgLoadState === 'error' || tenant.orgError || tenant.workspaceLoadState === 'error' || workspaceReadError.value) return 'error'
  if (
    tenant.orgLoadState !== 'ready' ||
    !tenant.workspaceSelectionHydrated ||
    tenant.workspaceLoadState !== 'ready' ||
    tenant.workspaceTransitioning
  ) return 'loading'
  if (!tenant.activeOrg) return 'error'
  if (tenant.workspaceMode === 'organization' || !tenant.workspaceUUID) return 'organization'
  if (!tenant.activeWorkspace) return 'error'
  if (!tenant.activeWorkspaceUsable) return 'pending'
  return 'ready'
})
const developerWorkspaceName = computed(() => {
  const workspace = tenant.activeWorkspace
  return workspace?.displayName?.trim() || workspace?.uuid || 'Workspace'
})
const developerAccessDisabledReason = computed<string | undefined>(() => {
  switch (developerWorkspaceState.value) {
    case 'ready': return undefined
    case 'loading': return 'Workspace context is loading. Wait for verification before opening developer access.'
    case 'error': return 'Workspace data could not be verified. Retry before opening developer access.'
    case 'organization': return 'Select a Workspace before opening developer access.'
    case 'pending': return `${developerWorkspaceName.value} will support developer access after its control plane is ready.`
  }
})
const developerAccessReady = computed(() => developerWorkspaceState.value === 'ready')

const appearanceOptions: Array<{
  mode: ThemeMode
  label: string
  icon: typeof Sun
}> = [
  { mode: 'light', label: 'Light', icon: Sun },
  { mode: 'dark', label: 'Dark', icon: Moon },
  { mode: 'system', label: 'System', icon: Monitor },
]

const popoverStyle = computed<CSSProperties>(() => ({
  top: `${position.value.top}px`,
  left: `${position.value.left}px`,
  visibility: positionReady.value ? 'visible' : 'hidden',
}))

function clamp(value: number, minimum: number, maximum: number) {
  const upperBound = Math.max(minimum, maximum)
  return Math.min(Math.max(value, minimum), upperBound)
}

function updatePosition() {
  if (!isOpen.value || disposed || typeof window === 'undefined') return

  const trigger = triggerRef.value
  const panel = panelRef.value
  if (!trigger || !panel) return

  const margin = 12
  const gap = 8
  const triggerRect = trigger.getBoundingClientRect()
  const panelRect = panel.getBoundingClientRect()
  const panelWidth = panelRect.width || Math.min(340, Math.max(0, window.innerWidth - margin * 2))
  const panelHeight = panelRect.height || Math.min(600, Math.max(0, window.innerHeight - margin * 2))
  const belowSpace = window.innerHeight - triggerRect.bottom - gap
  const aboveSpace = triggerRect.top - gap
  const placeAbove = belowSpace < panelHeight && aboveSpace > belowSpace

  const rawTop = placeAbove
    ? triggerRect.top - panelHeight - gap
    : triggerRect.bottom + gap
  const top = clamp(rawTop, margin, window.innerHeight - panelHeight - margin)

  // Prefer an end-aligned panel when there is room to the trigger's left;
  // otherwise align to its left edge. This keeps the panel attached to the
  // trigger while allowing it to open toward the roomier side of the viewport.
  const endAlignedLeft = triggerRect.right - panelWidth
  const startAlignedLeft = triggerRect.left
  const left = endAlignedLeft >= margin
    ? endAlignedLeft
    : startAlignedLeft + panelWidth <= window.innerWidth - margin
      ? startAlignedLeft
      : clamp(endAlignedLeft, margin, window.innerWidth - panelWidth - margin)

  position.value = { top, left }
  positionReady.value = true
}

function positionPopover() {
  if (!isOpen.value || typeof window === 'undefined') return

  if (positionFrame !== null) {
    window.cancelAnimationFrame(positionFrame)
    positionFrame = null
  }

  void nextTick(() => {
    if (!isOpen.value) return
    if (typeof window.requestAnimationFrame === 'function') {
      positionFrame = window.requestAnimationFrame(() => {
        positionFrame = null
        updatePosition()
      })
    } else {
      updatePosition()
    }
  })
}

function closeMenu(restoreFocus = false) {
  if (!isOpen.value) return
  isOpen.value = false
  if (deferredTabClose !== undefined) {
    clearTimeout(deferredTabClose)
    deferredTabClose = undefined
  }
  if (restoreFocus) {
    void nextTick(() => triggerRef.value?.focus())
  }
}

function openMenu() {
  if (isOpen.value) return
  isOpen.value = true
}

function toggleMenu() {
  if (isOpen.value) closeMenu()
  else openMenu()
}

function onPanelKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  event.preventDefault()
  event.stopPropagation()
  closeMenu(true)
}

function onDocumentKeydown(event: KeyboardEvent) {
  if (!isOpen.value) return
  if (event.key === 'Escape' && !panelRef.value?.contains(event.target as Node)) {
    event.preventDefault()
    closeMenu(true)
    return
  }
  if (event.key !== 'Tab' || deferredTabClose !== undefined) return
  // Let the browser move focus normally. The focusin handler closes as soon
  // as focus leaves the panel; this fallback covers environments where Tab
  // has no focus target and therefore emits no focusin event.
  deferredTabClose = setTimeout(() => {
    deferredTabClose = undefined
    if (isOpen.value && !panelRef.value?.contains(document.activeElement)) closeMenu()
  }, 0)
}

function observePanelResize() {
  resizeObserver?.disconnect()
  resizeObserver = null
  if (typeof ResizeObserver === 'undefined' || !panelRef.value) return
  resizeObserver = new ResizeObserver(() => {
    if (!isOpen.value || disposed) return
    positionPopover()
  })
  resizeObserver.observe(panelRef.value)
}

function onDocumentPointerdown(event: PointerEvent) {
  const target = event.target as Node | null
  if (!target) return
  if (triggerRef.value?.contains(target) || panelRef.value?.contains(target)) return
  closeMenu()
}

function onDocumentFocusin(event: FocusEvent) {
  const target = event.target as Node | null
  if (!target) return
  if (triggerRef.value?.contains(target) || panelRef.value?.contains(target)) return
  closeMenu()
}

function emitAndClose(eventName: 'cli' | 'undock' | 'logout') {
  if (eventName === 'cli') emit('cli')
  else if (eventName === 'undock') emit('undock')
  else emit('logout')
  // Every menu action has to leave focus somewhere deterministic. Restoring
  // the trigger also gives actions that open another surface (CLI setup) a
  // stable handoff target while that surface establishes its own focus.
  closeMenu(true)
}

function onRouteNavigation() {
  closeMenu(true)
}

watch(isOpen, async (open) => {
  if (open) {
    positionReady.value = false
    await nextTick()
    if (!isOpen.value || disposed) return
    updatePosition()
    await nextTick()
    if (!isOpen.value || disposed) return
    observePanelResize()
    // This is a non-modal popover dialog, not a menu. Focus the dialog itself
    // so assistive technology announces the surface; ordinary Tab order owns
    // movement between its links and buttons.
    panelRef.value?.focus()
    document.addEventListener('pointerdown', onDocumentPointerdown, true)
    document.addEventListener('keydown', onDocumentKeydown)
    document.addEventListener('focusin', onDocumentFocusin)
    window.addEventListener('resize', positionPopover, { passive: true })
    window.addEventListener('scroll', positionPopover, { capture: true, passive: true })
  } else {
    positionReady.value = false
    resizeObserver?.disconnect()
    resizeObserver = null
    if (typeof window !== 'undefined') {
      if (positionFrame !== null) {
        window.cancelAnimationFrame(positionFrame)
        positionFrame = null
      }
      window.removeEventListener('resize', positionPopover)
      window.removeEventListener('scroll', positionPopover, true)
    }
    document.removeEventListener('pointerdown', onDocumentPointerdown, true)
    document.removeEventListener('keydown', onDocumentKeydown)
    document.removeEventListener('focusin', onDocumentFocusin)
  }
})

watch(() => route.fullPath, onRouteNavigation)
watch(() => props.expanded, positionPopover)

onBeforeUnmount(() => {
  disposed = true
  if (typeof window !== 'undefined' && positionFrame !== null) {
    window.cancelAnimationFrame(positionFrame)
  }
  if (deferredTabClose !== undefined) clearTimeout(deferredTabClose)
  resizeObserver?.disconnect()
  resizeObserver = null
  document.removeEventListener('pointerdown', onDocumentPointerdown, true)
  document.removeEventListener('keydown', onDocumentKeydown)
  document.removeEventListener('focusin', onDocumentFocusin)
  if (typeof window !== 'undefined') {
    window.removeEventListener('resize', positionPopover)
    window.removeEventListener('scroll', positionPopover, true)
  }
})
</script>

<template>
  <button
    ref="triggerRef"
    type="button"
    :aria-label="`Account & access for ${email}`"
    :aria-controls="panelId"
    aria-haspopup="dialog"
    :aria-expanded="isOpen"
    class="account-trigger k-btn k-btn--ghost group flex min-w-0 items-center rounded-md text-left transition-colors hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
    :class="[
      expanded ? 'w-full gap-2 px-2.5 py-2' : 'h-8 w-8 justify-center p-0',
      contextRouteActive ? 'border-accent/30 text-accent' : '',
    ]"
    :title="expanded ? undefined : 'Account & access'"
    @click="toggleMenu"
  >
    <span class="k-avatar k-avatar--sm" aria-hidden="true">
      <UserRound v-if="initials === '?'" class="h-3 w-3" :stroke-width="1.75" />
      <span v-else>{{ initials }}</span>
    </span>

    <span v-if="expanded" class="min-w-0 flex-1">
      <span class="block truncate font-mono text-[10px] text-text-secondary group-hover:text-text-primary">{{ email }}</span>
      <span class="mt-0.5 block truncate text-[10px] text-text-secondary">{{ orgLabel }}</span>
    </span>
    <ChevronDown
      v-if="expanded"
      class="h-3.5 w-3.5 shrink-0 text-text-muted transition-transform"
      :class="isOpen ? 'rotate-180' : ''"
      :stroke-width="1.75"
      aria-hidden="true"
    />
  </button>

  <Teleport to="body">
    <div
      v-if="isOpen"
      :id="panelId"
      ref="panelRef"
      role="dialog"
      aria-label="Account and access"
      tabindex="-1"
      class="k-menu fixed z-[80] max-h-[calc(100vh-24px)] w-[340px] max-w-[calc(100vw-24px)] overflow-y-auto"
      :style="popoverStyle"
      @keydown="onPanelKeydown"
    >
      <div class="flex items-center gap-2 px-2 py-2">
        <span class="k-avatar k-avatar--sm" aria-hidden="true">
          <UserRound v-if="initials === '?'" class="h-3 w-3" :stroke-width="1.75" />
          <span v-else>{{ initials }}</span>
        </span>
        <span class="min-w-0 flex-1">
          <span class="block truncate font-mono text-[11px] text-text-primary">{{ email }}</span>
          <span class="mt-0.5 block text-[10px] text-text-secondary">{{ identityLabel }}</span>
        </span>
      </div>

      <router-link
        :to="organizationDestination"
        class="account-menu-item k-menu-item py-2"
        :class="organizationsActive ? 'is-selected' : ''"
        :aria-current="organizationsActive ? 'page' : undefined"
        @click="closeMenu(true)"
      >
        <span class="flex h-6 w-6 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
          <Building2 class="h-3.5 w-3.5 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        </span>
        <span class="min-w-0 flex-1">
          <span class="block truncate font-mono text-[11px] text-text-primary">{{ orgLabel }}</span>
          <span class="mt-0.5 block text-[10px] text-text-secondary">{{ orgDetail }}</span>
        </span>
        <ChevronDown class="h-3 w-3 -rotate-90 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
      </router-link>
      <div class="my-1 h-px bg-border-subtle" />

      <div class="px-2 py-1.5">
        <span class="k-eyebrow">Developer access</span>
      </div>
      <button
        type="button"
        class="account-menu-item k-menu-item"
        :disabled="!developerAccessReady"
        :title="developerAccessDisabledReason"
        @click="emitAndClose('cli')"
      >
        <Terminal class="h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">CLI setup</span>
        <Code2 class="h-3 w-3 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
      </button>
      <router-link
        v-if="developerAccessReady"
        to="/mcp"
        class="account-menu-item k-menu-item"
        :class="mcpActive ? 'is-selected' : ''"
        :aria-current="mcpActive ? 'page' : undefined"
        @click="closeMenu(true)"
      >
        <Plug class="h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">MCP Access</span>
        <ChevronDown class="h-3 w-3 -rotate-90 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
      </router-link>
      <button
        v-else
        type="button"
        class="account-menu-item k-menu-item"
        disabled
        :title="developerAccessDisabledReason"
      >
        <Plug class="h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">MCP Access</span>
        <ChevronDown class="h-3 w-3 -rotate-90 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
      </button>

      <div class="my-1 h-px bg-border-subtle" />

      <div class="px-2 py-1.5">
        <span class="k-eyebrow">Appearance</span>
      </div>
      <div class="grid grid-cols-3 gap-1 px-1">
        <button
          v-for="option in appearanceOptions"
          :key="option.mode"
          type="button"
          class="account-menu-item account-appearance-option k-menu-item justify-center px-2"
          :class="theme.mode === option.mode ? 'is-selected' : ''"
          :aria-pressed="theme.mode === option.mode"
          @click="theme.setMode(option.mode)"
        >
          <component :is="option.icon" class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          <span>{{ option.label }}</span>
        </button>
      </div>

      <div class="my-1 h-px bg-border-subtle" />

      <router-link
        to="/settings/workspaces"
        class="account-menu-item k-menu-item"
        :class="settingsActive ? 'is-selected' : ''"
        :aria-current="settingsActive ? 'page' : undefined"
        @click="closeMenu(true)"
      >
        <Settings class="h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">Settings</span>
        <ChevronDown class="h-3 w-3 -rotate-90 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
      </router-link>
      <router-link
        v-if="showPlatformAdmin"
        to="/bonkers"
        class="account-menu-item k-menu-item"
        :class="adminActive ? 'is-selected' : ''"
        :aria-current="adminActive ? 'page' : undefined"
        @click="closeMenu(true)"
      >
        <ShieldAlert class="h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">Platform admin</span>
        <ChevronDown class="h-3 w-3 -rotate-90 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
      </router-link>
      <button v-if="showUndock" type="button" class="account-menu-item k-menu-item" @click="emitAndClose('undock')">
        <Pin class="h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">{{ undockLabel }}</span>
      </button>

      <div class="k-menu-sep" />
      <button type="button" class="account-menu-item k-menu-item k-menu-item--danger" @click="emitAndClose('logout')">
        <LogOut class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">Logout</span>
      </button>
    </div>
  </Teleport>
</template>

<style scoped>
/* Compact menus keep the desktop rhythm, while touch users receive the same
   minimum hit area as the shell navigation. */
@media (pointer: coarse) {
  .account-trigger,
  .account-menu-item {
    min-height: 44px;
    min-width: 44px;
  }

  .account-appearance-option {
    min-height: 44px;
  }
}
</style>
