<script setup lang="ts">
// The edge detail page's Harness card. It reads status.harnesses (what the
// machine reports) and writes spec.harness (what the user asks for) with an
// ordinary spec merge patch as the caller — no provider verb, no runner token.
// See docs/edge-harness.md.
//
// Three facts stay separate on every row: detected (the executable is on the
// machine), enabled (spec.harness asks for it) and ready (its runner answers
// runner/v1). "Installed but switched off" and "asked for but not installed"
// are different situations, so they read differently.
import { computed, ref, watch } from 'vue'
import { Bot, ChevronDown, ChevronUp, PowerOff } from 'lucide-vue-next'
import { updateEdgeHarness } from './api'
import ResourceSectionCard from './portalkit/ResourceSectionCard.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import {
  HARNESS_NAMES,
  HARNESS_PERMISSION_MODES,
  formatAllowedTools,
  harnessLabel,
  harnessPermissionMode,
  parseAllowedTools,
  type EdgeHarnessSpec,
  type HarnessPermissionMode,
  type EdgeType,
  type ErrorResponse,
  type HarnessMode,
  type HarnessStatus,
} from './types'

const props = withDefaults(defineProps<{
  edgeName: string
  edgeType: EdgeType
  // harness is spec.harness. ABSENT means the CRD default, which is auto, so a
  // pre-existing edge renders as "every installed harness" and not as empty.
  harness?: EdgeHarnessSpec | null
  // harnesses is status.harnesses, as last reported by the machine.
  harnesses?: HarnessStatus[] | null
  // disabled locks the controls while the page owns a blocking mutation.
  disabled?: boolean
}>(), { harness: null, harnesses: null, disabled: false })

// changed asks the page for a fresh snapshot: the spec write lands immediately,
// the status that settles the card arrives on a later poll.
const emit = defineEmits<{ changed: [] }>()

type Tone = 'success' | 'warning' | 'danger' | 'muted'

interface HarnessRow {
  name: string
  label: string
  state: string
  tone: Tone
  detail: string
  version: string
  port: number | null
  reported: boolean
}

// requested is the spec this card just wrote, held only until the hub reads it
// back. Rendering it keeps the controls where the user left them WITHOUT
// pretending the machine has applied anything: the rows below stay whatever the
// agent last reported, and awaitingAgent says so in words.
const requested = ref<EdgeHarnessSpec | null>(null)
const saving = ref(false)
const writeError = ref<string | null>(null)
const awaitingAgent = ref(false)
const selectionExpanded = ref(false)
// The machine's ceiling: what a turn may do without asking. Its own disclosure,
// because it is a different decision from which harnesses the machine offers
// and it is the one with consequences for the host.
const limitsExpanded = ref(false)
const permissionMode = ref<HarnessPermissionMode>('acceptEdits')
const allowedToolsText = ref('')

function specKey(spec: EdgeHarnessSpec | null | undefined): string {
  const mode = spec?.mode ?? 'auto'
  return [
    mode,
    [...(spec?.enabled ?? [])].sort().join(','),
    harnessPermissionMode(spec),
    (spec?.allowedTools ?? []).join(','),
  ].join(':')
}

// limits is what the ceiling controls currently say, folded into any spec this
// card writes. Every write carries the WHOLE setting, so switching the harness
// off and on again cannot quietly reset a pre-approval the machine's owner
// granted.
const limits = computed(() => ({
  permissionMode: permissionMode.value,
  allowedTools: parseAllowedTools(allowedToolsText.value),
}))
const limitsDirty = computed(() => {
  const stored = props.harness
  return permissionMode.value !== harnessPermissionMode(stored) ||
    parseAllowedTools(allowedToolsText.value).join(',') !== (stored?.allowedTools ?? []).join(',')
})

function sameSpec(a: EdgeHarnessSpec | null | undefined, b: EdgeHarnessSpec | null | undefined): boolean {
  return specKey(a) === specKey(b)
}

const effective = computed<EdgeHarnessSpec>(() => requested.value ?? props.harness ?? {})
const mode = computed<HarnessMode>(() => effective.value.mode ?? 'auto')
const harnessOn = computed(() => mode.value !== 'none')
const controlsDisabled = computed(() => props.disabled || saving.value)

const reported = computed(() => new Map((props.harnesses ?? []).map((status) => [status.name, status])))

// Rows cover every harness the portal knows about plus anything a newer agent
// reports, so "you asked for Codex and it is not here" has somewhere to appear.
const rowNames = computed(() => [
  ...HARNESS_NAMES,
  ...(props.harnesses ?? []).map((status) => status.name).filter((name) => !HARNESS_NAMES.includes(name)),
])

