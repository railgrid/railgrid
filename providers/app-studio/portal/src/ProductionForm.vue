<script setup lang="ts">
import { computed, watch } from 'vue'
import {
  productionFieldID,
  fieldLabel,
  arrayInputValue,
  arrayInputValues,
  renameMapKey,
  visibleProductionProperties,
  validateProductionValues,
  type JSONSchema,
  type ProductionFormValues,
} from './productionForm'

const props = withDefaults(defineProps<{
  schema: JSONSchema | null
  values: ProductionFormValues
  imageInputs?: string[]
  disabled?: boolean
  immutablePaths?: string[]
  existingProduction?: boolean
  pathPrefix?: string
}>(), {
  imageInputs: () => [],
  disabled: false,
  immutablePaths: () => [],
  existingProduction: false,
  pathPrefix: '',
})

const emit = defineEmits<{
  (event: 'update:values', values: ProductionFormValues): void
  (event: 'validity', valid: boolean): void
}>()

const fields = computed(() => visibleProductionProperties(props.schema, props.imageInputs))
const issues = computed(() => validateProductionValues(props.schema, props.values, props.imageInputs))

watch(issues, (current) => emit('validity', current.length === 0), { immediate: true })

function fullPath(name: string): string {
  return props.pathPrefix ? `${props.pathPrefix}.${name}` : name
}

function fieldIssues(name: string): string[] {
  const path = name
  return issues.value.filter((issue) => issue.path === path || issue.path.startsWith(`${path}.`)).map((issue) => issue.message)
}

function fieldRequired(name: string): boolean {
  return props.schema?.required?.includes(name) ?? false
}

function fieldImmutable(name: string): boolean {
  if (!props.existingProduction) return false
  const path = fullPath(name)
  return props.immutablePaths.some((immutable) => path === immutable || path.startsWith(`${immutable}.`))
}

function fieldDisabled(name: string): boolean {
  return props.disabled || fieldImmutable(name)
}

function update(path: string[], value: unknown) {
  const next = { ...props.values }
  let cursor: Record<string, unknown> = next
  for (const segment of path.slice(0, -1)) {
    const existing = cursor[segment]
    const child = existing && typeof existing === 'object' && !Array.isArray(existing)
      ? { ...(existing as Record<string, unknown>) }
      : {}
    cursor[segment] = child
    cursor = child
  }
  cursor[path[path.length - 1]] = value
  emit('update:values', next)
}

function nestedValues(path: string[]): Record<string, unknown> {
  let cursor: unknown = props.values
  for (const segment of path) cursor = cursor && typeof cursor === 'object' ? (cursor as Record<string, unknown>)[segment] : undefined
  return cursor && typeof cursor === 'object' && !Array.isArray(cursor) ? cursor as Record<string, unknown> : {}
}

function scalarValue(path: string[]): unknown {
  return nestedValues(path.slice(0, -1))[path[path.length - 1]]
}

function inputType(field: JSONSchema): string {
  if (field.type === 'integer' || field.type === 'number') return 'number'
  return 'text'
}

function coerce(field: JSONSchema, raw: string): unknown {
  if (field.type === 'integer' || field.type === 'number') return raw === '' ? '' : Number(raw)
  return raw
}

function updateMapValue(path: string[], key: string, value: string) {
  const current = { ...nestedValues(path) }
  current[key] = value
  update(path, current)
}

function renameMapEntry(path: string[], oldKey: string, newKey: string) {
  update(path, renameMapKey(nestedValues(path), oldKey, newKey))
}

function mapEntries(path: string[]): Array<[string, unknown]> {
  return Object.entries(nestedValues(path))
}

function addMapEntry(path: string[]) {
  const current = nestedValues(path)
  let key = 'KEY'
  let suffix = 1
  while (Object.prototype.hasOwnProperty.call(current, key)) key = `KEY_${suffix++}`
  const next = { ...current, [key]: '' }
  update(path, next)
}

function removeMapEntry(path: string[], key: string) {
  const current = { ...nestedValues(path) }
  delete current[key]
  update(path, current)
}

function hasProperties(field: JSONSchema): boolean {
  return field.type === 'object' && !!field.properties
}

