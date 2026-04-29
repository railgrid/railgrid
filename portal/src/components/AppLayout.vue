<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { Hexagon, LayoutDashboard, Server, Layers, Bot, LogOut, Zap, Sun, Moon, Monitor, GripHorizontal, GripVertical, Pin } from 'lucide-vue-next'

const auth = useAuthStore()
const theme = useThemeStore()

const themeIcon = computed(() => {
  if (theme.mode === 'light') return Sun
  if (theme.mode === 'dark') return Moon
  return Monitor
})
const themeLabel = computed(() => {
  if (theme.mode === 'light') return 'Light'
  if (theme.mode === 'dark') return 'Dark'
  return 'System'
})
const route = useRoute()
const router = useRouter()

const now = ref(new Date())
let timer: ReturnType<typeof setInterval>

onMounted(() => {
  timer = setInterval(() => { now.value = new Date() }, 1000)
})
onUnmounted(() => clearInterval(timer))

const pad = (n: number) => String(n).padStart(2, '0')
const timeStr = computed(() =>
  `${pad(now.value.getHours())}:${pad(now.value.getMinutes())}:${pad(now.value.getSeconds())}`
)

const navItems = [
  { label: 'Dashboard', to: '/', icon: LayoutDashboard },
  { label: 'Edges', to: '/edges', icon: Server },
  { label: 'Workloads', to: '/workloads', icon: Layers },
  { label: 'MCP', to: '/mcp', icon: Bot },
]

function isActive(path: string) {
  if (path === '/') return route.path === '/'
  return route.path.startsWith(path)
}

function handleLogout() {
  auth.logout()
  router.push('/login')
}

// --- Draggable dock with edge-snap (all 4 edges) ---
type DockMode = 'float' | 'left' | 'right' | 'top' | 'bottom'
const DOCK_STORAGE_KEY = 'kedge-dock-state'
const SNAP_THRESHOLD = 80

const floatRef = ref<HTMLElement | null>(null)
const dockedRef = ref<HTMLElement | null>(null)
const isDragging = ref(false)
const nearEdge = ref<DockMode | null>(null)

interface DockState {
  mode: DockMode
  x: number
  y: number
}

function loadDockState(): DockState {
  try {
    const raw = localStorage.getItem(DOCK_STORAGE_KEY)
    if (!raw) return { mode: 'float', x: -1, y: -1 }
    const s = JSON.parse(raw) as DockState
    if (['left', 'right', 'top', 'bottom'].includes(s.mode)) return s
    if (s.x >= 0 && s.y >= 0 && s.x < window.innerWidth && s.y < window.innerHeight) return s
  } catch { /* ignore */ }
  return { mode: 'float', x: -1, y: -1 }
}

function saveDockState() {
  localStorage.setItem(DOCK_STORAGE_KEY, JSON.stringify(dockState.value))
}

const dockState = ref<DockState>(loadDockState())

const isDocked = computed(() => !isDragging.value && dockState.value.mode !== 'float')
const isVerticalDock = computed(() => isDocked.value && (dockState.value.mode === 'left' || dockState.value.mode === 'right'))
const isHorizontalDock = computed(() => isDocked.value && (dockState.value.mode === 'top' || dockState.value.mode === 'bottom'))
const showFloat = computed(() => !isDocked.value)

let dragOffset = { x: 0, y: 0 }
let dragSize = { w: 300, h: 48 }
const dragPos = ref<{ x: number; y: number }>({ x: 0, y: 0 })

function onDragStart(e: MouseEvent) {
  const el = dockedRef.value || floatRef.value
  if (!el) return

  const rect = el.getBoundingClientRect()
  dragOffset.x = e.clientX - rect.left
  dragOffset.y = e.clientY - rect.top

  isDragging.value = true

  nextTick(() => {
    const floatEl = floatRef.value
    if (floatEl) {
      dragSize.w = floatEl.offsetWidth
      dragSize.h = floatEl.offsetHeight
    }
  })

  dragPos.value = {
    x: Math.max(0, e.clientX - dragOffset.x),
    y: Math.max(0, e.clientY - dragOffset.y),
  }

  e.preventDefault()
}

