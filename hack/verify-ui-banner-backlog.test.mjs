import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'

import { collectOccurrences, scan, scanTemplateMarkup, LEGACY_CLASSES } from './verify-ui-banner-backlog.mjs'

const RULE = 'no-new-native-vue-feedback-recipe'

function makeBaseline(occurrences = []) {
  return { version: 1, rule: RULE, classTokens: LEGACY_CLASSES, occurrences }
}

function fixtureRepo(entries) {
  const repoRoot = mkdtempSync(path.join(os.tmpdir(), 'railgrid-ui-banner-'))
  execFileSync('git', ['init', '--quiet'], { cwd: repoRoot, stdio: 'ignore' })
  for (const [relative, content] of Object.entries(entries)) {
    const absolute = path.join(repoRoot, relative)
    mkdirSync(path.dirname(absolute), { recursive: true })
    writeFileSync(absolute, content)
  }
  return {
    repoRoot,
    close() { rmSync(repoRoot, { recursive: true, force: true }) },
  }
}

test('baseline path/tag multiplicity is stable across line movement and allows deletion', () => {
  const openingTag = '<div class="banner">'
  const fixture = fixtureRepo({
    'providers/edges/portal/src/App.vue': `<template>\n\n  ${openingTag}<p>old notice</p></div>\n</template>\n`,
  })
  try {
    const baseline = makeBaseline([{ path: 'providers/edges/portal/src/App.vue', openingTag, count: 2 }])
    assert.deepEqual(scan({ repoRoot: fixture.repoRoot, baseline }).diagnostics, [])
    writeFileSync(path.join(fixture.repoRoot, 'providers/edges/portal/src/App.vue'), `<template>\n\n\n  ${openingTag}<p>remaining notice</p></div>\n</template>\n`)
    assert.deepEqual(scan({ repoRoot: fixture.repoRoot, baseline }).diagnostics, [])
    writeFileSync(path.join(fixture.repoRoot, 'providers/edges/portal/src/App.vue'), '<template><p>consolidated</p></template>\n')
    assert.deepEqual(scan({ repoRoot: fixture.repoRoot, baseline }).diagnostics, [])
  } finally {
    fixture.close()
  }
})

test('a deleted tracked Vue file removes its occurrence without creating an incomplete-scan gap', () => {
  const openingTag = '<div class="banner error">'
  const relative = 'providers/edges/portal/src/Obsolete.vue'
  const fixture = fixtureRepo({ [relative]: `<template>${openingTag}old</div></template>\n` })
  try {
    execFileSync('git', ['add', relative], { cwd: fixture.repoRoot, stdio: 'ignore' })
    unlinkSync(path.join(fixture.repoRoot, relative))
    const baseline = makeBaseline([{ path: relative, openingTag, count: 1 }])
    const result = scan({ repoRoot: fixture.repoRoot, baseline })
    assert.deepEqual(result.diagnostics, [])
    assert.deepEqual(result.gaps, [])
    assert.ok(!result.files.includes(relative))
  } finally {
    fixture.close()
  }
})

test('rejects increased multiplicity and new path/opening-tag keys', () => {
  const openingTag = '<div class="banner error">'
  const fixture = fixtureRepo({
    'providers/edges/portal/src/App.vue': `<template>${openingTag}<p>one</p></div>${openingTag}<p>two</p></div></template>\n`,
    'portal/src/NewPage.vue': '<template><section class="banner warn">New</section></template>\n',
  })
  try {
    const result = scan({ repoRoot: fixture.repoRoot, baseline: makeBaseline([{ path: 'providers/edges/portal/src/App.vue', openingTag, count: 1 }]) })
    assert.deepEqual(result.diagnostics.map(diagnostic => [diagnostic.path, diagnostic.baselineCount, diagnostic.currentCount]), [
      ['portal/src/NewPage.vue', 0, 1],
      ['providers/edges/portal/src/App.vue', 1, 2],
    ])
    assert.ok(result.diagnostics.every(diagnostic => diagnostic.rule === RULE))
  } finally {
    fixture.close()
  }
})

test('only native elements with a literal class token are scanned', () => {
  const fixture = fixtureRepo({
    'portal/src/App.vue': `<script setup>const sample = '<div class="banner">'</script>
<template>
  <!-- <div class="banner">not rendered</div> -->
  <InlineNotification class="banner" />
  <div :class="'banner'">dynamic</div>
  <div class="bannerish">substring</div>
  <p>{{ '<button class="banner">' }}</p>
</template>
`,
  })
  try {
    const current = collectOccurrences(fixture.repoRoot)
    assert.deepEqual(current.occurrences, [])
    assert.deepEqual(scan({ repoRoot: fixture.repoRoot, baseline: makeBaseline() }).diagnostics, [])
  } finally {
    fixture.close()
  }
})

test('captures multiline native tags and exact multiplicity without line numbers', () => {
  const openingTag = '<p\n    role="alert"\n    class="banner error"\n  >'
  const markup = `<!-- ${openingTag} -->\n${openingTag}issue</p>`
  const found = scanTemplateMarkup(markup, 'providers/fixture/portal/src/Errors.vue')
  assert.deepEqual(found, [{ path: 'providers/fixture/portal/src/Errors.vue', openingTag }])
})

test('ignored Vue source is out of scope, while canonical and product Vue sources are scanned', () => {
  const fixture = fixtureRepo({
    '.gitignore': 'providers/fixture/portal/src/Ignored.vue\n',
    'providers/fixture/portal/src/Ignored.vue': '<template><div class="banner">ignored</div></template>\n',
    'providers/fixture/portal/src/App.vue': '<template><div>clean</div></template>\n',
    'providers/fixture/portal/src/portalkit/Vendored.vue': '<template><div class="banner">mirrored</div></template>\n',
    'provider-sdk/portalkit-vue/Canonical.vue': '<template><div class="banner">canonical</div></template>\n',
  })
  try {
    const current = collectOccurrences(fixture.repoRoot)
    assert.deepEqual(current.files, ['provider-sdk/portalkit-vue/Canonical.vue', 'providers/fixture/portal/src/App.vue'])
    assert.deepEqual(current.occurrences, [{ path: 'provider-sdk/portalkit-vue/Canonical.vue', openingTag: '<div class="banner">', count: 1 }])
  } finally {
    fixture.close()
  }
})


test('rejects the audited provider feedback recipes while allowing specialized read states', () => {
  for (const token of LEGACY_CLASSES) assert.equal(scanTemplateMarkup(`<div class="${token}">Failure</div>`).length, 1)
  assert.deepEqual(scanTemplateMarkup('<div class="kuery-read-state">No resources</div><InlineNotification class="read-error" />'), [])
})

test('requires the shared component for static outcome tones and undefined feedback hooks', () => {
  const failures = [
    '<section class="k-inline-notification k-inline-notification--warning"/>',
    '<div class="k-inline-notification--error k-inline-notification"/>',
    '<p class="k-inline-notification k-inline-notification--success"/>',
    '<p class="k-error"/>', '<div class="k-stale"/>',
  ]
  for (const markup of failures) assert.equal(scanTemplateMarkup(markup).length, 1, markup)
  for (const markup of [
    '<InlineNotification tone="error" />',
    '<div role="status" class="k-inline-notification k-inline-notification--info"/>',
    '<div class="k-inline-notification" :class="tone"/>',
    '<input class="k-input" aria-invalid="true"/>',
  ]) assert.equal(scanTemplateMarkup(markup).length, 0, markup)
})
