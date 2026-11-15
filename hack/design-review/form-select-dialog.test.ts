import { expect, it, vi } from 'vitest'
import '../../provider-sdk/portalkit/form-select'
import { createApp, h, nextTick, ref } from 'vue'
import FormSelect from '../../provider-sdk/portalkit-vue/FormSelect.vue'
import { configureClientAppIDs } from '../../provider-sdk/portalkit/document-id'

it.each(['Vue', 'framework-neutral'])('leaves modified shortcuts alone in %s', async kind => {
  const host = document.createElement('div')
  document.body.append(host)
  const options = [{ value: 'west', label: 'West' }, { value: 'east', label: 'East' }]
  const selected: string[] = []
  const app = kind === 'Vue' ? createApp({ render: () => h(FormSelect, {
    modelValue: 'west', options, 'onUpdate:modelValue': (value: string) => selected.push(value),
  }) }) : null
  const vanilla = kind === 'framework-neutral' ? document.createElement('railgrid-form-select') : null
  if (app) app.mount(host)
  if (vanilla) { vanilla.options = options; vanilla.value = 'west'; host.append(vanilla) }
  const trigger = host.querySelector<HTMLButtonElement>('[role="combobox"]')!
  const key = (name: string, modifiers: KeyboardEventInit = {}) => {
    const event = new KeyboardEvent('keydown', { key: name, bubbles: true, cancelable: true, ...modifiers })
    trigger.dispatchEvent(event)
    return event
  }
  try {
    expect(key('ArrowDown', { ctrlKey: true }).defaultPrevented).toBe(false)
    await nextTick()
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    expect(key('ArrowDown', { altKey: true }).defaultPrevented).toBe(true)
    await nextTick()
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    expect(key('ArrowUp', { altKey: true }).defaultPrevented).toBe(true)
    await nextTick()
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    key('ArrowDown')
    await nextTick()
    const active = trigger.getAttribute('aria-activedescendant')
    expect(key('ArrowDown', { metaKey: true }).defaultPrevented).toBe(false)
    expect(key('Enter', { ctrlKey: true }).defaultPrevented).toBe(false)
    await nextTick()
    expect(trigger.getAttribute('aria-activedescendant')).toBe(active)
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    expect(selected).toEqual([])
    expect(vanilla?.value ?? 'west').toBe('west')
    key('Tab', { shiftKey: true })
    await nextTick()
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
  } finally { app?.unmount(); host.remove() }
})

it.each(['Vue', 'framework-neutral'])('preserves the active value during open-list updates in %s', async kind => {
  const host = document.createElement('div')
  document.body.append(host)
  const west = { value: 'west', label: 'West' }
  const east = { value: 'east', label: 'East' }
  const north = { value: 'north', label: 'North' }
  const options = ref([west, east, north])
  const selected: string[] = []
  const app = kind === 'Vue' ? createApp({ render: () => h(FormSelect, {
    modelValue: 'west', options: options.value,
    'onUpdate:modelValue': (value: string) => selected.push(value),
  }) }) : null
  const vanilla = kind === 'framework-neutral' ? document.createElement('railgrid-form-select') : null
  if (app) app.mount(host)
  if (vanilla) { vanilla.options = options.value; vanilla.value = 'west'; host.append(vanilla) }
  const trigger = host.querySelector<HTMLButtonElement>('[role="combobox"]')!
  const key = (name: string) => trigger.dispatchEvent(new KeyboardEvent('keydown', { key: name, bubbles: true, cancelable: true }))
  const update = async (next: typeof options.value) => {
    options.value = next
    if (vanilla) vanilla.options = next
    await nextTick()
  }
  const active = () => document.getElementById(trigger.getAttribute('aria-activedescendant') || '')?.textContent?.trim()
  try {
    trigger.click()
    await nextTick()
    key('ArrowDown')
    await nextTick()
    expect(active()).toContain('East')
    await update([north, west, east])
    expect(active()).toContain('East')
    await update([north, west, { ...east, disabled: true }])
    expect(active()).toContain('West')
    await update([north])
    expect(active()).toContain('North')
    await update([])
    expect(trigger.hasAttribute('aria-activedescendant')).toBe(false)
    await update([north, west, east])
    expect(active()).toContain('West')
    key('ArrowDown')
    await nextTick()
    await update([east, north, west])
    key('Enter')
    await nextTick()
    expect(vanilla ? vanilla.value : selected.at(-1)).toBe('East'.toLowerCase())
  } finally { app?.unmount(); host.remove() }
})

