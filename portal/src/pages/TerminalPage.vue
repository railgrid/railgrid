<script setup lang="ts">
import { ref, computed } from 'vue'
import AppLayout from '@/components/AppLayout.vue'
import WebTerminal from '@/components/WebTerminal.vue'
import { useAuthStore } from '@/stores/auth'
import { TerminalSquare, Wifi, WifiOff, Loader2, ArrowLeft } from 'lucide-vue-next'

const props = defineProps<{ name: string }>()
const auth = useAuthStore()
const cluster = computed(() => auth.clusterName ?? 'default')
const status = ref('connecting')

function onStatus(s: string) {
  status.value = s
}
</script>

<template>
  <AppLayout>
    <!-- Back link -->
    <router-link
      :to="`/edges/${props.name}`"
      class="stagger-item mb-4 inline-flex items-center gap-1.5 rounded-lg px-2 py-1 text-[12px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
      style="animation-delay: 0ms"
    >
      <ArrowLeft class="h-3 w-3" :stroke-width="2" />
      Back to {{ props.name }}
    </router-link>

    <!-- Header -->
    <div class="stagger-item mb-4 flex items-center justify-between" style="animation-delay: 40ms">
      <div class="flex items-center gap-3">
        <div class="relative flex h-9 w-9 items-center justify-center">
          <div class="absolute inset-0 rounded-lg bg-accent/15 blur-md" />
          <div class="relative flex h-9 w-9 items-center justify-center rounded-lg border border-accent/20 bg-surface-overlay">
            <TerminalSquare class="h-4.5 w-4.5 text-accent" :stroke-width="1.75" />
          </div>
        </div>
        <div>
          <h1 class="text-[15px] font-semibold text-text-primary">SSH Terminal</h1>
          <p class="text-[11px] text-text-muted">
            <span class="font-mono text-accent">{{ props.name }}</span>
          </p>
        </div>
      </div>

      <div class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-1.5 backdrop-blur">
        <Loader2
          v-if="status === 'connecting'"
          class="h-3 w-3 animate-spin text-warning"
          :stroke-width="2"
        />
        <Wifi
          v-else-if="status === 'connected'"
          class="h-3 w-3 text-success"
          :stroke-width="2"
        />
        <WifiOff
          v-else
          class="h-3 w-3 text-danger"
          :stroke-width="2"
        />
        <span
          class="text-[11px] font-medium capitalize"
          :class="{
            'text-warning': status === 'connecting',
            'text-success': status === 'connected',
            'text-danger': status === 'disconnected' || status === 'error',
          }"
        >
          {{ status }}
        </span>
      </div>
    </div>

    <!-- Terminal -->
    <div class="border-beam stagger-item overflow-hidden rounded-2xl" style="animation-delay: 80ms">
      <div class="flex items-center gap-2 border-b border-border-subtle bg-surface-raised/90 px-4 py-2 backdrop-blur">
        <div class="flex gap-1.5">
          <div class="h-2.5 w-2.5 rounded-full bg-danger/60" />
          <div class="h-2.5 w-2.5 rounded-full bg-warning/60" />
          <div class="h-2.5 w-2.5 rounded-full bg-success/60" />
        </div>
        <span class="ml-2 font-mono text-[10px] text-text-muted">{{ props.name }} — ssh</span>
      </div>
      <div class="h-[calc(100vh-280px)] bg-[#0a0a0f]">
        <WebTerminal :cluster="cluster" :edge-name="props.name" @status="onStatus" />
      </div>
    </div>
  </AppLayout>
</template>
