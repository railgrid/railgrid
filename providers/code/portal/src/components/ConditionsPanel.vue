<script setup lang="ts">
import type { ConditionInfo } from '../types'

// ConditionsPanel renders a resource's status conditions verbatim so the
// reason/message a controller recorded is visible for debugging — shared by the
// Connection and Repository detail views. observed-vs-current generation, when
// supplied, surfaces "controller has not caught up" independently of the
// conditions themselves.
const props = defineProps<{
  conditions: ConditionInfo[]
  generation?: number
  observedGeneration?: number
  emptyText?: string
}>()

function condClass(status: string): string {
  if (status === 'True') return 'ok'
  if (status === 'False') return 'warn'
  return 'muted'
}

const reconciled = (): boolean =>
  props.observedGeneration === undefined ||
  props.generation === undefined ||
  props.observedGeneration >= props.generation
</script>

<template>
  <div class="panel">
    <h3 class="panel-title">Conditions</h3>
    <p v-if="observedGeneration !== undefined && !reconciled()" class="warn cond-lag">
      Controller has not caught up — spec generation {{ generation }}, observed {{ observedGeneration }}.
    </p>
    <p v-if="!conditions.length" class="empty">
      {{ emptyText || 'No conditions yet — the controller has not reconciled this resource.' }}
    </p>
    <table v-else class="table">
      <thead>
        <tr><th>Type</th><th>Status</th><th>Reason</th><th>Message</th><th>Since</th></tr>
      </thead>
      <tbody>
        <tr v-for="c in conditions" :key="c.type">
          <td><strong>{{ c.type }}</strong></td>
          <td><span :class="['badge', condClass(c.status)]">{{ c.status }}</span></td>
          <td>{{ c.reason || '—' }}</td>
          <td class="cond-msg">{{ c.message || '—' }}</td>
          <td class="muted">{{ c.lastTransitionTime || '—' }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.cond-lag { margin: 0 0 0.5rem; }
.cond-msg { max-width: 40ch; word-break: break-word; }
</style>
