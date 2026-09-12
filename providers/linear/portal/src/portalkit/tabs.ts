// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit the copies vendored
// into individual portals; edit here and run `make sync-portalkit`.
//
// Framework-neutral class helpers for the shared tab recipe. They keep Lit
// and string-building portals on the same markup vocabulary without making the
// kit depend on a renderer.

import { ensureFarosUIStyles } from './styles'

export interface TabClassOptions {
  active?: boolean
  disabled?: boolean
  className?: string
}

export interface TabCountClassOptions {
  attention?: boolean
  className?: string
}

function appendClass(base: string, className?: string): string {
  const extra = className?.trim()
  return extra ? `${base} ${extra}` : base
}

export function tabsClass(className?: string): string {
  ensureFarosUIStyles()
  return appendClass('k-tabs', className)
}

export function tabClass(options: TabClassOptions = {}): string {
  ensureFarosUIStyles()
  let classes = 'k-tab'
  if (options.active) classes += ' k-tab--active'
  if (options.disabled) classes += ' k-tab--disabled'
  return appendClass(classes, options.className)
}

export function tabCountClass(options: TabCountClassOptions = {}): string {
  ensureFarosUIStyles()
  let classes = 'k-tab__count'
  if (options.attention) classes += ' k-tab__count--attention'
  return appendClass(classes, options.className)
}
