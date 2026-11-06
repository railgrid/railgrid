<script setup lang="ts">
import { computed, ref } from 'vue'
import AIConversationComposer, { type AgentKitAttachmentTransport } from './agentkit/AIConversationComposer.vue'
import AssistantMessageAnnotations from './AssistantMessageAnnotations.vue'
import { api, isProjectAPINotFoundError } from './api'
import { assistantSlashCommands } from './assistantCommandPalette'
import type { AssistantComposerState } from './assistantCommandPalette'
import { assistantResourceProviders, discoverAssistantResources } from './assistantResources'
import { removeAssistantComposerDraftAttachment } from './assistantComposerDraft'
import type {
  ProjectAssistantAnnotation,
  ProjectAssistantContentPart,
  ProjectAssistantContextResource,
  ProjectAssistantRunMode,
  ProjectAssistantSkill,
  RailgridContext,
  ProviderItem,
} from './types'

const props = withDefaults(defineProps<{
  modelValue: string
  responseMode?: ProjectAssistantRunMode
  contentParts?: ProjectAssistantContentPart[]
  draftScopeKey?: string
  projectName: string
  skills: ProjectAssistantSkill[]
  selectedSkills?: ProjectAssistantSkill[]
  selectedResources?: ProjectAssistantContextResource[]
  ctx: RailgridContext | null
  providers: ProviderItem[]
  disabled?: boolean
  submitting?: boolean
  activeRun?: boolean
  queueingEnabled?: boolean
  placeholder?: string
  annotationDocumentId?: string
  annotationPagePath?: string
  unresolvedAnnotationIds?: string[]
}>(), {
  responseMode: 'default',
  contentParts: () => [],
  draftScopeKey: '',
  selectedSkills: () => [],
  selectedResources: () => [],
  disabled: false,
  submitting: false,
  activeRun: false,
  queueingEnabled: true,
  placeholder: 'Message this project',
  annotationDocumentId: '',
  annotationPagePath: '',
  unresolvedAnnotationIds: () => [],
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'update:contentParts': [value: ProjectAssistantContentPart[]]
  'update:selectedSkills': [value: ProjectAssistantSkill[]]
  'update:selectedResources': [value: ProjectAssistantContextResource[]]
  'update:attachmentsPending': [value: boolean]
  state: [value: AssistantComposerState]
  submit: [value: AssistantComposerState, intent: 'queue' | 'steer']
  selectMode: [mode: ProjectAssistantRunMode]
}>()

const inner = ref<InstanceType<typeof AIConversationComposer> | null>(null)
const resourceProviders = computed(() => assistantResourceProviders(props.providers))
const attachmentTransport = computed<AgentKitAttachmentTransport>(() => {
  const context = props.ctx
  const projectName = props.projectName
  return {
    upload: (file, signal, clientID) => api.uploadAssistantAttachment(context, projectName, file, signal, clientID),
    delete: (attachmentID) => api.deleteAssistantAttachment(context, projectName, attachmentID),
    isNotFoundError: isProjectAPINotFoundError,
    removeOwnerDraftReceipt: removeAssistantComposerDraftAttachment,
  }
})

function discoverResources(provider: { name: string; displayName: string; resourceTypes: Array<{ provider: string; providerDisplayName: string; apiVersion: string; kind: string; resource: string }> }) {
  const match = resourceProviders.value.find((candidate) => candidate.name === provider.name)
  return discoverAssistantResources(props.ctx, match?.resourceTypes ?? [])
}

function projectAnnotations(values: unknown[]): ProjectAssistantAnnotation[] {
  return values as ProjectAssistantAnnotation[]
}

function updateContentParts(parts: unknown[]) {
  emit('update:contentParts', parts as ProjectAssistantContentPart[])
}
function forwardState(state: unknown) { emit('state', state as AssistantComposerState) }
function forwardSubmit(state: unknown, intent: 'queue' | 'steer') { emit('submit', state as AssistantComposerState, intent) }

function recoverUnavailableAttachments(ids: readonly string[]) {
  return inner.value?.recoverUnavailableAttachments(ids) ?? Promise.resolve({ recovered: 0, removed: 0, unresolved: 0 })
}
function commitAttachments(ids: readonly string[], scope: string, owner: string) {
  inner.value?.commitAttachments(ids, scope, owner)
}
function adoptAttachmentDraftScope(previous: string, next: string, owner: string, ids: readonly string[]) {
  inner.value?.adoptAttachmentDraftScope(previous, next, owner, ids)
}

defineExpose({
  focus: () => inner.value?.focus(),
  openPalette: () => inner.value?.openPalette(),
  closePalette: (options?: { restoreFocus?: boolean }) => inner.value?.closePalette(options?.restoreFocus),
  recoverUnavailableAttachments,
  commitAttachments,
  adoptAttachmentDraftScope,
})
</script>

<template>
  <AIConversationComposer
    ref="inner"
    :model-value="modelValue"
    :response-mode="responseMode"
    :content-parts="contentParts"
    :draft-scope-key="draftScopeKey"
    :attachment-owner-key="projectName"
    :attachment-transport="attachmentTransport"
    :skills="skills"
    :selected-skills="selectedSkills"
    :selected-resources="selectedResources"
    :resource-providers="resourceProviders"
    :discover-resources="discoverResources"
    :commands="assistantSlashCommands"
    :disabled="disabled"
    :submitting="submitting"
    :active-run="activeRun"
    :queueing-enabled="queueingEnabled"
    :placeholder="placeholder"
    @update:model-value="emit('update:modelValue', $event)"
    @update:content-parts="updateContentParts"
    @update:selected-skills="emit('update:selectedSkills', $event)"
    @update:selected-resources="emit('update:selectedResources', $event)"
    @update:attachments-pending="emit('update:attachmentsPending', $event)"
    @state="forwardState"
    @submit="forwardSubmit"
    @select-mode="emit('selectMode', $event as ProjectAssistantRunMode)"
  >
    <template #annotations="{ annotations, removeAll }">
      <AssistantMessageAnnotations
        :annotations="projectAnnotations(annotations)"
        :current-document-id="annotationDocumentId"
        :current-page-path="annotationPagePath"
        :unresolved-annotation-ids="unresolvedAnnotationIds"
        :rebind-across-documents="true"
        :clearable="true"
        disclosure-id="assistant-composer-annotations"
        @remove-all="removeAll"
      />
    </template>
    <template #controls><slot name="controls" /></template>
    <template #actions><slot name="actions" /></template>
  </AIConversationComposer>
</template>
