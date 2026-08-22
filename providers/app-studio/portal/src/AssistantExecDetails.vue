<script setup lang="ts">
import { computed } from 'vue'
import { Check, ExternalLink, Loader2, Terminal, X } from 'lucide-vue-next'
import {
  assistantExecStatusPresentation,
  formatAssistantExecCommand,
  formatAssistantExecDuration,
  formatAssistantExecExit,
  parseAssistantExecDisclosure,
} from './assistantExecDisclosure'
import type { ProjectAssistantExecDisclosure } from './types'

const props = withDefaults(defineProps<{
  exec?: ProjectAssistantExecDisclosure
  variant?: 'approval' | 'activity'
}>(), {
  variant: 'activity',
})

// Keep this component safe even when a caller receives a view model assembled
// outside the feed parser. Rendering only the parser's allowlisted projection
// prevents raw tool arguments from entering the DOM.
const disclosure = computed(() => parseAssistantExecDisclosure(props.exec))
const command = computed(() => formatAssistantExecCommand(disclosure.value))
const exitLabel = computed(() => formatAssistantExecExit(disclosure.value))
const durationLabel = computed(() => formatAssistantExecDuration(disclosure.value?.durationMs))
const outputLabel = computed(() => disclosure.value?.outputTruncated ? 'Output was truncated to the bounded limit.' : '')
const activityMetadata = computed(() => {
  const exec = disclosure.value
  if (!exec) return []
  return [
    exec.component ? { label: 'Component', value: exec.component } : undefined,
    { label: 'Relative cwd', value: exec.workdir || '.' },
    durationLabel.value ? { label: 'Duration', value: durationLabel.value } : undefined,
    exitLabel.value ? { label: 'Exit status', value: exitLabel.value } : undefined,
  ].filter((row): row is { label: string; value: string } => Boolean(row))
})
const activityStatus = computed(() => {
  const exec = disclosure.value
  const presentation = assistantExecStatusPresentation(exec)
  switch (presentation.state) {
    case 'running': return { label: presentation.label, tone: 'running', icon: Loader2 }
    case 'failed':
    case 'timed_out': return { label: presentation.label, tone: 'danger', icon: X }
    case 'blocked': return { label: presentation.label, tone: 'attention', icon: X }
    case 'canceled': return { label: presentation.label, tone: 'muted', icon: X }
    default:
      return { label: 'Success', tone: 'success', icon: Check }
  }
})
const requestRows = computed(() => {
  const exec = disclosure.value
  if (!exec) return []
  return [
    exec.component ? { label: 'Component', value: exec.component } : undefined,
    { label: 'Relative cwd', value: exec.workdir || '.' },
    exec.timeoutSeconds !== undefined ? { label: 'Timeout', value: `${exec.timeoutSeconds}s` } : undefined,
    exec.authorityProfile ? { label: 'Authority', value: exec.authorityProfile } : undefined,
    exec.networkProfile ? { label: 'Network', value: exec.networkProfile } : undefined,
    exec.writebackPolicy ? { label: 'Writeback', value: exec.writebackPolicy } : undefined,
  ].filter((row): row is { label: string; value: string } => Boolean(row))
})
</script>

