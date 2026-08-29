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

import { computed, nextTick, onMounted, onUnmounted, ref, watch, watchEffect, type ComputedRef, type Ref, type StyleValue } from 'vue'
import { setLayoutInsets } from '@/composables/useLayoutInsets'

export type DockMode = 'float' | 'left' | 'right' | 'top' | 'bottom'

export interface DockState {
  mode: DockMode
  x: number
  y: number
}

export interface NavigationDock {
  dockState: Ref<DockState>
  floatRef: Ref<HTMLElement | null>
  dockedRef: Ref<HTMLElement | null>
  isDragging: Ref<boolean>
  nearEdge: Ref<DockMode | null>
  isDocked: ComputedRef<boolean>
  isVerticalDock: ComputedRef<boolean>
  isHorizontalDock: ComputedRef<boolean>
  showFloat: ComputedRef<boolean>
  isDefaultFloat: ComputedRef<boolean>
  hasCustomPos: ComputedRef<boolean>
  floatStyle: ComputedRef<StyleValue>
  layoutClass: ComputedRef<string>
  layoutInsetsStyle: ComputedRef<Record<string, string>>
  onDragStart: (event: PointerEvent) => void
  onDragHandleKeydown: (event: KeyboardEvent) => void
  resetDockPos: () => void
}

/** Persisted coordinates are only safe to clamp after validating their type and finiteness. */
export function isFiniteDockPosition(value: Pick<DockState, 'x' | 'y'>): boolean {
  return typeof value.x === 'number'
    && Number.isFinite(value.x)
    && typeof value.y === 'number'
    && Number.isFinite(value.y)
}

/** Keep an absolute dock position fully inside the available viewport. */
export function clampDockPosition(
  position: Pick<DockState, 'x' | 'y'>,
  viewport: { width: number; height: number },
  dock: { width: number; height: number },
): { x: number; y: number } {
  const maxX = Math.max(0, viewport.width - Math.max(0, dock.width))
  const maxY = Math.max(0, viewport.height - Math.max(0, dock.height))
  return {
    x: Math.max(0, Math.min(maxX, position.x)),
    y: Math.max(0, Math.min(maxY, position.y)),
  }
}

const DOCK_STORAGE_KEY = 'faros-dock-state'
const SNAP_THRESHOLD = 80

function browserStorage(): Storage | null {
  try {
    if (typeof window === 'undefined') return null
    return window.localStorage
  } catch {
    return null
  }
}

function loadDockState(): DockState {
  try {
    const raw = browserStorage()?.getItem(DOCK_STORAGE_KEY)
    if (!raw) return { mode: 'left', x: -1, y: -1 }
    const state = JSON.parse(raw) as DockState
    if (['left', 'right', 'top', 'bottom'].includes(state.mode)) {
      return { mode: state.mode, x: -1, y: -1 }
    }
    if (state.mode === 'float') {
      // Preserve float mode across refreshes while clamping only its position
      // to the current viewport. Invalid positions intentionally park at the
      // default floating location instead of changing the chosen mode.
      if (!isFiniteDockPosition(state) || state.x < 0 || state.y < 0) {
        return { mode: 'float', x: -1, y: -1 }
      }
      return {
        mode: 'float',
        ...clampDockPosition(
          state,
          { width: window.innerWidth, height: window.innerHeight },
          { width: 300, height: 48 },
        ),
      }
    }
  } catch { /* ignore unavailable, malformed, or restricted storage */ }
  return { mode: 'left', x: -1, y: -1 }
}