const rows = computed<HarnessRow[]>(() => rowNames.value.map((name) => {
  const status = reported.value.get(name)
  const base = {
    name,
    label: harnessLabel(name),
    version: status?.version ?? '',
    port: status?.port ?? null,
    reported: !!status,
  }
  if (!status) {
    return { ...base, state: 'Not reported', tone: 'muted' as Tone, detail: 'The agent has not reported this harness yet.' }
  }
  if (status.enabled && status.detected && status.ready) {
    return {
      ...base,
      state: 'Running',
      tone: 'success' as Tone,
      detail: status.port
        ? `Answering runner/v1 on port ${status.port}.`
        : 'Answering runner/v1 on this machine.',
    }
  }
  if (status.enabled && status.detected) {
    return {
      ...base,
      state: 'Starting',
      tone: 'warning' as Tone,
      detail: status.reasons?.length
        ? status.reasons.join(' ')
        : 'Switched on and installed; waiting for its runner to report ready.',
    }
  }
  if (status.enabled) {
    return {
      ...base,
      state: 'Not installed',
      tone: 'danger' as Tone,
      detail: 'Switched on here, but the executable is not on this machine. Install it on the host to make it available.',
    }
  }
  if (status.detected) {
    return {
      ...base,
      state: 'Switched off',
      tone: 'muted' as Tone,
      detail: 'Installed on this machine and not offered to this workspace.',
    }
  }
  return { ...base, state: 'Not installed', tone: 'muted' as Tone, detail: 'Not installed on this machine.' }
}))

const runningCount = computed(() => rows.value.filter((row) => row.state === 'Running').length)
const anyReported = computed(() => (props.harnesses ?? []).length > 0)

const summary = computed<{ status: string; tone: Tone }>(() => {
  if (!harnessOn.value) return { status: 'Off', tone: 'muted' }
  if (runningCount.value > 0) return { status: 'Running', tone: 'success' }
  if (rows.value.some((row) => reported.value.get(row.name)?.enabled)) return { status: 'Starting', tone: 'warning' }
  return { status: 'On', tone: 'muted' }
})

const chosenLabels = computed(() => (effective.value.enabled ?? []).map(harnessLabel))

const switchHelp = computed(() => {
  if (mode.value === 'none') return 'No harness runs on this machine.'
  if (mode.value === 'explicit') {
    const labels = chosenLabels.value
    const list = labels.length > 1
      ? `${labels.slice(0, -1).join(', ')} and ${labels[labels.length - 1]}`
      : labels[0] ?? 'nothing'
    return `Only ${list} ${labels.length > 1 ? 'are' : 'is'} offered, whether or not anything else is installed.`
  }
  return 'Every harness installed on the machine is offered.'
})

// ─── Per-harness choice ──────────────────────────────────────────────
// auto means "everything installed", so it starts with every box ticked: an
// unticked box is what turns the choice into an explicit list.
function selectionFor(spec: EdgeHarnessSpec): string[] {
  const specMode = spec.mode ?? 'auto'
  if (specMode === 'none') return []
  if (specMode === 'explicit') return rowNames.value.filter((name) => (spec.enabled ?? []).includes(name))
  return [...rowNames.value]
}

const selection = ref<string[]>(selectionFor(effective.value))
watch(() => `${specKey(effective.value)}|${rowNames.value.join(',')}`, () => {
  selection.value = selectionFor(effective.value)
}, { immediate: true })

// The ceiling controls follow the stored spec, and are NOT reset while the
// person is editing them: a poll landing mid-edit would otherwise throw away
// what they had typed.
watch(() => specKey(props.harness), () => {
  if (limitsDirty.value && limitsExpanded.value) return
  permissionMode.value = harnessPermissionMode(props.harness)
  allowedToolsText.value = formatAllowedTools(props.harness?.allowedTools)
}, { immediate: true })

// Every harness ticked is exactly what auto already means, and auto keeps
// picking up a harness installed later, so an all-ticked choice saves as auto
// rather than pinning today's list. No tick at all is none: an explicit empty
// list is not a thing the API accepts, and "offer nothing" is the opt-out.
function specFromSelection(names: string[]): EdgeHarnessSpec {
  const chosen = rowNames.value.filter((name) => names.includes(name))
  if (chosen.length === 0) return { mode: 'none' }
  if (chosen.length === rowNames.value.length) return { mode: 'auto' }
  return { mode: 'explicit', enabled: chosen }
}

const selectionSpec = computed(() => specFromSelection(selection.value))
const selectionDirty = computed(() => !sameSpec(selectionSpec.value, effective.value))

