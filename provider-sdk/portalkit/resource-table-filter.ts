// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies.
// Framework-neutral counterpart to portalkit-vue/ResourceTableFilter.vue for
// resource tables rendered by vanilla/Lit portals.

import { ic } from './icons'
import { ensureFarosUIStyles } from './styles'

export interface ResourceTableFilterOption {
  value: string
  label: string
}

let nextTableFilterID = 0

export class ResourceTableFilterElement extends HTMLElement {
  private readonly instanceID = `k-table-filter-${++nextTableFilterID}`
  private root: HTMLDivElement | null = null
  private trigger: HTMLButtonElement | null = null
  private valueNode: HTMLSpanElement | null = null
  private panel: HTMLDivElement | null = null
  private activeIndex = 0
  private isOpen = false
  private currentLabel = 'Filter'
  private currentAllLabel = 'Any'
  private currentValue = ''
  private currentOptions: readonly ResourceTableFilterOption[] = []

  get label(): string { return this.currentLabel }
  set label(value: string) { this.currentLabel = value || 'Filter'; this.syncControl() }

  get allLabel(): string { return this.currentAllLabel }
  set allLabel(value: string) { this.currentAllLabel = value || 'Any'; this.syncControl(); this.renderPanel() }

  get value(): string { return this.currentValue }
  set value(value: string) {
    this.currentValue = value || ''
    if (this.isOpen) this.setInitialActive()
    this.syncControl()
    this.renderPanel()
  }

