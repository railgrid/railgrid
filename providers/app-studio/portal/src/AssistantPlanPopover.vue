<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ChevronUp, ClipboardList, X } from 'lucide-vue-next'
import {
  assistantPlanProgress,
  assistantPlanSummary,
  type AssistantPlan,
} from './assistantPlan'
import AssistantPlanSteps from './AssistantPlanSteps.vue'

const props = defineProps<{ messageId: string; plan: AssistantPlan }>()
const rootRef = ref<HTMLElement | null>(null)
const desktopTriggerRef = ref<HTMLButtonElement | null>(null)
const mobileTriggerRef = ref<HTMLButtonElement | null>(null)
const mobileCloseRef = ref<HTMLButtonElement | null>(null)
const mobileSheetRef = ref<HTMLElement | null>(null)
const hovered = ref(false)
const focused = ref(false)
const pinned = ref(false)
const dismissed = ref(false)
const mobileOpen = ref(false)
const mounted = ref(false)
let openTimer: ReturnType<typeof window.setTimeout> | undefined
let closeTimer: ReturnType<typeof window.setTimeout> | undefined
let previousBodyOverflow = ''
let inertRoot: HTMLElement | null = null
let desktopMedia: MediaQueryList | null = null

const desktopOpen = computed(() => !dismissed.value && (hovered.value || focused.value || pinned.value))
const progress = computed(() => assistantPlanProgress(props.plan))
const panelID = `app-studio-assistant-plan-${props.messageId.replace(/[^a-zA-Z0-9_-]/g, '-')}`
const mobilePanelID = `${panelID}-mobile`
const titleID = `${panelID}-title`

function clearTimers() {
  if (openTimer !== undefined) window.clearTimeout(openTimer)
  if (closeTimer !== undefined) window.clearTimeout(closeTimer)
  openTimer = undefined
  closeTimer = undefined
}

function supportsHover(): boolean {
  return window.matchMedia('(hover: hover) and (pointer: fine)').matches
}

function scheduleHoverOpen() {
  if (!supportsHover()) return
  if (closeTimer !== undefined) window.clearTimeout(closeTimer)
  openTimer = window.setTimeout(() => {
    dismissed.value = false
    hovered.value = true
  }, 150)
}

function scheduleHoverClose() {
  if (!supportsHover()) return
  if (openTimer !== undefined) window.clearTimeout(openTimer)
  closeTimer = window.setTimeout(() => {
    hovered.value = false
  }, 250)
}

function onFocusIn() {
  dismissed.value = false
  focused.value = true
}

function onFocusOut(event: FocusEvent) {
  if (!rootRef.value?.contains(event.relatedTarget as Node | null)) focused.value = false
}

function togglePinned() {
  if (pinned.value) {
    dismissed.value = true
    pinned.value = false
    hovered.value = false
    return
  }
  dismissed.value = false
  pinned.value = true
}

function closeDesktop(restoreFocus = false) {
  clearTimers()
  hovered.value = false
  focused.value = false
  pinned.value = false
  dismissed.value = true
  if (restoreFocus) void nextTick(() => desktopTriggerRef.value?.focus())
}

function setWorkspaceInert(inert: boolean) {
  if (inert) {
    inertRoot = document.querySelector<HTMLElement>('[data-app-studio-workspace]')
    if (inertRoot) inertRoot.inert = true
    previousBodyOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return
  }
  if (inertRoot) inertRoot.inert = false
  inertRoot = null
  document.body.style.overflow = previousBodyOverflow
}

function openMobile() {
  mobileOpen.value = true
  setWorkspaceInert(true)
  void nextTick(() => mobileCloseRef.value?.focus())
}

function closeMobile(restoreFocus = true) {
  if (!mobileOpen.value) return
  mobileOpen.value = false
  setWorkspaceInert(false)
  if (restoreFocus) void nextTick(() => mobileTriggerRef.value?.focus())
}

function onDesktopBreakpoint(event: MediaQueryListEvent) {
  if (event.matches) closeMobile(false)
}

function onDocumentPointerDown(event: PointerEvent) {
  if (pinned.value && !rootRef.value?.contains(event.target as Node)) closeDesktop()
}

function onDocumentKeyDown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    if (mobileOpen.value) {
      event.preventDefault()
      closeMobile()
    } else if (desktopOpen.value) {
      event.preventDefault()
      closeDesktop(true)
    }
    return
  }
  if (!mobileOpen.value || event.key !== 'Tab') return
  const focusable = Array.from(
    mobileSheetRef.value?.querySelectorAll<HTMLElement>('button:not([disabled]), [href], [tabindex]:not([tabindex="-1"])') ?? [],
  )
  if (focusable.length === 0) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

