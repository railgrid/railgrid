import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const styles = await readFile(new URL('./style.css', import.meta.url), 'utf8')

test('uses PortalKit tabs for route navigation and keeps the route contract', () => {
  assert.match(app, /import Tabs from '\.\/portalkit\/Tabs\.vue'/)
  assert.match(app, /const edgeRouteTabs = \[[\s\S]*id: 'edges'[\s\S]*Server[\s\S]*id: 'workloads'[\s\S]*Boxes[\s\S]*id: 'services'[\s\S]*Plug/)

  const routeTabs = app.match(/<Tabs[\s\S]*?@select="\(id\) => navigate\(id === 'edges' \? '' : id\)"\s*\/>/)?.[0]
  assert.ok(routeTabs, 'route navigation should be rendered by PortalKit Tabs')
  assert.match(routeTabs, /v-if="!wizardOpen && !selected"/)
  assert.match(routeTabs, /:tabs="edgeRouteTabs"/)
  assert.match(routeTabs, /:active="view"/)
  assert.match(routeTabs, /aria-label="Edges sections"/)
  assert.doesNotMatch(routeTabs, /wiz-steps|wiz-step|style=/)
})

test('keeps wizard progression styling and overlay route guards intact', () => {
  assert.match(styles, /faros-provider-edges \.edges-app \{[^}]*display: flex;[^}]*flex-direction: column;[^}]*gap: 16px;/)
  assert.match(app, /<Workloads v-if="view === 'workloads' && !wizardOpen && !selected" \/>/)
  assert.match(app, /<Services v-else-if="view === 'services' && !wizardOpen && !selected" \/>/)
  assert.match(app, /<Wizard v-else-if="wizardOpen"/)
  assert.match(app, /<Detail[\s\S]*v-else-if="selected"/)
  assert.match(styles, /\.wiz-steps\s*\{[\s\S]*\.wiz-step\s*\{[\s\S]*\.wiz-step\.active\s*\{[\s\S]*\.wiz-step\.done\s*\{/)
})