  get options(): readonly ResourceTableFilterOption[] { return this.currentOptions }
  set options(options: readonly ResourceTableFilterOption[]) {
    this.currentOptions = Array.isArray(options) ? options : []
    if (this.isOpen) this.setInitialActive()
    this.syncControl()
    this.renderPanel()
  }

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
    this.closeFilter()
  }

  private get labelID(): string { return `${this.instanceID}-label` }
  private get valueID(): string { return `${this.instanceID}-value` }
  private get listboxID(): string { return `${this.instanceID}-listbox` }
  private optionID(index: number): string { return `${this.listboxID}-option-${index}` }
  private get optionList(): ResourceTableFilterOption[] {
    return [{ value: '', label: this.emptyOptionLabel() }, ...this.currentOptions]
  }

  private emptyOptionLabel(): string {
    const configured = this.currentAllLabel.trim()
    if (!configured) return 'Any'
    const normalized = configured.toLocaleLowerCase()
    const filterLabel = this.currentLabel.trim().toLocaleLowerCase()
    if (normalized === `all ${filterLabel}`) return 'All'
    if (normalized === `any ${filterLabel}`) return 'Any'
    return configured
  }

  private renderControl(): void {
    const root = document.createElement('div')
    root.className = 'k-table__filter'

    const label = document.createElement('span')
    label.id = this.labelID
    label.className = 'k-table__filter-label'
    root.append(label)

    const trigger = document.createElement('button')
    trigger.className = 'k-table__filter-trigger'
    trigger.type = 'button'
    trigger.setAttribute('role', 'combobox')
    trigger.setAttribute('aria-autocomplete', 'none')
    trigger.setAttribute('aria-haspopup', 'listbox')
    trigger.addEventListener('click', () => this.toggleFilter())
    trigger.addEventListener('keydown', this.onKeydown)

    const value = document.createElement('span')
    value.id = this.valueID
    value.className = 'k-table__filter-value'
    trigger.append(value)
    const chevron = document.createElement('span')
    chevron.innerHTML = ic('chevron-down')
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
    const label = this.root.querySelector<HTMLElement>('.k-table__filter-label')
    if (label) label.textContent = this.currentLabel
    this.root.classList.toggle('is-active', Boolean(this.currentValue))
    this.valueNode.textContent = this.optionList.find((option) => option.value === this.currentValue)?.label || this.currentValue || this.emptyOptionLabel()
    this.trigger.setAttribute('aria-controls', this.listboxID)
    this.trigger.setAttribute('aria-expanded', this.isOpen ? 'true' : 'false')
    this.trigger.setAttribute('aria-labelledby', `${this.labelID} ${this.valueID}`)
    if (this.isOpen) this.trigger.setAttribute('aria-activedescendant', this.optionID(this.activeIndex))
    else this.trigger.removeAttribute('aria-activedescendant')
  }

  private setInitialActive(): void {
    const index = this.optionList.findIndex((option) => option.value === this.currentValue)
    this.activeIndex = index >= 0 ? index : 0
  }

  private openFilter(): void {
    if (this.isOpen) return
    this.setInitialActive()
    this.isOpen = true
    this.renderPanel()
    this.syncControl()
    this.updatePanelPosition()
    this.scrollActiveOption()
  }

  private closeFilter(restoreFocus = false): void {
    if (!this.isOpen && !this.panel) return
    this.isOpen = false
    this.panel?.remove()
    this.panel = null
    this.syncControl()
    if (restoreFocus) queueMicrotask(() => this.trigger?.focus())
  }

  private toggleFilter(): void {
    if (this.isOpen) this.closeFilter()
    else this.openFilter()
  }

  private selectOption(option: ResourceTableFilterOption): void {
    this.currentValue = option.value
    this.closeFilter(true)
    this.syncControl()
    this.dispatchEvent(new CustomEvent<string>('change', { detail: option.value, bubbles: true, composed: true }))
  }

  private moveActive(delta: 1 | -1): void {
    const count = this.optionList.length
    if (!count) return
    this.activeIndex = (this.activeIndex + delta + count) % count
    this.syncControl()
    this.syncPanel()
    this.scrollActiveOption()
  }

  private onKeydown = (event: KeyboardEvent): void => {
    if (event.key === 'Tab') {
      if (this.isOpen) this.closeFilter()
      return
    }
    if (event.key === 'Escape') {
      if (!this.isOpen) return
      event.preventDefault(); this.closeFilter(true); return
    }
    if (!this.isOpen) {
      if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
      event.preventDefault(); this.openFilter(); return
    }
    if (event.key === 'ArrowDown') {
      event.preventDefault(); this.moveActive(1)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault(); this.moveActive(-1)
    } else if (event.key === 'Home') {
      event.preventDefault(); this.activeIndex = 0; this.syncControl(); this.syncPanel(); this.scrollActiveOption()
    } else if (event.key === 'End') {
      event.preventDefault(); this.activeIndex = Math.max(this.optionList.length - 1, 0); this.syncControl(); this.syncPanel(); this.scrollActiveOption()
    } else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      const option = this.optionList[this.activeIndex]
      if (option) this.selectOption(option)
    }
  }

  private renderPanel(): void {
    if (!this.isOpen) return
    this.panel?.remove()
    const panel = document.createElement('div')
    panel.className = 'k-menu k-table__filter-panel'

    const list = document.createElement('ul')
    list.id = this.listboxID
    list.className = 'k-table__filter-options'
    list.setAttribute('role', 'listbox')
    list.setAttribute('aria-label', this.currentLabel)
    this.optionList.forEach((option, index) => {
      const item = document.createElement('li')
      item.id = this.optionID(index)
      item.className = 'k-table__filter-option'
      item.setAttribute('role', 'option')
      item.setAttribute('aria-selected', option.value === this.currentValue ? 'true' : 'false')
      item.addEventListener('mouseenter', () => { this.activeIndex = index; this.syncControl(); this.syncPanel() })
      item.addEventListener('mousedown', (event) => event.preventDefault())
      item.addEventListener('click', () => this.selectOption(option))
      const check = document.createElement('span')
      check.innerHTML = ic('check')
      item.append(check.firstElementChild || check)
      const text = document.createElement('span')
      text.textContent = option.label
      item.append(text)
      list.append(item)
    })
    panel.append(list)
    document.body.append(panel)
    this.panel = panel
    this.syncPanel()
    this.updatePanelPosition()
  }

  private syncPanel(): void {
    this.panel?.querySelectorAll<HTMLElement>('[role="option"]').forEach((item, index) => {
      item.classList.toggle('is-active', index === this.activeIndex)
      item.setAttribute('aria-selected', this.optionList[index]?.value === this.currentValue ? 'true' : 'false')
    })
  }

  private updatePanelPosition(): void {
    if (!this.root || !this.panel) return
    const rect = this.root.getBoundingClientRect()
    const viewportWidth = window.innerWidth || document.documentElement.clientWidth || 0
    const viewportHeight = window.innerHeight || document.documentElement.clientHeight || 0
    const gap = 6
    const edge = 8
    const width = Math.min(Math.max(rect.width, 160), Math.max(viewportWidth - edge * 2, 0))
    const left = Math.min(Math.max(rect.left, edge), Math.max(viewportWidth - width - edge, edge))
    const roomBelow = viewportHeight - rect.bottom - gap - edge
    const roomAbove = rect.top - gap - edge
    const opensAbove = roomBelow < 220 && roomAbove > roomBelow
    const availableHeight = Math.min(320, Math.max(opensAbove ? roomAbove : roomBelow, 0))
    this.panel.style.setProperty('--k-table-filter-panel-max-height', `${availableHeight}px`)
    Object.assign(this.panel.style, opensAbove
      ? { top: '', bottom: `${viewportHeight - rect.top + gap}px`, left: `${left}px`, width: `${width}px` }
      : { top: `${rect.bottom + gap}px`, bottom: '', left: `${left}px`, width: `${width}px` })
  }

  private scrollActiveOption(): void {
    queueMicrotask(() => document.getElementById(this.optionID(this.activeIndex))?.scrollIntoView?.({ block: 'nearest' }))
  }

  private onDocumentPointerDown = (event: Event): void => {
    if (!this.isOpen) return
    const target = event.target as Node | null
    if (target && (this.contains(target) || this.panel?.contains(target))) return
    this.closeFilter()
  }

  private onDocumentFocusIn = (event: FocusEvent): void => {
    if (!this.isOpen) return
    const target = event.target as Node | null
    if (target && (this.contains(target) || this.panel?.contains(target))) return
    this.closeFilter()
  }

  private onViewportChange = (): void => { if (this.isOpen) this.updatePanelPosition() }
}

if (typeof customElements !== 'undefined' && !customElements.get('faros-resource-table-filter')) {
  customElements.define('faros-resource-table-filter', ResourceTableFilterElement)
}

declare global {
  interface HTMLElementTagNameMap {
    'faros-resource-table-filter': ResourceTableFilterElement
  }
}
