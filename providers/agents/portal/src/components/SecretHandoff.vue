<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { Check, Copy, X } from 'lucide-vue-next'

const props = withDefaults(defineProps<{ value: string; label?: string; copyLabel?: string }>(), {
  label: 'One-time setup value',
  copyLabel: 'Copy setup value',
})
const emit = defineEmits<{ cleared: [] }>()
const copied = ref(false)
const copyError = ref('')
let attempt = 0
let timer: ReturnType<typeof setTimeout> | undefined

function reset(): void {
  attempt++
  if (timer) clearTimeout(timer)
  copied.value = false
  copyError.value = ''
}

watch(() => props.value, reset)

async function copy(): Promise<void> {
  reset()
  const current = attempt
  try {
    await navigator.clipboard.writeText(props.value)
    if (current !== attempt) return
    copied.value = true
    timer = setTimeout(() => { copied.value = false }, 2000)
  } catch {
    if (current !== attempt) return
    copyError.value = 'Could not copy. Check clipboard permission and try again.'
  }
}

function clear(): void {
  reset()
  emit('cleared')
}

onBeforeUnmount(reset)
</script>

<template>
  <section class="k-card agents-secret-handoff" aria-label="One-time setup handoff">
    <div>
      <strong>{{ label }}</strong>
      <code aria-label="Masked one-time setup value">••••••••••••••••••••••••</code>
    </div>
    <div class="agents-secret-handoff-actions">
      <button type="button" class="k-btn k-btn--ghost" @click="copy"><Check v-if="copied" aria-hidden="true" /><Copy v-else aria-hidden="true" /> {{ copied ? 'Copied' : copyLabel }}</button>
      <button type="button" class="k-icon-action" :aria-label="`Dismiss ${label}`" @click="clear"><X aria-hidden="true" /></button>
    </div>
    <span class="sr-only" role="status" aria-live="polite">{{ copied ? `${label} copied.` : '' }}</span>
    <p v-if="copyError" class="agents-fielderr" role="alert">{{ copyError }}</p>
  </section>
</template>
