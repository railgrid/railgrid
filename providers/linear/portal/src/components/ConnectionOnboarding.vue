<script setup lang="ts">
import { Link2 } from 'lucide-vue-next';
import { useSession } from '../state';
import { LINEAR_JOURNEY_STEPS } from '../journey';
import FirstRunGuide from '../portalkit/FirstRunGuide.vue';
defineProps<{ issues?: boolean }>();
const session = useSession();
</script>
<template>
  <FirstRunGuide :steps="LINEAR_JOURNEY_STEPS" :current-step="0" journey-label="Get started with Linear" :title="issues ? 'Connect Linear to start working with issues' : 'Connect a Linear account'" :description="issues ? 'Create a connection first, then choose a team to browse, create and update issues.' : 'Connect once, choose the teams Faros can access, and manage issues in this workspace.'" primary-label="Add connection" @primary="session.navigate('connections/create')">
    <template #icon><Link2 :size="24" :stroke-width="1.8" /></template>
  </FirstRunGuide>
</template>
