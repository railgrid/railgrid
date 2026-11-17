import assert from 'node:assert/strict'
import test from 'node:test'
import { auditDirectory } from './audit-design-system-output.mjs'

import { analyzeCssSource, analyzeMarkupSource, analyzeModuleSource, candidateExtension, classifyInventoryFile, isLikelyInlineGoFrontend, resolveParsers, summarizeJsonInventory } from './audit-design-system.mjs'

test('CSS normalization groups equivalent declarations and keeps tokenized values distinct', () => {
  const result = analyzeCssSource(`
/* A comment is not a declaration: color: #123456 !important; */
.alpha { color: #fff; padding: 8px; }
.beta { padding: 8px; color: #FFF; }
.token { color: var(--color-primary); padding: 9px; }
`, { path: 'fixtures/positive.css' })

  assert.equal(result.evidenceStatus, 'static signals only; not confirmed findings')
  assert.equal(result.ruleCount, 3)
  assert.equal(result.styleSignals.filter(signal => signal.category === 'color-literal').length, 2)
  assert.equal(result.styleSignals.filter(signal => signal.category === 'token-reference').length, 1)
  assert.equal(result.styleSignals.filter(signal => signal.category === 'important-declaration').length, 0)
  assert.ok(result.normalizedDeclarationGroups.exact.some(group => group.occurrences.length === 2))
  assert.ok(result.normalizedDeclarationGroups.near.some(group => group.occurrences.length === 3))
  assert.ok(result.styleSignals.every(signal => signal.classification === 'signal-only' && signal.isFinding === false))
})

test('markup analysis ignores commented controls and reports live native controls as signals', () => {
  const result = analyzeMarkupSource(`
<!-- <button style="color: #f00">not rendered</button> -->
<button class="k-btn" style="padding: 8px; color: #fff">Save</button>
`, { path: 'fixtures/positive.html' })

  assert.equal(result.styleSignals.filter(signal => signal.category === 'native-control-markup').length, 1)
  assert.equal(result.styleSignals.filter(signal => signal.category === 'inline-style-markup').length, 1)
  assert.equal(result.styleSignals.filter(signal => signal.category === 'color-literal').length, 1)
  assert.ok(result.styleSignals.every(signal => signal.isFinding === false))
})

test('TypeScript AST records component imports, static routes, and unresolved manual route dispatch', t => {
  const parsers = resolveParsers()
  if (!parsers.typescript) {
    t.skip('TypeScript parser is not installed; the analyzer records an explicit parser gap in this environment.')
    return
  }
  const result = analyzeModuleSource(`
import Card from './Card.vue'
const routes = [
  { path: '/cards', component: Card },
  { path: \`/cards/\${id}\`, component: Card },
]
function parseSubPath(subPath: string) {
  if (subPath === 'cards') return Card
  return null
}
export const CardPanel = () => <Card />
`, { path: 'fixtures/CardPanel.tsx', knownPaths: ['fixtures/Card.vue'], parsers })

  assert.equal(result.imports.length, 1)
  assert.equal(result.imports[0].targetPath, 'fixtures/Card.vue')
  assert.equal(result.components.some(component => component.name === 'CardPanel'), true)
  assert.equal(result.componentUsages.some(usage => usage.tag === 'Card' && usage.targetPath === 'fixtures/Card.vue'), true)
  assert.deepEqual(result.routes.definitions.map(route => route.pattern), ['/cards', '/cards/:id'])
  assert.equal(result.routes.definitions[1].dynamic, true)
  assert.equal(result.routes.manualParsers.length, 1)
  assert.equal(result.routes.manualParsers[0].dynamicSegmentsUnresolved, true)
  assert.ok(result.routes.manualParsers[0].recognizedLiterals.some(literal => literal.value === 'cards'))
})

test('a path-shaped object outside a route declaration is not promoted to a route', t => {
  const parsers = resolveParsers()
  if (!parsers.typescript) {
    t.skip('TypeScript parser is not installed; the analyzer records an explicit parser gap in this environment.')
    return
  }
  const result = analyzeModuleSource(`const displayMetadata = { path: '/not-a-route' }\n`, { path: 'fixtures/metadata.ts', parsers })
  assert.equal(result.routes.definitions.length, 0)
  assert.equal(result.routes.manualParsers.length, 0)
})

test('Go markup scope includes standalone HTML examples and rejects XML plist templates', () => {
  const example = 'fmt.Fprint(w, `<!doctype html><main>Frontend example</main>`)'
  const plist = 'template.Must(template.New("plist").Parse(`<!DOCTYPE plist><plist><dict></dict></plist>`))'
  assert.equal(isLikelyInlineGoFrontend('providers/example/examples/frontend/main.go', example), true)
  assert.equal(isLikelyInlineGoFrontend('pkg/cli/cmd/launchd.go', plist), false)
})

