<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { Activity, AlertCircle, AlertTriangle, Check, CheckCircle, Clock, Cloud, Command, Database, Diamond, Hexagon, Inbox, Loader2, Plus, Puzzle, RefreshCw, Server, Sparkles, Target, Trash2, X, XCircle, Zap } from 'lucide-vue-next'
import { useThemeStore } from '@/stores/theme'
import { categoryIcons } from '@/lib/categoryIcons'
import { ic } from '../../provider-sdk/portalkit/icons'
import { normalizeProviderAgentContext } from '../../provider-sdk/portalkit/agent-context'
import { RAILGRID_UI_CORE_VERSION } from '../../provider-sdk/portalkit/styles'
import { RELATION_METADATA } from '../../providers/kuery/portal/src/graph'
import edgeIcon from '../../providers/edges/portal/public/icon.svg?url'

const props = defineProps({ stage: String, catalog: Object, entry: Object })
const theme = useThemeStore()
const values = ref({})
const assetQuery = ref('')
const device = ref({})
const workspace = ref(false)
const recovery = ref(false)
const rejectContext = ref(false)
const assetRows = computed(() => props.catalog.assets.filter(item => [item.name, item.contract, item.source.path].join(' ').toLowerCase().includes(assetQuery.value.toLowerCase().trim())))
const kitVersion = computed(() => values.value['--railgrid-ui-core-version'])
const swatches = computed(() => props.catalog.tokens.filter(item => item.name.startsWith('--color-')))
const selection = ref('Applications')
const overlayMessage = ref('')
const lensIcons = [
  [Loader2, 'Loading'], [CheckCircle, 'Success'], [Check, 'Confirm'], [XCircle, 'Failure'], [X, 'Dismiss'],
  [AlertTriangle, 'Warning'], [AlertCircle, 'Error detail'], [Plus, 'Create'], [Trash2, 'Delete'], [Inbox, 'Empty'],
  [Clock, 'Pending'], [RefreshCw, 'Retry'], [Puzzle, 'Fallback provider'], [Sparkles, 'Assistant'], [Hexagon, 'Brand'],
  [Diamond, 'Abstract marker'], [Zap, 'Activity'], [Command, 'Command'], [Target, 'Target'], [Cloud, 'Cloud'], [Server, 'Server'], [Database, 'Database'],
]
const sizes = [[24, 1.5, 'Decorative'], [16, 1.75, 'Standard'], [14, 1.75, 'Dense'], [12, 2.25, 'Micro']]
const exampleContext = computed(() => rejectContext.value
  ? { version: 1, title: 'Rollout record', resources: [{ apiVersion: 'edges.railgrid.ai/v1alpha1', kind: 'Edge', name: '/not/a/resource' }], data: { diagnostic: 'x'.repeat(8193) } }
  : { version: 1, title: 'Rollout record', summary: 'The selected edge is in staging.', resources: [{ apiVersion: 'edges.railgrid.ai/v1alpha1', kind: 'Edge', name: 'staging-west', resource: 'edges', uid: 'fixture-uid', resourceVersion: '17' }], data: { region: 'west', nodesReady: 3 } })
