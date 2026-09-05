import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const app = readFileSync(new URL('./App.vue', import.meta.url), 'utf8')
const provision = readFileSync(new URL('./views/ProvisionPage.vue', import.meta.url), 'utf8')
const detail = readFileSync(new URL('./views/InstanceDetailPage.vue', import.meta.url), 'utf8')

describe('Infrastructure primary provisioning workflow', () => {
  it('leaves primary section navigation to the manifest-owned shell', () => {
    expect(app).not.toContain("import Tabs from './portalkit/Tabs.vue'")
    expect(app).not.toContain('sectionTabs')
    expect(app).not.toContain('activeSection')
    expect(app).toContain("new CustomEvent('faros-navigate'")
    expect(app).toContain('parseInfrastructureSubPath(props.ctx?.subPath)')
  })

  it('uses one bounded, authoritative instance name', () => {
    expect(provision).toContain('class="k-create-surface k-create-surface--wide"')
    expect(provision).toContain('class="provision-identity"')
    expect(provision).toContain('const { name: _name, ...properties } = schema.properties')
    expect(provision).toContain('const writableValues = createWritableValues(currentTemplate.inputsSchema, values.value)')
    expect(provision).toContain('values: writableValues')
    expect(provision).toContain('id="infrastructure-instance-name"')
    expect(provision).toContain('required')
  })

  it('keeps internal tenancy details out of user-facing errors and labels status', () => {
    expect(provision).toContain('The selected workspace is no longer available. Choose a workspace in the sidebar, then try again.')
    expect(provision).not.toContain('Phase-3 hub wiring required')
    expect(detail).toContain('<span class="instance-status-label">Status</span>')
  })
})
