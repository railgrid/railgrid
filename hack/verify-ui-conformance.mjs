#!/usr/bin/env node

/**
 * Static, dependency-free guard for the Violet Circuit frontend contract.
 *
 * This intentionally is a scanner rather than a formatter.  It reports every
 * violation with a stable path/line/column/rule tuple so a migration can be
 * reviewed and repaired without a silent debt baseline.
 */

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(HERE, '..')
const DEFAULT_CONFIG_PATH = path.join(HERE, 'ui-conformance.config.json')

export const RULES = Object.freeze({
  LEGACY_PK: 'legacy-pk',
  PROVIDER_K_SELECTOR: 'provider-k-selector',
  NATIVE_DIALOG: 'native-dialog',
  FORBIDDEN_GLYPH: 'forbidden-glyph',
  UNKNOWN_COLOR_TOKEN: 'unknown-color-token',
  RAW_COLOR: 'raw-color',
  PILL_RADIUS: 'pill-radius',
  COMMON_WIDGET_SELECTOR: 'common-widget-selector',
  EXCEPTION_REGISTRY: 'exception-registry',
})

const SOURCE_EXTENSIONS = new Set([
  '.css',
  '.less',
  '.mjs',
  '.js',
  '.jsx',
  '.sass',
  '.scss',
  '.ts',
  '.tsx',
  '.vue',
  '.html',
])

const DEFAULT_CANONICAL_ROOTS = ['provider-sdk/portalkit', 'provider-sdk/portalkit-vue']
const DEFAULT_PROVIDER_ROOTS = ['providers/*/portal/src']
const DEFAULT_VENDORED_SEGMENTS = ['portalkit', 'portalkit-vue']
const DEFAULT_CANONICAL_CONSUMER_PATHS = []
const DEFAULT_TOKEN_AUTHORITY_PATHS = []

// This is the token vocabulary documented in docs/design-book.md §2 and
// declared by portal/src/assets/main.css.  Keep this list explicit: deriving
// it from a stylesheet would let a typo become a token merely by declaring it.
export const COLOR_TOKENS = Object.freeze(new Set([
  'surface',
  'surface-raised',
  'surface-overlay',
  'surface-hover',
  'surface-base',
  'border-subtle',
  'border-default',
  'accent',
  'accent-hover',
  'accent-subtle',
  'accent-glow',
  'text-primary',
  'text-secondary',
  'text-muted',
  'text-error',
  'success',
  'success-subtle',
  'success-border',
  'warning',
  'warning-subtle',
  'danger',
  'danger-subtle',
  'danger-hover',
  'danger-surface',
  'on-accent',
]))

const DARK_FALLBACKS = Object.freeze({
  surface: ['#0a0b12'],
  'surface-raised': ['#111320'],
  'surface-overlay': ['#171927'],
  'surface-hover': ['#1e2033'],
  'surface-base': ['#0a0b12'],
  'border-subtle': ['rgba(255,255,255,.07)'],
  'border-default': ['rgba(255,255,255,.11)'],
  accent: ['#8b6bff'],
  'accent-hover': ['#a18aff'],
  'accent-subtle': ['rgba(139,107,255,.14)'],
  'accent-glow': ['rgba(139,107,255,.3)'],
  'text-primary': ['#e9e9f2'],
  'text-secondary': ['#8a8ca6'],
  // Standalone provider styles retain the original fallback when host tokens
  // are unavailable. Accept both documented generations while those bundles
  // migrate independently; ordinary raw uses of either color remain invalid.
  'text-muted': ['#8587a1', '#5d5f78'],
  'text-error': ['#ff5d5d'],
  success: ['#2fd6a0'],
  'success-subtle': ['rgba(47,214,160,.12)'],
  'success-border': ['rgba(47,214,160,.3)'],
  warning: ['#f0a63a'],
  'warning-subtle': ['rgba(240,166,58,.12)'],
  danger: ['#ff5d5d'],
  'danger-subtle': ['rgba(255,93,93,.12)'],
  'danger-hover': ['#ff7676'],
  'danger-surface': ['rgba(255,93,93,.12)'],
  // Violet Circuit uses near-black on the brighter dark-theme violet and
  // white on the darker light-theme violet. Both are semantic on-accent
  // fallbacks; accepting them here does not permit either as an ad-hoc color.
  'on-accent': ['#0a0b12', '#fff', '#ffffff'],
})

// Keep this list deliberately small.  A provider's `header`, `form`, `field`,
// or `list` is usually page composition, not a reimplementation of a shared
// widget.  The selector rule is for names that are themselves design-system
// primitives; `hasCanonicalWidgetStyling` below then requires a visibly
// widget-like declaration block before reporting one.
const COMMON_WIDGET_NAMES = new Set([
  'badge',
  'btn',
  'button',
  'card',
  'dialog',
  'dropdown',
  'icon',
  'input',
  'menu',
  'modal',
  'panel',
  'skeleton',
  'spinner',
  'table',
  'tab',
  'tabs',
  'toast',
])

const RAW_NAMED_COLORS = new Set([
  'black',
  'blue',
  'gray',
  'green',
  'grey',
  'orange',
  'purple',
  'red',
  'violet',
  'white',
  'yellow',
])