const normalized = computed(() => normalizeProviderAgentContext(exampleContext.value))
const contextCode = computed(() => JSON.stringify(exampleContext.value, (key, value) => key === 'diagnostic' ? '(sample string of ' + value.length + ' bytes)' : value, 2))
const assess = [
  ['Keyboard', 'Open FormSelect and ActionMenu; use Arrow keys, Enter, Escape, and Tab. Check focus returns to the owning trigger.', 'design.components.form-select'],
  ['Live region', 'Refresh a settled empty snapshot and compare it with a failed first read. Verify the announced state with a screen reader.', 'design.patterns.resource-reads'],
  ['Coarse pointer', 'Measure the target and the compact checkbox glyph. Use a touch device or browser emulation for the media query.', 'design.patterns.controls'],
  ['Contrast and motion', 'Compare semantic text in both themes. Turn on the OS reduced-motion setting and advance progress.', 'design.foundations.geometry'],
  ['Categorical graph', 'Compare line style as well as color. A complete assistive-technology and color-blindness review is still open.', 'design.quality.known-oddities'],
]
const checks = [
  ['Design catalog', 'make verify-design-docs', 'Metadata, local links, stable IDs, and component document sections.'],
  ['Distribution parity', 'make verify-portalkit', 'Canonical source manifests and byte-identical consumer copies.'],
  ['UI conformance', 'make verify-ui-conformance', 'Shared vocabulary, allowed colors and radii, and exact exceptions.'],
  ['Browser and manual review', 'docs/dev-browser.md', 'Rendered appearance, keyboard, screen readers, touch, reduced motion, and end-to-end behavior.'],
]
const checklist = [
  ['Token color, radius, and glow', 'design.foundations.recipes'],
  ['Visible labels and read-state copy', 'design.content.ui-copy'],
  ['Keyboard, touch, contrast, and motion', 'design.accessibility.interaction'],
  ['Square status tags', 'design.components.status-badge'],
  ['Flat route tabs', 'design.components.tabs'],
  ['Light and dark; fixed-dark Dex', 'design.foundations.theming'],
  ['Shared primitives and source assets', 'design.components.portalkit-assets'],
  ['Fluid shell and local readable measure', 'design.patterns.fluid-shell'],
  ['Creation surface appropriate to the task', 'design.patterns.resource-creation'],
  ['Mono identifiers and aligned figures', 'design.foundations.typography'],
  ['Icon source, meaning, and geometry', 'design.foundations.iconography'],
]
function measure() {
  const css = getComputedStyle(document.documentElement)
  values.value = Object.fromEntries([...props.catalog.tokens.map(item => item.name), '--railgrid-ui-core-version', '--railgrid-ui-canonical'].map(key => [key, css.getPropertyValue(key).trim()]))
  const target = document.querySelector('#review-touch-target')
  const glyph = target?.querySelector('input')
  device.value = { coarse: matchMedia('(any-pointer: coarse)').matches, reduced: matchMedia('(prefers-reduced-motion: reduce)').matches, width: Math.round(target?.getBoundingClientRect().width || 0), height: Math.round(target?.getBoundingClientRect().height || 0), glyph: Math.round(glyph?.getBoundingClientRect().width || 0) }
}
watch(() => [theme.resolved, props.stage], async () => { await nextTick(); measure() }, { immediate: true })
const handoff = ref('')
const handoffFrame = ref(null)
function runHandoff() {
  handoffFrame.value.src = './handoff.html?run=' + Date.now()
  handoff.value = 'Loading isolated fixture…'
}
function reportHandoff() {
  const probe = handoffFrame.value?.contentDocument?.querySelector('[data-result]')
  if (probe) handoff.value = probe.textContent
}
</script>

