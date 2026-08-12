import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

test('recognizes a caret-relative slash token only at start or after whitespace', async () => {
  const { assistantSlashToken, consumeAssistantSlashToken, filterAssistantSlashCommands, projectAssistantComposerParts, assistantComposerPlainContent } = await vite.ssrLoadModule('/src/assistantCommandPalette.ts')
  assert.deepEqual(assistantSlashToken('/'), { start: 0, end: 1, query: '' })
  assert.deepEqual(assistantSlashToken('  /res'), { start: 2, end: 6, query: 'res' })
  assert.equal(assistantSlashToken('/resource compare these'), null)
  assert.deepEqual(assistantSlashToken('please /resource'), { start: 7, end: 16, query: 'resource' })
  assert.deepEqual(assistantSlashToken('first line\n/resource'), { start: 11, end: 20, query: 'resource' })
  assert.deepEqual(assistantSlashToken('before /resource after', 16), { start: 7, end: 16, query: 'resource' })
  assert.equal(assistantSlashToken('word/resource'), null)
  assert.equal(assistantSlashToken('https://example.test/resource'), null)
  assert.equal(assistantSlashToken('/src/app/main.ts'), null)
  assert.equal(assistantSlashToken('https://example.test'), null)
  assert.equal(consumeAssistantSlashToken('  /skill use the existing layout'), '  use the existing layout')
  assert.deepEqual(filterAssistantSlashCommands('rev').map(({ id }) => id), ['review'])
  const parts = projectAssistantComposerParts([
    { type: 'text', text: 'before ' },
    { type: 'skill', skillID: 'project:one' },
    { type: 'resource', resourceIndex: 0 },
    { type: 'text', text: ' after' },
    { type: 'resource', resourceIndex: -1 },
  ])
  assert.deepEqual(parts, [
    { type: 'text', text: 'before ' },
    { type: 'skill', skillID: 'project:one' },
    { type: 'resource', resourceIndex: 0 },
    { type: 'text', text: ' after' },
  ])
  assert.equal(assistantComposerPlainContent(parts), 'before  after')
})

test('builds metadata-only GraphQL from validated Provider Action identifiers', async () => {
  const { buildAssistantResourceQuery } = await vite.ssrLoadModule('/src/assistantResources.ts')
  const built = buildAssistantResourceQuery({
    apiVersion: 'databricks.faros.sh/v1alpha1',
    kind: 'Table',
    resource: 'tables',
  })
  assert.equal(built.groupField, 'databricks_faros_sh')
  assert.equal(built.versionField, 'v1alpha1')
  assert.equal(built.listField, 'Tables')
  assert.match(built.query, /items \{ metadata \{ name uid resourceVersion \} \}/)
  assert.doesNotMatch(built.query, /\bspec\b|\bstatus\b|labels/)
})

test('rejects malformed catalog identifiers and GraphQL injection attempts', async () => {
  const { buildAssistantResourceQuery, parseAssistantBoundResource } = await vite.ssrLoadModule('/src/assistantResources.ts')
  const invalid = [
    { apiVersion: 'group/v1 { injected', kind: 'Table', resource: 'tables' },
    { apiVersion: 'group/v1', kind: 'Table } mutation', resource: 'tables' },
    { apiVersion: 'group/v1', kind: 'Table', resource: 'tables { items' },
    { apiVersion: 'group//v1', kind: 'Table', resource: 'tables' },
    { apiVersion: 'bad..group/v1', kind: 'Table', resource: 'tables' },
    { apiVersion: 42, kind: {}, resource: ['tables'] },
  ]
  for (const bound of invalid) {
    assert.equal(parseAssistantBoundResource(bound), null)
    assert.throws(() => buildAssistantResourceQuery(bound), /invalid bound resource/)
  }
})

test('keeps only Ready providers with valid non-deprecated actions and deduplicates bound types', async () => {
  const { assistantResourceProviders } = await vite.ssrLoadModule('/src/assistantResources.ts')
  const action = (id, boundResource, deprecated = false) => ({ id, boundResource, deprecation: { deprecated } })
  const providers = assistantResourceProviders([{
    name: 'zeta', displayName: 'Zeta', ready: true, hasUI: true, hasBackend: true,
    actions: [
      action('read', { apiVersion: 'zeta.example.io/v1', kind: 'Widget', resource: 'widgets' }),
      action('update', { apiVersion: 'zeta.example.io/v1', kind: 'Widget', resource: 'widgets' }),
      action('old', { apiVersion: 'zeta.example.io/v1', kind: 'Legacy', resource: 'legacies' }, true),
      action('bad', { apiVersion: 'zeta.example.io/v1', kind: 'Bad', resource: 'bad query' }),
    ],
  }, {
    name: 'alpha', displayName: 'Alpha', ready: false, hasUI: true, hasBackend: true,
    actions: [action('read', { apiVersion: 'alpha.example.io/v1', kind: 'Thing', resource: 'things' })],
  }])
  assert.deepEqual(providers.map(({ name }) => name), ['zeta'])
  assert.deepEqual(providers[0].resourceTypes.map(({ kind }) => kind), ['Widget'])
})

