import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = fileURLToPath(new URL('.', import.meta.url))
const root = resolve(here, '../..')
const modules = process.env.RAILGRID_PORTAL_MODULES || resolve(root, 'portal/node_modules')
const require = createRequire(resolve(modules, 'vite/package.json'))
const postcss = require('postcss')
const hostPath = 'portal/src/assets/main.css'
const cssPath = 'provider-sdk/portalkit/railgrid-ui.css'
const shellPath = 'portal/src/components/AppLayout.vue'
const tabsPath = 'provider-sdk/portalkit-vue/Tabs.vue'
const badgePath = 'provider-sdk/portalkit-vue/StatusBadge.vue'
const read = path => readFileSync(resolve(root, path), 'utf8')
const host = postcss.parse(read(hostPath))
const kit = postcss.parse(read(cssPath))
const base = Object.fromEntries(host.nodes.find(node => node.type === 'atrule' && node.name === 'theme').nodes.filter(n => n.type === 'decl').map(n => [n.prop, n.value]))
const light = Object.fromEntries(host.nodes.find(node => node.type === 'rule' && node.selector === 'html.light').nodes.filter(n => n.type === 'decl').map(n => [n.prop, n.value]))
const colors = Object.fromEntries(Object.keys(base).filter(name => name.startsWith('--color-')).flatMap(name => {
  const key = name === '--color-accent' ? 'primary' : name.slice('--color-'.length)
  return [[key, light[name] ?? base[name]], ['dark-' + key, base[name]]]
}))
const radii = Object.fromEntries(Object.entries(base).filter(([name]) => name.startsWith('--radius-')).map(([name, value]) => [name.slice(9), value]))
const typography = {
  display: { fontFamily: base['--font-display'], letterSpacing: '-0.01em', fontVariation: '"wdth" 125' },
  headline: { fontFamily: base['--font-sans'], fontWeight: 600 },
  body: { fontFamily: base['--font-sans'], fontSize: '13px', fontWeight: 400 },
  control: { fontFamily: base['--font-sans'], fontSize: '13px', fontWeight: 600 },
  navigation: { fontFamily: base['--font-sans'], fontSize: '11px', fontWeight: 500 },
  label: { fontFamily: base['--font-mono'], fontSize: '10px', fontWeight: 600, letterSpacing: '0.06em' },
  metric: { fontFamily: base['--font-sans'], fontSize: '26px' },
}
const ref = (group, name) => '{' + group + '.' + name + '}'
const color = name => ref('colors', name)
const corner = name => ref('rounded', name)
const control = ref('typography', 'control')
const button = { typography: control, rounded: corner('md'), padding: '8px 14px' }
const tokenComponents = {
  'button-primary': { ...button, backgroundColor: color('primary'), textColor: color('on-accent') },
  'button-primary-hover': { backgroundColor: color('accent-hover') },
  'button-ghost': { ...button, backgroundColor: color('surface-overlay'), textColor: color('text-primary') },
  'button-ghost-hover': { backgroundColor: color('surface-hover') },
  'button-text': { ...button, backgroundColor: 'transparent' },
  'button-danger': { ...button, backgroundColor: color('danger-subtle'), textColor: color('danger') },
  'button-danger-solid': { ...button, backgroundColor: color('danger'), textColor: color('on-accent'), padding: '0 12px', height: '36px', rounded: corner('lg') },
  'input': { backgroundColor: color('surface-overlay'), textColor: color('text-primary'), rounded: corner('md'), padding: '8px 12px', typography: ref('typography','body') },
  'card': { backgroundColor: color('surface-raised'), rounded: corner('lg') },
  'status-tag': { rounded: corner('sm'), padding: '2.5px 8px', typography: ref('typography','label') },
  'shell-navigation': { rounded: corner('md'), typography: ref('typography','navigation'), padding: '8px 12px', backgroundColor: color('accent-subtle'), textColor: color('primary') },
  'provider-tab': { rounded: corner('md'), padding: '7px 13px', backgroundColor: color('accent-subtle'), textColor: color('accent-hover') },
}
function yaml(value, depth = 0) {
  return Object.entries(value).map(([key,val]) => {
    const pad = '  '.repeat(depth)
    return val && typeof val === 'object' ? pad + key + ':\n' + yaml(val, depth + 1) : pad + key + ': ' + JSON.stringify(val) + '\n'
  }).join('')
}
const body = read('hack/impeccable-preview/document-body.md')
const front = {
  name: 'Railgrid · Violet Circuit',
  description: 'Dense, precise, alive only where state changes.',
  colors, typography, rounded: radii,
  spacing: { 'tab-gap': '4px', 'badge-gap': '6px', 'button-gap': '8px', 'shell-nav-gap': '10px', 'field-inset-inline': '12px', 'button-inset-inline': '14px' },
  components: tokenComponents,
}
writeFileSync(resolve(root,'DESIGN.md'), '---\n' + yaml(front) + '---\n\n' + body)

