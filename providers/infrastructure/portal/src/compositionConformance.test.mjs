import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const styles = readFileSync(new URL('./style.css', import.meta.url), 'utf8')
const app = readFileSync(new URL('./App.vue', import.meta.url), 'utf8')
const provision = readFileSync(new URL('./views/ProvisionPage.vue', import.meta.url), 'utf8')
const dynamicForm = readFileSync(new URL('./components/DynamicForm.vue', import.meta.url), 'utf8')
const viewValue = readFileSync(new URL('./components/ViewValue.vue', import.meta.url), 'utf8')

describe('Infrastructure composition contracts', () => {
  it('does not recreate manifest-owned navigation inside the provider', () => {
    expect(app).not.toContain('class="infra-tabs"')
    expect(styles).not.toContain('.infra-tabs')
  })

  it('keeps provisioning fields in one vertical column and the identity cap', () => {
    const dynform = styles.match(/railgrid-provider-infrastructure \.dynform \{[\s\S]*?\n\}/)?.[0] ?? ''
    expect(dynform).toContain('display: flex;')
    expect(dynform).toContain('flex-direction: column;')
    expect(dynform).toContain('gap: 12px;')
    expect(dynform).toContain('min-width: 0;')
    expect(dynform).not.toContain('grid-template-columns:')
    expect(styles).not.toContain('container-name: infrastructure-form;')
    expect(styles).not.toContain('container-type: inline-size;')
    expect(styles).not.toContain('@container infrastructure-form')
    expect(styles).not.toMatch(/\.dynform-group \{[^}]*grid-column:/)
    expect(provision).not.toContain('infrastructure-provision-form')
    expect(provision).toContain('class="provision-identity"')
    expect(provision).toContain('class="provision-generated-fields"')
    expect(styles).toMatch(/\.provision-identity \{[\s\S]*max-width: 42rem;/)
    expect(styles).toMatch(/\.provision-generated-fields \{[\s\S]*width: min\(100%, 42rem\);/)
    expect(styles).toMatch(/\.dynform-row \{[\s\S]*min-width: 0;/)
    expect(styles).toMatch(/\.dynform-group \{[\s\S]*min-inline-size: 0;[\s\S]*min-width: 0;/)
  })

  it('keeps booleans and compact disclosure controls on shared recipes', () => {
    expect(dynamicForm).toContain('class="k-checkbox"')
    expect(viewValue).toContain('<CopyValue v-else-if="value.type === \'code\'"')
    expect(viewValue).not.toContain('<button')
    expect(dynamicForm).toContain('class="k-icon-action dynform-map-remove"')
    expect(dynamicForm).toContain(':data-k-tip=')
  })
})