<template>
  <div v-if="stage === 'colors'" class="spec-stack">
    <div class="spec-token-grid">
      <a v-for="item in swatches" :key="item.name" :href="item.source.href" class="spec-token" :title="values[item.name]">
        <span class="spec-swatch" :style="{ background: 'var(' + item.name + ')' }"></span>
        <span><code>{{ item.name.replace('--color-', '') }}</code><small>{{ values[item.name] || 'Not resolved' }}</small></span>
      </a>
    </div>
    <section class="spec-section">
      <h3>Text against the four surface levels</h3>
      <div class="spec-grid">
        <div v-for="surface in ['surface', 'surface-raised', 'surface-overlay', 'surface-hover']" :key="surface" class="spec-tile" :style="{ background: 'var(--color-' + surface + ')' }">
          <strong>{{ surface }}</strong><p v-for="tone in ['primary', 'secondary', 'muted', 'success', 'warning', 'danger']" :key="tone" :style="{ color: 'var(--color-' + (['primary','secondary','muted'].includes(tone) ? 'text-' : '') + tone + ')' }">{{ tone }} · staging-west</p>
        </div>
      </div>
    </section>
  </div>

  <div v-else-if="stage === 'type'" class="spec-stack">
    <div class="type-display spec-display">Railgrid</div>
    <div class="spec-grid">
      <section class="spec-section"><h3>Instrument Sans · interface</h3><p>Connect infrastructure to your workspace.</p><button class="k-btn k-btn--primary" type="button" @click="selection = 'Edge created in the fixture'">Create edge</button><p v-if="selection !== 'Applications'" role="status" class="spec-note">{{ selection }}</p></section>
      <section class="spec-section"><h3>IBM Plex Mono · data</h3><div class="spec-row"><code>edge/staging-west</code><span class="k-badge k-badge--success">Ready</span></div><code>2026-11-11 16:42:08Z</code></section>
      <section class="spec-section"><div class="k-eyebrow">Active connections</div><div class="k-kpi">1,028</div></section>
      <section class="spec-section"><h3>Tabular numerals</h3><div class="spec-numerals"><div><span>Edge 01</span><span>1,028</span></div><div><span>Edge 02</span><span>8,161</span></div><div><span>Edge 03</span><span>4,392</span></div></div></section>
    </div>
  </div>

  <div v-else-if="stage === 'icons'" class="spec-stack">
    <div class="spec-icon-grid"><div v-for="[Icon, label] in lensIcons" :key="label"><component :is="Icon" :size="16" :stroke-width="1.75" :class="label === 'Loading' ? 'k-spin' : undefined" aria-hidden="true" /><span>{{ label }}</span></div></div>
    <section class="spec-section"><h3>Scale and stroke</h3><div class="spec-row"><div v-for="[size, stroke, label] in sizes" :key="label" class="spec-icon"><Hexagon :size="size" :stroke-width="stroke" aria-hidden="true" /><span>{{ label }} · {{ size }} / {{ stroke }}</span></div></div></section>
    <section class="spec-section"><h3>Provider identity and vanilla rendering</h3><div class="spec-row"><span class="spec-icon"><img :src="edgeIcon" width="14" height="14" alt=""> Edges</span><span v-for="(Icon, name) in categoryIcons" :key="name" class="spec-icon"><component :is="Icon" :size="14" :stroke-width="1.75" aria-hidden="true" /> {{ name }}</span><span class="spec-icon"><span v-html="ic('plus')" aria-hidden="true"></span> PortalKit add</span></div></section>
  </div>

  <div v-else-if="stage === 'theme'" class="spec-stack">
    <div class="spec-row"><span class="k-badge k-badge--muted">Portal: {{ theme.mode }} → {{ theme.resolved }}</span><span class="k-badge k-badge--muted">Dex: fixed dark</span></div>
    <iframe class="spec-dex" title="Fixed-dark Dex login style specimen" src="./dex.html"></iframe>
  </div>

  <div v-else-if="stage === 'assets'" class="spec-stack">
    <label class="spec-field spec-fit">Filter asset names or contracts<input type="search" class="k-input" v-model="assetQuery"></label>
    <p class="spec-note" role="status">{{ assetRows.length }} assets</p>
    <div class="k-table spec-scroll"><table class="spec-data"><thead><tr><th>Canonical asset</th><th>Contract</th><th>Book / manifest</th></tr></thead><tbody>
      <tr v-for="item in assetRows" :key="item.source.path"><td><a :href="item.source.href">{{ item.source.path }}</a></td><td><a :href="'?entry=' + item.entry">{{ item.contract }}</a></td><td><a v-if="item.spec" :href="item.spec.href">Book L{{ item.spec.line }}</a><span v-if="item.spec"> · </span><a :href="item.manifest.href">Manifest L{{ item.manifest.line }}</a></td></tr>
    </tbody></table></div>
  </div>

  <div v-else-if="stage === 'integration'" class="spec-stack">
    <dl class="spec-definition"><div><dt>Host canonical marker</dt><dd><code>{{ values['--railgrid-ui-canonical'] || 'Absent' }}</code></dd></div><div><dt>Host core stylesheet version</dt><dd><code>{{ kitVersion || 'Absent' }}</code></dd></div><div><dt>PortalKit required version</dt><dd><code>{{ RAILGRID_UI_CORE_VERSION }}</code></dd></div><div><dt>Shared CSS handoff</dt><dd><code>{{ Number(kitVersion) >= RAILGRID_UI_CORE_VERSION ? 'Compatible' : 'Fallback required' }}</code></dd></div></dl>
    <div class="spec-row"><button type="button" class="k-btn k-btn--ghost" @click="runHandoff">Run isolated fallback probe</button><a class="review-source-link" href="?entry=design.components.portalkit-assets">Inspect distributed assets</a></div>
    <p v-if="handoff" class="spec-note" role="status">{{ handoff }}</p><iframe ref="handoffFrame" title="Isolated PortalKit style probe" hidden @load="reportHandoff"></iframe>
  </div>

  <div v-else-if="stage === 'context'" class="spec-stack">
    <label class="k-checkbox-hit"><input class="k-checkbox" type="checkbox" v-model="rejectContext"> Include an invalid reference and oversized value</label>
    <div class="spec-grid"><section class="spec-section"><h3>Published example</h3><pre class="spec-code">{{ contextCode }}</pre></section><section class="spec-section"><h3>Normalized result</h3><pre class="spec-code">{{ JSON.stringify(normalized, null, 2) }}</pre></section></div>
    <p class="spec-note" role="status">{{ normalized ? 'Context is eligible for display. Selection remains explicit.' : 'The context payload was rejected.' }}</p>
  </div>

  <div v-else-if="stage === 'shell'" class="spec-stack">
    <a class="k-btn k-btn--primary" href="./onboarding.html" target="_blank" rel="noopener">Open the mounted portal shell</a>
    <div class="spec-shell-body">
      <div class="k-table"><table class="spec-data"><thead><tr><th>Edge connection</th><th>Status</th><th>Workspace</th></tr></thead><tbody><tr><td><code>staging-west</code></td><td><span class="k-badge k-badge--success">Ready</span></td><td>Platform</td></tr></tbody></table></div>
      <p class="spec-note spec-prose-measure">A fluid route leaves room for resource inventories and for a short form or paragraph to keep its own reading measure.</p>
      <label class="spec-field spec-fit">Workspace search<input class="k-input" type="search" placeholder="Search workspaces…"></label>
    </div>
  </div>

  <div v-else-if="stage === 'overlay'" class="spec-stack">
    <p class="spec-note">Behavior study · simulated context and no model calls.</p>
    <div class="spec-line"><span>Edge connections</span><button type="button" class="k-btn k-btn--ghost" :aria-expanded="workspace" @click="workspace = !workspace"><Sparkles :size="16" aria-hidden="true" /> {{ workspace ? 'Close assistant' : 'Open assistant' }}</button></div>
    <section v-if="workspace" class="spec-section spec-stack"><div class="spec-line"><h3>Workspace assistant</h3><label class="k-checkbox-hit"><input v-model="recovery" type="checkbox" class="k-checkbox"> Simulate missing model</label></div>
      <div v-if="recovery" class="spec-stack"><p class="spec-note">No usable model is connected to this workspace.</p><a class="k-btn k-btn--primary" href="?entry=design.components.model-connections">Open Models fixture</a></div>
      <form v-else class="spec-stack" @submit.prevent="selection = 'Message captured in the fixture'; overlayMessage = ''"><label class="spec-field">Ask about this workspace<textarea v-model="overlayMessage" class="k-input" placeholder="What changed on staging-west?"></textarea></label><div class="spec-row"><span class="k-badge k-badge--muted">Context available</span><button class="k-btn k-btn--primary" type="submit" :disabled="!overlayMessage.trim()">Send</button></div><p v-if="selection !== 'Applications'" class="spec-note" role="status">{{ selection }}</p></form>
    </section>
    <div v-else class="island spec-tile"><Sparkles :size="16" class="inline-block" aria-hidden="true" /> Ask the workspace assistant</div>
  </div>

  <div v-else-if="stage === 'exceptions' || stage === 'oddities'" class="spec-stack">
    <section class="spec-section"><h3>Kuery relation legend · source palette</h3><div class="spec-token-grid">
      <div v-for="item in RELATION_METADATA" :key="item.name" class="spec-token"><span class="spec-line-sample" :style="{ borderTopStyle: item.lineStyle, borderTopColor: theme.resolved === 'dark' ? item.color : item.lightColor }"></span><span>{{ item.legendLabel }}<small>{{ item.lineStyle }} · {{ item.direction }}</small></span></div>
    </div><p class="spec-note">The design book records the full accessibility review as pending.</p></section>
    <template v-if="stage === 'oddities'">
      <section class="spec-section"><h3>Native fallbacks and unshipped studies</h3><div class="spec-grid"><label class="spec-field">Service type · native select<select class="k-input"><option>Home Assistant</option><option>UniFi</option></select></label><label class="spec-field">Range · native fallback<input type="range" min="0" max="100" value="42"></label><label class="spec-field">From date<input type="date" class="k-input"></label><label class="spec-field">To date<input type="date" class="k-input"></label></div></section>
      <section class="spec-section spec-stack"><h3>Global command search · unshipped study</h3><label class="spec-field">Search workspaces and resources <input v-model="assetQuery" class="k-input" type="search" placeholder="Type a resource name"></label><div class="k-menu spec-command-study"><span class="k-eyebrow">Workspaces</span><a v-for="item in ['Platform', 'Staging', 'Research'].filter(label => label.toLowerCase().includes(assetQuery.toLowerCase()))" :key="item" class="k-menu-item" href="?entry=design.patterns.navigation-and-feedback">{{ item }}</a></div></section>
    </template>
    <template v-else>
      <div class="spec-grid"><section class="spec-section"><h3>Terminal canvas · visual study</h3><pre class="spec-terminal"><code>$ kubectl get pods
