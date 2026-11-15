<script setup lang="ts">
import { Pencil } from 'lucide-vue-next'
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'

const props = withDefaults(defineProps<{
  title: string
  busy: boolean
  error: string
  heading?: string
  inputLabel?: string
  overlayTarget?: string
}>(), { heading: 'Rename chat', inputLabel: 'Chat title', overlayTarget: 'body' })
const emit = defineEmits<{ save: [title: string]; close: [] }>()
const value = ref(props.title)
const input = ref<HTMLInputElement | null>(null)
const dialog = ref<HTMLElement | null>(null)
const composing = ref(false)
const id = useId()
let previousFocus: HTMLElement | null = null
function close(): void {
  composing.value = false
  emit('close')
}
function save(): void {
  if (!composing.value && !props.busy && value.value.trim()) emit('save', value.value.trim())
}
function keydown(event: KeyboardEvent): void {
  // Escape may dismiss an IME candidate rather than the dialog.
  if (composing.value || event.isComposing || event.keyCode === 229) return
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    close()
  } else if (event.key === 'Tab') {
    const items = [...dialog.value!.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled)')]
    const first = items[0]
    const last = items[items.length - 1]
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
  }
}
onMounted(() => {
  previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  void nextTick(() => { input.value?.focus(); input.value?.select() })
})
onBeforeUnmount(() => {
  composing.value = false
  const target = previousFocus
  void nextTick(() => { if (target?.isConnected) target.focus() })
})
</script>

<template>
  <Teleport :to="overlayTarget">
    <div class="k-modal-overlay" @click.self="close">
      <form ref="dialog" class="k-modal k-modal--confirm" role="dialog" aria-modal="true" :aria-labelledby="`${id}-title`" :aria-busy="busy" @keydown="keydown" @submit.prevent="save">
        <div class="k-modal__head"><span class="k-modal__icon" aria-hidden="true"><Pencil :stroke-width="1.9" /></span><h2 :id="`${id}-title`" class="k-modal__title">{{ heading }}</h2></div>
        <div class="k-modal__body">
          <label :for="`${id}-input`">{{ inputLabel }}</label>
          <input :id="`${id}-input`" ref="input" v-model="value" class="k-input" maxlength="253" required :readonly="busy" :aria-invalid="error ? true : undefined" :aria-describedby="error ? `${id}-error` : undefined" @compositionstart="composing = true" @compositionend="composing = false" />
          <p v-if="error" :id="`${id}-error`" role="alert">{{ error }}</p>
        </div>
        <div class="k-modal__foot">
          <button class="k-modal-btn k-modal-btn--cancel" type="button" @click="close">Cancel</button>
          <button class="k-modal-btn k-modal-btn--confirm" type="submit" :disabled="busy || !value.trim()">{{ busy ? 'Saving…' : 'Save' }}</button>
        </div>
      </form>
    </div>
  </Teleport>
</template>
