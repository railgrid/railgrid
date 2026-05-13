<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch, nextTick, computed, defineExpose } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useAuthStore } from '@/stores/auth'
import { Wifi, WifiOff, Loader2, RotateCw, Eraser } from 'lucide-vue-next'

const props = defineProps<{
  edgeName: string
  cluster: string
  isActive: boolean
}>()

const auth = useAuthStore()
const termEl = ref<HTMLDivElement | null>(null)
const connectionStatus = ref<'connecting' | 'connected' | 'disconnected' | 'error'>('disconnected')

let terminal: Terminal | null = null
let fitAddon: FitAddon | null = null
let ws: WebSocket | null = null
let heartbeatTimer: ReturnType<typeof setInterval> | null = null
let dataDisposable: { dispose: () => void } | null = null
let resizeDisposable: { dispose: () => void } | null = null
let initialized = false

const statusLabel = computed(() => {
  switch (connectionStatus.value) {
    case 'connecting':
      return 'Connecting…'
    case 'connected':
      return 'Connected'
    case 'disconnected':
      return 'Disconnected'
    case 'error':
      return 'Connection error'
  }
  return 'Unknown'
})

function buildWsUrl(token: string): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const path = `/services/edges-proxy/clusters/${props.cluster}/apis/kedge.faros.sh/v1alpha1/edges/${props.edgeName}/ssh`
  return `${proto}//${location.host}${path}?token=${encodeURIComponent(token)}`
}

async function initialize() {
  if (initialized || !termEl.value) return
  initialized = true
  connectionStatus.value = 'connecting'

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
  terminal.open(termEl.value)
  fitAddon.fit()

  const token = await auth.getValidToken()
  ws = new WebSocket(buildWsUrl(token))
  ws.binaryType = 'arraybuffer'

  ws.onopen = () => {
    connectionStatus.value = 'connected'
    ws!.send(JSON.stringify({ type: 'resize', cols: terminal!.cols, rows: terminal!.rows }))
    heartbeatTimer = setInterval(() => {
      if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'heartbeat' }))
    }, 15000)
  }

  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) terminal!.write(new Uint8Array(ev.data))
    else terminal!.write(ev.data)
  }

  ws.onclose = () => {
    connectionStatus.value = 'disconnected'
    terminal?.write('\r\n\x1b[90m--- session ended ---\x1b[0m\r\n')
    stopHeartbeat()
  }

  ws.onerror = () => {
    connectionStatus.value = 'error'
  }

  dataDisposable = terminal.onData((data) => {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'cmd', cmd: btoa(data) }))
    }
  })

  resizeDisposable = terminal.onResize(({ cols, rows }) => {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resize', cols, rows }))
    }
  })
}

function stopHeartbeat() {
  if (heartbeatTimer) {
    clearInterval(heartbeatTimer)
    heartbeatTimer = null
  }
}

function resize() {
  nextTick(() => {
    try {
      fitAddon?.fit()
    } catch {
      // FitAddon throws when container is 0x0 (hidden tab); safe to ignore.
    }
  })
}

function focusTerminal() {
  terminal?.focus()
}

function clearTerminal() {
  terminal?.clear()
}

async function reconnect() {
  cleanup()
  initialized = false
  await nextTick()
  await initialize()
}

function cleanup() {
  stopHeartbeat()
  dataDisposable?.dispose()
  resizeDisposable?.dispose()
  dataDisposable = null
  resizeDisposable = null
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
    ws.close()
  }
  ws = null
  terminal?.dispose()
  terminal = null
  fitAddon = null
}

onMounted(() => {
  if (props.isActive) nextTick(() => initialize())
})

onUnmounted(() => {
  cleanup()
})

watch(
  () => props.isActive,
  (active) => {
    if (active && !initialized) {
      nextTick(() => initialize())
    } else if (active && initialized) {
      resize()
      focusTerminal()
    }
  },
)

defineExpose({ resize, focusTerminal, clearTerminal, reconnect })
</script>

<template>
  <div class="terminal-instance flex h-full w-full flex-col bg-[#0a0a0f]">
    <div class="flex h-7 items-center justify-between gap-2 border-b border-border-subtle bg-surface-overlay/40 px-3 text-[10px] text-text-muted">
      <div class="flex items-center gap-1.5">
        <component
          :is="connectionStatus === 'connected' ? Wifi : connectionStatus === 'connecting' ? Loader2 : WifiOff"
          class="h-3 w-3"
          :class="[
            connectionStatus === 'connected' ? 'text-success' : '',
            connectionStatus === 'connecting' ? 'animate-spin text-warning' : '',
            connectionStatus === 'disconnected' || connectionStatus === 'error' ? 'text-danger' : '',
          ]"
          :stroke-width="1.75"
        />
        <span>{{ statusLabel }}</span>
        <span class="font-mono text-text-muted/50">·</span>
        <span class="font-mono text-text-muted/70">{{ edgeName }}</span>
      </div>
      <div class="flex items-center gap-1">
        <button
          v-if="connectionStatus === 'disconnected' || connectionStatus === 'error'"
          class="flex h-5 w-5 items-center justify-center rounded text-text-muted transition-colors hover:bg-surface-hover hover:text-accent"
          title="Reconnect"
          @click="reconnect"
        >
          <RotateCw class="h-3 w-3" :stroke-width="1.75" />
        </button>
        <button
          class="flex h-5 w-5 items-center justify-center rounded text-text-muted transition-colors hover:bg-surface-hover hover:text-accent disabled:opacity-30"
          title="Clear"
          :disabled="connectionStatus !== 'connected'"
          @click="clearTerminal"
        >
          <Eraser class="h-3 w-3" :stroke-width="1.75" />
        </button>
      </div>
    </div>
    <div ref="termEl" class="min-h-0 flex-1" @click="focusTerminal" />
  </div>
</template>

<style scoped>
:deep(.xterm) {
  padding: 8px;
  height: 100%;
}
:deep(.xterm-viewport) {
  background: transparent !important;
}
</style>
