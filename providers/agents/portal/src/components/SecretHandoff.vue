<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { Check, Copy, X } from 'lucide-vue-next'

withDefaults(defineProps<{ value: string; label?: string; copyLabel?: string }>(), {
  label: 'One-time setup value',
  copyLabel: 'Copy setup value',
})
const emit = defineEmits<{ cleared: [] }>()
const copied = ref(false)
const copyError = ref('')

async function copy(value: string): Promise<void> {
  copyError.value = ''
  try {
    await navigator.clipboard.writeText(value)
    copied.value = true
  } catch {
    copyError.value = 'Could not copy. Check clipboard permission and try again.'
  }
}

function clear(): void {
  copied.value = false
  copyError.value = ''
  emit('cleared')
}

onBeforeUnmount(() => {
  copied.value = false
  copyError.value = ''
})
</script>

<template>
  <section class="k-card agents-secret-handoff" aria-label="One-time setup handoff">
    <div>
      <strong>{{ label }}</strong>
      <code aria-label="Masked one-time setup value">••••••••••••••••••••••••</code>
    </div>
    <div class="agents-secret-handoff-actions">
      <button type="button" class="k-btn k-btn--ghost" @click="copy(value)"><Check v-if="copied" aria-hidden="true" /><Copy v-else aria-hidden="true" /> {{ copied ? 'Copied' : copyLabel }}</button>
      <button type="button" class="k-icon-action" :aria-label="`Dismiss ${label}`" @click="clear"><X aria-hidden="true" /></button>
    </div>
    <span class="sr-only" role="status" aria-live="polite">{{ copied ? `${label} copied.` : '' }}</span>
    <p v-if="copyError" class="agents-fielderr" role="alert">{{ copyError }}</p>
  </section>
</template>
