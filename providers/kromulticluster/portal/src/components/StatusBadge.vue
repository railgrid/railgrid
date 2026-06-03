<script setup lang="ts">
defineProps<{ phase: string }>()
// Map kro / kedge phase strings to one of three visual buckets. Unknown
// strings render in the neutral bucket so a kro upgrade that introduces
// new phases still shows them without breaking the UI.
const okPhases = new Set(['Ready', 'Active', 'Succeeded', 'True'])
const warnPhases = new Set(['Pending', 'Provisioning', 'Updating', 'Unknown'])
function cls(p: string): string {
  if (okPhases.has(p)) return 'ok'
  if (warnPhases.has(p)) return 'warn'
  return 'fail'
}
</script>

<template>
  <span :class="['badge', cls(phase)]">{{ phase }}</span>
</template>
