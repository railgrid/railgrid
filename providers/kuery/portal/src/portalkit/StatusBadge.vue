<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies under providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`. -->
<script setup lang="ts">
import { computed } from 'vue'
import { AlertTriangle, CheckCircle, Circle, Clock, XCircle } from 'lucide-vue-next'

type Tone = 'success' | 'warning' | 'danger' | 'muted'
type ToneConfig = { toneClass: string; dotClass: string }

const props = withDefaults(
  defineProps<{
    status: string
    connected?: boolean | null
    tone?: Tone | null
  }>(),
  { connected: null, tone: null },
)

const toneConfig: Record<Tone, ToneConfig> = {
  success: { toneClass: 'k-badge--success', dotClass: 'k-badge__dot--success' },
  warning: { toneClass: 'k-badge--warning', dotClass: 'k-badge__dot--warning' },
  danger: { toneClass: 'k-badge--danger', dotClass: 'k-badge__dot--danger' },
  muted: { toneClass: 'k-badge--muted', dotClass: 'k-badge__dot--muted' },
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
    case 'active':
    case 'connected':
    case 'loaded':
      return { ...toneConfig.success, icon: CheckCircle }
    case 'scheduling':
    case 'pending':
    case 'provisioning':
    case 'running':
    case 'retrying':
    case 'status unavailable':
    case 'loading':
    case 'starting':
    case 'loaded unverified':
      return { ...toneConfig.warning, icon: Clock }
    case 'terminating':
    case 'failed':
    case 'error':
    case 'repository missing':
    case 'connection missing':
    case 'disconnected':
    case 'unreachable':
    case 'needs attention':
      return { ...toneConfig.danger, icon: AlertTriangle }
    default:
      return { ...toneConfig.muted, icon: Circle }
  }
})

const isLive = computed(() => {
  const status = props.status?.toLowerCase()
  return status === 'ready' &&
    props.connected !== false &&
    config.value.toneClass === toneConfig.success.toneClass
})
</script>

<template>
  <span
    class="k-badge"
    :class="config.toneClass"
  >
    <span
      class="k-badge__dot"
      :class="isLive ? 'live-dot' : config.dotClass"
    />
    {{ status }}
  </span>
</template>
