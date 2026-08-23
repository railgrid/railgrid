<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { ChevronRight, LoaderCircle } from 'lucide-vue-next'
import { isLeaf, treeCheckState, type RegistrationTreeState } from './registrationTree'

defineOptions({ name: 'LazyCheckboxTree' })

const props = withDefaults(defineProps<{
  tree: RegistrationTreeState
  parentId?: string
  label?: string
  rootLoading?: boolean
  rootError?: string
  rootNextPageToken?: string
  busy?: boolean
  activeId?: string
}>(), { label: 'Databricks resources', rootLoading: false, rootError: '', rootNextPageToken: '', busy: false })

const emit = defineEmits<{
  (event: 'expand', id: string): void
  (event: 'toggle', id: string, checked: boolean): void
  (event: 'load-more', parentId?: string): void
  (event: 'active', id: string): void
}>()

const isRoot = computed(() => props.parentId === undefined)
const ids = computed(() => props.parentId === undefined ? props.tree.roots : props.tree.nodes[props.parentId]?.childIds ?? [])
const parentNode = computed(() => props.parentId ? props.tree.nodes[props.parentId] : undefined)
const selected = computed(() => new Set(props.tree.selectedLeafIds))
const treeRoot = ref<HTMLElement | null>(null)
const localActiveId = ref('')
const effectiveActiveId = computed(() => props.activeId || localActiveId.value || (isRoot.value ? props.tree.roots[0] || (props.rootError ? 'action:root-retry' : props.rootNextPageToken ? 'action:root-more' : '') : ''))

function checked(id: string): boolean { return isLeaf(props.tree.nodes[id]) ? selected.value.has(id) : treeCheckState(props.tree, id) === 'true' }
function mixed(id: string): boolean { return !isLeaf(props.tree.nodes[id]) && treeCheckState(props.tree, id) === 'mixed' }

function activate(id: string): void {
  const node = props.tree.nodes[id]
  if (!node || node.disabled || node.loading) return
  emit('toggle', id, !checked(id))
}

function checkboxChanged(id: string, event: Event): void {
  emit('toggle', id, (event.currentTarget as HTMLInputElement).checked)
}

function expand(id: string): void {
  const node = props.tree.nodes[id]
  if (!node || isLeaf(node) || node.disabled || node.loading) return
  focusKey(id)
  emit('expand', id)
}

function setActive(id: string): void {
  if (isRoot.value) localActiveId.value = id
  else emit('active', id)
}

function focusKey(key: string): void {
  setActive(key)
  void nextTick(() => document.querySelector<HTMLElement>(`[data-tree-focus="${CSS.escape(key)}"]`)?.focus())
}

function runAction(key: string, focusAfter: string, action: () => void): void {
  localActiveId.value = key
  focusKey(focusAfter)
  action()
}

function keydown(event: KeyboardEvent): void {
  if (!isRoot.value) return
  const target = event.target instanceof Element ? event.target.closest<HTMLElement>('[data-tree-focus]') : null
  const key = target?.dataset.treeFocus
  if (!key) return
  const visible = [...(treeRoot.value?.querySelectorAll<HTMLElement>('[data-tree-focus]') ?? [])]
    .filter(item => !item.hasAttribute('disabled') && item.getAttribute('aria-disabled') !== 'true')
  const index = visible.indexOf(target!)
  if (event.key === 'ArrowDown' && index < visible.length - 1) { event.preventDefault(); focusKey(visible[index + 1].dataset.treeFocus!); return }
  if (event.key === 'ArrowUp' && index > 0) { event.preventDefault(); focusKey(visible[index - 1].dataset.treeFocus!); return }
  if (event.key === 'Home' && visible.length) { event.preventDefault(); focusKey(visible[0].dataset.treeFocus!); return }
  if (event.key === 'End' && visible.length) { event.preventDefault(); focusKey(visible.at(-1)!.dataset.treeFocus!); return }
  if ((event.key === 'ArrowDown' || event.key === 'ArrowUp') && index >= 0) { event.preventDefault(); return }
  else if (!target?.dataset.treeId) return
  const id = target?.dataset.treeId
  if (!id) return
  const node = props.tree.nodes[id]
  if (!node) return
  if (event.key === 'ArrowRight') {
    if (!isLeaf(node) && !node.expanded) expand(id)
    else if (!isLeaf(node) && node.childIds.length) focusKey(node.childIds[0])
  } else if (event.key === 'ArrowLeft') {
    if (!isLeaf(node) && node.expanded) expand(id)
    else if (node.parentId) focusKey(node.parentId)
  } else if (event.key === ' ' || event.key === 'Enter') activate(id)
  else return
  event.preventDefault()
}

watch([() => props.rootLoading, () => ids.value.length, () => props.rootError], ([loading, count, rootFailure]) => {
  if (!isRoot.value || loading || localActiveId.value !== 'action:root-retry') return
  if (count) focusKey(props.tree.roots[0])
  else if (rootFailure) focusKey('action:root-retry')
})
</script>

