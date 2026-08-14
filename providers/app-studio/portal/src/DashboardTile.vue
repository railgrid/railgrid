<script setup lang="ts">
// Dashboard tile for App Studio, mounted by
// <faros-dashboard-tile-app-studio> (see element.ts).
//
// App Studio's tile answers "where was I" more than "what exists", so it
// orders by updatedAt and shows each project's two runtime states side by
// side: the development preview and production. Those are the two questions a
// user actually has about a project — is my preview up, and is the promoted
// version live — and they live in different environments, so the tile reads
// them off the environment list rather than the project phase.

import { computed, h, onMounted, onUnmounted, ref, watch } from 'vue'
import { api } from './api'
import type { FarosContext, Project, ProjectEnvironment } from './types'
import {
  createTilePoller,
  hasWorkspaceContext,
  isBenignTileError,
  mostRecent,
  navigateFromTile,
  tileClass,
  tileErrorText,
  type TileContext,
  type TilePoller,
} from './portalkit/dashboardtile'
import { ic } from './portalkit/icons'

// Inline chevron — provider bundles are self-contained (no shared icon lib),
// the same reason the infrastructure tile inlines its own.
const ChevronRight = (props: { class?: string }) =>
  h(
    'svg',
    {
      xmlns: 'http://www.w3.org/2000/svg',
      viewBox: '0 0 24 24',
      fill: 'none',
      stroke: 'currentColor',
      'stroke-width': 2,
      'stroke-linecap': 'round',
      'stroke-linejoin': 'round',
      class: props.class,
    },
    [h('path', { d: 'm9 18 6-6-6-6' })],
  )

const props = defineProps<{ context: TileContext | null }>()

const rootRef = ref<HTMLElement | null>(null)
const projects = ref<Project[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
let poller: TilePoller | null = null

function environment(project: Project, name: string): ProjectEnvironment | undefined {
  return (project.environments ?? []).find((env) => env.name === name)
}

// A binding phase of Ready is the only state that means "you can open it".
// Anything else — provisioning, failed, or an environment that was never
// bound — reads as not ready, because from the dashboard they are the same
// action: go look at the project.
function environmentReady(project: Project, name: string): boolean {
  const env = environment(project, name)
  if (!env) return false
  const bindings = env.bindings ?? []
  if (bindings.length === 0) return (env.phase ?? '') === 'Ready'
  return bindings.some((b) => (b.phase ?? '') === 'Ready')
}

const stats = computed(() => {
  const total = projects.value.length
  const previewReady = projects.value.filter((p) => environmentReady(p, 'development')).length
  const productionReady = projects.value.filter((p) => environmentReady(p, 'production')).length
  return { total, previewReady, productionReady }
})

const rows = computed(() =>
  mostRecent(projects.value, (p) => p.updatedAt || p.createdAt).map((project) => ({
    project,
    preview: environmentReady(project, 'development'),
    production: !!environment(project, 'production') && environmentReady(project, 'production'),
    promoted: !!environment(project, 'production'),
  })),
)

async function load() {
  const ctx = props.context
  if (!hasWorkspaceContext(ctx)) {
    projects.value = []
    error.value = null
    loading.value = false
    return
  }
  try {
    // The App Studio client takes the context per call rather than through
    // module-level setters, so the tile passes its own — no shared mutable
    // state with the full provider app when both are mounted.
    projects.value = await api.listProjects(ctx as FarosContext)
    error.value = null
  } catch (e) {
    projects.value = []
    error.value = isBenignTileError(e) ? null : tileErrorText(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  poller = createTilePoller(load)
  poller.start()
})
onUnmounted(() => poller?.stop())
watch(() => props.context, () => poller?.refresh())
</script>

<template>
  <div ref="rootRef" :class="tileClass.root">
    <div v-if="loading" :class="tileClass.message">Loading projects&hellip;</div>
    <div v-else-if="error" :class="tileClass.error">Failed to load: {{ error }}</div>

    <template v-else>
      <div :class="tileClass.stats">
        <span :class="[tileClass.stat, tileClass.statTotal]">
          <span v-html="ic('package', tileClass.statIcon)" />
          <span :class="tileClass.statNum">{{ stats.total }}</span>
          <span :class="tileClass.statLabel">{{ stats.total === 1 ? 'project' : 'projects' }}</span>
        </span>
        <span :class="[tileClass.stat, tileClass.statMuted]">
          <span v-html="ic('eye', tileClass.statIcon)" />
          <span class="tabular-nums">{{ stats.previewReady }}</span>
          <span>preview up</span>
        </span>
        <span v-if="stats.productionReady > 0" :class="[tileClass.stat, tileClass.statOk]">
          <span v-html="ic('check', tileClass.statIcon)" />
          <span class="tabular-nums">{{ stats.productionReady }}</span>
          <span :class="tileClass.statLabel">in production</span>
        </span>
      </div>

      <div v-if="rows.length">
        <div :class="tileClass.sectionLabel">Recent</div>
        <ul :class="tileClass.list">
          <li v-for="row in rows" :key="row.project.name">
            <button
              type="button"
              :class="tileClass.row"
              @click="navigateFromTile(rootRef, row.project.name)"
            >
              <!-- The dot is the development preview: every project has one,
                   so it is the only state that can be read the same way on
                   every row. Production is the exception (most projects are
                   never promoted) and stays a chip, shown only when it exists
                   — a project with no production has nothing to be red about. -->
              <span
                :class="[tileClass.rowDot, row.preview ? 'bg-success' : 'bg-text-muted']"
                aria-hidden="true"
              />
              <span :class="tileClass.rowPrimary">
                {{ row.project.displayName || row.project.name }}
              </span>
              <span
                v-if="row.promoted"
                class="shrink-0 rounded px-1 py-px text-[10px] uppercase tracking-wide"
                :class="row.production ? 'bg-success/15 text-success' : 'bg-warning/15 text-warning'"
              >prod</span>
              <ChevronRight :class="tileClass.chevron" />
            </button>
          </li>
        </ul>
      </div>

      <div v-else :class="tileClass.empty">No projects yet — create one to get started.</div>
    </template>
  </div>
</template>
