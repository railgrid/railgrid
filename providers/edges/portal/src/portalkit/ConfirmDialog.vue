<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     Mount ONE instance at the app root; it renders whenever confirmDialog()
     sets confirmState.open. Native button activation follows the focused action;
     Escape/backdrop cancels. Styles are
     self-injected + token-based, so the component drops into any Vue provider
     portal (Tailwind or plain-CSS) without an extracted CSS asset. -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { Info, TriangleAlert } from 'lucide-vue-next'
import { confirmState, resolveConfirm } from './confirm'
import { ensureRailgridUIStyles } from '../portalkit/styles'

// Standalone provider portals load the exact canonical recipe through the
// shared helper; the host portal already imports the same railgrid-ui.css file.
ensureRailgridUIStyles()

const cancelBtn = ref<HTMLButtonElement | null>(null)
const confirmBtn = ref<HTMLButtonElement | null>(null)
const verificationInput = ref<HTMLInputElement | null>(null)
const verification = ref('')
const composingVerification = ref(false)
const modalRef = ref<HTMLElement | null>(null)
const dialogRef = ref<HTMLDialogElement | null>(null)
const instanceID = useId()
const titleID = `k-confirm-title-${instanceID}`
const messageID = `k-confirm-message-${instanceID}`
const verificationID = `k-confirm-verification-${instanceID}`
const irreversibleID = `k-confirm-irreversible-${instanceID}`

// Render the message as discrete paragraphs so a multi-line message reads
// cleanly instead of as one run-on line.
const paragraphs = computed(() =>
  confirmState.message.split('\n').map((s) => s.trim()).filter(Boolean),
)

function onConfirm() {
  if (confirmState.verificationText && verification.value !== confirmState.verificationText) return
  resolveConfirm(true)
}
function onCancel() {
  resolveConfirm(false)
}
function onNativeCancel() {
  if (!composingVerification.value) onCancel()
}
function onVerificationEnter(e: KeyboardEvent) {
  if (composingVerification.value || e.isComposing || e.keyCode === 229) return
  e.preventDefault()
  onConfirm()
}
function onKeydown(e: KeyboardEvent) {
  if (!confirmState.open) return
  const target = e.target
  if (!(target instanceof Node) || !modalRef.value?.contains(target)) return
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
    if (composingVerification.value || e.isComposing || e.keyCode === 229) return
    e.preventDefault()
    e.stopPropagation()
    onCancel()
  }
}

watch(() => confirmState.open, (open) => {
  if (!open) composingVerification.value = false
}, { flush: 'sync' })

// Native modal dialogs participate in the browser's shared top-layer stack,
// including dialogs opened by another independently bundled provider.
watch(() => confirmState.open, (open) => {
  if (!open) dialogRef.value?.close()
}, { flush: 'sync' })
onBeforeUnmount(() => {
  dialogRef.value?.close()
  if (confirmState.open) resolveConfirm(false)
})

// Replacing a request does not close the shared dialog, so reset the gate and
// focus for each request rather than only when `open` changes.
watch(() => confirmState.revision, (revision) => {
  verification.value = ''
  composingVerification.value = false
  nextTick(() => {
    if (!confirmState.open || confirmState.revision !== revision) return
    if (!dialogRef.value?.open) dialogRef.value?.showModal()
    const initial = confirmState.verificationText ? verificationInput.value : confirmState.danger ? cancelBtn.value : confirmBtn.value
    initial?.focus()
  })
}, { flush: 'sync' })
</script>

<template>
  <dialog v-if="confirmState.open" ref="dialogRef" class="k-modal-overlay" role="alertdialog" aria-modal="true"
    :aria-labelledby="titleID"
    :aria-describedby="[paragraphs.length ? messageID : '', confirmState.irreversibleMessage ? irreversibleID : ''].filter(Boolean).join(' ') || undefined"
    @click.self="onCancel" @cancel.prevent="onNativeCancel">
    <div
      ref="modalRef"
      class="k-modal k-modal--confirm"
      :class="{ 'k-modal--danger': confirmState.danger }"
      @keydown="onKeydown"
    >
      <div class="k-modal__head">
        <span v-if="!confirmState.danger" class="k-modal__icon" aria-hidden="true">
          <Info :stroke-width="1.9" />
        </span>
        <h2 :id="titleID" class="k-modal__title">{{ confirmState.title }}</h2>
      </div>
      <div v-if="paragraphs.length" :id="messageID" class="k-modal__body">
        <p v-for="(line, i) in paragraphs" :key="i" class="k-modal__message">{{ line }}</p>
      </div>
      <div v-if="confirmState.irreversibleMessage" :id="irreversibleID" class="k-modal__irreversible"><TriangleAlert :size="18" :stroke-width="1.9" aria-hidden="true" /><span>{{ confirmState.irreversibleMessage }}</span></div>
      <div v-if="confirmState.verificationText" class="k-modal__verification">
        <label :for="verificationID">To confirm, type {{ confirmState.verificationLabel || 'the name' }} <strong translate="no" dir="ltr">“{{ confirmState.verificationText }}”</strong></label>
        <input :id="verificationID" ref="verificationInput" v-model="verification" class="k-input" autocomplete="off" spellcheck="false" translate="no" dir="ltr" @compositionstart="composingVerification = true" @compositionend="composingVerification = false" @keydown.enter="onVerificationEnter" />
      </div>
      <div class="k-modal__foot">
        <button ref="cancelBtn" type="button" class="k-modal-btn k-modal-btn--cancel" @click="onCancel">{{ confirmState.cancelLabel }}</button>
        <button
          ref="confirmBtn"
          type="button"
          class="k-modal-btn k-modal-btn--confirm"
          :class="{ 'k-modal-btn--danger': confirmState.danger }"
          :disabled="!!confirmState.verificationText && verification !== confirmState.verificationText"
          @click="onConfirm"
        >{{ confirmState.confirmLabel }}</button>
      </div>
    </div>
  </dialog>
</template>
