import { createApp, h, KeepAlive, nextTick, ref } from 'vue'
import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'
import '../../portal/src/assets/main.css'
import '../../providers/code/portal/src/style.css'
import ConnectionsView from '../../providers/code/portal/src/views/ConnectionsView.vue'
import RepositoriesView from '../../providers/code/portal/src/views/RepositoriesView.vue'
import PackagesView from '../../providers/code/portal/src/views/PackagesView.vue'
import { api } from '../../providers/code/portal/src/api'
import ConfirmDialog from '../../providers/code/portal/src/portalkit/ConfirmDialog.vue'
import { createResourceDeletions } from '../../providers/code/portal/src/refresh'

let failConnections = false
let failRepositories = false
const view = new URLSearchParams(location.search).get('view')
const repositories = view === 'repositories'
const packages = view === 'packages'
const scenario = new URLSearchParams(location.search).get('scenario')
const oauthFailure = !repositories && !packages && scenario === 'oauth-failure'
let oauthReads = 0
const packageFailure = packages && scenario === 'search-failure'
let failPackageSearch = packageFailure
const linkScenario = scenario === 'links' || packageFailure
const repositoryFirstRun = repositories && new URLSearchParams(location.search).get('scenario') === 'repository-first-run'
let sampleRepositories = [{ name: 'railgrid-console', repo: 'platform/railgrid-console', connectionRef: 'github-team', visibility: 'Private', ready: true, htmlURL: 'https://github.com/example/railgrid-console' }]
let sampleConnections = [{ name: 'github-team', provider: 'github', type: 'pat', owner: 'platform', secretName: 'git-token', scopes: [], validated: true }]
const samplePackages = [{ name: 'railgrid-console', repositoryRef: 'railgrid-console', type: 'container', visibility: 'private', versionCount: 4, ready: true }]
// All operations available in the mounted views stay inside this fixture.
api.deleteConnection = async name => {
  sampleConnections = sampleConnections.filter(connection => connection.name !== name)
}
api.deleteRepository = async name => {
  sampleRepositories = sampleRepositories.filter(repository => repository.name !== name)
}
api.oauthConfig = async () => {
  if (oauthFailure) {
    oauthReads++
    if (oauthReads === 1) throw new Error('Simulated GitHub configuration failure')
    await new Promise(resolve => setTimeout(resolve, 2000))
    return { enabled: true }
  }
  return { enabled: new URLSearchParams(location.search).get('oauth') === 'enabled' }
}
api.listConnections = async () => {
  await new Promise(resolve => setTimeout(resolve, 400))
  if (failConnections) throw new Error('Simulated connection read failure')
  return linkScenario || repositoryFirstRun ? sampleConnections : []
}
api.listRepositoriesPage = async () => {
  await new Promise(resolve => setTimeout(resolve, 400))
  if (failRepositories) throw new Error('Simulated repository read failure')
  return { items: linkScenario ? sampleRepositories : [], continue: '', resourceVersion: '' }
}
api.listRepositories = async () => linkScenario ? sampleRepositories : []
api.listAllPackagesPage = async () => ({ items: linkScenario ? samplePackages : [], continue: packageFailure ? 'next' : '', resourceVersion: '' })
api.listAllPackages = async () => {
  if (failPackageSearch) throw new Error('Simulated package search failure')
  return linkScenario ? samplePackages : []
}

export const fixtureApp = createApp({
  setup() {
    const show = ref(true)
    const theme = ref('light')
    const selected = ref('')
    const deletions = createResourceDeletions()
    async function failRefresh(kind) {
      if (kind === 'connections') failConnections = true
      else failRepositories = true
      show.value = false
      await nextTick()
      show.value = true
    }
    return () => h('main', { style: 'padding: 24px' }, [
      ...(!packages ? [h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => failRefresh(repositories ? 'repositories' : 'connections') }, 'Simulate failed refresh')] : []),
      ...(repositories ? [h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => failRefresh('connections') }, 'Simulate failed connection refresh')] : []),
      ...(!packages ? [h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { failConnections = false; failRepositories = false } }, 'Allow retry to succeed')] : []),
      ...(packageFailure ? [h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { failPackageSearch = false } }, 'Allow search retry to succeed')] : []),
      h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { theme.value = theme.value === 'light' ? 'dark' : 'light'; document.documentElement.classList.toggle('light', theme.value === 'light') } }, `Switch to ${theme.value === 'light' ? 'dark' : 'light'} theme`),
      h('p', oauthFailure ? 'Simulated GitHub configuration failure; Retry succeeds after two seconds. No hub is contacted.' : linkScenario || repositoryFirstRun ? 'Sample resources and simulated reads; no hub is contacted.' : `Simulated empty ${repositories ? 'repositories and connections' : 'connections'} and reads; no hub is contacted.`),
      h('p', { role: 'status' }, selected.value ? `Selected ${selected.value}.` : ''),
      h(ConfirmDialog),
      h('railgrid-provider-code', [h(KeepAlive, null, { default: () => show.value ? h(packages ? PackagesView : repositories ? RepositoriesView : ConnectionsView, { deletions, onCreate: method => { selected.value = method || 'create' }, onCreateConnection: () => { selected.value = 'connection prerequisite' } }) : null })]),
    ])
  },
})
fixtureApp.mount('#app')
