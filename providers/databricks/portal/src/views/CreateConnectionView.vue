<script setup lang="ts">
import { ArrowLeft, LoaderCircle, RefreshCw } from 'lucide-vue-next'
import { inject, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { api } from '../api'
import { contextGenerationKey } from '../context'
import { formatDatabricksError } from '../errors'
import {
  createOperationLocks,
  operationKey,
} from '../refresh'
import { resourceNameError } from '../resourceName'
import ManualCreateGuidance from '../components/ManualCreateGuidance.vue'

const emit = defineEmits<{
  (event: 'cancel'): void
  (event: 'created', name: string): void
}>()

const loading = ref(false)
const loaded = ref(false)
const loadError = ref<string | null>(null)
const submitting = ref(false)
const formError = ref<string | null>(null)
const nameInput = ref<HTMLInputElement | null>(null)
const formErrorRef = ref<HTMLElement | null>(null)
const operations = createOperationLocks()
const contextGeneration = inject(contextGenerationKey, ref(0))

// Route-owned forms can be replaced while a read or write is in flight (for
// example when the host rotates the tenant context). Keep every async
// continuation tied to the mounted form that started it.
let mounted = false
let readGeneration = 0
let mutationGeneration = 0

interface ReadToken {
  generation: number
  context: number
}

const form = reactive({
  name: '',
  host: '',
  token: '',
})

function isCurrentRead(generation: number, expectedContext: number): boolean {
  return mounted && generation === readGeneration && contextGeneration.value === expectedContext
}

function isCurrentMutation(generation: number, expectedContext: number): boolean {
  return mounted && generation === mutationGeneration && contextGeneration.value === expectedContext
}

async function load(): Promise<ReadToken | null> {
  const generation = ++readGeneration
  const expectedContext = contextGeneration.value
  loading.value = true
  loadError.value = null
  loaded.value = false
  try {
    await api.listConnections()
    if (!isCurrentRead(generation, expectedContext)) return null
    loaded.value = true
    return { generation, context: expectedContext }
  } catch (error) {
    if (!isCurrentRead(generation, expectedContext)) return null
    loadError.value = formatDatabricksError(error)
    return null
  } finally {
    if (isCurrentRead(generation, expectedContext)) loading.value = false
  }
}

async function focusFormError(message: string, generation: number, expectedContext: number): Promise<void> {
  if (!isCurrentMutation(generation, expectedContext)) return
  formError.value = message
  await nextTick()
  if (isCurrentMutation(generation, expectedContext)) formErrorRef.value?.focus()
}

async function submit(): Promise<void> {
  if (!mounted || submitting.value) return
  const generation = ++mutationGeneration
  const expectedContext = contextGeneration.value
  formError.value = null
  if (!loaded.value) {
    await focusFormError('Connection list is still loading. Retry the read before creating a connection.', generation, expectedContext)
    return
  }
  if (!form.name || !form.host || !form.token) {
    await focusFormError('Name, workspace host, and token are required.', generation, expectedContext)
    return
  }
  const nameError = resourceNameError(form.name, 'Name')
  if (nameError) {
    await focusFormError(nameError, generation, expectedContext)
    return
  }
  const desiredName = form.name.trim()
  const payload = {
    name: desiredName,
    host: form.host,
    token: form.token,
  }
  const lock = operationKey('connection', desiredName)
  if (!operations.acquire(lock, 'creating')) {
    await focusFormError(`Connection "${desiredName}" already has an update in progress.`, generation, expectedContext)
    return
  }
  submitting.value = true
  try {
    // Re-read the complete collection immediately before applying. The page
    // the user entered from may have become stale while the form was open.
    const existing = await api.listConnections()
    if (!isCurrentMutation(generation, expectedContext)) return
    operations.reconcile('connection', existing.map(({ name, uid }) => ({ name, uid })))
    if (operations.isTombstoned(lock)) {
      await focusFormError(`Connection "${desiredName}" is still being removed. Retry after the list refresh confirms it is gone.`, generation, expectedContext)
      return
    }
    if (existing.some(connection => connection.name === desiredName)) {
      await focusFormError(`Connection "${desiredName}" already exists.`, generation, expectedContext)
      return
    }
    const created = await api.saveConnection(payload)
    if (!isCurrentMutation(generation, expectedContext)) return
    emit('created', created.name)
  } catch (error) {
    await focusFormError(formatDatabricksError(error), generation, expectedContext)
  } finally {
    operations.release(lock)
    if (isCurrentMutation(generation, expectedContext)) submitting.value = false
  }
}

function cancel(): void {
  if (!submitting.value) emit('cancel')
}

onMounted(() => {
  mounted = true
  void load().then(readToken => {
    if (readToken && isCurrentRead(readToken.generation, readToken.context)) nameInput.value?.focus()
  })
})

onBeforeUnmount(() => {
  mounted = false
  readGeneration += 1
  mutationGeneration += 1
})
</script>

<template>
  <section class="page k-create-page" :aria-busy="loading || submitting ? 'true' : 'false'">
    <button class="k-btn k-btn--ghost k-back-action" type="button" :disabled="submitting" @click="cancel">
      <ArrowLeft :size="14" aria-hidden="true" /> Connections
    </button>
    <header class="k-create-header">
      <h2 class="k-create-title">Create connection</h2>
      <p class="k-create-description">Connect a Databricks workspace for warehouse and table imports.</p>
    </header>

    <form class="k-create-surface k-create-surface--wide manual-create-form manual-create-form--connection" @submit.prevent="submit">
      <div class="k-create-body manual-create-body--guided">
        <div class="manual-create-form-fields manual-create-form-fields--connection">
          <p v-if="loading" class="muted" role="status"><LoaderCircle class="spin" :size="14" aria-hidden="true" /> Loading existing connections…</p>
          <p v-if="loadError" class="error" role="alert" aria-live="assertive">
            <span>Could not load existing connections: {{ loadError }}</span>
            <button class="k-btn k-btn--ghost" type="button" @click="load"><RefreshCw :size="14" aria-hidden="true" /> Retry</button>
          </p>
          <div class="manual-create-fields-grid manual-create-fields-grid--connection">
            <div class="field">
              <label class="field-label" for="connection-name">Name</label>
              <input id="connection-name" ref="nameInput" class="k-input" v-model="form.name" :disabled="loading || submitting" autocomplete="off" placeholder="orders-prod" required aria-required="true" aria-describedby="connection-name-hint connection-form-error" :aria-invalid="!!formError" />
              <span id="connection-name-hint" class="field-hint">How this workspace is referred to from faros. Use lowercase letters, numbers, and hyphens; the name is preserved exactly.</span>
            </div>
            <div class="field">
              <label class="field-label" for="connection-host">Workspace host</label>
              <input id="connection-host" class="k-input" v-model="form.host" :disabled="loading || submitting" autocomplete="url" placeholder="https://dbc-example.cloud.databricks.com" required aria-required="true" aria-describedby="connection-host-hint connection-form-error" :aria-invalid="!!formError" />
              <span id="connection-host-hint" class="field-hint">Use the HTTPS root URL from the Databricks browser address bar (AWS, Azure, or GCP), with no path.</span>
            </div>
            <div class="field">
              <label class="field-label" for="connection-token">Token</label>
              <input id="connection-token" class="k-input" v-model="form.token" :disabled="loading || submitting" type="password" autocomplete="new-password" placeholder="Paste token" required aria-required="true" aria-describedby="connection-token-hint connection-form-error" :aria-invalid="!!formError" />
              <span id="connection-token-hint" class="field-hint">Use a Databricks personal access token with access to the resources you plan to import.</span>
              <details class="field-disclosure">
                <summary>Where to find it and required access</summary>
                <p>In Databricks, open your avatar → Settings → Developer → Access tokens. The token identity needs SELECT on the catalogs and schemas you plan to import, plus access to a running SQL warehouse.</p>
              </details>
            </div>
            <p class="muted manual-create-security-note">Faros stores the token as a Secret in this workspace. Connection status shows whether Databricks accepted it.</p>
          </div>
        </div>
        <ManualCreateGuidance
          kind="connection"
          :values="{ name: form.name, host: form.host, tokenPresent: !!form.token }"
        />
      </div>
      <div class="k-create-actions">
        <span v-if="formError" id="connection-form-error" ref="formErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ formError }}</span>
        <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="cancel">Cancel</button>
        <button class="k-btn k-btn--primary" type="submit" :disabled="loading || submitting">{{ submitting ? 'Connecting…' : 'Create connection' }}</button>
      </div>
    </form>
  </section>
</template>
