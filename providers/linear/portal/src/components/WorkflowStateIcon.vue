<script setup lang="ts">
import { computed } from 'vue';
import { Circle, CircleDashed, CircleDot, CircleCheck, CircleX, CircleSlash, Inbox } from 'lucide-vue-next';
const props = defineProps<{ type?: string }>();
const presentation = computed(() => {
  switch (props.type) {
    case 'backlog': return { icon: CircleDashed, tone: 'neutral' };
    case 'unstarted': return { icon: Circle, tone: 'neutral' };
    case 'started': return { icon: CircleDot, tone: 'active' };
    case 'completed': return { icon: CircleCheck, tone: 'complete' };
    case 'canceled': return { icon: CircleX, tone: 'neutral' };
    case 'duplicate': return { icon: CircleSlash, tone: 'neutral' };
    case 'triage': return { icon: Inbox, tone: 'triage' };
    default: return { icon: Circle, tone: 'neutral' };
  }
});
</script>
<template>
  <component :is="presentation.icon" :size="14" :stroke-width="1.75" class="linear-workflow-icon" :class="`linear-workflow-icon--${presentation.tone}`" aria-hidden="true" />
</template>