test('sorts resource types deterministically when kind and API version tie', async () => {
  const { assistantResourceProviders } = await vite.ssrLoadModule('/src/assistantResources.ts')
  const bound = (resource) => ({ apiVersion: 'demo.example.io/v1', kind: 'Table', resource })
  const [provider] = assistantResourceProviders([{
    name: 'demo', displayName: 'Demo', ready: true, hasUI: true, hasBackend: true,
    actions: [
      { id: 'z', boundResource: bound('z-tables') },
      { id: 'a', boundResource: bound('a-tables') },
    ],
  }])
  assert.deepEqual(provider.resourceTypes.map(({ resource }) => resource), ['a-tables', 'z-tables'])
})

test('retains successful resource groups when another type fails and sanitizes warnings', async () => {
  const { discoverAssistantResources } = await vite.ssrLoadModule('/src/assistantResources.ts')
  const types = [{
    provider: 'demo', providerDisplayName: 'Demo', apiVersion: 'demo.example.io/v1', kind: 'Widget', resource: 'widgets',
  }, {
    provider: 'demo', providerDisplayName: 'Demo', apiVersion: 'demo.example.io/v1', kind: 'Gadget', resource: 'gadgets',
  }]
  const requests = []
  const fetcher = async (_url, init) => {
    requests.push(JSON.parse(init.body).query)
    if (init.body.includes('Gadgets')) return new Response('sensitive provider body', { status: 503 })
    return Response.json({ data: { demo_example_io: { v1: { Widgets: { items: [
      { metadata: { name: 'zulu', uid: 'u2', resourceVersion: '2' } },
      { metadata: { name: 'alpha', uid: 'u1', resourceVersion: '1' } },
      { metadata: { name: 'alpha', uid: 'duplicate', resourceVersion: '3' } },
    ] } } } } })
  }
  const result = await discoverAssistantResources({ tenant: 'root:org:ws', token: 'secret' }, types, fetcher)
  assert.equal(requests.length, 2)
  assert.deepEqual(result.groups.map(({ type }) => type.kind), ['Widget'])
  assert.deepEqual(result.groups[0].items.map(({ resourceRef }) => resourceRef.name), ['alpha', 'zulu'])
  assert.deepEqual(result.warnings, ['Gadget resources are temporarily unavailable.'])
  assert.doesNotMatch(result.warnings.join(' '), /sensitive|503/)
})

test('keeps only metadata identity from successful resource rows', async () => {
  const { discoverAssistantResources } = await vite.ssrLoadModule('/src/assistantResources.ts')
  const type = { provider: 'demo', providerDisplayName: 'Demo', apiVersion: 'demo.example.io/v1', kind: 'Widget', resource: 'widgets' }
  const result = await discoverAssistantResources({ tenant: 'root:org:ws', token: 'secret' }, [type], async () => Response.json({
    data: { demo_example_io: { v1: { Widgets: { items: [
      { metadata: { name: 'one', uid: 'uid-1', resourceVersion: '7', labels: { secret: 'must-not-be-used' } }, spec: { password: 'must-not-be-used' } },
      { metadata: { name: '', uid: 'ignored', resourceVersion: 'ignored' } },
    ] } } } },
  }))
  assert.deepEqual(result.groups[0].items, [{
    provider: 'demo', providerDisplayName: 'Demo', uid: 'uid-1', resourceVersion: '7',
    resourceRef: { apiVersion: type.apiVersion, kind: type.kind, resource: type.resource, name: 'one' },
  }])
})

test('renders one responsive accessible listbox and declares keyboard/back/focus behavior', async () => {
  const { default: AssistantCommandPalette } = await vite.ssrLoadModule('/src/AssistantCommandPalette.vue')
  const html = await renderToString(createSSRApp(AssistantCommandPalette, {
    open: true,
    commandQuery: '',
    ctx: null,
    providers: [],
    skills: [],
    selectedSkillIDs: [],
    selectedResources: [],
  }))
  assert.match(html, /aria-label="Assistant slash commands"/)
  assert.match(html, /role="listbox"/)
  assert.match(html, /\/skill/)
  assert.match(html, /\/resource/)
  const source = await readFile(new URL('./AssistantCommandPalette.vue', import.meta.url), 'utf8')
  assert.match(source, /event\.key === 'ArrowDown'/)
  assert.match(source, /event\.key === 'Enter'/)
  assert.match(source, /event\.key === 'Escape'/)
  assert.match(source, /searchRef\.value\?\.focus\(\)/)
  assert.match(source, /view\.value === 'resources'.*enterView\('providers'\)/s)
  assert.match(source, /fixed inset-x-2 bottom-2/)
  assert.match(source, /md:absolute/)
})

