#!/usr/bin/env node

/**
 * Static, dependency-free guard for the provider contract (AGENTS.md §5,
 * docs/providers.md, docs/roadmap/provider-contract-remediation.md §0.3).
 *
 * For every `providers/<name>/` that carries a `manifest.yaml` it asserts:
 *
 *   manifest-chart-parity  manifest.yaml spec == the CatalogEntry the chart
 *                          renders. The chart copy is the one that reaches
 *                          production; the manifest is what a reviewer reads.
 *                          Fields whose chart value is a Helm expression, and
 *                          the per-release coordinates (urls, versions, the
 *                          embedded values doc), are skipped.
 *   claims-parity          what manifest.yaml spec.requires DECLARES is exactly
 *                          what the GENERATED APIExport
 *                          (config/kcp/apiexport-<exportName>.yaml) CLAIMS, in
 *                          both directions; metadata.name must be
 *                          spec.export.name. A provider ships exactly two
 *                          declarative objects and `init` applies them
 *                          verbatim, so a claim in only one of the two is a
 *                          tenant asked to accept access the provider never
 *                          gets, or given access nobody was asked about.
 *
 *                          spec.requires is ONE list -- it replaced
 *                          spec.apiExport.permissionClaims and
 *                          spec.dependencies[].composes[], which could disagree
 *                          with each other -- so this is a plain equality on
 *                          group + resource + verbs + scope. A `<resource>/
 *                          <verb>` coordinate carries no verbs in the manifest
 *                          and every verb in the generated claim (the verb IS
 *                          the capability), which is the one asymmetry.
 *   claim-selector         a spec.requires entry on a core-group resource that
 *                          carries credentials -- today `secrets` -- must be
 *                          narrowed by selector.matchLabels. A claim is per
 *                          RESOURCE, not per name, so an unscoped `secrets`
 *                          requirement hands the provider every Secret in every
 *                          workspace that enables it, including the tenant's own
 *                          and other providers'. That is the side-door
 *                          docs/cross-provider-simplification.md X-4 closes,
 *                          and it is invisible in review precisely because the
 *                          entry looks the same either way.
 *   export-copy            deploy/chart/files/apiexport.yaml is byte-identical
 *                          to the generated APIExport. The chart copy is what
 *                          reaches production, and it is an OUTPUT: a
 *                          difference means codegen was not re-run (or the copy
 *                          was hand-edited).
 *   readme-missing         README.md and deploy/chart/README.md both exist.
 *   reserved-verb          no coordinate under spec.export.resources[] --
 *                          neither a verb nor an action -- is named after a
 *                          standard Kubernetes verb (get, list, watch, create,
 *                          update, patch, delete, deletecollection); the hub's
 *                          catalog admission refuses such a CatalogEntry
 *                          outright (ValidateProviderExport).
 *   subresource-name       every declared coordinate -- every
 *                          spec.export.resources[].verbs[].name and every
 *                          spec.export.resources[].actions[].name, paired with
 *                          the resource it hangs off -- makes a name kcp accepts
 *                          as a CUSTOM SUBRESOURCE on the APIExport. Both lists
 *                          are published that way by provider-sdk/apiexportgen,
 *                          and kcp holds spec.resources[].name to
 *                          ^[a-z][-a-z0-9]*[a-z0-9](/[a-z][-a-z0-9]*[a-z0-9])?$
 *                          (no underscores) and refuses `status` and `scale`
 *                          outright, because those belong to the object's own
 *                          shape. An action's version is NOT part of the
 *                          coordinate: `branches` v1 publishes
 *                          `repositories/branches`. ONE bad name makes the WHOLE
 *                          export unappliable, not just its entry, so it is
 *                          caught here rather than at a tenant's Enable.
 *   subresource-access-claim
 *                          an export that publishes ANY coordinate requires
 *                          authorization.k8s.io/subjectaccessreviews with verb
 *                          create. On the shard-forwarded path there is no
 *                          caller bearer: the provider runs a
 *                          SubjectAccessReview for the stamped caller through
 *                          its export virtual workspace, and kcp serves that
 *                          builtin there ONLY for an export that claims it
 *                          (verified against kcp-dev/kcp#4388: without the
 *                          claim every forwarded verb is a 500). Declare it in
 *                          manifest.yaml spec.requires, as edges does.
 *   export-group           every spec.export.resources[].apiVersion names a
 *                          group the generated APIExport actually SERVES
 *                          (spec.resources[].group). The apiVersion is what
 *                          lets a consumer address the coordinate without
 *                          knowing the provider's group, so a typo there sends
 *                          every caller to a group nothing answers on.
 *   requires-group         a spec.requires entry naming a `provider` names a
 *                          group that provider SERVES, cross-checked against
 *                          that provider's own manifest and generated export.
 *                          The usual mistake is naming the APIExport instead:
 *                          `code.providers.railgrid.ai` is the export,
 *                          `code.railgrid.ai` is the group it serves.
 *   requires-verb          a spec.requires `<resource>/<verb>` coordinate names
 *                          a verb or action the OWNING provider declares in its
 *                          own spec.export. Claiming a coordinate nobody
 *                          publishes is a claim kcp resolves to nothing, and it
 *                          is exactly what the old shape could not see: the
 *                          claim lived in one manifest and the declaration in
 *                          another.
 *   adhoc-rest             no `"/api/` route literal in main.go, server/ or
 *                          api/. Tenant traffic belongs on kcp resources or a
 *                          cluster-scoped data-plane path, not a flat REST
 *                          surface the hub cannot authorize.
 *
 * This is a scanner, not a fixer: every violation is reported with a stable
 * provider/check/path tuple. Exceptions live in
 * hack/provider-contract-exceptions.json keyed {provider, check, reason}, the
 * same way hack/ui-conformance-exceptions.json works, and an exception that
 * no longer silences anything is itself a violation so the file cannot rot
 * into a debt baseline.
 *
 * The YAML parser below covers the CatalogEntry subset (block maps and
 * sequences, flow sequences and maps, quoted and plain scalars, folded and
 * literal block scalars, anchors and aliases, comments). It is deliberately
 * small: both sides of every comparison go through it, so its opinions cancel
 * out. Helm rendering is simulated far enough to reach the CatalogEntry the
 * chart embeds -- `define`/`include ... indent`, the `{{`...`}}` literal
 * escape, and dropping control-flow lines.
 */

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(HERE, '..')
const DEFAULT_EXCEPTIONS_PATH = path.join(HERE, 'provider-contract-exceptions.json')

export const CHECKS = Object.freeze({
  MANIFEST_CHART_PARITY: 'manifest-chart-parity',
  CLAIMS_PARITY: 'claims-parity',
  CLAIM_SELECTOR: 'claim-selector',
  EXPORT_COPY: 'export-copy',
  README_MISSING: 'readme-missing',
  ADHOC_REST: 'adhoc-rest',
  RESERVED_VERB: 'reserved-verb',
  SUBRESOURCE_NAME: 'subresource-name',
  SUBRESOURCE_ACCESS_CLAIM: 'subresource-access-claim',
  EXPORT_GROUP: 'export-group',
  REQUIRES_GROUP: 'requires-group',
  REQUIRES_VERB: 'requires-verb',
  STALE_EXCEPTION: 'stale-exception',
})

// Exceptions may silence a real check, never the stale-exception report.
//
// The three cross-reference checks are exceptable because their other side may
// legitimately be out of this checkout (a provider that depends on one shipped
// from the external provider repo); the name and access-claim checks are not,
// because what they catch makes an APIExport unappliable or every forwarded
// verb a 500 and no reason excuses that.
const EXCEPTABLE_CHECKS = new Set([
  CHECKS.MANIFEST_CHART_PARITY,
  CHECKS.CLAIMS_PARITY,
  CHECKS.CLAIM_SELECTOR,
  CHECKS.EXPORT_COPY,
  CHECKS.README_MISSING,
  CHECKS.ADHOC_REST,
  CHECKS.EXPORT_GROUP,
  CHECKS.REQUIRES_GROUP,
  CHECKS.REQUIRES_VERB,
])

