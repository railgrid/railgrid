<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { ArrowLeft, Bot, Check } from 'lucide-vue-next'
import FormSelect from '../portalkit/FormSelect.vue'
import CreateGuidance from '../portalkit/CreateGuidance.vue'
import { mutate } from '../mutate'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import type { CreateSuccessDetail, Route } from '../router'
import type { Agent, AgentCreate as AgentCreateBody } from '../types'
import { useStoreRevision } from '../vue/runtime'

const NAME_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/
const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  authorityEpoch?: number
  createSession?: number
}>(), { authorityEpoch: 0, createSession: 0 })
const emit = defineEmits<{
  navigate: [route: Route]
  'create-success': [detail: CreateSuccessDetail]
  'create-cancel': [detail: Pick<CreateSuccessDetail, 'store' | 'authorityEpoch' | 'createSession'>]
}>()
const revision = useStoreRevision(() => props.store)

const name = ref('')
const modelCredential = ref('')
const systemPrompt = ref('')
const channel = ref('')
const web = ref(false)
const fanOut = ref(false)
const errors = reactive<Record<string, string>>({})
const busy = ref(false)
const nameInput = ref<HTMLInputElement | null>(null)

const agents = computed(() => { revision.value; return { ...props.store.agents } })
const credentials = computed(() => { revision.value; return { ...props.store.credentials } })
const channels = computed(() => { revision.value; return props.store.channelConnections() })
const credentialOptions = computed(() => credentials.value.data.map(item => ({
  value: item.name,
  label: `${item.name}${item.model ? ` (${item.model})` : ''}`,
})))
const channelOptions = computed(() => [
  { value: '', label: '— none —' },
  ...channels.value.map(item => ({
    value: item.metadata.name,
    label: `${item.spec.displayName || item.metadata.name} (${item.spec.type})`,
  })),
])
const capabilities = computed(() => [web.value ? 'web' : '', fanOut.value ? 'fan-out' : ''].filter(Boolean).join(', ') || 'Core only')

onMounted(() => { void nextTick(() => nameInput.value?.focus()) })

function clearErrors(): void {
  for (const key of Object.keys(errors)) delete errors[key]
}

function cancel(): void {
  if (busy.value) return
  emit('create-cancel', {
    store: props.store,
    authorityEpoch: props.authorityEpoch,
    createSession: props.createSession,
  })
}

async function submit(): Promise<void> {
  if (busy.value) return
  clearErrors()
  const normalizedName = name.value.trim()
  if (!normalizedName) errors.name = 'A name is required.'
  else if (!NAME_RE.test(normalizedName)) errors.name = 'Lowercase letters, digits and dashes only.'
  else if (agents.value.data.some(agent => agent.metadata.name === normalizedName)) errors.name = 'An agent with that name already exists.'
  if (!modelCredential.value) errors.modelCredential = 'Pick the model this agent reasons with.'
  if (Object.keys(errors).length) return

  const body: AgentCreateBody = {
    name: normalizedName,
    displayName: normalizedName,
    modelCredential: modelCredential.value,
  }
  const prompt = systemPrompt.value.trim()
  if (prompt) body.systemPrompt = prompt
  if (channel.value) body.channels = [{ name: 'primary', connectionRef: channel.value, primary: true }]
  const families = ['core']
  if (web.value) families.push('web')
  if (fanOut.value) families.push('spawn')
  if (families.length > 1) body.interactiveFamilies = families

  busy.value = true
  let result: Agent | undefined
  try {
    result = await mutate(props.store, {
      run: () => props.api.createAgent(body),
      success: `Agent “${normalizedName}” created.`,
      failure: 'Create failed',
      reload: ['agents'],
    })
  } finally {
    busy.value = false
  }
  if (result) emit('create-success', {
    resource: 'agent',
    name: normalizedName,
    item: result,
    store: props.store,
    authorityEpoch: props.authorityEpoch,
    createSession: props.createSession,
  })
}
</script>

