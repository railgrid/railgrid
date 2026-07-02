<script setup lang="ts">
import { computed } from 'vue'
import { CheckCircle, Clock, AlertTriangle, XCircle, Circle } from 'lucide-vue-next'

type Tone = 'success' | 'warning' | 'danger' | 'muted'

const props = withDefaults(
  defineProps<{
    status: string
    connected?: boolean | null
    tone?: Tone | null
  }>(),
  { connected: null, tone: null },
)

const toneConfig: Record<Tone, { bg: string; text: string; dot: string; glow: string }> = {
  success: { bg: 'bg-success-subtle', text: 'text-success', dot: 'bg-success', glow: 'text-success' },
  warning: { bg: 'bg-warning-subtle', text: 'text-warning', dot: 'bg-warning', glow: 'text-warning' },
  danger: { bg: 'bg-danger-subtle', text: 'text-danger', dot: 'bg-danger', glow: 'text-danger' },
  muted: { bg: 'bg-surface-overlay', text: 'text-text-muted', dot: 'bg-text-muted', glow: 'text-text-muted' },
}

const config = computed(() => {
  if (props.connected === false)
    return { bg: 'bg-danger-subtle', text: 'text-danger', icon: XCircle, dot: 'bg-danger', glow: 'text-danger' }

  if (props.tone) {
    const tone = toneConfig[props.tone]
    return { ...tone, icon: props.tone === 'danger' ? AlertTriangle : props.tone === 'warning' ? Clock : props.tone === 'success' ? CheckCircle : Circle }
  }

  switch (props.status?.toLowerCase()) {
    case 'ready':
    case 'succeeded':
    case 'committed':
      return { bg: 'bg-success-subtle', text: 'text-success', icon: CheckCircle, dot: 'bg-success', glow: 'text-success' }
    case 'scheduling':
    case 'pending':
    case 'provisioning':
    case 'running':
    case 'status unavailable':
      return { bg: 'bg-warning-subtle', text: 'text-warning', icon: Clock, dot: 'bg-warning', glow: 'text-warning' }
    case 'active':
      return { bg: 'bg-success-subtle', text: 'text-success', icon: CheckCircle, dot: 'bg-success', glow: 'text-success' }
    case 'terminating':
    case 'failed':
    case 'error':
    case 'repository missing':
    case 'connection missing':
      return { bg: 'bg-danger-subtle', text: 'text-danger', icon: AlertTriangle, dot: 'bg-danger', glow: 'text-danger' }
    default:
      return { bg: 'bg-surface-overlay', text: 'text-text-muted', icon: Circle, dot: 'bg-text-muted', glow: 'text-text-muted' }
  }
})
</script>

<template>
  <span
    class="inline-flex items-center gap-1.5 rounded-full border border-current/10 px-2.5 py-1 text-[11px] font-semibold uppercase tracking-wide transition-colors duration-150"
    :class="[config.bg, config.text]"
  >
    <span class="relative flex h-1.5 w-1.5">
      <span
        v-if="status?.toLowerCase() === 'ready' && connected !== false"
        class="live-dot absolute inline-flex h-full w-full rounded-full"
        :class="config.glow"
      />
      <span class="relative inline-flex h-1.5 w-1.5 rounded-full" :class="config.dot" />
    </span>
    {{ status }}
  </span>
</template>
