import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'

import { COMPONENT_REQUIRED_HEADINGS, scan } from './verify-design-docs.mjs'

const COMPONENT_HEADINGS = [
  'Purpose',
  'Use when',
  'Avoid when',
  'Anatomy and variants',
  'Behavior',
  'Content',
  'Layout and responsive behavior',
  'Accessibility',
  'Code and evidence',
  'Related guidance',
]

function fixture() {
  const repoRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'faros-design-docs-'))
  fs.mkdirSync(path.join(repoRoot, 'docs/design'), { recursive: true })
  fs.mkdirSync(path.join(repoRoot, 'src'), { recursive: true })
  fs.writeFileSync(path.join(repoRoot, 'docs/design-book.md'), '# Design intent\n')
  fs.writeFileSync(path.join(repoRoot, 'src/implementation.css'), '.surface {}\n')
  fs.writeFileSync(path.join(repoRoot, 'docs/notes.md'), '# Notes\n')
  return {
    repoRoot,
    write(relativePath, contents) {
      const absolutePath = path.join(repoRoot, relativePath)
      fs.mkdirSync(path.dirname(absolutePath), { recursive: true })
      fs.writeFileSync(absolutePath, contents)
    },
    cleanup() {
      fs.rmSync(repoRoot, { recursive: true, force: true })
    },
  }
}

function metadata(overrides = {}) {
  return {
    schema: 1,
    id: 'design.test.surface',
    title: 'Test surface',
    kind: 'pattern',
    status: 'active',
    authority: { design: 'normative', implementation: 'canonical' },
    implementation: { state: 'shipped' },
    appliesTo: ['portal'],
    owner: 'design-system',
    canonicalSource: [
      { path: 'docs/design-book.md#design-intent', role: 'design' },
      { path: 'src/implementation.css', role: 'implementation' },
    ],
    verification: {
      state: 'verified',
      checks: [{ kind: 'test', ref: 'focused validator test', status: 'passing' }],
    },
    relatedDocuments: [],
    ...overrides,
  }
}

function document(metadataValue, body = '# Test surface\n') {
  return `---\n${JSON.stringify(metadataValue, null, 2)}\n---\n\n${body}`
}

function componentBody(headings = COMPONENT_HEADINGS) {
  return `# Test component\n\n${headings.map((heading) => `## ${heading}\n\nGuidance.\n`).join('\n')}`
}

test('accepts a valid entry and emits a stable metadata catalog', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/surface.md', document(metadata()))
    const first = scan({ repoRoot: testFixture.repoRoot })
    const second = scan({ repoRoot: testFixture.repoRoot })

    assert.deepEqual(first.diagnostics, [])
    assert.equal(first.catalog.schema, 1)
    assert.equal(first.catalog.root, 'docs/design')
    assert.deepEqual(first.catalog.documents.map((entry) => entry.id), ['design.test.surface'])
    assert.equal(JSON.stringify(first.catalog), JSON.stringify(second.catalog))
    assert.ok(first.files.includes('docs/design/surface.md'))
  } finally {
    testFixture.cleanup()
  }
})

test('sorts catalog entries by metadata ID before path', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/z-path.md', document(metadata({ id: 'design.test.alpha' })))
    testFixture.write('docs/design/a-path.md', document(metadata({ id: 'design.test.zeta' })))
    const result = scan({ repoRoot: testFixture.repoRoot })

    assert.deepEqual(result.diagnostics, [])
    assert.deepEqual(result.catalog.documents.map((entry) => [entry.id, entry.path]), [
      ['design.test.alpha', 'docs/design/z-path.md'],
      ['design.test.zeta', 'docs/design/a-path.md'],
    ])
  } finally {
    testFixture.cleanup()
  }
})

test('accepts a component entry with the exact required headings', () => {
  const testFixture = fixture()
  try {
    assert.deepEqual(COMPONENT_REQUIRED_HEADINGS, COMPONENT_HEADINGS)
    testFixture.write('docs/design/component.md', document(metadata({
      id: 'design.test.component',
      kind: 'component',
    }), componentBody()))
    const result = scan({ repoRoot: testFixture.repoRoot })

    assert.deepEqual(result.diagnostics, [])
  } finally {
    testFixture.cleanup()
  }
})