<template>
  <div
    ref="treeRoot"
    :role="isRoot ? 'tree' : 'group'"
    :aria-label="isRoot ? label : undefined"
    :aria-multiselectable="isRoot ? 'true' : undefined"
    :aria-busy="isRoot && (rootLoading || busy) ? 'true' : undefined"
    :class="isRoot ? 'lazy-tree' : 'lazy-tree-group'"
    @keydown="keydown"
  >
    <template v-for="id in ids" :key="id">
      <div
        v-if="tree.nodes[id]"
        class="lazy-tree-item"
        role="treeitem"
        :data-tree-id="id"
        :data-tree-focus="id"
        :tabindex="id === effectiveActiveId ? 0 : -1"
        :aria-level="tree.nodes[id].depth"
        :aria-expanded="isLeaf(tree.nodes[id]) ? undefined : tree.nodes[id].expanded"
        :aria-checked="mixed(id) ? 'mixed' : checked(id)"
        :aria-disabled="tree.nodes[id].disabled ? 'true' : undefined"
        @focus="setActive(id)"
      >
        <div :class="['lazy-tree-row', { disabled: tree.nodes[id].disabled }]" @dblclick="expand(id)">
          <button v-if="!isLeaf(tree.nodes[id])" class="lazy-tree-expander" type="button" tabindex="-1" :aria-label="tree.nodes[id].expanded ? `Collapse ${tree.nodes[id].label}` : `Expand ${tree.nodes[id].label}`" :disabled="tree.nodes[id].disabled || tree.nodes[id].loading" @click.stop="expand(id)"><LoaderCircle v-if="tree.nodes[id].loading" class="spin" :stroke-width="1.75" /><ChevronRight v-else :class="{ expanded: tree.nodes[id].expanded }" :stroke-width="1.75" /></button>
          <span v-else class="lazy-tree-expander-spacer" />
          <input class="k-checkbox" type="checkbox" tabindex="-1" aria-hidden="true" :checked="checked(id)" :indeterminate="mixed(id)" :disabled="tree.nodes[id].disabled || tree.nodes[id].loading || busy" @mousedown.prevent @click.stop="focusKey(id)" @change="checkboxChanged(id, $event)" />
          <span class="lazy-tree-copy"><strong>{{ tree.nodes[id].label }}</strong><small v-if="tree.nodes[id].disabledReason">{{ tree.nodes[id].disabledReason }}</small><small v-else-if="tree.nodes[id].detail">{{ tree.nodes[id].detail }}</small></span>
        </div>
        <LazyCheckboxTree v-if="!isLeaf(tree.nodes[id]) && tree.nodes[id].expanded" :tree="tree" :parent-id="id" :busy="busy" :active-id="effectiveActiveId" @active="isRoot ? localActiveId = $event : emit('active', $event)" @expand="emit('expand', $event)" @toggle="(nodeId, value) => emit('toggle', nodeId, value)" @load-more="emit('load-more', $event)" />
      </div>
    </template>
    <p v-if="!isRoot && parentNode?.error" class="lazy-tree-error" role="alert">{{ parentNode.error }}</p>
    <button v-if="!isRoot && parentNode?.error" class="lazy-tree-more secondary" role="treeitem" type="button" :data-tree-focus="`action:retry:${parentId}`" :tabindex="effectiveActiveId === `action:retry:${parentId}` ? 0 : -1" :aria-level="(parentNode.depth || 0) + 1" :disabled="parentNode.loading || busy" @focus="setActive(`action:retry:${parentId}`)" @click="runAction(`action:retry:${parentId}`, parentId!, () => emit('expand', parentId!))">Retry {{ parentNode.label }}</button>
    <button v-if="!isRoot && parentNode?.nextPageToken" class="lazy-tree-more secondary" role="treeitem" type="button" :data-tree-focus="`action:more:${parentId}`" :tabindex="effectiveActiveId === `action:more:${parentId}` ? 0 : -1" :aria-level="(parentNode.depth || 0) + 1" :disabled="parentNode.loading || busy" @focus="setActive(`action:more:${parentId}`)" @click="runAction(`action:more:${parentId}`, parentId!, () => emit('load-more', parentId))">Load more in {{ parentNode.label }}</button>
    <p v-if="isRoot && rootError" class="lazy-tree-error" role="alert">{{ rootError }}</p>
    <button v-if="isRoot && rootError" class="secondary lazy-tree-root-more" role="treeitem" type="button" data-tree-focus="action:root-retry" :tabindex="effectiveActiveId === 'action:root-retry' ? 0 : -1" aria-level="1" :disabled="rootLoading || busy" @focus="setActive('action:root-retry')" @click="runAction('action:root-retry', tree.roots[0] || 'action:root-retry', () => emit('load-more', undefined))">Retry</button>
    <p v-if="isRoot && rootLoading" class="import-loading" role="status"><LoaderCircle class="spin" :stroke-width="1.75" /> Loading resources…</p>
    <button v-if="isRoot && rootNextPageToken" class="secondary lazy-tree-root-more" role="treeitem" type="button" data-tree-focus="action:root-more" :tabindex="effectiveActiveId === 'action:root-more' ? 0 : -1" aria-level="1" :disabled="rootLoading || busy" @focus="setActive('action:root-more')" @click="runAction('action:root-more', tree.roots.at(-1) || 'action:root-more', () => emit('load-more', undefined))">Load more</button>
    <p v-if="isRoot && !ids.length && !rootLoading && !rootError" class="empty">No resources returned.</p>
  </div>
</template>