function isMap(field: JSONSchema): boolean {
  return field.type === 'object' && !field.properties && !!field.additionalProperties
}

function fieldDescriptionID(path: string[]): string | undefined {
  return props.schema && path.length ? `${productionFieldID(props.pathPrefix, path)}-description` : undefined
}

function inputID(path: string | string[]): string {
  return productionFieldID(props.pathPrefix, path)
}
</script>

<template>
  <div v-if="!schema" class="rounded-md border border-border-subtle bg-surface-overlay px-3 py-3 text-[12px] leading-5 text-text-muted" role="status">
    The selected template has not exposed its production inputs yet. Refresh to try again.
  </div>
  <div v-else-if="fields.length === 0" class="rounded-md border border-border-subtle bg-surface-overlay px-3 py-3 text-[12px] leading-5 text-text-muted" role="status">
    This template has no additional production inputs.
  </div>
  <div v-else class="grid gap-4" aria-label="Production inputs">
    <template v-for="([name, field]) in fields" :key="name">
      <fieldset v-if="hasProperties(field)" class="grid gap-3 rounded-md border border-border-subtle p-3">
        <legend class="px-1 text-[11px] font-semibold uppercase tracking-wide text-text-muted">{{ field.title || fieldLabel(name) }}</legend>
        <p v-if="field.description" class="text-[11px] leading-4 text-text-muted">{{ field.description }}</p>
        <ProductionForm
          :schema="field"
          :values="nestedValues([name])"
          :image-inputs="imageInputs"
          :disabled="disabled"
          :immutable-paths="immutablePaths"
          :existing-production="existingProduction"
          :path-prefix="fullPath(name)"
          @update:values="value => update([name], value)"
        />
        <p v-if="fieldIssues(name).length" class="text-[10px] text-danger" role="alert">{{ fieldIssues(name)[0] }}</p>
      </fieldset>

      <fieldset v-else-if="isMap(field)" class="grid gap-2 rounded-md border border-border-subtle p-3">
        <legend class="px-1 text-[11px] font-semibold uppercase tracking-wide text-text-muted">{{ field.title || fieldLabel(name) }}</legend>
        <p v-if="field.description" :id="fieldDescriptionID([name])" class="text-[11px] leading-4 text-text-muted">{{ field.description }}</p>
        <div v-for="([key, value]) in mapEntries([name])" :key="key" class="flex items-center gap-2">
          <label :for="inputID(`${name}.${key}.key`)" class="sr-only">{{ fieldLabel(name) }} key</label>
          <input
            :id="inputID(`${name}.${key}.key`)"
            :value="key"
            class="w-36 rounded-md border border-border-subtle bg-surface px-2.5 py-2 font-mono text-[12px] text-text-primary outline-none focus:border-accent/50 disabled:opacity-60"
            :disabled="fieldDisabled(name)"
            placeholder="KEY"
            @change="renameMapEntry([name], key, ($event.target as HTMLInputElement).value)"
          >
          <label :for="inputID(`${name}.${key}.value`)" class="sr-only">{{ fieldLabel(name) }} {{ key }} value</label>
          <input
            :id="inputID(`${name}.${key}.value`)"
            :value="value"
            :aria-describedby="fieldDescriptionID([name])"
            class="min-w-0 flex-1 rounded-md border border-border-subtle bg-surface px-2.5 py-2 font-mono text-[12px] text-text-primary outline-none focus:border-accent/50 disabled:opacity-60"
            :disabled="fieldDisabled(name)"
            placeholder="Value"
            @input="updateMapValue([name], key, ($event.target as HTMLInputElement).value)"
          >
          <button type="button" class="shrink-0 text-[11px] font-medium text-danger hover:underline disabled:opacity-50" :disabled="fieldDisabled(name)" @click="removeMapEntry([name], key)">Remove</button>
        </div>
        <p v-if="fieldImmutable(name)" class="text-[10px] text-text-muted">Locked after the first production deployment.</p>
        <p v-else-if="fieldIssues(name).length" class="text-[10px] text-danger" role="alert">{{ fieldIssues(name)[0] }}</p>
        <button type="button" class="justify-self-start text-left text-[11px] font-medium text-accent hover:underline disabled:opacity-50" :disabled="fieldDisabled(name)" @click="addMapEntry([name])">Add {{ fieldLabel(name) }} value</button>
      </fieldset>

      <div v-else-if="field.type === 'array'" class="grid gap-1.5">
        <label :for="inputID(name)" class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">{{ field.title || fieldLabel(name) }}</label>
        <p v-if="field.description" :id="fieldDescriptionID([name])" class="text-[11px] leading-4 text-text-muted">{{ field.description }}</p>
        <textarea
          :id="inputID(name)"
          :value="arrayInputValue(scalarValue([name]))"
          :aria-describedby="fieldDescriptionID([name])"
          rows="3"
          class="min-h-20 resize-y rounded-md border border-border-subtle bg-surface px-2.5 py-2 font-mono text-[12px] leading-5 text-text-primary outline-none focus:border-accent/50 disabled:opacity-60"
          :disabled="fieldDisabled(name)"
          placeholder="One value per line"
          @input="update([name], arrayInputValues(($event.target as HTMLTextAreaElement).value, field.items, imageInputs))"
        />
        <span class="text-[10px] text-text-muted">Enter one value per line.</span>
        <span v-if="fieldImmutable(name)" class="text-[10px] text-text-muted">Locked after the first production deployment.</span>
        <span v-else-if="fieldIssues(name).length" class="text-[10px] text-danger" role="alert">{{ fieldIssues(name)[0] }}</span>
      </div>

      <div v-else class="grid gap-1.5">
        <label :for="inputID(name)" class="text-[11px] font-semibold uppercase tracking-wide text-text-muted">{{ field.title || fieldLabel(name) }}</label>
        <p v-if="field.description" :id="fieldDescriptionID([name])" class="text-[11px] leading-4 text-text-muted">{{ field.description }}</p>
        <select
          v-if="field.enum?.length"
          :id="inputID(name)"
          :value="scalarValue([name]) ?? ''"
          :aria-describedby="fieldDescriptionID([name])"
          class="h-9 rounded-md border border-border-subtle bg-surface px-2.5 text-[13px] text-text-primary outline-none focus:border-accent/50 disabled:opacity-60"
          :required="fieldRequired(name)"
          :disabled="fieldDisabled(name)"
          @change="update([name], coerce(field, ($event.target as HTMLSelectElement).value))"
        >
          <option v-if="!fieldRequired(name)" value="">Use template default</option>
          <option v-for="option in field.enum" :key="String(option)" :value="option ?? ''">{{ option }}</option>
        </select>
        <label v-else-if="field.type === 'boolean'" class="flex items-center gap-2 text-[13px] text-text-primary">
          <input
            :id="inputID(name)"
            type="checkbox"
            :checked="Boolean(scalarValue([name]))"
            :aria-describedby="fieldDescriptionID([name])"
            class="h-4 w-4 accent-accent"
            :disabled="fieldDisabled(name)"
            @change="update([name], ($event.target as HTMLInputElement).checked)"
          >
          Use {{ fieldLabel(name) }}
        </label>
        <input
          v-else
          :id="inputID(name)"
          :type="inputType(field)"
          :value="scalarValue([name]) ?? ''"
          :aria-describedby="fieldDescriptionID([name])"
          class="h-9 rounded-md border border-border-subtle bg-surface px-2.5 text-[13px] text-text-primary outline-none focus:border-accent/50 disabled:opacity-60"
          :required="fieldRequired(name)"
          :disabled="fieldDisabled(name)"
          :min="field.minimum"
          :max="field.maximum"
          :minlength="field.minLength"
          :maxlength="field.maxLength"
          :pattern="field.pattern"
          @input="update([name], coerce(field, ($event.target as HTMLInputElement).value))"
        >
        <span v-if="fieldImmutable(name)" class="text-[10px] text-text-muted">Locked after the first production deployment.</span>
        <span v-else-if="fieldIssues(name).length" class="text-[10px] text-danger" role="alert">{{ fieldIssues(name)[0] }}</span>
      </div>
    </template>
  </div>
</template>
