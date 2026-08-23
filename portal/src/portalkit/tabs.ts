// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit the copies vendored
// into individual portals; edit here and run `make sync-portalkit`.
//
// Framework-neutral class helpers for the shared tab recipe. They keep Lit
// and string-building portals on the same markup vocabulary without making the
// kit depend on a renderer.

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
  return appendClass('pk-tabs', className)
}

export function tabClass(options: TabClassOptions = {}): string {
  let classes = 'pk-tab'
  if (options.active) classes += ' is-active'
  if (options.disabled) classes += ' is-disabled'
  return appendClass(classes, options.className)
}

export function tabCountClass(options: TabCountClassOptions = {}): string {
  let classes = 'pk-tab-count'
  if (options.attention) classes += ' is-attention'
  return appendClass(classes, options.className)
}
