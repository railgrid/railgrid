<script setup>
import { computed, nextTick, onMounted, ref } from 'vue'
import { AlertCircle, ArrowRight, ChevronDown, Cloud, Inbox, Info, Loader2, Plus, Trash2, Upload } from 'lucide-vue-next'
import ActionMenu from '../../provider-sdk/portalkit-vue/ActionMenu.vue'
import ConditionsPanel from '../../provider-sdk/portalkit-vue/ConditionsPanel.vue'
import ConfirmDialog from '../../provider-sdk/portalkit-vue/ConfirmDialog.vue'
import CreateGuidance from '../../provider-sdk/portalkit-vue/CreateGuidance.vue'
import FirstRunGuide from '../../provider-sdk/portalkit-vue/FirstRunGuide.vue'
import FormSelect from '../../provider-sdk/portalkit-vue/FormSelect.vue'
import InlineNotification from '../../provider-sdk/portalkit-vue/InlineNotification.vue'
import LayoutSelector from '../../provider-sdk/portalkit-vue/LayoutSelector.vue'
import StatusBadge from '../../provider-sdk/portalkit-vue/StatusBadge.vue'
import Tabs from '../../provider-sdk/portalkit-vue/Tabs.vue'
import ToastHost from '../../provider-sdk/portalkit-vue/ToastHost.vue'
import { confirmDialog } from '../../provider-sdk/portalkit-vue/confirm'
import { confirmModal } from '../../provider-sdk/portalkit/modal'
import { clearToasts, toast } from '../../provider-sdk/portalkit-vue/toast'
import '../../provider-sdk/portalkit/form-select'

defineProps({ stage: String })
const notice = ref('')
const checked = ref(false)
const progress = ref(42)
const drop = ref('')
const hovering = ref(false)
const zone = ref(null)
function pick(file) {
  if (!file) return
  drop.value = file.name
  hovering.value = false
}
const isInvalidFile = computed(() => drop.value && !drop.value.endsWith('.txt'))
const zoneLabel = computed(() => !drop.value ? 'Choose or drop a text file' : isInvalidFile.value ? 'Choose a .txt file to continue' : 'Selected ' + drop.value)

const tabs = [{ id: 'models', label: 'Models', count: 2 }, { id: 'connections', label: 'Connections' }, { id: 'usage', label: 'Usage', disabled: true }]
const tab = ref('models')
const layout = ref('grid')
const items = [
  { id: 'rename', label: 'Rename edge' },
  { id: 'copy', label: 'Copy edge name' },
  { id: 'blocked', label: 'Disconnect edge', disabled: true },
  { id: 'delete', label: 'Delete edge', tone: 'danger' },
]
const options = [{ value: 'west', label: 'US West' }, { value: 'east', label: 'US East' }, { value: 'eu', label: 'EU Central' }, { value: 'private', label: 'Private region', disabled: true }]
const region = ref('west')
const errorVariants = ref([
  { id: 'border', title: '1 · Border', value: '' },
  { id: 'icon', title: '2 · Border and icon · selected', value: '' },
  { id: 'message', title: '3 · Border and message surface', value: '' },
])
const reviewRegion = ref('')
const reviewSubmitted = ref(false)
const reviewSuccess = ref(false)
const regionField = ref(null)
const reviewRegionInvalid = computed(() => reviewSubmitted.value && !reviewRegion.value)
async function submitReview() {
  reviewSubmitted.value = true
  reviewSuccess.value = Boolean(reviewRegion.value)
  if (!reviewSuccess.value) {
    await nextTick()
    regionField.value?.$el?.querySelector('[role="combobox"]')?.focus()
  }
}
function changeReviewRegion(value) {
  reviewRegion.value = value
  reviewSuccess.value = false
}
const scheduleZone = ref('America/Chicago')
const regionSearch = ref('')
const vanilla = ref(null)
const vanillaError = ref(null)
const vanillaValue = ref('east')
onMounted(() => {
  if (vanilla.value) {
    Object.assign(vanilla.value, { options, value: vanillaValue.value, labelledby: 'review-plain-region' })
  }
  if (vanillaError.value) {
    Object.assign(vanillaError.value, {
      options,
      labelledby: 'review-vanilla-error-label',
      invalid: true,
      required: true,
      errorMessage: 'Choose a region before creating the edge.',
    })
    vanillaError.value.addEventListener('change', () => {
      vanillaError.value.invalid = !vanillaError.value.value
    })
  }
})
const conditions = [
  { type: 'Ready', status: 'True', reason: 'Connected', message: 'The edge is reachable.', lastTransitionTime: '2026-11-10T16:24:00Z' },
  { type: 'EndpointVerified', status: 'False', reason: 'Pending', message: 'The endpoint has not been verified.' },
  { type: 'StorageDegraded', status: 'True', reason: 'DiskPressure', message: 'The node is low on free storage.' },
  { type: 'Healthy', status: 'Unknown', reason: 'ProbePending', message: 'Waiting for the health probe.' },
]
const conditionState = ref('current')
async function confirm(danger, legacy = false) {
  const input = { title: danger ? 'Delete staging-west?' : 'Refresh staging-west?', message: danger ? 'This removes the saved edge registration.' : 'Railgrid will request the latest connection state.', confirmLabel: danger ? 'Delete edge' : 'Refresh edge', danger }
  const ok = await (legacy ? confirmModal(input) : confirmDialog(input))
  notice.value = ok ? 'Confirmed in the fixture.' : 'Canceled.'
}
const inlineVisible = ref(true)
function notify(kind) {
  const messages = { ok: 'Edge connection saved.', info: 'Sync is still running.', warning: 'This edge is near its storage limit.', error: 'Could not save the edge connection.' }
  toast(kind, messages[kind], { scope: 'design-review' })
}
function undoToast() {
  toast('ok', 'Connection removed from the list.', {
    scope: 'design-review', duration: 'persistent',
    action: { label: 'Undo', run: () => { notice.value = 'Connection restored in the fixture.' } },
  })
}

