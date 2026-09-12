<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import type { FarosContext } from './api';
import { authorityKey, parseRoute } from './routes';
import Workspace from './Workspace.vue';
const props = defineProps<{ ctx: FarosContext | null }>();
const generation = ref(0);
let controller = new AbortController();
watch(() => authorityKey(props.ctx), () => { controller.abort(); controller = new AbortController(); generation.value++; }, { flush: 'sync' });
onBeforeUnmount(() => controller.abort());
const route = computed(() => parseRoute(props.ctx?.subPath));
</script>
<template>
  <p v-if="!ctx" class="linear-notice" role="status">Loading workspace context…</p>
  <p v-else-if="!ctx.tenant" class="linear-notice">Select a workspace to use Linear.</p>
  <Workspace v-else :key="generation" :context="() => ctx!" :authority-signal="controller.signal" :route="route" />
</template>
