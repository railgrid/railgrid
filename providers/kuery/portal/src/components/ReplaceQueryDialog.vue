<script lang="ts">
let dialogSequence = 0
const dialogPrefix = `kuery-replace-${Math.random().toString(36).slice(2)}`
</script>
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { Info } from 'lucide-vue-next'

const emit = defineEmits<{ confirm: []; cancel: [] }>()
const dialog = ref<HTMLElement | null>(null)
const confirm = ref<HTMLButtonElement | null>(null)
const instanceID = `${dialogPrefix}-${++dialogSequence}`
const titleID = `${instanceID}-title`
const messageID = `${instanceID}-message`
let previousFocus: HTMLElement | null = null
let mounted = false
onMounted(() => {
  mounted = true
  previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const original = previousFocus
  void nextTick(() => {
    if (mounted && document.activeElement === original) confirm.value?.focus()
  })
})
onBeforeUnmount(() => {
  mounted = false
  const restore = dialog.value?.contains(document.activeElement)
  const target = previousFocus
  if (restore) void nextTick(() => {
    if (target?.isConnected && (!document.activeElement || document.activeElement === document.body)) target.focus()
  })
})
function keydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); emit('cancel'); return }
  if (event.key !== 'Tab') return
  const buttons = dialog.value?.querySelectorAll<HTMLButtonElement>('button')
  if (!buttons?.length) return
  const first = buttons[0]!, last = buttons[buttons.length - 1]!
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}
</script>

<template>
  <div class="k-modal-overlay" @click.self="emit('cancel')">
    <div ref="dialog" class="k-modal k-modal--confirm" role="alertdialog" aria-modal="true" :aria-labelledby="titleID" :aria-describedby="messageID" @keydown="keydown">
      <div class="k-modal__head"><span class="k-modal__icon" aria-hidden="true"><Info :stroke-width="1.9" /></span><h2 :id="titleID" class="k-modal__title">Replace your query?</h2></div>
      <div :id="messageID" class="k-modal__body"><p class="k-modal__message">Selecting an example replaces your edited query.</p></div>
      <div class="k-modal__foot"><button type="button" class="k-modal-btn k-modal-btn--cancel" @click="emit('cancel')">Cancel</button><button ref="confirm" type="button" class="k-modal-btn k-modal-btn--confirm" @click="emit('confirm')">Replace query</button></div>
    </div>
  </div>
</template>