test('reports the exact missing component section and repair heading', () => {
  const testFixture = fixture()
  try {
    const missing = 'Accessibility'
    testFixture.write('docs/design/component.md', document(metadata({
      id: 'design.test.component-missing-section',
      kind: 'component',
    }), componentBody(COMPONENT_HEADINGS.filter((heading) => heading !== missing))))
    const result = scan({ repoRoot: testFixture.repoRoot })
    const errors = result.diagnostics.filter((error) => error.code === 'missing-component-section')

    assert.equal(errors.length, 1)
    assert.match(errors[0].message, /missing required component section "Accessibility"/)
    assert.match(errors[0].message, /add "## Accessibility"/)
  } finally {
    testFixture.cleanup()
  }
})

test('rejects extra, duplicate, and out-of-order component sections', () => {
  const cases = [
    {
      name: 'extra section',
      headings: [...COMPONENT_HEADINGS, 'Implementation notes'],
      code: 'unexpected-component-section',
    },
    {
      name: 'duplicate section',
      headings: [...COMPONENT_HEADINGS, 'Behavior'],
      code: 'duplicate-component-section',
    },
    {
      name: 'order drift',
      headings: [COMPONENT_HEADINGS[1], COMPONENT_HEADINGS[0], ...COMPONENT_HEADINGS.slice(2)],
      code: 'invalid-component-section-order',
    },
  ]
  const testFixture = fixture()
  try {
    for (const [index, entry] of cases.entries()) {
      testFixture.write(`docs/design/component-${index}.md`, document(metadata({
        id: `design.test.component-${index}`,
        kind: 'component',
      }), componentBody(entry.headings)))
    }

    const result = scan({ repoRoot: testFixture.repoRoot })

    for (const entry of cases) {
      assert.ok(
        result.diagnostics.some((error) => error.code === entry.code),
        `${entry.name} should emit ${entry.code}: ${JSON.stringify(result.diagnostics)}`,
      )
    }
  } finally {
    testFixture.cleanup()
  }
})

test('keeps shipped implementation maturity independent from verification evidence', () => {
  const testFixture = fixture()
  try {
    for (const [id, verificationState] of [
      ['design.test.shipped-partial', 'partial'],
      ['design.test.shipped-unverified', 'unverified'],
    ]) {
      testFixture.write(`docs/design/${id.split('.').at(-1)}.md`, document(metadata({
        id,
        verification: { state: verificationState, checks: [] },
      })))
    }

    const result = scan({ repoRoot: testFixture.repoRoot })

    assert.deepEqual(result.diagnostics, [])
    assert.deepEqual(result.catalog.documents.map((entry) => entry.id), [
      'design.test.shipped-partial',
      'design.test.shipped-unverified',
    ])
  } finally {
    testFixture.cleanup()
  }
})

test('uses not-applicable for policies with no implementation authority', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/review-policy.md', document(metadata({
      id: 'design.test.review-policy',
      kind: 'policy',
      authority: { design: 'normative', implementation: 'none' },
      implementation: { state: 'not-applicable' },
      canonicalSource: [{ path: 'docs/design-book.md#design-intent', role: 'design' }],
      verification: { state: 'unverified', checks: [] },
    })))

    const result = scan({ repoRoot: testFixture.repoRoot })

    assert.deepEqual(result.diagnostics, [])
  } finally {
    testFixture.cleanup()
  }
})

test('rejects both directions of the implementation authority pairing', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/authority-required.md', document(metadata({
      id: 'design.test.authority-required',
      implementation: { state: 'not-applicable' },
      canonicalSource: [{ path: 'docs/design-book.md#design-intent', role: 'design' }],
    })))
    testFixture.write('docs/design/state-required.md', document(metadata({
      id: 'design.test.state-required',
      authority: { design: 'normative', implementation: 'none' },
      implementation: { state: 'planned' },
      canonicalSource: [{ path: 'docs/design-book.md#design-intent', role: 'design' }],
    })))

    const result = scan({ repoRoot: testFixture.repoRoot })
    const mismatches = result.diagnostics.filter((error) => error.code === 'implementation-authority-mismatch')

    assert.equal(mismatches.length, 2)
    assert.ok(mismatches.some((error) => error.path.endsWith('authority-required.md')))
    assert.ok(mismatches.some((error) => error.path.endsWith('state-required.md')))
  } finally {
    testFixture.cleanup()
  }
})

