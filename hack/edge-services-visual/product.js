import '../../providers/edges/portal/src/main'
import './style.css'
import { installFixtureNavigation } from './product-navigation.mjs'
import { createFixtureFetch } from './product-fetch.mjs'

const isWorkloads = new URLSearchParams(location.search).get('view') === 'workloads'
const isCreate = new URLSearchParams(location.search).get('view') === 'create'
const isWorkloadCreate = new URLSearchParams(location.search).get('view') === 'workload-create'
const pageFailure = new URLSearchParams(location.search).get('scenario') === 'page-failure'
const emptyRefresh = new URLSearchParams(location.search).get('scenario') === 'empty-refresh'
const staleRefresh = new URLSearchParams(location.search).get('scenario') === 'stale-refresh'
const slowRefresh = new URLSearchParams(location.search).get('scenario') === 'slow-refresh'
const slowEmpty = new URLSearchParams(location.search).get('scenario') === 'slow-empty'
const firstRun = new URLSearchParams(location.search).get('scenario') === 'first-run'
const firstRunNoEdge = new URLSearchParams(location.search).get('scenario') === 'first-run-no-edge'
const targets = new URLSearchParams(location.search).get('scenario') === 'targets'
document.title = isWorkloads ? 'Edge Workloads product review' : 'Edge Services product review'
const fixtureFetch = createFixtureFetch({ origin: location.origin, isWorkloads, isWorkloadCreate, pageFailure, emptyRefresh, staleRefresh, slowRefresh, slowEmpty, firstRun, firstRunNoEdge, targets })

// Emulate the host's context property and navigation event contract. Preserve
// the existing query links while allowing refresh and browser back/forward.
const initialPath = isWorkloadCreate ? 'deploy/workload/manual' : isCreate ? 'create/service' : isWorkloads ? 'workloads' : 'services'
const app = document.querySelector('#app')
app.innerHTML = `<main class="example"><div class="fixture-controls"><button type="button" class="k-btn k-btn--ghost" id="theme">Switch to dark theme</button></div><p class="fixture-note">Simulated ${isWorkloads || isWorkloadCreate ? 'workloads' : 'services'}; ${firstRun || firstRunNoEdge ? 'first-use selection is simulated' : isCreate || isWorkloadCreate ? 'creation is simulated' : slowEmpty ? 'a delayed refresh returns an empty collection' : slowRefresh ? 'subsequent refreshes are delayed' : emptyRefresh || staleRefresh ? 'the first refresh requested after 10 seconds fails' : pageFailure ? 'the first request for page two fails' : 'the first delete fails and the next succeeds'}. No hub is contacted.</p><railgrid-provider-edges></railgrid-provider-edges></main>`
const element = app.querySelector('railgrid-provider-edges')
let theme = 'light'
const updateContext = installFixtureNavigation({ element, initialPath, fixtureFetch, getTheme: () => theme, window })
app.querySelector('#theme').addEventListener('click', event => {
  theme = theme === 'light' ? 'dark' : 'light'
  document.documentElement.classList.toggle('light', theme === 'light')
  event.currentTarget.textContent = `Switch to ${theme === 'light' ? 'dark' : 'light'} theme`
  updateContext()
})