it('keeps the framework-neutral listbox inside an open dialog', async () => {
  const dialog = document.createElement('dialog')
  dialog.open = true
  const select = document.createElement('railgrid-form-select')
  select.options = [{ value: 'west', label: 'US West' }, { value: 'east', label: 'US East' }]
  dialog.append(select)
  document.body.append(dialog)
  try {
    const trigger = select.querySelector<HTMLButtonElement>('[role="combobox"]')!
    trigger.focus()
    trigger.click()
    const list = dialog.querySelector<HTMLElement>('[role="listbox"]')
    expect(list).not.toBeNull()
    expect(trigger.getAttribute('aria-controls')).toBe(list!.id)
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }))
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await Promise.resolve()
    expect(select.value).toBe('east')
    expect(dialog.querySelector('[role="listbox"]')).toBeNull()
    expect(document.activeElement).toBe(trigger)
  } finally { dialog.remove() }
})

it('preserves native wrapping labels for both select implementations', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h('label', ['Deployment region', h(FormSelect, { modelValue: 'west', options: [{ value: 'west', label: 'US West' }] })]) })
  app.mount(host)
  const vanillaLabel = document.createElement('label')
  vanillaLabel.append('Backup region')
  const vanilla = document.createElement('railgrid-form-select')
  vanilla.options = [{ value: 'west', label: 'US West' }]
  vanilla.value = 'west'
  vanillaLabel.append(vanilla)
  document.body.append(vanillaLabel)
  try {
    for (const [trigger, label] of [[host.querySelector<HTMLButtonElement>('[role="combobox"]')!, host.querySelector('label')!], [vanilla.querySelector<HTMLButtonElement>('[role="combobox"]')!, vanillaLabel]] as const) {
      expect(trigger.hasAttribute('aria-labelledby')).toBe(false)
      expect([...trigger.labels!]).toContain(label)
      trigger.click()
      await Promise.resolve()
      const list = document.getElementById(trigger.getAttribute('aria-controls')!)!
      expect(label.id).toBeTruthy()
      expect(list.getAttribute('aria-labelledby')).toBe(label.id)
      trigger.click()
    }
  } finally { app.unmount(); host.remove(); vanillaLabel.remove() }
})

it('omits a disabled framework-neutral select from form data and restores it when enabled', () => {
  const form = document.createElement('form')
  const select = document.createElement('railgrid-form-select')
  select.name = 'region'
  select.options = [{ value: 'west', label: 'US West' }]
  select.value = 'west'
  form.append(select)
  document.body.append(form)
  try {
    expect(new FormData(form).get('region')).toBe('west')
    select.disabled = true
    expect(new FormData(form).has('region')).toBe(false)
    expect(select.querySelector<HTMLButtonElement>('[role="combobox"]')!.disabled).toBe(true)
    select.disabled = false
    expect(new FormData(form).get('region')).toBe('west')
  } finally { form.remove() }
})

