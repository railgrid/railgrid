import { gzipSync } from 'node:zlib'
import { readFile } from 'node:fs/promises'
import { createContext, Script } from 'node:vm'

import {
  collectManifestEntryArtifacts,
  listBuildArtifacts,
  manifestEntryKey,
  totalArtifactSize,
} from './build-budget-lib.mjs'

const distURL = new URL('../dist/', import.meta.url)
const artifactNames = await listBuildArtifacts(distURL)

async function artifact(name) {
  const data = await readFile(new URL(name, distURL))
  return { name, rawBytes: data.byteLength, gzipBytes: gzipSync(data, { level: 9 }).byteLength }
}

async function artifacts(names) {
  for (const name of names) {
    if (!artifactNames.includes(name)) throw new Error(`manifest references missing artifact ${name}`)
  }
  return Promise.all(names.map(artifact))
}

function enforce(label, items, budget) {
  const size = totalArtifactSize(items)
  console.log(`${label}: ${size.rawBytes} bytes raw, ${size.gzipBytes} bytes gzip`)
  if (size.rawBytes > budget.rawBytes || size.gzipBytes > budget.gzipBytes) {
    throw new Error(`${label} exceeds budget (${budget.rawBytes} raw / ${budget.gzipBytes} gzip bytes)`)
  }
}

const entry = await artifact('main.js')
const entrySource = (await readFile(new URL('main.js', distURL), 'utf8')).replace(/\/\*[\s\S]*?\*\/|\/\/[^\n]*/g, '')
if (!entrySource.includes('import(')) throw new Error('App Studio bootstrap must retain lazy dynamic imports')
for (const hashedImport of ['./assets/page-element-', './assets/tile-element-']) {
  if (!entrySource.includes(hashedImport)) {
    throw new Error(`App Studio bootstrap must retain content-hashed import ${hashedImport}`)
  }
}
if (/\bimport(?!\s*\()|\bexport(?:\s|\{|\*)/.test(entrySource)) {
  throw new Error('App Studio bootstrap contains static ES-module syntax and cannot run as a classic script')
}
const trimmedEntrySource = entrySource.trim()
if (!trimmedEntrySource.startsWith('(()=>{') || !trimmedEntrySource.endsWith('})();')) {
  throw new Error('App Studio bootstrap must isolate its classic-script declarations in an IIFE')
}
const bootstrap = new Script(entrySource, { filename: 'dist/main.js' })
const classicScriptContext = createContext({
  HTMLElement: class {},
  customElements: { get: () => true, define: () => undefined },
})
// A classic provider script can be encountered more than once across host
// surfaces. Replaying it in one global context proves it does not leak lexical
// declarations that make a subsequent load fail with a redeclaration error.
bootstrap.runInContext(classicScriptContext)
bootstrap.runInContext(classicScriptContext)
const manifest = JSON.parse(await readFile(new URL('.vite/manifest.json', distURL), 'utf8'))
const mainKey = manifestEntryKey(manifest, (item) => item.isEntry === true, 'main entry')
const pageKey = manifestEntryKey(manifest, (item) => item.name === 'page-element', 'page entry')
const tileKey = manifestEntryKey(manifest, (item) => item.name === 'tile-element', 'tile entry')
const bootstrapNames = collectManifestEntryArtifacts(manifest, mainKey)
const pageNames = collectManifestEntryArtifacts(manifest, pageKey)
const tileNames = collectManifestEntryArtifacts(manifest, tileKey)
const pageCSS = pageNames.filter((name) => name.endsWith('.css'))
if (pageCSS.length !== 1 || !/^assets\/page-element-[A-Za-z0-9_-]+\.css$/.test(pageCSS[0])) {
  throw new Error(`expected one content-hashed page stylesheet, found: ${pageCSS.join(', ') || 'none'}`)
}
const bootstrapArtifacts = await artifacts(bootstrapNames)
const pageArtifacts = await artifacts([...new Set([...bootstrapNames, ...pageNames])])
const tileArtifacts = await artifacts([...new Set([...bootstrapNames, ...tileNames])])
const allArtifacts = await Promise.all(artifactNames.map(artifact))

// The entry is the only eager download. Connecting the dashboard tile loads
// the small tile path; connecting the full provider loads the page path.
// Keep route budgets separate so a tiny registrar cannot hide growth in a
// lazy chunk, and keep a total budget to prevent duplication across routes.
enforce('App Studio bootstrap', bootstrapArtifacts, { rawBytes: 12_000, gzipBytes: 5_000 })
enforce('App Studio dashboard path', tileArtifacts, { rawBytes: 310_000, gzipBytes: 100_000 })
enforce('App Studio page path', pageArtifacts, { rawBytes: 1_180_000, gzipBytes: 335_000 })
enforce('App Studio total assets', allArtifacts, { rawBytes: 1_200_000, gzipBytes: 345_000 })
