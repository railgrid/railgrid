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
