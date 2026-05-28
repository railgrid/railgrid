<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useTerminalSessionsStore } from '@/stores/terminalSessions'

// Deep-link helper for /providers/kubernetes-edges/:name/terminal. The
// terminal itself lives in the portal's global dock (the provider
// bridges to it via terminal-adapter.ts → kedge-terminal-open event),
// so this view just opens a session and bounces back to the edge
// detail page. Was /edges/:name/terminal in the legacy SPA router;
// path shape preserved for back-compat with any saved deep links.
const props = defineProps<{ name: string }>()
const auth = useAuthStore()
const router = useRouter()
const store = useTerminalSessionsStore()

onMounted(() => {
  const cluster = auth.clusterName
  if (cluster) {
    store.openSession({ edgeName: props.name, cluster })
  }
  router.replace(`/${props.name}`)
})
</script>

<template>
  <div />
</template>
