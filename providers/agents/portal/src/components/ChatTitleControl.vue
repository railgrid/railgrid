<script setup lang="ts">
defineProps<{ title: string; disabled: boolean; busy: boolean }>()
const emit = defineEmits<{ start: [] }>()
</script>

<template>
  <button
    type="button"
    class="k-ai-conversation-title"
    :disabled="disabled"
    :aria-disabled="busy || undefined"
    :title="disabled ? 'Send a message before renaming this chat' : `Rename chat: ${title}`"
    aria-label="Rename chat"
    @click="!busy && emit('start')"
  ><span>{{ title }}</span></button>
</template>
