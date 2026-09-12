// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit the copies vendored
// into individual portals; edit here and run `make sync-portalkit`.
//
// Framework-neutral counterpart to portalkit-vue/FormSelect.vue. It owns the
// same combobox/listbox contract and composes the canonical k-input/k-menu CSS
// recipes, while remaining usable by the vanilla/Lit provider portals.

import { ic } from './icons'
import { ensureFarosUIStyles } from './styles'

export interface FormSelectOption {
  value: string
  label: string
  disabled?: boolean
}

let nextFormSelectID = 0

export class FormSelectElement extends HTMLElement {
  private readonly instanceID = `form-select-${++nextFormSelectID}`
  private root: HTMLDivElement | null = null
  private trigger: HTMLButtonElement | null = null
  private valueNode: HTMLSpanElement | null = null
  private hiddenInput: HTMLInputElement | null = null
  private panel: HTMLDivElement | null = null
  private activeIndex = -1
  private isOpen = false
  private currentValue = ''
  private currentOptions: readonly FormSelectOption[] = []
  private currentPlaceholder = 'Select an option'
  private currentDisabled = false
  private currentRequired = false
  private currentInvalid = false
  private currentDescribedby = ''
  private currentLabelledby = ''
  private currentName = ''

  get value(): string { return this.currentValue }
  set value(value: string) {
    this.currentValue = value || ''
    if (this.isOpen) this.setInitialActive()
    this.syncControl()
    this.renderPanel()
  }

  get options(): readonly FormSelectOption[] { return this.currentOptions }
  set options(options: readonly FormSelectOption[]) {
    this.currentOptions = Array.isArray(options) ? options : []
    if (this.isOpen) this.setInitialActive()
    this.syncControl()
    this.renderPanel()
  }

  get placeholder(): string { return this.currentPlaceholder }
  set placeholder(value: string) {
    this.currentPlaceholder = value || 'Select an option'
    this.syncControl()
    this.renderPanel()
  }

  get disabled(): boolean { return this.currentDisabled }
  set disabled(value: boolean) {
    this.currentDisabled = Boolean(value)
    if (this.currentDisabled) this.closeSelect()
    this.syncControl()
  }

  get required(): boolean { return this.currentRequired }
  set required(value: boolean) { this.currentRequired = Boolean(value); this.syncControl() }

  get invalid(): boolean { return this.currentInvalid }
  set invalid(value: boolean) { this.currentInvalid = Boolean(value); this.syncControl() }

  get describedby(): string { return this.currentDescribedby }
  set describedby(value: string) { this.currentDescribedby = value || ''; this.syncControl() }

  get labelledby(): string { return this.currentLabelledby }
  set labelledby(value: string) { this.currentLabelledby = value || ''; this.syncControl() }

  get name(): string { return this.currentName }
  set name(value: string) { this.currentName = value || ''; this.syncHiddenInput() }

  connectedCallback(): void {
    ensureFarosUIStyles()
    if (!this.root) this.renderControl()
    document.addEventListener('pointerdown', this.onDocumentPointerDown)
    document.addEventListener('focusin', this.onDocumentFocusIn)
    window.addEventListener('resize', this.onViewportChange)
    window.addEventListener('scroll', this.onViewportChange, true)
  }

  disconnectedCallback(): void {
    document.removeEventListener('pointerdown', this.onDocumentPointerDown)
    document.removeEventListener('focusin', this.onDocumentFocusIn)
    window.removeEventListener('resize', this.onViewportChange)
    window.removeEventListener('scroll', this.onViewportChange, true)
    this.closeSelect()
  }

  focus(options?: FocusOptions): void {
    this.trigger?.focus(options)
  }

  private get listboxID(): string { return `${this.instanceID}-listbox` }
  private get valueID(): string { return `${this.instanceID}-value` }
  private optionID(index: number): string { return `${this.listboxID}-option-${index}` }
  private get accessibleLabelledby(): string {
    return [this.currentLabelledby, this.valueID].filter(Boolean).join(' ')
  }

  private renderControl(): void {
    const root = document.createElement('div')
    root.className = 'k-form-select'
    root.dataset.formSelect = ''

    const trigger = document.createElement('button')
    trigger.id = this.instanceID
    trigger.className = 'k-input k-form-select__trigger'
    trigger.type = 'button'
    trigger.setAttribute('role', 'combobox')
    trigger.dataset.formSelectTrigger = ''
    trigger.setAttribute('aria-haspopup', 'listbox')
    trigger.addEventListener('click', () => this.toggleSelect())
    trigger.addEventListener('keydown', this.onKeydown)

    const value = document.createElement('span')
    value.id = this.valueID
    value.className = 'k-form-select__value'
    trigger.append(value)

    const chevron = document.createElement('span')
    chevron.innerHTML = ic('chevron-down', 'k-form-select__chevron')
    trigger.append(chevron.firstElementChild || chevron)

    root.append(trigger)
    this.replaceChildren(root)
    this.root = root
    this.trigger = trigger
    this.valueNode = value
    this.syncControl()
  }

