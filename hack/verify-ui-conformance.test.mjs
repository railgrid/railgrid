import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'

import { RULES, scan } from './verify-ui-conformance.mjs'

const BASE_CONFIG = {
  version: 1,
  canonicalRoots: ['provider-sdk/portalkit'],
  providerRoots: ['providers/fixture/portal/src'],
  vendoredSegments: ['portalkit', 'portalkit-vue'],
  canonicalConsumerPaths: [],
  tokenAuthorityPaths: [],
  includeTests: false,
  exceptions: { version: 1, exceptions: [] },
}

function fixtureRepo(entries, config = {}) {
  const repoRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'faros-ui-conformance-'))
  fs.mkdirSync(path.join(repoRoot, 'provider-sdk/portalkit'), { recursive: true })
  for (const [relative, content] of Object.entries(entries)) {
    const absolute = path.join(repoRoot, relative)
    fs.mkdirSync(path.dirname(absolute), { recursive: true })
    fs.writeFileSync(absolute, content)
  }
  return {
    repoRoot,
    run(overrides = {}) {
      return scan({ repoRoot, config: { ...BASE_CONFIG, ...config, ...overrides } })
    },
  }
}

function rules(result) {
  return new Set(result.diagnostics.map((diagnostic) => diagnostic.rule))
}

test('accepts k-* recipes, known tokens, and true circles', () => {
  const fixture = fixtureRepo({
    'providers/fixture/portal/src/App.vue': `<template><section class="k-card"><span class="k-badge">Ready</span><i class="k-dot" /><button class="h-6 w-0 rounded-full" :class="'group-hover:w-6'" /></section></template>\n<style>\n.faros-provider-fixture .feature { color: var(--color-accent); border-radius: 6px; }\n.faros-provider-fixture .dot { width: 6px; height: 6px; border-radius: 50%; background: var(--color-success); }\n</style>\n`,
    'providers/fixture/portal/src/portalkit/legacy.ts': `document.body.className = 'pk-old';\n`,
    'providers/fixture/portal/src/comments.ts': `// pk-old data-pk ⚙ →\n/* old .k-card */\n`,
    'providers/fixture/portal/dist/bundle.js': `window.alert('dist is not application source')\n`,
    'providers/fixture/portal/node_modules/fake/index.js': `window.confirm('dependency')\n`,
    'providers/fixture/portal/src/ignored.test.ts': `const emoji = '😀';\n`,
  })
  const result = fixture.run()
  assert.deepEqual(result.diagnostics, [])
  assert.ok(result.files.includes('providers/fixture/portal/src/App.vue'))
  assert.ok(!result.files.some((file) => file.includes('/portalkit/')))
  assert.ok(!result.files.some((file) => file.includes('/dist/')))
  assert.ok(!result.files.some((file) => file.includes('/node_modules/')))
  assert.ok(!result.files.some((file) => file.endsWith('.test.ts')))
})

