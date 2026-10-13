<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ArrowRight, Loader2 } from 'lucide-vue-next'
import { authenticationApi as api, type ProjectAuthentication } from './authenticationApi'
import {
  authenticationSupportKind,
  authenticationSupportIsError,
  authenticationSupportNotice,
  authenticationSupportStatus,
  authenticationSupportTone,
  canCreateCompatibleAuthenticationProject,
  hasPersistedProductionRuntime,
  shouldLoadAuthenticationGrants,
} from './authenticationState'
import type { RailgridContext, Project, ProjectPublishingGrant } from './types'
import InlineNotification from './portalkit/InlineNotification.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import { confirmDialog } from './portalkit/confirm'

const props = defineProps<{ ctx: RailgridContext | null; project: Project }>()
const emit = defineEmits<{
  (event: 'create-compatible-project'): void
  (event: 'setup-auth'): void
}>()

const state = ref<ProjectAuthentication | null>(null)
const grants = ref<ProjectPublishingGrant[]>([])
const user = ref('')
const loading = ref(true)
const grantsLoading = ref(false)
const saving = ref(false)
const error = ref('')
const grantsError = ref('')
const notice = ref('')
const grantsLoaded = ref(false)
const operation = ref<'enable' | 'disable' | 'approve' | 'revoke' | null>(null)
const operationGrantName = ref('')
const errorSource = ref<'authentication' | 'grants' | 'action' | null>(null)
let generation = 0

const supportNotice = computed(() => authenticationSupportNotice(state.value))
const supportIsError = computed(() => authenticationSupportIsError(state.value))
const supportStatus = computed(() => authenticationSupportStatus(state.value))
const supportTone = computed(() => authenticationSupportTone(state.value))
const showCreateCompatibleProject = computed(() => canCreateCompatibleAuthenticationProject(state.value))
const productionRuntimeExists = computed(() => hasPersistedProductionRuntime(state.value?.environments))
const productionAppID = computed(() => `${props.project.uid}/production`)

function contextKey(): string {
  return JSON.stringify([
    props.project.uid,
    props.project.name,
    props.ctx?.orgUUID,
    props.ctx?.workspaceUUID,
    props.ctx?.user?.userId,
  ])
}

function isCurrent(current: number, key: string): boolean {
  return current === generation && key === contextKey()
}

function clearFeedback() {
  error.value = ''
  errorSource.value = null
  grantsError.value = ''
  notice.value = ''
}

async function load() {
  const current = ++generation
  const key = contextKey()
  loading.value = true
  state.value = null
  grants.value = []
  grantsLoaded.value = false
  grantsLoading.value = false
  operationGrantName.value = ''
  clearFeedback()
  try {
    const result = await api.getProjectAuthentication(props.ctx, props.project.name)
    if (isCurrent(current, key)) state.value = result
  } catch (e) {
    if (isCurrent(current, key)) {
      error.value = e instanceof Error ? e.message : String(e)
      errorSource.value = 'authentication'
    }
  } finally {
    if (isCurrent(current, key)) loading.value = false
  }
}

async function loadGrants(force = false) {
  const currentState = state.value
  if (!currentState?.enabled || grantsLoading.value || (grantsLoaded.value && !force)) return
  if (!shouldLoadAuthenticationGrants(currentState.environments)) {
    grantsLoaded.value = true
    return
  }

  const current = generation
  const key = contextKey()
  grantsLoading.value = true
  grantsError.value = ''
  error.value = ''
  errorSource.value = null
  notice.value = ''
  try {
    const response = await api.getProjectAuthenticationGrants(props.ctx, props.project.name)
    if (isCurrent(current, key) && state.value === currentState) {
      grants.value = response.items || []
      grantsLoaded.value = true
    }
  } catch (e) {
    if (isCurrent(current, key) && state.value === currentState) {
      grantsError.value = e instanceof Error ? e.message : String(e)
      error.value = grantsError.value
      errorSource.value = 'grants'
    }
  } finally {
    if (isCurrent(current, key) && state.value === currentState) grantsLoading.value = false
  }
}

function onAccessToggle(event: Event) {
  if ((event.currentTarget as HTMLDetailsElement).open) void loadGrants()
}

