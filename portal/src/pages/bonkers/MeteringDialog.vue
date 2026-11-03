<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import { TriangleAlert, X } from 'lucide-vue-next'

const props = defineProps<{ title: string; busy: boolean; dirty: boolean }>()
const emit = defineEmits<{ close: [] }>()
const dialog = ref<HTMLDialogElement | null>(null)
const discard = ref(false)
const titleId = useId()
const discardTitleId = useId()
const discardMessageId = useId()
const keepEditingButton = ref<HTMLButtonElement | null>(null)
let dismissalTrigger: HTMLElement | null = null
let previousFocus: HTMLElement | null = null

function requestClose(): void {
  if (props.busy) return
  if (discard.value) { keepEditing(); return }
  if (props.dirty) {
    dismissalTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
    discard.value = true
    void nextTick(() => keepEditingButton.value?.focus())
  } else emit('close')
}

function keepEditing(): void {
  discard.value = false
  void nextTick(() => { if (dismissalTrigger?.isConnected) dismissalTrigger.focus() })
}

onMounted(async () => {
  previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  dialog.value?.showModal()
  await nextTick()
  const initial = dialog.value?.querySelector<HTMLElement>('[data-initial-focus]')
    ?? dialog.value?.querySelector<HTMLElement>('.metering-dialog__body input:not([disabled]), .metering-dialog__body select:not([disabled]), .metering-dialog__body button:not([disabled])')
  initial?.focus()
})
onBeforeUnmount(() => {
  dialog.value?.close()
  const trigger = previousFocus
  void nextTick(() => { if (trigger?.isConnected) trigger.focus() })
})
</script>

<template>
  <dialog ref="dialog" class="k-modal metering-dialog" :class="{ 'k-modal--confirm k-modal--danger metering-dialog--confirm': discard }" :role="discard ? 'alertdialog' : undefined" :aria-labelledby="discard ? discardTitleId : titleId" :aria-describedby="discard ? discardMessageId : undefined" :aria-busy="busy" @cancel.prevent="requestClose">
    <template v-if="discard">
      <div class="k-modal__head">
        <span class="k-modal__icon" aria-hidden="true"><TriangleAlert :stroke-width="1.9" /></span>
        <h2 :id="discardTitleId" class="k-modal__title">Discard unsaved changes?</h2>
      </div>
      <div :id="discardMessageId" class="k-modal__body"><p class="k-modal__message">Your changes to “{{ title }}” haven’t been saved. Discarding them cannot be undone.</p></div>
      <div class="k-modal__foot">
        <button ref="keepEditingButton" type="button" class="k-modal-btn k-modal-btn--cancel metering-dialog__confirm-button" @click="keepEditing">Keep editing</button>
        <button type="button" class="k-modal-btn k-modal-btn--confirm k-modal-btn--danger metering-dialog__confirm-button" @click="emit('close')">Discard changes</button>
      </div>
    </template>
    <header v-show="!discard" class="metering-dialog__header">
      <div class="min-w-0">
        <h2 :id="titleId" class="text-lg font-semibold text-text-primary">{{ title }}</h2>
        <slot name="context" />
      </div>
      <button type="button" class="k-btn k-btn--text shrink-0 p-2" aria-label="Close editor" :disabled="busy" @click="requestClose"><X class="h-4 w-4" /></button>
    </header>
    <!-- Keep the form mounted so dismissing confirmation retains fields and scroll. -->
    <div v-show="!discard" class="metering-dialog__body"><slot /></div>
    <footer v-show="!discard" class="metering-dialog__footer">
      <button type="button" class="k-btn" :disabled="busy" @click="requestClose">Cancel</button>
      <slot name="submit" />
    </footer>
  </dialog>
</template>

<style scoped>
.metering-dialog { width: min(680px, calc(100vw - 40px)); max-width: none; max-height: calc(100dvh - 40px); margin: auto; padding: 0; overflow: hidden; }
.metering-dialog--confirm { width: min(480px, calc(100vw - 32px)); }
.metering-dialog[open] { display: flex; flex-direction: column; }
.metering-dialog::backdrop { background: color-mix(in srgb, var(--color-surface) 65%, transparent); }
.metering-dialog__header { display: flex; justify-content: space-between; gap: 16px; padding: 22px 24px 18px; border-bottom: 1px solid var(--color-border-subtle); }
.metering-dialog__body { min-height: 0; overflow: auto; padding: 20px 24px 24px; }
.metering-dialog__footer { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 10px; padding: 16px 24px; background: var(--color-surface); border-top: 1px solid var(--color-border-default); }
@media (max-width: 640px) {
  .metering-dialog:not(.metering-dialog--confirm) { width: 100%; height: 100dvh; max-height: 100dvh; border: 0; border-radius: 0; }
  .metering-dialog__body { flex: 1; padding: 20px; }
  .metering-dialog__header, .metering-dialog__footer { padding: 16px 20px; }
  .metering-dialog__footer :deep(button), .metering-dialog__confirm-button { min-height: 44px; }
}
@media (prefers-reduced-motion: reduce) { .metering-dialog { animation: none; } }
</style>
