<script setup lang="ts">
// Rows of per-meter limits: the PlanLimit shape contrib-metering uses for a
// Plan's limits and an Entitlement's overrides. One editor for both, so the
// two forms in the Metering section cannot drift.
import { Plus, X } from 'lucide-vue-next'

export type Limit = { meter: string; shape: string; enforce: string; max: string }

const props = defineProps<{
  modelValue: Limit[]
  /** Prefix for the row controls' accessible names, e.g. "Override". */
  rowLabel: string
  idPrefix: string
  disabled?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: Limit[]] }>()

const shapes = [
  { value: 'Level', label: 'Level (current count)' },
  { value: 'Consumption', label: 'Consumption (summed)' },
]
const modes = [
  { value: 'Hard', label: 'Hard (deny over max)' },
  { value: 'Soft', label: 'Soft (flag over max)' },
  { value: 'Meter', label: 'Meter (measure only)' },
]

function update(index: number, patch: Partial<Limit>): void {
  emit('update:modelValue', props.modelValue.map((l, i) => (i === index ? { ...l, ...patch } : l)))
}
function add(): void {
  emit('update:modelValue', [...props.modelValue, { meter: '', shape: 'Level', enforce: 'Hard', max: '' }])
}
function remove(index: number): void {
  emit('update:modelValue', props.modelValue.filter((_, i) => i !== index))
}
</script>

<template>
  <div>
    <div
      v-for="(limit, i) in modelValue"
      :key="i"
      class="mb-2 flex flex-wrap items-end gap-2"
    >
      <div>
        <label :for="`${idPrefix}-meter-${i}`" class="block text-[11px] text-text-muted">Meter</label>
        <input
          :id="`${idPrefix}-meter-${i}`"
          :value="limit.meter"
          class="k-input mt-1 w-40 font-mono text-sm"
          placeholder="e.g. workspaces"
          :disabled="disabled"
          :aria-label="`${rowLabel} ${i + 1} meter`"
          required
          @input="update(i, { meter: ($event.target as HTMLInputElement).value.trim() })"
        />
      </div>
      <div>
        <label :for="`${idPrefix}-shape-${i}`" class="block text-[11px] text-text-muted">Shape</label>
        <select
          :id="`${idPrefix}-shape-${i}`"
          :value="limit.shape"
          class="k-input mt-1 w-44 text-sm"
          :disabled="disabled"
          :aria-label="`${rowLabel} ${i + 1} shape`"
          @change="update(i, { shape: ($event.target as HTMLSelectElement).value })"
        >
          <option v-for="s in shapes" :key="s.value" :value="s.value">{{ s.label }}</option>
        </select>
      </div>
      <div>
        <label :for="`${idPrefix}-enforce-${i}`" class="block text-[11px] text-text-muted">Enforce</label>
        <select
          :id="`${idPrefix}-enforce-${i}`"
          :value="limit.enforce"
          class="k-input mt-1 w-44 text-sm"
          :disabled="disabled"
          :aria-label="`${rowLabel} ${i + 1} enforcement`"
          @change="update(i, { enforce: ($event.target as HTMLSelectElement).value })"
        >
          <option v-for="m in modes" :key="m.value" :value="m.value">{{ m.label }}</option>
        </select>
      </div>
      <div>
        <label :for="`${idPrefix}-max-${i}`" class="block text-[11px] text-text-muted">
          Max<span v-if="limit.enforce === 'Meter'" class="text-text-muted"> (optional)</span>
        </label>
        <input
          :id="`${idPrefix}-max-${i}`"
          :value="limit.max"
          class="k-input mt-1 w-24 font-mono text-sm"
          inputmode="decimal"
          placeholder="e.g. 10"
          :disabled="disabled"
          :required="limit.enforce !== 'Meter'"
          :aria-label="`${rowLabel} ${i + 1} max`"
          @input="update(i, { max: ($event.target as HTMLInputElement).value.trim() })"
        />
      </div>
      <button
        type="button"
        class="k-btn k-btn--ghost px-2 py-1.5 text-xs disabled:opacity-50"
        :disabled="disabled"
        :aria-label="`Remove ${rowLabel.toLowerCase()} ${i + 1}`"
        @click="remove(i)"
      >
        <X class="h-3.5 w-3.5" :stroke-width="1.75" />
      </button>
    </div>
    <button
      type="button"
      class="k-btn k-btn--ghost px-2 py-1 text-xs disabled:opacity-50"
      :disabled="disabled"
      @click="add"
    >
      <Plus class="h-3.5 w-3.5" :stroke-width="1.75" />
      Add {{ rowLabel.toLowerCase() }}
    </button>
  </div>
</template>
