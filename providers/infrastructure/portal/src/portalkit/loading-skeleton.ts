/** Delayed first-read placeholder for portals that render HTML strings. */
export function loadingSkeletonMarkup(label: string, { compact = false } = {}): string {
  const text = label.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  const rows = '<div class="k-loading-shimmer"></div>'.repeat(compact ? 2 : 4)
  return `<div>
    <span class="sr-only k-loading-skeleton__status" role="status">${text}</span>
    <div class="k-loading-skeleton k-loading-reveal${compact ? ' k-loading-skeleton--compact' : ''}" aria-hidden="true">
      <div class="k-loading-skeleton__heading k-loading-shimmer"></div>
      <div class="k-loading-skeleton__panel">${rows}</div>
    </div>
  </div>`
}
