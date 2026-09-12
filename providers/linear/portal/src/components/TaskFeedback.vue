<script setup lang="ts">
import { useSession } from '../state';
import { resourcePath } from '../routes';
defineProps<{ task: { error: string; operationName: string; message: string; loading: boolean } }>();
const session = useSession();
</script>
<template>
  <div v-if="task.error" class="linear-error" role="alert">
    <p>{{ task.error }}</p>
    <a v-if="task.operationName" class="k-table-resource-link" :href="session.href(resourcePath('operations', task.operationName))" @click.prevent="session.navigate(resourcePath('operations', task.operationName))">Inspect operation</a>
  </div>
  <p v-if="task.message" class="linear-notice" role="status">{{ task.message }}</p>
  <span v-if="task.loading" class="linear-sr-only" role="status">Loading…</span>
</template>
