<script setup lang="ts">
import ResourceTimestamp from '../portalkit/ResourceTimestamp.vue'
import type { Schedule, Trigger } from '../types'
import { deliveryOutcomeLabel } from './webhook-deliveries'

defineProps<{ kind: 'schedule' | 'trigger'; status?: Schedule['status'] & Trigger['status'] }>()
</script>

<template>
  <template v-if="kind === 'schedule'"><template v-if="status?.nextRun">next <ResourceTimestamp :value="status.nextRun" label="Next scheduled run" /></template><template v-else>armed</template></template>
  <template v-else><template v-if="status?.lastFired">last fired <ResourceTimestamp :value="status.lastFired" label="Last fired" /></template><template v-else>armed</template><template v-if="status?.lastDelivery && status.lastDelivery.outcome !== 'fired'"> · last delivery <ResourceTimestamp :value="status.lastReceived || status.lastDelivery.time" label="Last delivery" /> {{ deliveryOutcomeLabel(status.lastDelivery.outcome).toLowerCase() }}<template v-if="status.lastDelivery.reason">: {{ status.lastDelivery.reason }}</template></template></template>
</template>