test('rich composer owns plain paste/IME guards, atomic chips, and bounded contentParts', async () => {
  const [composer, app] = await Promise.all([
    readFile(new URL('./AssistantRichComposer.vue', import.meta.url), 'utf8'),
    readFile(new URL('./App.vue', import.meta.url), 'utf8'),
  ])
  assert.match(composer, /contenteditable/)
  assert.match(composer, /@paste="handlePaste"/)
  assert.match(composer, /handleCompositionStart/)
  assert.match(composer, /handleCompositionEnd/)
  assert.match(composer, /dataset\.assistantChip/)
  assert.match(composer, /event\.key === 'Backspace'/)
  assert.match(composer, /event\.key === 'Delete'/)
  assert.match(composer, /contentParts/)
  assert.match(app, /:content-parts="assistantComposerParts"/)
  assert.match(app, /contentParts: turnContentParts/)
  assert.match(app, /assistantComposerHasChipContent/)
  assert.match(app, /hasStructuredContent/)
  assert.doesNotMatch(app, /[\u2726\u25c7]/u)
  assert.match(app, /clearSelectedTurnAttachments\(\)[\s\S]*firstProjectSubmissionAccepted/)
  assert.match(app, /assistantContentPartsForMessage\(message\)/)
})

test('palette guards duplicate/limited selections and cancels stale provider loads on back', async () => {
  const source = await readFile(new URL('./AssistantCommandPalette.vue', import.meta.url), 'utf8')
  assert.match(source, /selectedResourceKeys\.value\.has\(assistantResourceSelectionKey\(resource\)\)/)
  assert.match(source, /props\.selectedResources\.length >= 8/)
  assert.match(source, /resourceLoadSerial\+\+/)
  assert.match(source, /if \(view\.value === 'resources'\) return enterView\('providers'\)/)
})

test('consumes palette keys before a closing selection can submit the composer', async () => {
  const [palette, composer] = await Promise.all([
    readFile(new URL('./AssistantCommandPalette.vue', import.meta.url), 'utf8'),
    readFile(new URL('./AssistantRichComposer.vue', import.meta.url), 'utf8'),
  ])
  assert.match(palette, /event\.key === 'Enter'[\s\S]*event\.preventDefault\(\)[\s\S]*event\.stopPropagation\(\)[\s\S]*activateCurrent\(\)/)
  assert.match(palette, /event\.key === 'Escape'[\s\S]*event\.preventDefault\(\)[\s\S]*event\.stopPropagation\(\)[\s\S]*back\(\)/)
  assert.match(palette, /event\.key === 'ArrowDown' \|\| event\.key === 'ArrowUp'[\s\S]*event\.stopPropagation\(\)/)
  assert.match(palette, /view\.value === 'commands'/)
  assert.match(palette, /view\.value === 'skills'/)
  assert.match(palette, /view\.value === 'providers'/)
  assert.match(palette, /view\.value === 'resources'/)
  assert.match(composer, /if \(props\.disabled \|\| event\.defaultPrevented\) return/)
})

test('installs the palette key guard in capture phase for every selection view', async () => {
  const source = await readFile(new URL('./AssistantCommandPalette.vue', import.meta.url), 'utf8')
  assert.match(source, /document\.addEventListener\('keydown', handleKeydown, true\)/)
  assert.match(source, /document\.removeEventListener\('keydown', handleKeydown, true\)/)

  const activateStart = source.indexOf('function activateCurrent()')
  const activateEnd = source.indexOf('\n}\n\nfunction back(', activateStart)
  assert.ok(activateStart >= 0 && activateEnd > activateStart, 'activateCurrent should remain a bounded dispatch helper')
  const activateBody = source.slice(activateStart, activateEnd)
  assert.match(activateBody, /view\.value === 'commands'[\s\S]*chooseCommand/)
  assert.match(activateBody, /view\.value === 'skills'[\s\S]*chooseSkill/)
  assert.match(activateBody, /view\.value === 'providers'[\s\S]*chooseProvider/)
  assert.match(activateBody, /resources\.value\[activeIndex\.value\][\s\S]*chooseResource/)

  const keydownStart = source.indexOf('function handleKeydown(event: KeyboardEvent)')
  const keydownEnd = source.indexOf('\n}\n\nonMounted(', keydownStart)
  assert.ok(keydownStart >= 0 && keydownEnd > keydownStart, 'handleKeydown should remain a bounded event guard')
  const keydownBody = source.slice(keydownStart, keydownEnd)
  assert.match(keydownBody, /if \(!props\.open\) return/)
  assert.match(keydownBody, /event\.key === 'Enter'[\s\S]*event\.preventDefault\(\)[\s\S]*event\.stopPropagation\(\)[\s\S]*activateCurrent\(\)/)
})