test('reports every migration rule with actionable locations', () => {
  const fixture = fixtureRepo({
    'providers/fixture/portal/src/Legacy.vue': `<template>\n  <div class="pk-card rounded-full" id="pk-node" data-pk="legacy">⚙</div>\n</template>\n<style>\n.k-card { color: var(--color-accent); }\n.card { color: var(--color-unknown); border-radius: 999px; }\n</style>\n`,
    'providers/fixture/portal/src/native.ts': `window.confirm('delete?');\nalert('done');\nglobalThis['alert']('again');\n`,
    'providers/fixture/portal/src/colors.css': `.bad { color: #abc; background: rgb(1, 2, 3); border-color: red; }\n`,
  })
  const result = fixture.run()
  const found = rules(result)
  for (const rule of [
    RULES.LEGACY_PK,
    RULES.PROVIDER_K_SELECTOR,
    RULES.NATIVE_DIALOG,
    RULES.FORBIDDEN_GLYPH,
    RULES.UNKNOWN_COLOR_TOKEN,
    RULES.RAW_COLOR,
    RULES.PILL_RADIUS,
    RULES.COMMON_WIDGET_SELECTOR,
  ]) assert.ok(found.has(rule), `expected ${rule} in ${JSON.stringify(result.counts)}`)
  for (const diagnostic of result.diagnostics) {
    assert.match(diagnostic.path, /^providers\/fixture\/portal\/src\//)
    assert.ok(diagnostic.line >= 1)
    assert.ok(diagnostic.column >= 1)
    assert.ok(diagnostic.message)
  }
})

test('allows prose arrows and page layout names but catches icon content and restyled widgets', () => {
  const fixture = fixtureRepo({
    'providers/fixture/portal/src/semantics.vue': `<template>
  <p>Move A → B, then use ↑↓ to navigate.</p>
  <button class="link"><span class="icon">⚙</span> Settings</button>
</template>
<style>
.header { display: flex; gap: 8px; }
.form { display: flex; flex-direction: column; gap: 8px; }
.list { display: grid; gap: 8px; }
.panel { background: var(--color-surface-raised); border: 1px solid var(--color-border-subtle); border-radius: 6px; padding: 12px; }
</style>
`,
  })
  const result = fixture.run()
  assert.equal(result.counts[RULES.FORBIDDEN_GLYPH] ?? 0, 1)
  assert.equal(result.counts[RULES.COMMON_WIDGET_SELECTOR] ?? 0, 1)
  assert.ok(result.diagnostics.some((diagnostic) => diagnostic.match === '⚙'))
  assert.ok(result.diagnostics.some((diagnostic) => diagnostic.match === '.panel'))
  assert.ok(!result.diagnostics.some((diagnostic) => diagnostic.match === '→' || diagnostic.match === '↑' || diagnostic.match === '↓'))
  assert.ok(!result.diagnostics.some((diagnostic) => /\.(?:header|form|list)/.test(diagnostic.match)))
})

test('scans canonical CSS and multiline control glyph context', () => {
  const fixture = fixtureRepo({
    'provider-sdk/portalkit/faros-ui.css': `.k-icon::before {\n  content:\n    "⚙";\n}\n`,
    'providers/fixture/portal/src/multiline.vue': `<template>
  <p>Move A
    →
    B.</p>
  <button
    class="icon"
  >
    ⚙
  </button>
  <button
    type="button"
  >
    View ↗
  </button>
</template>
`,
  })
  const result = fixture.run()
  const glyphs = result.diagnostics.filter((diagnostic) => diagnostic.rule === RULES.FORBIDDEN_GLYPH)
  assert.equal(glyphs.length, 3)
  assert.equal(glyphs.filter((diagnostic) => diagnostic.path.endsWith('faros-ui.css')).length, 1)
  assert.equal(glyphs.filter((diagnostic) => diagnostic.path.endsWith('multiline.vue')).length, 2)
  assert.ok(!glyphs.some((diagnostic) => diagnostic.match === '→'))
  assert.ok(glyphs.some((diagnostic) => diagnostic.match === '⚙'))
  assert.ok(glyphs.some((diagnostic) => diagnostic.match === '↗'))
})

test('keeps the canonical stylesheet handoff and native table-row contract', () => {
  const styles = fs.readFileSync(new URL('../provider-sdk/portalkit/styles.ts', import.meta.url), 'utf8')
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const hostCss = fs.readFileSync(new URL('../portal/src/assets/main.css', import.meta.url), 'utf8')
  const table = fs.readFileSync(new URL('../provider-sdk/portalkit-vue/ResourceTable.vue', import.meta.url), 'utf8')

  assert.match(styles, /import farosUIStyles from '\.\/faros-ui\.css\?raw'/)
  assert.match(css, /--faros-ui-canonical:\s*1/)
  assert.match(hostCss, /@import "\.\/faros-ui\.css" layer\(components\);/)
  assert.match(styles, /Never mutate an existing style element/)
  assert.doesNotMatch(styles, /style\.textContent !== farosUIStyles/)
  assert.match(table, /:tabindex="interactive \? 0 : undefined"/)
  assert.match(table, /@keydown="onRowKeydown\(row, \$event\)"/)
  assert.match(table, /isExplicitControlTarget/)
  assert.doesNotMatch(table, /:role="interactive \? 'button' : undefined"/)
})

test('keeps the responsive ResourcePage title canonical across provider detail views', () => {
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const codeStyle = fs.readFileSync(new URL('../providers/code/portal/src/style.css', import.meta.url), 'utf8')
  const edgesStyle = fs.readFileSync(new URL('../providers/edges/portal/src/style.css', import.meta.url), 'utf8')
  const title = css.match(/\.k-resource-page__title\s*\{([^}]*)\}/s)?.[1] ?? ''

  assert.match(title, /font-size:\s*clamp\(24px,\s*4vw,\s*32px\)/)
  assert.match(title, /letter-spacing:\s*-\.02em/)
  assert.match(title, /line-height:\s*1\.12/)
  assert.match(title, /overflow-wrap:\s*anywhere/)
  assert.match(title, /word-break:\s*break-word/)
  assert.match(css, /@media\s*\(max-width:\s*520px\)[\s\S]*\.k-resource-page__title\s*\{[^}]*font-size:\s*22px;/)

  for (const relative of [
    '../providers/code/portal/src/views/ConnectionDetailView.vue',
    '../providers/code/portal/src/views/RepoDetailView.vue',
    '../providers/databricks/portal/src/views/ConnectionDetailView.vue',
    '../providers/databricks/portal/src/views/TableDetailView.vue',
    '../providers/databricks/portal/src/views/WarehouseDetailView.vue',
    '../providers/edges/portal/src/Detail.vue',
    '../providers/edges/portal/src/ServiceEdit.vue',
    '../providers/infrastructure/portal/src/views/InstanceDetailPage.vue',
    '../portal/src/pages/MCPPage.vue',
  ]) {
    assert.match(fs.readFileSync(new URL(relative, import.meta.url), 'utf8'), /<ResourcePage\b/)
  }

  assert.doesNotMatch(codeStyle, /(?:repo|connection)-detail__resource\s*>\s*section\s*>\s*header\s+h1/)
  assert.doesNotMatch(edgesStyle, /(?:edge|service)-detail__resource\s*>\s*section\s*>\s*header\s+h1/)
})

