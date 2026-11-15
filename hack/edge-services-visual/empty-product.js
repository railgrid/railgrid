import '../../providers/edges/portal/src/main'
import './style.css'

const params = new URLSearchParams(location.search)
const noEdge = params.get('scenario') === 'no-edge'
const fail = params.get('scenario') === 'failure'
const theme = params.get('theme') === 'dark' ? 'dark' : 'light'
const element = document.querySelector('railgrid-provider-edges')
const status = document.querySelector('[role=status]')
document.documentElement.classList.toggle('light', theme === 'light')

function json(body, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

async function fixtureFetch(input) {
  const { pathname } = new URL(String(input), location.origin)
  if (pathname.endsWith('/services') && fail) {
    return json({ kind: 'Status', message: 'Could not load services.' }, 503)
  }
  if (pathname.endsWith('/service-catalog')) return json([])
  const items = pathname.endsWith('/linuxservers') && !noEdge
    ? [{ metadata: { name: 'home-server' }, spec: {}, status: { phase: 'Ready', connected: true } }]
    : []
  return json({ kind: 'List', metadata: { resourceVersion: '1' }, items })
}

element.railgridContext = { tenant: 'review-workspace', fetch: fixtureFetch, theme, subPath: 'services' }
element.addEventListener('railgrid-navigate', ({ detail }) => {
  status.textContent = `Selected ${detail.path} in the fixture.`
})
