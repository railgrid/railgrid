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

const toneConfig: Record<Tone, { cls: string; icon: typeof CheckCircle }> = {
  success: { cls: 'k-badge--success', icon: CheckCircle },
  warning: { cls: 'k-badge--warning', icon: Clock },
  danger: { cls: 'k-badge--danger', icon: AlertTriangle },
  muted: { cls: 'k-badge--muted', icon: Circle },
}

const config = computed(() => {
  if (props.connected === false)
    return { cls: 'k-badge--danger', icon: XCircle }

  if (props.tone) return toneConfig[props.tone]

  switch (props.status?.toLowerCase()) {
    case 'ready':
    case 'succeeded':
    case 'committed':
    case 'active':
      return { cls: 'k-badge--success', icon: CheckCircle }
    case 'scheduling':
    case 'pending':
    case 'provisioning':
    case 'running':
    case 'status unavailable':
      return { cls: 'k-badge--warning', icon: Clock }
    case 'terminating':
    case 'failed':
    case 'error':
    case 'repository missing':
    case 'connection missing':
      return { cls: 'k-badge--danger', icon: AlertTriangle }
    default:
      return { cls: 'k-badge--muted', icon: Circle }
  }
})
</script>

<template>
  <span class="k-badge" :class="config.cls">
    <span class="relative flex h-1.5 w-1.5">
      <span
        v-if="status?.toLowerCase() === 'ready' && connected !== false"
        class="live-dot k-badge__dot absolute inline-flex h-full w-full"
      />
      <span class="k-badge__dot relative inline-flex" />
    </span>
    {{ status }}
  </span>
</template>