const EXCEPTION_KEYS = new Set(['provider', 'check', 'reason'])

// Spec fields the chart is expected to differ on: the urls are the in-cluster
// Service DNS against the manifest's dev loopback, the versions are stamped
// from the release, and valuesDoc is the chart's own README inlined.
export const SKIPPED_SPEC_PATHS = Object.freeze(new Set([
  'version',
  'serving.ui.url',
  'serving.backend.url',
  'serving.selfHosting.chart.version',
  'serving.selfHosting.valuesDoc',
]))

// The value a Helm expression collapses to. Comparison skips it: the chart
// computes it at install time, so there is nothing to hold the manifest to.
const HELM_VALUE = ' helm-expression '

const ESCAPE_OPEN = ''
const ESCAPE_CLOSE = ''

class ContractError extends Error {}

function fail(message) {
  throw new ContractError(message)
}

// ---------------------------------------------------------------------------
// YAML (the CatalogEntry subset)
// ---------------------------------------------------------------------------

function stripYAMLComment(line) {
  let quote = null
  for (let index = 0; index < line.length; index += 1) {
    const character = line[index]
    if (quote) {
      if (quote === '"' && character === '\\') {
        index += 1
        continue
      }
      if (character === quote) quote = null
      continue
    }
    if (character === '"' || character === "'") {
      quote = character
      continue
    }
    if (character === '#' && (index === 0 || /\s/.test(line[index - 1]))) return line.slice(0, index)
  }
  return line
}

function scanYAMLLines(text) {
  return text.split(/\r?\n/).map((raw, index) => {
    const withoutComment = stripYAMLComment(raw).replace(/\s+$/, '')
    const content = withoutComment.trimStart()
    return {
      raw,
      lineNumber: index + 1,
      content,
      indent: withoutComment.length - content.length,
      blank: content === '',
    }
  })
}

/** Index of the colon that ends a `key:` on this line, or -1 when there is none. */
function keyColonIndex(content) {
  let quote = null
  for (let index = 0; index < content.length; index += 1) {
    const character = content[index]
    if (quote) {
      if (quote === '"' && character === '\\') {
        index += 1
        continue
      }
      if (character === quote) quote = null
      continue
    }
    if (character === '"' || character === "'") {
      quote = character
      continue
    }
    if (character === ':' && (index + 1 === content.length || /\s/.test(content[index + 1]))) {
      return content.slice(0, index).trim() ? index : -1
    }
  }
  return -1
}

/** Splits `key: rest`, respecting quotes. Returns null when there is no key. */
function splitKey(content) {
  const colon = keyColonIndex(content)
  if (colon < 0) return null
  return { key: parseScalar(content.slice(0, colon).trim()), rest: content.slice(colon + 1).trim() }
}

function parseDoubleQuoted(text) {
  try {
    return JSON.parse(text)
  } catch {
    return text.slice(1, -1).replace(/\\(.)/g, (_, character) => (character === 'n' ? '\n' : character))
  }
}

function skipFlowSpace(state) {
  while (state.index < state.text.length && /\s/.test(state.text[state.index])) state.index += 1
}

function readFlowValue(state) {
  skipFlowSpace(state)
  const character = state.text[state.index]
  if (character === '[') return readFlowSequence(state)
  if (character === '{') return readFlowMapping(state)
  const start = state.index
  let quote = null
  while (state.index < state.text.length) {
    const current = state.text[state.index]
    if (quote) {
      if (quote === '"' && current === '\\') state.index += 1
      else if (current === quote) quote = null
    } else if (current === '"' || current === "'") quote = current
    else if (current === ',' || current === ']' || current === '}') break
    state.index += 1
  }
  return parseScalar(state.text.slice(start, state.index).trim())
}

function readFlowSequence(state) {
  state.index += 1 // [
  const items = []
  for (;;) {
    skipFlowSpace(state)
    if (state.index >= state.text.length) fail('unterminated flow sequence')
    if (state.text[state.index] === ']') {
      state.index += 1
      return items
    }
    items.push(readFlowValue(state))
    skipFlowSpace(state)
    if (state.text[state.index] === ',') state.index += 1
  }
}

function readFlowMapping(state) {
  state.index += 1 // {
  const map = {}
  for (;;) {
    skipFlowSpace(state)
    if (state.index >= state.text.length) fail('unterminated flow mapping')
    if (state.text[state.index] === '}') {
      state.index += 1
      return map
    }
    const start = state.index
    while (state.index < state.text.length && state.text[state.index] !== ':') state.index += 1
    const key = parseScalar(state.text.slice(start, state.index).trim())
    state.index += 1 // :
    map[key] = readFlowValue(state)
    skipFlowSpace(state)
    if (state.text[state.index] === ',') state.index += 1
  }
}

export function parseScalar(raw) {
  const text = raw.trim()
  if (text === '' || text === '~' || text === 'null') return null
  if (text === 'true') return true
  if (text === 'false') return false
  if (text.startsWith('"')) return parseDoubleQuoted(text)
  if (text.startsWith("'")) return text.slice(1, -1).replaceAll("''", "'")
  if (text.startsWith('[') || text.startsWith('{')) {
    const state = { text, index: 0 }
    const value = readFlowValue(state)
    skipFlowSpace(state)
    if (state.index !== text.length) fail(`trailing content after flow collection ${JSON.stringify(text)}`)
    return value
  }
  if (/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][-+]?[0-9]+)?$/.test(text)) return Number(text)
  return text
}

function foldLines(lines) {
  let folded = ''
  for (const line of lines) {
    if (folded === '') folded = line
    else if (line === '') folded += '\n'
    else if (folded.endsWith('\n')) folded += line
    else folded += ` ${line}`
  }
  return folded
}

class YAMLParser {
  constructor(lines) {
    this.lines = [...lines]
    this.index = 0
    this.anchors = new Map()
    this.lastPlain = false
  }

  significant() {
    while (this.index < this.lines.length && this.lines[this.index].blank) this.index += 1
    return this.index < this.lines.length ? this.lines[this.index] : null
  }

  parseNode(minimumIndent) {
    const line = this.significant()
    if (!line || line.indent < minimumIndent) return null
    if (line.content === '-' || line.content.startsWith('- ')) return this.parseSequence(line.indent)
    return this.parseMapping(line.indent)
  }

  parseMapping(indent) {
    const map = {}
    let lastKey = null
    for (;;) {
      const line = this.significant()
      if (!line || line.indent < indent) break
      if (line.indent > indent) {
        // A plain scalar wrapped onto the next line folds into the value.
        if (lastKey !== null && this.lastPlain && typeof map[lastKey] === 'string' && !line.content.startsWith('- ') && !splitKey(line.content)) {
          map[lastKey] = `${map[lastKey]} ${line.content}`
          this.index += 1
          continue
        }
        fail(`line ${line.lineNumber}: unexpected indent in ${JSON.stringify(line.raw)}`)
      }
      if (line.content === '-' || line.content.startsWith('- ')) break
      const parsed = splitKey(line.content)
      if (!parsed) fail(`line ${line.lineNumber}: expected "key: value", got ${JSON.stringify(line.raw)}`)
      this.index += 1
      lastKey = parsed.key
      map[lastKey] = this.parseValue(parsed.rest, indent)
    }
    return map
  }

  parseSequence(indent) {
    const items = []
    for (;;) {
      const line = this.significant()
      if (!line || line.indent !== indent) break
      if (line.content !== '-' && !line.content.startsWith('- ')) break
      if (line.content === '-') {
        this.index += 1
        items.push(this.parseNode(indent + 1))
        continue
      }
      const rest = line.content.slice(1).trimStart()
      const itemIndent = indent + (line.content.length - rest.length)
      if (splitKey(rest)) {
        // Re-enter as a mapping whose first key sits on the dash line.
        this.lines[this.index] = { ...line, content: rest, indent: itemIndent, raw: ' '.repeat(itemIndent) + rest }
        items.push(this.parseMapping(itemIndent))
        continue
      }
      this.index += 1
      items.push(this.parseValue(rest, itemIndent))
    }
    return items
  }