relay-87cc9   1/1   Running</code></pre></section><section class="spec-section"><h3>Application-owned preview · visual study</h3><iframe class="spec-app-preview" title="Independent white application canvas" sandbox="allow-same-origin" srcdoc="<body style='background:white;color:black;font:16px system-ui;padding:12px'>Staging Relay · healthy</body>"></iframe></section></div>
      <div class="spec-row"><a href="?entry=design.foundations.theming" class="review-source-link">Dex brand tiles and fixed-dark login</a><a href="?entry=design.components.ai-conversation" class="review-source-link">Conversational surface</a></div>
      <div class="k-table spec-scroll"><table class="spec-data"><thead><tr><th>Rule and match</th><th>Source</th><th>Reason</th></tr></thead><tbody>
        <tr v-for="item in catalog.exceptions" :key="item.path + ':' + item.line + ':' + item.column"><td><code>{{ item.rule }} · {{ item.match }}</code></td><td><a :href="item.source.href">{{ item.path }}:{{ item.line }}:{{ item.column }}</a></td><td>{{ item.reason }}</td></tr>
      </tbody></table></div>
    </template>
  </div>

  <div v-else-if="stage === 'accessibility'" class="spec-stack">
    <div class="spec-row"><label id="review-touch-target" class="k-checkbox-hit"><input type="checkbox" class="k-checkbox"> Include a recovery check</label><button class="k-btn k-btn--ghost" type="button" @click="measure">Measure target</button></div>
    <p class="spec-note" role="status"><code>{{ device.width }} × {{ device.height }}px</code> label · <code>{{ device.glyph }}px</code> glyph · {{ device.coarse ? 'coarse pointer detected' : 'fine pointer' }} · {{ device.reduced ? 'reduced motion' : 'standard motion' }}</p>
    <dl class="spec-definition"><div v-for="[label, note, id] in assess" :key="label"><dt><a class="review-source-link" :href="'?entry=' + id">{{ label }}</a></dt><dd>{{ note }}</dd></div></dl>
  </div>

  <div v-else-if="stage === 'checklist'" class="spec-stack">
    <p class="spec-note">Mark the checks you’ve reviewed in this session.</p>
    <div v-for="[label, id] in checklist" :key="id" class="spec-line spec-check"><label class="k-checkbox-hit"><input type="checkbox" class="k-checkbox"> {{ label }}</label><a class="review-source-link" :href="'?entry=' + id">Open fixture</a></div>
  </div>

  <div v-else-if="stage === 'conformance'" class="spec-stack">
    <dl class="spec-definition"><div v-for="[label, command, summary] in checks" :key="label"><dt>{{ label }}</dt><dd><code>{{ command }}</code><p>{{ summary }}</p></dd></div></dl>
    <a class="review-source-link" href="?entry=design.quality.exceptions">Inspect accepted exceptions</a>
    <a class="review-source-link" href="?entry=design.quality.review-checklist">Open review checklist</a>
  </div>
</template>
