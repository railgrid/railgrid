#!/usr/bin/env node
// Deterministic source inventory and signal collector for the Railgrid UI audit.
// This script reports static signals only; it does not label any occurrence a
// design-system violation or substitute for visual/manual review.

import { auditDirectory } from './audit-design-system-output.mjs'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, extname, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url))
const DEFAULT_ROOT = resolve(SCRIPT_DIR, '..')
const FRONTEND_EXTENSIONS = new Set([
  '.vue', '.ts', '.tsx', '.js', '.jsx', '.mjs', '.cjs', '.css', '.scss', '.sass', '.less',
  '.html', '.htm', '.mdx', '.svg', '.png', '.jpg', '.jpeg', '.gif', '.webp', '.ico', '.woff', '.woff2',
])
const CONFIG_NAMES = /^(?:package\.json|tsconfig(?:\.[^/]+)?\.json|vite\.config\.[cm]?[jt]s|vitest\.config\.[cm]?[jt]s|playwright\.config\.[cm]?[jt]s|tailwind\.config\.[cm]?[jt]s|postcss\.config\.[cm]?[jt]s)$/
const BASE_FRONTEND_ROOTS = [
  /^portal\//,
  /^providers\/[^/]+\/portal\//,
  /^provider-sdk\/(?:portalkit|portalkit-vue|agentkit|agentkit-vue)\//,
  /^hack\/dex\/web\//,
  /^hack\/design-review\//,
  /^providers\/[^/]+\/examples\//,
  /(?:^|\/)(?:examples?|demos?|samples?)\//,
]
const EXTRA_FRONTEND_FILES = new Set([
  'hack/sync-portalkit.sh',
  'hack/ui-conformance.config.json',
  'hack/ui-conformance-exceptions.json',
  'hack/verify-ui-conformance.mjs',
  'hack/verify-ui-conformance.test.mjs',
  'hack/create-flow-conformance.test.mjs',
  'hack/verify-agentkit-dependencies.test.mjs',
  'hack/verify-design-docs.mjs',
  'hack/verify-design-docs.test.mjs',
  'hack/verify-ui-banner-backlog.mjs',
  'hack/verify-ui-banner-backlog.test.mjs',
  'hack/ui-banner-backlog.json',
  'hack/agent-overlay-resize-test.mjs',
  'hack/metering-scroll-regression.mjs',
  'hack/test-agentkit-upload-retry.mjs',
  'provider-sdk/statuspage/statuspage.go',
  'pkg/server/auth/device_page.html',
])
const ASSET_EXTENSIONS = new Set(['.svg', '.png', '.jpg', '.jpeg', '.gif', '.webp', '.ico', '.woff', '.woff2'])
const SOURCE_EXTENSIONS = new Set(['.vue', '.ts', '.tsx', '.js', '.jsx', '.mjs', '.cjs', '.css', '.scss', '.sass', '.less', '.html', '.htm', '.mdx'])
const NATIVE_CONTROLS = new Set(['button', 'input', 'select', 'textarea', 'dialog'])
const SEMANTIC_CLASSES = /(?:^|[\s_-])(button|btn|card|panel|badge|status|tab|modal|dialog|alert|toast|tooltip|field|input|toolbar|empty|loading)(?:$|[\s_-])/i
const COLOR_RE = /(?:#[\da-f]{3,8}\b|\b(?:rgba?|hsla?)\(\s*[^)]*\))/ig
const DESIGN_PROPERTY_RE = /^(?:margin(?:-.+)?|padding(?:-.+)?|gap|row-gap|column-gap|font-size|line-height|letter-spacing|border-radius|border(?:-.+)?|box-shadow|width|height|min-width|max-width|min-height|max-height|inset(?:-.+)?|top|right|bottom|left)$/i

function posixPath(path) {
  return path.split(sep).join('/')
}

function sha256(content) {
  return createHash('sha256').update(content).digest('hex')
}

function readGitList(root, args) {
  try {
    return execFileSync('git', args, { cwd: root, encoding: 'buffer', stdio: ['ignore', 'pipe', 'ignore'] })
      .toString('utf8').split('\0').filter(Boolean).map(posixPath)
  } catch (error) {
    throw new Error(`Unable to enumerate audit scope with git ${args.join(' ')}: ${error.message}`)
  }
}

function getTrackedAndUntracked(root) {
  const tracked = new Set(readGitList(root, ['ls-files', '-z']))
  const untracked = new Set(readGitList(root, ['ls-files', '--others', '--exclude-standard', '-z']))
  const changed = new Set([
    ...readGitList(root, ['diff', '--name-only', '-z']),
    ...readGitList(root, ['diff', '--cached', '--name-only', '-z']),
  ])
  return { paths: [...new Set([...tracked, ...untracked])].sort(), tracked, untracked, changed }
}

function isInventoryRoot(path) {
  const parts = path.split('/')
  const hackVisualRoot = path.startsWith('hack/') && (parts.some(part => /-visual$/.test(part) || part === 'impeccable-preview'))
  const hackVisualTool = path.startsWith('hack/') && /(?:visual|browser|screenshot|hit-test|geometry|experience|preview|resize|regression)/i.test(parts.at(-1))
  const auditTool = /^hack\/audit-design-system(?:[-.]|\/)/.test(path)
  const previewBridge = /^providers\/infrastructure\/dev-agent\/preview-bridge[^/]*\.mjs$/.test(path)
  return isInFrontendRoot(path) || hackVisualRoot || hackVisualTool || auditTool || previewBridge || EXTRA_FRONTEND_FILES.has(path)
}

export function candidateExtension(path) {
  const extension = extname(path).toLowerCase()
  if (FRONTEND_EXTENSIONS.has(extension)) return extension
  if (isInventoryRoot(path) && ['.md', '.txt', '.yaml', '.yml', '.mod', '.sum'].includes(extension)) return extension
  if (extension === '.json' && (CONFIG_NAMES.test(path.split('/').at(-1)) || isInventoryRoot(path))) return extension
  if (path === 'hack/ui-conformance.config.json' || path === 'hack/ui-conformance-exceptions.json' || path === 'hack/ui-banner-backlog.json') return '.json'
  return null
}

export function summarizeJsonInventory(content) {
  const data = JSON.parse(content)
  if (Array.isArray(data)) return { type: 'array', items: data.length }
  if (data === null) return { type: 'null' }
  if (typeof data === 'object') return { type: 'object', keys: Object.keys(data).length }
  return { type: typeof data }
}

export function isLikelyInlineGoFrontend(path, content) {
  if (extname(path) !== '.go' || /_test\.go$/.test(path)) return false
  const example = /(?:^|\/)examples?\//.test(path)
  if (!example && !/(?:["']html\/template["']|template\.New\s*\(|template\.Must\s*\()/u.test(content)) return false
  return /`[^`]*<(?:!doctype\s+html\b|html|head|body|main|section|div|form|button|script|style)\b/siu.test(content)
}

function statusPageConsumer(path, content) {
  return path.endsWith('.go') && !path.endsWith('_test.go') &&
    content.includes('"github.com/railgrid/provider-sdk/statuspage"') && /\bstatuspage\.Render\s*\(/.test(content)
}

function isInFrontendRoot(path) {
  return BASE_FRONTEND_ROOTS.some(pattern => pattern.test(path))
}

export function classifyInventoryFile(path, extension = extname(path).toLowerCase(), content = '') {
  if (path.split('/').some(part => ['node_modules', 'vendor', 'dist', 'coverage', '.vite', 'build'].includes(part))) {
    return { included: false, category: 'generated-or-third-party', reason: 'Dependency, generated bundle, or build output; source inputs are inventoried instead.' }
  }
  const basename = path.split('/').at(-1)
  if (/^(?:package-lock\.json|npm-shrinkwrap\.json|pnpm-lock\.yaml|yarn\.lock|bun\.lock(?:b)?)$/.test(basename)) {
    return { included: false, category: 'generated-dependency-lockfile', reason: 'Dependency lockfile records generated package resolution, not an independent frontend implementation.' }
  }
  const examplePath = /(?:^|\/)(?:examples?|demos?|samples?)(?:\/|$)/.test(path)
  if (extension === '.go' && isLikelyInlineGoFrontend(path, content)) {
    return { included: true, category: 'inline-go-template', reason: 'Go source contains a raw HTML template used by a user-facing page or frontend example.' }
  }
  if (extension === '.go' && statusPageConsumer(path, content)) {
    return { included: true, category: 'standalone-status-page-caller', reason: 'Renders the shared status document from a browser endpoint.', analysisStatus: 'status-page-call-sites' }
  }
  const backendExample = examplePath && /(?:^|\/)(?:backend|server|api|controller|operator)(?:\/|$)/i.test(path)
  if (backendExample) {
    return { included: false, category: 'non-frontend-example-data', reason: 'Backend example code or data without detected user-facing HTML is retained in the inventory but outside the frontend implementation scope.' }
  }
  if (EXTRA_FRONTEND_FILES.has(path)) {
    const isTest = /\.(?:test|spec)\./.test(path) || path === 'hack/agent-overlay-resize-test.mjs' || path === 'hack/metering-scroll-regression.mjs' || path === 'hack/test-agentkit-upload-retry.mjs'
    return { included: true, category: path.endsWith('.go') ? 'inline-go-template' : isTest ? 'test-or-fixture' : 'frontend-tooling-or-config', reason: 'Explicitly relevant to shared UI distribution, conformance, design documentation validation, or embedded user-facing HTML.' }
  }
  const frontendExample = examplePath && (/(?:^|\/)frontend(?:\/|$)/i.test(path) || SOURCE_EXTENSIONS.has(extension))
  if (examplePath && !/^(?:README(?:\..+)?|LICENSE(?:\..+)?)$/i.test(basename) && (backendExample || !frontendExample)) {
    return { included: false, category: 'non-frontend-example-data', reason: 'Deployment manifests, backend code, or example packaging data are retained in the inventory but are outside the frontend implementation scope.' }
  }
  if (isInventoryRoot(path)) {
    const isLicense = /(?:^|\/)licenses?(?:\/|$)/i.test(path) || /license/i.test(basename)
    if (extension === '.md' || /^(?:README(?:\..+)?|LICENSE(?:\..+)?)$/i.test(basename) || isLicense) {
      return { included: true, category: 'reference-document', reason: 'Reference prose, license, or static text is inventoried by hash and is not AST or markup analyzed.', analysisStatus: 'reference-inventory-only' }
    }
    if (extension === '.txt' && basename === 'agent-digest-time-zones.txt') {
      return { included: true, category: 'frontend-data', reason: 'Raw-imported timezone names are inventoried by hash; plain text values are not UI markup.', analysisStatus: 'text-data-inventory-only' }
    }
    if (extension === '.txt' && basename === 'robots.txt') {
      return { included: true, category: 'frontend-static-text', reason: 'Static frontend text asset is inventoried by hash and is not parsed as markup.', analysisStatus: 'hash-inventory-only-asset' }
    }
    if (basename === '.gitignore') {
      return { included: true, category: 'frontend-tooling-config', reason: 'Ignore metadata is inventoried but is not UI source.', analysisStatus: 'configuration-inventory-only' }
    }
    if (path.startsWith('hack/') && ['.py', '.sh'].includes(extension)) {
      const isTest = /\.(?:test|spec)\./.test(basename)
      return { included: true, category: isTest ? 'test-or-fixture' : 'visual-review-tooling', reason: 'Browser or development helper is inventoried as review tooling; it is not parsed as frontend UI.', analysisStatus: 'inventory-only-configuration-or-tool' }
    }
    if (extension === '.json') {
      const isConfig = CONFIG_NAMES.test(basename)
      const isFixture = path.includes('/test/fixtures/')
      return { included: true, category: isFixture ? 'test-fixture-data' : isConfig ? 'frontend-configuration' : 'frontend-data', reason: isFixture ? 'JSON test fixture is parsed and inventoried as data; it is not treated as rendered markup.' : isConfig ? 'Frontend JSON configuration is parsed and inventoried; it is not treated as product markup.' : 'Runtime or generated frontend JSON data is parsed and inventoried; it is not treated as rendered markup.', analysisStatus: 'json-inventory-only' }
    }
    if (extension === '.go' && /_test\.go$/.test(path)) {
      return { included: true, category: 'test-or-fixture', reason: 'Go test source is inventoried; the frontend audit does not parse Go tests.', analysisStatus: 'inventory-only-unsupported-source' }
    }
    if (extension === '.go' && path.startsWith('hack/dex/web/')) {
      return { included: true, category: 'frontend-support-source', reason: 'Go embed wrapper supports the Dex frontend; frontend files are analyzed separately.', analysisStatus: 'inventory-only-frontend-support' }
    }
    if (basename === 'Dockerfile' || extension === '.mod' || extension === '.sum' || extension === '.yaml' || extension === '.yml') {
      return { included: true, category: 'frontend-tooling-or-example-config', reason: 'Build, ignore, or example configuration is inventoried by hash and is not frontend source code.', analysisStatus: 'configuration-inventory-only' }
    }
    if (!SOURCE_EXTENSIONS.has(extension) && !ASSET_EXTENSIONS.has(extension)) {
      return { included: true, category: 'unclassified-frontend-root-file', reason: 'File is retained because it is inside a declared frontend root; its format has no applicable source analyzer.', analysisStatus: 'inventory-only-unsupported-format' }
    }
    const role = path.includes('/test/') || /(?:^|\/)(?:__tests__|fixtures|stories)(?:\/|$)/.test(path) || /\.(?:test|spec)\./.test(path)
      ? 'test-or-fixture'
      : path.startsWith('hack/design-review/') ? 'review-example-or-tooling'
      : /^hack\/audit-design-system(?:[-.]|\/)/.test(path) ? 'audit-tooling'
          : /^providers\/infrastructure\/dev-agent\/preview-bridge[^/]*\.mjs$/.test(path) ? 'application-frontend-adjacent'
          : path.startsWith('hack/dex/web/') ? 'dex-login-frontend'
          : path.startsWith('hack/') ? 'visual-review-tooling'
        : /(?:^|\/)examples?\//.test(path) ? 'frontend-example'
          : path.startsWith('provider-sdk/') ? 'canonical-design-system-source'
              : 'application-frontend'
    return { included: true, category: role, reason: `Included from repository frontend root${examplePath ? ' as frontend example source' : ''}.` }
  }
  return { included: false, category: 'outside-frontend-scope', reason: 'Frontend-like extension outside declared app, shared UI, Dex, example, or UI-tooling roots; retained in inventory with the exclusion reason.' }
}

function worktreeRoots(root) {
  const roots = [root]
  try {
    const output = execFileSync('git', ['worktree', 'list', '--porcelain'], { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] })
    for (const match of output.matchAll(/^worktree (.+)$/gm)) roots.push(resolve(match[1]))
  } catch { /* A non-worktree checkout still uses its own dependencies. */ }
  return [...new Set(roots)]
}

function npmCacheRoots() {
  const cache = process.env.npm_config_cache || join(process.env.HOME || '', '.npm')
  const npx = join(cache, '_npx')
  if (!existsSync(npx)) return []
  try {
    return readdirSync(npx, { withFileTypes: true }).filter(entry => entry.isDirectory()).map(entry => entry.name).sort().map(name => join(npx, name, 'node_modules'))
  } catch { return [] }
}

function dependencyRoots(root) {
  const roots = []
  for (const worktree of worktreeRoots(root)) {
    roots.push(join(worktree, 'portal'))
    const providerRoot = join(worktree, 'providers')
    if (!existsSync(providerRoot)) continue
    try {
      for (const provider of readdirSync(providerRoot, { withFileTypes: true }).filter(entry => entry.isDirectory()).map(entry => entry.name).sort()) {
        const portal = join(providerRoot, provider, 'portal')
        if (existsSync(portal)) roots.push(portal)
      }
    } catch { /* A partial checkout may not expose provider folders. */ }
  }
  return roots
}

function packageVersion(requireFrom, name) {
  try {
    let directory = dirname(requireFrom.resolve(name))
    while (directory !== dirname(directory)) {
      const manifest = join(directory, 'package.json')
      if (existsSync(manifest)) {
        const data = JSON.parse(readFileSync(manifest, 'utf8'))
        if (data.name === name) return { version: data.version, entry: requireFrom.resolve(name) }
      }
      directory = dirname(directory)
    }
  } catch { /* Optional parser not installed. */ }
  return null
}

function loadParser(name, roots) {
  for (const root of roots) {
    const requireFrom = createRequire(join(root, 'package.json'))
    try {
      const value = requireFrom(name)
      const details = packageVersion(requireFrom, name)
      return { value, version: details?.version || 'unknown', source: root }
    } catch { /* Try the next installed dependency root. */ }
  }
  return null
}

export function resolveParsers(root = DEFAULT_ROOT) {
  const roots = [...dependencyRoots(root), ...worktreeRoots(root), ...npmCacheRoots()]
  const uniqueRoots = [...new Set(roots)]
  const names = ['typescript', '@vue/compiler-sfc', '@vue/compiler-dom', 'postcss']
  const loaded = Object.fromEntries(names.map(name => [name, loadParser(name, uniqueRoots)]))
  return {
    typescript: loaded.typescript?.value || null,
    vueSfc: loaded['@vue/compiler-sfc']?.value || null,
    vueDom: loaded['@vue/compiler-dom']?.value || null,
    postcss: loaded.postcss?.value || null,
    versions: Object.fromEntries(names.map(name => [name, loaded[name]?.version || null])),
  }
}

function sourcePosition(source, offset) {
  const before = source.slice(0, Math.max(0, offset))
  const line = 1 + (before.match(/\n/g) || []).length
  const lastBreak = before.lastIndexOf('\n')
  return { line, column: offset - lastBreak }
}

function resolveImportTarget(fromPath, moduleSpecifier, byPath) {
  let stem
  if (moduleSpecifier.startsWith('@/')) {
    let portalPrefix = null
    if (fromPath.startsWith('providers/')) {
      const marker = fromPath.indexOf('/portal/')
      if (marker >= 0) portalPrefix = fromPath.slice(0, marker + '/portal'.length)
    } else if (fromPath.startsWith('portal/')) portalPrefix = 'portal'
    if (!portalPrefix) return null
    stem = `${portalPrefix}/src/${moduleSpecifier.slice(2)}`
  } else if (moduleSpecifier.startsWith('.')) stem = posixPath(join(dirname(fromPath), moduleSpecifier))
  else return null
  stem = posixPath(stem)
  const options = [stem, ...['.vue', '.ts', '.tsx', '.js', '.jsx', '.mjs', '.json'].map(ext => `${stem}${ext}`), ...['index.vue', 'index.ts', 'index.tsx', 'index.js'].map(name => join(stem, name))]
  return options.find(option => byPath.has(posixPath(option))) || null
}

function normalizeCssValue(value) {
  return value.trim().toLowerCase().replace(/\s+/g, ' ').replace(/\s*([(),/:])\s*/g, '$1').replace(/\b0+(\.\d+)px\b/g, '$1px')
}

function normalizeDeclarations(declarations) {
  return declarations.map(({ prop, value, important = false }) => `${prop.toLowerCase()}:${normalizeCssValue(value)}${important ? '!important' : ''}`)
    .sort().join(';')
}

function isDesignLiteral(property, value) {
  return DESIGN_PROPERTY_RE.test(property) && /(?:^|\s)-?\d*\.?\d+(?:px|rem|em|ch|ex|%|vh|vw|vmin|vmax|dvh|dvw|pt|pc|cm|mm|in)\b/i.test(value)
}

function cssLocation(baseLine, node) {
  return { line: baseLine + node.source.start.line - 1, column: node.source.start.column }
}

function parseCss(css, path, baseLine, parser, signals, ruleRows, gaps) {
  if (!parser) {
    gaps.push({ path, reason: 'PostCSS parser unavailable; CSS receives lexical fallback only.' })
    scanCssFallback(css, path, baseLine, signals)
    return
  }
  let root
  try {
    root = parser.parse(css, { from: path })
  } catch (error) {
    gaps.push({ path, line: error.line ? baseLine + error.line - 1 : null, column: error.column || null, reason: `PostCSS could not parse source: ${error.reason || error.message}` })
    scanCssFallback(css, path, baseLine, signals)
    return
  }
  root.walkDecls(declaration => {
    const location = cssLocation(baseLine, declaration)
    const prop = declaration.prop
    const value = declaration.value
    if (prop.startsWith('--')) signals.push({ category: 'token-definition', path, ...location, token: prop, value: value.trim(), source: 'css-declaration' })
    for (const match of value.matchAll(/var\(\s*(--[\w-]+)/g)) signals.push({ category: 'token-reference', path, ...location, token: match[1], value: value.trim(), source: 'css-declaration' })
    if (declaration.important) signals.push({ category: 'important-declaration', path, ...location, property: prop, value: value.trim(), source: 'css-declaration' })
    if (prop.startsWith('--')) return
    for (const match of value.matchAll(COLOR_RE)) signals.push({ category: 'color-literal', path, ...location, property: prop, value: match[0], source: 'css-declaration' })
    if (isDesignLiteral(prop, value)) signals.push({ category: 'design-value-literal', path, ...location, property: prop, value: value.trim(), source: 'css-declaration' })
  })
  root.walkRules(rule => {
    const location = cssLocation(baseLine, rule)
    const selector = rule.selector.trim().replace(/\s+/g, ' ')
    const declarations = []
    rule.walkDecls(declaration => declarations.push({ prop: declaration.prop, value: declaration.value, important: declaration.important }))
    if (!declarations.length) return
    const normalized = normalizeDeclarations(declarations)
    const declarationSignature = [...new Set(declarations.map(declaration => declaration.prop.toLowerCase()))].sort().join(';')
    ruleRows.push({ path, ...location, selector, normalized, declarationSignature, declarations })
    if (/(?:^|[\s>+~])(?:body|html|button|input|select|textarea|a|table)(?:$|[\s.#:[>+~])/i.test(selector)) {
      signals.push({ category: 'global-or-native-selector', path, ...location, selector, source: 'css-rule' })
    }
    if (selector.includes(':deep(') || selector.includes('::v-deep') || selector.includes('>>>')) {
      signals.push({ category: 'deep-component-selector', path, ...location, selector, source: 'css-rule' })
    }
    const idCount = (selector.match(/#[\w-]+/g) || []).length
    const importantCount = declarations.filter(declaration => declaration.important).length
    if (idCount > 0 || importantCount > 0 || selector.includes(':not(') && (selector.match(/:/g) || []).length > 3) {
      signals.push({ category: 'specificity-escalation-candidate', path, ...location, selector, ids: idCount, importantDeclarations: importantCount, source: 'css-rule' })
    }
  })
  const seenSelectors = new Map()
  for (const row of ruleRows.filter(candidate => candidate.path === path)) {
    const previous = seenSelectors.get(row.selector)
    if (previous) signals.push({ category: 'repeated-selector-candidate', path, ...row, previous: { path: previous.path, line: previous.line }, source: 'css-rule' })
    else seenSelectors.set(row.selector, row)
  }
}

function scanCssFallback(css, path, baseLine, signals) {
  const ruleRegex = /([^{}]+)\{([^{}]*)\}/g
  for (const match of css.matchAll(ruleRegex)) {
    const declarations = []
    for (const chunk of match[2].split(';')) {
      const index = chunk.indexOf(':')
      if (index < 0) continue
      declarations.push({ prop: chunk.slice(0, index).trim(), value: chunk.slice(index + 1).replace(/!important\s*$/i, '').trim(), important: /!important\s*$/i.test(chunk) })
    }
    const loc = sourcePosition(css, match.index)
    for (const declaration of declarations) {
      if (declaration.prop.startsWith('--')) signals.push({ category: 'token-definition', path, line: baseLine + loc.line - 1, column: loc.column, token: declaration.prop, value: declaration.value, source: 'lexical-css-fallback' })
      if (!declaration.prop.startsWith('--') && !/var\(/i.test(declaration.value)) {
        for (const color of declaration.value.matchAll(COLOR_RE)) signals.push({ category: 'color-literal', path, line: baseLine + loc.line - 1, column: loc.column, property: declaration.prop, value: color[0], source: 'lexical-css-fallback' })
        if (isDesignLiteral(declaration.prop, declaration.value)) signals.push({ category: 'design-value-literal', path, line: baseLine + loc.line - 1, column: loc.column, property: declaration.prop, value: declaration.value, source: 'lexical-css-fallback' })
      }
      if (declaration.important) signals.push({ category: 'important-declaration', path, line: baseLine + loc.line - 1, column: loc.column, property: declaration.prop, value: declaration.value, source: 'lexical-css-fallback' })
    }
  }
}

function declarationGroups(ruleRows) {
  const exact = new Map()
  const near = new Map()
  for (const row of ruleRows) {
    const exactRows = exact.get(row.normalized) || []
    exactRows.push({ path: row.path, line: row.line, selector: row.selector })
    exact.set(row.normalized, exactRows)
    const key = row.declarationSignature
    if (key.split(';').length < 2) continue
    const nearRows = near.get(key) || []
    nearRows.push({ path: row.path, line: row.line, selector: row.selector, normalized: row.normalized })
    near.set(key, nearRows)
  }
  const toOccurrences = rows => [...rows].sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line)
  const exactGroups = [...exact.entries()].filter(([, rows]) => rows.length > 1).map(([normalizedDeclarations, rows]) => ({
    kind: 'exact-normalized-declarations', normalizedDeclarations, occurrences: toOccurrences(rows), isFinding: false,
  }))
  const nearGroups = [...near.entries()].filter(([, rows]) => rows.length > 1).map(([propertySet, rows]) => ({
    kind: 'same-property-set-value-variants', propertySet: propertySet.split(';'), occurrences: toOccurrences(rows), isFinding: false,
  }))
  return { exact: exactGroups, near: nearGroups }
}

function readClassValue(node) {
  const prop = node.props?.find(candidate => candidate.type === 6 && candidate.name === 'class')
  return prop?.value?.content || null
}

function markupSignals(markup, path, baseLine, baseOffset, parser, postcssParser, signals, gaps, context = 'markup', fullSource = markup) {
  const addForNode = (node, category, details = {}) => {
    const location = sourcePosition(fullSource, baseOffset + (node.loc?.start?.offset || 0))
    signals.push({ category, path, line: location.line, column: location.column, ...details, source: context })
  }
  if (parser) {
    let ast
    try { ast = parser.parse(markup, { comments: false }) } catch (error) {
      gaps.push({ path, reason: `Vue template parser could not parse ${context}: ${error.message}` })
    }
    if (ast) {
      const visit = node => {
        if (!node || typeof node !== 'object') return
        if (node.type === 1 && node.tag) {
          const tag = node.tag.toLowerCase()
          const classValue = readClassValue(node)
          if (NATIVE_CONTROLS.has(tag)) addForNode(node, 'native-control-markup', { tag: node.tag, className: classValue })
          if (classValue && SEMANTIC_CLASSES.test(classValue)) addForNode(node, 'semantic-pattern-class', { tag: node.tag, className: classValue })
          if (classValue && /(?:^|\s)(?:k-|railgrid-)/.test(classValue)) addForNode(node, 'design-system-class-markup', { tag: node.tag, className: classValue })
          for (const prop of node.props || []) {
            if (prop.type === 6 && prop.name === 'style' && prop.value) {
              const style = prop.value.content
              const location = sourcePosition(fullSource, baseOffset + prop.loc.start.offset)
              signals.push({ category: 'inline-style-markup', path, ...location, tag: node.tag, value: style, source: context })
              parseInlineStyle(style, path, location.line, location.column, signals, postcssParser)
            } else if (prop.type === 7 && prop.name === 'bind' && prop.arg?.content === 'style') {
              const location = sourcePosition(fullSource, baseOffset + prop.loc.start.offset)
              signals.push({ category: 'dynamic-inline-style-markup', path, ...location, tag: node.tag, expression: prop.exp?.content || null, source: context })
            }
          }
          if (node.tagType === 1) addForNode(node, 'component-tag-usage', { tag: node.tag })
        }
        for (const [key, value] of Object.entries(node)) {
          if (key === 'loc' || key === 'parent' || key === 'codegenNode') continue
          if (Array.isArray(value)) value.forEach(visit)
          else if (value && typeof value === 'object') visit(value)
        }
      }
      (ast.children || []).forEach(visit)
      return
    }
  }
  gaps.push({ path, reason: `${parser ? 'Vue template parser failed' : 'Vue template parser unavailable'}; markup received a lexical fallback for ${context}.` })
  for (const match of markup.matchAll(/<([a-z][\w-]*)\b([^>]*)>/gi)) {
    const tag = match[1]
    const attrs = match[2]
    const loc = sourcePosition(fullSource, baseOffset + match.index)
    const classValue = attrs.match(/\bclass\s*=\s*['"]([^'"]*)['"]/i)?.[1] || null
    if (NATIVE_CONTROLS.has(tag.toLowerCase())) signals.push({ category: 'native-control-markup', path, line: loc.line, column: loc.column, tag, className: classValue, source: `${context}-fallback` })
    if (classValue && SEMANTIC_CLASSES.test(classValue)) signals.push({ category: 'semantic-pattern-class', path, line: loc.line, column: loc.column, tag, className: classValue, source: `${context}-fallback` })
    const style = attrs.match(/\bstyle\s*=\s*['"]([^'"]*)['"]/i)?.[1]
    if (style) signals.push({ category: 'inline-style-markup', path, line: loc.line, column: loc.column, tag, value: style, source: `${context}-fallback` })
  }
}

function parseInlineStyle(style, path, baseLine, column, signals, postcss) {
  const content = `x { ${style} }`
  if (!postcss) {
    for (const match of style.matchAll(COLOR_RE)) signals.push({ category: 'color-literal', path, line: baseLine, column, property: 'inline-style', value: match[0], source: 'inline-style-fallback' })
    return
  }
  try {
    postcss.parse(content).walkDecls(declaration => {
      const value = declaration.value
      const location = { line: baseLine + declaration.source.start.line - 1, column: column + Math.max(0, declaration.source.start.column - 5) }
      if (!declaration.prop.startsWith('--') && !/var\(/i.test(value)) {
        for (const match of value.matchAll(COLOR_RE)) signals.push({ category: 'color-literal', path, ...location, property: declaration.prop, value: match[0], source: 'inline-style' })
        if (isDesignLiteral(declaration.prop, value)) signals.push({ category: 'design-value-literal', path, ...location, property: declaration.prop, value: value.trim(), source: 'inline-style' })
      }
      if (declaration.important) signals.push({ category: 'important-declaration', path, ...location, property: declaration.prop, value: value.trim(), source: 'inline-style' })
    })
  } catch { /* The markup signal itself remains useful. */ }
}

function componentTagKeys(name) {
  const kebab = name.replace(/([a-z0-9])([A-Z])/g, '$1-$2').toLowerCase()
  const pascal = kebab.split('-').filter(Boolean).map(part => part[0].toUpperCase() + part.slice(1)).join('')
  return new Set([name, kebab, pascal])
}

function nodeName(node, ts) {
  if (!node) return null
  if (ts.isIdentifier(node) || ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text
  return null
}

function importsFromSourceFile(sourceFile, ts, path, baseOffset = 0, fullText = '') {
  const imports = []
  for (const statement of sourceFile.statements) {
    if (!ts.isImportDeclaration(statement) && !ts.isExportDeclaration(statement)) continue
    const module = statement.moduleSpecifier
    if (!module || !(ts.isStringLiteral(module) || ts.isNoSubstitutionTemplateLiteral(module))) continue
    const specifier = module.text
    const loc = sourcePosition(fullText || sourceFile.text, baseOffset + statement.getStart(sourceFile))
    const bindings = []
    if (ts.isImportDeclaration(statement) && statement.importClause) {
      const clause = statement.importClause
      if (clause.name) bindings.push({ imported: 'default', local: clause.name.text })
      if (clause.namedBindings && ts.isNamespaceImport(clause.namedBindings)) bindings.push({ imported: '*', local: clause.namedBindings.name.text })
      else if (clause.namedBindings && ts.isNamedImports(clause.namedBindings)) {
        for (const binding of clause.namedBindings.elements) bindings.push({ imported: binding.propertyName?.text || binding.name.text, local: binding.name.text })
      }
    }
    imports.push({ path, line: loc.line, column: loc.column, specifier, bindings })
  }
  return imports
}

function componentDefinitions(path, extension, content, sfc, ts, scriptBlocks) {
  const definitions = []
  if (extension === '.vue') {
    const location = { line: 1, column: 1 }
    definitions.push({ path, name: path.split('/').at(-1).replace(/\.vue$/i, ''), kind: 'vue-sfc', ...location })
  }
  if (!ts) return definitions
  for (const block of scriptBlocks) {
    const sf = ts.createSourceFile(path, block.content, ts.ScriptTarget.Latest, true, block.kind)
    const visit = node => {
      if (ts.isFunctionDeclaration(node) && node.name && returnsJsx(node.body, ts)) {
        const location = sourcePosition(content, block.offset + node.getStart(sf))
        definitions.push({ path, name: node.name.text, kind: 'jsx-function', ...location })
      }
      if (ts.isVariableDeclaration(node) && node.name && ts.isIdentifier(node.name) && node.initializer && returnsJsx(node.initializer, ts)) {
        const location = sourcePosition(content, block.offset + node.getStart(sf))
        definitions.push({ path, name: node.name.text, kind: 'jsx-variable', ...location })
      }
      ts.forEachChild(node, visit)
    }
    visit(sf)
  }
  return definitions
}

function returnsJsx(node, ts) {
  if (!node) return false
  let found = false
  const visit = current => {
    if (ts.isJsxElement(current) || ts.isJsxSelfClosingElement(current) || ts.isJsxFragment(current)) found = true
    if (!found) ts.forEachChild(current, visit)
  }
  visit(node)
  return found
}

function collectRouteData(sourceFile, path, ts, baseOffset, fullText) {
  const routeDefinitions = []
  const manualParsers = []
  const routeLinkValues = []
  const hasVueRouter = sourceFile.text.includes('createRouter') || sourceFile.text.includes('RouteRecordRaw')
  const functionStack = []
  const walk = (node, routeContext = false) => {
    let nextContext = routeContext
    if (ts.isPropertyAssignment(node) && nodeName(node.name, ts) === 'routes') nextContext = true
    if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && /routes/i.test(node.name.text)) nextContext = true
    if (ts.isCallExpression(node) && /(?:createRouter|addRoute|addRoutes)$/.test(node.expression.getText(sourceFile))) nextContext = true
    if (ts.isFunctionDeclaration(node) && node.name) functionStack.push(node.name.text)
    else if (ts.isMethodDeclaration(node) && node.name) functionStack.push(node.name.getText(sourceFile))
    else if (ts.isArrowFunction(node) || ts.isFunctionExpression(node)) functionStack.push('')

    if (ts.isPropertyAssignment(node) && nodeName(node.name, ts) === 'path' && (nextContext || hasVueRouter)) {
      const parsedPattern = routePattern(node.initializer, ts, sourceFile)
      const location = sourcePosition(fullText, baseOffset + node.getStart(sourceFile))
      routeDefinitions.push({ path, ...location, pattern: parsedPattern.value, expression: parsedPattern.expression, routeKind: 'vue-router-config', dynamic: parsedPattern.dynamic, isFinding: false })
    }
    if (ts.isFunctionDeclaration(node) || ts.isMethodDeclaration(node) || ts.isArrowFunction(node) || ts.isFunctionExpression(node)) {
      const name = functionStack.at(-1) || ''
      if (/^(?:parse\w*(?:Route|Path|Hash|SubPath)|routeForSubPath|questionRunForSubPath|restoreRouteFromHash|(?:read|write|sync)Hash|normalizeSubPath)$/i.test(name)) {
        const literals = []
        const regexes = []
        const collect = child => {
          if (ts.isStringLiteral(child) || ts.isNoSubstitutionTemplateLiteral(child)) {
            const location = sourcePosition(fullText, baseOffset + child.getStart(sourceFile))
            if (child.text.length < 96 && child.text.trim() && !child.text.includes(' ')) literals.push({ value: child.text, ...location })
          } else if (ts.isRegularExpressionLiteral(child)) {
            const location = sourcePosition(fullText, baseOffset + child.getStart(sourceFile))
            regexes.push({ pattern: child.text, ...location })
          }
          ts.forEachChild(child, collect)
        }
        if (node.body) collect(node.body)
        const subPathDriven = /subpath|hash/i.test(name) || /\.subPath\b|\.hash\b/i.test(node.getText(sourceFile))
        if (subPathDriven) {
          const location = sourcePosition(fullText, baseOffset + node.getStart(sourceFile))
          const uniqueLiterals = [...new Map(literals.map(item => [item.value, item])).values()].sort((a, b) => a.value.localeCompare(b.value))
          manualParsers.push({ path, ...location, function: name || '<anonymous>', recognizedLiterals: uniqueLiterals, regexes, dynamicSegmentsUnresolved: true, reason: 'Manual dispatch parses route state in application code; dynamic resource identifiers or fallthrough behavior cannot be enumerated as concrete URLs from static literals alone.', isFinding: false })
        }
      }
    }

    if (ts.isJsxAttribute(node) && (node.name.getText(sourceFile) === 'to' || node.name.getText(sourceFile) === 'href') && node.initializer && ts.isStringLiteral(node.initializer)) {
      const value = node.initializer.text
      if (value.startsWith('/') || value.startsWith('#')) {
        const location = sourcePosition(fullText, baseOffset + node.getStart(sourceFile))
        routeLinkValues.push({ path, ...location, value, kind: 'jsx-link-reference' })
      }
    }

    ts.forEachChild(node, child => walk(child, nextContext))
    if (ts.isFunctionDeclaration(node) || ts.isMethodDeclaration(node) || ts.isArrowFunction(node) || ts.isFunctionExpression(node)) functionStack.pop()
  }
  walk(sourceFile)
  return { routeDefinitions, manualParsers, routeLinkValues }
}

function routePattern(node, ts, sourceFile) {
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return { value: node.text, expression: node.text, dynamic: false }
  if (ts.isTemplateExpression(node)) {
    const value = node.head.text + node.templateSpans.map(span => `:${span.expression.getText(sourceFile)}${span.literal.text}`).join('')
    return { value, expression: node.getText(sourceFile), dynamic: true }
  }
  if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.PlusToken) {
    const left = routePattern(node.left, ts, sourceFile)
    const right = routePattern(node.right, ts, sourceFile)
    const leftValue = left.value ?? '{dynamic}'
    const rightValue = right.value ?? '{dynamic}'
    return { value: leftValue + rightValue, expression: node.getText(sourceFile), dynamic: true }
  }
  return { value: null, expression: node.getText(sourceFile), dynamic: true }
}

function templateFragmentsFromScript(sourceFile, ts, baseOffset, fullText) {
  const fragments = []
  const visit = node => {
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      if (/<(?:!doctype|html|head|body|main|section|div|form|button|script|style)\b/i.test(node.text)) {
        fragments.push({ text: node.text, offset: baseOffset + node.getStart(sourceFile), source: 'script-html-string' })
      }
    } else if (ts.isTemplateExpression(node)) {
      const raw = node.getText(sourceFile).slice(1, -1)
      if (/<(?:!doctype|html|head|body|main|section|div|form|button|script|style)\b/i.test(raw)) {
        fragments.push({ text: raw, offset: baseOffset + node.getStart(sourceFile) + 1, source: 'script-html-template' })
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(sourceFile)
  return fragments
}

function inlineGoFragments(content) {
  const fragments = []
  for (const match of content.matchAll(/`([^`]*)`/gs)) {
    if (/<(?:!doctype|html|head|body|main|section|div|form|button|script|style)\b/i.test(match[1])) {
      fragments.push({ text: match[1], offset: match.index + 1, source: 'go-raw-html-template' })
    }
  }
  return fragments
}

// Keep the source offset when HTML templates contain Go directives that are not valid Vue markup.
function embeddedStyles(markup, path, offset, source, parsers, aggregate) {
  const rawElements = /<!--[\s\S]*?-->|<(script|style)\b[^>]*>([\s\S]*?)<\/\1\s*>/gi
  for (const match of markup.matchAll(rawElements)) {
    if (match[1]?.toLowerCase() !== 'style') continue
    const start = offset + match.index + match[0].indexOf('>') + 1
    parseCss(match[2], path, sourcePosition(source, start).line, parsers.postcss, aggregate.styleSignals, aggregate.ruleRows, aggregate.gaps)
  }
}

function extractStyleBlocks(content, path, sfc, signals, ruleRows, parsers, gaps) {
  if (sfc?.descriptor?.styles) {
    for (const style of sfc.descriptor.styles) {
      parseCss(style.content, path, style.loc.start.line, parsers.postcss, signals, ruleRows, gaps)
    }
  }
  if (['.css', '.scss', '.sass', '.less'].includes(extname(path).toLowerCase())) parseCss(content, path, 1, parsers.postcss, signals, ruleRows, gaps)
}

function parseSfc(content, path, parsers, gaps) {
  if (!parsers.vueSfc) {
    gaps.push({ path, reason: '@vue/compiler-sfc unavailable; Vue component/template/style extraction uses lexical fallback.' })
    return null
  }
  try {
    const parsed = parsers.vueSfc.parse(content, { filename: path })
    for (const error of parsed.errors || []) gaps.push({ path, reason: `Vue SFC parse diagnostic: ${typeof error === 'string' ? error : error.message}` })
    return parsed
  } catch (error) {
    gaps.push({ path, reason: `Vue SFC parser failed: ${error.message}` })
    return null
  }
}

function scriptBlocksFor(path, content, sfc, parsers, gaps) {
  const extension = extname(path).toLowerCase()
  if (extension === '.vue') {
    if (!sfc) {
      const blocks = []
      const scriptRe = /<script\b[^>]*>([\s\S]*?)<\/script>/gi
      for (const match of content.matchAll(scriptRe)) blocks.push({ content: match[1], offset: match.index + match[0].indexOf(match[1]), kind: 3 })
      return blocks
    }
    return [sfc.descriptor.script, sfc.descriptor.scriptSetup].filter(Boolean).map(block => ({ content: block.content, offset: block.loc.start.offset, kind: 3 }))
  }
  if (!['.ts', '.tsx', '.js', '.jsx', '.mjs', '.cjs'].includes(extension)) return []
  return [{ content, offset: 0, kind: extension.endsWith('x') ? 4 : extension === '.js' || extension === '.mjs' || extension === '.cjs' ? 1 : 3 }]
}

function parseSourceFile(script, path, parsers, gaps) {
  if (!parsers.typescript) {
    gaps.push({ path, reason: 'TypeScript parser unavailable; component/import/route AST analysis falls back to source patterns.' })
    return null
  }
  const extension = extname(path).toLowerCase()
  const kind = extension.endsWith('x') ? parsers.typescript.ScriptKind.TSX
    : ['.js', '.jsx', '.mjs', '.cjs'].includes(extension) ? (extension === '.jsx' ? parsers.typescript.ScriptKind.JSX : parsers.typescript.ScriptKind.JS)
      : parsers.typescript.ScriptKind.TS
  const sourceFile = parsers.typescript.createSourceFile(path, script.content, parsers.typescript.ScriptTarget.Latest, true, kind)
  if (sourceFile.parseDiagnostics?.length) {
    for (const diagnostic of sourceFile.parseDiagnostics) {
      gaps.push({ path, reason: `TypeScript parser diagnostic at local offset ${diagnostic.start ?? 0}; AST extraction may be incomplete.` })
    }
  }
  return sourceFile
}

function findMirror(path, content, root) {
  const candidates = []
  if (path === 'portal/src/assets/railgrid-ui.css') candidates.push('provider-sdk/portalkit/railgrid-ui.css')
  if (path.includes('/src/portalkit/')) {
    const name = path.split('/').at(-1)
    candidates.push(`provider-sdk/portalkit/${name}`, `provider-sdk/portalkit-vue/${name}`)
  }
  if (path.includes('/src/agentkit/')) {
    const name = path.split('/').at(-1)
    candidates.push(`provider-sdk/agentkit/${name}`, `provider-sdk/agentkit-vue/${name}`)
  }
  const existing = candidates.filter(candidate => existsSync(resolve(root, candidate)))
  for (const canonical of existing) {
    const source = readFileSync(resolve(root, canonical), 'utf8')
    if (content === source) return { canonical, status: 'byte-identical' }
    const transformed = source.replaceAll('../portalkit-vue/', '../portalkit/')
    if (content === transformed) return { canonical, status: 'manifest-transformed-identical' }
  }
  const preferred = path.includes('/src/agentkit/')
    ? existing.find(candidate => candidate.startsWith('provider-sdk/agentkit-vue/')) || existing[0]
    : path.includes('/src/portalkit/') && !path.startsWith('providers/quickstart/')
      ? existing.find(candidate => candidate.startsWith('provider-sdk/portalkit-vue/')) || existing[0]
      : existing[0]
  if (!preferred) return null
  const source = readFileSync(resolve(root, preferred), 'utf8')
  return { canonical: preferred, status: 'divergent-copy', canonicalSha256: sha256(source) }
}

function astAndMarkupForFile(record, content, sfc, parsers, byPath, mirrorTargets, aggregate) {
  const path = record.path
  const extension = record.extension
  const blocks = scriptBlocksFor(path, content, sfc, parsers, aggregate.gaps)
  const importsByLocal = new Map()
  const blockSources = []
  for (const block of blocks) {
    const sourceFile = parseSourceFile(block, path, parsers, aggregate.gaps)
    if (!sourceFile) continue
    blockSources.push({ block, sourceFile })
    for (const imported of importsFromSourceFile(sourceFile, parsers.typescript, path, block.offset, content)) {
      const resolvedTarget = resolveImportTarget(path, imported.specifier, byPath)
      const targetPath = resolvedTarget ? mirrorTargets.get(resolvedTarget) || resolvedTarget : null
      const importRecord = { ...imported, targetPath }
      aggregate.imports.push(importRecord)
      for (const binding of imported.bindings) {
        importsByLocal.set(binding.local, { ...binding, sourcePath: path, targetPath, line: imported.line })
      }
    }
    const routes = collectRouteData(sourceFile, path, parsers.typescript, block.offset, content)
    aggregate.routes.definitions.push(...routes.routeDefinitions.map(route => ({ ...route, sourceCategory: record.category })))
    aggregate.routes.manualParsers.push(...routes.manualParsers.map(route => ({ ...route, sourceCategory: record.category })))
    aggregate.routes.links.push(...routes.routeLinkValues.map(route => ({ ...route, sourceCategory: record.category })))
    aggregate.components.push(...componentDefinitions(path, extension, content, sfc, parsers.typescript, [{ ...block, kind: sourceFile.scriptKind }]).filter(definition => definition.kind !== 'vue-sfc').map(definition => ({ ...definition, sourceCategory: record.category })))
    for (const fragment of templateFragmentsFromScript(sourceFile, parsers.typescript, block.offset, content)) {
      const base = sourcePosition(content, fragment.offset)
      markupSignals(fragment.text, path, base.line, fragment.offset, parsers.vueDom, parsers.postcss, aggregate.styleSignals, aggregate.gaps, fragment.source, content)
    }
  }
  const definitions = componentDefinitions(path, extension, content, sfc, parsers.typescript, [])
  aggregate.components.push(...definitions.filter(definition => definition.kind === 'vue-sfc').map(definition => ({ ...definition, sourceCategory: record.category })))

  if (sfc?.descriptor?.template) {
    const template = sfc.descriptor.template
    const baseLine = template.loc.start.line
    markupSignals(template.content, path, baseLine, template.loc.start.offset, parsers.vueDom, parsers.postcss, aggregate.styleSignals, aggregate.gaps, 'vue-template', content)
    if (parsers.vueDom) {
      try {
        const ast = parsers.vueDom.parse(template.content, { comments: false })
        const visit = node => {
          if (node?.type === 1 && node.tagType === 1) {
            const tag = node.tag
            const names = [...importsByLocal.keys()].find(local => [...componentTagKeys(local)].includes(tag))
            const imported = names ? importsByLocal.get(names) : null
            if (imported?.targetPath) aggregate.componentUsages.push({ path, ...sourcePosition(content, template.loc.start.offset + node.loc.start.offset), tag, targetPath: imported.targetPath, importedAs: names, kind: 'vue-template-import-use', sourceCategory: record.category })
          }
          if (node?.type === 1 && ['router-link', 'RouterLink', 'a'].includes(node.tag)) {
            for (const prop of node.props || []) {
              const name = prop.type === 6 ? prop.name : prop.type === 7 && prop.arg ? prop.arg.content : ''
              if (!['to', 'href'].includes(name)) continue
              const value = prop.type === 6 ? prop.value?.content : prop.exp?.content
              if (!value) continue
              if (prop.type === 6 && !value.startsWith('/') && !value.startsWith('#')) continue
              aggregate.routes.links.push({ path, ...sourcePosition(content, template.loc.start.offset + prop.loc.start.offset), value, kind: prop.type === 6 ? 'vue-static-link-reference' : 'vue-dynamic-link-reference', dynamic: prop.type === 7, sourceCategory: record.category })
            }
          }
          for (const [key, value] of Object.entries(node || {})) {
            if (key === 'loc' || key === 'parent' || key === 'codegenNode') continue
            if (Array.isArray(value)) value.forEach(visit)
            else if (value && typeof value === 'object') visit(value)
          }
        }
        (ast.children || []).forEach(visit)
      } catch { /* markupSignals reports its parse gap. */ }
    }
  }
  for (const { block, sourceFile } of blockSources) {
    const ts = parsers.typescript
    if (!ts) continue
    const visitJsx = node => {
      if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
        const name = node.tagName.getText(sourceFile)
        const imported = importsByLocal.get(name)
        if (imported?.targetPath) {
          const location = sourcePosition(content, block.offset + node.getStart(sourceFile))
          aggregate.componentUsages.push({ path, ...location, tag: name, targetPath: imported.targetPath, importedAs: name, kind: 'jsx-import-use', sourceCategory: record.category })
        }
      }
      ts.forEachChild(node, visitJsx)
    }
    visitJsx(sourceFile)
  }
}

function addManualRouteSignals(record, content, parsers, aggregate) {
  if (record.extension === '.vue') return
  if (!['application-frontend', 'frontend-example', 'canonical-design-system-source', 'dex-login-frontend', 'inline-go-template'].includes(record.category)) return
  const executable = content.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
  if (!/(?:\.subPath\b|location\.hash|routeForSubPath|parse\w*(?:Route|Path|Hash))/i.test(executable)) return
  if (!aggregate.routes.manualParsers.some(route => route.path === record.path)) {
    const matches = [...content.matchAll(/(?:subPath|parse[A-Za-z]*(?:Route|Path|Hash)|routeForSubPath)/g)].map(match => ({
      line: sourcePosition(content, match.index).line,
      column: sourcePosition(content, match.index).column,
      signal: match[0],
    }))
    if (matches.length) aggregate.routes.manualParsers.push({ path: record.path, ...matches[0], function: '<manual-dispatch>', recognizedLiterals: [], regexes: [], dynamicSegmentsUnresolved: true, reason: 'Source references provider-relative subPath or location hash; static route AST parser did not identify a concrete parser function.', isFinding: false, sourceCategory: record.category })
  }
}

export function analyzeCssSource(css, { path = '<fixture.css>', baseLine = 1, parser = resolveParsers().postcss } = {}) {
  const styleSignals = []
  const ruleRows = []
  const gaps = []
  parseCss(css, path, baseLine, parser, styleSignals, ruleRows, gaps)
  return { evidenceStatus: 'static signals only; not confirmed findings', styleSignals: markSignals(styleSignals), normalizedDeclarationGroups: declarationGroups(ruleRows), ruleCount: ruleRows.length, gaps }
}

export function analyzeMarkupSource(markup, { path = '<fixture.html>', baseLine = 1, parsers = resolveParsers(), context = 'fixture-markup' } = {}) {
  const styleSignals = []
  const gaps = []
  const ruleRows = []
  embeddedStyles(markup, path, 0, markup, parsers, { styleSignals, ruleRows, gaps })
  markupSignals(markup, path, baseLine, 0, parsers.vueDom, parsers.postcss, styleSignals, gaps, context, markup)
  return { evidenceStatus: 'static signals only; not confirmed findings', styleSignals: markSignals(styleSignals), ruleCount: ruleRows.length, gaps }
}

export function analyzeModuleSource(source, { path = 'fixture.tsx', knownPaths = [], parsers = resolveParsers() } = {}) {
  const aggregate = {
    components: [], componentUsages: [], imports: [], routes: { definitions: [], manualParsers: [], links: [] },
    styleSignals: [], ruleRows: [], gaps: [],
  }
  const record = { path, extension: extname(path).toLowerCase(), category: 'application-frontend' }
  const availablePaths = new Set([path, ...knownPaths])
  const sfc = record.extension === '.vue' ? parseSfc(source, path, parsers, aggregate.gaps) : null
  astAndMarkupForFile(record, source, sfc, parsers, availablePaths, new Map(), aggregate)
  return {
    imports: aggregate.imports,
    components: aggregate.components,
    componentUsages: aggregate.componentUsages,
    routes: aggregate.routes,
    styleSignals: aggregate.styleSignals,
    gaps: aggregate.gaps,
  }
}

function markSignals(signals) {
  return signals.map(signal => ({ ...signal, classification: 'signal-only', isFinding: false }))
}

function summarize(records, aggregate, parserVersions) {
  const included = records.filter(record => record.included)
  const analyzed = included.filter(record => record.analysisStatus === 'analyzed')
  const excluded = records.filter(record => !record.included)
  const mirror = included.filter(record => record.mirror)
  const uniqueGaps = [...new Map(aggregate.gaps.map(gap => [`${gap.path}\0${gap.line ?? ''}\0${gap.column ?? ''}\0${gap.reason}`, gap])).values()]
    .sort((a, b) => a.path.localeCompare(b.path) || (a.line || 0) - (b.line || 0) || a.reason.localeCompare(b.reason))
  const styleSignals = markSignals(aggregate.styleSignals)
  const componentByPath = new Map()
  for (const component of aggregate.components) {
    const list = componentByPath.get(component.path) || []
    list.push(component)
    componentByPath.set(component.path, list)
  }
  const usesByPath = new Map()
  for (const usage of aggregate.componentUsages) {
    const list = usesByPath.get(usage.targetPath) || []
    list.push(usage)
    usesByPath.set(usage.targetPath, list)
  }
  const components = [...aggregate.components].sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line || a.name.localeCompare(b.name)).map(component => ({
    ...component,
    usages: [...(usesByPath.get(component.path) || [])].sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line),
  }))
  const countByCategory = values => Object.fromEntries([...values.reduce((map, value) => map.set(value, (map.get(value) || 0) + 1), new Map())].sort(([a], [b]) => a.localeCompare(b)))
  const productionRouteSources = new Set(['application-frontend', 'frontend-example', 'canonical-design-system-source', 'dex-login-frontend', 'inline-go-template'])
  const productionRoutes = aggregate.routes.definitions.filter(route => productionRouteSources.has(route.sourceCategory))
  const productionManualParsers = aggregate.routes.manualParsers.filter(route => productionRouteSources.has(route.sourceCategory))
  return {
    inventory: {
      schemaVersion: 1,
      evidenceStatus: 'mechanically analyzed; not manually reviewed',
      scope: {
        discoveredCandidateFiles: records.length,
        includedFiles: included.length,
        analyzedFiles: analyzed.length,
        skippedMirroredCopies: mirror.length,
        excludedFiles: excluded.length,
        countsByCategory: countByCategory(records.map(record => record.category)),
        includedCountsByCategory: countByCategory(included.map(record => record.category)),
        excludedCountsByCategory: countByCategory(excluded.map(record => record.category)),
        fileTypes: ['Vue SFC', 'TypeScript/JavaScript/JSX', 'CSS/SCSS/Sass/Less', 'HTML/MDX', 'frontend JSON data/config', 'reference Markdown/text', 'inventory-only YAML/module metadata', 'SVG/raster/font asset', 'detected inline Go HTML templates'],
        parserVersions,
      },
      files: records,
      components,
      imports: aggregate.imports.sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line || a.specifier.localeCompare(b.specifier)),
      routes: {
        definitions: aggregate.routes.definitions.sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line || String(a.pattern ?? '').localeCompare(String(b.pattern ?? ''))),
        manualParsers: aggregate.routes.manualParsers.sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line || a.function.localeCompare(b.function)),
        links: aggregate.routes.links.sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line),
        production: {
          definitions: productionRoutes.length,
          manualParsers: productionManualParsers.length,
          unresolvedDynamicRoutes: productionManualParsers.filter(parser => parser.dynamicSegmentsUnresolved).map(parser => ({ path: parser.path, line: parser.line, function: parser.function, reason: parser.reason })),
        },
        unresolvedDynamicRoutes: aggregate.routes.manualParsers.filter(parser => parser.dynamicSegmentsUnresolved).map(parser => ({ path: parser.path, line: parser.line, function: parser.function, sourceCategory: parser.sourceCategory, reason: parser.reason })),
      },
      analysis: {
        componentDefinitions: components.length,
        componentImportUsageEdges: aggregate.componentUsages.length,
        sourceImports: aggregate.imports.length,
        routeDefinitions: aggregate.routes.definitions.length,
        manualRouteParsers: aggregate.routes.manualParsers.length,
        routeLinkReferences: aggregate.routes.links.length,
        cssRuleCount: aggregate.ruleRows.length,
        signals: aggregate.styleSignals.length,
        parserGaps: uniqueGaps.length,
      },
      gaps: uniqueGaps,
    },
    analysis: {
      schemaVersion: 1,
      evidenceStatus: 'static signals only; each occurrence is a review candidate, not a confirmed finding',
      styleSignals: styleSignals.sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line || a.column - b.column || a.category.localeCompare(b.category)),
      normalizedDeclarationGroups: declarationGroups(aggregate.ruleRows),
      sourceRuleCount: aggregate.ruleRows.length,
      tokenDefinitions: styleSignals.filter(signal => signal.category === 'token-definition'),
      tokenReferences: styleSignals.filter(signal => signal.category === 'token-reference'),
      coverage: {
        candidateFiles: records.length,
        includedFiles: included.length,
        sourceFilesMechanicallyAnalyzed: analyzed.length,
        exactOrTransformedSyncedCopiesCompared: mirror.filter(record => record.mirror.status !== 'divergent-copy').length,
        divergentSyncedCopies: mirror.filter(record => record.mirror.status === 'divergent-copy').map(record => ({ path: record.path, canonical: record.mirror.canonical, workingSha256: record.sha256, canonicalSha256: record.mirror.canonicalSha256 })),
        excludedFilesByCategory: countByCategory(excluded.map(record => record.category)),
        analyzedFilesByCategory: countByCategory(analyzed.map(record => record.category)),
        parserGaps: uniqueGaps.length,
      },
      gaps: uniqueGaps,
    },
  }
}

export function buildAudit(root = DEFAULT_ROOT, options = {}) {
  const absoluteRoot = resolve(root)
  const git = getTrackedAndUntracked(absoluteRoot)
  const parsers = resolveParsers(absoluteRoot)
  const gaps = []
  const records = []
  const aggregate = {
    components: [], componentUsages: [], imports: [], routes: { definitions: [], manualParsers: [], links: [] },
    styleSignals: [], ruleRows: [], gaps,
  }
  const sourceContent = new Map()
  const candidatePaths = []
  for (const path of git.paths) {
    const absolutePath = resolve(absoluteRoot, path)
    if (!existsSync(absolutePath)) continue
    const ext = extname(path).toLowerCase()
    const extension = candidateExtension(path)
    const inInventoryRoot = isInventoryRoot(path)
    if (!extension && ext !== '.go' && !inInventoryRoot) continue
    if (!statSync(absolutePath).isFile()) continue
    const raw = readFileSync(absolutePath)
    const content = ASSET_EXTENSIONS.has(extension) ? '' : raw.toString('utf8')
    const inlineGo = ext === '.go' && isLikelyInlineGoFrontend(path, content)
    const statusPage = ext === '.go' && statusPageConsumer(path, content)
    if (extension || inlineGo || statusPage || inInventoryRoot) candidatePaths.push({ path, extension: inlineGo || statusPage ? '.go' : extension || ext || '<none>', content, raw })
  }
  const byPath = new Set(candidatePaths.map(candidate => candidate.path))
  for (const candidate of candidatePaths) {
    const { path, extension, content, raw } = candidate
    const scope = classifyInventoryFile(path, extension, content)
    const mirror = scope.included ? findMirror(path, content, absoluteRoot) : null
    const status = git.untracked.has(path) ? 'untracked' : git.changed.has(path) ? 'modified' : 'tracked'
    const record = {
      path,
      extension,
      category: scope.category,
      included: scope.included,
      reason: scope.reason,
      workingTreeStatus: status,
      sha256: sha256(raw),
      analysisStatus: !scope.included ? 'excluded' : mirror ? 'compared-as-synced-copy' : ASSET_EXTENSIONS.has(extension) ? 'hash-inventory-only-asset' : scope.analysisStatus || 'pending',
    }
    if (extension === '.go' && statusPageConsumer(path, content)) {
      record.statusPageCalls = [...content.matchAll(/\bstatuspage\.Render\s*\(/g)].map(match => sourcePosition(content, match.index))
    }
    if (mirror) record.mirror = mirror
    records.push(record)
    sourceContent.set(path, content)
  }
  records.sort((a, b) => a.path.localeCompare(b.path))

  const filesByPath = new Map(records.map(record => [record.path, record]))
  const mirrorTargets = new Map(records.filter(record => record.mirror).map(record => [record.path, record.mirror.canonical]))
  for (const record of records) {
    if (!record.included || record.mirror || ASSET_EXTENSIONS.has(record.extension)) continue
    const content = sourceContent.get(record.path)
    if (record.path.endsWith('.sh') || record.path === 'hack/verify-ui-conformance.mjs') {
      record.analysisStatus = 'inventory-only-configuration-or-tool'
      continue
    }
    if (record.extension === '.json') {
      try {
        record.jsonShape = summarizeJsonInventory(content)
        record.analysisStatus = 'parsed-json-inventory-only'
      } catch (error) {
        gaps.push({ path: record.path, reason: `JSON parser could not parse frontend data/config: ${error.message}` })
        record.analysisStatus = 'json-parse-gap'
      }
      continue
    }
    if (record.analysisStatus !== 'pending') {
      continue
    }
    const sfc = record.extension === '.vue' ? parseSfc(content, record.path, parsers, gaps) : null
    if (record.extension === '.vue' && !sfc) {
      // Keep an explicit component record in the inventory when the SFC parser is absent.
      aggregate.components.push({ path: record.path, name: record.path.split('/').at(-1).replace(/\.vue$/i, ''), kind: 'vue-sfc-unparsed', line: 1, column: 1 })
    }
    extractStyleBlocks(content, record.path, sfc, aggregate.styleSignals, aggregate.ruleRows, parsers, gaps)
    if (sfc) {
      for (const error of sfc.errors || []) gaps.push({ path: record.path, reason: `Vue SFC parser diagnostic: ${error.message || String(error)}` })
    }
    astAndMarkupForFile(record, content, sfc, parsers, byPath, mirrorTargets, aggregate)
    if (sfc?.descriptor?.template && !parsers.vueDom) gaps.push({ path: record.path, reason: 'Vue compiler DOM parser unavailable; template components and native controls use lexical fallback only.' })
    if (record.extension === '.html' || record.extension === '.htm' || record.extension === '.mdx') {
      embeddedStyles(content, record.path, 0, content, parsers, aggregate)
      markupSignals(content, record.path, 1, 0, parsers.vueDom, parsers.postcss, aggregate.styleSignals, gaps, 'html-source', content)
    }
    if (record.extension === '.go') {
      for (const fragment of inlineGoFragments(content)) {
        const loc = sourcePosition(content, fragment.offset)
        markupSignals(fragment.text, record.path, loc.line, fragment.offset, parsers.vueDom, parsers.postcss, aggregate.styleSignals, gaps, fragment.source, content)
        embeddedStyles(fragment.text, record.path, fragment.offset, content, parsers, aggregate)
      }
    }
    addManualRouteSignals(record, content, parsers, aggregate)
    record.analysisStatus = 'analyzed'
  }
  for (const usage of aggregate.componentUsages) {
    if (!filesByPath.has(usage.targetPath)) usage.unresolvedTarget = true
  }
  const result = summarize(records, aggregate, parsers.versions)
  if (options.outputDir) result.outputDir = options.outputDir
  return result
}

function parseArgs(args) {
  const options = { root: DEFAULT_ROOT, outputDir: process.env.AUDIT_DIR, write: true }
  for (let i = 0; i < args.length; i += 1) {
    if (args[i] === '--root') options.root = resolve(args[++i])
    else if (args[i] === '--output-dir') options.outputDir = args[++i]
    else if (args[i] === '--stdout') options.write = false
    else if (args[i] === '--help' || args[i] === '-h') options.help = true
    else throw new Error(`Unknown argument: ${args[i]}`)
  }
  return options
}

function main() {
  try {
    const options = parseArgs(process.argv.slice(2))
    if (options.help) {
      console.log('Usage: node hack/audit-design-system.mjs [--root PATH] [--output-dir PATH | --stdout] (or set AUDIT_DIR)')
      return
    }
    if (options.write) options.outputDir = auditDirectory(options.outputDir && resolve(options.root, options.outputDir))
    const result = buildAudit(options.root, { outputDir: options.outputDir })
    if (options.write) {
      const outputDir = resolve(options.root, options.outputDir)
      const inventoryPath = join(outputDir, 'inventory.json')
      const analysisPath = join(outputDir, 'analysis.json')
      // Create audit-only output directory; no production frontend paths are written.
      mkdirSync(outputDir, { recursive: true })
      writeFileSync(inventoryPath, `${JSON.stringify(result.inventory, null, 2)}\n`)
      writeFileSync(analysisPath, `${JSON.stringify(result.analysis, null, 2)}\n`)
      console.log(`Inventory: ${result.inventory.scope.includedFiles} in scope / ${result.inventory.scope.discoveredCandidateFiles} candidates; ${result.inventory.analysis.componentDefinitions} components; ${result.inventory.analysis.routeDefinitions} route declarations; ${result.inventory.analysis.parserGaps} parser gaps.`)
      console.log(`Wrote ${posixPath(relative(options.root, inventoryPath))} and ${posixPath(relative(options.root, analysisPath))}.`)
    } else {
      process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
    }
  } catch (error) {
    console.error(error.stack || error.message)
    process.exitCode = 1
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
