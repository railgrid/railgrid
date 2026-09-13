<script setup lang="ts">
import { computed, ref } from 'vue';
import { ArrowUpRight, Factory } from 'lucide-vue-next';
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue';
import { factoryHref, factoryProgress, pullRequestURL, workspaceHref } from '../factory';
import type { useFactory } from '../useFactory';
import { useSession } from '../state';
import FactoryTaskLink from './FactoryTaskLink.vue';
const props = defineProps<{ integration: ReturnType<typeof useFactory>; detail?: boolean }>();
const session = useSession();
const dismissKey = computed(() => JSON.stringify(['faros:linear:factory-banner', session.context().orgUUID, session.context().workspaceUUID, session.context().user?.sub || session.context().user?.email]));
const dismissedKey = ref('');
function dismissed() { try { return dismissedKey.value === dismissKey.value || sessionStorage.getItem(dismissKey.value) === 'dismissed'; } catch { return dismissedKey.value === dismissKey.value; } }
function dismiss() { dismissedKey.value = dismissKey.value; try { sessionStorage.setItem(dismissKey.value, 'dismissed'); } catch {} }
const enabled = computed(() => props.integration.snapshot.value?.enabled);
const tasks = computed(() => props.integration.tasks.value);
const count = computed(() => Object.keys(props.integration.byIssue.value).length);
const attention = computed(() => tasks.value.filter(task => ['warning', 'danger'].includes(factoryProgress(task).tone)).length);
</script>
<template>
  <section v-if="integration.supported.value && (enabled || integration.read.state.loading || integration.read.state.error || !dismissed())" aria-label="Factory integration" :aria-busy="integration.read.state.loading">
    <p v-if="integration.read.state.loading && !integration.snapshot.value" class="linear-notice" role="status">Checking Factory integration…</p>
    <ResourceSectionCard v-else-if="integration.read.state.error" title="Factory status is unavailable" description="Your Linear issues are still available. Factory progress could not be confirmed.">
      <p class="linear-notice">{{ integration.read.state.error }}</p>
      <template #actions><button class="k-btn k-btn--ghost" :disabled="integration.read.state.loading" @click="integration.load">{{ integration.read.state.loading ? 'Retrying…' : 'Retry Factory' }}</button></template>
    </ResourceSectionCard>
    <ResourceSectionCard v-else-if="integration.snapshot.value && !enabled && !dismissed()" title="Turn Linear issues into delivered work" description="Use the Factory provider to pick up eligible issues, run implementation tasks, and track delivery here.">
      <p class="linear-notice">Factory is not enabled in this workspace.</p>
      <template #actions><a class="k-btn k-btn--ghost" :href="workspaceHref(session.context(), 'providers')"><Factory :size="16" aria-hidden="true" />Explore Factory<ArrowUpRight :size="14" aria-hidden="true" /></a><button class="k-btn k-btn--ghost" @click="dismiss">Not now</button></template>
    </ResourceSectionCard>
    <template v-else-if="enabled">
      <div v-if="!detail && tasks.length" class="linear-factory-summary">
        <div class="linear-actions"><Factory :size="16" aria-hidden="true" /><strong>Factory</strong><span class="linear-page-meta">{{ count }} linked {{ count === 1 ? 'issue' : 'issues' }}<template v-if="attention"> · {{ attention }} {{ attention === 1 ? 'task needs' : 'tasks need' }} attention</template></span></div>
        <a class="k-btn k-btn--ghost" :href="factoryHref(session.context())">Open Factory<ArrowUpRight :size="14" aria-hidden="true" /></a>
      </div>
      <ResourceSectionCard v-else-if="!tasks.length" title="Factory · No linked work yet" description="Factory is enabled in this workspace. Eligible issues appear here when they are picked up by a configured intake route.">
        <p class="linear-notice">For setup, ask your workspace administrator to configure this team's intake route, target repository, and eligible workflow states.</p>
        <template #actions><a class="k-btn k-btn--ghost" :href="factoryHref(session.context())">Open Factory<ArrowUpRight :size="14" aria-hidden="true" /></a></template>
      </ResourceSectionCard>
      <ResourceSectionCard v-else title="Factory" description="Implementation and delivery progress for this issue.">
        <article v-for="task in tasks" :key="task.metadata.uid || task.metadata.name" class="linear-factory-task">
          <FactoryTaskLink :tasks="[task]" />
          <dl class="linear-facts"><div><dt>Task</dt><dd>{{ task.metadata.name }}</dd></div><div><dt>Repository</dt><dd>{{ task.spec?.repository || '—' }}</dd></div><div><dt>Attempts</dt><dd>{{ task.status?.attemptCount ?? 0 }}</dd></div></dl>
          <p v-if="task.status?.blocker" class="linear-notice">{{ task.status.blocker }}</p>
          <template v-if="factoryProgress(task).label === 'Needs input'"><p class="linear-prose">{{ task.status?.clarification?.questionText || 'This attempt needs clarification.' }}</p><p class="linear-notice">Reply to the Factory question in the Linear conversation to continue the attempt.</p></template>
          <a v-if="pullRequestURL(task)" class="linear-factory-link" :href="pullRequestURL(task)" target="_blank" rel="noopener noreferrer">View implementation PR<ArrowUpRight :size="14" aria-hidden="true" /></a>
        </article>
      </ResourceSectionCard>
      <p v-if="integration.read.state.loading" class="linear-page-meta" role="status">Refreshing Factory progress…</p>
    </template>
  </section>
</template>
