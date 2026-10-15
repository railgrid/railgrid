import { createApp, h, ref } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import WelcomeWizard from '@/components/WelcomeWizard.vue'
import { useGettingStartedFocus } from '@/composables/gettingStartedFocus'
import { useProvidersStore } from '@/stores/providers'
import { useTenantStore } from '@/stores/tenant'
import { useAuthStore } from '@/stores/auth'
import './fixture.css'
import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'

// Only this fixture supplies synthetic catalog data. Components, theme, shell,
// consent dialog and stores are the production implementations.
const catalog = [
  { name: 'infrastructure', displayName: 'Application Templates', category: 'Infrastructure', description: 'Deploy applications from configurable templates on your infrastructure.' },
  { name: 'code', displayName: 'Code', category: 'Development', description: 'Connect Git hosting and manage repositories for your projects.' },
  { name: 'edges', displayName: 'Edges', category: 'Infrastructure', description: 'Connect your servers and Kubernetes clusters.' },
  { name: 'app-studio', displayName: 'App Studio', category: 'Development', description: 'Build applications with AI project workspaces, development sandboxes, and publishing.', requires: [
    { provider: 'infrastructure', group: 'infrastructure.railgrid.ai', resources: [{ name: 'instances', verbs: ['create', 'get'] }] },
    { provider: 'code', group: 'code.railgrid.ai', resources: [{ name: 'repositories', verbs: ['create', 'get'] }] },
  ] },
  { name: 'agents', displayName: 'Agents', category: 'AI', description: 'Run agents with governed tools and model connections.', requires: [
    { provider: 'infrastructure', group: 'infrastructure.railgrid.ai', resources: [{ name: 'instances', verbs: ['create', 'get'] }] },
    { provider: 'edges', group: 'edges.railgrid.ai', resources: [{ name: 'services', verbs: ['get'] }] },
  ] },
].map(p => ({ ...p, scope: 'global', ready: true, export: { name: `${p.name}.providers.railgrid.ai`, path: `root:providers:${p.name}` }, serving: { ui: {} } }))

const app = createApp({
  setup() {
    const open = ref(true)
    const startAtGoal = ref(false)
    useGettingStartedFocus(open)
    const providers = useProvidersStore()
    const tenant = useTenantStore()
    const auth = useAuthStore()
    auth.user = { userId: 'fixture-user', email: 'fixture@example.test' }
    const requests = []
    tenant.$patch({ orgUUID: 'demo', workspaceUUID: 'workspace', workspaceMode: 'workspace', orgs: [{ uuid: 'demo', displayName: 'Demo', role: 'admin' }], workspacesByOrg: { demo: [{ uuid: 'workspace', orgUUID: 'demo', displayName: 'My Workspace', role: 'admin', clusterName: 'workspace-cluster' }] } })
    providers.$patch({ loaded: true, bindingsWorkspace: 'workspace', bindingsOrgUUID: 'demo', catalogOrgUUID: 'demo', bindingsLoadState: 'ready', items: structuredClone(catalog), categories: [{ name: 'Infrastructure', icon: 'Server' }, { name: 'Development', icon: 'Layers' }, { name: 'AI', icon: 'Bot' }] })
    // Replace only network actions. Tests can hold/fail a request explicitly.
    let failure = null
    providers.enable = async (...args) => {
      requests.push(args)
      if (failure) throw new Error(failure)
      providers.bindingNamesByProvider = { ...providers.bindingNamesByProvider, [args[0].name]: `binding-${args[0].name}` }
    }
    providers.load = async () => { providers.loading = false; providers.error = null }
    window.fixture = {
      providers, tenant, open, requests,
      failEnable(message) { failure = message },
      setRole(role) { tenant.orgs[0].role = role; tenant.workspacesByOrg.demo[0].role = role },
      resetCatalog() { providers.items = structuredClone(catalog) },
    }
    return () => h(AppLayout, null, { default: () => open.value
      ? h(WelcomeWizard, { startAtGoal: startAtGoal.value, onDismiss: () => { open.value = false } })
      : h('button', { class: 'k-btn k-btn--primary', onClick: () => { startAtGoal.value = true; open.value = true } }, 'Getting started') })
  },
})
app.use(createPinia())
app.use(createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] }))
app.mount('#app')