const RAW_COLOR_UTILITY_RE = /\b(?:bg|border|decoration|divide|fill|from|ring|stroke|text|to|via)-(?:black|blue|gray|green|grey|orange|purple|red|violet|white|yellow)(?=$|[-/:\s"'`])/g

// Arrows and other symbols occur in explanatory prose (for example `A → B`)
// and in keyboard hints.  They are only violations when the source gives us
// evidence that the character is rendered as an icon: an icon-only element,
// an edge affordance in a button/link, an icon-ish class/ARIA context, or a
// CSS/DOM content assignment.  Emoji follow the same context rule; ordinary
// user-facing prose is not silently reclassified as an icon.
const FORBIDDEN_GLYPH_RE = /[\u{2190}-\u{21ff}\u{2300}-\u{23ff}\u{2600}-\u{27ff}\u{1f000}-\u{1faff}]/gu
const ICON_CONTEXT_RE = /(?:\b(?:class|className|data-(?:icon|glyph|symbol)|role)\s*=\s*["'`][^"'`]*(?:icon|glyph|symbol|chevron|arrow|external|close|menu|spinner|avatar|brand|logo|img)[^"'`]*["'`]|\baria-hidden\s*=\s*["'`]true["'`])/i
const CONTENT_ASSIGNMENT_RE = /(?:\bcontent\s*:|\b(?:textContent|innerHTML|createTextNode|insertAdjacentHTML)\s*[=(]|\bic\s*\()/i
const MARKUP_TAG_RE = /<\s*(\/?)\s*([A-Za-z][A-Za-z0-9:._-]*)([^<>]*?)>/g
const VOID_TAG_NAMES = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'param', 'source', 'track', 'wbr'])

const EXCEPTION_KEYS = new Set(['rule', 'path', 'line', 'column', 'match', 'reference', 'reason'])
const EXCEPTION_RULES = new Set(Object.values(RULES).filter((rule) => rule !== RULES.EXCEPTION_REGISTRY))

const DEFAULT_CONFIG = Object.freeze({
  version: 1,
  canonicalRoots: DEFAULT_CANONICAL_ROOTS,
  providerRoots: DEFAULT_PROVIDER_ROOTS,
  vendoredSegments: DEFAULT_VENDORED_SEGMENTS,
  canonicalConsumerPaths: DEFAULT_CANONICAL_CONSUMER_PATHS,
  tokenAuthorityPaths: DEFAULT_TOKEN_AUTHORITY_PATHS,
  includeTests: false,
  exceptions: 'hack/ui-conformance-exceptions.json',
})

function compareStrings(left, right) {
  return left === right ? 0 : (left < right ? -1 : 1)
}

function cloneDefaultConfig() {
  return {
    ...DEFAULT_CONFIG,
    canonicalRoots: [...DEFAULT_CONFIG.canonicalRoots],
    providerRoots: [...DEFAULT_CONFIG.providerRoots],
    vendoredSegments: [...DEFAULT_CONFIG.vendoredSegments],
    canonicalConsumerPaths: [...DEFAULT_CONFIG.canonicalConsumerPaths],
    tokenAuthorityPaths: [...DEFAULT_CONFIG.tokenAuthorityPaths],
  }
}

function fail(message) {
  const error = new Error(message)
  error.code = 'UI_CONFORMANCE_CONFIG'
  throw error
}

function assertPlainObject(value, label) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail(`${label} must be an object`)
}

function assertStringArray(value, label) {
  if (!Array.isArray(value) || value.some((entry) => typeof entry !== 'string' || !entry.trim())) {
    fail(`${label} must be a string array`)
  }
}

function validateConfig(raw) {
  assertPlainObject(raw, 'configuration')
  const allowed = new Set([
    'version',
    'canonicalRoots',
    'providerRoots',
    'vendoredSegments',
    'canonicalConsumerPaths',
    'tokenAuthorityPaths',
    'includeTests',
    'exceptions',
  ])
  for (const key of Object.keys(raw)) {
    if (!allowed.has(key)) fail(`configuration has unknown key ${JSON.stringify(key)}`)
  }
  if (raw.version !== 1) fail('configuration.version must be 1')
  assertStringArray(raw.canonicalRoots, 'configuration.canonicalRoots')
  assertStringArray(raw.providerRoots, 'configuration.providerRoots')
  assertStringArray(raw.vendoredSegments, 'configuration.vendoredSegments')
  assertStringArray(raw.canonicalConsumerPaths, 'configuration.canonicalConsumerPaths')
  assertStringArray(raw.tokenAuthorityPaths, 'configuration.tokenAuthorityPaths')
  if (typeof raw.includeTests !== 'boolean') fail('configuration.includeTests must be boolean')
  if (!(typeof raw.exceptions === 'string' || Array.isArray(raw.exceptions) || (raw.exceptions && typeof raw.exceptions === 'object'))) {
    fail('configuration.exceptions must be a registry path, array, or registry object')
  }
  return raw
}

export function loadConfig(repoRoot = REPO_ROOT, configPath = DEFAULT_CONFIG_PATH) {
  const absolutePath = path.isAbsolute(configPath) ? configPath : path.resolve(repoRoot, configPath)
  let parsed
  try {
    parsed = JSON.parse(fs.readFileSync(absolutePath, 'utf8'))
  } catch (error) {
    fail(`cannot read ${path.relative(repoRoot, absolutePath) || absolutePath}: ${error.message}`)
  }
  return validateConfig(parsed)
}

function normalizeRelativePath(value) {
  return value.replaceAll('\\', '/').replace(/^\.\//, '')
}

function validateExceptionRegistry(registry, filesByPath, repoRoot) {
  if (Array.isArray(registry)) registry = { version: 1, exceptions: registry }
  assertPlainObject(registry, 'exception registry')
  const keys = Object.keys(registry)
  if (keys.some((key) => !['version', 'exceptions'].includes(key))) {
    fail(`exception registry has unknown key ${JSON.stringify(keys.find((key) => !['version', 'exceptions'].includes(key)))}`)
  }
  if (registry.version !== 1) fail('exception registry.version must be 1')
  if (!Array.isArray(registry.exceptions)) fail('exception registry.exceptions must be an array')

  const validated = []
  for (const [index, exception] of registry.exceptions.entries()) {
    assertPlainObject(exception, `exception registry.exceptions[${index}]`)
    for (const key of Object.keys(exception)) {
      if (!EXCEPTION_KEYS.has(key)) fail(`exception ${index} has unknown key ${JSON.stringify(key)}`)
    }
    for (const key of ['rule', 'path', 'match', 'reference', 'reason']) {
      if (typeof exception[key] !== 'string' || !exception[key].trim()) fail(`exception ${index}.${key} must be a non-empty string`)
    }
    if (!EXCEPTION_RULES.has(exception.rule)) fail(`exception ${index}.rule ${JSON.stringify(exception.rule)} is not an allowed rule`)
    if (!/^design-book\s+§\s*[0-9]+(?:\.[0-9]+)?(?:\b.*)?$/i.test(exception.reference)) {
      fail(`exception ${index}.reference must name a design-book section`)
    }
    if (/\b(?:debt|temporary|todo|legacy|baseline)\b/i.test(`${exception.reference} ${exception.reason}`)) {
      fail(`exception ${index} cannot describe debt, a baseline, or a temporary waiver`)
    }
    if (!Number.isInteger(exception.line) || exception.line < 1) fail(`exception ${index}.line must be a positive integer`)
    if (!Number.isInteger(exception.column) || exception.column < 1) fail(`exception ${index}.column must be a positive integer`)
    if (exception.match.includes('\n') || exception.match.includes('\r')) fail(`exception ${index}.match cannot contain a newline`)
    const normalizedPath = normalizeRelativePath(exception.path)
    const pathSegments = normalizedPath.split('/')
    if (path.isAbsolute(normalizedPath) || pathSegments.some((segment) => !segment || segment === '.' || segment === '..')) fail(`exception ${index}.path must be repository-relative`)
    const source = filesByPath.get(normalizedPath)
    if (!source) fail(`exception ${index} points to an unscanned path ${normalizedPath}`)
    const sourceLine = source.content.split(/\r?\n/)[exception.line - 1]
    if (sourceLine === undefined) fail(`exception ${index} points past the end of ${normalizedPath}`)
    const matchColumn = sourceLine.indexOf(exception.match)
    if (matchColumn < 0 || matchColumn + 1 !== exception.column) {
      fail(`exception ${index} locator does not match ${normalizedPath}:${exception.line}:${exception.column}`)
    }
    validated.push({ ...exception, path: normalizedPath })
  }
  return validated
}

function readExceptions(config, repoRoot, filesByPath) {
  if (Array.isArray(config.exceptions) || (config.exceptions && typeof config.exceptions === 'object')) {
    return validateExceptionRegistry(config.exceptions, filesByPath, repoRoot)
  }
  const exceptionPath = path.isAbsolute(config.exceptions) ? config.exceptions : path.resolve(repoRoot, config.exceptions)
  let registry
  try {
    registry = JSON.parse(fs.readFileSync(exceptionPath, 'utf8'))
  } catch (error) {
    fail(`cannot read ${path.relative(repoRoot, exceptionPath) || exceptionPath}: ${error.message}`)
  }
  return validateExceptionRegistry(registry, filesByPath, repoRoot)
}

function splitOverride(value) {
  return value.split(',').map((part) => part.trim()).filter(Boolean)
}

function resolveSpec(repoRoot, spec) {
  return path.isAbsolute(spec) ? path.normalize(spec) : path.resolve(repoRoot, spec)
}

function expandRootSpec(repoRoot, spec) {
  const normalized = normalizeRelativePath(spec)
  if (!normalized.includes('*')) return [resolveSpec(repoRoot, normalized)]

  const segments = normalized.split('/')
  const matches = []
  function visit(parent, index) {
    if (index === segments.length) {
      try {
        const stat = fs.lstatSync(parent)
        if (stat.isDirectory() && !stat.isSymbolicLink()) matches.push(parent)
      } catch {
        // A wildcard branch is allowed to have no matching child directory.
      }
      return
    }
    const segment = segments[index]
    if (segment === '*') {
      let entries
      try {
        entries = fs.readdirSync(parent, { withFileTypes: true }).sort((a, b) => compareStrings(a.name, b.name))
      } catch {
        return
      }
      for (const entry of entries) {
        if (entry.isDirectory() && !entry.isSymbolicLink()) visit(path.join(parent, entry.name), index + 1)
      }
      return
    }
    visit(path.join(parent, segment), index + 1)
  }
  visit(repoRoot, 0)
  return matches
}

function shouldSkipPath(fullPath, repoRoot, config, kind) {
  const relative = normalizeRelativePath(path.relative(repoRoot, fullPath))
  const segments = relative.split('/').filter(Boolean)
  if (segments.some((segment) => ['.git', 'dist', 'node_modules'].includes(segment))) return true
  if (kind === 'provider' && segments.some((segment) => config.vendoredSegments.includes(segment))) return true
  if (!config.includeTests && (segments.includes('test') || segments.includes('__tests__') || /(?:^|[._-])(test|spec)(?:[._-]|$)/i.test(path.basename(fullPath)))) return true
  return false
}

function collectFiles(repoRoot, rootSpecs, config, kind) {
  const files = []
  const seen = new Set()
  for (const spec of rootSpecs) {
    for (const root of expandRootSpec(repoRoot, spec)) {
      function visit(fullPath) {
        if (shouldSkipPath(fullPath, repoRoot, config, kind)) return
        let stat
        try {
          stat = fs.lstatSync(fullPath)
        } catch {
          return
        }
        if (stat.isSymbolicLink()) return
        if (stat.isDirectory()) {
          for (const entry of fs.readdirSync(fullPath, { withFileTypes: true }).sort((a, b) => compareStrings(a.name, b.name))) {
            visit(path.join(fullPath, entry.name))
          }
          return
        }
        if (!stat.isFile() || !SOURCE_EXTENSIONS.has(path.extname(fullPath).toLowerCase())) return
        const relative = normalizeRelativePath(path.relative(repoRoot, fullPath))
        if (seen.has(relative)) return
        seen.add(relative)
        files.push({ path: relative, absolutePath: fullPath, kind })
      }
      visit(root)
    }
  }
  return files.sort((a, b) => compareStrings(a.path, b.path))
}

function validateRootSpecs(repoRoot, rootSpecs, kind) {
  for (const spec of rootSpecs) {
    const matches = expandRootSpec(repoRoot, spec)
    if (!matches.length) fail(`${kind} root ${JSON.stringify(spec)} matched no directory`)
    for (const root of matches) {
      let stat
      try {
        stat = fs.lstatSync(root)
      } catch {
        fail(`${kind} root ${JSON.stringify(spec)} does not exist at ${root}`)
      }
      if (!stat.isDirectory() || stat.isSymbolicLink()) fail(`${kind} root ${JSON.stringify(spec)} is not a real directory at ${root}`)
    }
  }
}

// Mask comments without changing offsets.  Keeping newlines and string
// literals intact gives diagnostics the same line/column as the source file.
export function maskComments(source) {
  const chars = source.split('')
  let quote = null
  let regexLiteral = false
  let escaped = false
  let lineComment = false
  let blockComment = false
  for (let index = 0; index < chars.length; index += 1) {
    const char = chars[index]
    const next = chars[index + 1]
    if (lineComment) {
      if (char === '\n' || char === '\r') lineComment = false
      else chars[index] = ' '
      continue
    }
    if (blockComment) {
      if (char === '*' && next === '/') {
        chars[index] = ' '
        chars[index + 1] = ' '
        index += 1
        blockComment = false
      } else if (char !== '\n' && char !== '\r') {
        chars[index] = ' '
      }
      continue
    }
    if (regexLiteral) {
      if (escaped) escaped = false
      else if (char === '\\') escaped = true
      else if (char === '/') regexLiteral = false
      continue
    }
    if (quote) {
      if (escaped) escaped = false
      else if (char === '\\') escaped = true
      else if (char === quote) quote = null
      continue
    }
    if ((char === '/' && next === '/') && !quote) {
      chars[index] = ' '
      chars[index + 1] = ' '
      index += 1
      lineComment = true
      continue
    }
    if (char === '/' && next === '*') {
      chars[index] = ' '
      chars[index + 1] = ' '
      index += 1
      blockComment = true
      continue
    }
    if (char === '/') {
      const previous = chars[index - 1] ?? ''
      if ('=([{,:;!&|?'.includes(previous) || previous === '\n' || previous === '\r') {
        regexLiteral = true
        continue
      }
    }
    if (char === "'" || char === '"' || char === '`') quote = char
  }
  return chars.join('')
}

function lineStarts(source) {
  const starts = [0]
  for (let index = 0; index < source.length; index += 1) {
    if (source[index] === '\n') starts.push(index + 1)
  }
  return starts
}

function locationFor(starts, index) {
  let low = 0
  let high = starts.length
  while (low + 1 < high) {
    const middle = Math.floor((low + high) / 2)
    if (starts[middle] <= index) low = middle
    else high = middle
  }
  return { line: low + 1, column: index - starts[low] + 1 }
}

function sourceLineAt(source, starts, index) {
  const location = locationFor(starts, index)
  const lineStart = starts[location.line - 1]
  const lineEnd = source.indexOf('\n', lineStart)
  return source.slice(lineStart, lineEnd < 0 ? source.length : lineEnd).replace(/\r$/, '')
}

function addMatch(diagnostics, source, starts, rule, index, match, message) {
  const location = locationFor(starts, index)
  diagnostics.push({
    rule,
    path: source.path,
    line: location.line,
    column: location.column,
    match: match.length > 160 ? `${match.slice(0, 157)}...` : match,
    message,
    source: sourceLineAt(source.content, starts, index).trim(),
  })
}

function scanRegex(diagnostics, source, masked, starts, regex, rule, message) {
  for (const match of masked.matchAll(regex)) {
    addMatch(diagnostics, source, starts, rule, match.index, match[0], typeof message === 'function' ? message(match[0]) : message)
  }
}

function isProviderSource(source) {
  return source.kind === 'provider' && !source.canonicalConsumer
}

function cssRegions(source, masked) {
  const extension = path.extname(source.path).toLowerCase()
  if (extension !== '.vue' && !['.css', '.less', '.sass', '.scss'].includes(extension)) return []
  if (extension !== '.vue') return [{ text: masked, offset: 0 }]
  const regions = []
  const styleRe = /<style\b[^>]*>([\s\S]*?)<\/style\s*>/gi
  for (const match of masked.matchAll(styleRe)) regions.push({ text: match[1], offset: match.index + match[0].indexOf(match[1]) })
  return regions
}

function findMatchingBrace(text, openIndex) {
  let depth = 0
  let quote = null
  let escaped = false
  for (let index = openIndex; index < text.length; index += 1) {
    const char = text[index]
    if (quote) {
      if (escaped) escaped = false
      else if (char === '\\') escaped = true
      else if (char === quote) quote = null
      continue
    }
    if (char === "'" || char === '"') {
      quote = char
      continue
    }
    if (char === '{') depth += 1
    if (char === '}' && --depth === 0) return index
  }
  return -1
}

function hasCanonicalWidgetStyling(block, className) {
  // Ignore nested at-rule/selector bodies when counting declarations for the
  // selector that introduced this block.  This keeps a layout wrapper such as
  // `.panel { display:flex; gap:8px }` harmless while still catching a local
  // card/button recipe that repeats surface, border, radius, or typography.
  const declarations = block.replace(/[^{}]*\{[^{}]*\}/g, ' ')
  const properties = [...declarations.matchAll(/(?:^|[;\n])\s*([A-Za-z-]+)\s*:/g)].map((match) => match[1].toLowerCase())
  if (properties.length < 2) return false

  const visual = new Set([
    'background',
    'background-color',
    'border',
    'border-color',
    'border-radius',
    'box-shadow',
    'color',
    'fill',
    'font',
    'font-family',
    'font-size',
    'font-weight',
    'outline',
    'stroke',
    'text-shadow',
  ])
  if (properties.some((property) => visual.has(property))) return true

  // Icon/spinner/skeleton primitives can be visually re-derived by dimensions
  // and animation alone, but a lone width/height declaration is ordinary page
  // layout.  Require a little more evidence for this small subset.
  if (['icon', 'skeleton', 'spinner'].includes(className)) {
    return properties.length >= 3
  }
  return false
}

function scanCssSelectors(diagnostics, source, masked, starts) {
  const regions = cssRegions(source, masked)
  for (const region of regions) {
    const selectorRe = /([^{}]+)\{/g
    for (const selectorMatch of region.text.matchAll(selectorRe)) {
      const selector = selectorMatch[1]
      const selectorOffset = region.offset + selectorMatch.index
      const selectorStart = selectorOffset + selector.indexOf(selector.trim())
      if (isProviderSource(source)) {
        for (const match of selector.matchAll(/\.k-[A-Za-z0-9_-]+/g)) {
          addMatch(diagnostics, source, starts, RULES.PROVIDER_K_SELECTOR, selectorOffset + match.index, match[0], 'provider CSS must not redeclare a canonical .k-* recipe')
        }
      }
      // Strip at-rules before looking for widget classes.  Nested media/supports
      // blocks still leave the actual selector in the same prelude.
      const selectorWithoutAtRule = selector.replace(/@(?:media|supports|container|layer)\b[^,]*/g, ' ')
      const openBrace = selectorMatch.index + selector.length
      const closeBrace = findMatchingBrace(region.text, openBrace)
      const block = closeBrace > openBrace ? region.text.slice(openBrace + 1, closeBrace) : ''
      for (const match of selectorWithoutAtRule.matchAll(/\.([A-Za-z][A-Za-z0-9_-]*)/g)) {
        const className = match[1]
        if (!COMMON_WIDGET_NAMES.has(className) || !hasCanonicalWidgetStyling(block, className)) continue
        addMatch(diagnostics, source, starts, RULES.COMMON_WIDGET_SELECTOR, selectorStart + match.index, `.${className}`, `provider-local common widget selector .${className} must use the canonical k-* vocabulary`)
      }
    }
  }
}

function markupStackAt(masked, index) {
  const stack = []
  for (const tag of masked.matchAll(MARKUP_TAG_RE)) {
    if (tag.index >= index) break
    const full = tag[0]
    const name = tag[2].toLowerCase()
    if (tag[1]) {
      for (let stackIndex = stack.length - 1; stackIndex >= 0; stackIndex -= 1) {
        if (stack[stackIndex].name !== name) continue
        stack.splice(stackIndex, 1)
        break
      }
      continue
    }
    if (VOID_TAG_NAMES.has(name) || /\/\s*>$/.test(full)) continue
    stack.push({ name, openTag: full, openEnd: tag.index + full.length })
  }
  return stack
}

function matchingElementClose(masked, index, name) {
  let depth = 1
  for (const tag of masked.slice(index).matchAll(MARKUP_TAG_RE)) {
    const full = tag[0]
    if (tag[1] && tag[2].toLowerCase() === name) {
      depth -= 1
      if (depth === 0) return index + tag.index
    } else if (!tag[1] && tag[2].toLowerCase() === name && !VOID_TAG_NAMES.has(name) && !/\/\s*>$/.test(full)) {
      depth += 1
    }
  }
  return -1
}

function textWithoutMarkup(value) {
  return value.replace(/<\s*\/?[A-Za-z][^>]*>/g, ' ')
}

function hasMultilineContentAssignment(masked, index) {
  const start = Math.max(0, index - 512)
  const prefix = masked.slice(start, index)
  const boundary = Math.max(prefix.lastIndexOf(';'), prefix.lastIndexOf('{'), prefix.lastIndexOf('}'))
  return CONTENT_ASSIGNMENT_RE.test(prefix.slice(boundary + 1))
}

function isGlyphIconContent(source, masked, index, glyph) {
  const lineStart = masked.lastIndexOf('\n', index - 1) + 1
  const lineEnd = masked.indexOf('\n', index)
  const line = masked.slice(lineStart, lineEnd < 0 ? masked.length : lineEnd)

  if (ICON_CONTEXT_RE.test(line) || CONTENT_ASSIGNMENT_RE.test(line) || hasMultilineContentAssignment(masked, index)) return true

  // Keep enough markup context to classify controls whose attributes and text
  // span multiple lines. A prose arrow (`A → B`) has no enclosing icon
  // context, while `<button\n class="icon">\n ⚙\n</button>` does.
  const stack = markupStackAt(masked, index)
  if (stack.some((tag) => ICON_CONTEXT_RE.test(tag.openTag))) return true
  const parent = stack.at(-1)
  if (!parent) return false

  const closeIndex = matchingElementClose(masked, index + glyph.length, parent.name)
  if (closeIndex < 0) return false
  const beforeText = textWithoutMarkup(masked.slice(parent.openEnd, index)).trim()
  const afterText = textWithoutMarkup(masked.slice(index + glyph.length, closeIndex)).trim()
  if (!beforeText && !afterText) return true

  // Symbols at either edge of an interactive label are icon affordances
  // (`← Back`, `Open ↗`), but a symbol between words remains prose.
  const interactive = parent.name === 'button' || parent.name === 'a' || /\bclass(?:Name)?\s*=\s*["'`][^"'`]*\blink\b/i.test(parent.openTag)
  return interactive && (!beforeText || !afterText)
}

function scanGlyphs(diagnostics, source, masked, starts) {
  for (const match of masked.matchAll(FORBIDDEN_GLYPH_RE)) {
    if (!isGlyphIconContent(source, masked, match.index, match[0])) continue
    addMatch(diagnostics, source, starts, RULES.FORBIDDEN_GLYPH, match.index, match[0], 'Unicode/emoji glyph icons are forbidden; use Lucide or portalkit ic()')
  }
}

function findMatchingParen(text, openIndex) {
  let depth = 0
  let quote = null
  let escaped = false
  for (let index = openIndex; index < text.length; index += 1) {
    const char = text[index]
    if (quote) {
      if (escaped) escaped = false
      else if (char === '\\') escaped = true
      else if (char === quote) quote = null
      continue
    }
    if (char === "'" || char === '"') {
      quote = char
      continue
    }
    if (char === '(') depth += 1
    if (char === ')' && --depth === 0) return index
  }
  return -1
}

function fallbackTokenAt(text, index) {
  const open = text.lastIndexOf('var(', index)
  if (open < 0) return null
  const close = findMatchingParen(text, open)
  if (close < index) return null
  const inside = text.slice(open + 4, close)
  const tokenMatch = inside.match(/^\s*(--color-[A-Za-z0-9_-]+)\s*,/)
  if (!tokenMatch) return null
  const comma = inside.indexOf(',')
  const rawStart = open + 4 + comma + 1
  if (index < rawStart) return null
  return tokenMatch[1].slice('--color-'.length)
}

function normalizeColor(value) {
  const compact = value.toLowerCase().replace(/\s+/g, '')
  const hex = compact.match(/^#([0-9a-f]{3}|[0-9a-f]{4}|[0-9a-f]{6}|[0-9a-f]{8})$/)
  if (hex) {
    const expanded = hex[1].length <= 4 ? [...hex[1]].map((char) => char + char).join('') : hex[1]
    return `#${expanded}`
  }
  const rgba = compact.match(/^rgba?\(([^)]+)\)$/)
  if (rgba) {
    const values = rgba[1].split(',').map((value) => Number(value))
    if (values.every((value) => Number.isFinite(value))) return `rgba(${values.map((value) => Number(value.toFixed(4))).join(',')})`
  }
  return compact
}

function isAllowedFallback(text, index, raw) {
  const token = fallbackTokenAt(text, index)
  if (!token || !COLOR_TOKENS.has(token) || !DARK_FALLBACKS[token]) return false
  const normalized = normalizeColor(raw)
  return DARK_FALLBACKS[token].some((allowed) => normalizeColor(allowed) === normalized)
}

function isInsideURL(text, index) {
  const open = text.lastIndexOf('url(', index)
  if (open < 0) return false
  const close = findMatchingParen(text, open)
  return close >= index
}

function isTokenAuthorityDeclaration(source, masked, index) {
  if (!source.tokenAuthority) return false
  const lineStart = masked.lastIndexOf('\n', index - 1)
  const semicolon = masked.lastIndexOf(';', index - 1)
  const brace = masked.lastIndexOf('{', index - 1)
  const declarationStart = Math.max(lineStart, semicolon, brace) + 1
  const prefix = masked.slice(declarationStart, index)
  return /--(?:color|faros)-[A-Za-z0-9_-]+\s*:\s*[^;{}]*$/.test(prefix)
}

function scanColors(diagnostics, source, masked, starts) {
  for (const match of masked.matchAll(/var\(\s*(--color-[A-Za-z0-9_-]+)/g)) {
    const token = match[1].slice('--color-'.length)
    if (!COLOR_TOKENS.has(token)) addMatch(diagnostics, source, starts, RULES.UNKNOWN_COLOR_TOKEN, match.index + match[0].indexOf(match[1]), match[1], `unknown design token ${match[1]}`)
  }

  for (const match of masked.matchAll(/(--color-[A-Za-z0-9_-]+)\s*:/g)) {
    const token = match[1].slice('--color-'.length)
    if (!COLOR_TOKENS.has(token)) addMatch(diagnostics, source, starts, RULES.UNKNOWN_COLOR_TOKEN, match.index, match[1], `unknown design token declaration ${match[1]}`)
  }

  const rawRe = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?)\(/g
  for (const match of masked.matchAll(rawRe)) {
    const raw = match[0].endsWith('(') ? masked.slice(match.index, Math.min(masked.length, match.index + 120)).match(/^(?:rgba?|hsla?)\([^)]*\)/)?.[0] ?? match[0] : match[0]
    if (isInsideURL(masked, match.index) || isAllowedFallback(masked, match.index, raw) || isTokenAuthorityDeclaration(source, masked, match.index)) continue
    addMatch(diagnostics, source, starts, RULES.RAW_COLOR, match.index, raw, 'raw color must use a design token (dark-base var() fallbacks are the only CSS literal exception)')
  }

  for (const match of masked.matchAll(RAW_COLOR_UTILITY_RE)) {
    addMatch(diagnostics, source, starts, RULES.RAW_COLOR, match.index, match[0].trim(), 'raw color utility must use a design-token utility')
  }

  const declarationRe = /(?:^|[;{}])\s*(?:color|background(?:-color)?|border(?:-[A-Za-z-]+)?|outline(?:-color)?|fill|stroke|box-shadow|text-shadow)\s*:[^;{}\n]*/g
  for (const declaration of masked.matchAll(declarationRe)) {
    for (const named of declaration[0].matchAll(/\b(?:black|blue|gray|green|grey|orange|purple|red|violet|white|yellow)\b/gi)) {
      if (!RAW_NAMED_COLORS.has(named[0].toLowerCase())) continue
      addMatch(diagnostics, source, starts, RULES.RAW_COLOR, declaration.index + named.index, named[0], 'named raw color must use a design token')
    }
  }
}

function hasEqualCircleDimensions(classes) {
  // Vue's `:class` variants (`group-hover:w-6`) and a complete opening tag
  // arrive here together.  Treat variant punctuation/attribute delimiters as
  // token separators so the static `h-6` and the eventual `w-6` are visible.
  const normalized = classes.replace(/[^A-Za-z0-9_.-]+/g, ' ')
  const sizes = [...normalized.matchAll(/(?:^|\s)size-([0-9]+(?:\.[0-9]+)?)(?:\s|$)/g)].map((match) => match[1])
  const heights = [...normalized.matchAll(/(?:^|\s)h-([0-9]+(?:\.[0-9]+)?)(?:\s|$)/g)].map((match) => match[1])
  const widths = [...normalized.matchAll(/(?:^|\s)w-([0-9]+(?:\.[0-9]+)?)(?:\s|$)/g)].map((match) => match[1])
  if (sizes.some((size) => (!heights.length || heights.includes(size)) && (!widths.length || widths.includes(size)))) return true
  return heights.some((height) => widths.includes(height))
}

function markupTagContext(masked, index) {
  const open = masked.lastIndexOf('<', index)
  const close = masked.indexOf('>', index)
  if (open < 0 || close < index) return ''
  return masked.slice(open, close + 1)
}

function parseRadius(value) {
  const match = value.trim().match(/^([0-9]+(?:\.[0-9]+)?)(px|rem|em|%)$/i)
  if (!match) return null
  const amount = Number(match[1])
  const unit = match[2].toLowerCase()
  return { amount, unit, pixels: unit === 'rem' || unit === 'em' ? amount * 16 : amount }
}

function isCircleDeclaration(masked, index, value) {
  if (!/(?:50%|100%|9999?px|9999?rem|9999?em)/i.test(value)) return false
  const open = masked.lastIndexOf('{', index)
  const close = masked.indexOf('}', index)
  if (open < 0 || close < 0) return false
  const block = masked.slice(open, close)
  const dimensions = [...block.matchAll(/(?:width|height|inline-size|block-size)\s*:\s*([0-9]+(?:\.[0-9]+)?)(px|rem|em)/gi)]
  const pairs = new Map()
  for (const match of dimensions) pairs.set(match[0].split(':')[0].trim().toLowerCase(), Number(match[1]) * (match[2].toLowerCase() === 'px' ? 1 : 16))
  const width = pairs.get('width') ?? pairs.get('inline-size')
  const height = pairs.get('height') ?? pairs.get('block-size')
  return width !== undefined && height !== undefined && width === height
}

function scanRadii(diagnostics, source, masked, starts) {
  const radiusRe = /border-radius\s*:\s*([^;{}\n]+)/gi
  for (const match of masked.matchAll(radiusRe)) {
    const value = match[1].trim()
    const parsed = parseRadius(value)
    const pill = /(?:9999?px|9999?rem|9999?em|100%|50%)/i.test(value) || (parsed && parsed.pixels > 12)
    if (!pill || isCircleDeclaration(masked, match.index, value)) continue
    addMatch(diagnostics, source, starts, RULES.PILL_RADIUS, match.index, `${match[0].split(':')[0]}: ${value}`, 'pill/soft radius is outside the sharp radius law; use a k-* recipe or a design-book exact exception')
  }

  const classRe = /\bclass(?:Name)?\s*=\s*(["'`])([\s\S]*?)\1/g
  for (const match of masked.matchAll(classRe)) {
    const classes = match[2]
    const context = `${classes} ${markupTagContext(masked, match.index)} ${sourceLineAt(source.content, starts, match.index)}`
    for (const rounded of classes.matchAll(/(?:^|\s)(rounded-full|rounded-\[(?:[^\]]+)\])/g)) {
      const name = rounded[1]
      const arbitrary = name.match(/^rounded-\[([0-9.]+)(px|rem|em|%)\]$/i)
      const isPill = name === 'rounded-full' || (arbitrary && ((arbitrary[2] === '%' && Number(arbitrary[1]) >= 50) || (arbitrary[2] !== '%' && Number(arbitrary[1]) * (arbitrary[2] === 'px' ? 1 : 16) > 12)))
      if (!isPill || (name === 'rounded-full' && hasEqualCircleDimensions(context))) continue
      addMatch(diagnostics, source, starts, RULES.PILL_RADIUS, match.index + match[0].indexOf(name), name, 'pill/soft radius utility is outside the sharp radius law; use a k-* recipe or a design-book exact exception')
    }
  }
}

function scanFile(source) {
  source.content = fs.readFileSync(source.absolutePath, 'utf8')
  const masked = maskComments(source.content)
  const starts = lineStarts(source.content)
  const diagnostics = []

  scanRegex(diagnostics, source, masked, starts, /\b(?:pk-[A-Za-z0-9_-]+|data-pk(?:-[A-Za-z0-9_-]+)?)/g, RULES.LEGACY_PK, 'legacy pk-* or data-pk hook, selector, style hook, or DOM id is forbidden after the k-* migration')
  scanRegex(diagnostics, source, masked, starts, /\b(?:window|globalThis|document\.defaultView)\s*(?:\?\.\s*|\.\s*)(?:confirm|alert)\s*\(/g, RULES.NATIVE_DIALOG, 'native confirm()/alert() is forbidden; use the PortalKit dialog primitive')
  scanRegex(diagnostics, source, masked, starts, /\b(?:window|globalThis|document\.defaultView)\s*\[\s*["'](?:confirm|alert)["']\s*\]\s*\(/g, RULES.NATIVE_DIALOG, 'native confirm()/alert() is forbidden; use the PortalKit dialog primitive')
  scanRegex(diagnostics, source, masked, starts, /(?<![A-Za-z0-9_$.["'])\b(?:confirm|alert)\s*\(/g, RULES.NATIVE_DIALOG, 'native confirm()/alert() is forbidden; use the PortalKit dialog primitive')
  scanGlyphs(diagnostics, source, masked, starts)
  scanColors(diagnostics, source, masked, starts)
  scanRadii(diagnostics, source, masked, starts)
  scanCssSelectors(diagnostics, source, masked, starts)
  return diagnostics
}

function diagnosticSort(a, b) {
  return compareStrings(a.path, b.path) || a.line - b.line || a.column - b.column || compareStrings(a.rule, b.rule) || compareStrings(a.match, b.match)
}

function applyExceptions(diagnostics, exceptions) {
  const kept = []
  for (const diagnostic of diagnostics) {
    const exception = exceptions.find((candidate) => candidate.rule === diagnostic.rule && candidate.path === diagnostic.path && candidate.line === diagnostic.line && candidate.column === diagnostic.column && diagnostic.source.includes(candidate.match))
    if (!exception) kept.push(diagnostic)
  }
  return kept
}

export function scan(options = {}) {
  const repoRoot = path.resolve(options.repoRoot ?? REPO_ROOT)
  let config = options.config
  if (!config) config = loadConfig(repoRoot, options.configPath ?? path.relative(repoRoot, DEFAULT_CONFIG_PATH))
  else config = validateConfig({ ...cloneDefaultConfig(), ...config })
  if (options.includeTests !== undefined) config = validateConfig({ ...config, includeTests: options.includeTests })
  const canonicalRoots = options.canonicalRoots ?? config.canonicalRoots
  const providerRoots = options.providerRoots ?? config.providerRoots
  if (!Array.isArray(canonicalRoots) || !Array.isArray(providerRoots)) fail('canonicalRoots and providerRoots must be arrays')
  validateRootSpecs(repoRoot, canonicalRoots, 'canonical')
  validateRootSpecs(repoRoot, providerRoots, 'provider')
  const sources = [
    ...collectFiles(repoRoot, canonicalRoots, config, 'canonical'),
    ...collectFiles(repoRoot, providerRoots, config, 'provider'),
  ]
  const canonicalConsumerPaths = new Set(config.canonicalConsumerPaths.map(normalizeRelativePath))
  const tokenAuthorityPaths = new Set(config.tokenAuthorityPaths.map(normalizeRelativePath))
  for (const source of sources) {
    source.canonicalConsumer = canonicalConsumerPaths.has(source.path)
    source.tokenAuthority = tokenAuthorityPaths.has(source.path)
  }
  const filesByPath = new Map()
  for (const source of sources) {
    try {
      source.content = fs.readFileSync(source.absolutePath, 'utf8')
      filesByPath.set(source.path, source)
    } catch (error) {
      fail(`cannot read ${source.path}: ${error.message}`)
    }
  }
  let exceptions
  try {
    exceptions = options.exceptions ? validateExceptionRegistry(options.exceptions, filesByPath, repoRoot) : readExceptions(config, repoRoot, filesByPath)
  } catch (error) {
    return {
      diagnostics: [{
        rule: RULES.EXCEPTION_REGISTRY,
        path: options.exceptionsPath ?? (typeof config.exceptions === 'string' ? normalizeRelativePath(config.exceptions) : '<inline>'),
        line: 1,
        column: 1,
        match: '',
        message: error.message,
        source: '',
      }],
      files: sources.map((source) => source.path),
      counts: { [RULES.EXCEPTION_REGISTRY]: 1 },
    }
  }
  const diagnostics = []
  for (const source of sources) diagnostics.push(...scanFile(source))
  const filtered = applyExceptions(diagnostics, exceptions).sort(diagnosticSort)
  const counts = {}
  for (const diagnostic of filtered) counts[diagnostic.rule] = (counts[diagnostic.rule] ?? 0) + 1
  return { diagnostics: filtered, files: sources.map((source) => source.path), counts }
}

function parseArgs(argv) {
  const options = {}
  const canonicalRoots = []
  const providerRoots = []
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]
    if (arg === '--config') options.configPath = argv[++index]
    else if (arg === '--exceptions') options.exceptionsPath = argv[++index]
    else if (arg === '--canonical-root') canonicalRoots.push(argv[++index])
    else if (arg === '--provider-root') providerRoots.push(argv[++index])
    else if (arg === '--include-tests') options.includeTests = true
    else if (arg === '--help' || arg === '-h') options.help = true
    else fail(`unknown argument ${arg}`)
  }
  if (canonicalRoots.length) options.canonicalRoots = canonicalRoots
  if (providerRoots.length) options.providerRoots = providerRoots
  return options
}

function usage() {
  return [
    'Usage: node hack/verify-ui-conformance.mjs [options]',
    '',
    'Scans the host portal, Dex, every provider portal, and canonical provider-sdk/portalkit* roots.',
    'dist, node_modules, and byte-synced provider portalkit copies are excluded.',
    '',
    'Options:',
    '  --config PATH          JSON config (default hack/ui-conformance.config.json)',
    '  --exceptions PATH      Override the exception registry path',
    '  --canonical-root PATH  Replace canonical roots (repeatable)',
    '  --provider-root PATH   Replace provider roots (repeatable)',
    '  --include-tests        Include files named *.test.* / *.spec.* and test dirs',
  ].join('\n')
}

export function formatDiagnostic(diagnostic) {
  const match = diagnostic.match ? ` match=${JSON.stringify(diagnostic.match)}` : ''
  return `${diagnostic.path}:${diagnostic.line}:${diagnostic.column} [${diagnostic.rule}] ${diagnostic.message}${match}`
}

function main() {
  let options
  try {
    options = parseArgs(process.argv.slice(2))
    if (options.help) {
      console.log(usage())
      return
    }
    const envCanonical = process.env.FAROS_UI_CONFORMANCE_CANONICAL_ROOTS ?? process.env.UI_CONFORMANCE_CANONICAL_ROOTS
    const envProvider = process.env.FAROS_UI_CONFORMANCE_PROVIDER_ROOTS ?? process.env.UI_CONFORMANCE_PROVIDER_ROOTS
    const envExceptions = process.env.FAROS_UI_CONFORMANCE_EXCEPTIONS ?? process.env.UI_CONFORMANCE_EXCEPTIONS
    if (envCanonical && !options.canonicalRoots) options.canonicalRoots = splitOverride(envCanonical)
    if (envProvider && !options.providerRoots) options.providerRoots = splitOverride(envProvider)
    if (envExceptions && !options.exceptionsPath) options.exceptionsPath = envExceptions
    if (options.exceptionsPath) {
      const config = options.configPath ? loadConfig(REPO_ROOT, options.configPath) : loadConfig(REPO_ROOT)
      options.config = { ...config, exceptions: options.exceptionsPath }
    }
    const result = scan(options)
    console.log(`UI conformance scanned ${result.files.length} files: ${result.diagnostics.length} violation(s).`)
    for (const diagnostic of result.diagnostics) console.log(formatDiagnostic(diagnostic))
    const countText = Object.entries(result.counts).sort(([a], [b]) => compareStrings(a, b)).map(([rule, count]) => `${rule}=${count}`).join(' ')
    console.log(`UI conformance counts: ${countText || 'none'}`)
    if (result.diagnostics.length) process.exitCode = 1
  } catch (error) {
    console.error(`UI conformance configuration error: ${error.message}`)
    process.exitCode = 2
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))) main()
