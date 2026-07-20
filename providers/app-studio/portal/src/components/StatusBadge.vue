<script setup lang="ts">
import { computed } from 'vue'
import { CheckCircle, Clock, AlertTriangle, XCircle, Circle } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    status: string
    connected?: boolean | null
  }>(),
  { connected: null },
)

// Styling comes from the host-served shared recipe layer (kedge-ui.css:
// .k-badge / .k-badge--*), so this badge matches the host and every other
// provider without each build compiling its own utility classes.
const config = computed(() => {
  if (props.connected === false) return { cls: 'k-badge--danger', icon: XCircle }

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
