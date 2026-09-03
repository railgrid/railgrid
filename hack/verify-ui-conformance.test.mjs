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
  const resourcePage = css.match(/\.k-resource-page\s*\{([^}]*)\}/s)?.[1] ?? ''
  const title = css.match(/\.k-resource-page__title\s*\{([^}]*)\}/s)?.[1] ?? ''
  const subtitle = css.match(/\.k-resource-page__subtitle\s*\{([^}]*)\}/s)?.[1] ?? ''
  const status = css.match(/\.k-resource-page__status\s*\{([^}]*)\}/s)?.[1] ?? ''
  const sectionTitle = css.match(/\.k-resource-section-card__title\s*\{([^}]*)\}/s)?.[1] ?? ''

  assert.match(resourcePage, /container-name:\s*resource-page;/)
  assert.match(resourcePage, /container-type:\s*inline-size;/)
  assert.match(title, /font-size:\s*clamp\(24px,\s*4vw,\s*32px\)/)
  assert.match(title, /letter-spacing:\s*-\.02em/)
  assert.match(title, /line-height:\s*1\.12/)
  assert.match(title, /overflow-wrap:\s*anywhere/)
  assert.match(title, /word-break:\s*break-word/)
  assert.match(subtitle, /overflow-wrap:\s*anywhere/)
  assert.match(subtitle, /text-align:\s*start/)
  assert.match(status, /min-width:\s*0/)
  assert.match(status, /overflow-wrap:\s*anywhere/)
  assert.match(sectionTitle, /overflow-wrap:\s*anywhere/)

  for (const width of [620, 520, 420]) {
    assert.match(css, new RegExp(`@container resource-page \\(max-width: ${width}px\\)`))
    assert.match(css, new RegExp(`@media \\(max-width: ${width}px\\)`), `${width}px fallback remains available`)
  }
  assert.match(css, /@container resource-page \(max-width: 620px\)[\s\S]*\.k-resource-page__header[\s\S]*flex-direction:\s*column;[\s\S]*\.k-resource-stat-cards[\s\S]*repeat\(2/)
  assert.match(css, /@container resource-page \(max-width: 520px\)[\s\S]*\.k-resource-section-card__header[\s\S]*flex-direction:\s*column;[\s\S]*\.k-resource-section-card__actions[\s\S]*justify-content:\s*flex-start;/)
  assert.match(css, /@container resource-page \(max-width: 420px\)[\s\S]*\.k-resource-stat-cards[\s\S]*minmax\(0, 1fr\)[\s\S]*\.k-resource-page__read-error,[\s\S]*\.k-resource-page__stale[\s\S]*align-items:\s*flex-start;/)
  assert.match(css, /@supports not \(container-type: inline-size\)[\s\S]*@media \(max-width: 620px\)[\s\S]*@media \(max-width: 520px\)[\s\S]*@media \(max-width: 420px\)/)
  assert.doesNotMatch(css, /@media\s*\(max-width:\s*859px\)/)

  const resourceCoarsePointer = css.slice(css.indexOf('@media (pointer: coarse)', css.indexOf('.k-resource-page')))
  assert.match(resourceCoarsePointer, /\.k-resource-page__actions :where\(button, a\)/)
  assert.match(resourceCoarsePointer, /\.k-resource-section-card__actions :where\(button, a\)/)
  assert.match(resourceCoarsePointer, /\.k-resource-page__retry[\s\S]*min-height:\s*44px;[\s\S]*min-width:\s*44px;/)

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

test('keeps ResourcePage read-state announcements centralized and resilient', () => {
  const page = fs.readFileSync(new URL('../provider-sdk/portalkit-vue/ResourcePage.vue', import.meta.url), 'utf8')
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')

  assert.match(page, /props\.loaded === false && !props\.error/)
  assert.match(page, /const showInitialLoading = useDelayedLoading\(initialReadPending\)/)
  assert.match(page, /:aria-hidden="showInitialLoading \? undefined : 'true'"/)
  assert.match(page, /props\.refreshMode === 'foreground'[\s\S]*Refreshing \$\{props\.title\}[\s\S]*Updating \$\{props\.title\}/)
  assert.match(page, /const staleMessageRole = computed\([\s\S]*'background' \? 'status' : 'alert'/)
  assert.match(page, /function requestRetry\(\)[\s\S]*if \(retrying\.value\) return[\s\S]*retryRequested\.value = true[\s\S]*emit\('retry'\)/)
  assert.match(css, /\.k-resource-page__meta\s*\{[^}]*color:\s*var\(--color-text-secondary/s)
  assert.match(css, /\.k-resource-page__read-message\s*\{[^}]*color:\s*var\(--color-text-primary/s)
  assert.match(css, /@media\s*\(pointer:\s*coarse\)[\s\S]*\.k-resource-page__retry\s*\{[^}]*min-height:\s*44px;[^}]*min-width:\s*44px;/)

  for (const relative of [
    '../providers/code/portal/src/views/ConnectionDetailView.vue',
    '../providers/databricks/portal/src/views/ConnectionDetailView.vue',
    '../providers/databricks/portal/src/views/TableDetailView.vue',
    '../providers/databricks/portal/src/views/WarehouseDetailView.vue',
  ]) {
    const source = fs.readFileSync(new URL(relative, import.meta.url), 'utf8')
    assert.doesNotMatch(source, /class="sr-only"[^>]*role="status"[^>]*aria-live="polite"[^>]*>Updating(?: connection)?…<\/span>/)
  }
})

test('keeps ResourceBackLink browser affordances and disabled state canonical', () => {
  const back = fs.readFileSync(new URL('../provider-sdk/portalkit-vue/ResourceBackLink.vue', import.meta.url), 'utf8')
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')

  assert.match(back, /if \(props\.disabled\) \{[\s\S]*event\.preventDefault\(\)[\s\S]*return/)
  assert.match(back, /event\.button !== 0/)
  assert.match(back, /event\.metaKey[\s\S]*event\.ctrlKey[\s\S]*event\.shiftKey[\s\S]*event\.altKey/)
  assert.match(back, /event\.preventDefault\(\)[\s\S]*emit\('back', event\)/)
  assert.match(back, /:aria-disabled="disabled \? 'true' : undefined"/)
  assert.match(back, /:tabindex="disabled \? -1 : undefined"/)
  assert.match(back, /<slot>Back<\/slot>/)
  assert.match(css, /\.k-back-action\[aria-disabled="true"\][\s\S]*opacity:\s*0\.4/)
  assert.match(css, /@media\s*\(pointer:\s*coarse\),\s*\(any-pointer:\s*coarse\)[\s\S]*\.k-back-action\s*\{[^}]*min-height:\s*44px;[^}]*min-width:\s*44px;/)
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
  assert.match(table, /ariaLabel\?: string/)
  assert.match(table, /class="k-table__scroll" role="region" :aria-label="`\$\{tableAriaLabel\} scroll area`" tabindex="0"/)
  assert.match(table, /<table class="k-table__table" :aria-label="tableAriaLabel">/)
  assert.doesNotMatch(table, /Scrollable table/)
  assert.match(table, /<slot name="after-row" :row="row" :column-count="renderedColumnCount" \/>/)
  assert.match(table, /class="k-table__pagination"/)
  assert.doesNotMatch(table, /ResourceTable\.css/)

  assert.match(css, /\.k-table\.k-table--resource\s*\{[^}]*overflow:\s*hidden;/s)
  assert.match(css, /\.k-table__scroll\s*\{[^}]*overflow-x:\s*auto;/s)
  assert.match(css, /\.k-table__cell svg\s*\{[^}]*display:\s*inline-block;[^}]*vertical-align:\s*middle;/s)
  assert.match(table, /v-if="hasConfiguredControls" class="k-table__loading-controls" aria-hidden="true"/)
  assert.match(table, /v-for="filter in filters"[^>]*k-table__loading-control--filter/)
  assert.match(table, /const staleMessageRole = computed\(\(\) => props\.refreshMode === 'background' \? 'status' : 'alert'\)/)
  assert.match(table, /const staleMessageLive = computed\(\(\) => props\.refreshMode === 'background' \? 'polite' : 'assertive'\)/)
  assert.match(table, /class="k-table__stale" :role="staleMessageRole" :aria-live="staleMessageLive"/)
  assert.match(table, /const hasQuery = computed\(\(\) => !!currentQuery\.value\.trim\(\)\)/)
  assert.match(table, /const hasFacetFilters = computed\(\(\) => Object\.values\(currentFilters\.value\)\.some\(Boolean\)\)/)
  assert.match(table, /const clearActionLabel = computed\(\(\) => hasQuery\.value && hasFacetFilters\.value \? 'Clear all' : 'Clear filters'\)/)
  assert.match(table, /const noMatchText = computed\(\(\) => \{[\s\S]*props\.combinedFilterEmptyText[\s\S]*props\.searchEmptyText[\s\S]*props\.filterEmptyText/)
  assert.match(table, /<button v-if="hasFacetFilters" class="k-table__clear-filters"[^>]*>\{\{ clearActionLabel \}\}<\/button>/)
  assert.match(table, /<div v-if="hasFacetFilters" class="shimmer k-table__loading-control k-table__loading-control--clear" \/>/)
  assert.match(table, /:aria-label="`Search \$\{tableAriaLabel\}`"/)
  assert.match(table, /const MAX_SKELETON_COLUMNS = 6/)
  assert.match(table, /visibleColumns\.value\.slice\(0, MAX_SKELETON_COLUMNS\)/)
  assert.match(table, /--k-table-loading-columns/)
  assert.match(table, /variant\?: 'queryable' \| 'simple'/)
  assert.match(table, /variant: 'queryable'/)
  assert.match(table, /:class="`k-table--\$\{variant\}`"/)
  assert.match(css, /\.k-table__scroll:focus-visible\s*\{[^}]*box-shadow:\s*inset/s)
  assert.match(css, /\.k-table__pending-cell\s*\{[^}]*text-align:\s*center;/s)
  assert.match(css, /\.k-table__page-size\s*\{[^}]*margin-inline-start:\s*auto;/s)
  assert.match(css, /\.k-table__search-input::\-webkit-search-cancel-button\s*\{[^}]*appearance:\s*none;/s)
  assert.match(css, /\.k-table th\s*\{[^}]*font-family:\s*var\(--font-mono/s)
  assert.match(css, /\.k-table__heading\s*\{[^}]*font-family:\s*var\(--font-mono/s)
  assert.match(table, /primary\?: boolean/)
  assert.match(table, /ariaLabel\?: string/)
  assert.match(table, /fullValue\?: \(row: Record<string, unknown>\) => string/)
  assert.match(table, /align\?: 'start' \| 'center' \| 'end'/)
  assert.match(table, /col\.ariaLabel \|\| col\.label \|\| col\.key/)
  assert.match(table, /k-table__heading--\$\{col\.align \?\? 'start'\}/)
  assert.match(table, /k-table__cell--\$\{col\.align \?\? 'start'\}/)
  assert.match(table, /function primaryValue\(row: Record<string, unknown>\): string/)
  assert.match(table, /column\?\.fullValue\?\.\(row\)/)
  assert.match(table, /data-full-value="primaryValue\(row\)"/)
  assert.doesNotMatch(table, /data-full-value="primaryValue\(row\)"[^>]*title=/)
  assert.doesNotMatch(table, /class="k-table__primary-value"[^>]*aria-label=/)
  assert.match(table, /columns\.find\(column => column\.primary\)\?\.key[\s\S]*columns\.find\(column => column\.key === 'name'\)\?\.key[\s\S]*columns\[0\]\?\.key/)
  assert.match(table, /v-for="col in visibleColumns"/)
  assert.match(table, /class="k-table__primary-actions"/)
  assert.match(table, /<slot :name="actionsColumn\.key"/)
  assert.doesNotMatch(table, /v-for="col in columns"/)
  assert.match(css, /\.k-table__heading--primary\s*\{\s*width:\s*100%;\s*\}/)
  assert.match(css, /\.k-table__cell--primary\s*\{\s*width:\s*100%;\s*\}/)
  assert.match(css, /\.k-table__row:hover \.k-table__primary-actions,[\s\S]*\.k-table__row:focus-within \.k-table__primary-actions/)
  assert.match(css, /@media \(hover: none\)[\s\S]*\.k-table__primary-actions,[\s\S]*\.k-table__primary-actions \.k-table-action\s*\{[^}]*opacity:\s*1;/)
  assert.match(css, /--k-table-readable-muted:\s*color-mix\(in srgb, var\(--color-text-secondary, #8a8ca6\) 90%, var\(--color-surface-raised, #111320\)\);/)
  assert.match(css, /--k-table-control-border:\s*color-mix\(in srgb, var\(--color-text-secondary, #8a8ca6\) 70%, var\(--color-surface-overlay, #171927\)\);/)
  assert.match(css, /--k-table-action-idle:\s*color-mix\(in srgb, var\(--color-text-secondary, #8a8ca6\) 80%, var\(--color-surface-raised, #111320\)\);/)
  assert.match(css, /\.k-table__search-input,[\s\S]*\.k-table__page-size-select\s*\{[^}]*border:\s*1px solid var\(--k-table-control-border/s)
  assert.match(css, /\.k-table__heading\s*\{[^}]*color:\s*var\(--k-table-readable-muted/s)
  assert.match(css, /grid-template-columns:\s*repeat\(var\(--k-table-loading-columns, 3\), minmax\(0, 1fr\)\)/)
  assert.match(css, /\.k-table\.k-table--resource \.k-table__heading--start,\s*\.k-table\.k-table--resource \.k-table__cell--start\s*\{\s*text-align:\s*start;/)
  assert.match(css, /\.k-table\.k-table--resource \.k-table__heading--center,\s*\.k-table\.k-table--resource \.k-table__cell--center\s*\{\s*text-align:\s*center;/)
  assert.match(css, /\.k-table\.k-table--resource \.k-table__heading--end,\s*\.k-table\.k-table--resource \.k-table__cell--end\s*\{\s*text-align:\s*end;/)
  const genericTableRuleIndex = css.indexOf('.k-table th')
  assert.ok(genericTableRuleIndex >= 0, 'generic table header rule must remain present')
  for (const align of ['start', 'center', 'end']) {
    const alignedSelectorIndex = css.indexOf(`.k-table.k-table--resource .k-table__heading--${align}`)
    assert.ok(alignedSelectorIndex > genericTableRuleIndex, `${align} alignment override must follow generic table rules`)
  }
  assert.match(table, /<Teleport to="body">[\s\S]*class="k-table__primary-tooltip"/)
  assert.match(css, /\.k-table__primary-tooltip\s*\{[^}]*position:\s*fixed;[^}]*visibility:\s*hidden;/s)
  assert.match(css, /\.k-table__primary-tooltip--positioned\s*\{\s*visibility:\s*visible;/)
  assert.match(css, /\.k-table-action\s*\{[^}]*color:\s*var\(--k-table-action-idle/s)
  assert.match(css, /@media \(pointer: coarse\)[\s\S]*\.k-table__page-button,[\s\S]*\.k-table-action\s*\{[^}]*height:\s*44px;[^}]*min-height:\s*44px;[^}]*min-width:\s*44px;[\s\S]*opacity:\s*1;[\s\S]*pointer-events:\s*auto;[\s\S]*width:\s*44px;/)
  assert.match(css, /@media \(pointer: coarse\)[\s\S]*\.k-table__search-clear\s*\{[^}]*min-height:\s*44px;[^}]*min-width:\s*44px;/)
  assert.match(css, /@media \(pointer: coarse\)[\s\S]*\.k-table__primary-actions\s*\{[^}]*opacity:\s*1;[^}]*pointer-events:\s*auto;/)

  assert.match(sync, /VUE_FILES=\([^\n]*ResourceTable\.vue/)
  assert.match(sync, /VUE_FILES=\([^\n]*table\.ts/)
  assert.match(sync, /OBSOLETE_FILES=\([^\n]*ResourceTable\.css/)
})

test('keeps ResourceTable quiet color roles above their contrast floors in both themes', () => {
  const host = fs.readFileSync(new URL('../portal/src/assets/main.css', import.meta.url), 'utf8')
  const dark = host.match(/@theme\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  const light = host.match(/html\.light\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''

  const token = (block, name) => {
    const value = block.match(new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6});`))?.[1]
    assert.ok(value, `${name} must be an opaque hex token for contrast verification`)
    return value
  }
  const channels = value => [1, 3, 5].map(offset => Number.parseInt(value.slice(offset, offset + 2), 16))
  const mix = (foreground, background, amount) => channels(foreground)
    .map((value, index) => Math.round(value * amount + channels(background)[index] * (1 - amount)))
  const luminance = color => color
    .map(value => value / 255)
    .map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4)
    .reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0)
  const contrast = (left, right) => {
    const [lighter, darker] = [luminance(left), luminance(right)].sort((a, b) => b - a)
    return (lighter + 0.05) / (darker + 0.05)
  }

  for (const theme of [dark, light]) {
    const secondary = token(theme, 'color-text-secondary')
    const raised = token(theme, 'color-surface-raised')
    const overlay = token(theme, 'color-surface-overlay')
    assert.ok(contrast(mix(secondary, raised, 0.9), channels(raised)) >= 4.5, 'table tertiary text must meet WCAG AA')
    assert.ok(contrast(mix(secondary, raised, 0.8), channels(raised)) >= 3, 'idle action icons must meet non-text contrast')
    assert.ok(contrast(mix(secondary, overlay, 0.7), channels(overlay)) >= 3, 'table control borders must remain identifiable')
  }
})

test('gives every ResourceTable caller a descriptive table and scroll-region name', () => {
  const sourceRoots = [
    path.resolve(new URL('../portal/', import.meta.url).pathname),
    path.resolve(new URL('../providers/', import.meta.url).pathname),
  ]
  const vueFiles = []
  const visit = (directory) => {
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      if (['.git', 'dist', 'node_modules'].includes(entry.name)) continue
      const absolute = path.join(directory, entry.name)
      if (entry.isDirectory()) visit(absolute)
      else if (entry.isFile() && entry.name.endsWith('.vue')) vueFiles.push(absolute)
    }
  }
  sourceRoots.forEach(visit)

  let callers = 0
  for (const file of vueFiles) {
    const source = fs.readFileSync(file, 'utf8')
    for (const opening of source.matchAll(/<ResourceTable\b[^>]*>/g)) {
      callers += 1
      assert.match(opening[0], /(?:aria-label|:aria-label)=\s*(?:"[^"]+"|'[^']+')/, file)
      assert.doesNotMatch(opening[0], /Scrollable table/, file)
    }
  }
  assert.ok(callers > 0, 'ResourceTable callers should be discovered')

  const workloads = fs.readFileSync(new URL('../providers/edges/portal/src/Workloads.vue', import.meta.url), 'utf8')
  assert.match(workloads, /<template #after-row="\{ row, columnCount \}">/)
  assert.match(workloads, /<td :colspan="columnCount">/)
  assert.doesNotMatch(workloads, /:colspan="workloadColumns\.length"/)
})

test('keeps generic resource-table icon actions accessible, toned, and vendored', () => {
  const action = fs.readFileSync(new URL('../provider-sdk/portalkit-vue/ResourceTableActionButton.vue', import.meta.url), 'utf8')
  const css = fs.readFileSync(new URL('../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const sync = fs.readFileSync(new URL('./sync-portalkit.sh', import.meta.url), 'utf8')

  assert.match(action, /icon: Component/)
  assert.match(action, /label: string/)
  assert.match(action, /busyLabel\?: string/)
  assert.match(action, /busy\?: boolean/)
  assert.match(action, /disabled\?: boolean/)
  assert.match(action, /type ResourceTableActionTone = 'neutral' \| 'accent' \| 'warning' \| 'danger'/)
  assert.match(action, /class="k-table-action"/)
  assert.match(action, /:class="\[`k-table-action--\$\{tone\}`/)
  assert.match(action, /:data-k-tip="accessibleLabel"/)
  assert.doesNotMatch(action, /:title="accessibleLabel"/)
  assert.match(action, /:aria-label="accessibleLabel"/)
  assert.match(action, /:aria-busy="busy \|\| undefined"/)
  assert.match(action, /:disabled="disabled \|\| busy"/)
  assert.match(action, /@click\.stop="emit\('click', \$event\)"/)
  assert.match(action, /<Loader2 v-if="busy"[^>]*k-table-action__icon--spinning/)
  assert.match(action, /<component[\s\S]*:is="icon"[\s\S]*v-else/)

  for (const tone of ['neutral', 'accent', 'warning', 'danger']) {
    assert.match(css, new RegExp(`\\.k-table-action--${tone}:hover,`))
    assert.match(css, new RegExp(`\\.k-table-action--busy\\.k-table-action--${tone}\\s*\\{`))
  }
  assert.match(css, /\.k-table-action--busy\.k-table-action--accent\s*\{[^}]*color:\s*var\(--color-accent/s)
  assert.match(css, /\.k-table-action--busy\.k-table-action--warning\s*\{[^}]*color:\s*var\(--color-warning/s)
  assert.match(sync, /VUE_FILES=\([^)]*ResourceTableActionButton\.vue/)
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
  const navigationDock = fs.readFileSync(new URL('../portal/src/composables/useNavigationDock.ts', import.meta.url), 'utf8')
  assert.match(store, /const loaded = ref\(false\)/)
  assert.match(store, /identities\.value = i\s+loaded\.value = true/)
  assert.match(shell, /import \{ useSidebarExpansion \} from '@\/composables\/useSidebarExpansion'/)
  assert.match(shell, /:class="sidebarExpanded \? 'w-48' : 'w-14'"/)
  assert.match(appLayout, /const \{ sidebarExpanded, toggleSidebar \} = useSidebarExpansion\(\)/)
  assert.match(appLayout, /import \{ useNavigationDock \} from '@\/composables\/useNavigationDock'/)
  assert.match(appLayout, /\} = useNavigationDock\(sidebarExpanded\)/)
  assert.match(shell, /shadow-\[0_0_14px_var\(--color-accent-glow\)\]/)
  assert.match(shell, /:aria-current="\$route\.path === s\.to \? 'page' : undefined"/)
  assert.match(shell, /auth\.logout\(\)\s+void router\.replace\('\/login'\)/)
  assert.match(navigationDock, /onUnmounted\(\(\) => \{[\s\S]*setLayoutInsets\(\{ left: '0px', right: '0px', bottom: '0px' \}\)/)
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

test('accepts both theme-correct on-accent fallbacks without allowing raw foreground colors', () => {
  const fixture = fixtureRepo({
    'providers/fixture/portal/src/style.css': [
      '.dark-action { color: var(--color-on-accent, #0a0b12); }',
      '.light-action { color: var(--color-on-accent, #ffffff); }',
      '.bad { color: #0a0b12; }',
      '',
    ].join('\n'),
  })
  const result = fixture.run()
  const rawColors = result.diagnostics.filter((diagnostic) => diagnostic.rule === RULES.RAW_COLOR)
  assert.equal(rawColors.length, 1)
  assert.equal(rawColors[0].match, '#0a0b12')
  assert.match(rawColors[0].path, /providers\/fixture\/portal\/src\/style\.css$/)
})

test('accepts current and standalone-compatible muted token fallbacks', () => {
  const fixture = fixtureRepo({
    'providers/fixture/portal/src/style.css': [
      '.current { color: var(--color-text-muted, #8587a1); }',
      '.standalone { color: var(--color-text-muted, #5d5f78); }',
      '.raw { color: #5d5f78; }',
      '',
    ].join('\n'),
  })
  const rawColors = fixture.run().diagnostics.filter((diagnostic) => diagnostic.rule === RULES.RAW_COLOR)
  assert.equal(rawColors.length, 1)
  assert.equal(rawColors[0].match, '#5d5f78')
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