// desiredNames is what the effective spec resolves to given what the machine
// says is installed — the same decision the agent makes (ResolveHarnesses).
const desiredNames = computed(() => {
  if (mode.value === 'none') return []
  if (mode.value === 'explicit') return [...(effective.value.enabled ?? [])].sort()
  return (props.harnesses ?? []).filter((status) => status.detected).map((status) => status.name).sort()
})
const reportedEnabled = computed(() => (props.harnesses ?? [])
  .filter((status) => status.enabled)
  .map((status) => status.name)
  .sort())
const agentAgrees = computed(() => desiredNames.value.join(',') === reportedEnabled.value.join(','))

watch(agentAgrees, (agrees) => {
  if (agrees) awaitingAgent.value = false
})

// The hub read catching up with our own write is what retires `requested`.
watch(() => specKey(props.harness), () => {
  if (requested.value && sameSpec(props.harness, requested.value)) requested.value = null
})

async function write(spec: EdgeHarnessSpec) {
  if (controlsDisabled.value) return
  saving.value = true
  writeError.value = null
  try {
    await updateEdgeHarness(props.edgeName, props.edgeType, spec)
    requested.value = spec
    // The spec write is done; the machine has not confirmed it yet.
    awaitingAgent.value = !agentAgrees.value
    emit('changed')
  } catch (error) {
    requested.value = null
    writeError.value = (error as ErrorResponse)?.message ?? 'Could not save the harness setting.'
  } finally {
    saving.value = false
  }
}

function onToggle(event: Event) {
  const on = (event.target as HTMLInputElement).checked
  void write({ mode: on ? 'auto' : 'none', ...limits.value })
}

function onSaveLimits() {
  if (!limitsDirty.value) return
  void write({ ...(props.harness ?? { mode: 'auto' }), ...limits.value })
}

function onSaveSelection() {
  if (!selectionDirty.value) return
  void write({ ...selectionSpec.value, ...limits.value })
}

const announcement = computed(() => {
  if (saving.value) return 'Saving the harness setting.'
  if (awaitingAgent.value) return 'Harness setting saved. Waiting for the machine to confirm.'
  return `Coding harnesses are ${harnessOn.value ? 'on' : 'off'} for this machine. ${switchHelp.value}`
})
</script>

