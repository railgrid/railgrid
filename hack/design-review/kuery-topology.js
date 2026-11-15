import { createApp, h, ref, vShow, withDirectives } from 'vue'
import TopologyView from '../../providers/kuery/portal/src/components/TopologyView.vue'
import ImpactView from '../../providers/kuery/portal/src/components/ImpactView.vue'
import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'
import '@/assets/main.css'
import '../../providers/kuery/portal/src/style.css'
import './kuery-impact.css'

const cluster = { id: 'edge-west', cluster: 'review/edge-west', relations: { members: [
  { id: 'web', cluster: 'review/edge-west', object: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'web', namespace: 'production' } } },
  { id: 'pod', cluster: 'review/edge-west', object: { apiVersion: 'v1', kind: 'Pod', metadata: { name: 'web-7d9b', namespace: 'production' } } },
] } }
export const TopologyFixture = {
  setup() {
    const theme = ref('light')
    const fail = ref(false)
    const version = ref(0)
    const workspace = ref('review-workspace')
    const impact = ref(null)
    const fetchFixture = async (_input, init) => {
      const query = JSON.parse(init.body).input.query
      const requestedEdge = query.cluster?.name
      const filters = query.filter?.objects || []
      const members = cluster.relations.members
      const objects = query.root === 'clusters' ? (requestedEdge === 'review/edge-east' ? [] : [cluster]) : members.filter(member => filters.some(filter => filter.id ? filter.id === member.id : filter.name === member.object.metadata.name)).map(member => ({ ...member, relations: {} }))
      await new Promise(resolve => setTimeout(resolve, 600))
      return new Response(JSON.stringify(fail.value ? { message: 'Topology temporarily unavailable.' } : { result: { objects } }), { status: fail.value ? 503 : 200, headers: { 'Content-Type': 'application/json' } })
    }
    function toggleTheme() { theme.value = theme.value === 'light' ? 'dark' : 'light'; document.documentElement.classList.toggle('light', theme.value === 'light') }
    return () => h('main', { class: 'impact-fixture' }, [
      h('div', { class: 'impact-fixture__controls' }, [
        h('a', { href: '/?entry=design.components.relationship-graph', class: 'k-btn k-btn--ghost' }, 'Back to design book'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: toggleTheme }, `Switch to ${theme.value === 'light' ? 'dark' : 'light'} theme`),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { fail.value = true; version.value++ } }, 'Simulate failed refresh'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { fail.value = true } }, 'Fail next read'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { workspace.value = workspace.value === 'review-workspace' ? 'other-workspace' : 'review-workspace'; impact.value = null } }, 'Switch sample workspace'),
        ...(fail.value ? [h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { fail.value = false } }, 'Allow retry to succeed')] : []),
      ]),
      h('p', { class: 'impact-fixture__note' }, `Synthetic topology data in ${workspace.value}. This fixture does not contact a hub or change resources.`),
      h('railgrid-provider-kuery', [
        impact.value ? h(ImpactView, { context: { basePath: '/ui/providers/kuery/', tenant: workspace.value, orgUUID: 'review-org', workspaceUUID: workspace.value, token: `fixture-${version.value}`, theme: theme.value, fetch: fetchFixture }, anchor: impact.value, savedView: 'review', onBack: () => { impact.value = null }, onInspect: row => { impact.value = row } }) : null,
        withDirectives(h(TopologyView, { context: { basePath: '/ui/providers/kuery/', tenant: workspace.value, orgUUID: 'review-org', workspaceUUID: workspace.value, token: `fixture-${version.value}`, theme: theme.value, fetch: fetchFixture }, edges: ['review/edge-west', 'review/edge-east'], active: !impact.value, savedView: 'review', onInspect: row => { impact.value = row } }), [[vShow, !impact.value]]),
      ]),
    ])
  },
}

if (document.querySelector('#app')) createApp(TopologyFixture).mount('#app')