async function configure() {
  const currentState = state.value
  if (!currentState || saving.value) return

  const enable = !currentState.enabled
  if (enable && !currentState.supported) return

  const current = generation
  const key = contextKey()
  const projectName = props.project.name
  if (!enable) {
    const supportKind = authenticationSupportKind(currentState)
    const confirmed = await confirmDialog({
      title: 'Disable Railgrid Auth?',
      message: currentState.supported
        ? 'Turning off Railgrid Auth can stop sign-in and cause protected routes to reject requests. You can turn it back on while this template supports it.'
        : supportKind === 'pending' || supportKind === 'unknown'
          ? 'Turning off Railgrid Auth can stop sign-in and cause protected routes to reject requests. This template’s support check is incomplete, so check again before disabling it if you may need to turn it back on.'
          : 'Turning off Railgrid Auth can stop sign-in and cause protected routes to reject requests. This template does not currently have verified support, so you cannot turn it back on until its Railgrid Auth support is restored.',
      confirmLabel: 'Disable Railgrid Auth',
      danger: true,
    })
    if (!confirmed || !isCurrent(current, key) || state.value !== currentState || props.project.name !== projectName) return
  }
  if (!isCurrent(current, key) || state.value !== currentState) return

  saving.value = true
  operation.value = enable ? 'enable' : 'disable'
  clearFeedback()
  try {
    const result = await api.configureProjectAuthentication(props.ctx, props.project.name, enable)
    if (isCurrent(current, key)) {
      state.value = result
      grantsLoading.value = false
      grantsError.value = ''
      notice.value = result.enabled
        ? 'Railgrid Auth is enabled. Set up or review sign-in in your app.'
        : 'Railgrid Auth is disabled. Apps that use it may no longer accept sign-ins.'
      if (!result.enabled) {
        grants.value = []
        grantsLoaded.value = false
      }
    }
  } catch (e) {
    if (isCurrent(current, key)) {
      error.value = e instanceof Error ? e.message : String(e)
      errorSource.value = 'action'
    }
  } finally {
    if (isCurrent(current, key)) {
      saving.value = false
      operation.value = null
    }
  }
}

async function approve() {
  const submittedUser = user.value.trim()
  if (!submittedUser || saving.value || grantsLoading.value || !productionRuntimeExists.value) return
  const current = generation
  const key = contextKey()
  saving.value = true
  operation.value = 'approve'
  clearFeedback()
  try {
    const result = await api.addProjectAuthenticationGrant(props.ctx, props.project.name, submittedUser, productionAppID.value)
    if (isCurrent(current, key)) {
      grants.value = result.items || []
      grantsLoaded.value = true
      user.value = ''
      notice.value = 'Production app sign-in approved.'
    }
  } catch (e) {
    if (isCurrent(current, key)) {
      error.value = e instanceof Error ? e.message : String(e)
      errorSource.value = 'action'
    }
  } finally {
    if (isCurrent(current, key)) {
      saving.value = false
      operation.value = null
    }
  }
}

async function revoke(grant: ProjectPublishingGrant) {
  if (saving.value) return
  const current = generation
  const key = contextKey()
  saving.value = true
  operation.value = 'revoke'
  operationGrantName.value = grant.name
  clearFeedback()
  try {
    const result = await api.removeProjectAuthenticationGrant(props.ctx, props.project.name, grant.name)
    if (isCurrent(current, key)) {
      grants.value = result.items || []
      grantsLoaded.value = true
      notice.value = 'Sign-in approval removed. Existing sessions expire within 15 minutes.'
    }
  } catch (e) {
    if (isCurrent(current, key)) {
      error.value = e instanceof Error ? e.message : String(e)
      errorSource.value = 'action'
    }
  } finally {
    if (isCurrent(current, key)) {
      saving.value = false
      operation.value = null
      operationGrantName.value = ''
    }
  }
}

function retry() {
  if (errorSource.value === 'grants') void loadGrants(true)
  else void load()
}

watch(
  contextKey,
  () => {
    saving.value = false
    operation.value = null
    operationGrantName.value = ''
    user.value = ''
    void load()
  },
  { immediate: true },
)
onBeforeUnmount(() => { generation++ })
</script>

