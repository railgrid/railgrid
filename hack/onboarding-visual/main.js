import { createApp, Fragment, h, ref } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createWebHistory } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import WelcomeWizard from '@/components/WelcomeWizard.vue'
import ProvidersPage from '@/pages/ProvidersPage.vue'
import ConfirmDialog from '@/portalkit/ConfirmDialog.vue'
import ToastHost from '@/portalkit/ToastHost.vue'
import { useOrgProvidersStore } from '@/stores/orgProviders'
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
const selfHostingCatalog = structuredClone(catalog)
selfHostingCatalog.find(p => p.name === 'code').serving.selfHosting = { supported: true }

const router = createRouter({ history: createWebHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })

const app = createApp({
  setup() {
    const selfHosting = new URLSearchParams(location.search).get('surface') === 'self-hosting'
    const manualResume = new URLSearchParams(location.search).get('manual') === '1'
      && new URLSearchParams(location.search).get('fixture-registration') !== 'missing'
    const open = ref(!selfHosting)
    const startAtGoal = ref(false)
    useGettingStartedFocus(open)
    const providers = useProvidersStore()
    const tenant = useTenantStore()
    const auth = useAuthStore()
    auth.user = { userId: 'fixture-user', email: 'fixture@example.test' }
    const requests = []
    tenant.$patch({ orgUUID: 'demo', workspaceUUID: 'workspace', workspaceMode: 'workspace', orgs: [{ uuid: 'demo', displayName: 'Demo', role: 'admin' }], workspacesByOrg: { demo: [{ uuid: 'workspace', orgUUID: 'demo', displayName: 'My Workspace', role: 'admin', clusterName: 'workspace-cluster' }] } })
    providers.$patch({ loaded: true, bindingsWorkspace: 'workspace', bindingsOrgUUID: 'demo', bindingsRequestWorkspaceUUID: 'workspace', bindingsRequestOrgUUID: 'demo', bindingsLoadState: 'ready', catalogOrgUUID: 'demo', items: structuredClone(catalog), categories: [{ name: 'Infrastructure', icon: 'Server' }, { name: 'Development', icon: 'Layers' }, { name: 'AI', icon: 'Bot' }] })
    // Replace only network actions. Tests can hold/fail a request explicitly.
    let failure = null
    providers.enable = async (...args) => {
      requests.push(args)
      if (failure) throw new Error(failure)
      providers.bindingNamesByProvider = { ...providers.bindingNamesByProvider, [args[0].name]: `binding-${args[0].name}` }
    }
    providers.load = async () => {
      requests.push(['catalog-load'])
      providers.loading = false
      providers.error = null
      // Removing the org shadow reveals the platform catalog DTO again.
      if (selfHosting && !inventory.length) providers.items = structuredClone(selfHostingCatalog)
    }
    const orgProviders = useOrgProvidersStore()
    const inventory = manualResume
      ? [{ name: 'code', displayName: 'Code', workspacePath: 'root:orgs:demo:providers:code', registered: false, ready: false }]
      : []
    let installation = { phase: 'NotStarted', message: 'Ready to install on the selected Edge.', canRetry: true, attempt: 0 }
    let inventoryError = false
    if (selfHosting) {
      providers.items.find(p => p.name === 'code').serving.selfHosting = { supported: true }
      orgProviders.load = async () => {
        orgProviders.loaded = true
        orgProviders.loading = false
        orgProviders.error = inventoryError ? 'Fixture refresh failed' : null
        if (inventoryError) return false
        orgProviders.items = structuredClone(inventory)
        return true
      }
      orgProviders.loadInstallTargets = async () => {
        orgProviders.installTargets = [{ workspace: 'workspace', workspaceDisplayName: 'My Workspace', name: 'edge', eligible: true, connected: true }]
        orgProviders.installTargetsEligible = true
        orgProviders.installTargetsLoaded = true
      }
      const registration = () => ({
        provider: structuredClone(inventory[0]), kubeconfig: '# fixture credential only',
        instructions: { provider: 'code', namespace: 'railgrid-provider-code', releaseName: 'code', kubeconfigFilename: 'code.kubeconfig',
          steps: [{ title: 'Create the namespace', command: 'kubectl create namespace railgrid-provider-code', description: 'Run with cluster-admin credentials for edge.' },
            { title: 'Store the credential', command: 'kubectl -n railgrid-provider-code create secret generic railgrid-provider-kubeconfig --from-file=kubeconfig=./code.kubeconfig' },
            { title: 'Install the provider', command: 'helm upgrade --install code oci://ghcr.io/railgrid/charts/railgrid-code-provider --namespace railgrid-provider-code' }] },
      })
      orgProviders.register = async () => {
        requests.push(['register', 'code'])
        if (!inventory.length) inventory.push({ name: 'code', displayName: 'Code', workspacePath: 'root:orgs:demo:providers:code', registered: false, ready: false })
        await orgProviders.load()
        return registration()
      }
      orgProviders.instructions = async () => { requests.push(['instructions', 'code']); return registration() }
      orgProviders.remove = async name => {
        requests.push(['remove', name])
        const provider = inventory.find(item => item.name === name)
        if (provider) provider.deleting = true
        await orgProviders.load()
      }
      orgProviders.startInstallation = async (name, target, retry = false) => {
        requests.push(['install', name, target, retry])
        if (!inventory.length) inventory.push({ name, displayName: 'Code', workspacePath: 'root:orgs:demo:providers:code', registered: false, ready: false })
        installation = {
          phase: 'Installing',
          message: retry ? 'Retrying Helm release…' : 'Installing Helm release…',
          canRetry: false,
          attempt: retry ? installation.attempt + 1 : 1,
        }
        await orgProviders.load()
        return { ...installation }
      }
      orgProviders.getInstallation = async name => {
        requests.push(['installation-status', name])
        return { ...installation, phase: inventory[0]?.ready && installation.phase !== 'Failed' ? 'Ready' : installation.phase }
      }
      providers.enable = async (...args) => {
        requests.push(['enable', ...args])
        providers.bindingNamesByProvider = { code: 'binding-code' }
        providers.bindingsByProvider = { code: { bindingName: 'binding-code', selfHosted: true } }
      }
      providers.disable = async provider => { requests.push(['disable', provider.name]) }
    }
    window.fixture = {
      providers, tenant, open, requests, orgProviders, router,
      registerProvider() { inventory[0].registered = true },
      readyProvider() {
        inventory[0].registered = true
        inventory[0].ready = true
        inventory[0].hostingEdge = { name: 'edge', workspace: 'workspace', workspaceDisplayName: 'My Workspace' }
        inventory[0].version = '0.2.1'
        inventory[0].installedVersion = '0.2.1'
        const entry = providers.items.find(p => p.name === 'code')
        entry.scope = 'org'
        entry.orgUUID = 'demo'
        entry.shadowsPlatform = true
      },
      setInstallation(phase, message, canRetry = phase === 'Failed') {
        installation = {
          ...installation,
          phase,
          message,
          canRetry,
          ...(phase === 'Failed' ? { namespace: 'railgrid-provider-code', job: 'install-code-2' } : {}),
        }
      },
      completeRemoval(name) {
        const index = inventory.findIndex(item => item.name === name)
        if (index >= 0) inventory.splice(index, 1)
      },
      failInventory(value) { inventoryError = value },
      failEnable(message) { failure = message },
      setRole(role) { tenant.orgs[0].role = role; tenant.workspacesByOrg.demo[0].role = role },
      setCreationPolicy(policy) { tenant.orgs[0].catalogEntryCreation = policy },
      setWorkspaceBinding(copy) {
        const enabled = copy === 'organization' || copy === 'platform'
        providers.bindingNamesByProvider = enabled ? { code: 'binding-code' } : {}
        providers.bindingsByProvider = enabled ? {
          code: { bindingName: 'binding-code', exportPath: copy === 'organization' ? 'root:orgs:demo:providers:code' : 'root:providers:code', selfHosted: copy === 'organization' },
        } : {}
        providers.bindingsWorkspace = 'workspace'
        providers.bindingsOrgUUID = 'demo'
        providers.bindingsRequestWorkspaceUUID = 'workspace'
        providers.bindingsRequestOrgUUID = 'demo'
        providers.bindingsStale = false
        providers.bindingsLoadState = 'ready'
      },
      async refreshBindings() { requests.push(['refresh-bindings']); return undefined },
      async disableProvider(name) { requests.push(['disable', name]) },
      resetCatalog() { providers.items = structuredClone(catalog) },
    }
    return () => h(Fragment, null, [selfHosting ? h(ProvidersPage) : h(AppLayout, null, { default: () => open.value
      ? h(WelcomeWizard, { startAtGoal: startAtGoal.value, onDismiss: () => { open.value = false } })
      : h('button', { class: 'k-btn k-btn--primary', onClick: () => { startAtGoal.value = true; open.value = true } }, 'Getting started') }), h(ConfirmDialog), h(ToastHost, { owner: 'primary' })])
  },
})
app.use(createPinia())
app.use(router)
await router.isReady()
app.mount('#app')
