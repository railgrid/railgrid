import { auditDirectory } from './audit-design-system-output.mjs'
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
const auditDir = auditDirectory()
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
process.chdir(root)
const require = createRequire(path.join(root, 'portal/package.json'))
const ts = require('typescript')
const { parse } = require('@vue/compiler-sfc')
const postcss = require('postcss')
const rel = p => path.relative(root, p).split(path.sep).join('/')
const read = p => fs.readFileSync(p, 'utf8')
function walk(dir) {
  if (!fs.existsSync(dir)) return []
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap(d => d.isDirectory() ? walk(path.join(dir, d.name)) : [path.join(dir, d.name)])
}
function parseArray(script, name) {
  const start = script.match(new RegExp('^' + name + '=\\(', 'm'))
  if (!start) return []
  const open = start.index + name.length + 2
  const firstLineEnd = script.indexOf('\n', open)
  const firstLine = script.slice(open, firstLineEnd < 0 ? script.length : firstLineEnd)
  let body
  if (firstLine.includes(')')) body = firstLine.slice(0, firstLine.lastIndexOf(')'))
  else {
    const closeLine = script.indexOf('\n)', open)
    if (closeLine < 0) return []
    body = script.slice(open, closeLine)
  }
  body = body.split(/\r?\n/).map(line => line.replace(/#.*$/, '')).join('\n')
  return [...body.matchAll(/"([^"]+)"|\b([A-Za-z0-9_.-]+)\b/g)].map(x => x[1] || x[2]).filter(Boolean)
}
const sync = read('hack/sync-portalkit.sh')
const arrays = {}
for (const n of ['TS_PORTALS','TS_FILES','VUE_PORTALS','VUE_FILES','VUE_TOAST_FILES','VUE_TOAST_PORTALS','VUE_SHARED_FILES','AGENTKIT_PORTALS','AGENTKIT_FILES','AGENTKIT_VUE_FILES','TS_CANONICAL_ONLY','VUE_CANONICAL_ONLY','AGENTKIT_CANONICAL_ONLY','AGENTKIT_VUE_CANONICAL_ONLY']) arrays[n] = parseArray(sync, n)
const consumers = new Map()
function addConsumer(file, group, targets) {
  const x = consumers.get(file) || { groups: [], portals: new Set() }
  if (!x.groups.includes(group)) x.groups.push(group)
  for (const t of targets) x.portals.add(t)
  consumers.set(file, x)
}
for (const f of arrays.TS_FILES) addConsumer('provider-sdk/portalkit/' + f, 'vanilla-ts', arrays.TS_PORTALS)
for (const f of arrays.VUE_FILES) addConsumer('provider-sdk/portalkit-vue/' + f, 'vue-core', arrays.VUE_PORTALS)
for (const f of arrays.VUE_TOAST_FILES) addConsumer('provider-sdk/portalkit-vue/' + f, 'vue-toast', arrays.VUE_TOAST_PORTALS)
for (const f of arrays.VUE_SHARED_FILES) addConsumer('provider-sdk/portalkit/' + f, 'vue-shared', arrays.VUE_PORTALS)
for (const f of arrays.AGENTKIT_FILES) addConsumer('provider-sdk/agentkit/' + f, 'agentkit-plain', arrays.AGENTKIT_PORTALS)
for (const f of arrays.AGENTKIT_VUE_FILES) addConsumer('provider-sdk/agentkit-vue/' + f, 'agentkit-vue', arrays.AGENTKIT_PORTALS)
const canonicalRoots = ['provider-sdk/portalkit','provider-sdk/portalkit-vue','provider-sdk/agentkit','provider-sdk/agentkit-vue','portal/src/components']
const sourceFiles = canonicalRoots.flatMap(walk)
const designFiles = walk('docs/design').filter(p => p.endsWith('.md') && !p.includes('/templates/'))
const designEntries = []
for (const p of designFiles) {
  const s = read(p)
  if (!s.startsWith('---\n')) continue
  const end = s.indexOf('\n---\n', 4)
  if (end < 0) continue
  let meta
  try { meta = JSON.parse(s.slice(4, end)) } catch { continue }
  designEntries.push({ path: rel(p), id: meta.id, title: meta.title, kind: meta.kind, status: meta.status, authority: meta.authority, implementation: meta.implementation, verification: { state: meta.verification?.state, checks: meta.verification?.checks?.map(c => ({ kind: c.kind, ref: c.ref, status: c.status })) }, canonicalSource: meta.canonicalSource, relatedDocuments: meta.relatedDocuments })
}
function macroInfo(file) {
  const source = read(file)
  const { descriptor, errors } = parse(source, { filename: file })
  if (errors.length) throw new Error(`Cannot inventory ${file}: ${errors.join(', ')}`)
  const out = { props: [], emits: [], slots: [], slotOutlets: [], scriptExports: [] }
  for (const block of [descriptor.script, descriptor.scriptSetup].filter(Boolean)) {
    const sf = ts.createSourceFile(file + '.ts', block.content, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
    const lineOf = n => block.loc.start.line + sf.getLineAndCharacterOfPosition(n.getStart(sf)).line
    function visit(n) {
      if (ts.isCallExpression(n) && ts.isIdentifier(n.expression) && ['defineProps','defineEmits','defineSlots'].includes(n.expression.text)) {
        const key = n.expression.text === 'defineProps' ? 'props' : n.expression.text === 'defineEmits' ? 'emits' : 'slots'
        let signature = n.typeArguments?.[0]?.getText(sf)
        if (!signature && n.arguments?.length) signature = n.arguments.map(x => x.getText(sf)).join(', ')
        const entry = { signature: signature || null, line: lineOf(n) }
        if (key === 'props' && ts.isCallExpression(n.parent) && ts.isIdentifier(n.parent.expression) && n.parent.expression.text === 'withDefaults' && n.parent.arguments[1]) entry.defaults = n.parent.arguments[1].getText(sf)
        out[key].push(entry)
      }
      if ((ts.isInterfaceDeclaration(n) || ts.isTypeAliasDeclaration(n) || ts.isFunctionDeclaration(n) || ts.isClassDeclaration(n) || ts.isEnumDeclaration(n)) && n.modifiers?.some(m => m.kind === ts.SyntaxKind.ExportKeyword)) {
        out.scriptExports.push({ kind: ts.SyntaxKind[n.kind], name: n.name?.text || '(anonymous)', line: lineOf(n), signature: n.getText(sf).split('\n').slice(0, 16).join('\n').slice(0, 1800) })
      }
      ts.forEachChild(n, visit)
    }
    visit(sf)
  }
  function visitTemplate(node) {
    if (node.tag === 'slot') {
      const name = node.props.find(p => p.name === 'name')
      const boundName = node.props.find(p => p.name === 'bind' && p.arg?.content === 'name')
      const bindings = node.props.filter(p => p.name === 'bind' && p !== boundName)
      out.slotOutlets.push({
        name: boundName ? null : name?.value?.content || 'default',
        nameExpression: boundName?.exp?.content || null,
        scopeBindings: bindings.map(p => p.arg?.content || p.loc.source),
        line: node.loc.start.line,
      })
    }
    for (const child of node.children || []) visitTemplate(child)
  }
  if (descriptor.template?.ast) visitTemplate(descriptor.template.ast)
  return out
}
function exportedTS(file) {
  const source = read(file)
  const sf = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const out = []
  const exported = n => n.modifiers?.some(m => m.kind === ts.SyntaxKind.ExportKeyword)
  for (const st of sf.statements) {
    if (ts.isExportDeclaration(st)) {
      out.push({ kind: 'ExportDeclaration', name: st.exportClause?.getText(sf) || '*', line: sf.getLineAndCharacterOfPosition(st.getStart(sf)).line + 1, signature: st.getText(sf) })
      continue
    }
    if (ts.isFunctionDeclaration(st) && exported(st)) {
      const end = st.body ? st.body.getStart(sf) : st.end
      out.push({ kind: 'function', name: st.name?.text || 'default', line: sf.getLineAndCharacterOfPosition(st.getStart(sf)).line + 1, signature: source.slice(st.getStart(sf), end).trim() })
      continue
    }
    if ((ts.isInterfaceDeclaration(st) || ts.isTypeAliasDeclaration(st) || ts.isClassDeclaration(st) || ts.isEnumDeclaration(st)) && exported(st)) {
      out.push({ kind: ts.SyntaxKind[st.kind], name: st.name?.text || 'default', line: sf.getLineAndCharacterOfPosition(st.getStart(sf)).line + 1, signature: st.getText(sf).slice(0, 3200) })
      continue
    }
    if (ts.isVariableStatement(st) && exported(st)) {
      for (const d of st.declarationList.declarations) {
        const name = d.name.getText(sf)
        let signature = 'export ' + (st.declarationList.flags & ts.NodeFlags.Const ? 'const' : 'let') + ' ' + name + (d.type ? ': ' + d.type.getText(sf) : '')
        if (d.initializer) {
          if (ts.isArrowFunction(d.initializer) || ts.isFunctionExpression(d.initializer)) {
            const body = d.initializer.body?.getStart(sf) ?? d.initializer.end
            signature += ' = ' + source.slice(d.initializer.getStart(sf), body).trim()
          } else {
            const value = d.initializer.getText(sf)
            signature += value.length <= 180 && !value.includes('\n') ? ' = ' + value : ' = <initializer omitted; see source>'
          }
        }
        out.push({ kind: 'variable', name, line: sf.getLineAndCharacterOfPosition(d.getStart(sf)).line + 1, signature })
      }
    }
  }
  return out
}
function stylesheet(file) {
  const css = postcss.parse(read(file), { from: file })
  const selectors = new Map(), customProperties = new Map()
  css.walkRules(rule => {
    for (const match of rule.selector.matchAll(/\.([A-Za-z_][\w-]*)/g)) {
      if (!selectors.has(match[1])) selectors.set(match[1], rule.source.start.line)
    }
  })
  css.walkDecls(/^--/, decl => {
    if (!customProperties.has(decl.prop)) customProperties.set(decl.prop, { name: decl.prop, value: decl.value, line: decl.source.start.line })
  })
  return { path: file, classSelectors: [...selectors].map(([selector, line]) => ({ selector, line })), customProperties: [...customProperties.values()] }
}
// The source inventory resolves host aliases and maps distributed copies to their canonical file.
const inventory = JSON.parse(read(path.join(auditDir, 'inventory.json')))
const components = new Map(inventory.components.map(component => [component.path, component]))
const importsByTarget = new Map()
for (const edge of inventory.imports) {
  if (!edge.targetPath) continue
  if (!importsByTarget.has(edge.targetPath)) importsByTarget.set(edge.targetPath, [])
  importsByTarget.get(edge.targetPath).push({ path: edge.path, line: edge.line, specifier: edge.specifier })
}
const componentApis = [], moduleExports = [], assetInventory = []
for (const file of sourceFiles) {
  const rp = rel(file), base = path.basename(file), ext = path.extname(file), target = consumers.get(rp)
  const imports = importsByTarget.get(rp) || []
  const usages = components.get(rp)?.usages || []
  let kind = ext === '.vue' ? 'vue-component' : ext === '.css' ? 'stylesheet' : base === 'README.md' ? 'documentation' : /\.test\.mjs$/.test(base) ? 'test' : ext === '.ts' ? 'typescript-module' : 'other'
  assetInventory.push({ path: rp, kind, designEntries: designEntries.filter(entry => entry.canonicalSource?.some(source => source.path?.split('#')[0] === rp)).map(entry => entry.id), distributionGroups: target?.groups || [], consumerPortals: target ? [...target.portals].map(p => p + '/src/' + (target.groups.some(g => g.startsWith('agentkit')) ? 'agentkit' : 'portalkit')) : [], imports })
  if (ext === '.vue') componentApis.push({ path: rp, usages, ...macroInfo(file) })
  else if (ext === '.ts' && !base.endsWith('.test.ts')) moduleExports.push({ path: rp, exports: exportedTS(file) })
}
function braceBlock(text, start) {
  let depth = 0, opened = false
  for (let i = start; i < text.length; i++) {
    if (text[i] === '{') { depth++; opened = true }
    else if (text[i] === '}') { depth--; if (opened && depth === 0) return text.slice(start, i + 1) }
  }
  return ''
}
function blockProps(text, index) {
  if (index < 0) return []
  const b = braceBlock(text, index), found = new Map(), re = /(--[A-Za-z0-9_-]+)\s*:\s*([^;\n}]+)/g
  let m
  while ((m = re.exec(b))) found.set(m[1], m[2].trim())
  return [...found].map(([name, value]) => ({ name, value }))
}
const rootTokensFile = 'portal/src/assets/main.css', rootCss = read(rootTokensFile)
const baseTokens = blockProps(rootCss, rootCss.indexOf('@theme'))
const lightTokens = blockProps(rootCss, rootCss.indexOf('html.light {'))
const cssFiles = ['portal/src/assets/main.css','provider-sdk/portalkit/railgrid-ui.css','provider-sdk/portalkit/provider-fallback.css','provider-sdk/agentkit/agent-ui.css','provider-sdk/agentkit/activity.css','provider-sdk/agentkit/conversation.css']
const recipeStylesheets = cssFiles.map(stylesheet)
const familyMap = {}
for (const token of baseTokens) {
  const family = token.name.startsWith('--color-') ? 'color' : token.name.startsWith('--font-') ? 'font' : token.name.startsWith('--radius-') ? 'radius' : 'other'
  ;(familyMap[family] ??= []).push(token)
}
const exceptionRegistry = JSON.parse(read('hack/ui-conformance-exceptions.json'))
const config = JSON.parse(read('hack/ui-conformance.config.json'))
const runtimeStyleVersions = {
  portalKit: { constant: read('provider-sdk/portalkit/styles.ts').match(/RAILGRID_UI_CORE_VERSION\s*=\s*(\d+)/)?.[1], css: read('provider-sdk/portalkit/railgrid-ui.css').match(/--railgrid-ui-core-version:\s*(\d+)/)?.[1], source: 'provider-sdk/portalkit/styles.ts:22', stylesheet: 'provider-sdk/portalkit/railgrid-ui.css:13' },
  agentKit: { constant: read('provider-sdk/agentkit/styles.ts').match(/AGENT_UI_VERSION\s*=\s*(\d+)/)?.[1], css: read('provider-sdk/agentkit/agent-ui.css').match(/--railgrid-agent-ui-version:\s*(\d+)/)?.[1], source: 'provider-sdk/agentkit/styles.ts:17', stylesheet: 'provider-sdk/agentkit/agent-ui.css:11' }
}
const relevantDesignEntries = designEntries.filter(e => e.path === 'docs/design/foundations.md' || /^docs\/design\/(foundations\/|components\/|patterns\/|quality\/)/.test(e.path))
const output = {
  schemaVersion: 1,
  artifact: 'canonical-reference',
  purpose: 'Audit index of existing design authorities and their actual canonical sources/APIs. It points back to docs/design and does not replace or redefine those contracts.',
  authorities: { designKnowledgeBase: 'docs/design/README.md', implementationAuthority: ['portal/src/assets/main.css','provider-sdk/portalkit/','provider-sdk/portalkit-vue/','provider-sdk/agentkit/','provider-sdk/agentkit-vue/','portal/src/components/'], distributionAuthority: 'hack/sync-portalkit.sh', legacyPointer: 'docs/design-book.md' },
  documentationScope: { markdownDocumentCount: designFiles.length, metadataEntryCount: designEntries.length, relevantMetadataEntryCount: relevantDesignEntries.length, nonMetadataDocs: ['docs/design/README.md','docs/design/schema.md'], excludedTemplates: ['docs/design/templates/design-document.md','docs/design/templates/component-document.md'], entries: designEntries },
  standaloneStatusPage: {
    source: 'provider-sdk/statuspage/statuspage.go', api: 'Render(io.Writer, Page) error',
    callerOwnedFields: ['Title', 'Heading', 'Message', 'State', 'Script'],
    calls: inventory.files.flatMap(file => (file.statusPageCalls || []).map(location => ({ path: file.path, ...location }))),
    deviceEntry: 'pkg/server/auth/device_page.html',
    dexTemplates: inventory.files.filter(file => file.path.startsWith('hack/dex/web/templates/') && file.extension === '.html').map(file => file.path),
  },
  standaloneProviderStyles: { source: 'provider-sdk/portalkit/provider-fallback.css', inlineCopies: inventory.files.filter(file => file.included && file.extension === '.html' && read(file.path).includes('<style data-railgrid-provider-fallback>')).map(file => file.path) },
  canonicalRoots: canonicalRoots.map(p => ({ path: p, fileCount: walk(p).length })),
  sourceCoverage: { canonicalFileCount: sourceFiles.length, vueComponentCount: componentApis.length, typescriptModuleCount: moduleExports.length, stylesheetCount: recipeStylesheets.length, resolvedImportFiles: new Set(inventory.imports.filter(edge => edge.targetPath).map(edge => edge.path)).size, designMarkdownCount: designFiles.length, metadataEntryCount: designEntries.length, relevantMetadataEntryCount: relevantDesignEntries.length, extraction: 'All canonical files were enumerated. TypeScript AST extraction records Vue defineProps/defineEmits/defineSlots and exported declarations; CSS class selectors and custom-property declarations are extracted from the canonical stylesheet set. Import sites and template usage sites come from the resolved source inventory, including host, provider and fixture consumers.', manualRuntimeCoverage: 'Source extraction does not verify runtime behavior, route behavior, accessibility, or rendered appearance.' },
  distribution: { manifestPath: 'hack/sync-portalkit.sh', arrays, notes: ['Canonical provider-sdk files are authorities; portal src/portalkit and src/agentkit copies are generated distribution outputs.','AgentKit is optional and is distributed only to the two registered consuming portals: Agents and App Studio.','The canonical AgentKit Vue imports from ../portalkit-vue; sync rewrites declared core-component references to ../portalkit in distribution.','The portals remain self-contained rather than npm workspace consumers.'] },
  assets: assetInventory,
  componentApis,
  typescriptExports: moduleExports,
  tokenFamilies: { authority: rootTokensFile, darkBase: baseTokens, lightOverride: lightTokens, families: familyMap, additionalCustomProperties: [...new Map(recipeStylesheets.flatMap(x => x.customProperties).map(x => [x.name, x])).values()] },
  cssRecipeInventory: { stylesheets: recipeStylesheets.map(x => ({ path: x.path, classSelectorCount: x.classSelectors.length, selectorFamilies: [...new Set(x.classSelectors.map(s => s.selector.split('__')[0].split('--')[0]))].sort(), classSelectors: x.classSelectors })), notes: ['Class selectors are extracted from canonical CSS source; use the component/pattern design documents for the intended behavior.','Selector family grouping is syntactic (BEM root before __ or --), not an independently proposed taxonomy.'] },
  runtimeStyleVersions,
  exceptionRegistry: { path: 'hack/ui-conformance-exceptions.json', count: exceptionRegistry.exceptions.length, byRule: exceptionRegistry.exceptions.reduce((a,x) => (a[x.rule] = (a[x.rule] || 0) + 1, a), {}), entries: exceptionRegistry.exceptions },
  enforcement: { conformanceConfig: config, commands: ['node hack/audit-design-system-canonical.mjs','make verify-design-docs','make verify-portalkit','make verify-ui-conformance'], docs: ['docs/design/quality/ui-conformance.md','docs/design/quality/exceptions.md','docs/design/quality/review-checklist.md'], implementation: ['hack/audit-design-system-canonical.mjs','hack/verify-design-docs.mjs','hack/sync-portalkit.sh','hack/verify-ui-conformance.mjs','hack/verify-ui-conformance.test.mjs','hack/ui-conformance.config.json','hack/ui-conformance-exceptions.json'], coverageSummary: 'Design-doc validator checks JSON metadata and Markdown/source links. PortalKit verifier checks explicit manifests, byte-copy parity, unexpected assets and generated consumer boundaries. UI scanner covers four canonical roots plus configured portal roots; tests and generated provider copies are excluded, and exact source-located exceptions are required.', conformanceRules: ['legacy pre-k hooks','provider CSS redeclarations of .k-*','native dialogs','Unicode/emoji icons in icon context','unknown --color-* tokens','raw colors','pill/soft radii','provider-local common-widget selectors'] },
  documentationDiscrepancyRefs: ['CAN-E-001','CAN-E-002','CAN-E-003','CAN-E-004'],
  limitations: ['API extraction is AST-backed for Vue defineProps/defineEmits/defineSlots macros and exported TypeScript declarations; slot outlets include nested templates, bound names and source lines from the Vue compiler.','Indirect, dynamic and unresolved component references can lack a usage edge. An import alone does not prove that a component rendered.','Source API extraction does not verify all runtime behavior, keyboard interactions, visual output, or route/state coverage.','Design metadata is recorded as pointers and lifecycle/authority summary; docs/design remains the source of truth.','This canonical-source audit does not enumerate the repository-wide frontend as a whole; that is in the broader audit artifacts.']
}
fs.mkdirSync(auditDir, { recursive: true })
fs.writeFileSync(path.join(auditDir, 'canonical-reference.json'), JSON.stringify(output, null, 2) + '\n')
console.log(JSON.stringify({ entries: designEntries.length, relevantEntries: relevantDesignEntries.length, roots: output.canonicalRoots, assets: assetInventory.length, vueComponents: componentApis.length, tsModules: moduleExports.length, sourceFiles: sourceFiles.length, tokenCounts: Object.fromEntries(Object.entries(familyMap).map(([k,v]) => [k,v.length])), styleCounts: recipeStylesheets.map(x => ({ path: x.path, selectors: x.classSelectors.length, props: x.customProperties.length })), versions: runtimeStyleVersions, arrays: Object.fromEntries(Object.entries(arrays).map(([k,v]) => [k,v.length])) }, null, 2))
