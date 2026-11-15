import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'
import '@/assets/main.css'
import '../../providers/quickstart/portal/src/style.css'
import { QuickstartElement } from '../../providers/quickstart/portal/src/element.ts'

customElements.define('railgrid-provider-quickstart', QuickstartElement)
let failNext = false
const populated = new URLSearchParams(location.search).get('scenario') === 'populated'
const items = populated ? [{ apiVersion: 'quickstart.providers.railgrid.ai/v1alpha1', kind: 'Greeting', metadata: { name: 'welcome' }, spec: { message: 'Hello from the workspace' }, status: { observedAt: '2026-11-14T12:00:00Z', conditions: [{ type: 'Ready', status: 'True' }] } }] : []
const root = document.querySelector('#root')
const controls = document.createElement('div')
controls.innerHTML = '<button type="button" class="k-btn k-btn--ghost" data-fail>Fail next refresh</button> <button type="button" class="k-btn k-btn--ghost" data-theme>Switch theme</button><p>Simulated reads and greeting actions; no hub is contacted.</p>'
root.append(controls)
controls.querySelector('[data-fail]').addEventListener('click', () => { failNext = true })
controls.querySelector('[data-theme]').addEventListener('click', () => document.documentElement.classList.toggle('light'))
const element = document.createElement('railgrid-provider-quickstart')
root.append(element)
element.railgridContext = {
  tenant: 'fixture-workspace', user: { sub: 'fixture-user' },
  fetch: async (input, init = {}) => {
    if (init.method === 'POST' && populated && String(input).endsWith('/greet')) {
      await new Promise(resolve => setTimeout(resolve, 1000))
      return new Response(JSON.stringify({ result: { greeting: 'Hello from the workspace' } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (init.method && init.method !== 'GET') return new Response('Read-only fixture', { status: 405 })
    await new Promise(resolve => setTimeout(resolve, 1000))
    if (failNext) {
      failNext = false
      return new Response(JSON.stringify({ apiVersion: 'v1', kind: 'Status', status: 'Failure', message: 'Simulated read failure.', code: 503 }), { status: 503, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ apiVersion: 'v1', kind: 'GreetingList', metadata: { resourceVersion: '1' }, items }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  },
}
