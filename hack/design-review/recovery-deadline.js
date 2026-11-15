import { createApp, h, ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'
import '@/assets/main.css'
import OrgsSection from '@/pages/bonkers/OrgsSection.vue'
import { useAdminStore } from '@/stores/admin'
import './kuery-impact.css'

const pinia = createPinia()
setActivePinia(pinia)
const admin = useAdminStore()
const sampleNow = Date.now()
const deadline = new Date(sampleNow + 2 * 86_400_000).toISOString()
const requestedAt = new Date(sampleNow - 86_400_000).toISOString()
admin.orgs = [{ name: 'north', displayName: 'North', workspacePath: 'root:north', deletionRequestedAt: requestedAt, purgeAt: deadline, workspaces: [{ uuid: 'staging', displayName: 'Staging', clusterName: 'fixture', providers: [], deletionRequestedAt: requestedAt, purgeAt: deadline }] }]
admin.loaded = true
// No fixture control invokes a hub or changes a real organization.
admin.refresh = async () => {}
admin.undeleteOrg = async () => {}
admin.undeleteWorkspace = async () => {}
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: OrgsSection }, { path: '/bonkers/metering', component: OrgsSection }] })
await router.push({ path: '/', query: { organization: 'north' } })
await router.isReady()

createApp({
  setup() {
    const invalid = ref(false)
    const theme = ref('light')
    function setInvalid() {
      invalid.value = !invalid.value
      const value = invalid.value ? 'not-a-timestamp' : deadline
      admin.orgs[0].purgeAt = value
      admin.orgs[0].workspaces[0].purgeAt = value
    }
    return () => h('main', { class: 'impact-fixture' }, [
      h('div', { class: 'impact-fixture__controls' }, [
        h('a', { class: 'k-btn k-btn--ghost', href: '/?entry=design.components.timestamp' }, 'Back to timestamp review'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => router.push({ path: '/', query: { organization: 'north' } }) }, 'Organization'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => router.push({ path: '/', query: { organization: 'north', workspace: 'staging' } }) }, 'Workspace'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: setInvalid }, invalid.value ? 'Use valid deadline' : 'Use invalid deadline'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { theme.value = theme.value === 'light' ? 'dark' : 'light'; document.documentElement.classList.toggle('light', theme.value === 'light') } }, `Switch to ${theme.value === 'light' ? 'dark' : 'light'} theme`),
      ]),
      h('p', { class: 'impact-fixture__note' }, 'Synthetic admin data. Restore actions do not contact a hub or change resources.'),
      h(OrgsSection),
    ])
  },
}).use(pinia).use(router).mount('#app')
