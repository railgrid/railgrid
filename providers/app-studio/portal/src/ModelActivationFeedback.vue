<script setup lang="ts">
import { Loader2 } from 'lucide-vue-next'
import InlineNotification from './portalkit/InlineNotification.vue'

defineProps<{ loading: boolean; retrying: boolean; error: string | null }>()
const emit = defineEmits<{ check: [] }>()
</script>

<template>
  <p v-if="loading && !retrying" class="flex items-center gap-2 text-[13px] text-text-secondary" role="status">
    <Loader2 class="h-4 w-4 animate-spin text-accent motion-reduce:animate-none" aria-hidden="true" /> Waiting for the saved model to become available…
  </p>
  <InlineNotification
    v-else
    :tone="error ? 'error' : 'warning'"
    :message="error || 'The model is saved and still becoming available.'"
    action-label="Check again"
    action-busy-label="Checking model…"
    :action-busy="retrying"
    @action="emit('check')"
  />
</template>
