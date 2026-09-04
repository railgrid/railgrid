#!/usr/bin/env node

/**
 * Validate the structured metadata used by docs/design.
 *
 * Design documents deliberately use JSON as their front matter. JSON is a
 * useful, strict subset of YAML, and keeping parsing here dependency-free
 * means the documentation gate works in a fresh checkout without npm setup.
 * The validator also checks repository-local references so an entry cannot
 * advertise an implementation or a related document that no longer exists.
 */

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(HERE, '..')

export const DESIGN_SCHEMA_VERSION = 1
export const DESIGN_ROOT = 'docs/design'

export const DESIGN_KINDS = Object.freeze([
  'system',
  'principle',
  'token',
  'recipe',
  'component',
  'pattern',
  'journey',
  'policy',
  'decision',
  'reference',
])

export const DESIGN_STATUSES = Object.freeze([
  'active',
  'draft',
  'proposed',
  'deprecated',
  'superseded',
])

export const AUTHORITY_DESIGN = Object.freeze(['normative', 'informative'])
export const AUTHORITY_IMPLEMENTATION = Object.freeze(['canonical', 'reference', 'none'])
export const IMPLEMENTATION_STATES = Object.freeze([
  'shipped',
  'partial',
  'planned',
  'not-started',
  'not-applicable',
  'retired',
])
export const SOURCE_ROLES = Object.freeze(['design', 'implementation', 'reference'])
export const VERIFICATION_STATES = Object.freeze(['verified', 'partial', 'unverified'])
export const CHECK_KINDS = Object.freeze(['command', 'test', 'browser', 'review'])
export const CHECK_STATUSES = Object.freeze(['passing', 'failing', 'pending', 'not-run'])
export const RELATED_RELATIONS = Object.freeze([
  'related',
  'extends',
  'implements',
  'supersedes',
  'prerequisite',
  'see-also',
])

export const COMPONENT_REQUIRED_HEADINGS = Object.freeze([
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
])

const TOP_LEVEL_KEYS = new Set([
  'schema',
  'id',
  'title',
  'kind',
  'status',
  'authority',
  'implementation',
  'appliesTo',
  'owner',
  'canonicalSource',
  'verification',
  'relatedDocuments',
])

const AUTHORITY_KEYS = new Set(['design', 'implementation'])
const IMPLEMENTATION_KEYS = new Set(['state', 'notes'])
const SOURCE_KEYS = new Set(['path', 'role', 'label'])
const VERIFICATION_KEYS = new Set(['state', 'checks'])
const CHECK_KEYS = new Set(['kind', 'ref', 'status', 'evidence'])
const RELATED_KEYS = new Set(['id', 'relation', 'path'])
const SUPPORT_DOCUMENTS = new Set(['README.md', 'schema.md', 'CONTRIBUTING.md'])

const ID_RE = /^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$/
const OWNER_RE = /^[a-z][a-z0-9]*(?:[-./][a-z0-9]+)*$/
const APPLY_TO_RE = /^[a-z][a-z0-9]*(?:[-./:][a-z0-9]+)*$/
const EXTERNAL_REFERENCE_RE = /^(?:https?:|mailto:)/i
const MARKDOWN_LINK_RE = /!?(?:\[[^\]]*\])\(\s*(<[^>]+>|[^)\s]+)(?:\s+["'][^"']*["'])?\s*\)/g

export function isStableDesignID(value) {
  return typeof value === 'string' && ID_RE.test(value)
}

function isPlainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function normalizePath(value) {
  return value.replaceAll('\\', '/')
}

function compareStrings(left, right) {
  return left === right ? 0 : (left < right ? -1 : 1)
}

function relativePath(repoRoot, absolutePath) {
  const relative = normalizePath(path.relative(repoRoot, absolutePath))
  return relative || '.'
}

function diagnostic(code, documentPath, message, line = 1, column = 1) {
  return { code, path: normalizePath(documentPath), line, column, message }
}

function compareDiagnostics(left, right) {
  for (const key of ['path', 'line', 'column', 'code', 'message']) {
    if (left[key] < right[key]) return -1
    if (left[key] > right[key]) return 1
  }
  return 0
}

function compareDocuments(left, right) {
  const leftID = left.metadata?.id ?? ''
  const rightID = right.metadata?.id ?? ''
  for (const [leftValue, rightValue] of [[leftID, rightID], [left.path, right.path]]) {
    if (leftValue < rightValue) return -1
    if (leftValue > rightValue) return 1
  }
  return 0
}

