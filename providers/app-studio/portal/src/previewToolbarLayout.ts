export const PREVIEW_TOOLBAR_SECONDARY_COLLAPSE_WIDTH = 520
export const PREVIEW_TOOLBAR_ALL_COLLAPSE_WIDTH = 380

export type PreviewToolbarLayout = 'expanded' | 'compact' | 'collapsed'

export function previewToolbarLayout(width: number): PreviewToolbarLayout {
  if (!Number.isFinite(width) || width <= PREVIEW_TOOLBAR_ALL_COLLAPSE_WIDTH) {
    return 'collapsed'
  }
  if (width <= PREVIEW_TOOLBAR_SECONDARY_COLLAPSE_WIDTH) return 'compact'
  return 'expanded'
}
