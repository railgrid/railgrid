<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { confirmState, resolveConfirm } from './confirm'

// One instance is mounted at the app root; it renders whenever confirmDialog()
// sets confirmState.open. Enter confirms, Escape/backdrop cancels.
const confirmBtn = ref<HTMLButtonElement | null>(null)

// Render the message as discrete paragraphs so a multi-line message (e.g. the
// connection-switch warning) reads cleanly instead of as one run-on line.
const paragraphs = computed(() =>
  confirmState.message.split('\n').map(s => s.trim()).filter(Boolean),
)

function onConfirm() {
  resolveConfirm(true)
}
function onCancel() {
  resolveConfirm(false)
}
function onKeydown(e: KeyboardEvent) {
  if (!confirmState.open) return
  if (e.key === 'Escape') {
    e.preventDefault()
    onCancel()
  } else if (e.key === 'Enter') {
    e.preventDefault()
    onConfirm()
  }
}

// Focus the confirm button on open so keyboard users land inside the dialog;
// bind the key handler only while open.
watch(
  () => confirmState.open,
  open => {
    if (open) {
      window.addEventListener('keydown', onKeydown)
      nextTick(() => confirmBtn.value?.focus())
    } else {
      window.removeEventListener('keydown', onKeydown)
    }
  },
)
</script>

<template>
  <div v-if="confirmState.open" class="modal-overlay" @click.self="onCancel">
    <div class="modal" role="alertdialog" aria-modal="true" aria-labelledby="modal-title">
      <h3 id="modal-title" class="modal-title">{{ confirmState.title }}</h3>
      <p v-for="(line, i) in paragraphs" :key="i" class="modal-message">{{ line }}</p>
      <div class="modal-actions">
        <button class="secondary" @click="onCancel">{{ confirmState.cancelLabel }}</button>
        <button
          ref="confirmBtn"
          :class="confirmState.danger ? 'danger-solid' : 'primary'"
          @click="onConfirm"
        >{{ confirmState.confirmLabel }}</button>
      </div>
    </div>
  </div>
</template>
