<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Pencil, Plus } from 'lucide-vue-next'

import ResourcePage from '@/portalkit/ResourcePage.vue'
import ResourceBackLink from '@/portalkit/ResourceBackLink.vue'
import ResourceSectionCard from '@/portalkit/ResourceSectionCard.vue'
import ResourceTable from '@/portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '@/portalkit/ResourceTableDeleteButton.vue'
import ActionMenu from '@/portalkit/ActionMenu.vue'
import StatusBadge from '@/portalkit/StatusBadge.vue'
import { confirmDialog } from '@/portalkit/confirm'
import { useAdminStore, type AdminWorkspaceSeed } from '@/stores/admin'
import { changeTableQuery, tableState, type AdminTableChange } from './admin-relations'
import MeteringDialog from './MeteringDialog.vue'
import { seedExportPaths, seedNameError, seedStatus, starterSeedSpec, systemWorkspaceAt } from './platform-services'

// A workspace seed says what every tenant workspace gets without anyone
// enabling it: APIExports the hub binds there and objects it creates. The spec
// is edited whole, as YAML, because that is what it is: bindings with their
// permission claims and literal manifests. The hub validates on save and
// stores nothing it would refuse.
const admin = useAdminStore()
const route = useRoute()
const router = useRouter()

const columns = [
  { key: 'name', label: 'Name', primary: true },
  { key: 'status', label: 'Status' },
  { key: 'bindingCount', label: 'Bindings' },
  { key: 'objectCount', label: 'Objects' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]
const bindingColumns = [
  { key: 'name', label: 'Binding', primary: true },
  { key: 'exportName', label: 'APIExport' },
  { key: 'exportPath', label: 'Export workspace' },
  { key: 'tenantAccess', label: 'Tenant access' },
  { key: 'claims', label: 'Claims' },
]
const objectColumns = [
  { key: 'name', label: 'Object', primary: true },
  { key: 'kind', label: 'Kind' },
  { key: 'namespace', label: 'Namespace' },
  { key: 'gated', label: 'Created' },
]

const rows = computed<Record<string, unknown>[]>(() => admin.seeds.map(seed => ({
  ...seed,
  status: seedStatus(seed).label,
  bindingCount: seed.bindings.length,
  objectCount: seed.objects.length,
  exportPaths: seedExportPaths(seed).join(' '),
})))
const state = computed(() => tableState(route.query, 'seeds'))
function changeTable(change: AdminTableChange) { void router.replace({ query: changeTableQuery(route.query, 'seeds', change) }) }

const selectedId = computed(() => typeof route.query.seed === 'string' ? route.query.seed : '')
const selected = computed<AdminWorkspaceSeed | undefined>(() => admin.seeds.find(seed => seed.name === selectedId.value))
const selectedBindings = computed<Record<string, unknown>[]>(() => (selected.value?.bindings ?? []).map(b => ({ ...b })))
const selectedObjects = computed<Record<string, unknown>[]>(() => (selected.value?.objects ?? []).map((o, index) => ({ ...o, key: `${index}` })))
const listLocation = computed(() => {
  const query = { ...route.query }
  delete query.seed
  delete query.newSeed
  delete query.exportPath
  return { path: route.path, query }
})
function seedLocation(row: Record<string, unknown>) {
  return { path: route.path, query: { ...route.query, seed: String(row.name) } }
}
function serviceLocation(path: string) {
  const service = systemWorkspaceAt(path, admin.systemWorkspaces)
  return service ? { path: '/bonkers/system-services', query: { service: service.name } } : null
}

const detail = ref<HTMLElement | null>(null)
let listTrigger: HTMLElement | null = null
function openSeed(row: Record<string, unknown>) {
  listTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  void router.push(seedLocation(row))
}
function closeSeed() { void router.push(listLocation.value) }
watch([selectedId, () => admin.loaded], async ([name]) => {
  await nextTick()
  if (name) {
    const heading = detail.value?.querySelector<HTMLElement>('.k-resource-page__title')
    heading?.setAttribute('tabindex', '-1')
    heading?.focus()
  } else if (listTrigger?.isConnected) listTrigger.focus()
}, { immediate: true })

const readError = computed(() => admin.error ? 'Couldn’t refresh workspace seeds. Try again.' : null)
const detailError = computed(() => readError.value || (admin.loaded && !selected.value
  ? 'This workspace seed is no longer available. Return to Workspace seeds to choose another.' : null))

const busy = ref(false)
const deletingName = ref<string | null>(null)
const actionError = ref<string | null>(null)
const notice = ref('')

// The editor. `original` is what the fields held when it opened, so an
// untouched editor closes without asking.
const editor = ref<{ create: boolean; name: string; spec: string; original: string } | null>(null)
const editorError = ref<string | null>(null)
const saving = ref(false)
const editorNameError = computed(() => editor.value?.create ? seedNameError(editor.value.name.trim()) : '')
const editorDirty = computed(() => !!editor.value && `${editor.value.name}\n${editor.value.spec}` !== editor.value.original)
const editorTitle = computed(() => editor.value?.create ? 'New workspace seed' : `Edit seed “${editor.value?.name ?? ''}”`)

function openEditor(create: boolean, name: string, spec: string) {
  editorError.value = null
  editor.value = { create, name, spec, original: `${name}\n${spec}` }
}
function newSeed(name = '', exportPath = '') { openEditor(true, name, starterSeedSpec(exportPath)) }
function editSeed(seed: AdminWorkspaceSeed) { openEditor(false, seed.name, seed.spec) }
function closeEditor() {
  editor.value = null
  // A link from a system service opens the editor through the query; drop it
  // so a reload does not reopen an editor the admin closed.
  if (route.query.newSeed !== undefined || route.query.exportPath !== undefined) {
    const query = { ...route.query }
    delete query.newSeed
    delete query.exportPath
    void router.replace({ path: route.path, query })
  }
}
watch(() => [route.query.newSeed, route.query.exportPath], ([name, exportPath]) => {
  if (typeof name !== 'string' || editor.value) return
  newSeed(name, typeof exportPath === 'string' ? exportPath : '')
}, { immediate: true })

async function save() {
  const current = editor.value
  if (!current || saving.value || editorNameError.value) return
  const name = current.name.trim()
  if (!name) return
  saving.value = true
  editorError.value = null
  try {
    await admin.saveSeed(name, current.spec, current.create)
    notice.value = current.create
      ? `Seed “${name}” created. The hub applies it to every tenant workspace once it is accepted.`
      : `Seed “${name}” saved. The hub brings every tenant workspace to the new spec once it is accepted.`
    editor.value = null
    await admin.refresh()
    await router.replace({ path: route.path, query: { ...listLocation.value.query, seed: name } })
  } catch (e) {
    // Stays open with the hub's message: a refused spec is fixed in place.
    if ((e as Error).message !== 'forbidden') editorError.value = (e as Error).message
    else editor.value = null
  } finally {
    saving.value = false
  }
}

async function refresh() { await admin.refresh() }

async function remove(name: string) {
  if (busy.value) return
  const confirmed = await confirmDialog({
    title: `Delete workspace seed "${name}"?`,
    message: 'The hub stops applying it to workspaces. Bindings and objects it already created in tenant workspaces stay; removing those is done per workspace.',
    danger: true,
    confirmLabel: 'Delete',
  })
  if (!confirmed) return
  busy.value = true
  deletingName.value = name
  actionError.value = null
  try {
    await admin.deleteSeed(name)
    if (selectedId.value === name) await router.replace(listLocation.value)
    await admin.refresh()
  } catch (e) {
    if ((e as Error).message !== 'forbidden') actionError.value = (e as Error).message
  } finally {
    deletingName.value = null
    busy.value = false
  }
}

function accessTone(access: unknown): 'success' | 'warning' | 'muted' {
  return access === 'ReadWrite' ? 'warning' : access === 'ReadOnly' ? 'success' : 'muted'
}
</script>

<template>
  <section>
    <details v-if="admin.errorDetails" class="mb-4 text-xs text-text-muted"><summary class="cursor-pointer">Advanced error details</summary><pre class="mt-2 whitespace-pre-wrap break-all">{{ admin.errorDetails }}</pre></details>
    <p v-if="notice" class="mb-4 text-sm text-text-secondary" role="status">{{ notice }}</p>
    <div v-show="!selectedId">
      <h2 class="mb-1 text-base font-semibold text-text-primary">Workspace seeds</h2>
      <p class="mb-4 max-w-3xl text-sm text-text-muted">
        What every tenant workspace gets without anyone enabling it: APIExports the hub binds there and objects it creates.
        A seed applies to all workspaces, existing and new, with no tenant consent. Deleting a seed removes nothing it created.
      </p>
      <div class="mb-4">
        <button type="button" class="k-btn k-btn--primary px-3 py-1.5 text-sm" :disabled="busy" @click="newSeed()">
          <Plus class="h-4 w-4" :stroke-width="1.75" />New workspace seed
        </button>
      </div>
      <p v-if="actionError" class="mb-2 text-sm text-danger" role="alert" aria-live="assertive">{{ actionError }}</p>

      <ResourceTable v-bind="state" @change="changeTable"
        :columns="columns" :rows="rows" aria-label="Workspace seeds" row-key="name"
        :interactive="true" :row-aria-label="(row: Record<string, unknown>) => `View workspace seed ${row.name}`" @row-click="openSeed"
        searchable search-placeholder="Search workspace seeds…" :search-keys="['name', 'status', 'exportPaths', 'message']"
        paginated :loaded="admin.loaded" :loading="admin.loading" :error="readError" :stale="admin.loaded && !!admin.error" retryable
        empty-text="No workspace seeds. Tenant workspaces get only what their members enable." @retry="refresh">
        <template #name="{ row }">
          <router-link class="k-table-resource-link font-mono" :to="seedLocation(row)" @click.stop="listTrigger = $event.currentTarget as HTMLElement" @keydown.space.prevent.stop="openSeed(row)">{{ row.name }}</router-link>
        </template>
        <template #status="{ row }"><StatusBadge :status="seedStatus(row).label" :tone="seedStatus(row).tone" /></template>
        <template #actions="{ row }">
          <div class="flex items-center justify-end gap-2">
            <ResourceTableDeleteButton :label="`Delete workspace seed ${row.name}`" :busy-label="`Deleting workspace seed ${row.name}…`"
              :busy="deletingName === row.name" :disabled="busy" @click="remove(String(row.name))" />
          </div>
        </template>
      </ResourceTable>
    </div>

    <div v-if="selectedId" ref="detail" class="space-y-4">
      <ResourceBackLink :href="router.resolve(listLocation).href" aria-label="Back to workspace seeds" @back="closeSeed">Workspace seeds</ResourceBackLink>
      <ResourcePage :title="selectedId" kind="Workspace seed" compact-actions
        :loaded="!!selected" :loading="admin.loading" :error="detailError" :stale="!!selected && !!detailError" retryable @retry="refresh">
        <template v-if="selected" #status><StatusBadge :status="seedStatus(selected).label" :tone="seedStatus(selected).tone" /></template>
        <template v-if="selected" #meta><span>Applies to every tenant workspace</span></template>
        <template v-if="selected" #actions>
          <button type="button" class="k-btn k-btn--ghost inline-flex items-center gap-1.5" :disabled="busy || admin.loading" @click="editSeed(selected)">
            <Pencil class="h-4 w-4" aria-hidden="true" />Edit spec
          </button>
          <ActionMenu :label="`Actions for workspace seed ${selected.name}`" :busy="deletingName === selectedId" :busy-label="`Deleting workspace seed ${selectedId}…`"
            :disabled="busy || admin.loading || !!detailError" :items="[{ id: 'delete', label: 'Delete workspace seed', tone: 'danger' }]"
            @select="id => id === 'delete' && selected && remove(selected.name)" />
        </template>
        <template v-if="selected">
          <p v-if="actionError" class="text-sm text-danger" role="alert">{{ actionError }}</p>
          <div class="space-y-4">
            <ResourceSectionCard id="seed-status" title="Status" description="Whether the hub applies this seed. It does not report how many workspaces have been reached; a workspace that fails is in the hub’s log.">
              <p v-if="selected.accepted === 'False'" class="text-sm text-danger" role="status">Refused, and applied nowhere: {{ selected.message }}</p>
              <p v-else-if="selected.accepted === 'True'" class="text-sm text-text-secondary">{{ selected.message || 'Accepted. The hub applies it to every tenant workspace.' }}</p>
              <p v-else class="text-sm text-text-secondary">The hub has not looked at this version of the seed yet. Refresh in a moment.</p>
            </ResourceSectionCard>

            <ResourceSectionCard id="seed-bindings" title="Bindings" description="APIExports bound into every tenant workspace, with the permission claims listed in the spec accepted.">
              <ResourceTable :columns="bindingColumns" :rows="selectedBindings" aria-label="Seed bindings" row-key="name" :interactive="false" :loaded="true" empty-text="This seed binds nothing.">
                <template #name="{ value }"><span class="font-mono">{{ value }}</span></template>
                <template #exportName="{ value }"><span class="font-mono text-xs">{{ value }}</span></template>
                <template #exportPath="{ value }">
                  <router-link v-if="serviceLocation(String(value))" class="k-table-resource-link font-mono text-xs" :to="serviceLocation(String(value))!">{{ value }}</router-link>
                  <span v-else class="font-mono text-xs text-text-muted">{{ value }}</span>
                </template>
                <template #tenantAccess="{ value }"><StatusBadge :status="String(value)" :tone="accessTone(value)" /></template>
              </ResourceTable>
            </ResourceSectionCard>

            <ResourceSectionCard id="seed-objects" title="Objects" description="Created in every tenant workspace once the seed’s bindings are bound there.">
              <ResourceTable :columns="objectColumns" :rows="selectedObjects" aria-label="Seed objects" row-key="key" :interactive="false" :loaded="true" empty-text="This seed creates no objects.">
                <template #name="{ value }"><span class="font-mono">{{ value || '—' }}</span></template>
                <template #kind="{ row }"><span>{{ row.kind || row.resource }}</span><span class="block font-mono text-xs text-text-muted">{{ row.apiVersion }}</span></template>
                <template #namespace="{ value }"><span class="font-mono text-xs text-text-muted">{{ value || 'cluster-scoped' }}</span></template>
                <template #gated="{ value }"><span class="text-text-secondary">{{ value ? 'Where its provider is enabled' : 'Everywhere' }}</span></template>
              </ResourceTable>
            </ResourceSectionCard>

            <ResourceSectionCard id="seed-spec" title="Spec">
              <pre class="max-h-[32rem] overflow-auto rounded border border-border-default bg-surface-overlay p-3 font-mono text-xs text-text-primary">{{ selected.spec }}</pre>
            </ResourceSectionCard>
          </div>
        </template>
      </ResourcePage>
    </div>

    <MeteringDialog v-if="editor" :title="editorTitle" :busy="saving" :dirty="editorDirty" @close="closeEditor">
      <template #context><p class="mt-1 text-sm text-text-muted">Saving applies this to every tenant workspace, existing and new.</p></template>
      <form id="admin-seed-editor" class="space-y-4" @submit.prevent="save">
        <div v-if="editor.create">
          <label for="admin-seed-name" class="block text-[11px] text-text-muted">Name</label>
          <input id="admin-seed-name" v-model="editor.name" data-initial-focus placeholder="e.g. switchboard" class="k-input mt-1 w-64 font-mono text-sm"
            required :aria-invalid="!!editorNameError" :aria-describedby="editorNameError ? 'admin-seed-name-error' : undefined" />
          <p v-if="editorNameError" id="admin-seed-name-error" class="mt-1 text-sm text-danger">{{ editorNameError }}</p>
        </div>
        <div>
          <label for="admin-seed-spec" class="block text-[11px] text-text-muted">Spec (YAML)</label>
          <textarea id="admin-seed-spec" v-model="editor.spec" :data-initial-focus="editor.create ? undefined : ''" rows="22" spellcheck="false" autocapitalize="off" autocomplete="off"
            class="k-input mt-1 font-mono text-xs leading-5" required aria-describedby="admin-seed-spec-help" />
          <p id="admin-seed-spec-help" class="mt-1 text-xs text-text-muted">
            Permission claims must match what the export declares. Manifests are literal: the same object goes into every workspace. Nothing is removed when a binding or object is taken out.
          </p>
        </div>
        <p v-if="editorError" class="whitespace-pre-wrap text-sm text-danger" role="alert" aria-live="assertive">{{ editorError }}</p>
      </form>
      <template #submit>
        <button type="submit" form="admin-seed-editor" class="k-btn k-btn--primary" :disabled="saving || !editor.name.trim() || !editor.spec.trim() || !!editorNameError || (!editor.create && !editorDirty)">
          {{ saving ? 'Saving…' : editor.create ? 'Create and apply to all workspaces' : 'Save and apply to all workspaces' }}
        </button>
      </template>
    </MeteringDialog>
  </section>
</template>
