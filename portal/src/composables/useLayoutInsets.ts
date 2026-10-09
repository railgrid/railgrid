/*
Copyright 2026 The Railgrid Authors.

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

import { reactive, readonly } from 'vue'

// Shared, app-lifetime insets describing how much room the nav chrome
// (side/bottom docks) occupies. AppLayout is the single writer; the
// persistent TerminalDock (mounted at the app root, *outside* AppLayout's
// DOM subtree) is a reader. A plain reactive singleton is used instead of
// CSS custom properties because the dock lives above the router-view
// boundary and can't reliably inherit vars set inside a per-page component.
export interface LayoutInsets {
  left: string
  right: string
  bottom: string
}

export interface LayoutInsetsClaim {
  /** Publish insets. Ignored once a newer claim has taken over. */
  set(next: LayoutInsets): void
  /** Clear the insets, but only while this claim is still the live writer. */
  release(): void
}

const ZERO: LayoutInsets = { left: '0px', right: '0px', bottom: '0px' }

const state = reactive<LayoutInsets>({ ...ZERO })

function assign(next: LayoutInsets) {
  state.left = next.left
  state.right = next.right
  state.bottom = next.bottom
}

let nextOwner = 0
let activeOwner = 0

// Every page renders its own AppLayout inside <router-view>, so a navigation
// swaps one AppLayout instance for another. Vue runs the outgoing instance's
// `onUnmounted` hooks post-flush, *after* the incoming instance's setup has
// already published its insets; a plain "reset on unmount" therefore wiped
// the live sidebar width and left the TerminalDock sliding under the rail.
// Claims make the writer explicit: the newest claim owns the state, and a
// stale one can neither overwrite nor clear it.
export function claimLayoutInsets(): LayoutInsetsClaim {
  const owner = ++nextOwner
  activeOwner = owner
  return {
    set(next) {
      if (activeOwner !== owner) return
      assign(next)
    },
    release() {
      if (activeOwner !== owner) return
      activeOwner = 0
      assign(ZERO)
    },
  }
}

export function useLayoutInsets() {
  return readonly(state)
}