onMounted(() => {
  mounted.value = true
  desktopMedia = window.matchMedia('(min-width: 768px)')
  desktopMedia.addEventListener('change', onDesktopBreakpoint)
  document.addEventListener('pointerdown', onDocumentPointerDown)
  document.addEventListener('keydown', onDocumentKeyDown)
})

onBeforeUnmount(() => {
  clearTimers()
  document.removeEventListener('pointerdown', onDocumentPointerDown)
  document.removeEventListener('keydown', onDocumentKeyDown)
  desktopMedia?.removeEventListener('change', onDesktopBreakpoint)
  desktopMedia = null
  if (mobileOpen.value) setWorkspaceInert(false)
})
</script>

<template>
  <div
    ref="rootRef"
    class="absolute bottom-3 right-4 z-30 hidden md:block"
    @pointerenter="scheduleHoverOpen"
    @pointerleave="scheduleHoverClose"
    @focusin="onFocusIn"
    @focusout="onFocusOut"
  >
    <div
      v-show="desktopOpen"
      :id="panelID"
      class="absolute bottom-full right-0 mb-2 w-[min(320px,calc(100vw-2rem))] max-h-[min(50vh,360px)] overflow-auto rounded-xl border border-border-subtle bg-surface-raised p-2 shadow-2xl"
      role="region"
      :aria-labelledby="titleID"
    >
      <div :id="titleID" class="px-2 pb-1.5 pt-1 text-[11px] font-semibold uppercase tracking-[0.12em] text-text-muted">
        {{ progress.completed }} of {{ progress.total }} steps
      </div>
      <AssistantPlanSteps :message-id="`${messageId}-desktop`" :plan="plan" />
    </div>
    <button
      ref="desktopTriggerRef"
      type="button"
      class="inline-flex min-h-11 max-w-[min(360px,calc(100vw-2rem))] items-center gap-2 rounded-md border border-border-subtle bg-surface-raised/95 px-3 text-[12px] text-text-secondary shadow-lg backdrop-blur transition hover:border-accent/30 hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 motion-reduce:transition-none"
      :aria-expanded="desktopOpen"
      :aria-controls="panelID"
      @click="togglePinned"
    >
      <ClipboardList class="h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="1.75" />
      <span class="min-w-0 truncate font-medium text-text-primary">{{ assistantPlanSummary(plan) }}</span>
      <ChevronUp class="h-3.5 w-3.5 shrink-0 transition-transform motion-reduce:transition-none" :class="desktopOpen ? 'rotate-180' : ''" :stroke-width="1.75" />
    </button>
  </div>

  <Teleport v-if="mounted" to="#assistant-plan-mobile-anchor">
    <button
      ref="mobileTriggerRef"
      type="button"
      class="inline-flex min-h-11 max-w-full items-center gap-2 rounded-md border border-border-subtle bg-surface-raised px-3 text-[12px] text-text-secondary shadow-sm md:hidden"
      :aria-expanded="mobileOpen"
      :aria-controls="mobilePanelID"
      @click="openMobile"
    >
      <ClipboardList class="h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="1.75" />
      <span class="min-w-0 truncate font-medium text-text-primary">{{ assistantPlanSummary(plan) }}</span>
    </button>
  </Teleport>

  <Teleport v-if="mounted && mobileOpen" to="#app-studio-overlay-root">
    <div class="fixed inset-0 z-[100] flex items-end bg-surface/70 backdrop-blur-sm md:hidden" @pointerdown.self="closeMobile()">
      <section
        :id="mobilePanelID"
        ref="mobileSheetRef"
        class="flex max-h-[75vh] w-full flex-col rounded-t-lg border border-border-subtle bg-surface-raised shadow-2xl"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="`${mobilePanelID}-title`"
      >
        <header class="flex min-h-14 items-center justify-between gap-3 border-b border-border-subtle px-4">
          <div :id="`${mobilePanelID}-title`" class="text-[13px] font-semibold text-text-primary">
            {{ progress.completed }} of {{ progress.total }} steps
          </div>
          <button
            ref="mobileCloseRef"
            type="button"
            class="flex h-11 w-11 items-center justify-center rounded-md text-text-muted hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            aria-label="Close plan"
            @click="closeMobile()"
          >
            <X class="h-4 w-4" :stroke-width="1.75" />
          </button>
        </header>
        <AssistantPlanSteps :message-id="`${messageId}-mobile`" :plan="plan" mobile />
      </section>
    </div>
  </Teleport>
</template>
