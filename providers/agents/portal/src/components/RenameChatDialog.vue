<script setup lang="ts">
import { Pencil } from 'lucide-vue-next'
import { nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'

const props = defineProps<{ title: string; busy: boolean; error: string }>()
const emit = defineEmits<{ save: [title: string]; close: [] }>()
const value = ref(props.title)
const input = ref<HTMLInputElement | null>(null)
const dialog = ref<HTMLElement | null>(null)
const id = useId()
let previousFocus: HTMLElement | null = null
function keydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    emit('close')
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
  const target = previousFocus
  void nextTick(() => { if (target?.isConnected) target.focus() })
})
</script>

<template>
  <Teleport to="body">
    <div class="k-modal-overlay" @click.self="emit('close')">
      <form ref="dialog" class="k-modal k-modal--confirm" role="dialog" aria-modal="true" :aria-labelledby="`${id}-title`" :aria-busy="busy" @keydown="keydown" @submit.prevent="!busy && value.trim() && emit('save', value.trim())">
        <div class="k-modal__head"><span class="k-modal__icon" aria-hidden="true"><Pencil :stroke-width="1.9" /></span><h2 :id="`${id}-title`" class="k-modal__title">Rename chat</h2></div>
        <div class="k-modal__body">
          <label :for="`${id}-input`">Chat title</label>
          <input :id="`${id}-input`" ref="input" v-model="value" class="k-input" maxlength="253" required :readonly="busy" :aria-invalid="error ? true : undefined" :aria-describedby="error ? `${id}-error` : undefined" />
          <p v-if="error" :id="`${id}-error`" role="alert">{{ error }}</p>
        </div>
        <div class="k-modal__foot">
          <button class="k-modal-btn k-modal-btn--cancel" type="button" @click="emit('close')">Cancel</button>
          <button class="k-modal-btn k-modal-btn--confirm" type="submit" :disabled="busy || !value.trim()">{{ busy ? 'Saving…' : 'Save' }}</button>
        </div>
      </form>
    </div>
  </Teleport>
</template>
