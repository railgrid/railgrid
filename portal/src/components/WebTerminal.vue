<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ cluster: string; edgeName: string }>()
const emit = defineEmits<{ (e: 'status', status: string): void }>()

const termRef = ref<HTMLElement | null>(null)
const auth = useAuthStore()

let terminal: Terminal | null = null
let fitAddon: FitAddon | null = null
let ws: WebSocket | null = null
let heartbeatTimer: ReturnType<typeof setInterval> | null = null

function buildWsUrl(cluster: string, edgeName: string, token: string): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const path = `/services/edges-proxy/clusters/${cluster}/apis/kedge.faros.sh/v1alpha1/edges/${edgeName}/ssh`
  return `${proto}//${location.host}${path}?token=${encodeURIComponent(token)}`
}

async function connect() {
  if (!termRef.value) return

  emit('status', 'connecting')

  terminal = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', monospace",
    theme: {
      background: '#0a0a0f',
      foreground: '#c8c8d0',
      cursor: '#a78bfa',
      selectionBackground: '#a78bfa33',
      black: '#0a0a0f',
      red: '#f87171',
      green: '#34d399',
      yellow: '#fbbf24',
      blue: '#60a5fa',
      magenta: '#a78bfa',
      cyan: '#22d3ee',
      white: '#c8c8d0',
      brightBlack: '#404050',
      brightRed: '#fca5a5',
      brightGreen: '#6ee7b7',
      brightYellow: '#fcd34d',
      brightBlue: '#93c5fd',
      brightMagenta: '#c4b5fd',
      brightCyan: '#67e8f9',
      brightWhite: '#f0f0f5',
    },
  })

  fitAddon = new FitAddon()
  terminal.loadAddon(fitAddon)
  terminal.open(termRef.value)
  fitAddon.fit()

  const token = await auth.getValidToken()
  const url = buildWsUrl(props.cluster, props.edgeName, token)

  ws = new WebSocket(url)
  ws.binaryType = 'arraybuffer'

  ws.onopen = () => {
    emit('status', 'connected')
    // Send initial resize.
    ws!.send(JSON.stringify({
      type: 'resize',
      cols: terminal!.cols,
      rows: terminal!.rows,
    }))

    // Heartbeat every 15s.
    heartbeatTimer = setInterval(() => {
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'heartbeat' }))
      }
    }, 15000)
  }

  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) {
      terminal!.write(new Uint8Array(ev.data))
    } else {
      terminal!.write(ev.data)
    }
  }

  ws.onclose = () => {
    emit('status', 'disconnected')
    terminal?.write('\r\n\x1b[90m--- session ended ---\x1b[0m\r\n')
    cleanup()
  }

  ws.onerror = () => {
    emit('status', 'error')
  }

  // Forward keystrokes as base64-encoded cmd messages.
  terminal.onData((data) => {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({
        type: 'cmd',
        cmd: btoa(data),
      }))
    }
  })

  // Forward resize events.
  terminal.onResize(({ cols, rows }) => {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({
        type: 'resize',
        cols,
        rows,
      }))
    }
  })
}

function cleanup() {
  if (heartbeatTimer) {
    clearInterval(heartbeatTimer)
    heartbeatTimer = null
  }
}

function handleResize() {
  fitAddon?.fit()
}

onMounted(() => {
  connect()
  window.addEventListener('resize', handleResize)
})

onUnmounted(() => {
  window.removeEventListener('resize', handleResize)
  cleanup()
  if (ws?.readyState === WebSocket.OPEN || ws?.readyState === WebSocket.CONNECTING) {
    ws.close()
  }
  terminal?.dispose()
})

watch([() => props.cluster, () => props.edgeName], () => {
  if (ws?.readyState === WebSocket.OPEN) ws.close()
  terminal?.dispose()
  cleanup()
  connect()
})
</script>

<template>
  <div ref="termRef" class="h-full w-full" />
</template>

<style scoped>
:deep(.xterm) {
  padding: 12px;
  height: 100%;
}
:deep(.xterm-viewport) {
  border-radius: 0 0 12px 12px;
}
</style>
