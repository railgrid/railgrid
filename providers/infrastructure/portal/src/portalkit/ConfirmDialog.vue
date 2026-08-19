<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     Mount ONE instance at the app root; it renders whenever confirmDialog()
     sets confirmState.open. Enter confirms, Escape/backdrop cancels. Styles are
     self-injected + token-based, so the component drops into any Vue provider
     portal (Tailwind or plain-CSS) without an extracted CSS asset. -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { confirmState, resolveConfirm } from './confirm'
import confirmDialogStyles from './ConfirmDialog.css?raw'

// Standalone provider portals register one IIFE main.js and do not load Vite's
// extracted SFC CSS asset. Inject this canonical recipe explicitly so the
// dialog is styled in the real provider host, not only in Vite dev.
const STYLE_ID = 'faros-portalkit-confirm-dialog-css'
if (typeof document !== 'undefined') {
  let style = document.getElementById(STYLE_ID) as HTMLStyleElement | null
  if (!style) {
    style = document.createElement('style')
    style.id = STYLE_ID
    document.head.appendChild(style)
  }
  if (style.textContent !== confirmDialogStyles) style.textContent = confirmDialogStyles
}

const confirmBtn = ref<HTMLButtonElement | null>(null)
const modalRef = ref<HTMLElement | null>(null)
let previousFocus: HTMLElement | null = null

// Render the message as discrete paragraphs so a multi-line message reads
// cleanly instead of as one run-on line.
const paragraphs = computed(() =>
  confirmState.message.split('\n').map((s) => s.trim()).filter(Boolean),
)

function onConfirm() {
  resolveConfirm(true)
}
function onCancel() {
  resolveConfirm(false)
}
function onKeydown(e: KeyboardEvent) {
  if (!confirmState.open) return
  if (e.key === 'Tab') {
    const focusable = Array.from(modalRef.value?.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
    ) ?? [])
    if (focusable.length === 0) {
      e.preventDefault()
      return
    }
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
  } else if (e.key === 'Escape') {
    e.preventDefault()
    onCancel()
  } else if (e.key === 'Enter') {
    e.preventDefault()
    onConfirm()
  }
}

watch(
  () => confirmState.open,
  (open) => {
    if (open) {
      previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
      window.addEventListener('keydown', onKeydown)
      nextTick(() => confirmBtn.value?.focus())
    } else {
      window.removeEventListener('keydown', onKeydown)
      const target = previousFocus
      previousFocus = null
      nextTick(() => target?.isConnected && target.focus())
    }
  },
)

onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div v-if="confirmState.open" class="pk-confirm-overlay" @click.self="onCancel">
    <div ref="modalRef" class="pk-confirm-modal" :class="{ danger: confirmState.danger }" role="alertdialog" aria-modal="true" aria-labelledby="pk-confirm-title">
      <h3 id="pk-confirm-title" class="pk-confirm-title">{{ confirmState.title }}</h3>
      <p v-for="(line, i) in paragraphs" :key="i" class="pk-confirm-message">{{ line }}</p>
      <div class="pk-confirm-actions">
        <button type="button" class="pk-confirm-btn cancel" @click="onCancel">{{ confirmState.cancelLabel }}</button>
        <button
          ref="confirmBtn"
          type="button"
          class="pk-confirm-btn confirm"
          :class="{ danger: confirmState.danger }"
          @click="onConfirm"
        >{{ confirmState.confirmLabel }}</button>
      </div>
    </div>
  </div>
</template>
