<script setup lang="ts">
import { onMounted } from 'vue'
import {
  ShieldAlert, AlertCircle, RefreshCw, Puzzle, KeyRound, Building2, Users,
  Hexagon, ArrowLeft, LogOut,
} from 'lucide-vue-next'

import { useAdminStore } from '@/stores/admin'
import { useAuthStore } from '@/stores/auth'

const admin = useAdminStore()
const auth = useAuthStore()

const sections = [
  { to: '/bonkers/providers', label: 'Providers', icon: Puzzle },
  { to: '/bonkers/identities', label: 'Root identities', icon: KeyRound },
  { to: '/bonkers/organizations', label: 'Organizations', icon: Building2 },
  { to: '/bonkers/users', label: 'Users', icon: Users },
]

onMounted(() => admin.refresh())
</script>

<template>
  <!-- Standalone admin shell — deliberately NOT wrapped in the tenant AppLayout,
       so the platform-admin area has its own chrome and is visually separate. -->
  <div class="flex h-screen bg-surface text-text-primary">
    <!-- Admin sidebar -->
    <aside class="flex h-full w-56 flex-shrink-0 flex-col border-r border-border-subtle bg-surface-raised/80 px-3 py-4 backdrop-blur-xl">
      <div class="mb-5 flex items-center gap-2 px-1">
        <div class="relative flex h-7 w-7 items-center justify-center rounded-lg border border-accent/25 bg-surface-overlay/80">
          <Hexagon class="h-3.5 w-3.5 text-accent" :stroke-width="2.5" />
        </div>
        <div class="flex flex-col leading-tight">
          <span class="text-sm font-semibold">Platform admin</span>
          <span class="text-[10px] uppercase tracking-wide text-text-muted">kedge</span>
        </div>
      </div>

      <nav class="space-y-1">
        <router-link
          v-for="s in sections"
          :key="s.to"
          :to="s.to"
          class="flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm font-medium transition-colors"
          :class="$route.path === s.to
            ? 'bg-accent/15 text-accent'
            : 'text-text-muted hover:bg-surface-overlay/50 hover:text-text-secondary'"
        >
          <component :is="s.icon" class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
          <span>{{ s.label }}</span>
        </router-link>
      </nav>

      <div class="mt-auto space-y-1 border-t border-border-subtle/60 pt-3">
        <router-link
          to="/"
          class="flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm font-medium text-text-muted transition-colors hover:bg-surface-overlay/50 hover:text-text-secondary"
        >
          <ArrowLeft class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
          <span>Back to kedge</span>
        </router-link>
        <button
          class="flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-sm font-medium text-text-muted transition-colors hover:bg-surface-overlay/50 hover:text-text-secondary"
          @click="auth.logout()"
        >
          <LogOut class="h-4 w-4 flex-shrink-0" :stroke-width="1.75" />
          <span>Log out</span>
        </button>
      </div>
    </aside>

    <!-- Main content -->
    <main class="min-w-0 flex-1 overflow-y-auto">
      <div class="mx-auto max-w-6xl px-8 py-6">
        <header class="mb-6 flex items-center justify-between">
          <h1 class="flex items-center gap-2 text-xl font-semibold">
            <ShieldAlert class="h-5 w-5 text-accent" :stroke-width="2" />
            Platform admin
          </h1>
          <button
            class="inline-flex items-center gap-1.5 rounded-lg border border-border-subtle px-3 py-1.5 text-sm text-text-primary hover:border-accent/40"
            :disabled="admin.loading"
            @click="admin.refresh()"
          >
            <RefreshCw class="h-4 w-4" :class="admin.loading ? 'animate-spin' : ''" :stroke-width="2" />
            Refresh
          </button>
        </header>

        <div
          v-if="admin.forbidden"
          class="rounded-lg border border-danger/30 bg-danger-subtle px-4 py-3 text-sm text-danger flex items-start gap-2"
        >
          <AlertCircle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="2" />
          <span>Access denied. Your identity is not in the hub's <code>--admin-users</code> allowlist.</span>
        </div>

        <template v-else>
          <p v-if="admin.error" class="mb-3 text-sm text-danger">{{ admin.error }}</p>
          <router-view />
        </template>
      </div>
    </main>
  </div>
</template>
