<script setup lang="ts">
import { ArrowLeft, ArrowRight, LoaderCircle, RefreshCw } from 'lucide-vue-next'
import { computed, inject, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { api } from '../api'
import { contextGenerationKey } from '../context'
import { formatDatabricksError } from '../errors'
import {
  createOperationLocks,
  operationKey,
} from '../refresh'
import { importPrerequisiteMessage, nextValidWarehouseRef, warehousesForConnection } from '../tableRefs'
import { resourceNameError } from '../resourceName'
import type { DatabricksPrerequisiteKind } from '../journey'
import type { Connection, Table, Warehouse } from '../types'
import FormSelect from '../portalkit/FormSelect.vue'
import ManualCreateGuidance from '../components/ManualCreateGuidance.vue'

const props = defineProps<{
  /** When set, this route edits the named table instead of creating one. */
  editName?: string
}>()

const emit = defineEmits<{
  (event: 'cancel'): void
  (event: 'created', name: string): void
  (event: 'prerequisite', kind: DatabricksPrerequisiteKind): void
}>()

const connections = ref<Connection[]>([])
const warehouses = ref<Warehouse[]>([])
const loading = ref(false)
const loaded = ref(false)
const loadError = ref<string | null>(null)
const submitting = ref(false)
const formError = ref<string | null>(null)
const nameInput = ref<HTMLInputElement | null>(null)
const connectionInput = ref<{ focus: () => void } | null>(null)
const formErrorRef = ref<HTMLElement | null>(null)
const operations = createOperationLocks()
const contextGeneration = inject(contextGenerationKey, ref(0))
const editing = computed(() => props.editName !== undefined)

// Route-owned forms may be replaced while a read or write is in flight. Every
// async continuation must belong to the mounted form and latest attempt that
// started it.
let mounted = false
let readGeneration = 0
let mutationGeneration = 0

interface ReadToken {
  generation: number
  context: number
}

const form = reactive({
  name: '',
  connectionRef: '',
  warehouseRef: '',
  catalog: '',
  schema: '',
  table: '',
})

const tableImportBlocker = computed(() => !loaded.value
  ? ''
  : importPrerequisiteMessage(connections.value, warehouses.value, form.connectionRef))
const formWarehouses = computed(() => warehousesForConnection(warehouses.value, form.connectionRef))
const hasConnections = computed(() => connections.value.length > 0)
const hasSelectedConnection = computed(() => hasConnections.value && connections.value.some(connection => connection.name === form.connectionRef))
const connectionOptions = computed(() => connections.value.map(connection => ({ value: connection.name, label: connection.name })))
const warehouseOptions = computed(() => formWarehouses.value.map(warehouse => ({ value: warehouse.name, label: warehouse.name })))

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
  loaded.value = false
  loadError.value = null
  try {
    const tableRead: Promise<Table | null> = editing.value
      ? api.getTable(props.editName as string)
      : Promise.resolve(null)
    const [table, availableConnections, availableWarehouses] = await Promise.all([
      tableRead,
      api.listConnections(),
      api.listWarehouses(),
    ])
    if (!isCurrentRead(generation, expectedContext)) return null
    connections.value = availableConnections
    warehouses.value = availableWarehouses
    if (table) {
      // Keep the route-owned name as the immutable identity. The server read
      // supplies every editable reference and Databricks locator.
      form.name = props.editName as string
      form.connectionRef = table.connectionRef
      form.warehouseRef = table.warehouseRef
      form.catalog = table.catalog
      form.schema = table.schema
      form.table = table.table
    } else {
      if (!connections.value.some(connection => connection.name === form.connectionRef)) {
        form.connectionRef = connections.value[0]?.name ?? ''
      }
      form.warehouseRef = nextValidWarehouseRef(warehouses.value, form.connectionRef, form.warehouseRef)
    }
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

// Prefill the Databricks-provided samples catalog (readable in every
// workspace) so a first import needs no lookup: samples.nyctaxi.trips.
function fillDemo(): void {
  if (editing.value) return
  form.name = 'nyctaxi-trips'
  form.catalog = 'samples'
  form.schema = 'nyctaxi'
  form.table = 'trips'
  formError.value = null
}

async function submit(): Promise<void> {
  if (!mounted || submitting.value) return
  const generation = ++mutationGeneration
  const expectedContext = contextGeneration.value
  formError.value = null
  if (!loaded.value) {
    await focusFormError(
      editing.value
        ? 'Table and prerequisite reads are still loading. Retry before saving the table.'
        : 'Connection and warehouse lists are still loading. Retry the read before creating a table.',
      generation,
      expectedContext,
    )
    return
  }
  if (tableImportBlocker.value) {
    await focusFormError(tableImportBlocker.value, generation, expectedContext)
    return
  }
  if (!form.name || !form.connectionRef || !form.warehouseRef || !form.catalog || !form.schema || !form.table) {
    await focusFormError('All table fields are required.', generation, expectedContext)
    return
  }
  const nameError = resourceNameError(form.name, 'Name')
  if (nameError) {
    await focusFormError(nameError, generation, expectedContext)
    return
  }
  const desiredName = (props.editName ?? form.name).trim()
  const payload = {
    name: desiredName,
    connectionRef: form.connectionRef,
    warehouseRef: form.warehouseRef,
    catalog: form.catalog,
    schema: form.schema,
    table: form.table,
  }
  const lock = operationKey('table', desiredName)
  if (!operations.acquire(lock, editing.value ? 'saving' : 'creating')) {
    await focusFormError(`Table "${desiredName}" already has an update in progress.`, generation, expectedContext)
    return
  }
  submitting.value = true
  try {
    // Resolve duplicate and foreign-key checks from complete, current reads;
    // the collections that preceded this route may only have shown one page.
    const [existingTables, availableConnections, availableWarehouses] = await Promise.all([
      api.listTables(),
      api.listConnections(),
      api.listWarehouses(),
    ])
    if (!isCurrentMutation(generation, expectedContext)) return
    operations.reconcile('table', existingTables.map(({ name, uid }) => ({ name, uid })))
    if (operations.isTombstoned(lock)) {
      await focusFormError(`Table "${desiredName}" is still being removed. Retry after the list refresh confirms it is gone.`, generation, expectedContext)
      return
    }
    const existingTable = existingTables.find(table => table.name === desiredName)
    if (!editing.value && existingTable) {
      await focusFormError(`Table "${desiredName}" already exists.`, generation, expectedContext)
      return
    }
    if (editing.value && !existingTable) {
      await focusFormError(`Table "${desiredName}" no longer exists in this workspace.`, generation, expectedContext)
      return
    }
    if (!availableConnections.some(connection => connection.name === payload.connectionRef)) {
      await focusFormError('Selected connection is no longer available in this workspace.', generation, expectedContext)
      return
    }
    if (!availableWarehouses.some(warehouse => warehouse.name === payload.warehouseRef && warehouse.connectionRef === payload.connectionRef)) {
      await focusFormError('Selected warehouse must belong to the selected connection.', generation, expectedContext)
      return
    }
    const created = await api.saveTable(payload)
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
    if (!readToken || !isCurrentRead(readToken.generation, readToken.context)) return
    // The edit identity is intentionally disabled. Put focus on the first
    // editable control so keyboard users can continue immediately.
    const initialInput = editing.value ? connectionInput.value : nameInput.value
    initialInput?.focus()
  })
})

