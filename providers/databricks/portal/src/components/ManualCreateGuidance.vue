<script setup lang="ts">
import { Info } from 'lucide-vue-next'
import { computed, useId } from 'vue'

type ManualCreateKind = 'connection' | 'warehouse' | 'table'

interface ManualCreateValues {
  name?: string
  host?: string
  tokenPresent?: boolean
  connectionRef?: string
  warehouseRef?: string
  warehouseID?: string
  catalog?: string
  schema?: string
  table?: string
}

interface LiveValue {
  label: string
  value: string
  technical?: boolean
}

interface GuidanceCopy {
  heading: string
  description: string
  prerequisites: string[]
  nextSteps: string[]
}

const props = withDefaults(defineProps<{
  kind: ManualCreateKind
  values?: ManualCreateValues
  editing?: boolean
}>(), {
  values: () => ({}),
  editing: false,
})

const guidanceCopy: Record<ManualCreateKind, GuidanceCopy> = {
  connection: {
    heading: 'Name and connect Databricks',
    description: 'Choose the name Faros will use for this connection, then provide the Databricks host and token.',
    prerequisites: [
      'A Faros-local connection name to use when registering warehouses and tables.',
      'An HTTPS Databricks workspace root URL from the browser address bar. Leave off any path.',
      'A Databricks personal access token from Settings > Developer > Access tokens.',
      'The token identity needs SELECT on the catalogs and schemas you plan to import, plus access to a running SQL warehouse.',
    ],
    nextSteps: [
      'Faros stores the Databricks token as a Secret in this Workspace; the token is not shown after submission.',
      'The connection controller checks the Databricks host and credential and reports the result.',
      'Use the Faros connection name when registering a warehouse, then a table.',
    ],
  },
  warehouse: {
    heading: 'Register the warehouse handle',
    description: 'Use the SQL warehouse handle, not the workspace identifier in the browser URL.',
    prerequisites: [
      'A Connection already registered in this Workspace.',
      'The 16-character hexadecimal warehouse ID from SQL Warehouses > Connection details > /sql/1.0/warehouses/<id>, not the numeric ?o= workspace ID in the browser URL.',
      'The token identity needs Can use permission, and the warehouse must be startable (serverless or auto-start) for queries.',
    ],
    nextSteps: [
      'Faros records the warehouse ID against the selected Connection; no warehouse credentials are copied.',
      'After submission, the warehouse controller validates the handle and observes its state.',
      'Ready is shown only when that controller reports it; this form does not pre-validate the warehouse.',
    ],
  },
  table: {
    heading: 'Register the table handle',
    description: 'Keep the Databricks locator exact so downstream tools can refer to one stable tableRef.',
    prerequisites: [
      'A Connection and a Warehouse already registered in this Workspace.',
      'A Warehouse that belongs to the same Connection selected for this table.',
      'The exact catalog.schema.table identifier used by Databricks.',
    ],
    nextSteps: [
      'Faros records metadata only: catalog, schema, table, and the selected references. It does not read table rows here.',
      'The table controller refreshes schema metadata after registration and reports status separately.',
      'App Studio can use the tableRef for design-time metadata and guidance; MCP table tools use the bound resource after authorization and readiness.',
    ],
  },
}

const id = useId().replace(/[^a-zA-Z0-9_-]/g, '-')
const titleID = `manual-create-guidance-${id}-title`
const prerequisiteID = `manual-create-guidance-${id}-prerequisites`
const nextStepsID = `manual-create-guidance-${id}-next-steps`
const valuesID = `manual-create-guidance-${id}-values`

const copy = computed<GuidanceCopy>(() => {
  if (props.kind !== 'table' || !props.editing) return guidanceCopy[props.kind]
  return {
    ...guidanceCopy.table,
    heading: 'Update the table handle',
    description: 'Keep the table locator exact while updating its metadata-only registration.',
    nextSteps: [
      'The table name remains the stable tableRef identity while these references and locators are updated.',
      'The table controller refreshes schema metadata after saving and reports status separately.',
      'App Studio guidance and MCP table tools continue to use the bound resource after authorization and readiness.',
    ],
  }
})

function present(value: string | undefined, fallback = 'Not entered yet'): string {
  const normalized = value?.trim()
  return normalized || fallback
}

const liveValues = computed<LiveValue[]>(() => {
  const values = props.values
  if (props.kind === 'connection') {
    return [
      { label: 'Faros name', value: present(values.name) },
      { label: 'Databricks host', value: present(values.host), technical: true },
      { label: 'Databricks token', value: values.tokenPresent ? 'Provided (stored as a Secret)' : 'Not entered yet' },
    ]
  }

  if (props.kind === 'warehouse') {
    return [
      { label: 'Name', value: present(values.name) },
      { label: 'Connection', value: present(values.connectionRef), technical: true },
      { label: 'Warehouse ID', value: present(values.warehouseID), technical: true },
    ]
  }

  const catalog = values.catalog?.trim() || ''
  const schema = values.schema?.trim() || ''
  const table = values.table?.trim() || ''
  const fullName = catalog && schema && table
    ? `${catalog}.${schema}.${table}`
    : 'Not entered yet (catalog.schema.table)'

  return [
    { label: 'TableRef name', value: present(values.name) },
    { label: 'Exact table', value: fullName, technical: true },
    { label: 'Connection', value: present(values.connectionRef), technical: true },
    { label: 'Warehouse', value: present(values.warehouseRef), technical: true },
  ]
})

const outputHeading = computed(() => props.editing ? 'What Faros will save' : 'What Faros will register')
</script>

<template>
  <aside class="manual-create-guidance" :aria-labelledby="titleID">
    <div class="manual-create-guidance__heading">
      <Info :size="16" :stroke-width="1.75" aria-hidden="true" />
      <h3 :id="titleID">{{ copy.heading }}</h3>
    </div>
    <p class="manual-create-guidance__description">{{ copy.description }}</p>

    <section class="manual-create-guidance__section" :aria-labelledby="prerequisiteID">
      <h4 :id="prerequisiteID">Prerequisites</h4>
      <ul>
        <li v-for="prerequisite in copy.prerequisites" :key="prerequisite">{{ prerequisite }}</li>
      </ul>
    </section>

    <section class="manual-create-guidance__section" :aria-labelledby="valuesID">
      <h4 :id="valuesID">{{ outputHeading }}</h4>
      <dl class="manual-create-guidance__values">
        <template v-for="item in liveValues" :key="item.label">
          <dt>{{ item.label }}</dt>
          <dd :class="{ 'manual-create-guidance__value--technical': item.technical }">
            <code v-if="item.technical">{{ item.value }}</code>
            <span v-else>{{ item.value }}</span>
          </dd>
        </template>
      </dl>
    </section>

    <section class="manual-create-guidance__section" :aria-labelledby="nextStepsID">
      <h4 :id="nextStepsID">Next steps</h4>
      <ol>
        <li v-for="step in copy.nextSteps" :key="step">{{ step }}</li>
      </ol>
    </section>
  </aside>
</template>