<template>
  <ResourceSectionCard
    id="edge-harness"
    eyebrow="Execution"
    title="Harness"
    description="Coding agents this machine offers as a local runner, such as Claude Code."
  >
    <template #actions>
      <span class="edge-section-card__count"><strong>{{ runningCount }}</strong> running</span>
      <StatusBadge :status="summary.status" :tone="summary.tone" />
      <button
        type="button"
        class="k-btn k-btn--ghost"
        :aria-expanded="selectionExpanded"
        aria-controls="edges-harness-choice"
        @click="selectionExpanded = !selectionExpanded"
      >
        {{ selectionExpanded ? 'Hide harnesses' : 'Choose harnesses' }}
        <component :is="selectionExpanded ? ChevronUp : ChevronDown" :size="14" aria-hidden="true" />
      </button>
      <button
        type="button"
        class="k-btn k-btn--ghost"
        :aria-expanded="limitsExpanded"
        aria-controls="edges-harness-limits"
        @click="limitsExpanded = !limitsExpanded"
      >
        {{ limitsExpanded ? 'Hide permissions' : 'Permissions' }}
        <component :is="limitsExpanded ? ChevronUp : ChevronDown" :size="14" aria-hidden="true" />
      </button>
    </template>

    <div class="edge-harness">
      <p class="edge-detail__sr-only" role="status" aria-live="polite">{{ announcement }}</p>
      <div v-if="writeError" class="banner error" role="alert">{{ writeError }}</div>

      <label class="edge-harness__switch k-checkbox-hit">
        <input
          id="edges-harness-enabled"
          class="k-checkbox"
          type="checkbox"
          role="switch"
          aria-labelledby="edges-harness-enabled-label"
          aria-describedby="edges-harness-enabled-help"
          :checked="harnessOn"
          :disabled="controlsDisabled"
          @change="onToggle"
        />
        <span class="edge-harness__switch-text">
          <span id="edges-harness-enabled-label" class="edge-harness__switch-title">
            <Bot :size="14" aria-hidden="true" /> Run coding harnesses on this machine
          </span>
          <span id="edges-harness-enabled-help" class="muted small">{{ switchHelp }}</span>
        </span>
      </label>

      <div v-if="!harnessOn" class="waiting edge-harness__off" role="note">
        <PowerOff :size="14" aria-hidden="true" />
        <span>Off — no harness runs on this machine, and nothing in this workspace can start a coding turn on it. Session state is kept, so switching back resumes where it left off.</span>
      </div>

      <div v-if="awaitingAgent" class="waiting" role="status">
        Saved. The machine applies this within a few seconds; this card updates when its agent confirms.
      </div>

      <p class="muted small edge-harness__note">
        A harness runs code on this machine as the agent's runner account, for callers who can reach it in this
        workspace. The model account is always the caller's, so no model credential is stored on the host.
      </p>

      <p v-if="!anyReported" class="muted small">
        This machine has not reported a harness yet. It reports on every heartbeat once its agent is connected.
      </p>

      <!--
        The machine's ceiling: what a turn may do without asking. Its own
        disclosure because it is a different decision from which harnesses are
        offered, and the consequential one — this is the machine's blast radius,
        which is why it is set here by whoever owns the machine rather than by
        whoever writes the agent.
      -->
      <fieldset v-if="limitsExpanded" id="edges-harness-limits" class="edge-harness__choice" aria-describedby="edges-harness-permissions-help">
        <legend class="lbl">Permissions</legend>
        <p id="edges-harness-permissions-help" class="muted small edge-harness__choice-hint">
          What a turn may do on this machine without stopping to ask. A caller cannot raise this.
        </p>

        <div class="edge-harness__modes">
          <label v-for="option in HARNESS_PERMISSION_MODES" :key="option.id" class="edge-harness__mode k-checkbox-hit">
            <input
              v-model="permissionMode"
              class="k-radio"
              type="radio"
              name="edges-harness-permission"
              :aria-labelledby="`edges-harness-permission-${option.id}-label`"
              :aria-describedby="`edges-harness-permission-${option.id}-help`"
              :value="option.id"
              :disabled="controlsDisabled"
            />
            <span class="edge-harness__mode-text">
              <span :id="`edges-harness-permission-${option.id}-label`" class="edge-harness__mode-title">{{ option.label }}</span>
              <span :id="`edges-harness-permission-${option.id}-help`" class="muted small">{{ option.blurb }}</span>
            </span>
          </label>
        </div>

        <label class="edge-harness__advanced">
          <span id="edges-harness-tools-label" class="lbl">Always allow <span class="muted small">— advanced</span></span>
          <textarea
            v-model="allowedToolsText"
            class="k-input edge-harness__tools"
            rows="3"
            spellcheck="false"
            :disabled="controlsDisabled"
            placeholder="WebSearch&#10;Bash(git *)"
            aria-labelledby="edges-harness-tools-label"
            aria-describedby="edges-harness-tools-hint"
          />
          <span id="edges-harness-tools-hint" class="muted small">
            Tool patterns that go ahead without asking, one per line. A pattern may contain spaces, so
            they are separated by lines or commas — never by spaces alone. Leave empty to be asked about
            everything the mode above does not already allow.
          </span>
        </label>

        <div class="edge-harness__choice-actions">
          <button
            type="button"
            class="k-btn k-btn--ghost"
            :disabled="controlsDisabled || !limitsDirty"
            :aria-busy="saving || undefined"
            @click="onSaveLimits"
          >
            {{ saving ? 'Saving…' : 'Save permissions' }}
          </button>
        </div>
      </fieldset>

      <!--
        The disclosure governs the WHOLE per-harness block, not a label style
        inside it. A button that says "Hide harnesses" and then leaves the list
        of harnesses on screen is answering a different question than the one it
        was asked; the header's count and badge are what the collapsed card
        summarises with.
      -->
      <fieldset v-if="selectionExpanded" id="edges-harness-choice" class="edge-harness__choice" aria-describedby="edges-harness-choice-help">
        <legend class="lbl">Harnesses offered</legend>
        <p id="edges-harness-choice-help" class="muted small edge-harness__choice-hint">
          Ticking every harness is the same as offering every installed one, so that is what gets saved — a machine
          that gains a harness later then picks it up on its own.
        </p>

        <ul class="edge-harness__rows">
          <li v-for="row in rows" :key="row.name" class="edge-harness__row">
            <label class="edge-harness__row-name k-checkbox-hit">
              <input
                v-model="selection"
                class="k-checkbox"
                type="checkbox"
                :value="row.name"
                :disabled="controlsDisabled"
              />
              <span>{{ row.label }}</span>
            </label>
            <div class="edge-harness__row-state">
              <StatusBadge :status="row.state" :tone="row.tone" />
              <span v-if="row.version" class="mono">v{{ row.version }}</span>
            </div>
            <p class="muted small edge-harness__row-detail">{{ row.detail }}</p>
          </li>
        </ul>

        <div class="edge-harness__choice-actions">
          <button
            type="button"
            class="k-btn k-btn--ghost"
            :disabled="controlsDisabled || !selectionDirty"
            :aria-busy="saving || undefined"
            @click="onSaveSelection"
          >
            {{ saving ? 'Saving…' : 'Save harness choice' }}
          </button>
        </div>
      </fieldset>
    </div>
  </ResourceSectionCard>
</template>