onBeforeUnmount(() => {
  mounted = false
  readGeneration += 1
  mutationGeneration += 1
})

watch(() => form.connectionRef, connectionRef => {
  form.warehouseRef = nextValidWarehouseRef(warehouses.value, connectionRef, form.warehouseRef)
})
</script>

<template>
  <section class="page k-create-page" :aria-busy="loading || submitting ? 'true' : 'false'">
    <button class="k-btn k-btn--ghost k-back-action" type="button" :disabled="submitting" @click="cancel">
      <ArrowLeft :size="14" aria-hidden="true" /> Tables
    </button>
    <header class="k-create-header">
      <h2 class="k-create-title">{{ editing ? 'Edit table' : 'Register table' }}</h2>
      <p class="k-create-description">{{ editing ? 'Update the metadata-only Databricks table handle used by App Studio and MCP.' : 'Register a metadata-only Databricks table handle for App Studio and MCP.' }}</p>
    </header>

    <form class="k-create-surface k-create-surface--wide manual-create-form manual-create-form--table" @submit.prevent="submit">
      <div class="k-create-body manual-create-body--guided">
        <div class="manual-create-form-fields manual-create-form-fields--table">
          <div class="databricks-resource-panel-head">
            <span class="databricks-resource-panel-title">{{ editing ? 'Table configuration' : 'Table details' }}</span>
            <button v-if="!editing" class="k-btn k-btn--ghost databricks-inline-action" type="button" :disabled="loading || submitting" @click="fillDemo" title="Prefill samples.nyctaxi.trips — Databricks demo data available in every workspace">Fill with demo data</button>
          </div>
          <p v-if="loading" class="muted" role="status"><LoaderCircle class="spin" :size="14" aria-hidden="true" /> {{ editing ? 'Loading table, connections, and warehouses…' : 'Loading connections and warehouses…' }}</p>
          <p v-if="loadError" class="error" role="alert" aria-live="assertive">
            <span>{{ editing ? 'Could not load table and prerequisites' : 'Could not load table prerequisites' }}: {{ loadError }}</span>
            <button class="k-btn k-btn--ghost" type="button" @click="load"><RefreshCw :size="14" aria-hidden="true" /> Retry</button>
          </p>
          <div v-if="tableImportBlocker" class="prerequisite" role="status">
            <span class="prerequisite-copy">{{ tableImportBlocker }}</span>
            <button v-if="!editing && !hasSelectedConnection" class="k-btn k-btn--ghost prerequisite-action" type="button" @click="emit('prerequisite', 'connection')">
              Create connection <ArrowRight :size="14" :stroke-width="1.75" aria-hidden="true" />
            </button>
            <button v-else-if="!editing && !formWarehouses.length" class="k-btn k-btn--ghost prerequisite-action" type="button" @click="emit('prerequisite', 'warehouse')">
              Register warehouse <ArrowRight :size="14" :stroke-width="1.75" aria-hidden="true" />
            </button>
          </div>
          <div class="form-grid">
            <label class="field" for="table-name">
              <span class="field-label">Name</span>
              <input id="table-name" ref="nameInput" class="k-input" v-model="form.name" :disabled="loading || submitting" :readonly="editing" autocomplete="off" placeholder="order-history" required aria-required="true" aria-describedby="table-name-hint table-form-error" :aria-invalid="!!formError" />
              <span id="table-name-hint" class="field-hint">The stable tableRef exposed to App Studio. Use lowercase letters, numbers, and hyphens; the name is preserved exactly{{ editing ? ' and cannot be changed.' : '.' }}</span>
            </label>
            <div class="field">
              <label id="table-connection-label" class="field-label" for="table-connection">Connection</label>
              <FormSelect
                id="table-connection"
                ref="connectionInput"
                v-model="form.connectionRef"
                name="connectionRef"
                :options="connectionOptions"
                placeholder="Select connection"
                :disabled="loading || submitting || !hasConnections"
                required
                :invalid="!!formError"
                labelledby="table-connection-label"
                describedby="table-connection-hint table-form-error"
              />
              <span id="table-connection-hint" class="field-hint">The Databricks workspace connection for this table.</span>
            </div>
            <div class="field">
              <label id="table-warehouse-label" class="field-label" for="table-warehouse">Warehouse</label>
              <FormSelect
                id="table-warehouse"
                v-model="form.warehouseRef"
                name="warehouseRef"
                :options="warehouseOptions"
                :placeholder="formWarehouses.length ? 'Select warehouse' : 'No warehouses for this connection'"
                :disabled="loading || submitting || !formWarehouses.length"
                required
                :invalid="!!formError"
                labelledby="table-warehouse-label"
                describedby="table-warehouse-hint table-form-error"
              />
              <span id="table-warehouse-hint" class="field-hint">A warehouse that belongs to the selected connection.</span>
            </div>
            <label class="field" for="table-catalog">
              <span class="field-label">Catalog</span>
              <input id="table-catalog" class="k-input" v-model="form.catalog" :disabled="loading || submitting" autocomplete="off" placeholder="sales" required aria-required="true" aria-describedby="table-catalog-hint table-form-error" :aria-invalid="!!formError" />
              <span id="table-catalog-hint" class="field-hint">The Databricks catalog containing the table.</span>
            </label>
            <label class="field" for="table-schema">
              <span class="field-label">Schema</span>
              <input id="table-schema" class="k-input" v-model="form.schema" :disabled="loading || submitting" autocomplete="off" placeholder="gold" required aria-required="true" aria-describedby="table-schema-hint table-form-error" :aria-invalid="!!formError" />
              <span id="table-schema-hint" class="field-hint">The Databricks schema containing the table.</span>
            </label>
            <label class="field" for="table-table">
              <span class="field-label">Table</span>
              <input id="table-table" class="k-input" v-model="form.table" :disabled="loading || submitting" autocomplete="off" placeholder="order_history" required aria-required="true" aria-describedby="table-table-hint table-form-error" :aria-invalid="!!formError" />
              <span id="table-table-hint" class="field-hint">The exact table identifier in the selected catalog and schema.</span>
            </label>
          </div>
        </div>
        <ManualCreateGuidance
          kind="table"
          :editing="editing"
          :values="{ name: form.name, connectionRef: form.connectionRef, warehouseRef: form.warehouseRef, catalog: form.catalog, schema: form.schema, table: form.table }"
        />
      </div>
      <div class="k-create-actions">
        <span v-if="formError" id="table-form-error" ref="formErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ formError }}</span>
        <button class="k-btn k-btn--ghost" type="button" :disabled="submitting" @click="cancel">Cancel</button>
        <button class="k-btn k-btn--primary" type="submit" :disabled="loading || submitting || !!tableImportBlocker">{{ submitting ? (editing ? 'Saving…' : 'Registering…') : (editing ? 'Save changes' : 'Register table') }}</button>
      </div>
    </form>
  </section>
</template>
