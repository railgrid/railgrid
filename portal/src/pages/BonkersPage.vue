<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import {
  ShieldAlert, AlertCircle, RefreshCw, Puzzle, KeyRound, Building2, Users,
  Hexagon, ArrowLeft, LogOut, PanelLeftClose, PanelLeftOpen,
} from 'lucide-vue-next'

import { useSidebarExpansion } from '@/composables/useSidebarExpansion'
import { useAdminStore } from '@/stores/admin'
import { useAuthStore } from '@/stores/auth'

const admin = useAdminStore()
const auth = useAuthStore()
const router = useRouter()
const { sidebarExpanded, toggleSidebar } = useSidebarExpansion()

const sections = [
  { to: '/bonkers/providers', label: 'Providers', icon: Puzzle },
  { to: '/bonkers/identities', label: 'Root identities', icon: KeyRound },
  { to: '/bonkers/organizations', label: 'Organizations', icon: Building2 },
  { to: '/bonkers/users', label: 'Users', icon: Users },
]

onMounted(() => admin.refresh())

function handleLogout() {
  auth.logout()
  void router.replace('/login')
}
</script>

<template>
  <!-- Platform-wide data must not inherit tenant/workspace navigation, but its
       shell follows the same rail geometry and visual states as AppLayout. -->
  <div class="flex h-screen bg-surface text-text-primary">
    <!-- Admin sidebar -->
    <aside
      class="relative z-50 flex h-full flex-shrink-0 flex-col border-r border-border-default bg-surface-raised px-2 py-3 transition-[width] duration-200"
      :class="sidebarExpanded ? 'w-48' : 'w-14'"
    >
      <div class="mb-1 flex items-center gap-2 px-2" :class="sidebarExpanded ? '' : 'flex-col gap-1.5 px-0'">
        <div class="flex h-7 w-7 items-center justify-center rounded-lg border border-border-default bg-surface-overlay">
          <Hexagon class="h-3.5 w-3.5 text-accent" :stroke-width="2" />
        </div>
        <template v-if="sidebarExpanded">
          <span class="type-display text-[11px] font-bold tracking-[0.08em] text-text-primary">FAROS</span>
          <span class="k-badge k-badge--muted px-1.5 py-px text-[8px]">Admin</span>
        </template>
        <button
          type="button"
          class="k-btn k-btn--text flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-md p-0 text-text-muted transition-colors hover:bg-surface-overlay/50 hover:text-text-secondary"
          :class="sidebarExpanded ? 'ml-auto' : ''"
          :title="sidebarExpanded ? 'Collapse sidebar' : 'Expand sidebar'"
          @click="toggleSidebar"
        >
          <component :is="sidebarExpanded ? PanelLeftClose : PanelLeftOpen" class="h-3.5 w-3.5" :stroke-width="1.75" />
        </button>
      </div>

      <div class="mx-2 my-2 h-px bg-border-default/50" />
      <p v-if="sidebarExpanded" class="px-3 pb-1 text-[9px] font-semibold uppercase tracking-wider text-text-muted/70">Platform</p>

      <nav class="flex flex-col">
        <router-link
          v-for="s in sections"
          :key="s.to"
          :to="s.to"
          class="flex items-center gap-2.5 rounded-md px-3 py-2 text-[11px] font-medium transition-all duration-200"
          :class="[$route.path === s.to
            ? 'bg-accent/15 text-accent shadow-[0_0_14px_var(--color-accent-glow)]'
            : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary', sidebarExpanded ? '' : 'justify-center']"
          :aria-current="$route.path === s.to ? 'page' : undefined"
          :title="sidebarExpanded ? undefined : s.label"
        >
          <component :is="s.icon" class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
          <span v-if="sidebarExpanded">{{ s.label }}</span>
        </router-link>
      </nav>

      <div class="mt-auto">
        <div class="mx-2 my-2 h-px bg-border-default/50" />
        <router-link
          to="/"
          class="flex items-center gap-2.5 rounded-md px-3 py-2 text-[11px] font-medium text-text-muted transition-colors hover:bg-surface-overlay/50 hover:text-text-secondary"
          :class="sidebarExpanded ? '' : 'justify-center'"
          :title="sidebarExpanded ? undefined : 'Back to faros'"
        >
          <ArrowLeft class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
          <span v-if="sidebarExpanded">Back to faros</span>
        </router-link>
        <button
          type="button"
          class="k-btn k-btn--text flex w-full justify-start gap-2.5 rounded-md px-3 py-2 text-[11px] text-text-muted transition-colors hover:bg-surface-overlay/50 hover:text-text-secondary"
          :class="sidebarExpanded ? '' : 'justify-center'"
          :title="sidebarExpanded ? undefined : 'Log out'"
          @click="handleLogout"
        >
          <LogOut class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
          <span v-if="sidebarExpanded">Log out</span>
        </button>
      </div>
    </aside>

    <!-- Main content -->
    <main class="min-w-0 flex-1 overflow-y-auto">
      <div class="w-full px-8 py-5">
        <header class="mb-6 flex items-center justify-between">
          <h1 class="flex items-center gap-2 text-[17px] font-bold">
            <ShieldAlert class="h-5 w-5 text-accent" :stroke-width="1.75" />
            Platform admin
          </h1>
          <button
            type="button"
            class="k-btn k-btn--ghost inline-flex items-center gap-1.5 px-3 py-1.5 text-[12px]"
            :disabled="admin.loading"
            @click="admin.refresh()"
          >
            <RefreshCw class="h-4 w-4" :class="admin.loading ? 'animate-spin' : ''" :stroke-width="1.75" />
            Refresh
          </button>
        </header>

        <div
          v-if="admin.forbidden"
          class="k-card flex items-start gap-2 border-danger/30 bg-danger-subtle px-4 py-3 text-[13px] text-danger"
        >
          <AlertCircle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="1.75" />
          <span>Access denied. Your identity is not in the hub's <code>--admin-users</code> allowlist.</span>
        </div>

        <template v-else>
          <!-- Flat admin lists surface retry/stale state inside ResourceTable.
               Organizations remain grouped cards, so their read error stays at shell level. -->
          <p v-if="admin.error && $route.name === 'bonkers-organizations'" class="mb-3 text-[13px] text-danger">{{ admin.error }}</p>
          <router-view />
        </template>
      </div>
    </main>
  </div>
</template>
