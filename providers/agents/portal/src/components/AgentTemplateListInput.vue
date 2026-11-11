<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'

const props = defineProps<{
  id: string
  modelValue: string
  placeholder: string
  labelledby: string
  describedby: string
  disabled: boolean
  invalid: boolean
  required: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const input = ref<HTMLInputElement | null>(null)
const entries = ref<string[]>([])
const pending = ref('')
const value = computed(() => [...entries.value, pending.value.trim()].filter(Boolean).join('\n'))

watch(() => props.modelValue, incoming => {
  if (incoming === value.value) return
  entries.value = incoming.split('\n').map(line => line.trim()).filter(Boolean)
  pending.value = ''
}, { immediate: true })

function updatePending(event: Event): void {
  if ((event as InputEvent).isComposing) return
  const text = (event.target as HTMLInputElement).value.trim()
  emit('update:modelValue', [...entries.value, text].filter(Boolean).join('\n'))
}

function enter(event: KeyboardEvent): void {
  if (event.isComposing) return
  event.preventDefault()
  commit()
}

function commit(): void {
  if (pending.value.trim()) entries.value.push(pending.value.trim())
  pending.value = ''
  emit('update:modelValue', value.value)
}

function paste(event: ClipboardEvent): void {
  const text = event.clipboardData?.getData('text/plain')
  if (!text || !/[\r\n]/.test(text)) return
  event.preventDefault()
  if (pending.value.trim()) entries.value.push(pending.value.trim())
  entries.value.push(...text.split(/\r\n|[\r\n]/).map(line => line.trim()).filter(Boolean))
  pending.value = ''
  emit('update:modelValue', value.value)
}

function remove(index: number): void {
  entries.value.splice(index, 1)
  emit('update:modelValue', value.value)
  input.value?.focus()
}
</script>

<template>
  <div class="k-input agents-template-list">
    <span v-for="(entry, index) in entries" :key="index" class="agents-template-token">
      {{ entry }}
      <button class="k-btn k-btn--text" type="button" :disabled="disabled" :aria-label="'Remove ' + entry" @click="remove(index)">
        <X aria-hidden="true" />
      </button>
    </span>
    <input :id="id" ref="input" v-model="pending" :placeholder="placeholder" :disabled="disabled"
      :aria-labelledby="labelledby" :aria-describedby="describedby" :aria-invalid="invalid || undefined"
      :aria-required="required || undefined" autocomplete="off"
      @input="updatePending"
      @keydown.enter="enter" @blur="commit" @paste="paste" />
  </div>
</template>
