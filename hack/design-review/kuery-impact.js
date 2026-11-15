import { createApp, h, ref } from 'vue'
import ImpactView from '../../providers/kuery/portal/src/components/ImpactView.vue'
import '@fontsource-variable/instrument-sans'
import '@fontsource/ibm-plex-mono/400.css'
import '@/assets/main.css'
import '../../providers/kuery/portal/src/style.css'
import './kuery-impact.css'

const object = (id, kind, name, namespace = '') => ({ id, cluster: 'review/edge-west', object: { apiVersion: kind === 'Deployment' ? 'apps/v1' : 'v1', kind, metadata: { name, ...(namespace ? { namespace } : {}) } } })
const anchor = object('web', 'Deployment', 'web', 'production')
const result = { ...anchor, relations: {
  owners: [object('owner', 'Deployment', 'platform', 'production')],
  references: [object('config', 'ConfigMap', 'web-config', 'production')],
  'descendants+': [object('pod', 'Pod', 'web-7d9b', 'production')],
  'selected-by': [object('service', 'Service', 'web', 'production')],
  'linked+': [object('peer', 'Deployment', 'worker', 'production')],
} }
const scenario = new URLSearchParams(location.search).get('scenario')
const empty = scenario === 'empty'
if (scenario === 'all-relations') Object.assign(result.relations, {
  selects: [object('selector', 'Service', 'traffic-router', 'production')],
  grouped: [object('group', 'ConfigMap', 'workload-group', 'production')],
  namespace: [object('namespace', 'Namespace', 'production')],
  namespaced: [object('contained', 'Pod', 'web-sidecar', 'production')],
})
if (scenario === 'unknown-relation') result.relations['unrecognized-relation'] = [object('unknown', 'ConfigMap', 'unclassified', 'production')]
if (scenario === 'long-relations') result.relations[`unrecognized-${'relationship'.repeat(8)}`] = [object('long', 'ConfigMap', `configuration-${'identifier'.repeat(10)}`, 'production')]
if (scenario === 'relation-aliases') result.relations = {
  descendants: [object('pod', 'Pod', 'web-7d9b', 'production')],
  linked: [object('peer', 'Deployment', 'worker', 'production')],
}

createApp({
  setup() {
    const theme = ref('light')
    const selected = ref('')
    const currentAnchor = ref(anchor)
    const refreshVersion = ref(0)
    const missing = ref(false)
    const slow = ref(false)
    const workspace = ref('review-workspace')
    const transportRevoked = ref(false)
    const deniedFetch = async () => new Response('{}', { status: 403 })
    const fetchFixture = async () => {
      const isMissing = missing.value
      if (slow.value) await new Promise(resolve => setTimeout(resolve, 1500))
      const current = currentAnchor.value
      const data = current.id === anchor.id && !empty ? result : { ...current, relations: {} }
      return new Response(JSON.stringify({ result: { objects: isMissing ? [] : [data] } }), { headers: { 'Content-Type': 'application/json' } })
    }
    function toggleTheme() {
      theme.value = theme.value === 'light' ? 'dark' : 'light'
      document.documentElement.classList.toggle('light', theme.value === 'light')
    }
    return () => h('main', { class: 'impact-fixture' }, [
      h('div', { class: 'impact-fixture__controls' }, [
        h('a', { href: '/?entry=design.quality.known-oddities', class: 'k-btn k-btn--ghost' }, 'Back to design book'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: toggleTheme }, `Switch to ${theme.value === 'light' ? 'dark' : 'light'} theme`),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { missing.value = !missing.value; refreshVersion.value++ } }, missing.value ? 'Restore sample result' : 'Simulate missing object'),
        ...(missing.value ? [h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { missing.value = false } }, 'Allow next read to succeed')] : []),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', 'aria-pressed': slow.value, onClick: () => { slow.value = !slow.value } }, slow.value ? 'Slow reads on' : 'Slow reads off'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { workspace.value = workspace.value === 'review-workspace' ? 'other-workspace' : 'review-workspace' } }, 'Switch sample workspace'),
        h('button', { type: 'button', class: 'k-btn k-btn--ghost', onClick: () => { transportRevoked.value = !transportRevoked.value } }, transportRevoked.value ? 'Restore sample access' : 'Simulate access change'),
      ]),
      h('p', { class: 'impact-fixture__note' }, `Synthetic relationship data in ${workspace.value}${scenario === 'all-relations' ? '; all relation types are combined for palette review, not as a realistic topology' : ''}. This fixture does not contact a hub or change resources.`),
      h('railgrid-provider-kuery', [h(ImpactView, {
        context: { basePath: '/ui/providers/kuery/', tenant: workspace.value, orgUUID: 'review-org', workspaceUUID: workspace.value, token: `fixture-${refreshVersion.value}`, theme: theme.value, fetch: transportRevoked.value ? deniedFetch : fetchFixture },
        anchor: currentAnchor.value, savedView: 'review', onInspect: row => { currentAnchor.value = row; selected.value = `${row.object.kind} ${row.object.metadata.name} selected in the fixture.` },
      })]),
      h('p', { role: 'status', class: 'impact-fixture__note' }, selected.value),
    ])
  },
}).mount('#app')