  parseValue(rest, indent) {
    this.lastPlain = false
    let text = rest
    let anchor = null
    const anchored = text.match(/^&([A-Za-z0-9_.-]+)\s*/)
    if (anchored) {
      anchor = anchored[1]
      text = text.slice(anchored[0].length).trim()
    }
    let value
    if (text === '') {
      value = this.parseNode(indent + 1)
      if (value === null) {
        // A block sequence may sit at the same indent as the key it belongs
        // to (`actions:` then `- id: ...` in the same column).
        const next = this.significant()
        if (next && next.indent === indent && (next.content === '-' || next.content.startsWith('- '))) value = this.parseSequence(indent)
      }
    } else if (/^[|>][-+]?[0-9]*$/.test(text)) {
      value = this.readBlockScalar(indent, text)
    } else if (/^\*[A-Za-z0-9_.-]+$/.test(text)) {
      const alias = text.slice(1)
      if (!this.anchors.has(alias)) fail(`unknown alias *${alias}`)
      value = this.anchors.get(alias)
    } else {
      value = parseScalar(text)
      this.lastPlain = typeof value === 'string' && !/^["'[{]/.test(text)
    }
    if (anchor) this.anchors.set(anchor, value)
    return value
  }

  readBlockScalar(indent, header) {
    const folded = header[0] === '>'
    const chomp = header.includes('-') ? 'strip' : header.includes('+') ? 'keep' : 'clip'
    const collected = []
    let blockIndent = null
    while (this.index < this.lines.length) {
      const line = this.lines[this.index]
      if (line.raw.trim() === '') {
        collected.push('')
        this.index += 1
        continue
      }
      const rawIndent = line.raw.length - line.raw.trimStart().length
      if (rawIndent <= indent) break
      if (blockIndent === null) blockIndent = rawIndent
      collected.push(line.raw.slice(blockIndent).replace(/\s+$/, ''))
      this.index += 1
    }
    while (collected.length && collected[collected.length - 1] === '') collected.pop()
    if (!collected.length) return ''
    const body = folded ? foldLines(collected) : collected.join('\n')
    return chomp === 'strip' ? body : `${body}\n`
  }
}

/**
 * True when `raw` ends inside a quoted scalar, i.e. a quote that OPENED a token
 * is still open at end of line.
 *
 * Only a quote in token position counts: one that follows the start of the
 * line, whitespace, `:`, `,`, `[`, `{` or `-`. An apostrophe inside a plain
 * scalar (`description: don't`) follows a letter, so it opens nothing and a
 * one-line plain scalar is never mistaken for a scalar that continues.
 */
function unterminatedQuote(raw, quote = null) {
  let open = quote
  for (let index = 0; index < raw.length; index += 1) {
    const character = raw[index]
    if (open) {
      if (open === '"' && character === '\\') {
        index += 1
        continue
      }
      if (character === open) open = null
      continue
    }
    if (character !== '"' && character !== "'") continue
    const before = index === 0 ? '' : raw[index - 1]
    if (before === '' || /[\s:,[{-]/.test(before)) open = character
  }
  return open
}

/**
 * Folds a multi-line quoted scalar onto one physical line.
 *
 * YAML lets a quoted scalar run across lines -- the external provider repo
 * writes action descriptions that way -- and every reader below (comment
 * stripping, indent, `key: value`) is line-at-a-time. Folding first means they
 * never see the continuation: a line break inside a quoted scalar folds to a
 * single space, which is what YAML says it means, and the joined line then
 * parses like any other. Lines outside a quoted scalar are untouched.
 */
function joinQuotedScalars(lines) {
  const joined = []
  for (let index = 0; index < lines.length; index += 1) {
    let raw = lines[index]
    let open = unterminatedQuote(raw)
    while (open && index + 1 < lines.length) {
      index += 1
      raw = `${raw} ${lines[index].trim()}`
      open = unterminatedQuote(lines[index].trim(), open)
    }
    joined.push(raw)
  }
  return joined
}

/** Parses the YAML subset into its documents. */
export function parseYAML(text) {
  const documents = []
  let current = []
  for (const line of joinQuotedScalars(text.split(/\r?\n/))) {
    if (/^---\s*$/.test(line)) {
      documents.push(current)
      current = []
      continue
    }
    current.push(line)
  }
  documents.push(current)
  return documents
    .map((lines) => new YAMLParser(scanYAMLLines(lines.join('\n'))).parseNode(0))
    .filter((document) => document !== null)
}

function catalogEntrySpec(text, label) {
  const documents = parseYAML(text)
  const entry = documents.find((document) => document && document.kind === 'CatalogEntry')
  if (!entry) fail(`${label} has no kind: CatalogEntry document`)
  if (!entry.spec || typeof entry.spec !== 'object') fail(`${label} CatalogEntry has no spec`)
  return entry.spec
}

// ---------------------------------------------------------------------------
// Helm (enough of it to reach the embedded CatalogEntry)
// ---------------------------------------------------------------------------

const DIRECTIVE_ONLY = /^\{\{[-#]?[\s\S]*\}\}$/
const INCLUDE_LINE = /^(\s*)\{\{-?\s*include\s+"([^"]+)"\s+\.\s*(?:\|\s*n?indent\s+([0-9]+)\s*)?-?\}\}\s*$/

function isDirectiveOnly(line) {
  const trimmed = line.trim()
  return trimmed.startsWith('{{') && trimmed.endsWith('}}') && DIRECTIVE_ONLY.test(trimmed)
}

function blockOpener(line) {
  return /^\{\{-?\s*(?:if|range|with|define|block)\b/.test(line.trim())
}

function blockCloser(line) {
  return /^\{\{-?\s*end\s*-?\}\}$/.test(line.trim())
}

/**
 * Collapses a Helm expression in value position to HELM_VALUE, after restoring
 * the `{{`...`}}` literal escape (a template that renders a literal `{{...}}`
 * into the CatalogEntry, e.g. an edge-agent placeholder).
 */
function neutralizeHelmExpressions(line) {
  const literals = []
  let text = line.replace(/\{\{-?\s*`([^`]*)`\s*-?\}\}/g, (_, inner) => {
    literals.push(inner)
    return `${ESCAPE_OPEN}${literals.length - 1}${ESCAPE_CLOSE}`
  })
  if (text.includes('{{')) {
    const indent = text.length - text.trimStart().length
    const content = text.trimStart()
    const colon = keyColonIndex(content)
    // A value Helm computes at install time becomes HELM_VALUE and is skipped
    // by the comparison; a line that is nothing but an expression (a helper
    // include, a control-flow remnant) contributes no YAML at all.
    text = colon < 0 ? '' : `${' '.repeat(indent)}${content.slice(0, colon + 1)} ${HELM_VALUE}`
  }
  return text.replace(new RegExp(`${ESCAPE_OPEN}([0-9]+)${ESCAPE_CLOSE}`, 'g'), (_, index) => literals[Number(index)])
}

/** Expands `define`/`include`, drops control flow, and returns plain YAML. */
export function renderChartTemplate(text) {
  const lines = text.split(/\r?\n/)
  const defines = new Map()
  const body = []

  for (let index = 0; index < lines.length; index += 1) {
    const defined = lines[index].trim().match(/^\{\{-?\s*define\s+"([^"]+)"\s*-?\}\}$/)
    if (!defined) {
      body.push(lines[index])
      continue
    }
    const collected = []
    let depth = 1
    index += 1
    for (; index < lines.length; index += 1) {
      if (blockCloser(lines[index])) {
        depth -= 1
        if (depth === 0) break
      } else if (blockOpener(lines[index])) depth += 1
      collected.push(lines[index])
    }
    defines.set(defined[1], collected)
  }

  const expanded = []
  for (const line of body) {
    const include = line.match(INCLUDE_LINE)
    if (include) {
      const template = defines.get(include[2])
      if (!template) continue // a chart helper (labels, fullname): not CatalogEntry content
      const pad = ' '.repeat(Number(include[3] ?? 0))
      for (const inner of template) expanded.push(inner.trim() === '' ? inner : pad + inner)
      continue
    }
    if (isDirectiveOnly(line)) continue
    expanded.push(line)
  }

  return expanded.map(neutralizeHelmExpressions).join('\n')
}

/** Lifts the CatalogEntry out of the ConfigMap the chart embeds it in. */
export function chartCatalogEntryYAML(text) {
  const rendered = renderChartTemplate(text).split('\n')
  const start = rendered.findIndex((line) => /^\s*catalogentry\.yaml:\s*\|\s*$/.test(line))
  if (start < 0) return rendered.join('\n')
  const block = []
  let blockIndent = null
  for (let index = start + 1; index < rendered.length; index += 1) {
    const line = rendered[index]
    if (line.trim() === '') {
      block.push('')
      continue
    }
    const indent = line.length - line.trimStart().length
    if (blockIndent === null) blockIndent = indent
    if (indent < blockIndent) break
    block.push(line.slice(blockIndent))
  }
  return block.join('\n')
}

// ---------------------------------------------------------------------------
// Check 1: manifest / chart parity
// ---------------------------------------------------------------------------

function kindOf(value) {
  if (value === null) return 'null'
  if (Array.isArray(value)) return 'array'
  return typeof value
}

function preview(value) {
  const text = typeof value === 'string' ? value : JSON.stringify(value)
  return text.length > 80 ? `${text.slice(0, 77)}...` : text
}

function compareSpec(fieldPath, manifestValue, chartValue, differences) {
  if (SKIPPED_SPEC_PATHS.has(fieldPath.replace(/\[[0-9]+\]/g, ''))) return
  if (chartValue === HELM_VALUE) return
  const label = fieldPath || '<spec>'
  if (manifestValue === undefined) {
    differences.push(`spec.${label}: only the chart sets it (${preview(chartValue)})`)
    return
  }
  if (chartValue === undefined) {
    differences.push(`spec.${label}: only manifest.yaml sets it (${preview(manifestValue)})`)
    return
  }
  const manifestKind = kindOf(manifestValue)
  const chartKind = kindOf(chartValue)
  if (manifestKind !== chartKind) {
    differences.push(`spec.${label}: manifest.yaml has ${manifestKind}, the chart has ${chartKind}`)
    return
  }
  if (manifestKind === 'array') {
    if (manifestValue.length !== chartValue.length) {
      differences.push(`spec.${label}: manifest.yaml has ${manifestValue.length} item(s), the chart has ${chartValue.length}`)
      return
    }
    for (const [index, item] of manifestValue.entries()) compareSpec(`${fieldPath}[${index}]`, item, chartValue[index], differences)
    return
  }
  if (manifestKind === 'object') {
    for (const key of [...new Set([...Object.keys(manifestValue), ...Object.keys(chartValue)])].sort()) {
      compareSpec(fieldPath ? `${fieldPath}.${key}` : key, manifestValue[key], chartValue[key], differences)
    }
    return
  }
  if (manifestValue !== chartValue) {
    differences.push(`spec.${label}: manifest.yaml ${preview(manifestValue)} != chart ${preview(chartValue)}`)
  }
}

// ---------------------------------------------------------------------------
// Check 2: claims parity between the manifest and the generated APIExport
// ---------------------------------------------------------------------------

function claimKey(claim) {
  return `${claim.group || ''}|${claim.resource}|${[...claim.verbs].sort().join(',')}|${selectorKey(claim.matchLabels)}`
}

/**
 * A claim's scope, rendered stably. It is part of the parity key because the
 * manifest's `selector` and the export's `defaultSelector` are the same
 * decision written twice: a narrowed manifest against a still-blanket export
 * is a provider asking tenants for less than its APIExport declares.
 */
function selectorKey(matchLabels) {
  const entries = Object.entries(matchLabels ?? {}).sort(([a], [b]) => a.localeCompare(b))
  return entries.length ? entries.map(([k, v]) => `${k}=${v}`).join(',') : '*'
}

function describeClaim(claim) {
  const scope = selectorKey(claim.matchLabels)
  return `${claim.resource}${claim.group ? `.${claim.group}` : ''} [${[...claim.verbs].sort().join(' ')}] scoped ${scope}`
}

/**
 * The verb set a generated claim carries for a `<resource>/<verb>` coordinate.
 *
 * A coordinate is claimed whole: the verb IS the capability, and which HTTP
 * method it uses -- which is what kcp maps onto an RBAC verb -- is the serving
 * provider's transport detail. So the manifest entry carries no verbs
 * (ValidateProviderRequiredResource refuses any) and the generated claim spells
 * every verb.
 */
const COORDINATE_VERBS = Object.freeze(['*'])

/**
 * Every claim manifest.yaml spec.requires declares, flattened one per resource
 * entry and normalized into the same shape as a generated kcp claim.
 *
 * spec.requires is the ONE place a provider says what it needs that it does not
 * own: another provider's kinds and verbs, and the platform builtins its own
 * machinery depends on. provider-sdk/apiexportgen emits exactly one permission
 * claim per entry here, with no identityHash, so it resolves per consumer
 * workspace against whichever copy of that provider the workspace bound.
 *
 * `provider` and the old `tenantScoped` are not part of a claim: the first is a
 * dependency edge the hub reads at Enable, and the second is gone -- everything
 * under requires is tenant-scoped by definition.
 */
function requiredClaims(spec) {
  const requirements = Array.isArray(spec?.requires) ? spec.requires : []
  const claims = []
  for (const requirement of requirements) {
    const group = requirement?.group ?? ''
    const resources = Array.isArray(requirement?.resources) ? requirement.resources : []
    for (const resource of resources) {
      const name = String(resource?.name ?? '')
      claims.push({
        group,
        resource: name,
        verbs: name.includes('/') ? [...COORDINATE_VERBS] : Array.isArray(resource?.verbs) ? resource.verbs.map(String) : [],
        matchLabels: claimMatchLabels(resource),
      })
    }
  }
  return claims
}

/**
 * Reads a claim list off the generated APIExport. The scope is spelled
 * `selector` on a manifest requirement but `defaultSelector` on the kcp
 * APIExport claim (kcp's name for the scope an export SUGGESTS; what binds is
 * the selector the hub writes on each tenant's accepted claim). Both spellings
 * normalize to matchLabels, so the comparison is on
 * group + resource + verbs + scope.
 */
function normalizeClaims(raw) {
  if (!Array.isArray(raw)) return []
  return raw.map((claim) => ({
    group: claim?.group ?? '',
    resource: String(claim?.resource ?? ''),
    verbs: Array.isArray(claim?.verbs) ? claim.verbs.map(String) : [],
    matchLabels: claimMatchLabels(claim),
  }))
}

/** matchLabels off either spelling of a claim's scope, or null for unscoped. */
function claimMatchLabels(claim) {
  const selector = claim?.selector ?? claim?.defaultSelector
  const labels = selector?.matchLabels
  if (!labels || typeof labels !== 'object' || Array.isArray(labels)) return null
  const out = {}
  for (const [key, value] of Object.entries(labels)) out[String(key)] = String(value)
  return Object.keys(out).length ? out : null
}

/**
 * SCOPED_CORE_RESOURCES are the core-group resources a claim may not take
 * unscoped. It mirrors provider-sdk/install.ScopedCoreResources, which refuses
 * the same thing at provider init; this copy is what makes it a review-time
 * failure instead of a rollout-time one.
 */
export const SCOPED_CORE_RESOURCES = Object.freeze(new Set(['secrets']))

/**
 * Check 2b: a core-group credential resource must be required with a selector.
 */
function checkClaimSelectors(repoRoot, provider, providerDir, violations) {
  const spec = readManifestSpec(repoRoot, providerDir)
  if (!spec) return // manifest-chart-parity already reports an unreadable manifest
  const manifestPath = path.relative(repoRoot, path.join(providerDir, 'manifest.yaml'))
  for (const claim of requiredClaims(spec)) {
    if (claim.group || !SCOPED_CORE_RESOURCES.has(claim.resource)) continue
    if (claim.matchLabels) continue
    violations.push(violation(provider, CHECKS.CLAIM_SELECTOR, `requires the core resource ${claim.resource} with no selector.matchLabels: a claim is per resource, not per name, so this reaches every ${claim.resource} in every workspace that enables ${provider}. Narrow it to the objects this provider owns, e.g. selector.matchLabels["railgrid.ai/owner"]: ${provider}`, { path: manifestPath }))
  }
}

/** The generated APIExport a provider ships, named after the export. */
export function generatedExportPath(providerDir, exportName) {
  return path.join(providerDir, 'config', 'kcp', `apiexport-${exportName}.yaml`)
}

/** The chart's copy of it — an output, byte-identical by construction. */
export function chartExportPath(providerDir) {
  return path.join(providerDir, 'deploy', 'chart', 'files', 'apiexport.yaml')
}

function readAPIExport(text, label) {
  const documents = parseYAML(text)
  const entry = documents.find((document) => document && document.kind === 'APIExport')
  if (!entry) fail(`${label} has no kind: APIExport document`)
  return entry
}

// ---------------------------------------------------------------------------
// Check 4: no ad-hoc REST
// ---------------------------------------------------------------------------

// Matches a route literal whether it is a bare path ("/api/x"), a net/http
// 1.22 pattern ("GET /api/x") or a gorilla/mux prefix ("/api").
const AD_HOC_REST_ROUTE = /"(?:(?:GET|PUT|HEAD|POST|PATCH|DELETE|OPTIONS)\s+)?(\/api(?:\/[^"\n]*)?)"/g

function goSourceFiles(providerDir) {
  const files = []
  const main = path.join(providerDir, 'main.go')
  if (fs.existsSync(main)) files.push(main)
  for (const directory of ['server', 'api']) {
    const absolute = path.join(providerDir, directory)
    if (!fs.existsSync(absolute) || !fs.statSync(absolute).isDirectory()) continue
    for (const entry of fs.readdirSync(absolute).sort()) {
      if (entry.endsWith('.go') && !entry.endsWith('_test.go')) files.push(path.join(absolute, entry))
    }
  }
  return files
}

/** Blanks out `//` and `/* *​/` comments while keeping every offset stable. */
function maskGoComments(source) {
  let out = ''
  let state = 'code'
  for (let index = 0; index < source.length; index += 1) {
    const character = source[index]
    const next = source[index + 1]
    if (state === 'code') {
      if (character === '/' && next === '/') {
        state = 'line'
        out += '  '
        index += 1
        continue
      }
      if (character === '/' && next === '*') {
        state = 'block'
        out += '  '
        index += 1
        continue
      }
      if (character === '"' || character === '`') state = character
      out += character
      continue
    }
    if (state === 'line') {
      if (character === '\n') {
        state = 'code'
        out += character
      } else out += ' '
      continue
    }
    if (state === 'block') {
      if (character === '*' && next === '/') {
        state = 'code'
        out += '  '
        index += 1
        continue
      }
      out += character === '\n' ? character : ' '
      continue
    }
    if (state === '"' && character === '\\') {
      out += character + (next ?? '')
      index += 1
      continue
    }
    if (character === state) state = 'code'
    out += character
  }
  return out
}

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

function listProviders(repoRoot) {
  const root = path.join(repoRoot, 'providers')
  if (!fs.existsSync(root)) return []
  return fs
    .readdirSync(root)
    .sort()
    .filter((name) => fs.existsSync(path.join(root, name, 'manifest.yaml')))
}

function readExceptions(options) {
  let registry = options.exceptions
  if (!registry) {
    const exceptionPath = options.exceptionsPath
      ? path.resolve(options.repoRoot ?? REPO_ROOT, options.exceptionsPath)
      : DEFAULT_EXCEPTIONS_PATH
    if (!fs.existsSync(exceptionPath)) return []
    try {
      registry = JSON.parse(fs.readFileSync(exceptionPath, 'utf8'))
    } catch (error) {
      fail(`cannot read ${exceptionPath}: ${error.message}`)
    }
  }
  if (Array.isArray(registry)) registry = { version: 1, exceptions: registry }
  if (!registry || typeof registry !== 'object') fail('exception registry must be an object')
  if (registry.version !== 1) fail('exception registry.version must be 1')
  if (!Array.isArray(registry.exceptions)) fail('exception registry.exceptions must be an array')
  return registry.exceptions.map((exception, index) => {
    if (!exception || typeof exception !== 'object' || Array.isArray(exception)) fail(`exception ${index} must be an object`)
    for (const key of Object.keys(exception)) {
      if (!EXCEPTION_KEYS.has(key)) fail(`exception ${index} has unknown key ${JSON.stringify(key)}`)
    }
    for (const key of ['provider', 'check', 'reason']) {
      if (typeof exception[key] !== 'string' || !exception[key].trim()) fail(`exception ${index}.${key} must be a non-empty string`)
    }
    if (!EXCEPTABLE_CHECKS.has(exception.check)) fail(`exception ${index}.check ${JSON.stringify(exception.check)} is not an exceptable check`)
    return { ...exception }
  })
}

function violation(provider, check, message, location) {
  return { provider, check, message, path: location?.path ?? null, line: location?.line ?? null }
}

function checkManifestChartParity(repoRoot, provider, providerDir, violations) {
  const manifestPath = path.join(providerDir, 'manifest.yaml')
  const chartPath = path.join(providerDir, 'deploy/chart/templates/catalogentry.yaml')
  const relativeChart = path.relative(repoRoot, chartPath)
  if (!fs.existsSync(chartPath)) {
    violations.push(violation(provider, CHECKS.MANIFEST_CHART_PARITY, `no chart CatalogEntry template; manifest.yaml has no production counterpart`, { path: relativeChart }))
    return
  }
  let manifestSpec
  let chartSpec
  try {
    manifestSpec = catalogEntrySpec(fs.readFileSync(manifestPath, 'utf8'), path.relative(repoRoot, manifestPath))
  } catch (error) {
    violations.push(violation(provider, CHECKS.MANIFEST_CHART_PARITY, `manifest.yaml is unreadable: ${error.message}`, { path: path.relative(repoRoot, manifestPath) }))
    return
  }
  try {
    chartSpec = catalogEntrySpec(chartCatalogEntryYAML(fs.readFileSync(chartPath, 'utf8')), relativeChart)
  } catch (error) {
    violations.push(violation(provider, CHECKS.MANIFEST_CHART_PARITY, `chart CatalogEntry is unreadable: ${error.message}`, { path: relativeChart }))
    return
  }
  const differences = []
  compareSpec('', manifestSpec, chartSpec, differences)
  for (const difference of differences) {
    violations.push(violation(provider, CHECKS.MANIFEST_CHART_PARITY, difference, { path: relativeChart }))
  }
}

/**
 * The manifest's CatalogEntry spec, or null when the manifest itself is the
 * problem (already reported by the manifest/chart parity check).
 */
function readManifestSpec(repoRoot, providerDir) {
  const manifestPath = path.join(providerDir, 'manifest.yaml')
  try {
    return catalogEntrySpec(fs.readFileSync(manifestPath, 'utf8'), path.relative(repoRoot, manifestPath))
  } catch {
    return null
  }
}

/**
 * Reads the manifest's spec.export, or reports why it cannot. Returns null when
 * the manifest itself is the problem (already reported elsewhere).
 */
function manifestExport(repoRoot, provider, providerDir, violations, check) {
  const spec = readManifestSpec(repoRoot, providerDir)
  if (!spec) return null
  const manifestPath = path.join(providerDir, 'manifest.yaml')
  const name = spec?.export?.name
  if (typeof name !== 'string' || !name) {
    violations.push(violation(provider, check, 'manifest.yaml declares no spec.export.name, so no APIExport can be generated', { path: path.relative(repoRoot, manifestPath) }))
    return null
  }
  return { spec, name }
}

function checkClaimsParity(repoRoot, provider, providerDir, violations) {
  const declared = manifestExport(repoRoot, provider, providerDir, violations, CHECKS.CLAIMS_PARITY)
  if (!declared) return
  const generatedPath = generatedExportPath(providerDir, declared.name)
  const relativeGenerated = path.relative(repoRoot, generatedPath)
  if (!fs.existsSync(generatedPath)) {
    violations.push(violation(provider, CHECKS.CLAIMS_PARITY, `no generated APIExport at ${relativeGenerated}; run make codegen-${provider}-provider`, { path: relativeGenerated }))
    return
  }
  let generated
  try {
    generated = readAPIExport(fs.readFileSync(generatedPath, 'utf8'), relativeGenerated)
  } catch (error) {
    violations.push(violation(provider, CHECKS.CLAIMS_PARITY, `generated APIExport is unreadable: ${error.message}`, { path: relativeGenerated }))
    return
  }
  const generatedName = generated.metadata?.name
  if (generatedName !== declared.name) {
    violations.push(violation(provider, CHECKS.CLAIMS_PARITY, `generated APIExport is named ${JSON.stringify(generatedName ?? null)} but manifest.yaml declares spec.export.name ${JSON.stringify(declared.name)}`, { path: relativeGenerated }))
  }

  // One list on each side, so this is a plain equality on
  // group + resource + verbs + scope. It used to accept either of two manifest
  // sources, because a claim could be written twice and disagree with itself;
  // spec.requires is one list and that whole class of drift is gone with it.
  const requiredByKey = new Map(requiredClaims(declared.spec).map((claim) => [claimKey(claim), claim]))
  const exportByKey = new Map(normalizeClaims(generated.spec?.permissionClaims).map((claim) => [claimKey(claim), claim]))

  for (const [key, claim] of requiredByKey) {
    if (exportByKey.has(key)) continue
    violations.push(violation(provider, CHECKS.CLAIMS_PARITY, `manifest.yaml requires ${describeClaim(claim)} but the generated APIExport does not claim it; run make codegen-${provider}-provider`, { path: relativeGenerated }))
  }
  for (const [key, claim] of exportByKey) {
    if (requiredByKey.has(key)) continue
    violations.push(violation(provider, CHECKS.CLAIMS_PARITY, `the generated APIExport claims ${describeClaim(claim)} but manifest.yaml spec.requires does not declare it`, { path: relativeGenerated }))
  }
}

/**
 * The chart's files/apiexport.yaml is what ships. It is produced by copying the
 * generated file, so anything but a byte-for-byte match means the two have
 * drifted -- and the copy is the one that reaches production.
 */
function checkExportChartCopy(repoRoot, provider, providerDir, violations) {
  const declared = manifestExport(repoRoot, provider, providerDir, violations, CHECKS.EXPORT_COPY)
  if (!declared) return
  const generatedPath = generatedExportPath(providerDir, declared.name)
  if (!fs.existsSync(generatedPath)) return // reported by claims-parity
  const copyPath = chartExportPath(providerDir)
  const relativeCopy = path.relative(repoRoot, copyPath)
  if (!fs.existsSync(copyPath)) {
    violations.push(violation(provider, CHECKS.EXPORT_COPY, `the chart ships no ${path.basename(copyPath)}; run make codegen-${provider}-provider`, { path: relativeCopy }))
    return
  }
  if (!fs.readFileSync(generatedPath).equals(fs.readFileSync(copyPath))) {
    violations.push(violation(provider, CHECKS.EXPORT_COPY, `differs from ${path.relative(repoRoot, generatedPath)}; the chart copy is an output, run make codegen-${provider}-provider`, { path: relativeCopy }))
  }
}

function checkReadmes(repoRoot, provider, providerDir, violations) {
  for (const relative of ['README.md', 'deploy/chart/README.md']) {
    const absolute = path.join(providerDir, relative)
    if (!fs.existsSync(absolute)) {
      violations.push(violation(provider, CHECKS.README_MISSING, `${relative} is missing`, { path: path.relative(repoRoot, absolute) }))
    }
  }
}

// The standard Kubernetes verbs, as apis/providers/v1alpha1/export.go reserves
// them. A declared coordinate with one of these names would read as the
// object's own update or delete in an RBAC rule; the hub refuses the whole
// CatalogEntry (ValidateProviderExport) and the provider never becomes Ready,
// so this belongs in `make verify`, not in a running hub.
const RESERVED_COORDINATE_VERBS = new Set(['get', 'list', 'watch', 'create', 'update', 'patch', 'delete', 'deletecollection'])

function checkReservedVerbs(repoRoot, provider, providerDir, violations) {
  const spec = readManifestSpec(repoRoot, providerDir)
  if (!spec) return // manifest-chart-parity already reports an unreadable manifest
  const manifestPath = path.join(providerDir, 'manifest.yaml')
  for (const { resource, verb, source } of declaredCoordinates(spec)) {
    if (!RESERVED_COORDINATE_VERBS.has(verb.toLowerCase())) continue
    violations.push(violation(provider, CHECKS.RESERVED_VERB, `${source} (${resource || '?'}/${verb}) is named after the standard Kubernetes verb "${verb}"; the hub refuses the CatalogEntry (ValidateProviderExport). Name it after what it does (edit, discard, ...)`, { path: path.relative(repoRoot, manifestPath) }))
  }
}

// The pattern kcp puts on APIExport spec.resources[].name (a kubebuilder
// Pattern on apisv1alpha2.ResourceSchema.Name), and the two names its APIExport
// admission refuses outright. Kept identical to
// provider-sdk/apiexportgen/subresources.go, which is the generator this guards:
// there the bad name fails `make codegen`, here it fails `make verify` even for
// a provider whose parent resource is minted at runtime and never reaches the
// export's resource list at all.
const KCP_SUBRESOURCE_NAME = /^[a-z][-a-z0-9]*[a-z0-9](\/[a-z][-a-z0-9]*[a-z0-9])?$/
const SCHEMA_OWNED_SUBRESOURCES = new Set(['status', 'scale'])

/**
 * Every {resource, verb} coordinate the manifest declares, walking
 * spec.export.resources[] in declaration order -- each resource's verbs, then
 * its actions, exactly as ProviderExport.Coordinates() does. `source` is the
 * spec path a reader can go look at.
 *
 * An action contributes its NAME only: the coordinate is
 * `<resource>/<action name>` and the action's version is nowhere in any path
 * (the serving provider restores it from its own declaration), so `branches`
 * v1 on `repositories` publishes `repositories/branches`.
 */
function declaredCoordinates(spec) {
  const out = []
  const resources = Array.isArray(spec?.export?.resources) ? spec.export.resources : []
  resources.forEach((entry, index) => {
    const resource = String(entry?.name ?? '').trim()
    const verbs = Array.isArray(entry?.verbs) ? entry.verbs : []
    verbs.forEach((verb, verbIndex) => {
      out.push({
        resource,
        verb: String(verb?.name ?? '').trim(),
        source: `spec.export.resources[${index}].verbs[${verbIndex}]`,
      })
    })
    const actions = Array.isArray(entry?.actions) ? entry.actions : []
    actions.forEach((action, actionIndex) => {
      const name = String(action?.name ?? '').trim()
      const version = String(action?.version ?? '').trim()
      out.push({
        resource,
        verb: name,
        action: true,
        source: `spec.export.resources[${index}].actions[${actionIndex}] (${name || '?'}${version ? `/${version}` : ''})`,
      })
    })
  })
  return out
}

function checkSubresourceNames(repoRoot, provider, providerDir, violations) {
  const spec = readManifestSpec(repoRoot, providerDir)
  if (!spec) return // manifest-chart-parity already reports an unreadable manifest
  const manifestPath = path.join(providerDir, 'manifest.yaml')
  const relative = path.relative(repoRoot, manifestPath)
  for (const { resource, verb, source } of declaredCoordinates(spec)) {
    if (!resource || !verb) continue // an incomplete coordinate is the generator's own error
    const name = `${resource}/${verb}`
    if (SCHEMA_OWNED_SUBRESOURCES.has(verb)) {
      violations.push(violation(provider, CHECKS.SUBRESOURCE_NAME, `${source} declares ${JSON.stringify(name)}; "status" and "scale" belong to the object's shape, are declared on the APIResourceSchema, and kcp's APIExport admission refuses them as custom subresources. Rename the verb (e.g. runtime-status)`, { path: relative }))
      continue
    }
    if (!KCP_SUBRESOURCE_NAME.test(name)) {
      violations.push(violation(provider, CHECKS.SUBRESOURCE_NAME, `${source} declares ${JSON.stringify(name)}, which kcp's APIExport admission rejects: spec.resources[].name must match ${KCP_SUBRESOURCE_NAME.source} (lower-case letters, digits and hyphens — no underscores). One bad name makes the whole export unappliable`, { path: relative }))
    }
  }
}

/** The builtin review API a proxied gate needs through the export VW. */
const SUBRESOURCE_ACCESS_CLAIM = { group: 'authorization.k8s.io', resource: 'subjectaccessreviews', verb: 'create' }

/**
 * An export that publishes ANY coordinate must require the review API.
 *
 * It reads the declaration, not the generated export: spec.export is where the
 * coordinates now live and spec.requires is where the claim now lives, so the
 * whole check is answerable from manifest.yaml and holds even before codegen
 * has run.
 */
function checkSubresourceAccessClaim(repoRoot, provider, providerDir, violations) {
  const spec = readManifestSpec(repoRoot, providerDir)
  if (!spec) return // manifest-chart-parity already reports an unreadable manifest
  const published = declaredCoordinates(spec)
    .filter(({ resource, verb }) => resource && verb)
    .map(({ resource, verb }) => `${resource}/${verb}`)
  if (published.length === 0) return
  const required = requiredClaims(spec).some((claim) =>
    claim.group === SUBRESOURCE_ACCESS_CLAIM.group && claim.resource === SUBRESOURCE_ACCESS_CLAIM.resource && claim.verbs.includes(SUBRESOURCE_ACCESS_CLAIM.verb))
  if (required) return
  const shown = published.slice(0, 3).map((name) => JSON.stringify(name)).join(', ') + (published.length > 3 ? `, … (${published.length})` : '')
  violations.push(violation(provider, CHECKS.SUBRESOURCE_ACCESS_CLAIM, `publishes coordinate(s) ${shown} but spec.requires has no ${SUBRESOURCE_ACCESS_CLAIM.group} ${SUBRESOURCE_ACCESS_CLAIM.resource} entry with verb create; the proxied gate runs its SubjectAccessReview through the export virtual workspace, which kcp serves only for an export that claims it. Add it to manifest.yaml spec.requires and re-run make codegen-${provider}-provider`, { path: path.relative(repoRoot, path.join(providerDir, 'manifest.yaml')) }))
}

/**
 * The api groups a provider SERVES, for the cross-reference checks below.
 *
 * Two sources, because neither alone is complete: the generated APIExport's
 * spec.resources[].group is authoritative (it is the same projection
 * pkg/hub/providers.APIExportGroups makes off the live object) but is absent for
 * a provider that mints its schemas at runtime, and the manifest's
 * spec.export.resources[].apiVersion only lists the resources that hang a verb
 * or an action off themselves. The union is what a requirement is checked
 * against, so this errs towards accepting rather than towards a false report.
 */
function servedGroups(providerDir, spec) {
  const groups = new Set()
  const resources = Array.isArray(spec?.export?.resources) ? spec.export.resources : []
  for (const resource of resources) {
    const group = String(resource?.apiVersion ?? '').split('/')[0]
    if (group && String(resource?.apiVersion ?? '').includes('/')) groups.add(group)
  }
  const exportName = spec?.export?.name
  if (typeof exportName === 'string' && exportName) {
    const generatedPath = generatedExportPath(providerDir, exportName)
    if (fs.existsSync(generatedPath)) {
      try {
        const generated = readAPIExport(fs.readFileSync(generatedPath, 'utf8'), generatedPath)
        for (const resource of Array.isArray(generated.spec?.resources) ? generated.spec.resources : []) {
          const group = String(resource?.group ?? '')
          if (group) groups.add(group)
        }
      } catch {
        // an unreadable generated export is reported by claims-parity
      }
    }
  }
  return groups
}

/**
 * What every provider in the tree serves and publishes, keyed by CatalogEntry
 * name (which is the directory name, and what a requirement's `provider` field
 * holds). Built once per scan: a requirement is checked against its OWNER's
 * declaration, so every check below needs every provider's.
 */
function buildRegistry(repoRoot, providers) {
  const registry = new Map()
  for (const provider of providers) {
    const providerDir = path.join(repoRoot, 'providers', provider)
    const spec = readManifestSpec(repoRoot, providerDir)
    if (!spec) continue
    registry.set(provider, {
      spec,
      groups: servedGroups(providerDir, spec),
      coordinates: new Set(declaredCoordinates(spec).filter(({ resource, verb }) => resource && verb).map(({ resource, verb }) => `${resource}/${verb}`)),
    })
  }
  return registry
}

function shownSet(values) {
  const sorted = [...values].sort()
  return sorted.length ? sorted.join(', ') : 'none'
}

/**
 * Check: every spec.export.resources[].apiVersion names a group the generated
 * APIExport actually serves.
 *
 * A provider whose export serves nothing yet (`resources: []`, what a provider
 * that mints its schemas at runtime ships) is skipped: there is no served-group
 * list to hold the apiVersion to, and inventing one would report every entry.
 */
function checkExportGroups(repoRoot, provider, providerDir, violations) {
  const spec = readManifestSpec(repoRoot, providerDir)
  if (!spec) return
  const relative = path.relative(repoRoot, path.join(providerDir, 'manifest.yaml'))
  const resources = Array.isArray(spec?.export?.resources) ? spec.export.resources : []
  if (!resources.length) return
  const exportName = spec?.export?.name
  if (typeof exportName !== 'string' || !exportName) return // reported by claims-parity
  const generatedPath = generatedExportPath(providerDir, exportName)
  if (!fs.existsSync(generatedPath)) return // reported by claims-parity
  let generated
  try {
    generated = readAPIExport(fs.readFileSync(generatedPath, 'utf8'), generatedPath)
  } catch {
    return // reported by claims-parity
  }
  const served = new Set(
    (Array.isArray(generated.spec?.resources) ? generated.spec.resources : [])
      .map((resource) => String(resource?.group ?? ''))
      .filter(Boolean),
  )
  if (!served.size) return
  resources.forEach((resource, index) => {
    const apiVersion = String(resource?.apiVersion ?? '').trim()
    const name = String(resource?.name ?? '?')
    if (!apiVersion.includes('/')) {
      violations.push(violation(provider, CHECKS.EXPORT_GROUP, `spec.export.resources[${index}] (${name}) declares apiVersion ${JSON.stringify(apiVersion)}, which is not "group/version"; a provider exports no core kinds, so the group is never empty`, { path: relative }))
      return
    }
    const group = apiVersion.split('/')[0]
    if (served.has(group)) return
    violations.push(violation(provider, CHECKS.EXPORT_GROUP, `spec.export.resources[${index}] (${name}) declares apiVersion ${JSON.stringify(apiVersion)} but the generated APIExport serves no ${group}; it serves ${shownSet(served)}. The apiVersion is how a consumer addresses the coordinate without knowing this provider's group`, { path: relative }))
  })
}

/**
 * Check: a spec.requires entry naming a provider names a group that provider
 * serves, and every `<resource>/<verb>` coordinate it claims is one that
 * provider declares in its own spec.export.
 *
 * An entry with no `provider` is a platform builtin (authorization.k8s.io, the
 * core group) which no provider serves and nothing cross-checks.
 */
function checkRequirements(repoRoot, provider, providerDir, violations, registry) {
  const spec = readManifestSpec(repoRoot, providerDir)
  if (!spec) return
  const relative = path.relative(repoRoot, path.join(providerDir, 'manifest.yaml'))
  const requirements = Array.isArray(spec?.requires) ? spec.requires : []
  requirements.forEach((requirement, index) => {
    const owner = String(requirement?.provider ?? '').trim()
    if (!owner) return
    const group = String(requirement?.group ?? '').trim()
    const declared = registry.get(owner)
    if (!declared) {
      violations.push(violation(provider, CHECKS.REQUIRES_GROUP, `spec.requires[${index}] names provider ${JSON.stringify(owner)}, which is no provider in this tree (there is no providers/${owner}/manifest.yaml). A requirement's provider is a CatalogEntry metadata.name and it is also the dependency the hub refuses to enable without`, { path: relative }))
      return
    }
    if (!group) {
      violations.push(violation(provider, CHECKS.REQUIRES_GROUP, `spec.requires[${index}] names provider ${owner} but no group; a requirement on a provider must name the API group that provider serves, not the core group`, { path: relative }))
      return
    }
    if (declared.groups.size && !declared.groups.has(group)) {
      violations.push(violation(provider, CHECKS.REQUIRES_GROUP, `spec.requires[${index}] asks provider ${owner} for group ${group}, which ${owner} does not serve; it serves ${shownSet(declared.groups)}. The group is what the provider SERVES, not the name of its APIExport`, { path: relative }))
      return
    }
    const resources = Array.isArray(requirement?.resources) ? requirement.resources : []
    resources.forEach((resource, resourceIndex) => {
      const name = String(resource?.name ?? '').trim()
      if (!name.includes('/')) return
      if (declared.coordinates.has(name)) return
      violations.push(violation(provider, CHECKS.REQUIRES_VERB, `spec.requires[${index}].resources[${resourceIndex}] claims the coordinate ${name} in ${group}, but ${owner} declares no such verb or action on it (spec.export publishes ${shownSet(declared.coordinates)}). A claim on a coordinate nobody publishes resolves to nothing`, { path: relative }))
    })
  })
}

function checkAdHocREST(repoRoot, provider, providerDir, violations) {
  for (const file of goSourceFiles(providerDir)) {
    const source = fs.readFileSync(file, 'utf8')
    const masked = maskGoComments(source)
    const relative = path.relative(repoRoot, file)
    AD_HOC_REST_ROUTE.lastIndex = 0
    for (const match of masked.matchAll(AD_HOC_REST_ROUTE)) {
      const line = masked.slice(0, match.index).split('\n').length
      violations.push(violation(provider, CHECKS.ADHOC_REST, `ad-hoc REST route ${JSON.stringify(match[1])}; tenant traffic belongs on a kcp resource or a cluster-scoped data-plane path`, { path: relative, line }))
    }
  }
}

/** Runs every check. Returns {violations, excused, providers}. */
export function verify(options = {}) {
  const repoRoot = path.resolve(options.repoRoot ?? REPO_ROOT)
  const exceptions = readExceptions({ ...options, repoRoot })
  const providers = options.providers ?? listProviders(repoRoot)
  // Every provider in the tree, not just the ones being reported on: a
  // requirement is checked against its OWNER's declaration.
  const registry = buildRegistry(repoRoot, listProviders(repoRoot))
  const raw = []
  for (const provider of providers) {
    const providerDir = path.join(repoRoot, 'providers', provider)
    checkManifestChartParity(repoRoot, provider, providerDir, raw)
    checkClaimsParity(repoRoot, provider, providerDir, raw)
    checkClaimSelectors(repoRoot, provider, providerDir, raw)
    checkExportChartCopy(repoRoot, provider, providerDir, raw)
    checkReadmes(repoRoot, provider, providerDir, raw)
    checkAdHocREST(repoRoot, provider, providerDir, raw)
    checkReservedVerbs(repoRoot, provider, providerDir, raw)
    checkSubresourceNames(repoRoot, provider, providerDir, raw)
    checkSubresourceAccessClaim(repoRoot, provider, providerDir, raw)
    checkExportGroups(repoRoot, provider, providerDir, raw)
    checkRequirements(repoRoot, provider, providerDir, raw, registry)
  }
  raw.sort((a, b) => a.provider.localeCompare(b.provider) || a.check.localeCompare(b.check) || (a.path ?? '').localeCompare(b.path ?? '') || (a.line ?? 0) - (b.line ?? 0) || a.message.localeCompare(b.message))

  const used = new Set()
  const violations = []
  const excused = []
  for (const item of raw) {
    const index = exceptions.findIndex((exception) => exception.provider === item.provider && exception.check === item.check)
    if (index < 0) violations.push(item)
    else {
      used.add(index)
      excused.push({ ...item, reason: exceptions[index].reason })
    }
  }
  for (const [index, exception] of exceptions.entries()) {
    if (used.has(index)) continue
    violations.push(violation(exception.provider, CHECKS.STALE_EXCEPTION, `exception for ${exception.provider}/${exception.check} no longer silences anything; delete it (${exception.reason})`, { path: path.relative(repoRoot, DEFAULT_EXCEPTIONS_PATH) }))
  }

  const counts = {}
  for (const item of violations) counts[item.check] = (counts[item.check] ?? 0) + 1
  return { violations, excused, providers, counts }
}

export function formatViolation(item) {
  const location = item.path ? `${item.path}${item.line ? `:${item.line}` : ''}` : `providers/${item.provider}`
  return `${location} [${item.check}] ${item.provider}: ${item.message}`
}

function parseArgs(argv) {
  const options = {}
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]
    if (arg === '--json') options.json = true
    else if (arg === '--exceptions') options.exceptionsPath = argv[++index]
    else if (arg === '--repo-root') options.repoRoot = argv[++index]
    else if (arg === '--help' || arg === '-h') options.help = true
    else fail(`unknown argument ${arg}`)
  }
  return options
}

function usage() {
  return [
    'Usage: node hack/verify-provider-contract.mjs [options]',
    '',
    'Checks every providers/*/ with a manifest.yaml against the provider contract:',
    '  manifest-chart-parity  manifest.yaml spec == the chart CatalogEntry',
    '  claims-parity          spec.requires == the generated APIExport\'s permissionClaims',
    '  claim-selector         a core-group secrets requirement is narrowed by selector.matchLabels',
    '  export-copy            deploy/chart/files/apiexport.yaml == the generated APIExport',
    '  readme-missing         README.md and deploy/chart/README.md exist',
    '  adhoc-rest             no "/api/ route literal in main.go, server/, api/',
    '  reserved-verb          no export coordinate named after a standard Kubernetes verb',
    '  subresource-name       every <resource>/<verb> coordinate is a name kcp accepts',
    '  subresource-access-claim  an export publishing a coordinate requires subjectaccessreviews',
    '  export-group           every export resource apiVersion names a group the export serves',
    '  requires-group         a requirement on a provider names a group that provider serves',
    '  requires-verb          a required <resource>/<verb> is one its owner declares',
    '',
    'Options:',
    '  --exceptions PATH  JSON registry (default hack/provider-contract-exceptions.json)',
    '  --repo-root PATH   Scan another checkout',
    '  --json             Machine-readable output',
  ].join('\n')
}

function main() {
  let options
  try {
    options = parseArgs(process.argv.slice(2))
    if (options.help) {
      console.log(usage())
      return
    }
    const result = verify(options)
    if (options.json) {
      console.log(JSON.stringify(result, null, 2))
    } else {
      console.log(`Provider contract scanned ${result.providers.length} provider(s): ${result.violations.length} violation(s), ${result.excused.length} excused.`)
      for (const item of result.violations) console.log(formatViolation(item))
      const counts = Object.entries(result.counts).sort(([a], [b]) => a.localeCompare(b)).map(([check, count]) => `${check}=${count}`).join(' ')
      console.log(`Provider contract counts: ${counts || 'none'}`)
    }
    if (result.violations.length) process.exitCode = 1
  } catch (error) {
    console.error(`Provider contract configuration error: ${error.message}`)
    process.exitCode = 2
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))) main()
