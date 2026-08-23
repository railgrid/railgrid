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
  const table = fs.readFileSync(new URL('../provider-sdk/portalkit-vue/ResourceTable.vue', import.meta.url), 'utf8')

  assert.match(styles, /import farosUIStyles from '\.\/faros-ui\.css\?raw'/)
  assert.match(css, /--faros-ui-canonical:\s*1/)
  assert.match(styles, /Never mutate an existing style element/)
  assert.doesNotMatch(styles, /style\.textContent !== farosUIStyles/)
  assert.match(table, /:tabindex="interactive \? 0 : undefined"/)
  assert.match(table, /@keydown="onRowKeydown\(row, \$event\)"/)
  assert.match(table, /isExplicitControlTarget/)
  assert.doesNotMatch(table, /:role="interactive \? 'button' : undefined"/)
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