<template>
  <div class="agents-create-page k-create-page">
    <button type="button" class="k-btn k-btn--ghost k-back-action" :disabled="busy" @click="cancel">
      <ArrowLeft aria-hidden="true" /> Agents
    </button>
    <header class="k-create-header">
      <h1 class="k-create-title">Create agent</h1>
      <p class="k-create-description">Choose the model, instructions, and optional channel this agent starts with.</p>
    </header>

    <div v-if="agents.error && !agents.hasSnapshot" class="k-card agents-state agents-state-error" role="alert">
      Could not load existing agents. {{ agents.error }}
      <button class="k-btn k-btn--ghost" type="button" :disabled="agents.loading" @click="store.load('agents')">Retry</button>
    </div>
    <div v-else-if="credentials.error && !credentials.hasSnapshot" class="k-card agents-state agents-state-error" role="alert">
      Could not load model credentials. {{ credentials.error }}
      <button class="k-btn k-btn--ghost" type="button" :disabled="credentials.loading" @click="store.load('credentials')">Retry</button>
    </div>
    <div v-else-if="!agents.hasSnapshot || !credentials.hasSnapshot" class="k-card agents-state agents-state-loading k-loading-reveal" role="status">
      Loading existing agents and model credentials…
    </div>

    <template v-else>
      <div v-if="agents.error" class="agents-stale" role="status">
        Showing the last loaded agents. {{ agents.error }}
        <button class="k-btn k-btn--ghost" type="button" :disabled="agents.loading" @click="store.load('agents')">Retry</button>
      </div>
      <div v-if="credentials.error" class="agents-stale" role="status">
        Showing the last loaded model credentials. {{ credentials.error }}
        <button class="k-btn k-btn--ghost" type="button" :disabled="credentials.loading" @click="store.load('credentials')">Retry</button>
      </div>

      <form class="agents-create-form agents-guided-form k-create-surface k-create-surface--guided" aria-label="Create agent" :aria-busy="busy" @submit.prevent="submit">
        <div class="k-create-body k-create-body--guided">
          <div class="k-create-fields">
            <label for="agent-create-name">
              Name *
              <input
                id="agent-create-name"
                ref="nameInput"
                v-model="name"
                class="k-input"
                name="name"
                placeholder="research-bot"
                autocomplete="off"
                required
                :disabled="busy"
                :aria-invalid="errors.name ? 'true' : undefined"
                :aria-describedby="errors.name ? 'agent-create-name-hint agent-create-name-error' : 'agent-create-name-hint'"
              />
              <span v-if="errors.name" id="agent-create-name-error" class="agents-fielderr" role="alert">{{ errors.name }}</span>
              <span id="agent-create-name-hint" class="agents-hint">A short id you'll reference from schedules and triggers.</span>
            </label>

            <label id="agent-create-model-label">
              Model credential *
              <FormSelect
                id="agent-create-model"
                v-model="modelCredential"
                name="modelCredential"
                :options="credentialOptions"
                placeholder="— pick a model —"
                :required="true"
                :disabled="busy"
                :invalid="Boolean(errors.modelCredential)"
                labelledby="agent-create-model-label"
                :describedby="errors.modelCredential ? 'agent-create-model-hint agent-create-model-error' : 'agent-create-model-hint'"
              />
              <span v-if="errors.modelCredential" id="agent-create-model-error" class="agents-fielderr" role="alert">{{ errors.modelCredential }}</span>
              <span id="agent-create-model-hint" class="agents-hint">The credential and model endpoint used for every turn.</span>
              <span v-if="credentialOptions.length === 0" class="agents-hint">
                No model credentials yet —
                <button type="button" class="k-btn k-btn--ghost agents-linkbtn" :disabled="busy" @click="emit('navigate', { kind: 'create', resource: 'model' })">
                  add one under Models
                </button>
                first.
              </span>
            </label>

            <label>
              System prompt
              <span class="agents-hint">optional — persona and standing instructions, not mechanics</span>
              <textarea v-model="systemPrompt" class="k-input" rows="3" placeholder="You are a concise assistant that…" :disabled="busy" />
            </label>

            <label id="agent-create-channel-label">
              Primary channel <span class="agents-hint">optional — where this agent messages you</span>
              <FormSelect v-model="channel" :options="channelOptions" :disabled="busy" labelledby="agent-create-channel-label" />
            </label>

            <fieldset class="agents-cap-fs">
              <legend>Can do <span class="agents-hint">— changeable later</span></legend>
              <label class="agents-cap">
                <input v-model="web" type="checkbox" :disabled="busy" />
                <span><strong>Read the web</strong> <span class="muted">— fetch pages; search needs a websearch tool</span></span>
              </label>
              <label class="agents-cap">
                <input v-model="fanOut" type="checkbox" :disabled="busy" />
                <span><strong>Research fan-out</strong> <span class="muted">— work independent parts in parallel</span></span>
              </label>
            </fieldset>
          </div>

          <CreateGuidance
            title="Prepare a usable agent"
            description="Choose the identity Faros will create and the model it can use immediately."
            :prerequisites="[
              credentialOptions.length ? 'A model credential is available in this workspace.' : 'Add a model credential before creating the agent.',
              'Optional channel connections can be added now or attached later from Config.',
            ]"
            :values="[
              { label: 'Agent name', value: name.trim() || 'Not entered yet', technical: true },
              { label: 'Model', value: modelCredential || 'Not selected', technical: true },
              { label: 'Primary channel', value: channel || 'None', technical: true },
              { label: 'Capabilities', value: capabilities },
            ]"
            :next-steps="[
              'Faros creates the agent and opens its Config workspace.',
              'Start a conversation to verify the model and instructions.',
              'Attach toolsets, schedules, and triggers when the core behavior is ready.',
            ]"
          >
            <template #icon><Bot aria-hidden="true" /></template>
          </CreateGuidance>
        </div>

        <div class="k-create-actions">
          <button type="button" class="k-btn k-btn--ghost secondary" :disabled="busy" @click="cancel">Cancel</button>
          <button class="k-btn k-btn--primary" type="submit" :disabled="busy || credentialOptions.length === 0">
            <Check aria-hidden="true" /> {{ busy ? 'Creating…' : 'Create agent' }}
          </button>
        </div>
      </form>
    </template>
  </div>
</template>