function lineColumnAt(text, offset) {
  const bounded = Math.max(0, Math.min(offset, text.length))
  const prefix = text.slice(0, bounded)
  const lines = prefix.split('\n')
  return { line: lines.length, column: lines.at(-1).length + 1 }
}

function parseJSONFrontMatter(text, documentPath) {
  const source = text.replace(/^\uFEFF/, '').replaceAll('\r\n', '\n')
  const lines = source.split('\n')
  if (lines[0] !== '---') {
    return {
      metadata: null,
      body: source,
      diagnostics: [diagnostic(
        'missing-metadata',
        documentPath,
        'design documents must begin with a JSON metadata block between --- lines',
      )],
    }
  }

  const closingIndex = lines.findIndex((line, index) => index > 0 && line === '---')
  if (closingIndex === -1) {
    return {
      metadata: null,
      body: '',
      diagnostics: [diagnostic(
        'unterminated-metadata',
        documentPath,
        'metadata block is missing its closing --- line',
      )],
    }
  }

  const metadataText = lines.slice(1, closingIndex).join('\n').trim()
  if (!metadataText) {
    return {
      metadata: null,
      body: lines.slice(closingIndex + 1).join('\n'),
      diagnostics: [diagnostic('malformed-metadata', documentPath, 'metadata block cannot be empty')],
    }
  }

  let metadata
  try {
    metadata = JSON.parse(metadataText)
  } catch (error) {
    const position = typeof error?.message === 'string'
      ? Number(error.message.match(/position (\d+)/i)?.[1] ?? 0)
      : 0
    const location = lineColumnAt(metadataText, Number.isFinite(position) ? position : 0)
    return {
      metadata: null,
      body: lines.slice(closingIndex + 1).join('\n'),
      diagnostics: [diagnostic(
        'malformed-metadata',
        documentPath,
        `metadata is not valid JSON: ${error.message}`,
        location.line + 1,
        location.column,
      )],
    }
  }

  if (!isPlainObject(metadata)) {
    return {
      metadata: null,
      body: lines.slice(closingIndex + 1).join('\n'),
      diagnostics: [diagnostic('malformed-metadata', documentPath, 'metadata must be a JSON object')],
    }
  }

  return {
    metadata,
    body: lines.slice(closingIndex + 1).join('\n'),
    diagnostics: [],
  }
}

function reportUnknownKeys(value, allowed, label, documentPath, errors) {
  if (!isPlainObject(value)) return
  for (const key of Object.keys(value).sort()) {
    if (!allowed.has(key)) errors.push(diagnostic(
      'unknown-metadata-key',
      documentPath,
      `${label} has unknown key ${JSON.stringify(key)}`,
    ))
  }
}

function requireString(value, field, documentPath, errors, { pattern, description } = {}) {
  if (typeof value !== 'string' || !value.trim()) {
    errors.push(diagnostic('invalid-metadata-type', documentPath, `${field} must be a non-empty string`))
    return false
  }
  if (pattern && !pattern.test(value)) {
    errors.push(diagnostic(
      'invalid-metadata-value',
      documentPath,
      `${field} must ${description ?? 'match its documented format'}`,
    ))
    return false
  }
  return true
}

function requireEnum(value, field, values, documentPath, errors) {
  if (!values.includes(value)) {
    errors.push(diagnostic(
      'invalid-enum',
      documentPath,
      `${field} must be one of: ${values.join(', ')}`,
    ))
    return false
  }
  return true
}

function requireStringArray(value, field, documentPath, errors, { nonEmpty = true, pattern } = {}) {
  if (!Array.isArray(value)) {
    errors.push(diagnostic('invalid-metadata-type', documentPath, `${field} must be an array of strings`))
    return false
  }
  if (nonEmpty && value.length === 0) {
    errors.push(diagnostic('missing-metadata', documentPath, `${field} must contain at least one entry`))
  }
  const seen = new Set()
  for (const entry of value) {
    if (typeof entry !== 'string' || !entry.trim()) {
      errors.push(diagnostic('invalid-metadata-type', documentPath, `${field} entries must be non-empty strings`))
      continue
    }
    if (pattern && !pattern.test(entry)) {
      errors.push(diagnostic('invalid-metadata-value', documentPath, `${field} entry ${JSON.stringify(entry)} has an invalid format`))
    }
    if (seen.has(entry)) errors.push(diagnostic('duplicate-metadata-value', documentPath, `${field} contains duplicate ${JSON.stringify(entry)}`))
    seen.add(entry)
  }
  return true
}

