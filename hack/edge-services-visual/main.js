import { createApp, nextTick, ref } from 'vue'
import ResourceTable from '../../provider-sdk/portalkit-vue/ResourceTable.vue'
import StatusBadge from '../../provider-sdk/portalkit-vue/StatusBadge.vue'
import './style.css'

const columns = [
  { key: 'name', label: 'Service', primary: true },
  { key: 'edge', label: 'Edge' },
  { key: 'type', label: 'Type' },
  { key: 'target', label: 'Target' },
  { key: 'status', label: 'Status' },
]
const rows = [
  { name: 'home-assistant', edge: 'home-server', type: 'Home Assistant', target: 'localhost:8123', status: 'Ready' },
  { name: 'unifi-console', edge: 'home-server', type: 'UniFi', target: '192.168.1.1:443', status: 'Ready' },
  { name: 'sonarr', edge: 'media-server', type: 'Sonarr', target: 'localhost:8989', status: 'Pending' },
  { name: 'radarr', edge: 'media-server', type: 'Radarr', target: 'localhost:7878', status: 'Ready' },
]

createApp({
  components: { ResourceTable, StatusBadge },
  setup() {
    const failed = ref(false)
    const failureControl = ref(null)
    const theme = ref('light')
    function toggleTheme() {
      theme.value = theme.value === 'light' ? 'dark' : 'light'
      document.documentElement.classList.toggle('light', theme.value === 'light')
    }
    async function retry() {
      failed.value = false
      await nextTick()
      failureControl.value?.focus()
    }
    return { columns, rows, failed, failureControl, retry, theme, toggleTheme }
  },
  template: `
    <main class="example">
      <div class="fixture-controls">
        <button ref="failureControl" class="k-btn k-btn--ghost" @click="failed = !failed">{{ failed ? 'Restore refresh' : 'Simulate failed refresh' }}</button>
        <button class="k-btn k-btn--ghost" @click="toggleTheme">Switch to {{ theme === 'light' ? 'dark' : 'light' }} theme</button>
      </div>
      <header class="heading">
        <div><h1>Services</h1><p>Apps reachable from your edges.</p></div>
        <button class="k-btn k-btn--primary" type="button" disabled aria-describedby="creation-note">New service</button>
      </header>
      <ResourceTable :columns="columns" :rows="rows" aria-label="Services" row-key="name"
        :loaded="true" :error="failed ? 'Could not refresh.' : null"
        :stale="failed" :retryable="failed" searchable search-placeholder="Search services…"
        :search-keys="['name', 'edge', 'type', 'target']" @retry="retry">
        <template #status="{ value }"><StatusBadge :status="String(value)" /></template>
      </ResourceTable>
      <p id="creation-note" class="fixture-note">Service creation is unavailable in this example.</p>
    </main>`,
}).mount('#app')
