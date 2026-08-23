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
  profile: []
  undock: []
  logout: []
}>()

const auth = useAuthStore()
const theme = useThemeStore()
const route = useRoute()

const isOpen = ref(false)
const triggerRef = ref<HTMLButtonElement | null>(null)
const panelRef = ref<HTMLElement | null>(null)
const positionReady = ref(false)
const position = ref({ top: 0, left: 0 })

let positionFrame: number | null = null
let disposed = false
const panelId = useId()

const email = computed(() => auth.user?.email?.trim() || 'Authenticated user')
const mcpActive = computed(() => route.path === '/mcp' || route.path.startsWith('/mcp/'))
const settingsActive = computed(() => route.path === '/tenant' || route.path.startsWith('/tenant/'))
const adminActive = computed(() => route.path === '/bonkers' || route.path.startsWith('/bonkers/'))
const contextRouteActive = computed(() => mcpActive.value || settingsActive.value || adminActive.value)
const initials = computed(() => {
  const value = email.value === 'Authenticated user' ? '' : email.value
  const parts = value.split(/[@.\s_-]+/).filter(Boolean)
  if (parts.length >= 2) return `${parts[0][0]}${parts[1][0]}`.toUpperCase()
  return (parts[0]?.slice(0, 2) || '?').toUpperCase()
})

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
  if (!isOpen.value || typeof window === 'undefined') return

  const trigger = triggerRef.value
  const panel = panelRef.value
  if (!trigger || !panel) return

  const margin = 12
  const gap = 8
  const triggerRect = trigger.getBoundingClientRect()
  const panelRect = panel.getBoundingClientRect()
  const panelWidth = panelRect.width || Math.min(320, Math.max(0, window.innerWidth - margin * 2))
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

function getFocusableItems() {
  const panel = panelRef.value
  if (!panel) return []
  return Array.from(panel.querySelectorAll<HTMLElement>('button:not([disabled]), a[href]'))
}

function focusFirstItem() {
  getFocusableItems()[0]?.focus()
}

function closeMenu(restoreFocus = false) {
  if (!isOpen.value) return
  isOpen.value = false
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
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    closeMenu(true)
  }
}

function onDocumentKeydown(event: KeyboardEvent) {
  if (isOpen.value && event.key === 'Escape' && !panelRef.value?.contains(event.target as Node)) {
    event.preventDefault()
    closeMenu(true)
  }
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

function emitAndClose(eventName: 'cli' | 'profile' | 'undock' | 'logout') {
  if (eventName === 'cli') emit('cli')
  else if (eventName === 'profile') emit('profile')
  else if (eventName === 'undock') emit('undock')
  else emit('logout')
  closeMenu()
}

function onRouteNavigation() {
  closeMenu()
}

watch(isOpen, async (open) => {
  if (open) {
    positionReady.value = false
    await nextTick()
    if (!isOpen.value || disposed) return
    updatePosition()
    await nextTick()
    if (!isOpen.value || disposed) return
    focusFirstItem()
    document.addEventListener('pointerdown', onDocumentPointerdown, true)
    document.addEventListener('keydown', onDocumentKeydown)
    document.addEventListener('focusin', onDocumentFocusin)
    window.addEventListener('resize', positionPopover, { passive: true })
    window.addEventListener('scroll', positionPopover, { capture: true, passive: true })
  } else {
    positionReady.value = false
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
    class="k-btn k-btn--ghost group flex min-w-0 items-center rounded-md text-left transition-colors hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
    :class="[
      expanded ? 'w-full gap-2 px-2.5 py-2' : 'h-8 w-8 justify-center p-0',
      contextRouteActive ? 'border-accent/30 text-accent' : '',
    ]"
    :title="expanded ? undefined : 'Account & access'"
    @click="toggleMenu"
  >
    <span v-if="expanded" class="relative flex h-5 w-5 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-raised text-text-secondary">
      <UserRound class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
      <span class="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-success" aria-hidden="true" />
    </span>
    <span v-else class="k-avatar k-avatar--sm" aria-hidden="true">
      <UserRound v-if="initials === '?'" class="h-3 w-3" :stroke-width="1.75" />
      <span v-else>{{ initials }}</span>
    </span>

    <span v-if="expanded" class="min-w-0 flex-1">
      <span class="block truncate font-mono text-[10px] text-text-secondary group-hover:text-text-primary">{{ email }}</span>
      <span class="mt-0.5 block text-[10px] text-text-muted">Account &amp; access</span>
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
      class="k-menu fixed z-[80] max-h-[calc(100vh-24px)] w-[320px] max-w-[calc(100vw-24px)] overflow-y-auto"
      :style="popoverStyle"
      @keydown="onPanelKeydown"
    >
      <div class="px-2 py-1.5">
        <span class="k-eyebrow">Identity</span>
      </div>
      <button type="button" class="k-menu-item" @click="emitAndClose('profile')">
        <span class="k-avatar k-avatar--sm" aria-hidden="true">
          <UserRound v-if="initials === '?'" class="h-3 w-3" :stroke-width="1.75" />
          <span v-else>{{ initials }}</span>
        </span>
        <span class="min-w-0 flex-1">
          <span class="block truncate font-mono text-[11px] text-text-primary">{{ email }}</span>
          <span class="mt-0.5 block text-[10px] text-text-muted">View profile</span>
        </span>
        <ChevronDown class="h-3 w-3 -rotate-90 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
      </button>
      <div class="my-1 h-px bg-border-subtle" />

      <div class="px-2 py-1.5">
        <span class="k-eyebrow">Developer access</span>
      </div>
      <button type="button" class="k-menu-item" @click="emitAndClose('cli')">
        <Terminal class="h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">CLI setup</span>
        <Code2 class="h-3 w-3 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
      </button>
      <router-link
        to="/mcp"
        class="k-menu-item"
        :class="mcpActive ? 'is-selected' : ''"
        :aria-current="mcpActive ? 'page' : undefined"
        @click="closeMenu()"
      >
        <Plug class="h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">MCP Access</span>
        <ChevronDown class="h-3 w-3 -rotate-90 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
      </router-link>

      <div class="my-1 h-px bg-border-subtle" />

      <div class="px-2 py-1.5">
        <span class="k-eyebrow">Appearance</span>
      </div>
      <div class="grid grid-cols-3 gap-1 px-1">
        <button
          v-for="option in appearanceOptions"
          :key="option.mode"
          type="button"
          class="k-menu-item justify-center px-2"
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
        to="/tenant"
        class="k-menu-item"
        :class="settingsActive ? 'is-selected' : ''"
        :aria-current="settingsActive ? 'page' : undefined"
        @click="closeMenu()"
      >
        <Settings class="h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">Settings</span>
        <ChevronDown class="h-3 w-3 -rotate-90 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
      </router-link>
      <router-link
        v-if="showPlatformAdmin"
        to="/bonkers"
        class="k-menu-item"
        :class="adminActive ? 'is-selected' : ''"
        :aria-current="adminActive ? 'page' : undefined"
        @click="closeMenu()"
      >
        <ShieldAlert class="h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">Platform admin</span>
        <ChevronDown class="h-3 w-3 -rotate-90 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
      </router-link>
      <button v-if="showUndock" type="button" class="k-menu-item" @click="emitAndClose('undock')">
        <Pin class="h-3.5 w-3.5 shrink-0 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">{{ undockLabel }}</span>
      </button>

      <div class="k-menu-sep" />
      <button type="button" class="k-menu-item k-menu-item--danger" @click="emitAndClose('logout')">
        <LogOut class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
        <span class="flex-1">Logout</span>
      </button>
    </div>
  </Teleport>
</template>