function validateMetadata(metadata, documentPath) {
  const errors = []
  reportUnknownKeys(metadata, TOP_LEVEL_KEYS, 'metadata', documentPath, errors)

  if (metadata.schema !== DESIGN_SCHEMA_VERSION) {
    errors.push(diagnostic('invalid-schema', documentPath, `schema must be ${DESIGN_SCHEMA_VERSION}`))
  }
  requireString(metadata.id, 'id', documentPath, errors, {
    pattern: ID_RE,
    description: 'be a stable lowercase dot-separated identifier',
  })
  requireString(metadata.title, 'title', documentPath, errors)
  requireEnum(metadata.kind, 'kind', DESIGN_KINDS, documentPath, errors)
  requireEnum(metadata.status, 'status', DESIGN_STATUSES, documentPath, errors)

  if (!isPlainObject(metadata.authority)) {
    errors.push(diagnostic('invalid-metadata-type', documentPath, 'authority must be an object'))
  } else {
    reportUnknownKeys(metadata.authority, AUTHORITY_KEYS, 'authority', documentPath, errors)
    requireEnum(metadata.authority.design, 'authority.design', AUTHORITY_DESIGN, documentPath, errors)
    requireEnum(metadata.authority.implementation, 'authority.implementation', AUTHORITY_IMPLEMENTATION, documentPath, errors)
  }

  if (!isPlainObject(metadata.implementation)) {
    errors.push(diagnostic('invalid-metadata-type', documentPath, 'implementation must be an object'))
  } else {
    reportUnknownKeys(metadata.implementation, IMPLEMENTATION_KEYS, 'implementation', documentPath, errors)
    requireEnum(metadata.implementation.state, 'implementation.state', IMPLEMENTATION_STATES, documentPath, errors)
    if (metadata.implementation.notes !== undefined) requireString(metadata.implementation.notes, 'implementation.notes', documentPath, errors)
  }

  requireStringArray(metadata.appliesTo, 'appliesTo', documentPath, errors, { pattern: APPLY_TO_RE })
  requireString(metadata.owner, 'owner', documentPath, errors, {
    pattern: OWNER_RE,
    description: 'be a lowercase owner/team slug',
  })

  if (!Array.isArray(metadata.canonicalSource)) {
    errors.push(diagnostic('invalid-metadata-type', documentPath, 'canonicalSource must be an array of source objects'))
  } else {
    if (metadata.canonicalSource.length === 0) {
      errors.push(diagnostic('missing-metadata', documentPath, 'canonicalSource must contain at least one source'))
    }
    let designSources = 0
    let implementationSources = 0
    for (const source of metadata.canonicalSource) {
      if (!isPlainObject(source)) {
        errors.push(diagnostic('invalid-metadata-type', documentPath, 'canonicalSource entries must be objects'))
        continue
      }
      reportUnknownKeys(source, SOURCE_KEYS, 'canonicalSource entry', documentPath, errors)
      requireString(source.path, 'canonicalSource.path', documentPath, errors)
      requireEnum(source.role, 'canonicalSource.role', SOURCE_ROLES, documentPath, errors)
      if (source.label !== undefined) requireString(source.label, 'canonicalSource.label', documentPath, errors)
      if (source.role === 'design') designSources += 1
      if (source.role === 'implementation') implementationSources += 1
    }
    if (designSources === 0) errors.push(diagnostic('missing-metadata', documentPath, 'canonicalSource needs a design source'))
    const implementationState = metadata.implementation?.state
    const implementationAuthority = metadata.authority?.implementation
    if (['shipped', 'partial', 'retired'].includes(implementationState) && implementationSources === 0) {
      errors.push(diagnostic('missing-metadata', documentPath, `implementation.state ${JSON.stringify(implementationState)} needs a canonicalSource entry with role implementation`))
    }
    if (implementationAuthority === 'none' && implementationSources > 0) {
      errors.push(diagnostic('invalid-metadata-value', documentPath, 'authority.implementation none cannot list implementation canonical sources'))
    }
    if (implementationState === 'not-applicable' && implementationAuthority !== 'none') {
      errors.push(diagnostic('implementation-authority-mismatch', documentPath, 'implementation.state not-applicable requires authority.implementation none'))
    }
    if (implementationAuthority === 'none' && implementationState !== 'not-applicable') {
      errors.push(diagnostic('implementation-authority-mismatch', documentPath, 'authority.implementation none requires implementation.state not-applicable'))
    }
  }

  if (!isPlainObject(metadata.verification)) {
    errors.push(diagnostic('invalid-metadata-type', documentPath, 'verification must be an object'))
  } else {
    reportUnknownKeys(metadata.verification, VERIFICATION_KEYS, 'verification', documentPath, errors)
    requireEnum(metadata.verification.state, 'verification.state', VERIFICATION_STATES, documentPath, errors)
    if (!Array.isArray(metadata.verification.checks)) {
      errors.push(diagnostic('invalid-metadata-type', documentPath, 'verification.checks must be an array'))
    } else {
      for (const check of metadata.verification.checks) {
        if (!isPlainObject(check)) {
          errors.push(diagnostic('invalid-metadata-type', documentPath, 'verification.checks entries must be objects'))
          continue
        }
        reportUnknownKeys(check, CHECK_KEYS, 'verification check', documentPath, errors)
        requireEnum(check.kind, 'verification check.kind', CHECK_KINDS, documentPath, errors)
        requireString(check.ref, 'verification check.ref', documentPath, errors)
        requireEnum(check.status, 'verification check.status', CHECK_STATUSES, documentPath, errors)
        if (check.evidence !== undefined) requireString(check.evidence, 'verification check.evidence', documentPath, errors)
      }
      const verificationState = metadata.verification.state
      const passing = metadata.verification.checks.filter((check) => check?.status === 'passing')
      const failing = metadata.verification.checks.filter((check) => check?.status === 'failing')
      if (verificationState === 'verified' && (passing.length === 0 || failing.length > 0)) {
        errors.push(diagnostic('invalid-verification', documentPath, 'verified documentation needs a passing check and cannot include failing checks'))
      }
    }
  }

  if (!Array.isArray(metadata.relatedDocuments)) {
    errors.push(diagnostic('invalid-metadata-type', documentPath, 'relatedDocuments must be an array'))
  } else {
    const seenRelations = new Set()
    for (const related of metadata.relatedDocuments) {
      if (!isPlainObject(related)) {
        errors.push(diagnostic('invalid-metadata-type', documentPath, 'relatedDocuments entries must be objects'))
        continue
      }
      reportUnknownKeys(related, RELATED_KEYS, 'relatedDocuments entry', documentPath, errors)
      requireString(related.id, 'relatedDocuments.id', documentPath, errors, { pattern: ID_RE, description: 'be a stable lowercase dot-separated identifier' })
      requireEnum(related.relation, 'relatedDocuments.relation', RELATED_RELATIONS, documentPath, errors)
      if (related.path !== undefined) requireString(related.path, 'relatedDocuments.path', documentPath, errors)
      if (related.id && related.id === metadata.id) errors.push(diagnostic('invalid-related-document', documentPath, 'relatedDocuments cannot point to the document itself'))
      const relationKey = `${related.id}\u0000${related.relation}`
      if (seenRelations.has(relationKey)) errors.push(diagnostic('duplicate-related-document', documentPath, `relatedDocuments repeats ${JSON.stringify(related.id)}`))
      seenRelations.add(relationKey)
    }
  }

  return errors
}