<template>
  <div v-if="disclosure && variant === 'approval'" class="mt-2 rounded-lg border border-border-subtle bg-surface-raised/70 p-2.5 text-[11px] leading-4 text-text-secondary">
    <div class="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-text-muted">
      <Terminal class="h-3.5 w-3.5 text-accent" :stroke-width="1.75" aria-hidden="true" />
      Command execution
    </div>

    <div v-if="command" class="mb-2 min-w-0">
      <div class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-text-muted">Command</div>
      <code class="block max-h-20 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border-subtle bg-surface px-2 py-1.5 font-mono text-[11px] text-text-primary">{{ command }}</code>
    </div>

    <div v-if="disclosure.argv?.length" class="mb-2 min-w-0">
      <div class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-text-muted">Sanitized argv</div>
      <div class="flex flex-wrap gap-1" aria-label="Sanitized argv">
        <code
          v-for="(token, index) in disclosure.argv"
          :key="`${index}-${token}`"
          class="max-w-full break-all rounded border border-border-subtle bg-surface px-1.5 py-0.5 font-mono text-[11px] text-text-primary"
        >{{ token }}</code>
      </div>
    </div>

    <dl v-if="requestRows.length" class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
      <template v-for="row in requestRows" :key="row.label">
        <dt class="font-medium text-text-muted">{{ row.label }}</dt>
        <dd class="min-w-0 break-words font-mono text-text-primary">{{ row.value }}</dd>
      </template>
    </dl>

  </div>

  <div v-else-if="disclosure" class="mt-1 rounded-lg border border-border-default bg-surface-raised/55 px-3 py-2.5 text-[11px] text-text-secondary">
    <div class="font-medium text-text-muted">Direct command</div>
    <div v-if="command" class="mt-2 whitespace-pre-wrap break-words font-mono text-[11px] leading-5 text-text-secondary">
      {{ command }}
    </div>

    <div v-if="disclosure.argv?.length" class="mt-2 min-w-0">
      <div class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-text-muted">Sanitized argv</div>
      <div class="flex flex-wrap gap-1" aria-label="Sanitized argv">
        <code
          v-for="(token, index) in disclosure.argv"
          :key="`${index}-${token}`"
          class="max-w-full break-all rounded border border-border-subtle bg-surface px-1.5 py-0.5 font-mono text-[11px] text-text-primary"
        >{{ token }}</code>
      </div>
    </div>

    <dl v-if="activityMetadata.length" class="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-[10px] text-text-muted">
      <template v-for="row in activityMetadata" :key="row.label">
        <dt>{{ row.label }}</dt>
        <dd class="min-w-0 break-words font-mono text-text-secondary">{{ row.value }}</dd>
      </template>
    </dl>

    <section v-if="disclosure.stdout?.length" class="mt-2" aria-label="Standard output">
      <div class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-text-muted">stdout</div>
      <pre class="max-h-48 overflow-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-5 text-text-secondary">{{ disclosure.stdout.join('\n') }}</pre>
    </section>
    <section v-if="disclosure.stderr?.length" class="mt-2" aria-label="Standard error">
      <div class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-danger">stderr</div>
      <pre class="max-h-48 overflow-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-5 text-text-secondary">{{ disclosure.stderr.join('\n') }}</pre>
    </section>
    <div v-if="!disclosure.stdout?.length && !disclosure.stderr?.length" class="mt-2 font-mono text-[11px] leading-5 text-text-muted">No output</div>
    <div v-if="outputLabel" class="mt-2 text-[11px] text-text-muted">{{ outputLabel }}</div>
    <div v-if="disclosure.detail || disclosure.detailURL" class="mt-2 text-[11px] leading-5 text-text-secondary">
      <span v-if="disclosure.detail">{{ disclosure.detail }}</span>
      <a
        v-if="disclosure.detailURL"
        class="ml-1 inline-flex items-center gap-1 font-medium text-accent hover:text-accent-hover"
        :href="disclosure.detailURL"
        target="_blank"
        rel="noreferrer"
      >
        Details
        <ExternalLink class="h-3 w-3" :stroke-width="2" aria-hidden="true" />
      </a>
    </div>
    <div class="mt-3 flex items-center justify-between gap-3 text-[11px] text-text-muted">
      <span>{{ [durationLabel, exitLabel].filter(Boolean).join(' · ') }}</span>
      <span
        class="inline-flex items-center gap-1.5 font-medium"
        :class="{
          'text-success': activityStatus.tone === 'success',
          'text-danger': activityStatus.tone === 'danger',
          'text-warning': activityStatus.tone === 'attention',
          'text-accent': activityStatus.tone === 'running',
        }"
      >
        <component :is="activityStatus.icon" class="h-3.5 w-3.5" :class="activityStatus.tone === 'running' ? 'animate-spin motion-reduce:animate-none' : ''" :stroke-width="1.75" aria-hidden="true" />
        {{ activityStatus.label }}
      </span>
    </div>
  </div>
</template>