function onDragMove(e: MouseEvent) {
  if (!isDragging.value) return

  const x = Math.max(0, Math.min(window.innerWidth - dragSize.w, e.clientX - dragOffset.x))
  const y = Math.max(0, Math.min(window.innerHeight - dragSize.h, e.clientY - dragOffset.y))
  dragPos.value = { x, y }

  // Detect closest edge
  const distL = e.clientX
  const distR = window.innerWidth - e.clientX
  const distT = e.clientY
  const distB = window.innerHeight - e.clientY
  const minDist = Math.min(distL, distR, distT, distB)

  if (minDist < SNAP_THRESHOLD) {
    if (minDist === distL) nearEdge.value = 'left'
    else if (minDist === distR) nearEdge.value = 'right'
    else if (minDist === distT) nearEdge.value = 'top'
    else nearEdge.value = 'bottom'
  } else {
    nearEdge.value = null
  }
}

function onDragEnd() {
  if (!isDragging.value) return

  if (nearEdge.value) {
    dockState.value = { mode: nearEdge.value, x: -1, y: -1 }
  } else {
    dockState.value = { mode: 'float', x: dragPos.value.x, y: dragPos.value.y }
  }

  isDragging.value = false
  nearEdge.value = null
  saveDockState()
}

function resetDockPos() {
  dockState.value = { mode: 'float', x: -1, y: -1 }
  localStorage.removeItem(DOCK_STORAGE_KEY)
}

onMounted(() => {
  window.addEventListener('mousemove', onDragMove)
  window.addEventListener('mouseup', onDragEnd)
})
onUnmounted(() => {
  window.removeEventListener('mousemove', onDragMove)
  window.removeEventListener('mouseup', onDragEnd)
})

const isDefaultFloat = computed(() => !isDragging.value && dockState.value.mode === 'float' && dockState.value.x < 0)
const hasCustomPos = computed(() => dockState.value.mode !== 'float' || dockState.value.x >= 0)

const floatStyle = computed(() => {
  if (isDragging.value) {
    return { left: `${dragPos.value.x}px`, top: `${dragPos.value.y}px` }
  }
  if (dockState.value.mode === 'float' && dockState.value.x >= 0) {
    return { left: `${dockState.value.x}px`, top: `${dockState.value.y}px` }
  }
  return {}
})

// Layout direction based on dock mode
const layoutClass = computed(() => {
  if (isVerticalDock.value) return 'flex-row'
  return 'flex-col'
})
</script>

