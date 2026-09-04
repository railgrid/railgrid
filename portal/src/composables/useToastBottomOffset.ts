/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/** The visual gap used by both toast renderers when no chrome is present. */
export const TOAST_EDGE_GAP_PX = 16

/** The rendered height of the minimized TerminalDock header. */
export const MINIMIZED_TERMINAL_HEIGHT_PX = 36

export interface ToastBottomOffsetInput {
  /** The bottom inset published by the active navigation dock. */
  navigationBottom: string
  /** Whether the TerminalDock is currently mounted and exposed. */
  terminalVisible: boolean
  /** Session count gates the TerminalDock's v-if. */
  terminalSessionCount: number
  terminalHeight: number
  terminalMinimized: boolean
  terminalFullscreen: boolean
}

/**
 * Convert the layout-inset contract to pixels. The navigation composable
 * publishes px values today; invalid or future non-px values fail closed to
 * zero so a malformed preference cannot move notifications off-screen.
 */
export function parsePixelLength(value: string): number {
  const match = /^(-?\d+(?:\.\d+)?)px$/i.exec(value.trim())
  if (!match) return 0
  const parsed = Number(match[1])
  return Number.isFinite(parsed) ? Math.max(0, parsed) : 0
}

/**
 * Compute the root toast clearance from the active shell chrome.
 *
 * A fullscreen terminal is an overlay that reaches the viewport bottom, so
 * there is no useful empty strip in which to park a toast. Keep the normal
 * edge gap (and any bottom navigation inset) in that mode; the toast layer is
 * intentionally above the terminal's z-index and remains reachable.
 */
export function toastBottomOffsetPx(input: ToastBottomOffsetInput): number {
  const navigationBottom = parsePixelLength(input.navigationBottom)
  const hasTerminal = input.terminalVisible && input.terminalSessionCount > 0
  if (!hasTerminal || input.terminalFullscreen) {
    return navigationBottom + TOAST_EDGE_GAP_PX
  }

  const terminalHeight = input.terminalMinimized
    ? MINIMIZED_TERMINAL_HEIGHT_PX
    : Number.isFinite(input.terminalHeight) ? Math.max(0, input.terminalHeight) : 0
  return navigationBottom + terminalHeight + TOAST_EDGE_GAP_PX
}