function splitReference(reference) {
  const hash = reference.indexOf('#')
  if (hash === -1) return { target: reference, anchor: '' }
  return { target: reference.slice(0, hash), anchor: reference.slice(hash + 1) }
}

function maskRange(masked, source, start, end) {
  for (let index = start; index < end; index += 1) {
    if (source[index] !== '\n') masked[index] = ' '
  }
}

/**
 * Replace fenced blocks and inline code spans with spaces while preserving
 * line breaks. This keeps the source offsets and Markdown syntax outside code
 * intact, but prevents code examples from becoming validator input.
 */
function maskMarkdownCode(text) {
  const source = text.replaceAll('\r\n', '\n')
  const masked = source.split('')
  const maskedCode = new Array(source.length).fill(false)
  const mask = (start, end) => {
    maskRange(masked, source, start, end)
    for (let index = start; index < end; index += 1) maskedCode[index] = true
  }

  let offset = 0
  let fence = null
  for (const line of source.split('\n')) {
    const lineEnd = offset + line.length
    if (fence) {
      mask(offset, lineEnd)
      const closing = new RegExp(`^ {0,3}${fence.marker}{${fence.length},}\\s*$`)
      if (closing.test(line)) fence = null
      offset = lineEnd + 1
      continue
    }

    const opening = line.match(/^ {0,3}(`{3,}|~{3,})(.*)$/)
    const marker = opening?.[1]
    const info = opening?.[2] ?? ''
    if (marker && (marker[0] === '~' || !info.includes('`'))) {
      mask(offset, lineEnd)
      fence = { marker: marker[0], length: marker.length }
    }
    offset = lineEnd + 1
  }

  const isEscaped = (index) => {
    let backslashes = 0
    for (let cursor = index - 1; cursor >= 0 && source[cursor] === '\\'; cursor -= 1) backslashes += 1
    return backslashes % 2 === 1
  }

  for (let index = 0; index < source.length; index += 1) {
    if (maskedCode[index] || source[index] !== '`' || isEscaped(index)) continue
    let runLength = 1
    while (source[index + runLength] === '`') runLength += 1
    let closingStart = -1
    let cursor = index + runLength
    while (cursor < source.length) {
      if (maskedCode[cursor] || source[cursor] !== '`') {
        cursor += 1
        continue
      }
      let closingLength = 1
      while (source[cursor + closingLength] === '`') closingLength += 1
      if (closingLength === runLength) {
        closingStart = cursor
        break
      }
      cursor += closingLength
    }
    if (closingStart === -1) continue
    mask(index, closingStart + runLength)
    index = closingStart + runLength - 1
  }

  return masked.join('')
}

function headingSlugs(text) {
  const slugs = new Set()
  const counts = new Map()
  for (const { text: headingText } of markdownHeadings(text)) {
    const heading = headingText
      .replace(/[`*_~]/g, '')
      .replace(/<[^>]*>/g, '')
      .normalize('NFKD')
      .replace(/[\u0300-\u036f]/g, '')
      .toLowerCase()
      .replace(/[^a-z0-9\s-]/g, '')
      .trim()
      .replace(/\s+/g, '-')
    if (!heading) continue
    const count = counts.get(heading) ?? 0
    counts.set(heading, count + 1)
    slugs.add(count === 0 ? heading : `${heading}-${count}`)
  }
  return slugs
}

function markdownHeadings(text) {
  const headings = []
  const source = text.replaceAll('\r\n', '\n')
  const masked = maskMarkdownCode(source)
  const lines = source.split('\n')
  const maskedLines = masked.split('\n')
  for (const [index, line] of lines.entries()) {
    const syntaxMatch = maskedLines[index].match(/^ {0,3}(#{1,6})(?:\s+|$)/)
    if (!syntaxMatch) continue
    const match = line.match(/^ {0,3}(#{1,6})\s+(.+?)\s*#*\s*$/)
    if (!match || match[1].length !== syntaxMatch[1].length) continue
    headings.push({ level: match[1].length, text: match[2].trim(), line: index + 1 })
  }
  return headings
}

function validateComponentHeadings(document, errors) {
  const headings = markdownHeadings(document.body).filter((heading) => heading.level === 2)
  const required = new Set(COMPONENT_REQUIRED_HEADINGS)
  const seen = new Set()

  for (const heading of headings) {
    if (seen.has(heading.text)) {
      errors.push(diagnostic(
        'duplicate-component-section',
        document.path,
        `component section ${JSON.stringify(heading.text)} is duplicated`,
        heading.line,
      ))
    }
    seen.add(heading.text)
    if (!required.has(heading.text)) {
      errors.push(diagnostic(
        'unexpected-component-section',
        document.path,
        `component section ${JSON.stringify(heading.text)} is not one of the required sections`,
        heading.line,
      ))
    }
  }

  for (const heading of COMPONENT_REQUIRED_HEADINGS) {
    if (!seen.has(heading)) {
      errors.push(diagnostic(
        'missing-component-section',
        document.path,
        `missing required component section "${heading}"; add "## ${heading}"`,
      ))
    }
  }

  const hasExactlyRequiredSections = headings.length === COMPONENT_REQUIRED_HEADINGS.length
    && headings.every((heading) => required.has(heading.text))
    && seen.size === COMPONENT_REQUIRED_HEADINGS.length
  if (hasExactlyRequiredSections && headings.some((heading, index) => heading.text !== COMPONENT_REQUIRED_HEADINGS[index])) {
    errors.push(diagnostic(
      'invalid-component-section-order',
      document.path,
      `component sections must appear in this order: ${COMPONENT_REQUIRED_HEADINGS.join(', ')}`,
      headings.find((heading, index) => heading.text !== COMPONENT_REQUIRED_HEADINGS[index]).line,
    ))
  }
}

function checkLocalReference({ reference, baseDirectory, repoRoot, documentPath, errors, code, label, checkAnchor = true }) {
  if (typeof reference !== 'string' || !reference.trim()) return
  const trimmed = reference.trim()
  if (EXTERNAL_REFERENCE_RE.test(trimmed) || trimmed.startsWith('//')) return
  const { target, anchor } = splitReference(trimmed)
  let decodedTarget
  let decodedAnchor
  try {
    decodedTarget = decodeURIComponent(target)
    decodedAnchor = decodeURIComponent(anchor)
  } catch {
    errors.push(diagnostic('invalid-reference', documentPath, `${label} is not URI-decodable: ${JSON.stringify(reference)}`))
    return
  }
  let absolute
  if (!decodedTarget) {
    absolute = path.resolve(repoRoot, documentPath)
  } else if (decodedTarget.startsWith('/')) {
    absolute = path.resolve(repoRoot, decodedTarget.slice(1))
  } else {
    absolute = path.resolve(baseDirectory, decodedTarget)
  }
  const rootRelative = path.relative(repoRoot, absolute)
  if (rootRelative.startsWith('..') || path.isAbsolute(rootRelative)) {
    errors.push(diagnostic('invalid-reference', documentPath, `${label} must stay inside the repository: ${JSON.stringify(reference)}`))
    return
  }
  if (!fs.existsSync(absolute)) {
    errors.push(diagnostic(code, documentPath, `${label} points to missing local path ${JSON.stringify(normalizePath(rootRelative))}`))
    return
  }
  if (checkAnchor && decodedAnchor && /\.(?:md|markdown|html?)$/i.test(absolute)) {
    let source
    try {
      source = fs.readFileSync(absolute, 'utf8')
    } catch (error) {
      errors.push(diagnostic(code, documentPath, `${label} cannot read ${JSON.stringify(normalizePath(rootRelative))}: ${error.message}`))
      return
    }
    if (!headingSlugs(source).has(decodedAnchor.toLowerCase())) {
      errors.push(diagnostic('missing-anchor', documentPath, `${label} points to missing anchor ${JSON.stringify(decodedAnchor)} in ${JSON.stringify(normalizePath(rootRelative))}`))
    }
  }
}

function validateDocumentReferences(document, repoRoot, errors) {
  for (const source of Array.isArray(document.metadata.canonicalSource) ? document.metadata.canonicalSource : []) {
    if (isPlainObject(source) && typeof source.path === 'string') {
      checkLocalReference({
        reference: source.path,
        baseDirectory: repoRoot,
        repoRoot,
        documentPath: document.path,
        errors,
        code: 'missing-canonical-source',
        label: 'canonicalSource.path',
      })
    }
  }
  for (const related of Array.isArray(document.metadata.relatedDocuments) ? document.metadata.relatedDocuments : []) {
    if (isPlainObject(related) && typeof related.path === 'string') {
      checkLocalReference({
        reference: related.path,
        baseDirectory: repoRoot,
        repoRoot,
        documentPath: document.path,
        errors,
        code: 'missing-related-link',
        label: 'relatedDocuments.path',
      })
    }
  }

  const body = maskMarkdownCode(document.body)
  for (const match of body.matchAll(MARKDOWN_LINK_RE)) {
    const rawTarget = match[1]
    const target = rawTarget.startsWith('<') && rawTarget.endsWith('>')
      ? rawTarget.slice(1, -1)
      : rawTarget
    if (!target || EXTERNAL_REFERENCE_RE.test(target) || target.startsWith('//')) continue
    checkLocalReference({
      reference: target,
      baseDirectory: path.dirname(path.resolve(repoRoot, document.path)),
      repoRoot,
      documentPath: document.path,
      errors,
      code: 'missing-link',
      label: 'markdown link',
    })
  }
}

function validateReciprocalImplements(documents, errors) {
  const documentsByID = new Map()
  const countsByID = new Map()
  for (const document of documents) {
    const id = document.metadata.id
    if (!isStableDesignID(id)) continue
    countsByID.set(id, (countsByID.get(id) ?? 0) + 1)
    documentsByID.set(id, document)
  }

  for (const document of documents) {
    const sourceID = document.metadata.id
    if (!isStableDesignID(sourceID) || countsByID.get(sourceID) !== 1) continue
    const relatedDocuments = Array.isArray(document.metadata.relatedDocuments)
      ? document.metadata.relatedDocuments
      : []
    for (const related of relatedDocuments) {
      if (!isPlainObject(related) || related.relation !== 'implements' || !isStableDesignID(related.id)) continue
      const target = documentsByID.get(related.id)
      if (!target || countsByID.get(related.id) !== 1) continue
      const targetRelations = Array.isArray(target.metadata.relatedDocuments)
        ? target.metadata.relatedDocuments
        : []
      const reciprocal = targetRelations.some((candidate) => (
        isPlainObject(candidate)
        && candidate.relation === 'implements'
        && candidate.id === sourceID
      ))
      if (!reciprocal) continue
      errors.push(diagnostic(
        'reciprocal-implements',
        document.path,
        `relatedDocuments implements ${JSON.stringify(related.id)} in both directions; remove the inverse implements relation from ${target.path}`,
      ))
    }
  }
}

function reportUnreadableDirectory(directory, repoRoot, errors, reportedFailures, error) {
  const absolute = path.resolve(directory)
  if (reportedFailures.has(absolute)) return
  reportedFailures.add(absolute)
  const relative = relativePath(repoRoot, absolute)
  const reason = error instanceof Error ? error.message : String(error)
  errors.push(diagnostic(
    'unreadable-directory',
    relative,
    `cannot traverse directory ${JSON.stringify(relative)}: ${reason}; check that it exists and is readable`,
  ))
}

function collectMarkdownFiles(directory, repoRoot, files = [], errors = [], reportedFailures = new Set()) {
  let entries
  try {
    entries = fs.readdirSync(directory, { withFileTypes: true }).sort((left, right) => compareStrings(left.name, right.name))
  } catch (error) {
    reportUnreadableDirectory(directory, repoRoot, errors, reportedFailures, error)
    return files
  }
  for (const entry of entries) {
    const absolute = path.join(directory, entry.name)
    const relative = normalizePath(path.relative(repoRoot, absolute))
    if (entry.isDirectory()) {
      if (entry.name === 'templates') continue
      collectMarkdownFiles(absolute, repoRoot, files, errors, reportedFailures)
      continue
    }
    if (!entry.isFile() || path.extname(entry.name).toLowerCase() !== '.md') continue
    if (SUPPORT_DOCUMENTS.has(entry.name)) continue
    files.push(relative)
  }
  return files.sort()
}

function collectSupportDocumentFiles(directory, repoRoot, files = [], errors = [], reportedFailures = new Set()) {
  let entries
  try {
    entries = fs.readdirSync(directory, { withFileTypes: true }).sort((left, right) => compareStrings(left.name, right.name))
  } catch (error) {
    reportUnreadableDirectory(directory, repoRoot, errors, reportedFailures, error)
    return files
  }
  for (const entry of entries) {
    const absolute = path.join(directory, entry.name)
    const relative = normalizePath(path.relative(repoRoot, absolute))
    if (entry.isDirectory()) {
      if (entry.name === 'templates') continue
      collectSupportDocumentFiles(absolute, repoRoot, files, errors, reportedFailures)
      continue
    }
    if (entry.isFile() && SUPPORT_DOCUMENTS.has(entry.name)) files.push(relative)
  }
  return files.sort()
}

function clone(value) {
  return JSON.parse(JSON.stringify(value))
}

export function buildCatalog(documents, root = DESIGN_ROOT) {
  const sorted = [...documents].sort(compareDocuments)
  return {
    schema: DESIGN_SCHEMA_VERSION,
    root: normalizePath(root),
    documents: sorted.map((document) => ({
      path: document.path,
      ...clone(document.metadata),
    })),
  }
}

export function validateDesignDocs({ repoRoot = REPO_ROOT, designRoot = DESIGN_ROOT } = {}) {
  const resolvedRepoRoot = path.resolve(repoRoot)
  const resolvedDesignRoot = path.isAbsolute(designRoot)
    ? path.resolve(designRoot)
    : path.resolve(resolvedRepoRoot, designRoot)
  const displayRoot = relativePath(resolvedRepoRoot, resolvedDesignRoot)
  const errors = []
  const documents = []

  let designRootStat
  try {
    designRootStat = fs.statSync(resolvedDesignRoot)
  } catch (error) {
    if (error?.code === 'ENOENT') {
      errors.push(diagnostic('missing-design-root', displayRoot, `design root does not exist: ${displayRoot}`))
    } else {
      reportUnreadableDirectory(resolvedDesignRoot, resolvedRepoRoot, errors, new Set(), error)
    }
  }
  if (designRootStat && !designRootStat.isDirectory()) {
    errors.push(diagnostic('invalid-design-root', displayRoot, `design root is not a directory: ${displayRoot}`))
  } else if (designRootStat) {
    const reportedTraversalFailures = new Set()
    const files = collectMarkdownFiles(resolvedDesignRoot, resolvedRepoRoot, [], errors, reportedTraversalFailures)
    for (const documentPath of files) {
      const absolute = path.resolve(resolvedRepoRoot, documentPath)
      let source
      try {
        source = fs.readFileSync(absolute, 'utf8')
      } catch (error) {
        errors.push(diagnostic('unreadable-document', documentPath, `cannot read document: ${error.message}`))
        continue
      }
      const parsed = parseJSONFrontMatter(source, documentPath)
      errors.push(...parsed.diagnostics)
      if (!parsed.metadata) continue
      const document = { path: documentPath, metadata: parsed.metadata, body: parsed.body }
      const metadataErrors = validateMetadata(parsed.metadata, documentPath)
      errors.push(...metadataErrors)
      if (parsed.body.trim() === '') errors.push(diagnostic('missing-body', documentPath, 'design document body must not be empty'))
      if (parsed.metadata.kind === 'component') validateComponentHeadings({ path: documentPath, body: parsed.body }, errors)
      validateDocumentReferences(document, resolvedRepoRoot, errors)
      documents.push(document)
    }

    for (const documentPath of collectSupportDocumentFiles(resolvedDesignRoot, resolvedRepoRoot, [], errors, reportedTraversalFailures)) {
      const absolute = path.resolve(resolvedRepoRoot, documentPath)
      let source
      try {
        source = fs.readFileSync(absolute, 'utf8')
      } catch (error) {
        errors.push(diagnostic('unreadable-document', documentPath, `cannot read document: ${error.message}`))
        continue
      }
      validateDocumentReferences({ path: documentPath, metadata: {}, body: source }, resolvedRepoRoot, errors)
    }
  }

  const byID = new Map()
  for (const document of documents) {
    if (!isStableDesignID(document.metadata.id)) continue
    const existing = byID.get(document.metadata.id) ?? []
    existing.push(document)
    byID.set(document.metadata.id, existing)
  }
  for (const [id, duplicates] of byID.entries()) {
    if (duplicates.length < 2) continue
    for (const document of duplicates) {
      errors.push(diagnostic('duplicate-id', document.path, `id ${JSON.stringify(id)} is also used by ${duplicates.filter((other) => other !== document).map((other) => other.path).sort().join(', ')}`))
    }
  }
  for (const document of documents) {
    for (const related of Array.isArray(document.metadata.relatedDocuments) ? document.metadata.relatedDocuments : []) {
      if (!isPlainObject(related) || !isStableDesignID(related.id)) continue
      if (!byID.has(related.id)) errors.push(diagnostic('missing-related-document', document.path, `relatedDocuments.id ${JSON.stringify(related.id)} does not identify a document in ${displayRoot}`))
    }
  }
  validateReciprocalImplements(documents, errors)

  const diagnostics = errors.sort(compareDiagnostics)
  const catalog = buildCatalog(documents, displayRoot)
  return {
    catalog,
    diagnostics,
    documents: [...documents].sort(compareDocuments),
    files: documents.map((document) => document.path).sort(),
  }
}

// Keep the scanner-shaped name familiar to the other dependency-free gates.
export const scan = validateDesignDocs

function usage() {
  return [
    'Usage: node hack/verify-design-docs.mjs [--json|--catalog] [--repo-root PATH] [--design-root PATH]',
    '',
    'Validate docs/design metadata and local references. --json and --catalog emit',
    'the deterministic catalog object with an errors array instead of human text.',
  ].join('\n')
}

function parseArgs(argv) {
  const options = { repoRoot: REPO_ROOT, designRoot: DESIGN_ROOT, json: false }
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index]
    if (argument === '--help' || argument === '-h') {
      console.log(usage())
      process.exit(0)
    }
    if (argument === '--json' || argument === '--catalog') {
      options.json = true
      continue
    }
    if (argument === '--repo-root' || argument === '--design-root') {
      const value = argv[index + 1]
      if (!value || value.startsWith('--')) throw new Error(`${argument} needs a path`)
      options[argument === '--repo-root' ? 'repoRoot' : 'designRoot'] = value
      index += 1
      continue
    }
    throw new Error(`unknown argument ${JSON.stringify(argument)}`)
  }
  return options
}

function main() {
  let options
  try {
    options = parseArgs(process.argv.slice(2))
  } catch (error) {
    console.error(`verify-design-docs: ${error.message}`)
    console.error(usage())
    process.exitCode = 2
    return
  }
  const result = validateDesignDocs(options)
  if (options.json) {
    console.log(JSON.stringify({ ...result.catalog, errors: result.diagnostics }, null, 2))
  } else if (result.diagnostics.length > 0) {
    for (const error of result.diagnostics) {
      console.error(`${error.path}:${error.line}:${error.column} ${error.code}: ${error.message}`)
    }
    console.error(`verify-design-docs: ${result.diagnostics.length} error(s) in ${result.catalog.documents.length} document(s)`)
  } else {
    console.log(`verify-design-docs: ${result.catalog.documents.length} document(s) valid`)
  }
  if (result.diagnostics.length > 0) process.exitCode = 1
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
