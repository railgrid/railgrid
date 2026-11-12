<script setup>
import { computed, ref } from 'vue'
import { Bot } from 'lucide-vue-next'
import AIActivityFeed from '../../provider-sdk/agentkit-vue/AIActivityFeed.vue'
import AIAttachmentReceipts from '../../provider-sdk/agentkit-vue/AIAttachmentReceipts.vue'
import AIConversationComposer from '../../provider-sdk/agentkit-vue/AIConversationComposer.vue'
import AIConversationIdentity from '../../provider-sdk/agentkit-vue/AIConversationIdentity.vue'
import AIConversationRail from '../../provider-sdk/agentkit-vue/AIConversationRail.vue'
import AIConversationTurn from '../../provider-sdk/agentkit-vue/AIConversationTurn.vue'
import AIExecutionDetails from '../../provider-sdk/agentkit-vue/AIExecutionDetails.vue'
import AIInterrupt from '../../provider-sdk/agentkit-vue/AIInterrupt.vue'
import AIModelReasoningPicker from '../../provider-sdk/agentkit-vue/AIModelReasoningPicker.vue'
import AIPlanSteps from '../../provider-sdk/agentkit-vue/AIPlanSteps.vue'
import AIPrimaryAction from '../../provider-sdk/agentkit-vue/AIPrimaryAction.vue'
import AITurnProgress from '../../provider-sdk/agentkit-vue/AITurnProgress.vue'
import AITimestamp from '../../provider-sdk/agentkit-vue/AITimestamp.vue'
import AIWorkbenchPane from '../../provider-sdk/agentkit-vue/AIWorkbenchPane.vue'
import AIWorkbenchTabs from '../../provider-sdk/agentkit-vue/AIWorkbenchTabs.vue'
import ModelCollection from '../../provider-sdk/agentkit-vue/ModelCollection.vue'
import ModelConnectionCard from '../../provider-sdk/agentkit-vue/ModelConnectionCard.vue'
import ModelConnectionForm from '../../provider-sdk/agentkit-vue/ModelConnectionForm.vue'
import ModelUsageSection from '../../provider-sdk/agentkit-vue/ModelUsageSection.vue'
import ApprovalModePicker from '../../providers/app-studio/portal/src/ApprovalModePicker.vue'
import ReleasePipeline from '../../providers/app-studio/portal/src/ReleasePipeline.vue'
import { developmentPreviewDisplayPhase } from '../../providers/app-studio/portal/src/previewState'
import { releasePipelineView } from '../../providers/app-studio/portal/src/promotionState'
import StatusBadge from '../../provider-sdk/portalkit-vue/StatusBadge.vue'