it('lets Escape dismiss each select before it reaches the enclosing surface', async () => {
  const host = document.createElement('div')
  const vueHost = document.createElement('div')
  host.append(vueHost)
  document.body.append(host)
  const app = createApp({ render: () => h(FormSelect, { modelValue: 'west', options: [{ value: 'west', label: 'US West' }], labelledby: 'escape-label' }) })
  app.mount(vueHost)
  const vanilla = document.createElement('railgrid-form-select')
  vanilla.options = [{ value: 'west', label: 'US West' }]
  host.append(vanilla)
  let escaped = 0
  host.addEventListener('keydown', event => { if (event.key === 'Escape') escaped++ })
  try {
    for (const trigger of host.querySelectorAll<HTMLButtonElement>('[role="combobox"]')) {
      trigger.focus()
      trigger.click()
      await Promise.resolve()
      expect(document.getElementById(trigger.getAttribute('aria-controls')!), trigger.outerHTML).not.toBeNull()
      trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await Promise.resolve()
      expect(document.getElementById(trigger.getAttribute('aria-controls')!)).toBeNull()
      expect(document.activeElement).toBe(trigger)
      expect(escaped).toBe(0)
      trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      expect(escaped).toBe(1)
      escaped = 0
      await nextTick()
    }
  } finally { app.unmount(); host.remove() }
})

it('ignores IME confirmation keys while preserving ordinary keyboard selection', async () => {
  const host = document.createElement('div')
  document.body.append(host)
  const selected: string[] = []
  const app = createApp({ render: () => h(FormSelect, {
    modelValue: 'west',
    options: [{ value: 'west', label: 'US West' }, { value: 'east', label: 'US East' }],
    'onUpdate:modelValue': (value: string) => selected.push(value),
  }) })
  app.mount(host)
  const trigger = host.querySelector<HTMLButtonElement>('[role="combobox"]')!
  const key = (name: string, composing = false, keyCode = 0) => trigger.dispatchEvent(new KeyboardEvent('keydown', { key: name, isComposing: composing, keyCode, bubbles: true, cancelable: true }))
  try {
    key('Enter', true)
    await nextTick()
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    key('Enter')
    await nextTick()
    key('ArrowDown')
    key('Enter', false, 229)
    key('Escape', true)
    await nextTick()
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    expect(selected).toEqual([])
    key('Enter')
    await nextTick()
    expect(selected).toEqual(['east'])
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
  } finally { app.unmount(); host.remove() }
})

it('does not open the framework-neutral select for an IME composition key', () => {
  const select = document.createElement('railgrid-form-select')
  select.options = [{ value: 'west', label: 'US West' }]
  document.body.append(select)
  try {
    const trigger = select.querySelector<HTMLButtonElement>('[role="combobox"]')!
    const event = new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true, cancelable: true })
    trigger.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(false)
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
  } finally { select.remove() }
})

it('keeps generated select IDs distinct across independent Vue roots', async () => {
  const hosts = [document.createElement('div'), document.createElement('div')]
  hosts.forEach(host => document.body.append(host))
  const apps = hosts.map(host => {
    const app = createApp({ render: () => h(FormSelect, { modelValue: 'west', options: [{ value: 'west', label: 'US West' }] }) })
    configureClientAppIDs(app)
    app.mount(host)
    return app
  })
  try {
    const triggers = hosts.map(host => host.querySelector<HTMLButtonElement>('[role="combobox"]')!)
    expect(triggers[0].id).not.toBe(triggers[1].id)
    expect(triggers[0].getAttribute('aria-controls')).not.toBe(triggers[1].getAttribute('aria-controls'))
    triggers[1].focus()
    triggers[1].click()
    await nextTick()
    const list = document.getElementById(triggers[1].getAttribute('aria-controls')!)!
    expect(list.getAttribute('role')).toBe('listbox')
    expect(list.contains(document.getElementById(triggers[1].getAttribute('aria-activedescendant')!))).toBe(true)
  } finally {
    apps.forEach(app => app.unmount())
    hosts.forEach(host => host.remove())
  }
})


it('shares document IDs across separately evaluated bundles', async () => {
  const first = await import('../../provider-sdk/portalkit/document-id')
  const id = first.nextDocumentID()
  vi.resetModules()
  const second = await import('../../provider-sdk/portalkit/document-id')
  expect(second.nextDocumentID()).not.toBe(id)
})
