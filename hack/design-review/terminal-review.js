import { createApp, h, ref } from 'vue'
import { createPinia } from 'pinia'
import TerminalDock from '@/components/TerminalDock.vue'
import { useTerminalSessionsStore } from '@/stores/terminalSessions'
import { useAuthStore } from '@/stores/auth'
import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'
import './kuery-impact.css'
import './terminal-review.css'

const pinia = createPinia()
// The real component is mounted without a credential, so it cannot open SSH.
useAuthStore(pinia).token = null
const sessions = useTerminalSessionsStore(pinia)
function reset() {
  sessions.closeAllSessions()
  sessions.openSession({ edgeName: 'staging-west', cluster: 'fixture' })
  sessions.openSession({ edgeName: 'production-east', cluster: 'fixture' })
  sessions.panelState = { height: 420, isMinimized: false, isFullscreen: false, splitLayout: 'vertical', paneSizes: [50, 50] }
}
reset()

createApp({
  setup() {
    const light = ref(true)
    return () => h('main', { class: 'impact-fixture' }, [
      h('div', { class: 'impact-fixture__controls' }, [
        h('a', { href: './?entry=design.components.terminal-dock', class: 'k-btn k-btn--ghost' }, 'Back to terminal dock'),
        h('button', { class: 'k-btn k-btn--ghost', type: 'button', onClick: () => { light.value = !light.value; document.documentElement.classList.toggle('light', light.value) } }, `Switch to ${light.value ? 'dark' : 'light'} theme`),
        h('button', { class: 'k-btn k-btn--ghost', type: 'button', onClick: reset }, 'Reset sample sessions'),
      ]),
      h('h1', 'Terminal dock'),
      h('p', { class: 'impact-fixture__note' }, 'Sample sessions only. No SSH connection or hub is contacted.'),
      h(TerminalDock),
    ])
  },
}).use(pinia).mount('#app')