export function useNavigationDock(sidebarExpanded: Ref<boolean>): NavigationDock {
  const floatRef = ref<HTMLElement | null>(null)
  const dockedRef = ref<HTMLElement | null>(null)
  const isDragging = ref(false)
  const nearEdge = ref<DockMode | null>(null)
  const dockState = ref<DockState>(loadDockState())

  const isDocked = computed(() => !isDragging.value && dockState.value.mode !== 'float')
  const isVerticalDock = computed(() => isDocked.value && (dockState.value.mode === 'left' || dockState.value.mode === 'right'))
  const isHorizontalDock = computed(() => isDocked.value && (dockState.value.mode === 'top' || dockState.value.mode === 'bottom'))
  const showFloat = computed(() => !isDocked.value)

  let dragOffset = { x: 0, y: 0 }
  let dragSize = { w: 300, h: 48 }
  let activePointerId: number | null = null
  let floatResizeObserver: ResizeObserver | null = null
  let clampFrame: number | null = null
  let disposed = false
  const dragPos = ref<{ x: number; y: number }>({ x: 0, y: 0 })

  function saveDockState(): void {
    try {
      browserStorage()?.setItem(DOCK_STORAGE_KEY, JSON.stringify(dockState.value))
    } catch { /* ignore unavailable or quota-limited storage */ }
  }

  function onDragStart(event: PointerEvent): void {
    if (event.button !== 0 || activePointerId !== null) return
    const element = dockedRef.value || floatRef.value
    if (!element) return

    const rect = element.getBoundingClientRect()
    dragOffset.x = event.clientX - rect.left
    dragOffset.y = event.clientY - rect.top

    activePointerId = event.pointerId
    isDragging.value = true

    nextTick(() => {
      const floatingElement = floatRef.value
      if (floatingElement) {
        dragSize.w = floatingElement.offsetWidth
        dragSize.h = floatingElement.offsetHeight
      }
    })

    dragPos.value = {
      x: Math.max(0, event.clientX - dragOffset.x),
      y: Math.max(0, event.clientY - dragOffset.y),
    }

    event.preventDefault()
  }

  function onDragMove(event: PointerEvent): void {
    if (!isDragging.value || (activePointerId !== null && event.pointerId !== activePointerId)) return

    const x = Math.max(0, Math.min(window.innerWidth - dragSize.w, event.clientX - dragOffset.x))
    const y = Math.max(0, Math.min(window.innerHeight - dragSize.h, event.clientY - dragOffset.y))
    dragPos.value = { x, y }

    const distL = event.clientX
    const distR = window.innerWidth - event.clientX
    const distT = event.clientY
    const distB = window.innerHeight - event.clientY
    const minDist = Math.min(distL, distR, distT, distB)

    if (minDist < SNAP_THRESHOLD) {
      if (minDist === distL) nearEdge.value = 'left'
      else if (minDist === distR) nearEdge.value = 'right'
      else if (minDist === distT) nearEdge.value = 'top'
      else nearEdge.value = 'bottom'
    } else {
      nearEdge.value = null
    }
  }

  function onDragEnd(event?: PointerEvent): void {
    if (!isDragging.value || (event && activePointerId !== null && event.pointerId !== activePointerId)) return

    if (nearEdge.value) {
      dockState.value = { mode: nearEdge.value, x: -1, y: -1 }
    } else {
      dockState.value = { mode: 'float', x: dragPos.value.x, y: dragPos.value.y }
    }

    isDragging.value = false
    nearEdge.value = null
    activePointerId = null
    saveDockState()
  }

  function clampFloatPosition(x: number, y: number): { x: number; y: number } {
    return clampDockPosition(
      { x, y },
      { width: window.innerWidth, height: window.innerHeight },
      { width: dragSize.w, height: dragSize.h },
    )
  }

  function measureFloatingDock(): void {
    const element = floatRef.value
    if (!element) return
    if (element.offsetWidth > 0) dragSize.w = element.offsetWidth
    if (element.offsetHeight > 0) dragSize.h = element.offsetHeight
  }

  function reclampFloatingDock(): void {
    if (disposed) return
    measureFloatingDock()

    if (isDragging.value) {
      dragPos.value = clampFloatPosition(dragPos.value.x, dragPos.value.y)
      return
    }
    if (dockState.value.mode !== 'float' || dockState.value.x < 0) return

    const position = clampFloatPosition(dockState.value.x, dockState.value.y)
    if (position.x === dockState.value.x && position.y === dockState.value.y) return
    dockState.value = { mode: 'float', ...position }
    saveDockState()
  }

  function scheduleFloatingDockClamp(): void {
    if (disposed || clampFrame !== null) return
    if (typeof window.requestAnimationFrame === 'function') {
      clampFrame = window.requestAnimationFrame(() => {
        clampFrame = null
        reclampFloatingDock()
      })
      return
    }
    void nextTick(reclampFloatingDock)
  }

  function observeFloatingDock(element: HTMLElement | null): void {
    floatResizeObserver?.disconnect()
    floatResizeObserver = null
    if (element && typeof ResizeObserver !== 'undefined') {
      floatResizeObserver = new ResizeObserver(scheduleFloatingDockClamp)
      floatResizeObserver.observe(element)
    }
    if (element) scheduleFloatingDockClamp()
  }

  function currentDockPosition(): { x: number; y: number } {
    const rect = (dockedRef.value || floatRef.value)?.getBoundingClientRect()
    if (rect) return clampFloatPosition(rect.left, rect.top)
    return clampFloatPosition(
      (window.innerWidth - dragSize.w) / 2,
      window.innerHeight - dragSize.h - 16,
    )
  }

  function setDockMode(mode: DockMode): void {
    if (mode === 'float') {
      const position = dockState.value.mode === 'float' && dockState.value.x >= 0
        ? clampFloatPosition(dockState.value.x, dockState.value.y)
        : currentDockPosition()
      dockState.value = { mode, ...position }
    } else {
      dockState.value = { mode, x: -1, y: -1 }
    }
    saveDockState()
  }

  function moveFloatingDock(deltaX: number, deltaY: number): void {
    const position = dockState.value.mode === 'float' && dockState.value.x >= 0
      ? clampFloatPosition(dockState.value.x, dockState.value.y)
      : currentDockPosition()
    dockState.value = {
      mode: 'float',
      ...clampFloatPosition(position.x + deltaX, position.y + deltaY),
    }
    saveDockState()
  }

  function onDragHandleKeydown(event: KeyboardEvent): void {
    const edgeByKey: Record<string, DockMode> = {
      ArrowLeft: 'left',
      ArrowRight: 'right',
      ArrowUp: 'top',
      ArrowDown: 'bottom',
    }

    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      setDockMode('float')
      return
    }
    if (event.key === 'Home') {
      event.preventDefault()
      setDockMode('left')
      return
    }
    if (event.key === 'End') {
      event.preventDefault()
      setDockMode('right')
      return
    }
    if (event.key === 'PageUp') {
      event.preventDefault()
      setDockMode('top')
      return
    }
    if (event.key === 'PageDown') {
      event.preventDefault()
      setDockMode('bottom')
      return
    }

    const edge = edgeByKey[event.key]
    if (!edge) return
    event.preventDefault()
    if (event.shiftKey) setDockMode(edge)
    else if (dockState.value.mode === 'float') moveFloatingDock(
      edge === 'left' ? -32 : edge === 'right' ? 32 : 0,
      edge === 'top' ? -32 : edge === 'bottom' ? 32 : 0,
    )
    else setDockMode(edge)
  }

  function resetDockPos(): void {
    dockState.value = { mode: 'float', x: -1, y: -1 }
    saveDockState()
  }

  watch(floatRef, observeFloatingDock)

  onMounted(() => {
    disposed = false
    window.addEventListener('pointermove', onDragMove)
    window.addEventListener('pointerup', onDragEnd)
    window.addEventListener('pointercancel', onDragEnd)
    window.addEventListener('resize', scheduleFloatingDockClamp)
    window.addEventListener('orientationchange', scheduleFloatingDockClamp)
    observeFloatingDock(floatRef.value)
  })

  onUnmounted(() => {
    disposed = true
    window.removeEventListener('pointermove', onDragMove)
    window.removeEventListener('pointerup', onDragEnd)
    window.removeEventListener('pointercancel', onDragEnd)
    window.removeEventListener('resize', scheduleFloatingDockClamp)
    window.removeEventListener('orientationchange', scheduleFloatingDockClamp)
    floatResizeObserver?.disconnect()
    floatResizeObserver = null
    if (clampFrame !== null && typeof window.cancelAnimationFrame === 'function') {
      window.cancelAnimationFrame(clampFrame)
      clampFrame = null
    }
    // TerminalDock outlives routed layouts. Never carry this page's dock
    // clearance into standalone shells such as platform admin or login.
    setLayoutInsets({ left: '0px', right: '0px', bottom: '0px' })
  })

  const isDefaultFloat = computed(() => !isDragging.value && dockState.value.mode === 'float' && dockState.value.x < 0)
  const hasCustomPos = computed(() => dockState.value.mode !== 'float' || dockState.value.x >= 0)

  const floatStyle = computed<StyleValue>(() => {
    if (isDragging.value) {
      return { left: `${dragPos.value.x}px`, top: `${dragPos.value.y}px` }
    }
    if (dockState.value.mode === 'float' && dockState.value.x >= 0) {
      return { left: `${dockState.value.x}px`, top: `${dockState.value.y}px` }
    }
    return {}
  })

  const layoutClass = computed(() => {
    if (isVerticalDock.value) return 'flex-row'
    return 'flex-col'
  })

  const layoutInsetsStyle = computed<Record<string, string>>(() => {
    const railWidth = sidebarExpanded.value ? '12rem' : '3.5rem'
    const left = isVerticalDock.value && dockState.value.mode === 'left' ? railWidth : '0px'
    const right = isVerticalDock.value && dockState.value.mode === 'right' ? railWidth : '0px'
    const bottom = isHorizontalDock.value && dockState.value.mode === 'bottom' ? '44px' : '0px'
    return {
      '--app-inset-left': left,
      '--app-inset-right': right,
      '--app-inset-bottom': bottom,
    }
  })

  // AppLayout's DOM cannot pass these CSS variables to the app-level terminal
  // dock, so publish the same reactive inset contract as the former inline
  // implementation.
  watchEffect(() => {
    setLayoutInsets({
      left: layoutInsetsStyle.value['--app-inset-left'],
      right: layoutInsetsStyle.value['--app-inset-right'],
      bottom: layoutInsetsStyle.value['--app-inset-bottom'],
    })
  })

  return {
    dockState,
    floatRef,
    dockedRef,
    isDragging,
    nearEdge,
    isDocked,
    isVerticalDock,
    isHorizontalDock,
    showFloat,
    isDefaultFloat,
    hasCustomPos,
    floatStyle,
    layoutClass,
    layoutInsetsStyle,
    onDragStart,
    onDragHandleKeydown,
    resetDockPos,
  }
}