<template>
  <div class="cross-grid relative flex h-screen bg-surface" :class="layoutClass">
    <!-- Ambient orbs -->
    <div class="pointer-events-none fixed inset-0 overflow-hidden">
      <div class="absolute -top-40 left-1/3 h-80 w-80 rounded-full bg-accent/4 blur-[160px]" />
      <div class="absolute bottom-1/3 right-1/4 h-64 w-64 rounded-full bg-success/3 blur-[140px]" />
      <div class="absolute top-1/2 left-3/4 h-48 w-48 rounded-full bg-accent/3 blur-[120px]" />
    </div>

    <!-- Edge snap hint overlays -->
    <Transition name="fade">
      <div v-if="nearEdge === 'left'" class="fixed inset-y-0 left-0 z-[60] w-48 rounded-r-xl bg-accent/10 border-r-2 border-accent/40" />
    </Transition>
    <Transition name="fade">
      <div v-if="nearEdge === 'right'" class="fixed inset-y-0 right-0 z-[60] w-48 rounded-l-xl bg-accent/10 border-l-2 border-accent/40" />
    </Transition>
    <Transition name="fade">
      <div v-if="nearEdge === 'top'" class="fixed inset-x-0 top-0 z-[60] h-11 rounded-b-xl bg-accent/10 border-b-2 border-accent/40" />
    </Transition>
    <Transition name="fade">
      <div v-if="nearEdge === 'bottom'" class="fixed inset-x-0 bottom-0 z-[60] h-11 rounded-t-xl bg-accent/10 border-t-2 border-accent/40" />
    </Transition>

    <!-- VERTICAL SIDEBAR (left or right) -->
    <aside
      v-if="isVerticalDock"
      ref="dockedRef"
      class="relative z-50 flex h-full w-48 flex-shrink-0 flex-col border-border-subtle bg-surface-raised/80 py-3 px-2 backdrop-blur-xl"
      :class="dockState.mode === 'left' ? 'order-first border-r' : 'order-last border-l'"
    >
      <!-- Drag handle + Logo -->
      <div class="flex items-center gap-2 px-2 mb-1">
        <div
          class="flex h-6 w-6 cursor-grab items-center justify-center rounded-lg text-text-muted/30 transition-colors hover:text-text-muted"
          @mousedown="onDragStart"
        >
          <GripVertical class="h-3 w-3" :stroke-width="2" />
        </div>
        <div class="relative flex h-7 w-7 items-center justify-center">
          <div class="absolute inset-0 rounded-lg bg-accent/20 blur-md" />
          <div class="relative flex h-7 w-7 items-center justify-center rounded-lg border border-accent/25 bg-surface-overlay/80">
            <Hexagon class="h-3.5 w-3.5 text-accent" :stroke-width="2.5" />
          </div>
        </div>
        <span class="text-[11px] font-bold tracking-tight text-text-primary">KEDGE</span>
        <div class="flex items-center gap-0.5 rounded-full border border-success/20 bg-success-subtle px-1.5 py-px">
          <Zap class="h-2 w-2 text-success" :stroke-width="2.5" fill="currentColor" />
          <span class="text-[7px] font-semibold uppercase tracking-widest text-success">Live</span>
        </div>
      </div>

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <!-- Nav items with labels -->
      <router-link
        v-for="item in navItems"
        :key="item.to"
        :to="item.to"
        class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-[11px] font-medium transition-all duration-200"
        :class="isActive(item.to) ? 'bg-accent/15 text-accent' : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
      >
        <component :is="item.icon" class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
        <span>{{ item.label }}</span>
      </router-link>

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <!-- Theme toggle -->
      <button
        class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-[11px] font-medium text-text-muted transition-all hover:bg-surface-overlay/50 hover:text-text-secondary"
        @click="theme.toggle()"
      >
        <component :is="themeIcon" class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
        <span>{{ themeLabel }}</span>
      </button>

      <div class="flex-1" />

      <!-- Status -->
      <div v-if="auth.user" class="flex items-center gap-2 px-3 py-1.5">
        <div class="h-1.5 w-1.5 rounded-full bg-success flex-shrink-0" />
        <span class="font-mono text-[9px] text-text-muted truncate">{{ auth.user.email }}</span>
      </div>
      <span class="px-3 font-mono text-[9px] tabular-nums text-text-muted/50">
        {{ timeStr }}
      </span>

      <div class="mx-2 my-2 h-px bg-border-default/50" />

      <!-- Undock -->
      <button
        class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-[11px] font-medium text-text-muted/40 transition-all hover:text-accent"
        @click="resetDockPos"
      >
        <Pin class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="2" />
        <span>Undock</span>
      </button>

      <!-- Logout -->
      <button
        class="flex items-center gap-2.5 rounded-xl px-3 py-2 text-[11px] font-medium text-text-muted transition-all hover:bg-danger-subtle hover:text-danger"
        @click="handleLogout"
      >
        <LogOut class="h-3.5 w-3.5 flex-shrink-0" :stroke-width="2" />
        <span>Logout</span>
      </button>
    </aside>

    <!-- HORIZONTAL BAR (top or bottom) -->
    <nav
      v-if="isHorizontalDock"
      ref="dockedRef"
      class="relative z-50 flex w-full flex-shrink-0 items-center gap-1.5 border-border-subtle bg-surface-raised/80 px-4 py-1.5 backdrop-blur-xl"
      :class="dockState.mode === 'top' ? 'order-first border-b' : 'order-last border-t'"
    >
      <!-- Drag handle -->
      <div
        class="flex h-7 w-5 cursor-grab items-center justify-center rounded-lg text-text-muted/30 transition-colors hover:text-text-muted"
        @mousedown="onDragStart"
      >
        <GripHorizontal class="h-3 w-3" :stroke-width="2" />
      </div>

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <!-- Logo -->
      <div class="flex items-center gap-1.5 px-1">
        <div class="relative flex h-6 w-6 items-center justify-center">
          <div class="absolute inset-0 rounded-md bg-accent/20 blur-md" />
          <div class="relative flex h-6 w-6 items-center justify-center rounded-md border border-accent/25 bg-surface-overlay/80">
            <Hexagon class="h-3 w-3 text-accent" :stroke-width="2.5" />
          </div>
        </div>
        <span class="text-[11px] font-bold tracking-tight text-text-primary">KEDGE</span>
        <div class="flex items-center gap-0.5 rounded-full border border-success/20 bg-success-subtle px-1.5 py-px">
          <Zap class="h-2 w-2 text-success" :stroke-width="2.5" fill="currentColor" />
          <span class="text-[8px] font-semibold uppercase tracking-widest text-success">Live</span>
        </div>
      </div>

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <!-- Nav items -->
      <router-link
        v-for="item in navItems"
        :key="item.to"
        :to="item.to"
        class="flex items-center gap-1.5 rounded-xl px-3 py-1.5 text-[11px] font-medium transition-all duration-200"
        :class="isActive(item.to) ? 'bg-accent/15 text-accent' : 'text-text-muted hover:text-text-secondary'"
      >
        <component :is="item.icon" class="h-3.5 w-3.5" :stroke-width="1.75" />
        <span v-if="isActive(item.to)">{{ item.label }}</span>
      </router-link>

      <div class="flex-1" />

      <!-- Status -->
      <span v-if="auth.clusterName" class="px-1 font-mono text-[9px] tracking-wider text-text-muted">
        {{ auth.clusterName }}
      </span>
      <button
        class="flex items-center gap-1 rounded-md border border-border-subtle px-1.5 py-1 text-text-muted transition-all hover:border-accent/30 hover:text-text-secondary"
        :title="`Theme: ${themeLabel}`"
        @click="theme.toggle()"
      >
        <component :is="themeIcon" class="h-3 w-3" :stroke-width="1.75" />
        <span class="text-[8px] font-semibold uppercase tracking-wider">{{ themeLabel }}</span>
      </button>
      <span class="px-0.5 font-mono text-[9px] tabular-nums tracking-wider text-text-muted/50">
        {{ timeStr }}
      </span>
      <div v-if="auth.user" class="flex items-center gap-1 rounded-full border border-border-subtle bg-surface-overlay/50 px-2 py-1 backdrop-blur">
        <div class="h-1.5 w-1.5 rounded-full bg-success" />
        <span class="font-mono text-[9px] text-text-muted">{{ auth.user.email }}</span>
      </div>

      <div class="mx-0.5 h-5 w-px bg-border-default/40" />

      <button
        class="flex h-7 w-7 items-center justify-center rounded-lg text-text-muted/50 transition-all hover:text-accent"
        title="Undock to floating bar"
        @click="resetDockPos"
      >
        <Pin class="h-3 w-3" :stroke-width="2" />
      </button>

      <button
        class="flex h-7 w-7 items-center justify-center rounded-lg text-text-muted transition-all hover:bg-danger-subtle hover:text-danger"
        title="Logout"
        @click="handleLogout"
      >
        <LogOut class="h-3 w-3" :stroke-width="2" />
      </button>
    </nav>

    <!-- Main content -->
    <main class="relative z-10 flex-1 overflow-y-auto px-8 py-5">
      <div class="dot-grid pointer-events-none absolute inset-0 opacity-40" />
      <div class="relative z-10">
        <slot />
      </div>
    </main>

    <!-- FLOATING MODE (also shown during drag) -->
    <div
      v-if="showFloat"
      ref="floatRef"
      class="fixed z-50 select-none"
      :class="{
        'bottom-4 left-1/2 -translate-x-1/2': isDefaultFloat,
        'cursor-grabbing': isDragging,
      }"
      :style="floatStyle"
    >
      <div class="island flex items-center gap-1 rounded-2xl px-2 py-1.5">
        <div
          class="island-nav flex h-8 w-5 cursor-grab items-center justify-center rounded-lg text-text-muted/30 transition-colors hover:text-text-muted"
          :class="{ 'cursor-grabbing': isDragging }"
          @mousedown="onDragStart"
        >
          <GripHorizontal class="h-3 w-3" :stroke-width="2" />
        </div>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <div class="flex items-center gap-1.5 px-1.5">
          <div class="relative flex h-6 w-6 items-center justify-center">
            <div class="absolute inset-0 rounded-md bg-accent/20 blur-md" />
            <div class="relative flex h-6 w-6 items-center justify-center rounded-md border border-accent/25 bg-surface-overlay/80 backdrop-blur">
              <Hexagon class="h-3 w-3 text-accent" :stroke-width="2.5" />
            </div>
          </div>
          <span class="text-[11px] font-bold tracking-tight text-text-primary">KEDGE</span>
          <div class="flex items-center gap-0.5 rounded-full border border-success/20 bg-success-subtle px-1.5 py-px">
            <Zap class="h-2 w-2 text-success" :stroke-width="2.5" fill="currentColor" />
            <span class="text-[8px] font-semibold uppercase tracking-widest text-success">Live</span>
          </div>
        </div>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <router-link
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="island-nav flex items-center gap-1.5 rounded-xl px-3 py-1.5 text-[11px] font-medium transition-all duration-200"
          :class="isActive(item.to) ? 'active bg-accent/15 text-accent' : 'text-text-muted hover:text-text-secondary'"
        >
          <component :is="item.icon" class="h-3.5 w-3.5" :stroke-width="1.75" />
          <span v-if="isActive(item.to)">{{ item.label }}</span>
        </router-link>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <span v-if="auth.clusterName" class="px-1 font-mono text-[9px] tracking-wider text-text-muted">
          {{ auth.clusterName }}
        </span>
        <button
          class="flex items-center gap-1 rounded-md border border-border-subtle px-1.5 py-1 text-text-muted transition-all hover:border-accent/30 hover:text-text-secondary"
          :title="`Theme: ${themeLabel}`"
          @click="theme.toggle()"
        >
          <component :is="themeIcon" class="h-3 w-3" :stroke-width="1.75" />
          <span class="text-[8px] font-semibold uppercase tracking-wider">{{ themeLabel }}</span>
        </button>
        <span class="px-0.5 font-mono text-[9px] tabular-nums tracking-wider text-text-muted/50">
          {{ timeStr }}
        </span>
        <div v-if="auth.user" class="flex items-center gap-1 rounded-full border border-border-subtle bg-surface-overlay/50 px-2 py-1 backdrop-blur">
          <div class="h-1.5 w-1.5 rounded-full bg-success" />
          <span class="font-mono text-[9px] text-text-muted">{{ auth.user.email }}</span>
        </div>

        <div class="mx-0.5 h-5 w-px bg-border-default/40" />

        <button
          v-if="hasCustomPos && !isDragging"
          class="island-nav flex h-7 w-7 items-center justify-center rounded-lg text-text-muted/50 transition-all duration-200 hover:text-accent"
          title="Reset to default position"
          @click="resetDockPos"
        >
          <Pin class="h-3 w-3" :stroke-width="2" />
        </button>

        <button
          class="island-nav flex h-7 w-7 items-center justify-center rounded-lg text-text-muted transition-all duration-200 hover:bg-danger-subtle hover:text-danger"
          title="Logout"
          @click="handleLogout"
        >
          <LogOut class="h-3 w-3" :stroke-width="2" />
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.15s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
