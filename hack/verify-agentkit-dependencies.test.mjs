import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const coreRoots = ['provider-sdk/portalkit', 'provider-sdk/portalkit-vue']
const agentRoots = ['provider-sdk/agentkit', 'provider-sdk/agentkit-vue']
const modules = folders => folders.flatMap(folder => readdirSync(resolve(root, folder))
  .filter(name => /\.(?:ts|vue)$/.test(name) && !/\.test\./.test(name))
  .map(name => resolve(root, folder, name)))
const imports = source => [...source.matchAll(/\b(?:from\s*|import\s*)['"]([^'"]+)['"]/g)].map(match => match[1])

test('PortalKit has no reverse dependency or optional AI stylesheet recipes', () => {
  for (const path of modules(coreRoots)) {
    for (const imported of imports(readFileSync(path, 'utf8'))) {
      assert.doesNotMatch(imported, /agentkit/, `${path} imports optional AgentKit`)
    }
  }
  const css = readFileSync(resolve(root, 'provider-sdk/portalkit/faros-ui.css'), 'utf8')
  assert.doesNotMatch(css, /\.k-(?:ai-|model-|workbench-tab)/)
})

test('canonical AgentKit relative imports resolve without provider source or transport dependencies', () => {
  for (const path of modules(agentRoots)) {
    for (const imported of imports(readFileSync(path, 'utf8'))) {
      if (!imported.startsWith('.')) continue
      const target = resolve(dirname(path), imported.split('?')[0])
      assert.ok(target.startsWith(resolve(root, 'provider-sdk') + '/'), `${path} reaches outside SDK: ${imported}`)
      assert.ok(['', '.ts', '.vue'].some(extension => existsSync(target + extension)), `${path} has an unresolved canonical import: ${imported}`)
      assert.ok([...coreRoots, ...agentRoots].some(folder => target.startsWith(resolve(root, folder) + '/')), `${path} reaches non-presentation SDK code: ${imported}`)
    }
  }
})