function rules(predicate) {
  const result = []
  kit.walkRules(rule => {
    const selectors = rule.selectors.filter(predicate)
    if (!selectors.length) return
    const copy = rule.clone({ selector: selectors.join(', ').replace(/\.k-/g, '.ds-').replace(/html\.light\s+/g, ':host-context(html.light) ') })
    if (rule.parent.type === 'atrule') {
      const at = postcss.atRule({ name: rule.parent.name, params: rule.parent.params })
      at.append(copy)
      result.push(at.toString())
    } else result.push(copy.toString())
  })
  return result.join('\n')
}
const focus = 'outline: 2px solid var(--color-accent); outline-offset: 2px;'
const buttonReset = '.ds-btn { font-family: var(--font-sans); line-height: 1.5; }'
const component = (name, kind, refersTo, description, html, css) => ({ name, kind, refersTo, description, html, css })
const buttonRows = [
  ['Primary button','primary','Create edge','Use for the main action in a task. The solid accent glows more strongly on hover.'],
  ['Ghost button','ghost','Refresh','A quiet secondary action on an overlay surface.'],
  ['Text button','text','Show details','A low emphasis action with inherited text and a transparent surface.'],
  ['Tinted danger button','danger','Remove connection','A destructive action that uses a semantic tint and never glows.'],
  ['Solid danger button','danger-solid','Delete workspace','A prominent destructive settings action with an explicit focus outline.'],
]
const trash = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 6h18M8 6V4h8v2m3 0-1 14H6L5 6m5 4v6m4-6v6"/></svg>'
const buttonSpecs = buttonRows.map(([name, variant, label, description]) => component(
  name, 'button', 'button-' + variant, description,
  '<button type="button" class="ds-btn ds-btn--' + variant + '">' + (variant === 'danger-solid' ? trash : '') + label + '</button>',
  buttonReset + '\n' + rules(s => /^\.k-btn(?:$|:disabled)/.test(s) || new RegExp('^\\.k-btn--' + variant + '(?:$|:| )').test(s)),
))
const fieldSpec = component('Text field','input','input','Focus the field to see the border, ring, and glow. The field carries its own visible label.',
  '<label class="ds-field">Edge name<input class="ds-input" placeholder="staging-west" aria-describedby="ds-help"><span id="ds-help">The name identifies this edge in the workspace.</span></label>',
  '.ds-field {display:grid;gap:5px;max-width:27rem;font:500 13px/1.5 var(--font-sans)} .ds-field > span {color:var(--color-text-secondary);font-weight:400;font-size:12px} .ds-input {font-family:var(--font-sans);line-height:1.5}\n' +
  rules(s => /^(?::where\(\.k-create-fields\) )?\.k-input(?:$|::placeholder|:focus|:where\()/.test(s)))
const cardSpec = component('Raised and flat cards','card','card','The same surface and hairline, with or without the small lift.',
  '<div class="ds-card-row"><article class="ds-card"><strong>Staging relay</strong><p>Raised resource summary</p></article><article class="ds-card ds-card--flat"><strong>Recovery notes</strong><p>Flat contextual panel</p></article></div>',
  '.ds-card-row{display:flex;gap:8px;flex-wrap:wrap}.ds-card{padding:12px;min-width:9rem;font:400 13px/1.5 var(--font-sans)} .ds-card p{margin:3px 0 0;color:var(--color-text-secondary)}\n' + rules(s => /^(?:html\.light )?\.k-card(?:$|--flat$)/.test(s)))
const statusSpec = component('Semantic status tags','chip','status-tag','Square tags carry the meaning in words as well as color.',
  '<div class="ds-status-row">' + [['success','Ready'],['warning','Pending'],['danger','Failed'],['muted','Unknown']].map(([tone,label]) => '<span class="ds-badge ds-badge--'+tone+'"><span class="ds-badge__dot" aria-hidden="true"></span>'+label+'</span>').join('') + '</div>',
  '.ds-status-row {display:flex;align-items:center;gap:7px;flex-wrap:wrap}\n' + rules(s => /^\.k-badge(?:$|--(?:success|warning|danger|muted)$|__dot$)/.test(s)))
const shellIcon = '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.75" aria-hidden="true"><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/></svg>'
const navigationSpec = component('Shell navigation and provider tabs','nav','shell-navigation','Move the current item in each group. The shell glows; provider route tabs remain flat.',
  '<div class="ds-nav-pair"><div><p class="ds-nav-label">Shell navigation</p><nav class="ds-shell" aria-label="Sample shell navigation"><a class="ds-shell-link" href="#dashboard" aria-current="page">'+shellIcon+'Dashboard</a><a class="ds-shell-link" href="#providers">'+shellIcon+'Providers</a></nav></div><div><p class="ds-nav-label">Provider sections</p><nav class="ds-tabs" aria-label="Sample provider sections"><a class="ds-tab" href="#models" aria-current="page">Models</a><a class="ds-tab" href="#connections">Connections</a></nav></div></div>',
  '.ds-nav-pair{display:grid;gap:14px}.ds-nav-label{margin:0 0 7px;font:500 11px/1.4 var(--font-mono);color:var(--color-text-secondary)} .ds-shell{display:flex;gap:4px;flex-wrap:wrap}.ds-shell-link{display:inline-flex;align-items:center;gap:10px;padding:8px 12px;border-radius:4px;color:var(--color-text-secondary);font:500 11px/1.5 var(--font-sans);text-decoration:none;transition:color .2s,background-color .2s}.ds-shell-link:hover{background:color-mix(in srgb,var(--color-surface-overlay) 50%,transparent);color:var(--color-text-primary)}.ds-shell-link[aria-current=page]{background:var(--color-accent-subtle);color:var(--color-accent);box-shadow:0 0 14px var(--color-accent-glow)}.ds-shell-link:focus-visible{'+focus+'}.ds-tabs{font-family:var(--font-sans)} .ds-tabs a{text-decoration:none}\n' +
  rules(s => /^\.k-tabs(?:$| > \.k-tab$)|^\.k-tab(?:$|:|\[|--active)/.test(s) && !s.includes('__count')))

const components = [...buttonSpecs, fieldSpec, cardSpec, statusSpec, navigationSpec]
const names = { primary: 'Circuit Violet', surface: 'Cool Paper', 'surface-raised': 'White Panel', 'surface-overlay': 'Field Slate', 'surface-hover': 'Hover Step', 'text-primary': 'Ink', success: 'Confirmed', warning: 'Attention', danger: 'Failure' }
const titleCase = name => name.replaceAll('-', ' ').replace(/\b\w/g, x => x.toUpperCase())
const colorMeta = Object.fromEntries(Object.entries(colors).map(([name,value]) => {
  const dark = name.startsWith('dark-')
  const key = name.replace(/^dark-/, '')
  const displayName = dark && key === 'surface' ? 'Night Ground' : (dark ? 'Dark ' : '') + (names[key] ?? titleCase(key))
  return [name, { role: key.startsWith('accent') || ['primary','on-accent'].includes(key) ? 'primary' : 'neutral', displayName, canonical: value, tonalRamp: [.15,.26,.37,.49,.60,.71,.83,.95].map(l => 'oklch(from ' + value + ' ' + l + ' c h / 1)') }]
}))
const overview = body.match(/## Overview\n\n([\s\S]+?)\*\*Key Characteristics:\*\*\n\n([\s\S]+?)\n\nThis/ )
const dos = [...body.matchAll(/^- \*\*Do\*\* (.+)$/gm)].map(([,s]) => 'Do ' + s)
const donts = [...body.matchAll(/^- \*\*Don't\*\* (.+)$/gm)].map(([,s]) => "Don't " + s)
let section
const narrativeRules = []
for (const line of body.split('\n')) {
  if (line.startsWith('## ')) section = ({ 'Elevation & Depth':'elevation' })[line.slice(3)] ?? line.slice(3).toLowerCase()
  const match = line.match(/^\*\*(The .+ Rule)\.\*\* (.+)$/)
  if (match && ['colors','typography','elevation'].includes(section)) {
    const after = body.slice(body.indexOf(line) + line.length).match(/^([^\n]*\n)*(?=\n)/)?.[0].trim().replaceAll('\n',' ')
    narrativeRules.push({ name: match[1], body: [match[2], after].filter(Boolean).join(' '), section })
  }
}
const sidecar = {
  schemaVersion: 2, generatedAt: new Date().toISOString(), title: 'Design System: Railgrid',
  extensions: {
    colorMeta,
    typographyMeta: Object.fromEntries(Object.keys(typography).map(key => [key, { displayName: titleCase(key), purpose: ({display:'Brand only', headline:'Functional titles', body:'Reading and operations', control:'Action labels', navigation:'Shell destinations', label:'Machine facts', metric:'Tabular KPI numerals'})[key] }])),
    shadows: [
      { name:'card-lift', value:'0 1px 2px color-mix(in srgb, var(--color-surface) 40%, transparent)', purpose:'Dark card lift; light uses text-primary at 4%.' },
      { name:'current-shell', value:'0 0 14px var(--color-accent-glow)', purpose:'Current shell destination.' },
      { name:'primary-action', value:'0 0 16px var(--color-accent-glow)', purpose:'Primary action; hover increases blur to 22px.' },
      { name:'field-focus', value:'0 0 0 3px var(--color-accent-subtle), 0 0 14px var(--color-accent-glow)', purpose:'Visible field focus.' },
    ],
    motion: [
      { name:'control-feedback', value:'150ms ease', purpose:'Shared control state change.' },
      { name:'progress', value:'300ms ease', purpose:'Progress fill transforms; immediate under reduced motion.' },
      { name:'working-spinner', value:'800ms linear', purpose:'In-flight indicator; static with reduced motion.' },
    ],
    breakpoints: [{ name:'create-actions-dock', value:'620px' },{ name:'toast-insets', value:'640px' }],
  },
  components,
  narrative: {
    northStar: overview[1].match(/"([^"]+)"/)[1], overview: overview[1].split('\n\n').slice(1).join('\n\n').trim(),
    keyCharacteristics: overview[2].split('\n').map(s => s.replace(/^- /,'')),
    rules: narrativeRules, dos, donts,
  },
}
mkdirSync(resolve(root,'.impeccable'),{recursive:true})
writeFileSync(resolve(root,'.impeccable/design.json'), JSON.stringify(sidecar,null,2)+'\n')
const refs = [
  ...buttonRows.map(([, variant]) => [[cssPath, '.k-btn--' + variant + ' {']]),
  [[cssPath, '.k-input {'], [cssPath, '.k-input:focus {']],
  [[cssPath, '.k-card {'], [cssPath, 'html.light .k-card {']],
  [[cssPath, '.k-badge {'], [badgePath, 'const toneConfig']],
  [[shellPath, 'shadow-[0_0_14px_var(--color-accent-glow)]'], [cssPath, '.k-tab {'], [tabsPath, 'aria-current']],
].map(group => group.map(([path, needle]) => {
  const line = read(path).split('\n').findIndex(line => line.includes(needle)) + 1
  if (!line) throw Error('Source locator missing: ' + path + ' ' + needle)
  return { path, line }
}))
const themeCSS = ':root {' + Object.entries(base).map(pair => pair.join(':')).join(';') + '}\n:root.light {' + Object.entries(light).map(pair => pair.join(':')).join(';') + '}'
writeFileSync(resolve(here,'snapshot.json'), JSON.stringify({ refs, themeCSS, light, base, hostPath, tokenLines: Object.fromEntries(Object.keys(light).map(key => {
  const rows = read(hostPath).split('\n')
  const hits = rows.flatMap((line,i) => line.trim().startsWith(key+':') ? [i+1] : [])
  return [key, hits]
})) },null,2)+'\n')
console.log('Generated', Object.keys(light).length, 'theme pairs and', components.length, 'interactive specimens.')