  private syncControl(): void {
    if (!this.root || !this.trigger || !this.valueNode) return
    const selected = this.currentOptions.find((option) => option.value === this.currentValue)
    this.valueNode.textContent = selected?.label || this.currentValue || this.currentPlaceholder
    this.valueNode.classList.toggle('is-placeholder', !this.currentValue)
    this.root.classList.toggle('is-open', this.isOpen)
    this.root.classList.toggle('is-disabled', this.currentDisabled)
    this.root.classList.toggle('is-invalid', this.currentInvalid)
    this.trigger.disabled = this.currentDisabled
    this.trigger.setAttribute('aria-controls', this.listboxID)
    this.trigger.setAttribute('aria-expanded', this.isOpen ? 'true' : 'false')
    this.trigger.setAttribute('aria-labelledby', this.accessibleLabelledby)
    this.setOptionalAttribute(this.trigger, 'aria-activedescendant', this.isOpen && this.activeIndex >= 0 ? this.optionID(this.activeIndex) : '')
    this.setOptionalAttribute(this.trigger, 'aria-describedby', this.currentDescribedby)
    this.setOptionalAttribute(this.trigger, 'aria-required', this.currentRequired ? 'true' : '')
    this.setOptionalAttribute(this.trigger, 'aria-invalid', this.currentInvalid ? 'true' : '')
    this.syncHiddenInput()
  }

  private syncHiddenInput(): void {
    if (!this.root) return
    if (!this.currentName) {
      this.hiddenInput?.remove()
      this.hiddenInput = null
      return
    }
    if (!this.hiddenInput) {
      this.hiddenInput = document.createElement('input')
      this.hiddenInput.className = 'k-form-select__value-input'
      this.hiddenInput.type = 'hidden'
      this.root.append(this.hiddenInput)
    }
    this.hiddenInput.name = this.currentName
    this.hiddenInput.value = this.currentValue
    this.hiddenInput.required = this.currentRequired
  }

  private setOptionalAttribute(element: HTMLElement, name: string, value: string): void {
    if (value) element.setAttribute(name, value)
    else element.removeAttribute(name)
  }

  private isSelectable(index: number): boolean {
    const option = this.currentOptions[index]
    return Boolean(option && !option.disabled)
  }

  private firstSelectableIndex(): number {
    return this.currentOptions.findIndex((option) => !option.disabled)
  }

  private lastSelectableIndex(): number {
    for (let index = this.currentOptions.length - 1; index >= 0; index -= 1) {
      if (this.isSelectable(index)) return index
    }
    return -1
  }

  private setInitialActive(): void {
    const selectedIndex = this.currentOptions.findIndex((option) => option.value === this.currentValue)
    this.activeIndex = selectedIndex >= 0 && this.isSelectable(selectedIndex) ? selectedIndex : this.firstSelectableIndex()
  }

  private openSelect(): void {
    if (this.isOpen || this.currentDisabled) return
    this.setInitialActive()
    this.isOpen = true
    this.renderPanel()
    this.syncControl()
    this.updatePanelPosition()
    this.scrollActiveOption()
  }

  private closeSelect(restoreFocus = false): void {
    if (!this.isOpen && !this.panel) return
    this.isOpen = false
    this.panel?.remove()
    this.panel = null
    this.syncControl()
    if (restoreFocus) queueMicrotask(() => this.trigger?.focus())
  }

  private toggleSelect(): void {
    if (this.isOpen) this.closeSelect()
    else this.openSelect()
  }

  private selectOption(option: FormSelectOption): void {
    if (this.currentDisabled || option.disabled) return
    this.currentValue = option.value
    this.closeSelect(true)
    this.syncControl()
    this.dispatchEvent(new CustomEvent<string>('change', { detail: option.value, bubbles: true, composed: true }))
  }

  private moveActive(delta: 1 | -1): void {
    const count = this.currentOptions.length
    if (!count) return
    let index = this.activeIndex
    for (let attempts = 0; attempts < count; attempts += 1) {
      index = (index + delta + count) % count
      if (this.isSelectable(index)) {
        this.activeIndex = index
        this.syncControl()
        this.syncPanel()
        this.scrollActiveOption()
        return
      }
    }
  }

