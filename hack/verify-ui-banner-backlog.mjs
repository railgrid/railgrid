#!/usr/bin/env node
// Prevent new native Vue markup from using the retired provider-local notification
// recipes. Existing source occurrences are an explicit, path/tag/count backlog.

import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = resolve(HERE, '..')
const BASELINE_PATH = 'hack/ui-banner-backlog.json'
const RULE = 'no-new-native-vue-feedback-recipe'
export const LEGACY_CLASSES = Object.freeze(['banner', 'read-error', 'stale-banner', 'mutation-error', 'connection-detail__deleting', 'repo-detail__deleting', 'kuery-inline-error', 'kuery-warning', 'k-error', 'k-stale'])
const NATIVE_TAGS = new Set([
  'a', 'abbr', 'address', 'area', 'article', 'aside', 'audio', 'b', 'base', 'bdi', 'bdo', 'blockquote', 'body', 'br', 'button',
  'canvas', 'caption', 'cite', 'code', 'col', 'colgroup', 'data', 'datalist', 'dd', 'del', 'details', 'dfn', 'dialog', 'div', 'dl', 'dt',
  'em', 'embed', 'fieldset', 'figcaption', 'figure', 'footer', 'form', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'head', 'header', 'hr', 'i',
  'iframe', 'img', 'input', 'ins', 'kbd', 'label', 'legend', 'li', 'main', 'map', 'mark', 'menu', 'meter', 'nav', 'noscript', 'object',
  'ol', 'optgroup', 'option', 'output', 'p', 'picture', 'pre', 'progress', 'q', 'rp', 'rt', 'ruby', 's', 'samp', 'section', 'select',
  'small', 'source', 'span', 'strong', 'sub', 'summary', 'sup', 'table', 'tbody', 'td', 'textarea', 'tfoot', 'th', 'thead',
  'time', 'tr', 'track', 'u', 'ul', 'var', 'video', 'wbr',
])
const MIRRORED_SEGMENTS = new Set(['portalkit', 'portalkit-vue', 'agentkit', 'agentkit-vue'])

function isProductVue(path) {
  if (!path.endsWith('.vue')) return false
  if (/^(?:portal\/src|providers\/[^/]+\/portal\/src)\//.test(path)) {
    const sourceRelative = path.split('/src/')[1] || ''
    if (sourceRelative.split('/').some(segment => MIRRORED_SEGMENTS.has(segment))) return false
    if (/(?:^|\/)(?:__tests__|fixtures|stories)(?:\/|$)/.test(sourceRelative) || /\.(?:test|spec)\.vue$/.test(path)) return false
    return true
  }
  return /^provider-sdk\/(?:portalkit-vue|agentkit-vue)\//.test(path)
}

function listVueSources(repoRoot) {
  const tracked = execFileSync('git', ['ls-files', '-z'], { cwd: repoRoot, encoding: 'buffer', stdio: ['ignore', 'pipe', 'ignore'] }).toString('utf8').split('\0').filter(Boolean)
  const untracked = execFileSync('git', ['ls-files', '--others', '--exclude-standard', '-z'], { cwd: repoRoot, encoding: 'buffer', stdio: ['ignore', 'pipe', 'ignore'] }).toString('utf8').split('\0').filter(Boolean)
  return [...new Set([...tracked, ...untracked])].filter(isProductVue).sort()
}

function tagAt(source, start) {
  if (source[start] !== '<') return null
  if (source.startsWith('<!--', start)) {
    const end = source.indexOf('-->', start + 4)
    return { comment: true, end: end < 0 ? source.length : end + 3 }
  }
  if (source.startsWith('{{', start)) {
    const end = source.indexOf('}}', start + 2)
    return { interpolation: true, end: end < 0 ? source.length : end + 2 }
  }
  let cursor = start + 1
  let closing = false
  if (source[cursor] === '/') { closing = true; cursor += 1 }
  if (source[cursor] === '!' || source[cursor] === '?') {
    const end = source.indexOf('>', cursor + 1)
    return { declaration: true, end: end < 0 ? source.length : end + 1 }
  }
  const nameStart = cursor
  while (cursor < source.length && /[A-Za-z0-9:_-]/.test(source[cursor])) cursor += 1
  if (cursor === nameStart) return null
  const name = source.slice(nameStart, cursor)
  const afterName = cursor
  let quote = ''
  while (cursor < source.length) {
    const char = source[cursor]
    if (quote) {
      if (char === quote) quote = ''
    } else if (char === '"' || char === "'") quote = char
    else if (char === '>') break
    cursor += 1
  }
  if (cursor >= source.length) return { malformed: true, name, end: source.length }
  const end = cursor + 1
  const raw = source.slice(start, end)
  const selfClosing = /\/\s*>$/.test(raw)
  return { name, lowerName: name.toLowerCase(), closing, selfClosing, start, end, afterName, raw }
}

