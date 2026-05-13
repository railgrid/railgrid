<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useTerminalSessionsStore } from '@/stores/terminalSessions'

// Legacy deep-link route: /edges/:name/terminal. The terminal now lives in a
// global dock, so we open a session and redirect back to the edge detail page.
const props = defineProps<{ name: string }>()
const auth = useAuthStore()
const router = useRouter()
const store = useTerminalSessionsStore()

onMounted(() => {
  const cluster = auth.clusterName
  if (cluster) {
    store.openSession({ edgeName: props.name, cluster })
  }
  router.replace(`/edges/${props.name}`)
})
</script>

<template>
  <div />
</template>
