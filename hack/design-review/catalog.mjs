import { existsSync, readFileSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, extname, posix, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { validateDesignDocs } from '../verify-design-docs.mjs'
const { reviews } = await import(new URL('./reviews.mjs?loaded=' + Date.now(), import.meta.url).href)

export const repo = resolve(fileURLToPath(new URL('../..', import.meta.url)))
const require = createRequire(resolve(repo, 'portal/package.json'))
const MarkdownIt = require('markdown-it')
const markdown = new MarkdownIt({ html: false })
const foundations = 'portal/src/assets/main.css'
const recipes = 'provider-sdk/portalkit/railgrid-ui.css'
const bookRoot = 'docs/design/'
const read = path => readFileSync(resolve(repo, path), 'utf8')
const route = params => '?' + new URLSearchParams(params)
const rows = text => text.split(/\r?\n/)
const slug = text => text.toLowerCase().replace(/[^\p{L}\p{N} _-]/gu, '').trim().replace(/ /g, '-')
const sectionName = inline => inline.children.filter(child => child.type !== 'html_inline').map(child => child.content).join('')

function localLink(href, path) {
  const [target, fragment] = href.split('#')
  if (/^(?:https?:|mailto:)/.test(target)) return href
  const file = target ? posix.normalize(posix.join(dirname(path), decodeURI(target))) : path
  return { file, fragment }
}

export function reviewCatalog() {
  const { catalog, diagnostics } = validateDesignDocs()
  if (diagnostics.length) throw new Error(JSON.stringify(diagnostics, null, 2))
  const documents = catalog.documents
  const byFile = Object.fromEntries(documents.map(doc => [doc.path, doc.id]))
  const sources = {}
  const images = {}
  function source(path, needle) {
    const [file, fragment] = path.split('#')
    if (/^(?:https?:|mailto:)/.test(file)) return { path, href: path }
    const location = resolve(repo, file)
    if (!location.startsWith(repo + '/') || !existsSync(location)) throw new Error('Missing review source: ' + file)
    if (!statSync(location).isFile()) return { path, directory: true }
    if (statSync(location).size > 1024 * 1024 || /\.(png|svg|jpg|webp|woff2?)$/i.test(file)) return { path, asset: true }
    const lines = sources[file] ??= rows(read(file))
    const line = needle ? lines.findIndex(line => line.includes(needle)) + 1
      : fragment?.match(/^L(\d+)$/) ? Number(fragment.slice(1))
      : fragment ? lines.findIndex(line => /^#+ /.test(line) && slug(line.replace(/^#+ /, '').replace(/\x60/g, '')) === fragment) + 1
      : 1
    if (!line || line > lines.length) throw new Error('Missing review source locator: ' + path + ' ' + (needle || ''))
    return { path: file, line, href: route({ source: file, line: String(line) }) }
  }
  function documentBody(doc) {
    const fileRows = rows(read(doc.path))
    const offset = fileRows.indexOf('---', 1) + 1
    const sections = []
    const tokens = markdown.parse(fileRows.slice(offset).join('\n'), {})
    for (let i = 0; i < tokens.length; i++) {
      const token = tokens[i]
      if (token.type === 'heading_open') {
        const title = sectionName(tokens[i + 1])
        const line = token.map[0] + offset + 1
        const anchor = slug(title)
        token.attrSet('id', 'rule-' + line)
        if (token.tag !== 'h1') {
          sections.push({ title, line, anchor, level: Number(token.tag[1]) })
        }
      }
      if (token.type !== 'inline') continue
      for (const child of token.children) {
        if (child.type !== 'link_open' && child.type !== 'image') continue
        const attr = child.type === 'image' ? 'src' : 'href'
        const ref = localLink(child.attrGet(attr), doc.path)
        if (typeof ref === 'string') continue
        const { file, fragment } = ref
        if (child.type === 'image') {
          const location = resolve(repo, file)
          if (!location.startsWith(repo + '/') || !existsSync(location)) continue
          const format = extname(file).slice(1)
          images[file] ??= 'data:image/' + format + ';base64,' + readFileSync(location).toString('base64')
          child.attrSet(attr, images[file])
          child.attrSet('loading', 'lazy')
        } else if (byFile[file]) {
          child.attrSet(attr, route({ entry: byFile[file], view: 'contract', ...(fragment ? { section: fragment } : {}) }))
        } else if (existsSync(resolve(repo, file))) {
          const ref = source(file)
          child.attrSet(attr, ref.href || route({ entry: doc.id, view: 'sources' }))
        }
      }
    }
    return { sections, html: markdown.renderer.render(tokens, markdown.options, {}) }
  }

  const ids = new Set(documents.map(doc => doc.id.replace(/^design\./, '')))
  for (const id of ids) if (!reviews[id]) throw new Error('Missing review fixture: design.' + id)
  for (const id of Object.keys(reviews)) if (!ids.has(id)) throw new Error('Orphaned review fixture: design.' + id)
  const entries = documents.map(doc => {
    const review = reviews[doc.id.slice('design.'.length)]
    return {
      ...doc,
      ...documentBody(doc),
      review: { ...review, sources: (review.sources ?? []).map(([path, needle]) => source(path, needle)) },
      sources: doc.canonicalSource.map(ref => ({ ...source(ref.path), role: ref.role, label: ref.label })),
    }
  })
  const tokens = [...new Set([...read(foundations).matchAll(/^\s*(--(?:color|font|radius)-[\w-]+):/gm)].map(match => match[1]))]
    .map(name => ({ name, source: source(foundations, name + ':') }))
  const recipeRows = rows(read(bookRoot + 'foundations/recipes.md'))
    .filter(line => line.startsWith('| `'))
    .map(line => {
      const [label, description] = line.split('|').slice(1, 3).map(part => part.trim().replace(/\x60/g, ''))
      const selector = label.startsWith('[') ? '[data-k-tip]' : label.split(' ')[0]
      const needle = rows(read(recipes)).find(line => line.trimStart().startsWith(selector) && /[,{]/.test(line))?.trim()
      return { label, description, source: source(recipes, needle || selector) }
    })
  const assetDoc = bookRoot + 'components/portalkit-assets.md'
  const documented = new Map()
  let kit = 'portalkit'
  for (const [i, line] of rows(read(assetDoc)).entries()) {
    if (line === '## Vue assets') kit = 'portalkit-vue'
    if (line === '## Optional AgentKit assets') kit = 'agentkit-vue'
    const match = line.match(/^\| `([^`]+)` \| \[([^\]]+)\]\(([^)]+)\)/)
    if (!match) continue
    const [, name, contract, target] = match
    const id = byFile[posix.normalize(posix.join(dirname(assetDoc), target))]
    documented.set(kit + '/' + name, { name, contract, entry: id, spec: source(assetDoc + '#L' + (i + 1)) })
  }
  const manifest = 'hack/sync-portalkit.sh'
  const distribution = [
    ['TS_FILES', 'portalkit'], ['VUE_SHARED_FILES', 'portalkit'], ['VUE_FILES', 'portalkit-vue'],
    ['VUE_TOAST_FILES', 'portalkit-vue'], ['AGENTS_LEGACY_FILES', 'portalkit'],
    ['AGENTKIT_FILES', 'agentkit'], ['AGENTKIT_VUE_FILES', 'agentkit-vue'],
  ]
  const fallbacks = {
    'page-context.ts': 'components.provider-agent-context',
    'navigation.ts': 'patterns.navigation-and-feedback',
    'kube.ts': 'components.portalkit-support',
    'page-state.ts': 'patterns.resource-reads',
    'ResourceBoard.vue': 'components.resource-board',
    'ResourceTableExpandButton.vue': 'components.resource-table-actions',
    'ResourceTableActionTooltip.vue': 'components.resource-table-actions',
    'useUserLabels.ts': 'components.resource-table',
  }
  const assets = new Map()
  const manifestCode = read(manifest)
  for (const [array, kit] of distribution) {
    const match = manifestCode.match(new RegExp('^' + array + '=\\(([^)]*)\\)', 'm'))
    if (!match) throw new Error('Missing distribution array: ' + array)
    for (const name of match[1].trim().split(/\s+/)) {
      const key = kit + '/' + name
      if (assets.has(key)) continue
      const found = documented.get(key) ?? (kit === 'agentkit' ? documented.get('agentkit-vue/' + name) : undefined)
      const fallback = fallbacks[name] ?? (kit.startsWith('agentkit') ? 'components.ai-conversation' : 'components.portalkit-support')
      const doc = entries.find(entry => entry.id === (found?.entry ?? 'design.' + fallback))
      const begin = rows(manifestCode.slice(0, match.index)).length
      const line = begin + rows(match[0]).findIndex(line => line.trim().split(/[()\s]+/).includes(name))
      assets.set(key, {
        name, contract: found?.contract ?? doc.title, entry: doc.id, spec: found?.spec,
        source: source('provider-sdk/' + key), manifest: source(manifest + '#L' + line),
      })
    }
  }
  const exceptions = JSON.parse(read('hack/ui-conformance-exceptions.json')).exceptions.map(ref => ({
    ...ref, source: source(ref.path + '#L' + ref.line),
  }))
  return { entries, tokens, recipeRows, assets: [...assets.values()], exceptions, sources }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const { entries, tokens, recipeRows, assets, exceptions } = reviewCatalog()
  console.log(`${entries.length} entries · ${entries.reduce((sum, entry) => sum + entry.sections.length, 0)} linked sections · ${tokens.length} tokens · ${recipeRows.length} recipe rows · ${assets.length} asset references · ${exceptions.length} exception locators`)
}