test('keeps the ResourcePage title-first metadata and actions contract canonical', () => {
  const page = fs.readFileSync(new URL('../provider-sdk/portalkit-vue/ResourcePage.vue', import.meta.url), 'utf8')
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const title = page.indexOf('<h1 class="k-resource-page__title">')
  const meta = page.indexOf('class="k-resource-page__meta"')
  const subtitle = page.indexOf('class="k-resource-page__subtitle"')
  const headerSide = page.indexOf('class="k-resource-page__header-side"')
  const headerEnd = page.indexOf('</header>', headerSide)
  const metadataSource = page.slice(meta, subtitle)
  const headerSideSource = page.slice(headerSide, headerEnd)

  assert.match(page, /kind\?: string/)
  assert.doesNotMatch(page, /\beyebrow\b/)
  assert.ok(title >= 0 && title < meta && meta < subtitle, 'title, metadata, and subtitle must be ordered')
  assert.match(metadataSource, /class="k-resource-page__kind"[^>]*>\{\{ kind \}\}/)
  assert.match(metadataSource, /slot name="meta"/)
  assert.match(metadataSource, /class="k-resource-page__status"[^>]*><slot name="status" \/>/)
  assert.match(metadataSource, /class="k-resource-page__separator"[^>]*aria-hidden="true">·<\/span>/)
  assert.doesNotMatch(page, /k-resource-page__eyebrow/)
  assert.doesNotMatch(headerSideSource, /\$slots\.status|k-resource-page__status/)
  assert.match(headerSideSource, /\$slots\.actions/)
  const kindRule = css.match(/\.k-resource-page__kind\s*\{([^}]*)\}/s)?.[1] ?? ''
  assert.match(kindRule, /display:\s*inline-flex;/)
  assert.match(kindRule, /flex:\s*0 0 auto;/)
  assert.match(kindRule, /align-items:\s*center;/)
  assert.doesNotMatch(kindRule, /(?:color|font-family|font-size|font-weight|letter-spacing|text-transform):/)
  assert.match(css, /\.k-resource-page__separator\s*\{[\s\S]*flex:\s*0 0 auto;/)
  assert.match(css, /\.k-resource-page__status\s*\{[^}]*display:\s*inline-flex;/)

  for (const relative of [
    '../providers/code/portal/src/views/ConnectionDetailView.vue',
    '../providers/code/portal/src/views/RepoDetailView.vue',
    '../providers/databricks/portal/src/views/ConnectionDetailView.vue',
    '../providers/databricks/portal/src/views/TableDetailView.vue',
    '../providers/databricks/portal/src/views/WarehouseDetailView.vue',
    '../providers/edges/portal/src/Detail.vue',
    '../providers/edges/portal/src/ServiceEdit.vue',
    '../providers/infrastructure/portal/src/views/InstanceDetailPage.vue',
    '../portal/src/pages/MCPPage.vue',
  ]) {
    const source = fs.readFileSync(new URL(relative, import.meta.url), 'utf8')
    for (const match of source.matchAll(/<ResourcePage\b[^>]*>/g)) {
      assert.doesNotMatch(match[0], /\beyebrow\s*=/, `${relative} ResourcePage uses kind instead of eyebrow`)
    }
  }
})