test('reports malformed metadata instead of silently treating it as prose', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/no-frontmatter.md', '# Missing metadata\n')
    testFixture.write('docs/design/bad-json.md', '---\n{"schema": 1,}\n---\n\n# Bad\n')
    testFixture.write('docs/design/bad-shape.md', document(metadata({ canonicalSource: {}, relatedDocuments: {} })))
    const result = scan({ repoRoot: testFixture.repoRoot })

    assert.ok(result.diagnostics.some((error) => error.code === 'missing-metadata' && error.path.endsWith('no-frontmatter.md')))
    assert.ok(result.diagnostics.some((error) => error.code === 'malformed-metadata' && error.path.endsWith('bad-json.md')))
    assert.ok(result.diagnostics.some((error) => error.code === 'invalid-metadata-type' && error.path.endsWith('bad-shape.md')))
    assert.ok(result.diagnostics.every((error) => error.line >= 1 && error.column >= 1))
  } finally {
    testFixture.cleanup()
  }
})

test('rejects duplicate IDs across otherwise valid entries', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/one.md', document(metadata({ title: 'One' })))
    testFixture.write('docs/design/two.md', document(metadata({ title: 'Two' })))
    const result = scan({ repoRoot: testFixture.repoRoot })
    const duplicates = result.diagnostics.filter((error) => error.code === 'duplicate-id')

    assert.equal(duplicates.length, 2)
    assert.deepEqual(duplicates.map((error) => error.path), ['docs/design/one.md', 'docs/design/two.md'])
  } finally {
    testFixture.cleanup()
  }
})

test('checks missing local sources, related links, and body links', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/missing.md', document(metadata({
      canonicalSource: [
        { path: 'docs/design-book.md#design-intent', role: 'design' },
        { path: 'src/does-not-exist.css', role: 'implementation' },
      ],
      relatedDocuments: [{ id: 'design.unknown', relation: 'related', path: 'docs/missing-notes.md' }],
    }), '[Missing body link](missing-page.md)\n'))
    const result = scan({ repoRoot: testFixture.repoRoot })
    const codes = new Set(result.diagnostics.map((error) => error.code))

    assert.ok(codes.has('missing-canonical-source'))
    assert.ok(codes.has('missing-related-link'))
    assert.ok(codes.has('missing-related-document'))
    assert.ok(codes.has('missing-link'))
  } finally {
    testFixture.cleanup()
  }
})

test('validates README links without requiring catalog metadata', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/README.md', '[Existing](../notes.md#notes)\n[Missing](missing.md)\n')
    testFixture.write('docs/notes.md', '# Notes\n')

    const result = scan({ repoRoot: testFixture.repoRoot })

    assert.deepEqual(result.files, [])
    assert.deepEqual(result.diagnostics.map((error) => error.code), ['missing-link'])
    assert.equal(result.diagnostics[0].path, 'docs/design/README.md')
  } finally {
    testFixture.cleanup()
  }
})

test('validates same-document anchors', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/anchors.md', document(metadata({
      id: 'design.test.anchors',
    }), '# Test surface\n\n[Existing](#local-section)\n[Missing](#missing-section)\n\n## Local section\n'))

    const result = scan({ repoRoot: testFixture.repoRoot })

    assert.deepEqual(result.diagnostics.map((error) => error.code), ['missing-anchor'])
    assert.match(result.diagnostics[0].message, /missing-section/)
  } finally {
    testFixture.cleanup()
  }
})

test('ignores headings and links inside fenced or inline code', () => {
  const testFixture = fixture()
  try {
    const body = `${componentBody(COMPONENT_HEADINGS.filter((heading) => heading !== 'Purpose'))}

[Existing](../notes.md#notes)
[Missing](missing-normal.md)
[Real anchor](#test-component)
[Fake fenced anchor](#fenced-anchor)
[Fake inline anchor](#inline-anchor)

\`\`\`markdown
## Purpose
[Ignored fenced link](missing-fenced.md)
## Fenced anchor
\`\`\`

\`## Inline anchor\`
\`[Ignored inline link](missing-inline.md)\`
`
    testFixture.write('docs/design/code-examples.md', document(metadata({
      id: 'design.test.code-examples',
      kind: 'component',
    }), body))

    const result = scan({ repoRoot: testFixture.repoRoot })
    const codes = result.diagnostics.map((error) => error.code)

    assert.equal(codes.filter((code) => code === 'missing-component-section').length, 1)
    assert.match(result.diagnostics.find((error) => error.code === 'missing-component-section').message, /Purpose/)
    assert.equal(codes.filter((code) => code === 'unexpected-component-section').length, 0)
    assert.equal(codes.filter((code) => code === 'duplicate-component-section').length, 0)
    assert.equal(codes.filter((code) => code === 'missing-link').length, 1)
    assert.equal(codes.filter((code) => code === 'missing-anchor').length, 2)
  } finally {
    testFixture.cleanup()
  }
})

