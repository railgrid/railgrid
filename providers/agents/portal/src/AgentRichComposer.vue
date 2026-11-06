<script setup lang="ts">
import { computed, ref } from 'vue'
import { ApiError, type ApiClient } from './api'
import { agentResourceProviders, discoverAgentResources } from './agentResources'
import AIConversationComposer, { type AgentKitAttachmentTransport } from './agentkit/AIConversationComposer.vue'
import type { AgentKitComposerPart, AgentKitComposerState, AgentKitContextResource, AgentKitSkill, AgentKitResourceProvider } from './agentkit/composer'

const props = withDefaults(defineProps<{
  api: ApiClient
  agent: string
  ownerKey: string
  draftStorageKey: string
  modelValue: string
  contentParts?: AgentKitComposerPart[]
  draftScopeKey: string
  skills: AgentKitSkill[]
  selectedSkills?: AgentKitSkill[]
  selectedResources?: AgentKitContextResource[]
  providers?: Array<{ name: string; displayName: string; ready: boolean; export?: { resources?: Array<{ name: string; apiVersion: string; kind: string; actions?: Array<{ deprecation?: { deprecated?: boolean } }> }> } }>
  disabled?: boolean
  submitting?: boolean
  attachmentsPending?: boolean
  placeholder?: string
}>(), {
  contentParts: () => [],
  selectedSkills: () => [],
  selectedResources: () => [],
  providers: () => [],
  disabled: false,
  submitting: false,
  attachmentsPending: false,
  placeholder: 'Message the agent',
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'update:contentParts': [value: AgentKitComposerPart[]]
  'update:selectedSkills': [value: AgentKitSkill[]]
  'update:selectedResources': [value: AgentKitContextResource[]]
  'update:attachmentsPending': [value: boolean]
  state: [value: AgentKitComposerState]
  submit: [value: AgentKitComposerState]
}>()

const inner = ref<InstanceType<typeof AIConversationComposer> | null>(null)
const resourceProviders = computed(() => agentResourceProviders(props.providers))
const attachmentTransport = computed<AgentKitAttachmentTransport>(() => {
  const api = props.api
  const agent = props.agent
  const sessionID = props.draftScopeKey
  const storageKey = props.draftStorageKey
  return {
    upload: (file, signal, clientID) => api.uploadAgentAttachment(agent, sessionID, file, signal, clientID),
    delete: (attachmentID) => api.deleteAgentAttachment(agent, sessionID, attachmentID),
    isNotFoundError: (error) => error instanceof ApiError && error.status === 404,
    removeOwnerDraftReceipt: (_scopeKey, attachmentID) => removeReceiptFromStoredDraft(storageKey, attachmentID),
  }
})

async function discoverResources(provider: AgentKitResourceProvider) {
  const match = resourceProviders.value.find((candidate) => candidate.name === provider.name)
  return discoverAgentResources(props.api.context(), match?.resourceTypes ?? [])
}

function removeReceiptFromStoredDraft(storageKey: string, attachmentID: string): boolean {
  if (!storageKey || typeof localStorage === 'undefined') return false
  try {
    const raw = localStorage.getItem(storageKey)
    if (!raw) return true
    const value = JSON.parse(raw) as { contentParts?: AgentKitComposerPart[] }
    if (Array.isArray(value.contentParts)) {
      value.contentParts = value.contentParts.filter((part) => part.type !== 'attachment' || part.attachment.id !== attachmentID)
      localStorage.setItem(storageKey, JSON.stringify(value))
    }
    return true
  } catch {
    return false
  }
}

function updateContentParts(parts: unknown[]) { emit('update:contentParts', parts as AgentKitComposerPart[]) }
function forwardState(value: AgentKitComposerState) { emit('state', value) }
function forwardSubmit(value: AgentKitComposerState) { emit('submit', value) }

function focus() { inner.value?.focus() }
function commitAttachments(ids: readonly string[], scope: string, owner: string) { inner.value?.commitAttachments(ids, scope, owner) }

defineExpose({ focus, commitAttachments })
</script>

<template>
  <AIConversationComposer
    ref="inner"
    :model-value="modelValue"
    :content-parts="contentParts"
    :draft-scope-key="draftScopeKey"
    :attachment-owner-key="ownerKey"
    :attachment-transport="attachmentTransport"
    :skills="skills"
    :selected-skills="selectedSkills"
    :selected-resources="selectedResources"
    :resource-providers="resourceProviders"
    :discover-resources="discoverResources"
    :disabled="disabled"
    :submitting="submitting"
    :active-run="false"
    :queueing-enabled="false"
    :placeholder="placeholder"
    @update:model-value="emit('update:modelValue', $event)"
    @update:content-parts="updateContentParts"
    @update:selected-skills="emit('update:selectedSkills', $event)"
    @update:selected-resources="emit('update:selectedResources', $event)"
    @update:attachments-pending="emit('update:attachmentsPending', $event)"
    @state="forwardState"
    @submit="forwardSubmit"
  >
    <template #controls><slot name="controls" /></template>
    <template #actions><slot name="actions" /></template>
  </AIConversationComposer>
</template>