test('inventory accounts frontend JSON data and configs without treating them as rendered markup', () => {
  assert.equal(candidateExtension('providers/agents/portal/public/agent-templates.json'), '.json')
  assert.equal(candidateExtension('providers/agents/portal/src/model-snapshot-suffix.json'), '.json')
  assert.equal(candidateExtension('providers/agents/portal/README.md'), '.md')
  assert.equal(candidateExtension('portal/README.md'), '.md')
  assert.equal(candidateExtension('portal/public/animation.gif'), '.gif')
  assert.equal(candidateExtension('docs/README.md'), null)

  const templates = classifyInventoryFile('providers/agents/portal/public/agent-templates.json', '.json')
  assert.equal(templates.included, true)
  assert.equal(templates.category, 'frontend-data')
  assert.equal(templates.analysisStatus, 'json-inventory-only')
  assert.deepEqual(summarizeJsonInventory('[{"name":"reviewer"},{"name":"planner"}]'), { type: 'array', items: 2 })

  const config = classifyInventoryFile('providers/agents/portal/package.json', '.json')
  assert.equal(config.included, true)
  assert.equal(config.category, 'frontend-configuration')
  assert.deepEqual(summarizeJsonInventory('{"scripts":{},"dependencies":{}}'), { type: 'object', keys: 2 })
  assert.throws(() => summarizeJsonInventory('{invalid'), SyntaxError)
})

test('inventory gives docs, text data, generated files, assets, and non-frontend example data explicit roles', () => {
  const readme = classifyInventoryFile('provider-sdk/portalkit/README.md', '.md')
  assert.equal(readme.included, true)
  assert.equal(readme.category, 'reference-document')
  assert.equal(readme.analysisStatus, 'reference-inventory-only')

  const timeZones = classifyInventoryFile('providers/agents/portal/src/agent-digest-time-zones.txt', '.txt')
  assert.equal(timeZones.included, true)
  assert.equal(timeZones.category, 'frontend-data')
  assert.equal(timeZones.analysisStatus, 'text-data-inventory-only')

  const ignoreRules = classifyInventoryFile('providers/agents/portal/.gitignore', '<none>')
  assert.equal(ignoreRules.included, true)
  assert.equal(ignoreRules.analysisStatus, 'configuration-inventory-only')

  const lockfile = classifyInventoryFile('portal/package-lock.json', '.json')
  assert.equal(lockfile.included, false)
  assert.equal(lockfile.category, 'generated-dependency-lockfile')
  assert.match(lockfile.reason, /generated package resolution/)

  const dexAsset = classifyInventoryFile('hack/dex/web/static/main.css', '.css')
  assert.equal(dexAsset.included, true)
  assert.equal(dexAsset.category, 'dex-login-frontend')

  const generatedPlaceholder = classifyInventoryFile('providers/app-studio/portal/dist/.gitkeep', '<none>')
  assert.equal(generatedPlaceholder.included, false)
  assert.equal(generatedPlaceholder.category, 'generated-or-third-party')

  const gif = classifyInventoryFile('portal/public/animation.gif', '.gif')
  assert.equal(gif.included, true)

  const yamlManifest = classifyInventoryFile('providers/infrastructure/examples/rgds/redis-cache.yaml', '.yaml')
  assert.equal(yamlManifest.included, false)
  assert.equal(yamlManifest.category, 'non-frontend-example-data')
  assert.match(yamlManifest.reason, /outside the frontend implementation scope/)

  const frontendHtml = 'fmt.Fprint(w, `<!doctype html><main>Frontend example</main>`)'
  const frontend = classifyInventoryFile('providers/infrastructure/examples/application/apps/frontend/main.go', '.go', frontendHtml)
  assert.equal(frontend.included, true)
  assert.equal(frontend.category, 'inline-go-template')

  const backend = classifyInventoryFile('providers/infrastructure/examples/application/apps/backend/main.go', '.go', 'fmt.Fprintln(w, `{"items":[]}`)')
  assert.equal(backend.included, false)
  assert.equal(backend.category, 'non-frontend-example-data')

  const backendHtml = classifyInventoryFile('providers/infrastructure/examples/application/apps/backend/main.go', '.go', frontendHtml)
  assert.equal(backendHtml.included, true)
  assert.equal(backendHtml.category, 'inline-go-template')
})

test('records token references alongside fallback literals and important declarations', () => {
  const { styleSignals } = analyzeCssSource('.notice { color: var(--color-danger, #f00) !important }')
  for (const category of ['token-reference', 'color-literal', 'important-declaration']) {
    assert.ok(styleSignals.some(signal => signal.category === category), category)
  }
})

test('writing audit exports takes an explicit destination', () => {
  assert.throws(() => auditDirectory(''), /Set AUDIT_DIR/)
  assert.equal(auditDirectory('/tmp/local-audit'), '/tmp/local-audit')
})

test('shared server-rendered status page callers are inventoried without raw HTML', () => {
  const source = 'import "github.com/railgrid/provider-sdk/statuspage"\nfunc done() { statuspage.Render(w, result) }'
  assert.equal(classifyInventoryFile('pkg/auth/callback.go', '.go', source).analysisStatus, 'status-page-call-sites')
  assert.equal(classifyInventoryFile('pkg/auth/callback_test.go', '.go', source).included, false)
})

test('standalone HTML stylesheet locations exclude comments and script strings', () => {
  const result = analyzeMarkupSource([
    '<!-- <style>main { color: #bad }</style> -->',
    '<script>const sample = "<style>b {color: #bad}</style>"</script>',
    '<style>',
    '.notice { color: #123456 }',
    '</style>',
  ].join('\n'))
  assert.equal(result.ruleCount, 1)
  const colors = result.styleSignals.filter(signal => signal.category === 'color-literal')
  assert.deepEqual(colors.map(({ value, line }) => ({ value, line })), [{ value: '#123456', line: 4 }])
})