function findTemplateClose(source, contentStart) {
  let depth = 1
  let cursor = contentStart
  while (cursor < source.length) {
    const start = source.indexOf('<', cursor)
    if (start < 0) return null
    const interpolation = source.indexOf('{{', cursor)
    if (interpolation >= 0 && interpolation < start) {
      const end = source.indexOf('}}', interpolation + 2)
      cursor = end < 0 ? source.length : end + 2
      continue
    }
    const tag = tagAt(source, start)
    if (!tag) { cursor = start + 1; continue }
    if (tag.comment || tag.interpolation || tag.declaration) { cursor = tag.end; continue }
    if (tag.lowerName === 'template') {
      if (tag.closing) depth -= 1
      else if (!tag.selfClosing) depth += 1
      if (depth === 0) return { contentEnd: start, closeEnd: tag.end }
    }
    cursor = tag.end
  }
  return null
}

function templateBlocks(source, path, gaps) {
  const blocks = []
  let cursor = 0
  while (cursor < source.length) {
    const start = source.indexOf('<', cursor)
    if (start < 0) break
    const tag = tagAt(source, start)
    if (!tag) { cursor = start + 1; continue }
    if (tag.comment || tag.declaration || tag.interpolation || tag.closing) { cursor = tag.end; continue }
    if (tag.lowerName === 'script' || tag.lowerName === 'style') {
      const close = new RegExp(`<\\/${tag.lowerName}\\s*>`, 'i').exec(source.slice(tag.end))
      cursor = close ? tag.end + close.index + close[0].length : source.length
      continue
    }
    if (tag.lowerName === 'template') {
      const language = tag.raw.match(/\blang\s*=\s*(["']?)([^\s"'>]+)\1/i)?.[2]
      if (language && language.toLowerCase() !== 'html') gaps.push({ path, reason: `Unsupported Vue template language ${JSON.stringify(language)}; native class scan cannot safely parse it.` })
      const closing = findTemplateClose(source, tag.end)
      if (!closing) {
        gaps.push({ path, reason: 'Unclosed Vue template block; native class scan is incomplete.' })
        break
      }
      blocks.push({ content: source.slice(tag.end, closing.contentEnd), offset: tag.end })
      cursor = closing.closeEnd
      continue
    }
    // SFC custom blocks (i18n, docs, route metadata) are not rendered markup.
    const close = new RegExp(`<\\/${tag.lowerName}\\s*>`, 'i').exec(source.slice(tag.end))
    cursor = close ? tag.end + close.index + close[0].length : tag.end
  }
  return blocks
}

function attributesFromTag(tag) {
  const attributes = []
  const raw = tag.raw
  let cursor = 1 + tag.name.length
  const limit = raw.length - 1
  while (cursor < limit) {
    while (cursor < limit && /\s|\//.test(raw[cursor])) cursor += 1
    if (cursor >= limit) break
    const nameStart = cursor
    while (cursor < limit && !/[\s=/>]/.test(raw[cursor])) cursor += 1
    if (cursor === nameStart) { cursor += 1; continue }
    const name = raw.slice(nameStart, cursor)
    while (cursor < limit && /\s/.test(raw[cursor])) cursor += 1
    let value = null
    let staticValue = false
    if (raw[cursor] === '=') {
      cursor += 1
      while (cursor < limit && /\s/.test(raw[cursor])) cursor += 1
      if (raw[cursor] === '"' || raw[cursor] === "'") {
        const quote = raw[cursor++]
        const valueStart = cursor
        while (cursor < limit && raw[cursor] !== quote) cursor += 1
        value = raw.slice(valueStart, cursor)
        if (raw[cursor] === quote) cursor += 1
        staticValue = true
      } else {
        const valueStart = cursor
        while (cursor < limit && !/[\s>]/.test(raw[cursor])) cursor += 1
        value = raw.slice(valueStart, cursor)
        staticValue = true
      }
    }
    attributes.push({ name, value, staticValue })
  }
  return attributes
}

function literalFeedbackClass(tag) {
  for (const attribute of attributesFromTag(tag)) {
    if (attribute.name !== 'class' || !attribute.staticValue || attribute.value == null) continue
    if (attribute.value.includes('{{') || attribute.value.includes('}}')) continue
    const tokens = attribute.value.split(/\s+/)
    if (tokens.some(token => LEGACY_CLASSES.includes(token))) return true
    if (tokens.includes('k-inline-notification') && tokens.some(token => /^k-inline-notification--(?:error|warning|success)$/.test(token))) return true
  }
  return false
}

export function scanTemplateMarkup(markup, path = '<fixture.vue>') {
  const occurrences = []
  let cursor = 0
  while (cursor < markup.length) {
    const start = markup.indexOf('<', cursor)
    if (start < 0) break
    const interpolation = markup.indexOf('{{', cursor)
    if (interpolation >= 0 && interpolation < start) {
      const end = markup.indexOf('}}', interpolation + 2)
      cursor = end < 0 ? markup.length : end + 2
      continue
    }
    const tag = tagAt(markup, start)
    if (!tag) { cursor = start + 1; continue }
    cursor = tag.end
    if (tag.comment || tag.interpolation || tag.declaration || tag.closing || tag.malformed) continue
    if (!NATIVE_TAGS.has(tag.lowerName) || !literalFeedbackClass(tag)) continue
    occurrences.push({ path, openingTag: tag.raw })
  }
  return occurrences
}

export function collectOccurrences(repoRoot = REPO_ROOT) {
  const occurrences = []
  const gaps = []
  const files = listVueSources(repoRoot).filter(path => {
    const absolute = resolve(repoRoot, path)
    return existsSync(absolute) && statSync(absolute).isFile()
  })
  for (const path of files) {
    let source
    try { source = readFileSync(resolve(repoRoot, path), 'utf8') } catch (error) {
      gaps.push({ path, reason: `Cannot read Vue source: ${error.message}` })
      continue
    }
    for (const block of templateBlocks(source, path, gaps)) occurrences.push(...scanTemplateMarkup(block.content, path))
  }
  const counts = new Map()
  for (const occurrence of occurrences) {
    const key = `${occurrence.path}\0${occurrence.openingTag}`
    counts.set(key, (counts.get(key) || 0) + 1)
  }
  return {
    files,
    occurrences: [...counts.entries()].map(([key, count]) => {
      const separator = key.indexOf('\0')
      return { path: key.slice(0, separator), openingTag: key.slice(separator + 1), count }
    }).sort((a, b) => a.path.localeCompare(b.path) || a.openingTag.localeCompare(b.openingTag)),
    gaps: gaps.sort((a, b) => a.path.localeCompare(b.path) || a.reason.localeCompare(b.reason)),
  }
}

function validateBaseline(baseline) {
  if (!baseline || baseline.version !== 1 || baseline.rule !== RULE || JSON.stringify(baseline.classTokens) !== JSON.stringify(LEGACY_CLASSES) || !Array.isArray(baseline.occurrences)) {
    throw new Error(`Invalid ${BASELINE_PATH}: expected version 1, canonical legacy classTokens, rule ${RULE}, and an occurrences array.`)
  }
  const counts = new Map()
  for (const occurrence of baseline.occurrences) {
    if (typeof occurrence.path !== 'string' || typeof occurrence.openingTag !== 'string' || !Number.isSafeInteger(occurrence.count) || occurrence.count < 1) {
      throw new Error(`Invalid baseline occurrence: ${JSON.stringify(occurrence)}`)
    }
    const key = `${occurrence.path}\0${occurrence.openingTag}`
    if (counts.has(key)) throw new Error(`Duplicate baseline key: ${occurrence.path} ${JSON.stringify(occurrence.openingTag)}`)
    counts.set(key, occurrence.count)
  }
  return counts
}

export function scan({ repoRoot = REPO_ROOT, baseline } = {}) {
  const baselineValue = baseline || JSON.parse(readFileSync(resolve(repoRoot, BASELINE_PATH), 'utf8'))
  const allowed = validateBaseline(baselineValue)
  const current = collectOccurrences(repoRoot)
  const diagnostics = []
  for (const occurrence of current.occurrences) {
    const key = `${occurrence.path}\0${occurrence.openingTag}`
    const baselineCount = allowed.get(key) || 0
    if (occurrence.count > baselineCount) diagnostics.push({
      rule: RULE,
      path: occurrence.path,
      openingTag: occurrence.openingTag,
      baselineCount,
      currentCount: occurrence.count,
      newCount: occurrence.count - baselineCount,
      message: `Native Vue markup added ${occurrence.count - baselineCount} occurrence(s) of the retired local notification recipe; use shared InlineNotification or native field validation.`,
    })
  }
  return { files: current.files, occurrences: current.occurrences, gaps: current.gaps, diagnostics }
}

function main() {
  try {
    const args = process.argv.slice(2)
    if (args.includes('--help') || args.includes('-h')) {
      console.log('Usage: node hack/verify-ui-banner-backlog.mjs [--print-occurrences]')
      return
    }
    const result = args.includes('--print-occurrences') ? collectOccurrences(REPO_ROOT) : scan({ repoRoot: REPO_ROOT })
    if (args.includes('--print-occurrences')) {
      process.stdout.write(`${JSON.stringify(result, null, 2)}\n`)
      return
    }
    for (const gap of result.gaps) console.error(`${gap.path} [${RULE}] scan incomplete: ${gap.reason}`)
    for (const diagnostic of result.diagnostics) {
      console.error(`${diagnostic.path} [${diagnostic.rule}] ${diagnostic.message} (baseline=${diagnostic.baselineCount}, current=${diagnostic.currentCount})`)
      console.error(`  ${JSON.stringify(diagnostic.openingTag)}`)
    }
    if (result.gaps.length || result.diagnostics.length) process.exitCode = 1
    else console.log(`UI feedback backlog guard passed (${result.files.length} product Vue files; ${result.occurrences.reduce((sum, occurrence) => sum + occurrence.count, 0)} baseline occurrences).`)
  } catch (error) {
    console.error(error.stack || error.message)
    process.exitCode = 1
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
