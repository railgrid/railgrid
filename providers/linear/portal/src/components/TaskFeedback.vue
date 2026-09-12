<script setup lang="ts">
import { useSession } from '../state';
import { resourcePath } from '../routes';
withDefaults(defineProps<{ task: { error: string; operationName: string; message: string; loading: boolean }; errorPresented?: boolean }>(), { errorPresented: false });
const session = useSession();
</script>
<template>
  <div v-if="task.error && (!errorPresented || task.operationName)" class="linear-error" :role="errorPresented ? undefined : 'alert'">
    <p v-if="!errorPresented">{{ task.error }}</p>
    <a v-if="task.operationName" class="k-table-resource-link" :href="session.href(resourcePath('operations', task.operationName))" @click.prevent="session.navigate(resourcePath('operations', task.operationName))">Inspect operation</a>
  </div>
  <p v-if="task.message" class="linear-notice" role="status">{{ task.message }}</p>
  <span v-if="task.loading" class="linear-sr-only" role="status">Loading…</span>
</template>
