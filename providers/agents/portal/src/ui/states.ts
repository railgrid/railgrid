// Distinct loading / error / empty renderings for a store slice. Every list in
// the app funnels through this so a failing API can never masquerade as "you
// have nothing yet".

import { html, type TemplateResult } from 'lit'
import type { Slice } from '../store'
import { icon, type IconName } from './icon'

export interface SliceViewOptions<T> {
  slice: Slice<T[]>
  // emptyIcon/emptyText render when the load succeeded and returned nothing.
  emptyIcon: IconName
  emptyText: string
  // retry re-runs the loader from the error state.
  retry: () => void
  // content renders the non-empty case.
  content: (rows: T[]) => TemplateResult
}

export function sliceView<T>(o: SliceViewOptions<T>): TemplateResult {
  const s = o.slice
  // Once a slice has produced an authoritative snapshot, a later background
  // failure must not replace it with a full-page error. Keep populated *and*
  // empty snapshots stable and add a compact stale-data notice instead.
  if (s.error && s.hasSnapshot) {
    const content = s.data.length ? o.content(s.data) : emptyState(o.emptyIcon, o.emptyText)
    return html`${staleState(s.error, o.retry)}${content}`
  }
  if (s.error) return errorState(s.error, o.retry)
  if (!s.loaded && s.loading) return loadingState()
  if (!s.data.length) return emptyState(o.emptyIcon, o.emptyText)
  return o.content(s.data)
}

export function loadingState(label = 'Loading…'): TemplateResult {
  return html`<div class="k-card agents-state agents-state-loading" role="status">
    <span class="agents-spinner" aria-hidden="true"></span> ${label}
  </div>`
}

export function emptyState(name: IconName, text: string): TemplateResult {
  return html`<div class="k-card agents-state agents-state-empty" role="status">${icon(name)} ${text}</div>`
}

export function errorState(message: string, retry?: () => void): TemplateResult {
  return html`<div class="k-card agents-state agents-state-error" role="alert">
    <span>${icon('x')} ${message}</span>
    ${retry ? html`<button class="k-btn k-btn--ghost secondary" @click=${retry}>${icon('refresh')} Retry</button>` : null}
  </div>`
}

export function staleState(message: string, retry?: () => void): TemplateResult {
  return html`<div class="k-card agents-state agents-state-error" role="status" aria-live="polite">
    <span>${icon('x')} Could not refresh. Showing the last loaded data. ${message}</span>
    ${retry ? html`<button class="k-btn k-btn--ghost secondary" @click=${retry}>${icon('refresh')} Retry</button>` : null}
  </div>`
}
