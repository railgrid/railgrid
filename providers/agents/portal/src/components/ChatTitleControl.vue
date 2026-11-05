<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'

const props = defineProps<{ title: string; editing: boolean; modelValue: string; disabled: boolean; busy: boolean }>()
const emit = defineEmits<{ start: []; 'update:modelValue': [value: string]; commit: []; cancel: [] }>()
const input = ref<HTMLInputElement | null>(null)
const button = ref<HTMLButtonElement | null>(null)
watch(() => props.editing, editing => {
  if (editing) void nextTick(() => { input.value?.focus(); input.value?.select() })
})
function commit(event: KeyboardEvent): void {
  if (event.isComposing) return
  event.preventDefault()
  emit('commit')
  void nextTick(() => button.value?.focus())
}
function cancel(): void {
  emit('cancel')
  void nextTick(() => button.value?.focus())
}
</script>

<template>
  <input
    v-if="editing"
    ref="input"
    :value="modelValue"
    type="text"
    class="k-ai-conversation-title-input"
    aria-label="Rename chat"
    maxlength="253"
    :disabled="busy"
    @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
    @keydown.enter.exact="commit"
    @keydown.esc.stop.prevent="cancel"
    @blur="emit('commit')"
  />
  <!-- Keep the title focusable while saving so Enter can return focus to it.
       aria-disabled and the activation guard prevent another edit until settled. -->
  <button
    v-else
    ref="button"
    type="button"
    class="k-ai-conversation-title"
    :disabled="disabled"
    :aria-disabled="busy || undefined"
    :title="disabled ? 'Send a message before renaming this chat' : `Rename chat: ${title}`"
    aria-label="Rename chat"
    @click="!busy && emit('start')"
  ><span>{{ title }}</span></button>
</template>