test('keeps sidebar divider and child toggles on the borderless text-button recipe', () => {
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const layout = fs.readFileSync(new URL('../portal/src/components/AppLayout.vue', import.meta.url), 'utf8')

  assert.match(css, /\.k-btn--text\s*\{[^}]*background:\s*transparent;[^}]*border-color:\s*transparent;/s)
  assert.equal((layout.match(/k-btn k-btn--text mt-3 mb-1/g) ?? []).length, 2)
  assert.equal((layout.match(/k-btn k-btn--text -mr-1 flex h-4 w-4/g) ?? []).length, 2)
})

test('keeps page-level back navigation intrinsic-width on the shared recipe', () => {
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const provision = fs.readFileSync(new URL('../providers/infrastructure/portal/src/views/ProvisionPage.vue', import.meta.url), 'utf8')

  assert.match(css, /\.k-back-action\s*\{[^}]*align-self:\s*flex-start;[^}]*inline-size:\s*fit-content;/s)
  assert.match(provision, /class="k-btn k-btn--ghost k-back-action"/)
})

test('keeps resource-table controls and wide-table scrolling in the canonical recipe', () => {
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const table = fs.readFileSync(new URL('../provider-sdk/portalkit-vue/ResourceTable.vue', import.meta.url), 'utf8')
  const sync = fs.readFileSync(new URL('./sync-portalkit.sh', import.meta.url), 'utf8')

  assert.match(table, /class="k-table k-table--resource"/)
  assert.match(table, /class="k-table__controls"/)
  assert.match(table, /class="k-table__scroll" role="region" aria-label="Scrollable table" tabindex="0"/)
  assert.match(table, /class="k-table__pagination"/)
  assert.doesNotMatch(table, /ResourceTable\.css/)

  assert.match(css, /\.k-table\.k-table--resource\s*\{[^}]*overflow:\s*hidden;/s)
  assert.match(css, /\.k-table__scroll\s*\{[^}]*overflow-x:\s*auto;/s)
  assert.match(css, /\.k-table__cell svg\s*\{[^}]*display:\s*inline-block;[^}]*vertical-align:\s*middle;/s)
  assert.match(table, /v-if="hasConfiguredControls" class="k-table__loading-controls" aria-hidden="true"/)
  assert.match(table, /v-for="filter in filters"[^>]*k-table__loading-control--filter/)
  assert.match(table, /variant\?: 'queryable' \| 'simple'/)
  assert.match(table, /variant: 'queryable'/)
  assert.match(table, /:class="`k-table--\$\{variant\}`"/)
  assert.match(css, /\.k-table__scroll:focus-visible\s*\{[^}]*box-shadow:\s*inset/s)
  assert.match(css, /\.k-table__pending-cell\s*\{[^}]*text-align:\s*center;/s)
  assert.match(css, /\.k-table__page-size\s*\{[^}]*margin-inline-start:\s*auto;/s)
  assert.match(css, /\.k-table__search-input::\-webkit-search-cancel-button\s*\{[^}]*appearance:\s*none;/s)
  assert.match(css, /\.k-table th\s*\{[^}]*font-family:\s*var\(--font-mono/s)
  assert.match(css, /\.k-table__heading\s*\{[^}]*font-family:\s*var\(--font-mono/s)

  assert.match(sync, /VUE_FILES=\([^\n]*ResourceTable\.vue table\.ts/)
  assert.match(sync, /OBSOLETE_FILES=\([^\n]*ResourceTable\.css/)
})

test('keeps resource section cards bounded and supports headerless sections', () => {
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const sectionCard = fs.readFileSync(new URL('../provider-sdk/portalkit-vue/ResourceSectionCard.vue', import.meta.url), 'utf8')
  const card = css.match(/\.k-resource-section-card\s*\{([^}]*)\}/s)?.[1] ?? ''

  assert.match(card, /box-sizing:\s*border-box/)
  assert.match(sectionCard, /title\?:\s*string/)
  assert.match(sectionCard, /v-if="props\.title"[\s\S]*class="k-resource-section-card__title"/)
  assert.match(sectionCard, /v-if="props\.eyebrow \|\| props\.title \|\| props\.description \|\| \$slots\.actions"/)
  assert.match(sectionCard, /:aria-labelledby="props\.title && headingId \? headingId : undefined"/)
})

test('keeps platform-admin flat lists and navigation on shared host patterns', () => {
  const root = new URL('../portal/src/pages/bonkers/', import.meta.url)
  for (const file of ['ProvidersSection.vue', 'IdentitiesSection.vue', 'UsersSection.vue']) {
    const source = fs.readFileSync(new URL(file, root), 'utf8')
    assert.match(source, /import ResourceTable from '@\/portalkit\/ResourceTable\.vue'/)
    assert.match(source, /<ResourceTable/)
    assert.match(source, /:interactive="false"/)
    assert.match(source, /:loaded="admin\.loaded"/)
    assert.doesNotMatch(source, /<table\b/)
  }

  const providers = fs.readFileSync(new URL('ProvidersSection.vue', root), 'utf8')
  const identities = fs.readFileSync(new URL('IdentitiesSection.vue', root), 'utf8')
  assert.match(providers, /provider\.registered \? \(provider\.ready \? 'ready' : 'not ready'\) : ''/)
  assert.match(providers, /:status="providerFlag\(row, 'ready'\) \? 'ready' : 'not ready'"/)
  assert.match(identities, /\[row\.path, row\.group, row\.resource, row\.export\]/)

  const store = fs.readFileSync(new URL('../portal/src/stores/admin.ts', import.meta.url), 'utf8')
  const shell = fs.readFileSync(new URL('../portal/src/pages/BonkersPage.vue', import.meta.url), 'utf8')
  const appLayout = fs.readFileSync(new URL('../portal/src/components/AppLayout.vue', import.meta.url), 'utf8')
  assert.match(store, /const loaded = ref\(false\)/)
  assert.match(store, /identities\.value = i\s+loaded\.value = true/)
  assert.match(shell, /import \{ useSidebarExpansion \} from '@\/composables\/useSidebarExpansion'/)
  assert.match(shell, /:class="sidebarExpanded \? 'w-48' : 'w-14'"/)
  assert.match(appLayout, /const \{ sidebarExpanded, toggleSidebar \} = useSidebarExpansion\(\)/)
  assert.match(shell, /shadow-\[0_0_14px_var\(--color-accent-glow\)\]/)
  assert.match(shell, /:aria-current="\$route\.path === s\.to \? 'page' : undefined"/)
  assert.match(shell, /auth\.logout\(\)\s+void router\.replace\('\/login'\)/)
  assert.match(appLayout, /setLayoutInsets\(\{ left: '0px', right: '0px', bottom: '0px' \}\)/)
})

test('keeps dense checkboxes compact without decorative focus glow', () => {
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const checkbox = css.match(/\.k-checkbox\s*\{([^}]*)\}/)?.[1] ?? ''
  const focus = css.match(/\.k-checkbox:focus\s*\{([^}]*)\}/)?.[1] ?? ''
  const focusVisible = css.match(/\.k-checkbox:focus-visible\s*\{([^}]*)\}/)?.[1] ?? ''

  assert.match(checkbox, /width:\s*14px/)
  assert.match(checkbox, /height:\s*14px/)
  assert.match(checkbox, /min-width:\s*0/)
  assert.match(checkbox, /min-height:\s*0/)
  assert.match(checkbox, /padding:\s*0/)
  assert.match(checkbox, /accent-color:\s*var\(--color-accent/)
  assert.match(focus, /box-shadow:\s*none/)
  assert.match(focusVisible, /0 0 0 3px var\(--color-accent-subtle/)
  assert.doesNotMatch(`${checkbox}\n${focus}\n${focusVisible}`, /accent-glow/)
})

test('supports an exact, design-book-referenced exception and rejects stale locators', () => {
  const source = 'faros-provider-fixture .bubble {\n  border-radius: 14px;\n}\n'
  const valid = fixtureRepo({ 'providers/fixture/portal/src/style.css': source })
  const validResult = valid.run({
    exceptions: {
      version: 1,
      exceptions: [{
        rule: RULES.PILL_RADIUS,
        path: 'providers/fixture/portal/src/style.css',
        line: 2,
        column: 3,
        match: 'border-radius: 14px',
        reference: 'design-book §3 chat bubbles',
        reason: 'App Studio conversational bubble geometry is explicitly sanctioned.',
      }],
    },
  })
  assert.deepEqual(validResult.diagnostics, [])

  const stale = valid.run({
    exceptions: {
      version: 1,
      exceptions: [{
        rule: RULES.PILL_RADIUS,
        path: 'providers/fixture/portal/src/style.css',
        line: 2,
        column: 4,
        match: 'border-radius: 14px',
        reference: 'design-book §3 chat bubbles',
        reason: 'App Studio conversational bubble geometry is explicitly sanctioned.',
      }],
    },
  })
  assert.equal(stale.diagnostics.length, 1)
  assert.equal(stale.diagnostics[0].rule, RULES.EXCEPTION_REGISTRY)
  assert.match(stale.diagnostics[0].message, /locator/)
})

test('rejects malformed or debt-like exception registries', () => {
  const fixture = fixtureRepo({ 'providers/fixture/portal/src/style.css': '.x { color: var(--color-accent); }\n' })
  const malformed = fixture.run({
    exceptions: {
      version: 1,
      exceptions: [{
        rule: RULES.RAW_COLOR,
        path: 'providers/fixture/portal/src/style.css',
        line: 1,
        column: 1,
        match: 'color',
        reference: 'design-book §2',
        reason: 'temporary debt baseline',
      }],
    },
  })
  assert.equal(malformed.diagnostics.length, 1)
  assert.equal(malformed.diagnostics[0].rule, RULES.EXCEPTION_REGISTRY)
  assert.match(malformed.diagnostics[0].message, /debt|temporary|baseline/i)

  const unknownKey = fixture.run({
    exceptions: {
      version: 1,
      exceptions: [{
        rule: RULES.RAW_COLOR,
        path: 'providers/fixture/portal/src/style.css',
        line: 1,
        column: 1,
        match: 'color',
        reference: 'design-book §2',
        reason: 'A precise sanctioned exception.',
        expires: '2099-01-01',
      }],
    },
  })
  assert.equal(unknownKey.diagnostics.length, 1)
  assert.equal(unknownKey.diagnostics[0].rule, RULES.EXCEPTION_REGISTRY)
  assert.match(unknownKey.diagnostics[0].message, /unknown key/)
})

test('canonical roots are replaceable without weakening provider scanning', () => {
  const fixture = fixtureRepo({
    'custom/canonical.ts': `const className = 'pk-canonical';\n`,
    'providers/fixture/portal/src/App.vue': `<template><div class="k-card">ok</div></template>\n`,
  })
  const result = fixture.run({ canonicalRoots: ['custom'] })
  assert.ok(result.diagnostics.some((diagnostic) => diagnostic.rule === RULES.LEGACY_PK && diagnostic.path === 'custom/canonical.ts'))
})

test('scans host and standalone surfaces while recognizing exact authorities', () => {
  const fixture = fixtureRepo({
    'portal/src/assets/faros-ui.css': '.k-card { color: var(--color-text-primary); }\n',
    'portal/src/assets/main.css': ':root { --color-surface: #0a0b12; }\n.bad { color: #abc; }\n',
    'providers/fixture/portal/public/index.html': '<style>:root { --color-surface: #0a0b12; } body { color: #abc; }</style>\n',
  }, {
    providerRoots: ['portal', 'providers/*/portal'],
    canonicalConsumerPaths: ['portal/src/assets/faros-ui.css'],
    tokenAuthorityPaths: ['portal/src/assets/main.css', 'providers/fixture/portal/public/index.html'],
  })
  const result = fixture.run()
  assert.ok(result.files.includes('portal/src/assets/faros-ui.css'))
  assert.ok(result.files.includes('providers/fixture/portal/public/index.html'))
  assert.ok(!result.diagnostics.some((diagnostic) => diagnostic.rule === RULES.PROVIDER_K_SELECTOR))
  assert.equal(result.diagnostics.filter((diagnostic) => diagnostic.rule === RULES.RAW_COLOR).length, 2)
  assert.ok(result.diagnostics.some((diagnostic) => diagnostic.path === 'portal/src/assets/main.css' && diagnostic.match === '#abc'))
  assert.ok(result.diagnostics.some((diagnostic) => diagnostic.path.endsWith('/public/index.html') && diagnostic.match === '#abc'))
})

test('rejects unknown color token declarations in authority stylesheets', () => {
  const fixture = fixtureRepo({
    'providers/fixture/portal/src/style.css': ':root { --color-surafce: #0a0b12; }\n',
  }, {
    tokenAuthorityPaths: ['providers/fixture/portal/src/style.css'],
  })
  const result = fixture.run()
  assert.ok(result.diagnostics.some((diagnostic) => diagnostic.rule === RULES.UNKNOWN_COLOR_TOKEN && diagnostic.match === '--color-surafce'))
})
