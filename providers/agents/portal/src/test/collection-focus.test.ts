import { defineComponent, h, ref, watch } from 'vue'
import { describe, expect, it } from 'vitest'
import ResourceTable from '../portalkit/ResourceTable.vue'
import { useCollectionSession } from '../portalkit/useCollectionSession'
import { mountVue, settleVue } from './vue-helper'

describe('collection focus', () => {
  it('retains controls when emptied and resets the session on scope change', async () => {
    const rows = ref<{ id: string; kind: string }[]>([])
    const scope = ref(1)
    const query = ref('')
    const filters = ref<Record<string, string>>({})
    const Harness = defineComponent({ setup() {
      const shown = useCollectionSession(() => rows.value.length > 0 || !!query.value || Object.values(filters.value).some(Boolean), () => scope.value)
      return () => shown.value ? h(ResourceTable, {
        rows: rows.value, columns: [{ key: 'id', label: 'Name' }], rowKey: 'id', ariaLabel: 'Items', loaded: true,
        searchable: true, filters: [{ key: 'kind', label: 'Kind' }], query: query.value, filterValues: filters.value,
        'onUpdate:query': (value: string) => { query.value = value },
        'onUpdate:filterValues': (value: Record<string, string>) => { filters.value = value },
      }) : h('p', 'First run')
    } })
    const { element } = await mountVue(Harness, {})
    expect(element.textContent).toContain('First run')
    rows.value = [{ id: 'one', kind: 'alpha' }]
    await settleVue()
    const input = element.querySelector<HTMLInputElement>('.k-table__search-input')!
    input.focus(); input.value = 'one'; input.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue()
    rows.value = []
    await settleVue()
    input.value = ''; input.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue()
    expect(document.activeElement).toBe(input)
    expect(element.querySelector('.k-table__search-input')).toBe(input)
    rows.value = [{ id: 'one', kind: 'alpha' }]
    await settleVue()
    element.querySelector<HTMLButtonElement>('.k-table__filter-trigger')!.click()
    await settleVue()
    ;[...document.querySelectorAll<HTMLElement>('.k-table__filter-option')].find(el => el.textContent?.trim() === 'alpha')!.click()
    await settleVue()
    rows.value = []
    await settleVue()
    const clear = element.querySelector<HTMLButtonElement>('.k-table__clear-filters')!
    clear.focus(); clear.click()
    await settleVue()
    expect(document.activeElement).toBe(input)
    const external = document.createElement('button'); document.body.append(external); external.focus()
    rows.value = [{ id: 'two', kind: 'beta' }]
    await settleVue()
    rows.value = []
    await settleVue()
    expect(document.activeElement).toBe(external)
    scope.value = 2
    await settleVue()
    expect(element.textContent).toContain('First run')
    external.remove()
  })

  it('batches scope reset with cleared controls and the new authority snapshot', async () => {
    const authority = ref('a')
    const rows = ref(['old'])
    const Child = defineComponent({
      props: { scope: { type: String, required: true }, rows: { type: Array<string>, required: true } },
      setup(props) {
        const query = ref('active')
        const filter = ref('selected')
        const shown = useCollectionSession(() => props.rows.length > 0 || !!query.value || !!filter.value, () => props.scope)
        watch(() => props.scope, () => { query.value = ''; filter.value = '' }, { flush: 'sync' })
        return () => h('div', { 'data-shown': String(shown.value), 'data-query': query.value, 'data-filter': filter.value })
      },
    })
    // App Studio synchronously clears the previous snapshot on authority change.
    watch(authority, () => { rows.value = [] }, { flush: 'sync' })
    const Parent = defineComponent({ setup: () => () => h(Child, { scope: authority.value, rows: rows.value }) })
    const { element } = await mountVue(Parent, {})
    expect(element.firstElementChild?.getAttribute('data-shown')).toBe('true')
    authority.value = 'b'
    await settleVue()
    expect(element.firstElementChild?.getAttribute('data-query')).toBe('')
    expect(element.firstElementChild?.getAttribute('data-filter')).toBe('')
    expect(element.firstElementChild?.getAttribute('data-shown')).toBe('false')
    rows.value = ['new']
    await settleVue()
    expect(element.firstElementChild?.getAttribute('data-shown')).toBe('true')
    // The new snapshot can already be populated in the same render.
    authority.value = 'c'
    rows.value = ['newer']
    await settleVue()
    expect(element.firstElementChild?.getAttribute('data-shown')).toBe('true')
    rows.value = []
    await settleVue()
    expect(element.firstElementChild?.getAttribute('data-shown')).toBe('true')
  })

})