defineProps({ stage: String })
const threads = ref([{ id: 'rollout', title: 'Review staging rollout' }, { id: 'probes', title: 'Check edge probes' }, { id: 'notes', title: 'Write recovery notes' }])
const activeThread = ref('rollout')
const showThreads = ref(false)
const pinned = ref(['rollout'])
const unread = ref(['probes'])
const snapshot = ref('waiting')
const progressOpen = ref(true)
const activityOpen = ref(true)
const approved = ref(false)
const message = ref('')
const sent = ref('')
const workbench = ref(false)
const pane = ref('plan')
const tabs = computed(() => ['plan', 'activity', 'readme'].map(id => ({ id, controlId: 'review-tab-' + id, controls: 'review-workbench', selected: pane.value === id, tabindex: pane.value === id ? 0 : -1, title: ({ plan: 'Plan', activity: 'Activity', readme: 'README.md' })[id] })))
const plan = { steps: [
  { id: 'check', status: 'completed', content: 'Check current edge connectivity' },
  { id: 'inspect', status: 'in_progress', content: 'Inspect rollback target' },
  { id: 'apply', status: 'pending', content: 'Prepare rollout changes' },
] }
const activity = [{ key: 'rollout', label: 'Rollout checks', rows: [
  { id: 'status', title: 'Read deployment status', target: 'edge/staging-west', status: 'succeeded', outcome: 'Deployment is available', expandable: true },
  { id: 'write', title: 'Apply rollout change', target: 'edge/staging-west', status: 'waiting', attention: true, needsReview: true },
] }]
const command = { command: 'kubectl rollout status deploy/relay', status: 'succeeded', exitCode: 0, output: ['deployment "relay" successfully rolled out'] }
const attachment = { id: 'receipt-1', filename: 'rollout-notes.txt', contentType: 'text/plain', sizeBytes: 28, createdAt: '2026-11-11T16:00:00Z', sha256: '0'.repeat(64) }
const skills = [{ id: 'rollout', name: 'Rollout review', description: 'Check rollout state and recovery options.', scope: 'workspace', enabled: true }, { id: 'verify', name: 'Verify health', description: 'Check readiness and probes.', scope: 'workspace', enabled: true }]
const responseMode = ref('default')
const studioMode = ref('Ask')
const approvalMode = ref('on_request')
const modelRows = [
  { id: 'scout', name: 'Scout', model: 'scout-v2', endpoint: 'https://models.example.test/v1', configured: true, default: true, status: 'Ready', tone: 'success' },
  { id: 'builder', name: 'Builder', model: 'builder-v1', endpoint: 'Local runtime', configured: false, default: false, status: 'Not tested', tone: 'muted' },
]
const selectedID = ref('scout')
const effort = ref('medium')
const modelFormOpen = ref(false)
const modelName = ref('')
const modelID = ref('')
const provider = ref('custom')
const endpoint = ref('https://models.example.test/v1')
const credential = ref('')
const discovered = ref([])
const result = ref('')
const testError = ref('')
function discover() {
  discovered.value = [{ id: 'scout-v2', name: 'Scout v2', compatibility: 'recommended' }, { id: 'builder-v1', name: 'Builder v1', compatibility: 'available' }]
}
const active = computed(() => ['running', 'waiting'].includes(snapshot.value))
function submit(value) {
  sent.value = value.content
  message.value = ''
}
function createThread() {
  const id = 'draft-' + (threads.value.length + 1)
  threads.value = [...threads.value, { id, title: 'New thread' }]
  activeThread.value = id
}
function flip(list, id) { return list.includes(id) ? list.filter(item => item !== id) : [...list, id] }

const preview = [
  ['No route', {}],
  ['Starting sandbox', { starting: true }],
  ['Waiting for the document', { previewURL: '/preview', documentState: 'connecting' }],
  ['Document loaded, bridge unavailable', { previewURL: '/preview', frameLoaded: true, documentState: 'unavailable' }],
  ['Bridge verified', { previewURL: '/preview', documentState: 'connected' }],
  ['Authorization failed', { authorizationError: 'Unavailable' }],
]
const release = ref('unavailable')
const sha = 'a'.repeat(40)
const releaseView = computed(() => {
  if (release.value === 'unavailable') return releasePipelineView(null)
  const built = ['verified', 'deploying', 'ready'].includes(release.value)
  const component = { name: 'relay', built, image: built ? 'registry.example.test/relay:sha-' + sha : '', tag: built ? 'sha-' + sha : '' }
  const data = {
    build: { status: built ? 'built' : 'pending', commitSHA: sha, components: [component], run: { found: true, headSHA: sha, status: ['running', 'deploying'].includes(release.value) ? 'in_progress' : 'completed', conclusion: release.value === 'failed' ? 'failure' : 'success' } },
    ...(release.value === 'deploying' ? { production: { phase: 'Deploying' }, requestedRolloutRevision: 'requested-2', observedRolloutRevision: 'observed-1' } : {}),
    ...(release.value === 'ready' ? { production: { phase: 'Ready', commitSHA: sha }, requestedRolloutRevision: 'revision-2', observedRolloutRevision: 'revision-2' } : {}),
  }
  return releasePipelineView(data, {}, { artifactNeedsAttention: release.value === 'lagging' })
})
</script>

