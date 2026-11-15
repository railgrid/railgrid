import { createApp, h, markRaw, ref } from 'vue'
import '@fontsource-variable/instrument-sans'
import '@fontsource-variable/archivo/wdth.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import './fixture.css'
import '../../providers/agents/portal/src/style.css'
import { AppStore } from '../../providers/agents/portal/src/store'
import Toolsets from '../../providers/agents/portal/src/views/Toolsets.vue'

const api = {
  patchToolset: async (name, spec) => {
    const item = { metadata: { name }, spec }
    store.toolsets.data = [item]
    return item
  },
  listToolsets: async () => store.toolsets.data,
}
const store = new AppStore(api)
Object.assign(store.connections, { loaded: true, hasSnapshot: true, data: [{ metadata: { name: 'team-search' }, spec: { type: 'websearch' } }] })
Object.assign(store.toolsets, { loaded: true, hasSnapshot: true, data: [{ metadata: { name: 'research' }, spec: { displayName: 'Research', connections: ['team-search'], families: ['core', 'web'] } }] })

createApp({
  setup() {
    const armed = ref(false)
    const theme = ref('light')
    function toggleTheme() {
      theme.value = theme.value === 'light' ? 'dark' : 'light'
      document.documentElement.className = theme.value
    }
    function armRefresh() {
      if (armed.value) return
      armed.value = true
      setTimeout(() => {
        store.connections.data = []
        store.dispatchEvent(new Event('change'))
        armed.value = false
      }, 5000)
    }
    return () => h('div', [
      h('p', 'Simulated data; no backend is contacted. Arm the refresh, then focus team-search before it completes. Reload to restart.'),
      h('button', { type: 'button', class: 'k-btn k-btn--ghost', disabled: armed.value, onClick: armRefresh }, armed.value ? 'Refresh armed' : 'Arm connection refresh'),
      h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: toggleTheme }, `Switch to ${theme.value === 'light' ? 'dark' : 'light'} theme`),
      h('railgrid-provider-agents', [h(Toolsets, { store: markRaw(store), api: markRaw(api), editRoute: true, editName: 'research' })]),
    ])
  },
}).mount('#root')
