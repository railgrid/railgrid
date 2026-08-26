<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowLeft, Ellipsis, GitBranch, KeyRound, Link2, Plug, RefreshCw, User } from 'lucide-vue-next'
import { api } from '../api'
import type { ConnectionDetail, ErrorResponse } from '../types'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceStatCards, { type ResourceStatCard } from '../portalkit/ResourceStatCards.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  createOperationLocks,
  operationKey,
  type LatestRefreshController,
  type ResourceDeletions,
  type ResourceRefreshMode,
} from '../refresh'

const props = defineProps<{ name: string; deletions: ResourceDeletions }>()
const emit = defineEmits<{ (e: 'back'): void }>()
const deletionScope = 'connection'

const conn = ref<ConnectionDetail | null>(null)
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const loading = ref(true)
const loaded = ref(false)
const actionsMenu = ref<HTMLDetailsElement | null>(null)
const operations = createOperationLocks()
let mounted = false
let refresh!: LatestRefreshController
const refreshMode = ref<ResourceRefreshMode>('foreground')

const validated = computed(() => conn.value?.conditions.find(c => c.type === 'Validated'))
const reconciled = computed(() =>
  !!conn.value &&
  conn.value.observedGeneration !== undefined &&
  conn.value.generation !== undefined &&
  conn.value.observedGeneration >= conn.value.generation,
)
const connectionDeleteInFlight = computed(() => operations.phase(operationKey('connection', conn.value?.name || props.name)) === 'deleting')
const deleting = computed(() => !!conn.value && (
  connectionDeleteInFlight.value ||
  !!conn.value.deletionTimestamp ||
  props.deletions.has(deletionScope, conn.value.name, conn.value.uid)
))
const hint = computed(() => {
  const c = conn.value
  if (!c || c.validated) return ''
  if (!c.conditions.length || !reconciled.value) {
    return 'Waiting for the connection controller to validate the credential. This usually takes a few seconds after creation.'
  }
  switch (validated.value?.reason) {
    case 'CredentialUnavailable':
      return `The credential Secret could not be read. Check that the Secret "${c.secretName}"` +
        (c.secretNamespace ? ` in namespace "${c.secretNamespace}"` : '') +
        ` exists and holds key "${c.secretKey || 'token'}".`
    case 'ValidationFailed':
      return 'The git host rejected the credential. The token may be expired, revoked, or missing the scopes needed for this owner.'
    case 'ProviderNotFound':
      return `No backend is registered for provider "${c.provider}". This is a provider configuration issue — contact a platform admin.`
    default:
      return validated.value?.message || 'The connection is not validated yet.'
  }
})

const connectionStatus = computed(() => {
  if (deleting.value) return 'Deleting'
  if (!conn.value) return loading.value ? 'Loading' : 'Unavailable'
  return conn.value.validated ? 'ready' : 'pending'
})
const connectionStatusTone = computed<'success' | 'warning' | 'danger' | 'muted' | null>(() => {
  if (connectionStatus.value === 'ready') return 'success'
  if (connectionStatus.value === 'Unavailable') return 'danger'
  if (connectionStatus.value === 'Deleting' || connectionStatus.value === 'Loading' || connectionStatus.value === 'pending') return 'warning'
  return 'muted'
})
const detailRefreshing = computed(() => loading.value)
const foregroundRefreshing = computed(() => loading.value && refreshMode.value === 'foreground')
const connectionActionBusy = computed(() =>
  !conn.value ||
  foregroundRefreshing.value ||
  deleting.value ||
  operations.isLocked(operationKey('connection', conn.value?.name || props.name)),
)
// ResourcePage distinguishes an explicit first read from the expected
// TenantMissing/no-context state while the host changes workspaces. Keep the
// null sentinel for that no-context state so the body is not replaced by a
// misleading error or skeleton.
const connectionReadState = computed<boolean | null>(() => {
  if (loaded.value) return true
  if (error.value) return false
  return loading.value ? false : null
})
const poller = createAdaptiveRefreshTimer(() => load('background'), () => {
  if (!loaded.value || !conn.value || error.value) return FAST_REFRESH_MS
  if (deleting.value || !conn.value.validated || !reconciled.value) return FAST_REFRESH_MS
  return STABLE_REFRESH_MS
})

