<script setup lang="ts">
import { useSession } from '../state';
import { resourcePath } from '../routes';
defineProps<{ name: string; loading: boolean }>();
defineEmits<{ resume: []; separate: [] }>();
const session = useSession();
</script>
<template>
  <div v-if="name" class="linear-form">
    <p class="linear-notice" role="status">A write is awaiting confirmation. Leaving this page does not cancel it.</p>
    <div class="linear-actions">
      <a class="k-table-resource-link" :href="session.href(resourcePath('operations', name))" @click.prevent="session.navigate(resourcePath('operations', name))">Inspect submitted operation</a>
      <button type="button" class="k-btn k-btn--ghost" :disabled="loading" @click="$emit('resume')">Check outcome</button>
    </div>
    <details v-if="!loading">
      <summary class="k-btn k-btn--ghost">Prepare a separate write</summary>
      <p class="linear-notice">First inspect the operation and check Linear. A separate write can duplicate an issue or comment if this one already succeeded. The original operation remains in history.</p>
      <button type="button" class="k-btn k-btn--ghost" @click="$emit('separate')">I checked Linear; prepare a separate write</button>
    </details>
  </div>
</template>
