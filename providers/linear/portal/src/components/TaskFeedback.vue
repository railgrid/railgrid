<script setup lang="ts">
withDefaults(defineProps<{ task: { error: string; operationName: string; message: string; loading: boolean }; errorPresented?: boolean }>(), { errorPresented: false });
</script>
<template>
  <div v-if="task.error && (!errorPresented || task.operationName)" class="linear-error" :role="errorPresented ? undefined : 'alert'">
    <p v-if="!errorPresented">{{ task.error }}</p>
  </div>
  <p v-if="task.message" class="linear-notice" role="status">{{ task.message }}</p>
  <span v-if="task.loading" class="linear-sr-only" role="status">Loading…</span>
</template>
