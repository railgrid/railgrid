<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowLeft, Loader2, RefreshCw } from 'lucide-vue-next'
import AppLayout from '@/components/AppLayout.vue'
import SelfHostInstructions from '@/components/SelfHostInstructions.vue'
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import { useTenantStore } from '@/stores/tenant'
import { useOrgProvidersStore, type OrgProviderRegistration } from '@/stores/orgProviders'
import { useProvidersStore } from '@/stores/providers'
import { useAuthStore } from '@/stores/auth'

const tenant = useTenantStore()
const orgProviders = useOrgProvidersStore()
const providers = useProvidersStore()
const auth = useAuthStore()
const { scopePath } = useScopedNavigation()
const name = ref('')
const targetKey = ref('')
const busy = ref(false)
const checking = ref(false)
const error = ref('')
const registration = ref<OrgProviderRegistration | null>(null)
let generation = 0
const canManage = computed(() => tenant.activeOrg?.role === 'admin')
const enrollmentAvailable = computed(() => orgProviders.enrollment?.available === true)
const enrollmentMessage = computed(() => orgProviders.enrollment?.reason || (
  orgProviders.loading && !orgProviders.loaded
    ? 'Checking hosted provider enrollment availability…'
    : 'Hosted provider enrollment availability could not be verified by this hub.'
))
const targets = computed(() => orgProviders.eligibleInstallTargets)
const target = computed(() => targets.value.find(t => `${t.workspace}/${t.name}` === targetKey.value) ?? (targets.value.length === 1 ? targets.value[0] : null))
const normalizedName = computed(() => name.value.trim())
const nameError = computed(() => {
  const value = normalizedName.value
  if (!value) return ''
  if (value.length > 63 || !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(value)) return 'Use 1–63 lowercase letters, numbers, or hyphens; start and end with a letter or number.'
  if (orgProviders.isSelfHosted(value)) return 'This organization already registered that name. Open its install details in Self-Hosting.'
  if (providers.items.some(p => p.name === value)) return 'This name belongs to a platform provider. Use its Self-host action to replace it.'
  return ''
})
const installed = computed(() => registration.value ? orgProviders.byName[registration.value.provider.name] : null)

function reset() {
  generation++
  registration.value = null
  name.value = ''
  targetKey.value = ''
  error.value = ''
  busy.value = false
  checking.value = false
}
watch(() => tenant.orgUUID, () => { reset(); void refreshTargets() })
watch(canManage, allowed => { if (!allowed) reset() })
watch(() => `${auth.user?.userId || auth.user?.email || ''}/${canManage.value}`, reset)
onBeforeUnmount(reset)
async function refreshTargets() {
  await Promise.all([orgProviders.load(), orgProviders.loadInstallTargets(), providers.load()])
}
onMounted(refreshTargets)

async function register() {
  if (busy.value || !canManage.value || !enrollmentAvailable.value || !target.value || !normalizedName.value || nameError.value) return
  const current = ++generation
  const org = tenant.orgUUID
  const selected = { workspace: target.value.workspace, name: target.value.name }
  busy.value = true
  error.value = ''
  try {
    const response = await orgProviders.register(normalizedName.value, undefined, selected)
    if (generation === current && tenant.orgUUID === org && canManage.value) registration.value = response
  } catch (e) {
    if (generation === current) error.value = e instanceof Error ? e.message : 'Registration failed. Try again.'
  } finally {
    if (generation === current) busy.value = false
  }
}
async function checkInstall() {
  if (checking.value) return
  const current = generation
  checking.value = true
  try { await Promise.all([orgProviders.load(), providers.load()]) }
  finally { if (generation === current) checking.value = false }
}
</script>