const steps = [
  { label: 'Connect an edge', description: 'Connect a Linux server or Kubernetes cluster.' },
  { label: 'Enable a provider', description: 'Add a capability to the workspace.' },
  { label: 'Review workspace', description: 'Check the connection and capability.' },
]
const current = ref(0)
const name = ref('')
const saved = ref('')
const adding = ref(false)
const target = ref('west')
const primitives = ['primary', 'ghost', 'text', 'danger', 'danger-solid']
const sizes = ['xs', 'sm', 'md', 'lg', '2xl', '3xl']
</script>

<template>
  <div v-if="stage === 'primitives'" class="spec-stack">
    <div class="spec-row">
      <button v-for="variant in primitives" :key="variant" type="button" class="k-btn" :class="'k-btn--'+variant" @click="notice = variant + ' action selected.'">
        <Trash2 v-if="variant.startsWith('danger')" :size="16" :stroke-width="1.75" aria-hidden="true" />
        {{ ({ primary: 'Create edge', ghost: 'Refresh', text: 'Show details', danger: 'Remove', 'danger-solid': 'Delete edge' })[variant] }}
      </button>
      <button type="button" class="k-btn k-btn--ghost" disabled>Unavailable</button>
      <button type="button" class="k-btn k-btn--ghost" aria-label="Inspect resource" data-k-tip="Inspect resource" @click="notice = 'Opened the sample resource.'"><Info :size="16" :stroke-width="1.75" aria-hidden="true" /></button>
    </div>
    <p v-if="notice" class="spec-note" role="status">{{ notice }}</p>
    <div class="spec-grid">
      <section class="spec-section">
        <h3>Shape and surface</h3>
        <div class="spec-row spec-radius-row">
          <div v-for="size in sizes" :key="size"><span class="spec-radius" :style="{ borderRadius: 'var(--radius-' + size + ')' }"></span><code>{{ size }}</code></div>
        </div>
        <div class="spec-row">
          <span class="k-avatar" aria-label="Avery Chen">AC</span>
          <span class="k-avatar k-avatar--sm" aria-label="Sam Rivera">SR</span>
          <StatusBadge status="Ready" />
          <StatusBadge status="Failed" />
          <span class="spec-note inline-flex items-center gap-1.5">
            <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-success live-dot" aria-hidden="true"></span>
            Live
          </span>
        </div>
      </section>
      <section class="spec-section">
        <h3>Focus and activity</h3>
        <label class="spec-field">Edge name<input class="k-input" value="staging-west"></label>
        <div class="spec-row" style="margin-top: 8px">
          <button type="button" role="switch" class="spec-switch-hit" :aria-checked="checked" @click="checked = !checked"><span class="k-toggle" :aria-checked="checked"><span class="k-toggle__knob"></span></span> Notifications</button>
          <span class="spec-note" role="status"><Loader2 :size="14" :stroke-width="1.75" class="k-spin inline-block" aria-hidden="true" /> Refreshing status</span>
          <span aria-label="Shortcut: Control K"><kbd class="k-kbd">Ctrl</kbd> + <kbd class="k-kbd">K</kbd></span>
        </div>
      </section>
      <section class="spec-section">
        <h3>Progress</h3>
        <div v-for="tone in ['accent', 'warning', 'danger']" :key="tone" class="spec-progress-row">
          <code>{{ tone }}</code><div class="k-progress" role="progressbar" :aria-label="tone + ' upload example'" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="progress">
            <div class="k-progress__bar" :class="'k-progress__bar--' + tone" :style="{ '--k-progress-value': progress / 100 }"></div>
          </div>
        </div>
        <button type="button" class="k-btn k-btn--ghost" @click="progress = progress >= 90 ? 12 : progress + 24">Advance progress · {{ progress }}%</button>
      </section>
      <section class="spec-section">
        <h3>Dropzone and disclosure</h3>
        <button class="k-dropzone" :class="{ 'is-dragover': hovering, 'is-error': isInvalidFile }" type="button" @click="zone.click()" @dragover.prevent="hovering = true" @dragleave="hovering = false" @drop.prevent="pick($event.dataTransfer.files[0])"><Upload :size="16" :stroke-width="1.75" aria-hidden="true" /> {{ zoneLabel }}</button>
        <input ref="zone" hidden type="file" @change="pick($event.target.files[0]); $event.target.value = ''">
        <details class="k-resource-technical k-resource-technical--compact">
          <summary class="k-resource-technical__summary"><span class="k-resource-technical__summary-label">Technical details</span><ChevronDown :size="14" class="k-resource-technical__chevron" aria-hidden="true" /></summary>
          <div class="k-resource-technical__body"><pre class="k-resource-technical__pre">edge/staging-west · connection probe pending</pre></div>
        </details>
      </section>
      <section class="spec-section">
        <h3>Raised, flat, and skeleton</h3>
        <div class="spec-row"><div class="k-card spec-tile">Raised card</div><div class="k-card k-card--flat spec-tile">Flat card</div><div class="spec-tile shimmer" role="img" aria-label="Loading skeleton"></div></div>
      </section>
      <section class="spec-section">
        <h3>Idle, selected, and empty</h3>
        <div class="island spec-row spec-tile"><span>Dashboard</span><span class="island-nav active">Edges</span></div>
        <div class="contour-grid-fade spec-tile"><Inbox :size="20" :stroke-width="1.5" aria-hidden="true" /><p>No connections yet</p></div>
      </section>
    </div>
  </div>

  <div v-else-if="stage === 'navigation'" class="spec-stack">
    <Tabs :tabs="tabs" :active="tab" aria-label="Sample provider sections" @select="tab = $event" />
    <div class="spec-line"><p>{{ tab === 'models' ? '2 configured models' : '1 provider connection' }}</p><LayoutSelector v-model="layout" /></div>
    <div :class="layout === 'grid' ? 'spec-grid' : 'spec-stack'"><div v-for="name in (tab === 'models' ? ['Scout', 'Builder'] : ['Default provider'])" :key="name" class="k-card spec-tile">{{ name }}</div></div>
    <section class="spec-section">
      <h3>Action menus</h3>
      <div class="spec-row">
        <ActionMenu label="Actions for staging-west" :items="items" @select="notice = $event + ' selected'" />
        <ActionMenu label="More actions" show-label :items="items" @select="notice = $event + ' selected'" />
        <ActionMenu label="Add tile" show-label primary-menu :items="[{ id: 'usage', label: 'Plan usage' }, { id: 'edges', label: 'Edges' }]" @select="notice = $event + ' selected'" />
        <ActionMenu label="More create actions" primary-label="Create edge" :items="items" @select="notice = $event + ' selected'" @primary="notice = 'Create edge selected'" />
        <ActionMenu label="Issuing token" :items="items" :busy="true" busy-label="Issuing token for staging-west…" />
        <ActionMenu label="Unavailable edge actions" :items="items" disabled />
      </div>
      <p class="spec-note" role="status">{{ notice }}</p>
    </section>
  </div>

  <div v-else-if="stage === 'status'" class="spec-stack">
    <div class="spec-row">
      <StatusBadge status="Ready" /><StatusBadge status="Pending" /><StatusBadge status="Failed" /><StatusBadge status="Unknown" />
      <StatusBadge status="Queued" tone="warning" /><StatusBadge status="Disconnected" :connected="false" />
    </div>
    <label class="spec-field spec-fit">Condition snapshot<select v-model="conditionState" class="k-input"><option value="current">Current generation</option><option value="reconciling">Reconciling</option><option value="empty">No conditions</option></select></label>
    <ConditionsPanel show-title :conditions="conditionState === 'empty' ? [] : conditions" :generation="5" :observed-generation="conditionState === 'reconciling' ? 4 : 5" :semantics="{ StorageDegraded: { polarity: 'negative', severity: 'warning' } }" />
  </div>

  <div v-else-if="stage === 'feedback'" class="spec-stack">
    <section class="spec-section">
      <h3>Confirmation</h3>
      <div class="spec-row">
        <button class="k-btn k-btn--primary" type="button" @click="confirm(false)">Refresh edge</button>
        <button class="k-btn k-btn--danger" type="button" @click="confirm(true)">Delete edge</button>
        <button class="k-btn k-btn--ghost" type="button" @click="confirm(false, true)">Vanilla confirmation</button>
      </div>
      <ConfirmDialog />
    </section>
    <section class="spec-section">
      <h3>Toasts</h3>
      <div class="spec-row">
        <button v-for="[kind, label] in [['ok', 'Success'], ['info', 'Info'], ['warning', 'Warning'], ['error', 'Error']]" :key="kind" class="k-btn k-btn--ghost" type="button" @click="notify(kind)">{{ label }}</button>
        <button class="k-btn k-btn--ghost" type="button" @click="undoToast">Remove with Undo</button>
        <button class="k-btn k-btn--text" type="button" @click="clearToasts('design-review')">Clear toasts</button>
      </div>
      <ToastHost />
    </section>
    <section class="spec-section">
      <h3>Contextual feedback</h3>
      <InlineNotification v-if="inlineVisible" tone="error" title="Could not connect staging-west" message="Check that the edge agent is running, then try again." action-label="Retry connection" dismissible @action="notice = 'Connection retry requested.'" @dismiss="inlineVisible = false" />
      <button v-else class="k-btn k-btn--ghost" type="button" @click="inlineVisible = true">Restore example</button>
    </section>
    <p class="spec-note" role="status">{{ notice }}</p>
  </div>

  <div v-else-if="stage === 'controls'" class="spec-stack">
    <section class="spec-stack">
      <div>
        <h3>Error treatment comparisons</h3>
        <p class="spec-note">Three fixture-only proposals. Choose a region in each to see it recover. Use the Theme control above to compare light and dark.</p>
      </div>
      <div class="spec-error-variants">
        <section v-for="variant in errorVariants" :key="variant.id" class="spec-error-variant" :class="'spec-error-variant--' + variant.id">
          <h4>{{ variant.title }}</h4>
          <div class="spec-field">
            <span :id="'variant-label-' + variant.id">Region · required</span>
            <FormSelect v-model="variant.value" :options="options" :labelledby="'variant-label-' + variant.id" :describedby="'variant-help-' + variant.id" :invalid="!variant.value" required />
            <small v-if="!variant.value" :id="'variant-help-' + variant.id" class="spec-error-message">
              <AlertCircle v-if="variant.id === 'icon' || variant.id === 'message'" :size="15" :stroke-width="1.75" aria-hidden="true" />
              <span>Choose a region before creating the edge.</span>
            </small>
            <small v-else :id="'variant-help-' + variant.id">Choose where this edge will run.</small>
          </div>
        </section>
      </div>
    </section>
    <section class="spec-section spec-stack">
      <h3>Shared FormSelect validation</h3>
      <p class="spec-note">Submit without a region, then choose one and submit again. This example uses the shared border and icon treatment with caller-owned validation.</p>
      <form class="spec-stack" novalidate @submit.prevent="submitReview">
        <div class="spec-field">
          <span id="review-required-label">Region · required</span>
          <FormSelect ref="regionField" :model-value="reviewRegion" :options="options" labelledby="review-required-label" :describedby="reviewRegionInvalid ? undefined : 'review-required-help'" :invalid="reviewRegionInvalid" :error-message="reviewRegionInvalid ? 'Choose a region before creating the edge.' : ''" required @update:model-value="changeReviewRegion" />
          <small v-if="!reviewRegionInvalid" id="review-required-help">Choose where this edge will run.</small>
        </div>
        <div><button class="k-btn k-btn--primary" type="submit">Create sample edge</button></div>
        <p v-if="reviewSuccess" role="status" class="spec-note">Sample edge ready for {{ options.find(option => option.value === reviewRegion)?.label }}.</p>
      </form>
    </section>
    <section class="spec-section spec-stack">
      <h3>Framework-neutral shared error</h3>
      <div class="spec-field">
        <span id="review-vanilla-error-label">Region · required</span>
        <railgrid-form-select ref="vanillaError"></railgrid-form-select>
      </div>
    </section>
    <div>
      <div class="spec-grid k-create-fields">
      <div class="spec-field"><span id="review-region">Region · required</span><FormSelect v-model="region" :options="options" labelledby="review-region" describedby="review-region-help" required /><small id="review-region-help">Choose a region close to the edge.</small></div>
      <div class="spec-field"><span id="review-plain-region">Framework-neutral region</span><railgrid-form-select ref="vanilla" @change="vanillaValue = $event.target.value"></railgrid-form-select><small>Current value: {{ vanillaValue }}</small></div>
      <label class="spec-field">Native region<select class="k-input" v-model="target"><option v-for="option in options" :key="option.value" :value="option.value" :disabled="option.disabled">{{ option.label }}</option></select></label>
      <div class="spec-field"><span id="review-invalid">Missing region</span><FormSelect model-value="" :options="options" labelledby="review-invalid" describedby="review-invalid-help" invalid required /><small id="review-invalid-help" class="spec-danger">Choose a region before creating the edge.</small></div>
      <div class="spec-field"><span id="review-empty">No options</span><FormSelect model-value="" :options="[]" placeholder="No regions available" labelledby="review-empty" describedby="review-empty-help" /><small id="review-empty-help">Connect an edge, then refresh regions.</small></div>
      <div class="spec-field"><span id="review-disabled">Locked region</span><FormSelect model-value="west" :options="options" labelledby="review-disabled" disabled /></div>
      <label class="spec-field">Workspace name<input class="k-input" value="Platform engineering" aria-describedby="review-workspace-help"><small id="review-workspace-help">Shown in navigation. Workspace IDs stay stable when display names change.</small></label>
      <label class="spec-field">Description · optional<textarea class="k-input" rows="3" placeholder="Describe what this workspace is for"></textarea></label>
      </div>
    </div>
    <fieldset class="spec-section" aria-describedby="review-group-help">
      <legend>Connection method</legend>
      <div class="spec-row">
        <label class="k-checkbox-hit"><input type="radio" class="k-checkbox" name="connection-method" checked> Agent</label>
        <label class="k-checkbox-hit"><input type="radio" class="k-checkbox" name="connection-method"> Manual</label>
      </div>
      <p id="review-group-help" class="spec-note">The agent connects outbound from the edge.</p>
    </fieldset>
    <fieldset class="spec-section">
      <legend>Notifications</legend>
      <label class="k-checkbox-hit"><input type="checkbox" class="k-checkbox"> Notify on failure</label>
    </fieldset>
    <section class="spec-section"><h3>Native fallbacks · reference examples</h3>
      <div class="spec-grid"><label class="spec-field">Capacity · {{ progress }}%<input v-model.number="progress" type="range" min="0" max="100"></label>
      <label class="spec-field">Start time<input class="k-input" type="datetime-local" value="2026-11-11T10:00" aria-describedby="review-time-help"><small id="review-time-help">Local time in {{ scheduleZone }}. Confirm the offset before scheduling.</small></label>
      <label class="spec-field">Time zone<select v-model="scheduleZone" class="k-input"><option>America/Chicago</option><option>UTC</option></select></label></div>
      <p class="spec-note">Scheduling study · ambiguous and nonexistent daylight-saving times require explicit handling in a real form.</p>
    </section>
    <section class="spec-section spec-stack">
      <h3>Finding an option · proposal</h3>
      <p class="spec-note">The current FormSelect uses arrow keys. For long or changing lists, evaluate a searchable picker with loading, empty, and error states.</p>
      <label class="spec-field">Search regions<input v-model="regionSearch" class="k-input" type="search" placeholder="Search regions"></label>
      <ul class="spec-note"><li v-for="option in options.filter(option => option.label.toLowerCase().includes(regionSearch.toLowerCase()))" :key="option.value">{{ option.label }}{{ option.disabled ? ' · unavailable' : '' }}</li></ul>
      <p v-if="!options.some(option => option.label.toLowerCase().includes(regionSearch.toLowerCase()))" role="status" class="spec-note">No regions match. Clear your search or connect an edge.</p>
    </section>
    <section class="spec-section">
      <h3>Global command palette · open design</h3>
      <p class="spec-note">Define scope, a visible entry point, platform-specific shortcuts, focus and Escape behavior, results states, and a narrow layout before fixing its size. The contextual slash palette remains separate.</p>
    </section>
  </div>

  <div v-else-if="stage === 'creation'" class="spec-stack">
    <div class="spec-row"><a class="k-btn k-btn--ghost" href="./onboarding.html" target="_blank" rel="noopener">Open workspace onboarding <ArrowRight :size="14" aria-hidden="true" /></a><a class="k-btn k-btn--ghost" href="./onboarding.html?surface=self-hosting" target="_blank" rel="noopener">Open provider self-hosting <ArrowRight :size="14" aria-hidden="true" /></a></div>
    <div class="spec-grid">
      <form class="spec-stack" @submit.prevent="saved = name">
        <h3>Create an edge</h3>
        <div class="k-create-fields spec-stack"><label class="spec-field">Name · required<input v-model="name" class="k-input" required placeholder="staging-west"></label>
          <label class="spec-field">Environment<select class="k-input"><option>Staging</option><option>Production</option></select></label></div>
        <div class="spec-row"><button class="k-btn k-btn--primary" type="submit" :disabled="!name.trim()">Create edge</button><button class="k-btn k-btn--text" type="button" @click="name = ''; saved = ''">Cancel</button></div>
        <p v-if="saved" role="status" class="spec-note">Created {{ saved }} in the fixture.</p>
      </form>
      <CreateGuidance title="Before you connect an edge" description="An edge agent opens an outbound connection to Railgrid." :prerequisites="['A Linux server or Kubernetes cluster you administer.']" :values="[{ label: 'Resource name', value: name || 'staging-west', technical: true }]" :next-steps="['Install the edge agent.', 'Check the connection state.']" />
    </div>
    <section class="spec-section"><div class="spec-line"><h3>Parent-owned additions</h3><button class="k-btn k-btn--ghost" type="button" :aria-expanded="adding" @click="adding = !adding"><Plus :size="16" :stroke-width="1.75" aria-hidden="true" /> Add label</button></div>
      <form v-if="adding" class="spec-row" @submit.prevent="adding = false; notice = 'Label added in the fixture.'"><input class="k-input spec-fit" aria-label="Label value" placeholder="environment=staging" required><button class="k-btn k-btn--primary" type="submit">Add label</button></form><p class="spec-note" role="status">{{ notice }}</p></section>
    <FirstRunGuide title="Set up your workspace" description="Connect infrastructure, then enable a provider to manage it." :steps="steps" :current-step="Math.min(2, current)" :complete="current >= 3" :primary-label="current >= 3 ? 'Start over' : 'Continue setup'" secondary-label="Explore later" @primary="current = current >= 3 ? 0 : current + 1" @secondary="notice = 'Setup left for later.'"><template #icon><Cloud :size="22" :stroke-width="1.5" aria-hidden="true" /></template></FirstRunGuide>
  </div>
</template>