test('fails closed with an actionable diagnostic when design traversal is unreadable', () => {
  const testFixture = fixture()
  const unreadableDirectory = path.join(testFixture.repoRoot, 'docs/design/private')
  const originalReaddirSync = fs.readdirSync
  try {
    fs.mkdirSync(unreadableDirectory, { recursive: true })
    testFixture.write('docs/design/entry.md', document(metadata()))
    fs.readdirSync = (directory, options) => {
      if (path.resolve(directory) === unreadableDirectory) {
        const error = new Error('permission denied')
        error.code = 'EACCES'
        throw error
      }
      return originalReaddirSync(directory, options)
    }

    const result = scan({ repoRoot: testFixture.repoRoot })
    const errors = result.diagnostics.filter((error) => error.code === 'unreadable-directory')

    assert.equal(errors.length, 1)
    assert.match(errors[0].message, /cannot traverse directory/)
    assert.match(errors[0].message, /permission denied/)
    assert.match(errors[0].message, /check that it exists and is readable/)
  } finally {
    fs.readdirSync = originalReaddirSync
    testFixture.cleanup()
  }
})

test('rejects reciprocal implements relations', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/alpha.md', document(metadata({
      id: 'design.test.alpha',
      relatedDocuments: [{ id: 'design.test.beta', relation: 'implements' }],
    })))
    testFixture.write('docs/design/beta.md', document(metadata({
      id: 'design.test.beta',
      relatedDocuments: [{ id: 'design.test.alpha', relation: 'implements' }],
    })))

    const result = scan({ repoRoot: testFixture.repoRoot })
    const errors = result.diagnostics.filter((error) => error.code === 'reciprocal-implements')

    assert.equal(errors.length, 2)
    assert.deepEqual(errors.map((error) => error.path), [
      'docs/design/alpha.md',
      'docs/design/beta.md',
    ])
    assert.ok(errors.every((error) => /both directions/.test(error.message)))
  } finally {
    testFixture.cleanup()
  }
})

test('rejects enum values outside the documented vocabulary', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/enums.md', document(metadata({
      kind: 'widget',
      status: 'released',
      authority: { design: 'binding', implementation: 'source-of-truth' },
      implementation: { state: 'done' },
      canonicalSource: [
        { path: 'docs/design-book.md#design-intent', role: 'intent' },
        { path: 'src/implementation.css', role: 'code' },
      ],
      verification: {
        state: 'complete',
        checks: [{ kind: 'ci', ref: 'check', status: 'green' }],
      },
      relatedDocuments: [{ id: 'design.other', relation: 'blocks' }],
    })))
    const result = scan({ repoRoot: testFixture.repoRoot })
    const invalidEnums = result.diagnostics.filter((error) => error.code === 'invalid-enum')

    assert.ok(invalidEnums.length >= 8, `expected enum diagnostics, got ${JSON.stringify(result.diagnostics)}`)
    assert.ok(invalidEnums.some((error) => error.message.includes('kind')))
    assert.ok(invalidEnums.some((error) => error.message.includes('verification check.status')))
  } finally {
    testFixture.cleanup()
  }
})

test('keeps templates and support documents out of the catalog scan', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/README.md', '# Index\n')
    testFixture.write('docs/design/schema.md', '# Schema\n')
    testFixture.write('docs/design/templates/example.md', '# Template without metadata\n')
    testFixture.write('docs/design/entry.md', document(metadata()))
    const result = scan({ repoRoot: testFixture.repoRoot })

    assert.deepEqual(result.diagnostics, [])
    assert.deepEqual(result.files, ['docs/design/entry.md'])
  } finally {
    testFixture.cleanup()
  }
})

test('recursively catalogs taxonomy entries while excluding nested templates', () => {
  const testFixture = fixture()
  try {
    testFixture.write('docs/design/tokens/surface.md', document(metadata({
      id: 'design.test.tokens.surface',
      title: 'Nested surface token',
      kind: 'token',
    })))
    testFixture.write('docs/design/tokens/templates/example.md', document(metadata({
      id: 'design.test.tokens.template',
      title: 'Template that must not be cataloged',
    })))

    const result = scan({ repoRoot: testFixture.repoRoot })

    assert.deepEqual(result.diagnostics, [])
    assert.deepEqual(result.files, ['docs/design/tokens/surface.md'])
    assert.deepEqual(result.catalog.documents.map((entry) => entry.id), ['design.test.tokens.surface'])
  } finally {
    testFixture.cleanup()
  }
})
