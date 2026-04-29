<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { useGraphQLQuery, graphqlMutate } from '@/composables/useGraphQL'
import {
  GET_VIRTUAL_WORKLOAD,
  type GetVirtualWorkloadResult,
} from '@/graphql/queries/workloads'
import { UPDATE_VIRTUAL_WORKLOAD, DELETE_VIRTUAL_WORKLOAD } from '@/graphql/mutations'
import {
  ArrowLeft,
  Layers,
  Hash,
  Clock,
  Image,
  MapPin,
  Server,
  Globe,
  Minus,
  Plus,
  Trash2,
  AlertTriangle,
} from 'lucide-vue-next'

const props = defineProps<{ name: string; namespace: string }>()
const router = useRouter()

const { data, loading, error, refetch } = useGraphQLQuery<GetVirtualWorkloadResult>(
  GET_VIRTUAL_WORKLOAD,
  { name: props.name, namespace: props.namespace },
  10000,
)

const workload = computed(() => data.value?.kedge_faros_sh?.v1alpha1?.VirtualWorkload)

// --- Scale ---
const scaleInput = ref<number | null>(null)
const scaling = ref(false)
const scaleError = ref<string | null>(null)

const currentReplicas = computed(() => workload.value?.spec?.replicas ?? 0)

function initScale() {
  scaleInput.value = currentReplicas.value
}

async function doScale(replicas: number) {
  scaling.value = true
  scaleError.value = null
  try {
    await graphqlMutate(UPDATE_VIRTUAL_WORKLOAD, {
      name: props.name,
      namespace: props.namespace,
      object: { spec: { replicas } },
    })
    refetch()
  } catch (e) {
    scaleError.value = e instanceof Error ? e.message : 'Scale failed'
  } finally {
    scaling.value = false
  }
}

async function handleScale(delta: number) {
  const target = (scaleInput.value ?? currentReplicas.value) + delta
  if (target < 0) return
  scaleInput.value = target
  await doScale(target)
}

async function handleScaleSubmit() {
  if (scaleInput.value === null || scaleInput.value === currentReplicas.value) return
  await doScale(scaleInput.value)
}

// --- Delete ---
const showDeleteConfirm = ref(false)
const deleteDeleting = ref(false)
const deleteErr = ref<string | null>(null)

async function handleDelete() {
  deleteDeleting.value = true
  deleteErr.value = null
  try {
    await graphqlMutate(DELETE_VIRTUAL_WORKLOAD, {
      name: props.name,
      namespace: props.namespace,
    })
    router.push('/workloads')
  } catch (e) {
    deleteErr.value = e instanceof Error ? e.message : 'Delete failed'
  } finally {
    deleteDeleting.value = false
  }
}

const details = computed(() => {
  if (!workload.value) return []
  return [
    { label: 'Namespace', value: workload.value.metadata?.namespace, icon: Layers },
    { label: 'Replicas', value: `${workload.value.status?.readyReplicas ?? 0}/${workload.value.spec?.replicas ?? 0} ready`, icon: Layers },
    { label: 'Available', value: String(workload.value.status?.availableReplicas ?? 0), icon: Layers },
    { label: 'Strategy', value: workload.value.spec?.placement?.strategy, icon: MapPin },
    { label: 'Created', value: workload.value.metadata?.creationTimestamp, icon: Clock },
    { label: 'UID', value: workload.value.metadata?.uid, icon: Hash, mono: true },
  ].filter((d) => d.value)
})

const edgeSelectorLabels = computed(() => {
  const labels = workload.value?.spec?.placement?.edgeSelector?.matchLabels
  if (!labels) return null
  return Object.entries(labels).map(([k, v]) => `${k}=${v}`).join(', ')
})
</script>