<template>
  <section class="grid gap-4 rounded-lg border border-border-subtle bg-transparent p-3" aria-labelledby="project-auth-heading" :aria-busy="loading || saving || grantsLoading">
    <div class="flex items-start gap-2.5">
      <div class="min-w-0">
        <h3 id="project-auth-heading" class="text-[13px] font-semibold text-text-primary">Railgrid Auth</h3>
        <p class="mt-1 max-w-[65ch] text-[12px] leading-5 text-text-muted">Let people sign in to your app with their Railgrid account.</p>
      </div>
    </div>

    <p v-if="loading" role="status" class="flex items-center gap-2 text-[12px] text-text-muted">
      <Loader2 class="h-4 w-4 animate-spin" aria-hidden="true" />
      Checking Railgrid Auth…
    </p>

    <template v-else-if="state">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <StatusBadge :status="state.enabled ? 'Enabled' : supportStatus" :tone="state.enabled ? 'success' : supportTone" />
        <button
          v-if="state.enabled"
          type="button"
          class="k-btn"
          :disabled="saving"
          @click="configure"
        >
          <Loader2 v-if="operation === 'disable'" class="h-4 w-4 animate-spin" aria-hidden="true" />
          {{ operation === 'disable' ? 'Disabling Railgrid Auth…' : 'Disable Railgrid Auth' }}
        </button>
        <button
          v-else-if="state.supported"
          type="button"
          class="k-btn k-btn--primary"
          :disabled="saving"
          @click="configure"
        >
          <Loader2 v-if="operation === 'enable'" class="h-4 w-4 animate-spin" aria-hidden="true" />
          {{ operation === 'enable' ? 'Enabling Railgrid Auth…' : 'Enable Railgrid Auth' }}
        </button>
      </div>

      <template v-if="!state.enabled && !state.supported">
        <p
          v-if="supportNotice"
          :role="supportIsError ? 'alert' : 'status'"
          class="max-w-[70ch] text-[12px] leading-5"
          :class="supportIsError ? 'text-danger' : 'text-text-muted'"
        >{{ supportNotice }}</p>
        <div v-if="showCreateCompatibleProject" class="flex flex-wrap items-center gap-3 border-t border-border-subtle pt-3">
          <button type="button" class="k-btn k-btn--primary" :disabled="saving" @click="emit('create-compatible-project')">
            Create a compatible project <ArrowRight class="h-3.5 w-3.5" aria-hidden="true" />
          </button>
          <p class="text-[12px] text-text-muted">Your current project stays unchanged.</p>
        </div>
        <button
          v-else-if="supportStatus === 'Checking support' || supportStatus === 'Support status unavailable'"
          type="button"
          class="k-btn justify-self-start"
          :disabled="loading || saving"
          @click="load"
        >
          Check again
        </button>
      </template>

      <template v-else-if="!state.enabled && state.supported">
        <p class="max-w-[70ch] text-[12px] leading-5 text-text-muted">This project’s development template supports Railgrid Auth.</p>
        <div class="grid gap-2 border-t border-border-subtle pt-3">
          <h4 class="text-[13px] font-semibold text-text-primary">Enabling auth is the first step</h4>
          <p class="max-w-[70ch] text-[12px] leading-5 text-text-muted">Set up or review sign-in and access checks in your app after enabling Railgrid Auth.</p>
        </div>
      </template>

      <p
        v-if="state.enabled && supportNotice"
        :role="supportIsError ? 'alert' : 'status'"
        class="max-w-[70ch] text-[12px] leading-5 text-text-muted"
        :class="supportIsError ? 'text-danger' : 'text-text-muted'"
      >{{ supportNotice }}</p>

      <template v-if="state.enabled">
        <div class="grid gap-2 border-t border-border-subtle pt-4">
          <div>
            <h4 class="text-[13px] font-semibold text-text-primary">Set up or review sign-in</h4>
            <p class="mt-1 max-w-[70ch] text-[12px] leading-5 text-text-muted">Enabling Railgrid Auth does not add a sign-in screen or access checks to your app.</p>
          </div>
          <button type="button" class="k-btn k-btn--primary justify-self-start" :disabled="saving" @click="emit('setup-auth')">
            Set up with App Studio <ArrowRight class="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        </div>

        <details class="group border-t border-border-subtle pt-3" @toggle="onAccessToggle">
          <summary class="flex min-h-10 cursor-pointer list-none items-center justify-between gap-3 text-[13px] font-medium text-text-primary focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent [&::-webkit-details-marker]:hidden">
            <span>Who can sign in</span>
            <span class="font-mono text-[10px] text-text-muted group-open:hidden">Show</span>
            <span class="hidden font-mono text-[10px] text-text-muted group-open:inline">Hide</span>
          </summary>
          <div class="grid gap-3 pb-1 pt-3">
            <p class="max-w-[70ch] text-[12px] leading-5 text-text-muted">Workspace members can sign in. Approve additional Railgrid users for the Production app without adding them to your organization or workspace.</p>

            <ul v-if="state.environments.length" class="grid gap-1 text-[11px] text-text-secondary" aria-label="Railgrid Auth runtime status">
              <li v-for="env in state.environments" :key="env.environment" class="flex flex-wrap items-center gap-x-2">
                <span>{{ env.environment === 'development' ? 'Development' : env.environment === 'production' ? 'Production' : env.environment }}:</span>
                <span class="font-mono text-[10px] text-text-muted">{{ env.runtimeReady ? 'Runtime configured' : env.configured ? 'Runtime starting' : 'Configuration pending' }}</span>
              </li>
            </ul>

            <p v-if="!productionRuntimeExists" class="text-[12px] leading-5 text-text-muted">Deploy Production before approving additional app users.</p>
            <form v-else class="flex flex-wrap items-end gap-2" @submit.prevent="approve">
              <label class="grid min-w-0 flex-1 gap-1.5">
                <span class="text-[12px] font-medium text-text-secondary">Railgrid user ID</span>
                <input v-model="user" class="k-input" autocomplete="off" placeholder="Existing Railgrid user ID" :disabled="saving" />
              </label>
              <button type="submit" class="k-btn" :disabled="saving || grantsLoading || !user.trim()">
                <Loader2 v-if="operation === 'approve'" class="h-4 w-4 animate-spin" aria-hidden="true" />
                {{ operation === 'approve' ? 'Approving…' : 'Approve sign-in' }}
              </button>
            </form>

            <div v-if="grantsLoading" role="status" class="flex items-center gap-2 text-[12px] text-text-muted">
              <Loader2 class="h-4 w-4 animate-spin" aria-hidden="true" /> Loading sign-in approvals…
            </div>
            <div v-else-if="grantsError" class="grid justify-items-start gap-2" role="alert">
              <p class="text-[12px] leading-5 text-danger">Could not load Production sign-in approvals: {{ grantsError }}</p>
              <button type="button" class="k-btn" :disabled="saving" @click="retry">Retry</button>
            </div>
            <ul v-else-if="grants.length" class="grid gap-2" aria-label="Users approved to sign in to the Production app">
              <li v-for="grant in grants" :key="grant.name" class="flex flex-wrap items-center justify-between gap-3 text-[12px]">
                <span class="min-w-0 break-all text-text-secondary">{{ grant.user }}</span>
                <button type="button" class="k-btn shrink-0" :disabled="saving" @click="revoke(grant)">
                  <Loader2 v-if="operation === 'revoke' && operationGrantName === grant.name" class="h-4 w-4 animate-spin" aria-hidden="true" />
                  {{ operation === 'revoke' && operationGrantName === grant.name ? 'Removing…' : 'Remove approval' }}
                </button>
              </li>
            </ul>
            <p v-else-if="grantsLoaded && productionRuntimeExists" role="status" class="text-[12px] leading-5 text-text-muted">No additional users are approved to sign in to the Production app.</p>
          </div>
        </details>
      </template>
    </template>

    <InlineNotification v-if="error && errorSource !== 'grants'" tone="error" :message="error" />
    <InlineNotification v-else-if="notice" tone="success" :message="notice" />
    <button v-if="errorSource === 'authentication'" type="button" class="k-btn justify-self-start" :disabled="loading || saving" @click="retry">Retry</button>
  </section>
</template>
