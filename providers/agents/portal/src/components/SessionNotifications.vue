<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { ApiClient } from '../api'
import type { SessionNotification } from '../types'

const props = defineProps<{ api: ApiClient; agent: string; session: string }>()
const emit = defineEmits<{ close: [] }>()
const items = ref<SessionNotification[]>([])
const loading = ref(false)
const error = ref('')
let requestID = 0
const hasSession = computed(() => !!props.agent && !!props.session)
const newestFirst = computed(() => [...items.value].sort((left, right) =>
  Date.parse(right.createdAt) - Date.parse(left.createdAt),
))

async function refresh(): Promise<void> {
  const id = ++requestID
  const agent = props.agent
  const session = props.session
  items.value = []
  error.value = ''
  if (!agent || !session) return
  loading.value = true
  try {
    const result = await props.api.listSessionNotifications(agent, session)
    if (id === requestID && props.agent === agent && props.session === session) items.value = result
  } catch (cause) {
    if (id === requestID && props.agent === agent && props.session === session) error.value = (cause as Error).message || 'Request failed'
  } finally {
    if (id === requestID) loading.value = false
  }
}

watch(() => [props.api, props.agent, props.session] as const, () => { void refresh() }, { immediate: true })
const createdAt = (value: string): string => {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? 'Time unavailable' : date.toLocaleString()
}
</script>

<template>
  <section class="agents-session-notifications" aria-labelledby="session-notifications-title">
    <header class="agents-session-notifications__header">
      <div>
        <h2 id="session-notifications-title">Session notifications</h2>
        <p>Separate from the conversation transcript and model context.</p>
      </div>
      <div class="agents-session-notifications__actions">
        <button type="button" class="k-btn k-btn--ghost secondary" :disabled="loading || !hasSession" @click="refresh">
          {{ loading ? 'Refreshing…' : 'Refresh' }}
        </button>
        <button type="button" class="k-btn k-btn--ghost secondary" aria-label="Close session notifications" @click="emit('close')">Close</button>
      </div>
    </header>
    <p v-if="loading && !items.length" class="agents-session-notifications__state" role="status">Loading notifications…</p>
    <div v-else-if="error" class="agents-session-notifications__state" role="alert">
      <p>Could not load session notifications. {{ error }}</p>
      <button type="button" class="k-btn k-btn--ghost secondary" :disabled="loading" @click="refresh">Retry</button>
    </div>
    <p v-else-if="!items.length" class="agents-session-notifications__state" role="status">No notifications for this session.</p>
    <ol v-else class="agents-session-notifications__list" :aria-busy="loading">
      <li v-for="item in newestFirst" :key="item.id">
        <time :datetime="item.createdAt">{{ createdAt(item.createdAt) }}</time>
        <p>{{ item.payload }}</p>
      </li>
    </ol>
  </section>
</template>
