export interface OverlaySize { width: number; height: number }

// Rebase on each pointer movement, including at a limit, so reversing direction
// immediately moves the edge instead of unwinding an invisible overshoot.
export function resizeOverlay(size: OverlaySize, dx: number, dy: number, bounds: OverlaySize): OverlaySize {
  const clamp = (value: number, minimum: number, maximum: number) => Math.min(maximum, Math.max(Math.min(minimum, maximum), value))
  return {
    width: clamp(Math.min(size.width, bounds.width) - 2 * dx, 320, bounds.width),
    height: clamp(Math.min(size.height, bounds.height) - dy, 280, bounds.height),
  }
}

// A right-docked panel has one moving edge. Keep at least 360px for the
// workspace on desktop after navigation; narrow viewports fill the remaining width.
export function resizeSidePanel(width: number, dx: number, viewportWidth: number, navigationWidth = 0): number {
  const available = Math.max(0, viewportWidth - navigationWidth)
  if (viewportWidth < 768) return available
  const maximum = Math.max(0, Math.min(available * 0.6, available - 360))
  return Math.min(maximum, Math.max(320, Math.min(width, maximum) - dx))
}
