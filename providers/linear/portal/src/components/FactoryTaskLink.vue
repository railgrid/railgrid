<script setup lang="ts">
import { ArrowUpRight, Factory } from 'lucide-vue-next';
import { factoryHref, factoryProgress, type FactoryTask } from '../factory';
import { useSession } from '../state';
import StatusBadge from '../portalkit/StatusBadge.vue';
defineProps<{ tasks: FactoryTask[] }>();
const session = useSession();
</script>
<template>
  <div v-if="tasks.length" class="linear-factory-links">
    <a v-for="task in tasks" :key="task.metadata.uid || task.metadata.name" class="linear-factory-link" :href="factoryHref(session.context(), task)" :aria-label="`Open Factory task ${task.metadata.name}: ${factoryProgress(task).label}`" @click.stop>
      <Factory :size="14" aria-hidden="true" /><span>Factory</span><StatusBadge :status="factoryProgress(task).label" :tone="factoryProgress(task).tone" /><ArrowUpRight :size="14" aria-hidden="true" />
    </a>
  </div>
</template>
