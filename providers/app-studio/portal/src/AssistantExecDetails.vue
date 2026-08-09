<script setup lang="ts">
import { computed } from 'vue'
import { ExternalLink, Terminal } from 'lucide-vue-next'
import {
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
const outputLabel = computed(() => disclosure.value?.outputTruncated ? 'Output truncated to the latest bounded lines.' : '')
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
  <div v-if="disclosure" class="mt-2 rounded-lg border border-border-subtle bg-surface-raised/70 p-2.5 text-[11px] leading-4 text-text-secondary">
    <div v-if="variant === 'approval'" class="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-text-muted">
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

    <dl v-if="variant === 'activity' && (exitLabel || durationLabel)" class="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 border-t border-border-subtle/70 pt-2">
      <template v-if="exitLabel">
        <dt class="font-medium text-text-muted">Status</dt>
        <dd class="font-mono text-text-primary">{{ exitLabel }}</dd>
      </template>
      <template v-if="durationLabel">
        <dt class="font-medium text-text-muted">Duration</dt>
        <dd class="font-mono text-text-primary">{{ durationLabel }}</dd>
      </template>
    </dl>

    <div v-if="variant === 'activity' && disclosure.stdout?.length" class="mt-2">
      <div class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-text-muted">stdout</div>
      <pre class="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border-subtle bg-surface px-2 py-1.5 font-mono text-[11px] leading-4 text-text-secondary">{{ disclosure.stdout.join('\n') }}</pre>
    </div>
    <div v-if="variant === 'activity' && disclosure.stderr?.length" class="mt-2">
      <div class="mb-1 text-[10px] font-semibold uppercase tracking-wide text-danger">stderr</div>
      <pre class="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md border border-danger/20 bg-danger-subtle/30 px-2 py-1.5 font-mono text-[11px] leading-4 text-danger">{{ disclosure.stderr.join('\n') }}</pre>
    </div>
    <div v-if="variant === 'activity' && outputLabel" class="mt-1 text-[11px] text-text-muted">{{ outputLabel }}</div>
    <div v-if="variant === 'activity' && (disclosure.summary || disclosure.detail || disclosure.detailURL)" class="mt-2 border-t border-border-subtle/70 pt-2 text-[11px] leading-5 text-text-secondary">
      <span v-if="disclosure.summary || disclosure.detail">{{ disclosure.detail || disclosure.summary }}</span>
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
  </div>
</template>
