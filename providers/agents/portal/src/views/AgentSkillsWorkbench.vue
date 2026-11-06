<script setup lang="ts">
import { BookOpen } from 'lucide-vue-next'
import AIWorkbenchPane from '../agentkit/AIWorkbenchPane.vue'
import AgentSkillActivation from './AgentSkillActivation.vue'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import type { Route } from '../router'
const props = defineProps<{ store: AppStore; api: ApiClient; name: string; authorityEpoch?: number }>()
const emit = defineEmits<{ navigate: [route: Route] }>()
</script>
<template>
  <AIWorkbenchPane title="Skills" description="Manage which skills this agent can use. Select enabled skills in chat for an individual turn.">
    <template #actions><button type="button" class="k-btn k-btn--ghost" @click="emit('navigate', { kind: 'menu', menu: 'skills', returnAgent: name })"><BookOpen :size="14" aria-hidden="true" /> Open library</button></template>
    <AgentSkillActivation :store="props.store" :api="props.api" :name="props.name" :authority-epoch="props.authorityEpoch" />
  </AIWorkbenchPane>
</template>