<template>
  <AppLayout>
    <main class="k-resource-page mx-auto w-full max-w-3xl p-6">
      <router-link :to="scopePath('/providers')" class="mb-5 inline-flex items-center gap-2 text-sm text-text-muted hover:text-accent">
        <ArrowLeft class="h-4 w-4" /> Providers
      </router-link>
      <h1 class="text-xl font-semibold text-text-primary">Register your own provider</h1>
      <p class="mt-2 mb-6 text-sm text-text-muted">Make your provider available to {{ tenant.activeOrg?.displayName || 'your organization' }}. Deploy it on a connected cluster, then enable it in the workspaces that need it.</p>

      <p v-if="!canManage" class="text-sm text-text-muted" role="status">An organization admin must register and manage providers.</p>
      <template v-else-if="registration">
        <section class="mb-6 space-y-3" aria-labelledby="provider-install-heading">
          <h2 id="provider-install-heading" class="text-base font-semibold text-text-primary">Deploy {{ registration.provider.name }}</h2>
          <SelfHostInstructions :registration="registration" />
        </section>
        <section class="border-t border-border-subtle pt-5" aria-labelledby="provider-status-heading">
          <h2 id="provider-status-heading" class="text-base font-semibold text-text-primary">Check installation</h2>
          <p class="mt-2 text-sm text-text-muted" role="status" aria-live="polite">
            {{ installed?.ready ? 'The provider is ready. Open the catalog to review its permissions and enable it in a workspace.' : installed?.registered ? 'The provider registered its APIs and is not ready yet. Check its init and readiness logs, then retry.' : 'Waiting for the provider to publish its APIs. Deploy your chart using the credential above.' }}
          </p>
          <p v-if="orgProviders.error" class="mt-2 text-sm text-danger" role="alert">{{ orgProviders.error }}</p>
          <div class="mt-4 flex flex-wrap gap-3">
            <button type="button" class="k-btn k-btn--secondary" :disabled="checking" @click="checkInstall">
              <Loader2 v-if="checking" class="h-4 w-4 animate-spin" /><RefreshCw v-else class="h-4 w-4" /> Check installation
            </button>
            <router-link v-if="installed?.ready" :to="scopePath('/providers')" class="k-btn k-btn--primary">Open provider catalog</router-link>
          </div>
        </section>
      </template>
      <form v-else class="space-y-5" @submit.prevent="register">
        <div v-if="!enrollmentAvailable" class="rounded-lg border border-warning/30 bg-warning-subtle px-3 py-2.5 text-sm text-warning" role="status" aria-live="polite">
          <p>{{ enrollmentMessage }}</p>
          <button v-if="!orgProviders.loading" type="button" class="mt-1 font-medium underline" @click="orgProviders.load">Retry availability check</button>
        </div>
        <div>
          <label for="byo-provider-name" class="mb-2 block text-sm font-medium text-text-primary">Provider name</label>
          <input id="byo-provider-name" v-model="name" class="k-input w-full" required maxlength="63" autocomplete="off" spellcheck="false" :disabled="busy || !enrollmentAvailable" :aria-invalid="!!nameError" aria-describedby="byo-name-help" />
          <p id="byo-name-help" class="mt-2 text-sm" :class="nameError ? 'text-danger' : 'text-text-muted'">{{ nameError || 'Use the name from your provider manifest. It must be unique in this organization.' }}</p>
        </div>
        <div>
          <label for="byo-provider-edge" class="mb-2 block text-sm font-medium text-text-primary">Hosting cluster</label>
          <select id="byo-provider-edge" v-model="targetKey" class="k-input w-full" :disabled="busy || !enrollmentAvailable || orgProviders.installTargetsLoading || !targets.length" :required="targets.length !== 1" aria-describedby="byo-edge-help">
            <option value="">{{ targets.length === 1 ? `${targets[0].name} · ${targets[0].workspaceDisplayName || targets[0].workspace}` : 'Select a connected cluster' }}</option>
            <option v-for="edge in targets" :key="`${edge.workspace}/${edge.name}`" :value="`${edge.workspace}/${edge.name}`">{{ edge.name }} · {{ edge.workspaceDisplayName || edge.workspace }}</option>
          </select>
          <p id="byo-edge-help" class="mt-2 text-sm text-text-muted">{{ orgProviders.installTargetsLoading ? 'Checking connected clusters…' : targets.length ? 'Railgrid reaches your provider through this cluster’s outbound Edge connection.' : orgProviders.installTargetsReason || 'Connect a Kubernetes cluster in Edges before registering a provider.' }}</p>
          <button v-if="orgProviders.installTargetsError" type="button" class="k-btn k-btn--ghost mt-2" @click="refreshTargets">Retry cluster check</button>
        </div>
        <p class="text-sm text-text-muted">When hosted enrollment is available, registration creates a provider workspace and a credential for your deployment. You install your own chart. Provider pages run isolated from the console’s login and page.</p>
        <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
        <div class="flex items-center gap-3 border-t border-border-subtle pt-4">
          <button type="submit" class="k-btn k-btn--primary" :disabled="busy || !enrollmentAvailable || !target || !normalizedName || !!nameError">
            <Loader2 v-if="busy" class="h-4 w-4 animate-spin" /> {{ busy ? 'Registering…' : 'Register provider' }}
          </button>
          <router-link :to="scopePath('/providers')" class="k-btn k-btn--ghost">Cancel</router-link>
        </div>
      </form>
    </main>
  </AppLayout>
</template>