const connectionStatCards = computed<ResourceStatCard[]>(() => {
  const cards: ResourceStatCard[] = [
    {
      id: 'connection',
      label: 'Connection',
      value: connectionStatus.value,
      detail: conn.value?.message || undefined,
      icon: Plug,
      tone: connectionStatusTone.value === 'muted' ? 'default' : connectionStatusTone.value || 'default',
    },
    {
      id: 'provider',
      label: 'Provider',
      value: conn.value?.provider || '—',
      icon: GitBranch,
    },
    {
      id: 'type',
      label: 'Type',
      value: conn.value?.type || '—',
      icon: Link2,
    },
    {
      id: 'owner',
      label: 'Owner',
      value: conn.value?.owner || '—',
      icon: User,
      mono: true,
    },
  ]
  const login = conn.value?.login?.trim()
  if (login) cards.push({ id: 'login', label: 'Login', value: login, icon: User, mono: true })
  if (conn.value?.scopes.length) {
    cards.push({
      id: 'scopes',
      label: 'Scopes',
      value: String(conn.value.scopes.length),
      detail: 'granted',
      icon: KeyRound,
    })
  }
  return cards
})

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function load(mode: ResourceRefreshMode = 'foreground') {
  if (mode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  refresh.request(mode)
}

async function deleteConnection() {
  const current = conn.value
  if (!current || deleting.value) return
  const ok = await confirmDialog({
    title: `Delete connection "${current.name}"?`,
    message: 'Repositories using it will stop reconciling.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok || !mounted) return
  const lock = operationKey('connection', current.name)
  if (!operations.acquire(lock, 'deleting')) return
  mutationError.value = null
  try {
    await api.deleteConnection(current.name)
    props.deletions.acknowledge(deletionScope, current.name, current.uid)
    emit('back')
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

function deleteFromMenu() {
  actionsMenu.value?.removeAttribute('open')
  void deleteConnection()
}

refresh = createLatestRefreshController(async (requestID, mode) => {
  refreshMode.value = mode
  loading.value = true
  try {
    const next = await api.getConnection(props.name)
    if (!refresh.isCurrent(requestID)) return
    conn.value = next
    if (next.deletionTimestamp) props.deletions.acknowledge(deletionScope, next.name, next.uid)
    loaded.value = true
    error.value = null
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    if (err.reason === 'NotFound' && (deleting.value || props.deletions.has(deletionScope, props.name))) {
      // A confirmed not-found must not leave a tombstoned snapshot looking
      // current. The collection owns the next route after deletion.
      conn.value = null
      emit('back')
      return
    }
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (refresh.isCurrent(requestID)) {
      loading.value = false
      poller.schedule()
    }
  }
})

watch(() => props.name, () => {
  conn.value = null
  error.value = null
  mutationError.value = null
  loaded.value = false
  loading.value = true
  refreshMode.value = 'foreground'
  refresh.invalidate()
  load()
})

onMounted(() => {
  mounted = true
  load()
  poller.schedule()
})
onUnmounted(() => {
  mounted = false
  poller.stop()
  refresh.stop()
})
</script>

<template>
  <div class="connection-detail">
    <a class="k-btn k-btn--ghost connection-detail__back" href="/ui/providers/code/connections" @click.prevent="emit('back')">
      <ArrowLeft :size="14" aria-hidden="true" /> Connections
    </a>

    <div class="connection-detail__resource">
      <div class="connection-detail__provider-mark" role="img" :aria-label="`${conn?.provider || 'Provider unavailable'} mark`">
        <Plug :size="20" :stroke-width="1.75" aria-hidden="true" />
      </div>

      <ResourcePage
        :title="conn?.name || name"
        kind="Connection"
        :loaded="connectionReadState"
        :loading="loading"
        :refresh-mode="refreshMode"
        :error="error"
        :stale="loaded && !!error"
        retryable
        @retry="load"
      >
        <template #meta>
          <span>{{ conn?.provider || 'Provider unavailable' }}</span>
          <span class="connection-header__separator" aria-hidden="true">·</span>
          <span>{{ conn?.type || 'Type unavailable' }}</span>
          <template v-if="conn?.login">
            <span class="connection-header__separator" aria-hidden="true">·</span>
            <span>authenticated as <code>{{ conn.login }}</code></span>
          </template>
        </template>
        <template #status>
          <StatusBadge :status="connectionStatus" :tone="connectionStatusTone" :title="conn?.message" />
        </template>

        <template #actions>
          <div class="connection-detail__actions" role="group" aria-label="Connection actions">
            <button
              type="button"
              class="k-btn k-btn--ghost"
              :disabled="connectionActionBusy"
              :aria-busy="detailRefreshing || undefined"
              @click="load()"
            >
              <RefreshCw :size="14" :class="{ spin: foregroundRefreshing }" aria-hidden="true" />
              {{ foregroundRefreshing ? 'Refreshing…' : 'Refresh' }}
            </button>
            <details ref="actionsMenu" class="connection-detail__menu">
              <summary class="k-btn k-btn--ghost" aria-label="More connection actions">
                <Ellipsis :size="16" aria-hidden="true" />
                <span class="sr-only">More actions</span>
              </summary>
              <div class="connection-detail__menu-popover">
                <button
                  type="button"
                  class="connection-detail__menu-item"
                  :disabled="connectionActionBusy"
                  @click="deleteFromMenu"
                >
                  Delete connection
                </button>
              </div>
            </details>
          </div>
        </template>

        <template #summary>
          <ResourceStatCards :cards="connectionStatCards" density="compact" aria-label="Connection summary" />
        </template>

        <template #body>
          <template v-if="conn">
            <span v-if="foregroundRefreshing && loaded && !error" class="sr-only" role="status" aria-live="polite">Updating connection…</span>
            <p v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">{{ mutationError }}</p>
            <p v-if="deleting" class="connection-detail__deleting" role="status" aria-live="polite">
              Deleting this connection. The last successful snapshot remains visible until the hub confirms removal.
            </p>

            <div class="connection-detail__sections">
              <ResourceSectionCard id="connection-overview" eyebrow="Configuration" title="Overview" description="Connection identity and the provider account it represents.">
                <div v-if="!deleting && !conn.validated && hint" class="connection-detail__hint" role="status" aria-live="polite">
                  <h3 class="connection-detail__hint-title">Status</h3>
                  <p>{{ hint }}</p>
                </div>

                <dl class="props connection-detail__facts">
                  <dt>Provider</dt><dd>{{ conn.provider }}</dd>
                  <dt>Type</dt><dd>{{ conn.type }}</dd>
                  <dt>Owner</dt><dd>{{ conn.owner }}</dd>
                  <dt v-if="conn.login">Login</dt><dd v-if="conn.login">{{ conn.login }}</dd>
                  <dt v-if="conn.scopes.length">Scopes</dt>
                  <dd v-if="conn.scopes.length">
                    <code v-for="scope in conn.scopes" :key="scope" class="chip">{{ scope }}</code>
                  </dd>
                  <dt v-if="conn.baseURL">Base URL</dt><dd v-if="conn.baseURL"><code>{{ conn.baseURL }}</code></dd>
                  <dt v-if="conn.observedGeneration !== undefined">Reconciled</dt>
                  <dd v-if="conn.observedGeneration !== undefined">
                    <span v-if="reconciled" class="muted">up to date (generation {{ conn.generation }})</span>
                    <span v-else class="warn">controller has not caught up (spec {{ conn.generation }}, observed {{ conn.observedGeneration }})</span>
                  </dd>
                </dl>
              </ResourceSectionCard>

              <ResourceSectionCard id="connection-credentials" eyebrow="Credentials" title="Credential reference" description="The connection points to a workspace Secret; the credential value is never shown here.">
                <dl class="props connection-detail__facts">
                  <dt>Secret name</dt>
                  <dd><code>{{ conn.secretName }}</code></dd>
                  <dt v-if="conn.secretNamespace">Namespace</dt>
                  <dd v-if="conn.secretNamespace"><code>{{ conn.secretNamespace }}</code></dd>
                  <dt v-if="conn.secretKey">Key</dt>
                  <dd v-if="conn.secretKey"><code>{{ conn.secretKey }}</code></dd>
                </dl>
              </ResourceSectionCard>

              <ResourceSectionCard id="connection-conditions" eyebrow="Diagnostics" title="Health" description="Controller validation and reconciliation evidence for this connection.">
                <ConditionsPanel
                  :conditions="conn.conditions"
                  :generation="conn.generation"
                  :observed-generation="conn.observedGeneration"
                  empty-text="No conditions yet — the controller has not reconciled this connection."
                />
                <dl class="connection-detail__health-facts" aria-label="Connection health facts">
                  <div><dt>Validation</dt><dd>{{ conn.validated ? 'Validated' : 'Waiting for validation' }}</dd></div>
                  <div><dt>Validation reason</dt><dd class="mono">{{ validated?.reason || '—' }}</dd></div>
                  <div><dt>Reconciliation</dt><dd>{{ reconciled ? 'Current' : 'Pending' }}</dd></div>
                </dl>
              </ResourceSectionCard>
            </div>
          </template>
        </template>
      </ResourcePage>
    </div>
  </div>
</template>

<style scoped>
.chip { margin-right: 0.35rem; }
</style>
