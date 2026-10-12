<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ShieldCheck, Loader2 } from 'lucide-vue-next'
import { authenticationApi as api, type ProjectAuthentication } from './authenticationApi'
import type { RailgridContext, Project, ProjectPublishingGrant } from './types'
import InlineNotification from './portalkit/InlineNotification.vue'

const props = defineProps<{ ctx: RailgridContext | null; project: Project }>()
const state = ref<ProjectAuthentication | null>(null)
const grants = ref<ProjectPublishingGrant[]>([])
const user = ref('')
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
let generation = 0
const production = computed(() => state.value?.environments.find(env => env.environment === 'production'))
const productionAppID = computed(() => `${props.project.uid}/production`)
async function load() {
  const current = ++generation
  loading.value = true; error.value = ''; state.value = null; grants.value = []
  try {
    const result = await api.getProjectAuthentication(props.ctx, props.project.name)
    if (current !== generation) return
    state.value = result
    const response = await api.getProjectAuthenticationGrants(props.ctx, props.project.name)
    if (current === generation) grants.value = response.items || []
  } catch (e) { if (current === generation) error.value = e instanceof Error ? e.message : String(e) }
  finally { if (current === generation) loading.value = false }
}
async function configure() {
  if (!state.value || saving.value) return
  const current = generation
  saving.value = true; error.value = ''; notice.value = ''
  try {
    const result = await api.configureProjectAuthentication(props.ctx, props.project.name, !state.value.enabled)
    if (current === generation) { state.value = result; notice.value = result.enabled ? 'Railgrid Auth enabled. Ask the assistant to integrate login and protect your app’s data.' : 'Railgrid Auth disabled. Protected application routes will reject access until you update them.' }
  } catch (e) { if (current === generation) error.value = e instanceof Error ? e.message : String(e) }
  finally { if (current === generation) saving.value = false }
}
async function approve() {
  if (!user.value.trim() || saving.value) return
  const current = generation
  saving.value = true; error.value = ''; notice.value = ''
  try {
    const result = await api.addProjectAuthenticationGrant(props.ctx, props.project.name, user.value.trim(), productionAppID.value)
    if (current === generation) { grants.value = result.items || []; user.value = ''; notice.value = 'App access approved. No organization or workspace membership was added.' }
  } catch (e) { if (current === generation) error.value = e instanceof Error ? e.message : String(e) }
  finally { if (current === generation) saving.value = false }
}
async function revoke(grant: ProjectPublishingGrant) {
  if (saving.value) return
  const current = generation
  saving.value = true; error.value = ''; notice.value = ''
  try {
    const result = await api.removeProjectAuthenticationGrant(props.ctx, props.project.name, grant.name)
    if (current === generation) { grants.value = result.items || []; notice.value = 'Access removed. Existing sessions expire within 15 minutes.' }
  } catch (e) { if (current === generation) error.value = e instanceof Error ? e.message : String(e) }
  finally { if (current === generation) saving.value = false }
}
watch(() => [props.project.uid, props.ctx?.orgUUID, props.ctx?.workspaceUUID, props.ctx?.user?.userId, props.ctx?.token], () => { saving.value = false; notice.value = ''; user.value = ''; void load() }, { immediate: true })
onBeforeUnmount(() => { generation++ })
</script>

<template>
  <section class="grid gap-4 rounded-lg border border-border-subtle bg-surface-overlay/40 p-3" aria-labelledby="project-auth-heading">
    <div class="flex items-start gap-2.5">
      <ShieldCheck class="mt-0.5 h-4 w-4 shrink-0 text-text-muted" :stroke-width="1.75" />
      <div><h3 id="project-auth-heading" class="text-[12px] font-semibold text-text-primary">Authentication</h3>
        <p class="mt-1 text-[12px] leading-5 text-text-muted">Let approved users sign in with their Railgrid account. App login is separate from sharing and workspace membership.</p></div>
    </div>
    <p v-if="loading" role="status" class="flex items-center gap-2 text-[12px] text-text-muted"><Loader2 class="h-4 w-4 animate-spin" />Checking authentication…</p>
    <template v-else-if="state">
      <div class="flex flex-wrap items-center justify-between gap-3"><span class="text-[13px] font-medium text-text-primary">{{ state.enabled ? 'Railgrid Auth enabled' : 'Authentication disabled' }}</span>
        <button class="k-btn" :disabled="saving || (!state.enabled && !state.supported)" @click="configure">{{ state.enabled ? 'Disable Railgrid Auth' : 'Enable Railgrid Auth' }}</button>
      </div>
      <p v-if="!state.supported" class="text-[12px] text-text-muted">This runtime template does not support Railgrid Auth yet.</p>
      <p class="text-[12px] leading-5 text-text-muted">Your app still needs login UI, protected backend routes, and checks that users can only access their own data.</p>
      <ul v-if="state.enabled" class="grid gap-1 text-[12px] text-text-secondary">
        <li v-for="env in state.environments" :key="env.environment">{{ env.environment === 'development' ? 'Development' : env.environment === 'production' ? 'Production' : env.environment }}: {{ env.runtimeReady ? 'Runtime configured' : env.configured ? 'Runtime starting' : 'Configuration pending' }}</li>
      </ul>
      <div v-if="state.enabled" class="grid gap-3 border-t border-border-subtle pt-3">
        <p class="text-[12px] text-text-muted">Workspace members can sign in. Approve other existing Railgrid users for Production only.</p>
        <form v-if="production" class="flex flex-wrap items-end gap-2" @submit.prevent="approve">
          <label class="grid min-w-0 flex-1 gap-1.5"><span class="text-[12px] font-medium text-text-secondary">Railgrid user ID</span><input v-model="user" class="k-input" placeholder="Existing user ID" :disabled="saving" /></label>
          <button class="k-btn" :disabled="saving || !user.trim()">Approve user</button>
        </form>
        <p v-else class="text-[12px] text-text-muted">Deploy Production before approving app users.</p>
        <ul class="grid gap-2"><li v-for="grant in grants" :key="grant.name" class="flex items-center justify-between gap-3 text-[12px]"><span class="break-all text-text-secondary">{{ grant.user }}</span><button class="k-btn shrink-0" :disabled="saving" @click="revoke(grant)">Remove access</button></li></ul>
      </div>
    </template>
    <InlineNotification v-if="error || notice" :tone="error ? 'error' : 'success'" :message="error || notice" />
    <button v-if="error" class="k-btn justify-self-start" :disabled="loading || saving" @click="load">Retry</button>
  </section>
</template>