<template>
  <AppLayout>
    <!-- Back link -->
    <router-link
      to="/workloads"
      class="stagger-item mb-5 inline-flex items-center gap-1.5 rounded-lg px-2 py-1 text-[12px] text-text-muted transition-all hover:bg-surface-hover hover:text-accent"
      style="animation-delay: 0ms"
    >
      <ArrowLeft class="h-3 w-3" :stroke-width="2" />
      Back to workloads
    </router-link>

    <div v-if="error" class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle p-4 text-[13px] text-danger">
      {{ error }}
    </div>

    <div v-else-if="loading && !data" class="mt-16 flex flex-col items-center justify-center gap-3">
      <div class="shimmer h-8 w-8 rounded-xl" />
      <div class="shimmer h-3 w-40 rounded" />
    </div>

    <template v-else-if="workload">
      <!-- Hero + Conditions grid -->
      <div class="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <!-- Hero (2 cols) -->
        <div
          class="border-beam stagger-item col-span-1 flex flex-col rounded-2xl border border-border-subtle bg-surface-raised/80 p-6 backdrop-blur lg:col-span-2"
          style="animation-delay: 40ms"
        >
          <div class="flex items-start gap-4">
            <div class="relative flex h-14 w-14 shrink-0 items-center justify-center">
              <div class="absolute inset-0 rounded-xl bg-accent/15 blur-md" />
              <div class="relative flex h-14 w-14 items-center justify-center rounded-xl border border-accent/20 bg-surface-overlay">
                <Layers class="h-7 w-7 text-accent" :stroke-width="1.5" />
              </div>
            </div>
            <div class="flex-1">
              <div class="flex items-center gap-3">
                <h1 class="text-gradient text-xl font-bold tracking-tight">{{ workload.metadata?.name }}</h1>
                <StatusBadge :status="workload.status?.phase" />
              </div>
              <div class="mt-1.5 flex items-center gap-4 text-[12px] text-text-muted">
                <span class="rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 font-mono text-[11px]">{{ workload.metadata?.namespace }}</span>
                <span class="font-mono text-[11px] text-text-muted/60">VirtualWorkload</span>
              </div>
            </div>
          </div>

          <!-- Details grid -->
          <dl class="mt-6 grid grid-cols-2 gap-x-8 gap-y-3 border-t border-border-subtle pt-5">
            <div
              v-for="item in details"
              :key="item.label"
              class="flex items-center justify-between"
            >
              <dt class="flex items-center gap-2 text-[12px] text-text-muted">
                <component :is="item.icon" class="h-3 w-3" :stroke-width="1.75" />
                {{ item.label }}
              </dt>
              <dd
                class="text-[12px] text-text-secondary"
                :class="{ 'font-mono text-[11px]': item.mono, 'max-w-[160px] truncate': item.mono }"
              >
                {{ item.value }}
              </dd>
            </div>
          </dl>

          <!-- Edge Selector -->
          <div v-if="edgeSelectorLabels" class="mt-4 border-t border-border-subtle pt-4">
            <span class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Edge Selector</span>
            <span class="ml-2 rounded-md border border-border-subtle bg-surface-overlay px-2 py-0.5 font-mono text-[11px] text-text-secondary">{{ edgeSelectorLabels }}</span>
          </div>

          <!-- Access -->
          <div v-if="workload.spec?.access?.expose" class="mt-3 border-t border-border-subtle pt-4">
            <div class="flex items-center gap-2">
              <Globe class="h-3 w-3 text-accent" :stroke-width="1.75" />
              <span class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Exposed</span>
              <span v-if="workload.spec.access.dnsName" class="font-mono text-[11px] text-accent">{{ workload.spec.access.dnsName }}</span>
              <span v-if="workload.spec.access.port" class="font-mono text-[11px] text-text-muted">:{{ workload.spec.access.port }}</span>
            </div>
          </div>
        </div>

        <!-- Conditions -->
        <div
          class="stagger-item rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
          style="animation-delay: 120ms"
        >
          <h2 class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Conditions</h2>
          <div v-if="workload.status?.conditions?.length" class="mt-4 space-y-2">
            <div
              v-for="cond in workload.status.conditions"
              :key="cond.type"
              class="rounded-xl border border-border-subtle bg-surface-overlay/60 p-3 transition-colors duration-150 hover:border-border-default"
            >
              <div class="flex items-center justify-between">
                <span class="text-[12px] font-medium text-text-primary">{{ cond.type }}</span>
                <StatusBadge :status="cond.status === 'True' ? 'Ready' : 'Pending'" />
              </div>
              <p v-if="cond.message" class="mt-1.5 text-[11px] leading-relaxed text-text-muted">{{ cond.message }}</p>
            </div>
          </div>
          <div v-else class="mt-8 flex flex-col items-center text-text-muted/30">
            <p class="text-[12px]">No conditions</p>
          </div>
        </div>
      </div>

      <!-- Actions row -->
      <div class="stagger-item mt-5 flex flex-wrap items-center gap-3" style="animation-delay: 160ms">
        <!-- Scale controls -->
        <div class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-raised/80 px-3 py-2 backdrop-blur">
          <span class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Scale</span>
          <button
            class="flex h-7 w-7 items-center justify-center rounded-lg border border-border-subtle text-text-muted transition-all hover:bg-surface-hover hover:text-text-primary disabled:opacity-40"
            :disabled="scaling"
            @click="handleScale(-1)"
          >
            <Minus class="h-3 w-3" :stroke-width="2" />
          </button>
          <input
            v-model.number="scaleInput"
            type="number"
            min="0"
            max="100"
            class="w-12 rounded-lg border border-border-subtle bg-surface-overlay px-2 py-1 text-center font-mono text-[12px] text-text-primary focus:border-accent/50 focus:outline-none"
            @focus="initScale"
            @keyup.enter="handleScaleSubmit"
          />
          <button
            class="flex h-7 w-7 items-center justify-center rounded-lg border border-border-subtle text-text-muted transition-all hover:bg-surface-hover hover:text-text-primary disabled:opacity-40"
            :disabled="scaling"
            @click="handleScale(1)"
          >
            <Plus class="h-3 w-3" :stroke-width="2" />
          </button>
          <button
            v-if="scaleInput !== null && scaleInput !== currentReplicas"
            class="rounded-lg bg-accent px-3 py-1 text-[11px] font-medium text-white transition-all hover:bg-accent-hover disabled:opacity-50"
            :disabled="scaling"
            @click="handleScaleSubmit"
          >
            {{ scaling ? 'Scaling...' : 'Apply' }}
          </button>
        </div>

        <!-- Delete -->
        <button
          class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle px-4 py-2 text-[12px] font-medium text-danger transition-all hover:bg-danger/20"
          @click="showDeleteConfirm = true"
        >
          <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
          Delete
        </button>
      </div>

      <!-- Errors -->
      <div v-if="scaleError" class="mt-3 rounded-lg border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger">
        {{ scaleError }}
      </div>

      <!-- Simple workload spec -->
      <div
        v-if="workload.spec?.simple"
        class="stagger-item mt-5 rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
        style="animation-delay: 200ms"
      >
        <h2 class="text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Workload Spec</h2>
        <div class="mt-4 rounded-xl border border-border-subtle bg-surface-overlay/60 p-4">
          <div class="space-y-3 text-[12px]">
            <div class="flex items-center gap-2 text-text-muted">
              <Image class="h-3 w-3" :stroke-width="1.75" />
              <span class="text-[11px]">Image:</span>
              <span class="font-mono text-[11px] text-text-secondary">{{ workload.spec.simple.image }}</span>
            </div>
            <div v-if="workload.spec.simple.ports?.length" class="flex items-center gap-2 text-text-muted">
              <span class="text-[11px]">Ports:</span>
              <span v-for="p in workload.spec.simple.ports" :key="p.containerPort" class="font-mono text-[11px] text-text-secondary">
                {{ p.containerPort }}/{{ p.protocol }}
              </span>
            </div>
            <div v-if="workload.spec.simple.command?.length" class="flex items-center gap-2 text-text-muted">
              <span class="text-[11px]">Command:</span>
              <span class="font-mono text-[11px] text-text-secondary">{{ workload.spec.simple.command.join(' ') }}</span>
            </div>
            <div v-if="workload.spec.simple.args?.length" class="flex items-center gap-2 text-text-muted">
              <span class="text-[11px]">Args:</span>
              <span class="font-mono text-[11px] text-text-secondary">{{ workload.spec.simple.args.join(' ') }}</span>
            </div>
          </div>
          <div v-if="workload.spec.simple.env?.length" class="mt-3 border-t border-border-subtle pt-3">
            <span class="text-[10px] font-semibold uppercase tracking-wider text-text-muted">Environment</span>
            <div class="mt-2 space-y-1">
              <div v-for="e in workload.spec.simple.env" :key="e.name" class="flex items-center gap-2 font-mono text-[11px]">
                <span class="text-text-secondary">{{ e.name }}</span>
                <span class="text-text-muted">=</span>
                <span class="text-text-muted">{{ e.value ?? '(ref)' }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Edge placements -->
      <div
        class="stagger-item mt-5 rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur"
        style="animation-delay: 240ms"
      >
        <div class="flex items-center justify-between">
          <h2 class="flex items-center gap-2 text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
            <Server class="h-3 w-3" :stroke-width="1.75" />
            Edge Placements
            <span class="rounded-full bg-surface-overlay px-1.5 py-0.5 font-mono text-[10px] text-text-muted">{{ workload.status?.edges?.length ?? 0 }}</span>
          </h2>
        </div>
        <div class="mt-4 space-y-1.5">
          <div
            v-for="edge in workload.status?.edges ?? []"
            :key="edge.edgeName"
            class="flex items-center justify-between rounded-xl border border-border-subtle bg-surface-overlay/50 px-4 py-2.5 transition-all duration-150 hover:bg-accent/[0.03]"
          >
            <div class="flex items-center gap-3">
              <Server class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
              <router-link
                :to="`/edges/${edge.edgeName}`"
                class="text-[13px] font-medium text-text-primary hover:text-accent transition-colors"
              >
                {{ edge.edgeName }}
              </router-link>
            </div>
            <div class="flex items-center gap-3">
              <span class="font-mono text-[10px] text-text-muted">
                {{ edge.readyReplicas }} ready
              </span>
              <StatusBadge :status="edge.phase ?? 'Unknown'" />
              <span v-if="edge.message" class="max-w-[200px] truncate text-[10px] text-text-muted" :title="edge.message">
                {{ edge.message }}
              </span>
            </div>
          </div>
          <div v-if="!workload.status?.edges?.length" class="py-8 text-center text-[12px] text-text-muted/40">
            No edge placements yet
          </div>
        </div>
      </div>
    </template>

    <!-- Delete confirmation -->
    <Teleport to="body">
      <div
        v-if="showDeleteConfirm"
        class="fixed inset-0 z-[100] flex items-center justify-center bg-black/50 backdrop-blur-sm"
        @click.self="showDeleteConfirm = false"
      >
        <div class="w-full max-w-md rounded-2xl border border-border-subtle bg-surface-raised p-6 shadow-2xl">
          <div class="flex items-center gap-3 mb-3">
            <div class="flex h-10 w-10 items-center justify-center rounded-xl bg-danger-subtle">
              <AlertTriangle class="h-5 w-5 text-danger" :stroke-width="1.75" />
            </div>
            <h3 class="text-[14px] font-bold text-text-primary">Delete Virtual Workload?</h3>
          </div>
          <p class="text-[12px] text-text-muted">
            This will permanently delete
            <span class="font-mono font-medium text-text-secondary">{{ name }}</span>
            and remove it from all edges. This action cannot be undone.
          </p>
          <div v-if="deleteErr" class="mt-3 rounded-lg border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger">
            {{ deleteErr }}
          </div>
          <div class="mt-5 flex items-center justify-end gap-3">
            <button
              class="rounded-lg border border-border-subtle px-4 py-2 text-[12px] font-medium text-text-secondary transition-all hover:bg-surface-hover"
              @click="showDeleteConfirm = false"
              :disabled="deleteDeleting"
            >
              Cancel
            </button>
            <button
              class="rounded-lg bg-danger px-4 py-2 text-[12px] font-medium text-white transition-all hover:bg-danger/80 disabled:opacity-50"
              @click="handleDelete"
              :disabled="deleteDeleting"
            >
              {{ deleteDeleting ? 'Deleting...' : 'Delete' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </AppLayout>
</template>
