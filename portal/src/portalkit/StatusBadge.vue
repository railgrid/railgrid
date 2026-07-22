<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies under providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`. -->
<script setup lang="ts">
import { computed } from 'vue'
import { AlertTriangle, CheckCircle, Circle, Clock, XCircle } from 'lucide-vue-next'

type Tone = 'success' | 'warning' | 'danger' | 'muted'
type ToneConfig = { toneClass: string; dotClass: string; pulseClass: string }

const props = withDefaults(
  defineProps<{
    status: string
    connected?: boolean | null
    tone?: Tone | null
  }>(),
  { connected: null, tone: null },
)

const toneConfig: Record<Tone, ToneConfig> = {
  success: { toneClass: 'tone-success', dotClass: 'dot-success', pulseClass: 'pulse-success' },
  warning: { toneClass: 'tone-warning', dotClass: 'dot-warning', pulseClass: 'pulse-warning' },
  danger: { toneClass: 'tone-danger', dotClass: 'dot-danger', pulseClass: 'pulse-danger' },
  muted: { toneClass: 'tone-muted', dotClass: 'dot-muted', pulseClass: 'pulse-muted' },
}

const config = computed(() => {
  if (props.connected === false)
    return { ...toneConfig.danger, icon: XCircle }

  if (props.tone) {
    const tone = toneConfig[props.tone]
    return { ...tone, icon: props.tone === 'danger' ? AlertTriangle : props.tone === 'warning' ? Clock : props.tone === 'success' ? CheckCircle : Circle }
  }

  switch (props.status?.toLowerCase()) {
    case 'ready':
    case 'succeeded':
    case 'committed':
      return { ...toneConfig.success, icon: CheckCircle }
    case 'scheduling':
    case 'pending':
    case 'provisioning':
    case 'running':
    case 'status unavailable':
      return { ...toneConfig.warning, icon: Clock }
    case 'active':
      return { ...toneConfig.success, icon: CheckCircle }
    case 'terminating':
    case 'failed':
    case 'error':
    case 'repository missing':
    case 'connection missing':
      return { ...toneConfig.danger, icon: AlertTriangle }
    default:
      return { ...toneConfig.muted, icon: Circle }
  }
})
</script>

<template>
  <span
    class="status-badge"
    :class="config.toneClass"
  >
    <span class="status-badge-dot-wrap">
      <span
        v-if="status?.toLowerCase() === 'ready' && connected !== false"
        class="live-dot status-badge-pulse"
        :class="config.pulseClass"
      />
      <span class="status-badge-dot" :class="config.dotClass" />
    </span>
    {{ status }}
  </span>
</template>
