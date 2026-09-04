import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const styles = readFileSync(new URL('./style.css', import.meta.url), 'utf8')
const provision = readFileSync(new URL('./views/ProvisionPage.vue', import.meta.url), 'utf8')
const dynamicForm = readFileSync(new URL('./components/DynamicForm.vue', import.meta.url), 'utf8')
const viewValue = readFileSync(new URL('./components/ViewValue.vue', import.meta.url), 'utf8')
const templateCard = readFileSync(new URL('./components/TemplateCard.vue', import.meta.url), 'utf8')

describe('Infrastructure composition contracts', () => {
  it('adds deliberate columns for wide template catalogs', () => {
    expect(styles).toMatch(/@media \(min-width: 1440px\)[\s\S]*repeat\(4, minmax\(0, 1fr\)\)/)
    expect(styles).toMatch(/@media \(min-width: 1920px\)[\s\S]*repeat\(5, minmax\(0, 1fr\)\)/)
  })

  it('keeps provisioning fields in one vertical column and the identity cap', () => {
    const dynform = styles.match(/faros-provider-infrastructure \.dynform \{[\s\S]*?\n\}/)?.[0] ?? ''
    expect(dynform).toContain('display: flex;')
    expect(dynform).toContain('flex-direction: column;')
    expect(dynform).toContain('gap: 12px;')
    expect(dynform).toContain('min-width: 0;')
    expect(dynform).not.toContain('grid-template-columns:')
    expect(styles).not.toContain('container-name: infrastructure-form;')
    expect(styles).not.toContain('container-type: inline-size;')
    expect(styles).not.toContain('@container infrastructure-form')
    expect(styles).not.toMatch(/\.dynform-group \{[\s\S]*?grid-column:/)
    expect(styles).not.toContain('.dynform-error')
    expect(provision).not.toContain('infrastructure-provision-form')
    expect(provision).toContain('class="provision-identity"')
    expect(styles).toMatch(/\.provision-identity \{[\s\S]*max-width: 42rem;/)
    expect(styles).toMatch(/\.dynform-row \{[\s\S]*min-width: 0;/)
    expect(styles).toMatch(/\.dynform-group \{[\s\S]*min-inline-size: 0;[\s\S]*min-width: 0;/)
  })

  it('keeps booleans and compact disclosure controls on shared recipes', () => {
    expect(dynamicForm).toContain('class="k-checkbox"')
    expect(viewValue).toContain('k-icon-action')
    expect(viewValue).toContain(':data-k-tip=')
    expect(viewValue).not.toContain(':title=')
    expect(templateCard).toContain(':data-k-tip=')
    expect(templateCard).not.toContain(':title=')
  })
})
