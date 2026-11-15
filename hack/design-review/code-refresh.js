import { createApp, ref } from 'vue'
import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'
import '../../portal/src/assets/main.css'
import '../../providers/code/portal/src/style.css'
import ConnectionDetailView from '../../providers/code/portal/src/views/ConnectionDetailView.vue'
import RepoDetailView from '../../providers/code/portal/src/views/RepoDetailView.vue'
import { api } from '../../providers/code/portal/src/api'
import { createResourceDeletions } from '../../providers/code/portal/src/refresh'

const repository = { name: 'orders', uid: 'review-repo', generation: 1, observedGeneration: 1, connectionRef: 'github', repo: 'orders', owner: 'railgrid', visibility: 'private', defaultBranch: 'main', ready: true, conditions: [{ type: 'Ready', status: 'True', reason: 'Reconciled', message: 'Repository is ready.' }] }
const connection = { name: 'github', uid: 'review-connection', generation: 1, observedGeneration: 1, provider: 'github', type: 'pat', owner: 'railgrid', secretName: 'github-token', scopes: ['repo'], validated: true, conditions: [{ type: 'Validated', status: 'True', reason: 'Verified', message: 'Credential validated.' }] }
const reads = { connection: 0, repository: 0 }
async function sample(kind, value) {
  if (++reads[kind] > 1) await new Promise(resolve => setTimeout(resolve, 3000))
  return value
}
api.getConnection = () => sample('connection', connection)
api.getRepository = () => sample('repository', repository)
api.listConnections = async () => [connection]
api.listDeployKeys = async () => []
api.listCollaborators = async () => []
api.listPackagesPage = async () => ({ items: [], continue: '', resourceVersion: '' })
api.listPackages = async () => []
const rejectMutation = async () => { throw new Error('Changes are unavailable in this fixture.') }
for (const method of ['deleteConnection', 'deleteRepository', 'updateRepositoryConnection', 'createDeployKey', 'deleteDeployKey', 'createCollaborator', 'deleteCollaborator']) api[method] = rejectMutation

createApp({
  components: { ConnectionDetailView, RepoDetailView },
  setup() {
    const theme = ref('light')
    const deletions = createResourceDeletions()
    const repositoryView = new URLSearchParams(location.search).get('view') === 'repository'
    function toggleTheme() { theme.value = theme.value === 'light' ? 'dark' : 'light'; document.documentElement.classList.toggle('light', theme.value === 'light') }
    return { theme, deletions, repositoryView, toggleTheme }
  },
  template: `<main style="padding: 24px"><button type="button" class="k-btn k-btn--ghost" @click="toggleTheme">Switch to {{ theme === 'light' ? 'dark' : 'light' }} theme</button><p>Simulated Code data. Refresh takes three seconds; no hub is contacted.</p><nav><a href="?view=connection">Connection</a> · <a href="?view=repository">Repository</a></nav><railgrid-provider-code><RepoDetailView v-if="repositoryView" name="orders" :deletions="deletions" /><ConnectionDetailView v-else name="github" :deletions="deletions" /></railgrid-provider-code></main>`,
}).mount('#app')