  private onKeydown = (event: KeyboardEvent): void => {
    if (event.key === 'Tab') {
      if (this.isOpen) this.closeSelect()
      return
    }
    if (event.key === 'Escape') {
      if (!this.isOpen) return
      event.preventDefault()
      this.closeSelect(true)
      return
    }
    if (!this.isOpen) {
      if (!['Enter', ' ', 'ArrowDown', 'ArrowUp'].includes(event.key)) return
      event.preventDefault()
      this.openSelect()
      return
    }
    if (event.key === 'ArrowDown') {
      event.preventDefault(); this.moveActive(1)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault(); this.moveActive(-1)
    } else if (event.key === 'Home') {
      event.preventDefault(); this.activeIndex = this.firstSelectableIndex(); this.syncControl(); this.syncPanel(); this.scrollActiveOption()
    } else if (event.key === 'End') {
      event.preventDefault(); this.activeIndex = this.lastSelectableIndex(); this.syncControl(); this.syncPanel(); this.scrollActiveOption()
    } else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      const option = this.currentOptions[this.activeIndex]
      if (option) this.selectOption(option)
    }
  }

  private renderPanel(): void {
    if (!this.isOpen) return
    this.panel?.remove()
    const panel = document.createElement('div')
    panel.id = this.listboxID
    panel.className = 'k-menu k-form-select__panel k-form-select__portal'
    panel.setAttribute('role', 'listbox')
    panel.setAttribute('aria-labelledby', this.accessibleLabelledby)
    panel.addEventListener('keydown', this.onKeydown)

    if (!this.currentOptions.length) {
      const empty = document.createElement('p')
      empty.className = 'k-form-select__empty'
      empty.setAttribute('role', 'status')
      empty.textContent = this.currentPlaceholder
      panel.append(empty)
    } else {
      this.currentOptions.forEach((option, index) => {
        const item = document.createElement('button')
        item.id = this.optionID(index)
        item.className = 'k-menu-item k-form-select__option'
        item.type = 'button'
        item.setAttribute('role', 'option')
        item.tabIndex = -1
        item.disabled = Boolean(option.disabled)
        item.setAttribute('aria-selected', option.value === this.currentValue ? 'true' : 'false')
        if (option.disabled) item.setAttribute('aria-disabled', 'true')
        item.addEventListener('mouseenter', () => {
          if (option.disabled) return
          this.activeIndex = index
          this.syncControl()
          this.syncPanel()
        })
        item.addEventListener('mousedown', (event) => event.preventDefault())
        item.addEventListener('click', () => this.selectOption(option))

        const label = document.createElement('span')
        label.className = 'k-form-select__option-label'
        label.textContent = option.label
        item.append(label)
        if (option.value === this.currentValue) {
          const check = document.createElement('span')
          check.innerHTML = ic('check', 'k-form-select__check')
          item.append(check.firstElementChild || check)
        }
        panel.append(item)
      })
    }

    document.body.append(panel)
    this.panel = panel
    this.syncPanel()
    this.updatePanelPosition()
  }

  private syncPanel(): void {
    if (!this.panel) return
    this.panel.querySelectorAll<HTMLButtonElement>('[role="option"]').forEach((item, index) => {
      item.classList.toggle('is-active', index === this.activeIndex)
      item.classList.toggle('is-selected', this.currentOptions[index]?.value === this.currentValue)
      item.setAttribute('aria-selected', this.currentOptions[index]?.value === this.currentValue ? 'true' : 'false')
    })
  }

  private updatePanelPosition(): void {
    if (!this.trigger || !this.panel) return
    const rect = this.trigger.getBoundingClientRect()
    const viewportWidth = window.innerWidth || document.documentElement.clientWidth || 0
    const viewportHeight = window.innerHeight || document.documentElement.clientHeight || 0
    const gap = 6
    const edge = 8
    const width = Math.min(Math.max(rect.width, 200), Math.max(viewportWidth - edge * 2, 0))
    const left = Math.min(Math.max(rect.left, edge), Math.max(viewportWidth - width - edge, edge))
    const roomBelow = viewportHeight - rect.bottom - gap - edge
    const roomAbove = rect.top - gap - edge
    const opensAbove = roomBelow < 220 && roomAbove > roomBelow
    const availableHeight = Math.min(320, Math.max(opensAbove ? roomAbove : roomBelow, 0))
    Object.assign(this.panel.style, opensAbove
      ? { position: 'fixed', top: '', bottom: `${Math.max(viewportHeight - rect.top + gap, edge)}px`, left: `${left}px`, width: `${width}px`, maxHeight: `${availableHeight}px` }
      : { position: 'fixed', top: `${Math.max(rect.bottom + gap, edge)}px`, bottom: '', left: `${left}px`, width: `${width}px`, maxHeight: `${availableHeight}px` })
  }

  private scrollActiveOption(): void {
    if (this.activeIndex < 0) return
    queueMicrotask(() => document.getElementById(this.optionID(this.activeIndex))?.scrollIntoView?.({ block: 'nearest' }))
  }

  private onDocumentPointerDown = (event: Event): void => {
    if (!this.isOpen) return
    const target = event.target as Node | null
    if (target && (this.contains(target) || this.panel?.contains(target))) return
    this.closeSelect()
  }

  private onDocumentFocusIn = (event: FocusEvent): void => {
    if (!this.isOpen) return
    const target = event.target as Node | null
    if (target && (this.contains(target) || this.panel?.contains(target))) return
    this.closeSelect()
  }

  private onViewportChange = (): void => {
    if (this.isOpen) this.updatePanelPosition()
  }
}

if (typeof customElements !== 'undefined' && !customElements.get('faros-form-select')) {
  customElements.define('faros-form-select', FormSelectElement)
}

declare global {
  interface HTMLElementTagNameMap {
    'faros-form-select': FormSelectElement
  }
}
