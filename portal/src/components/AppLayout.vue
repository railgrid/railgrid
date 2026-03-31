<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { Hexagon, LayoutDashboard, Server, Layers, Bot, LogOut, Zap, Sun, Moon, Monitor } from 'lucide-vue-next'

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
</script>

<template>
  <div class="cross-grid relative flex h-screen flex-col bg-surface">
    <!-- Ambient orbs -->
    <div class="pointer-events-none fixed inset-0 overflow-hidden">
      <div class="absolute -top-40 left-1/3 h-80 w-80 rounded-full bg-accent/4 blur-[160px]" />
      <div class="absolute bottom-1/3 right-1/4 h-64 w-64 rounded-full bg-success/3 blur-[140px]" />
      <div class="absolute top-1/2 left-3/4 h-48 w-48 rounded-full bg-accent/3 blur-[120px]" />
    </div>

    <!-- Side vertical label -->
    <div class="pointer-events-none fixed left-4 top-1/2 z-20 -translate-y-1/2">
      <span class="float-label text-[9px] font-semibold uppercase text-text-muted/20">kedge command center</span>
    </div>

    <!-- Top bar: logo + clock + user -->
    <div class="relative z-10 flex items-center justify-between px-8 pt-5 pb-1">
      <div class="flex items-center gap-3">
        <div class="relative flex h-8 w-8 items-center justify-center">
          <div class="absolute inset-0 rounded-lg bg-accent/20 blur-md" />
          <div class="relative flex h-8 w-8 items-center justify-center rounded-lg border border-accent/25 bg-surface-overlay/80 backdrop-blur">
            <Hexagon class="h-4 w-4 text-accent" :stroke-width="2.5" />
          </div>
        </div>
        <div class="flex items-center gap-2">
          <span class="text-[13px] font-bold tracking-tight text-text-primary">KEDGE</span>
          <div class="flex items-center gap-1 rounded-full border border-success/20 bg-success-subtle px-2 py-0.5">
            <Zap class="h-2.5 w-2.5 text-success" :stroke-width="2.5" fill="currentColor" />
            <span class="text-[9px] font-semibold uppercase tracking-widest text-success">Live</span>
          </div>
        </div>
      </div>

      <div class="flex items-center gap-4">
        <span v-if="auth.clusterName" class="font-mono text-[10px] tracking-wider text-text-muted">
          {{ auth.clusterName }}
        </span>
        <button
          class="flex items-center gap-1.5 rounded-lg border border-border-subtle px-2 py-1 text-text-muted transition-all hover:border-accent/30 hover:text-text-secondary"
          :title="`Theme: ${themeLabel} — click to toggle`"
          @click="theme.toggle()"
        >
          <component :is="themeIcon" class="h-3 w-3" :stroke-width="1.75" />
          <span class="text-[9px] font-semibold uppercase tracking-wider">{{ themeLabel }}</span>
        </button>
        <span class="font-mono text-[10px] tabular-nums tracking-wider text-text-muted/60">
          {{ timeStr }}
        </span>
        <div v-if="auth.user" class="flex items-center gap-1.5 rounded-full border border-border-subtle bg-surface-overlay/50 px-2.5 py-1 backdrop-blur">
          <div class="h-1.5 w-1.5 rounded-full bg-success" />
          <span class="font-mono text-[10px] text-text-muted">{{ auth.user.email }}</span>
        </div>
      </div>
    </div>

    <!-- Energy separator -->
    <div class="relative z-10 mx-8">
      <div class="energy-line h-px" />
    </div>

    <!-- Main content -->
    <main class="relative z-10 flex-1 overflow-y-auto px-8 pb-24 pt-5">
      <div class="dot-grid pointer-events-none absolute inset-0 opacity-40" />
      <div class="relative z-10">
        <slot />
      </div>
    </main>

    <!-- Floating island dock -->
    <div class="fixed bottom-5 left-1/2 z-50 -translate-x-1/2">
      <div class="island flex items-center gap-1 rounded-2xl px-2 py-1.5">
        <router-link
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="island-nav flex items-center gap-2 rounded-xl px-3.5 py-2 text-[12px] font-medium transition-all duration-200"
          :class="
            isActive(item.to)
              ? 'active bg-accent/15 text-accent'
              : 'text-text-muted hover:text-text-secondary'
          "
        >
          <component :is="item.icon" class="h-4 w-4" :stroke-width="1.75" />
          <span v-if="isActive(item.to)">{{ item.label }}</span>
        </router-link>

        <div class="mx-1 h-6 w-px bg-border-default" />

        <button
          class="island-nav flex h-8 w-8 items-center justify-center rounded-xl text-text-muted transition-all duration-200 hover:bg-danger-subtle hover:text-danger"
          title="Logout"
          @click="handleLogout"
        >
          <LogOut class="h-3.5 w-3.5" :stroke-width="2" />
        </button>
      </div>
    </div>
  </div>
</template>