<template>
  <div v-if="stage === 'conversation' || stage === 'studio'" class="spec-stack">
    <div class="spec-line">
      <AIConversationIdentity><template #icon><Bot :size="18" :stroke-width="1.5" aria-hidden="true" /></template><template #title>{{ stage === 'studio' ? 'Staging console' : 'Scout' }}</template><template #context>{{ threads.find(thread => thread.id === activeThread)?.title }}</template></AIConversationIdentity>
      <div class="spec-row"><button type="button" class="k-btn k-btn--ghost" :aria-expanded="showThreads" @click="showThreads = !showThreads">Threads</button><button type="button" class="k-btn k-btn--ghost" :aria-expanded="workbench" @click="workbench = !workbench">Workbench</button></div>
    </div>
    <AIConversationRail v-if="showThreads" :threads="threads" :active-thread-i-d="activeThread" :unread-thread-i-ds="unread" :pinned-thread-i-ds="pinned" :capabilities="{ create: true, pin: true, unread: true }" @select="activeThread = $event" @create="createThread" @toggle-pin="pinned = flip(pinned, $event)" @set-unread="(id, next) => unread = next ? [...new Set([...unread, id])] : unread.filter(item => item !== id)" />
    <div class="spec-row">
      <label class="spec-field spec-fit">Turn state<select v-model="snapshot" class="k-input" @change="approved = false"><option value="waiting">Approval required</option><option value="running">In progress</option><option value="completed">Completed</option><option value="aborted">Stopped</option><option value="failed">Failed</option></select></label>
      <template v-if="stage === 'studio'"><label class="spec-field spec-fit">Project mode<select v-model="studioMode" class="k-input"><option>Plan</option><option>Ask</option><option>Build</option></select></label><ApprovalModePicker :mode="approvalMode" @select="approvalMode = $event" /></template>
    </div>
    <div v-if="stage === 'studio'" class="spec-note" role="status">{{ ({ Plan: 'Prepare an actionable plan.', Ask: 'Investigate without editing the project.', Build: 'Make changes in the development workspace.' })[studioMode] }} Fixture policy: {{ approvalMode.replaceAll('_', ' ') }}.</div>
    <div class="spec-conversation">
      <AIConversationTurn turn-id="request" role="user" bubble copy-text="Check the staging rollout and tell me whether it can be reversed.">
        <template #before><AIAttachmentReceipts :attachments="[attachment]" /></template>
        Check the staging rollout and tell me whether it can be reversed.
      </AIConversationTurn>
      <AIConversationTurn turn-id="response" role="assistant" :copy-text="snapshot === 'failed' ? 'Could not read the rollback target.' : snapshot === 'aborted' ? 'Stopped. The rollout was not changed.' : 'The staging rollout is healthy. A rollback is available.'">
        <template #progress><AITurnProgress turn-id="response" :status="approved ? 'completed' : snapshot" :duration="active ? null : '12s'" :expanded="progressOpen" @toggle="progressOpen = $event"><AIPlanSteps :plan="plan" /></AITurnProgress></template>
        <template #trace><AIActivityFeed panel-id="review-activity" :groups="activity" :expanded="activityOpen" :count="2" @toggle="activityOpen = !activityOpen"><template #details="{ row }"><AIExecutionDetails v-if="row.id === 'status'" :execution="command" /></template></AIActivityFeed></template>
        <template #output><p v-if="snapshot === 'failed'">Could not read the rollback target.</p><p v-else-if="snapshot === 'aborted'">Stopped. The rollout was not changed.</p><p v-else>The staging rollout is healthy. A rollback is available.</p></template>
        <template v-if="snapshot === 'waiting'" #interrupt>
          <AIInterrupt kind="approval" :status="approved ? 'resolved' : 'pending'" title="Approval required" description="Allow this fixture to mark the rollout change as approved?"><template v-if="!approved" #actions><button type="button" class="k-btn k-btn--primary" @click="approved = true">Approve</button><button type="button" class="k-btn k-btn--ghost" @click="snapshot = 'aborted'">Deny</button></template></AIInterrupt>
        </template>
      </AIConversationTurn>
      <AIConversationTurn v-if="sent" role="user" bubble>{{ sent }}</AIConversationTurn>
    </div>
    <AIConversationComposer v-model="message" :skills="skills" :response-mode="responseMode" draft-scope-key="design-review" :active-run="active && !approved" placeholder="Ask about the rollout" @select-mode="responseMode = $event" @submit="submit">
      <template #controls><AIModelReasoningPicker compact :models="modelRows" :selected-i-d="selectedID" :effort="effort" :effort-options="selectedID === 'scout' ? [{ value: 'low', label: 'Low' }, { value: 'medium', label: 'Medium' }, { value: 'high', label: 'High' }] : []" @select="selectedID = $event" @update:effort="effort = $event" /></template>
      <template #actions><AIPrimaryAction :state="active && !approved ? 'stop' : 'send'" :disabled="!active && !message.trim()" @click="active && !approved ? snapshot = 'aborted' : submit({ content: message })" /></template>
    </AIConversationComposer>
    <section v-if="workbench" class="spec-section spec-stack">
      <AIWorkbenchTabs :tabs="tabs" fit-viewport @select="pane = $event" />
      <div id="review-workbench" role="tabpanel" :aria-labelledby="'review-tab-' + pane">
        <AIWorkbenchPane v-if="pane === 'plan'" title="Rollout plan" variant="document"><AIPlanSteps :plan="plan" /></AIWorkbenchPane>
        <AIWorkbenchPane v-else-if="pane === 'activity'" title="Command receipt" variant="tool"><AIExecutionDetails :execution="command" /></AIWorkbenchPane>
        <AIWorkbenchPane v-else title="README.md" variant="document"><p class="spec-note">Verify edge health before promotion.</p></AIWorkbenchPane>
      </div>
    </section>
  </div>
  <div v-else-if="stage === 'models'" class="spec-stack">
    <div class="spec-line"><h3>Workspace models</h3><button class="k-btn k-btn--primary" type="button" :aria-expanded="modelFormOpen" @click="modelFormOpen = !modelFormOpen">Add model</button></div>
    <ModelCollection :rows="modelRows" preference-key="railgrid:design-review:models">
      <template #cards="{ rows }"><ModelConnectionCard v-for="row in rows" :key="row.id" :name="row.name" :model="row.model" :endpoint="row.endpoint" :configured="row.configured" :is-default="row.default" :test-state="row.configured ? 'Connection verified' : 'Connection failed'" :test-tone="row.configured ? 'success' : 'danger'"><template #actions><button type="button" class="k-btn k-btn--text" @click="modelFormOpen = true; modelName = row.name; modelID = row.model">Edit {{ row.name }}</button></template></ModelConnectionCard></template>
    </ModelCollection>
    <AIModelReasoningPicker :models="modelRows" :selected-i-d="selectedID" :effort="effort" :effort-options="selectedID === 'scout' ? [{ value: 'low', label: 'Low' }, { value: 'medium', label: 'Medium' }, { value: 'high', label: 'High' }] : []" @select="selectedID = $event" @update:effort="effort = $event" />
    <ModelConnectionForm v-if="modelFormOpen" v-model:name="modelName" v-model:base-u-r-l="endpoint" v-model:credential="credential" v-model:model="modelID" :provider-preset="provider" :provider-options="[{ value: 'custom', label: 'Custom endpoint' }, { value: 'local', label: 'Local runtime' }]" provider-label-id="review-model-provider" provider-guidance="Use a fixture endpoint." credential-label="API key" credential-placeholder="Fixture value only" credential-hint="This value stays in this page." model-hint="Use a known model identifier or discover the fixture models." :discovered-models="discovered" :test-error="testError" :save-disabled="!modelName || !modelID" @update:provider="provider = $event" @discover="discover" @test="testError = 'The fixture endpoint did not respond.'" @save="result = 'Saved to this fixture only.'; modelFormOpen = false" @cancel="modelFormOpen = false" />
    <p v-if="result" role="status" class="spec-note">{{ result }}</p>
    <ModelUsageSection provider="Scout" :available="false" />
  </div>
  <div v-else-if="stage === 'evidence'" class="spec-stack">
    <section class="spec-section"><h3>Development preview evidence</h3><dl class="spec-definition"><div v-for="[label, input] in preview" :key="label"><dt>{{ label }}</dt><dd><StatusBadge :status="developmentPreviewDisplayPhase({ previewURL: '', authorizationError: '', documentState: 'disabled', ...input })" /></dd></div></dl></section>
    <section class="spec-section spec-stack"><h3>Release pipeline</h3>
      <label class="spec-field spec-fit">Evidence snapshot<select class="k-input" v-model="release"><option value="unavailable">No release evidence</option><option value="running">CI running</option><option value="failed">Matching CI run failed</option><option value="lagging">CI passed; artifact missing</option><option value="verified">Artifact verified</option><option value="deploying">New revision not observed</option><option value="ready">Production reports ready</option></select></label>
      <ReleasePipeline :pipeline="releaseView" />
    </section>
    <section class="spec-section"><h3>Attended result admission · study</h3><dl class="spec-definition"><div><dt>Reviewed provider and current HTML digest</dt><dd><StatusBadge status="Eligible" tone="success" /></dd></div><div><dt>Changed digest or missing result evidence</dt><dd><StatusBadge status="Unavailable" tone="warning" /></dd></div></dl></section>
  </div>
</template>
