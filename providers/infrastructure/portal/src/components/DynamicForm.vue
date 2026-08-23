<script setup lang="ts">
// DynamicForm renders a JSON-schema-shaped object into labeled inputs.
// Supported leaf types: string, integer, number, boolean, plus enum
// (rendered as a <select>). Nested objects (type: object with properties)
// are rendered as a labeled group via recursion. Out of scope: arrays,
// oneOf/anyOf, $ref. Server-side validation is authoritative; this form
// only catches the obvious cases so the submit roundtrip doesn't fail for
// simple typos.
//
// Platform-computed fields (those whose description begins with "Computed by
// the platform") are hidden: the controller injects them (e.g. expose.fqdn,
// credentialsSecretName) and a tenant must not set them.
//
// Two-way binding flows through a single `values` prop the parent owns
// (v-model:values), so navigating away and back re-hydrates the form. The
// component references itself for nested objects (Vue infers the name from
// the filename).

import { computed } from 'vue'
import type { JSONSchema } from '../types'

const props = withDefaults(defineProps<{
  schema: JSONSchema
  values: Record<string, unknown>
  // The recursive renderer passes the complete object path so every control
  // remains addressable even when sibling objects expose the same field.
  pathPrefix?: string[]
}>(), {
  pathPrefix: () => [],
})
const emit = defineEmits<{ (e: 'update:values', v: Record<string, unknown>): void }>()

// COMPUTED_PREFIX marks fields the platform fills in; they're never user input.
const COMPUTED_PREFIX = 'Computed by the platform'

interface Field {
  name: string
  type: string
  required: boolean
  description?: string
  enum?: unknown[]
  minimum?: number
  maximum?: number
  // Set for nested objects: the sub-schema rendered by a nested DynamicForm.
  nested?: JSONSchema
}

const fields = computed<Field[]>(() => {
  const out: Field[] = []
  const props2 = props.schema?.properties || {}
  const required = new Set(props.schema?.required || [])
  for (const [name, spec] of Object.entries(props2)) {
    // Hide platform-computed fields (controller-injected; not tenant input).
    if ((spec.description || '').startsWith(COMPUTED_PREFIX)) continue
    const isObject = spec.type === 'object' && !!spec.properties
    out.push({
      name,
      type: spec.type || 'string',
      required: required.has(name),
      description: spec.description,
      enum: spec.enum,
      minimum: spec.minimum,
      maximum: spec.maximum,
      nested: isObject ? { type: 'object', properties: spec.properties, required: spec.required } : undefined,
    })
  }
  // Stable order: required first, then alphabetic. Keeps the form visually
  // predictable across renders even if the schema's key order shifts.
  out.sort((a, b) => {
    if (a.required !== b.required) return a.required ? -1 : 1
    return a.name.localeCompare(b.name)
  })
  return out
})

function update(name: string, value: unknown) {
  emit('update:values', { ...props.values, [name]: value })
}

// nestedValues returns the sub-object for a nested field (an empty object when
// unset), so the nested DynamicForm always gets a defined `values` prop.
function nestedValues(name: string): Record<string, unknown> {
  const v = props.values[name]
  return v && typeof v === 'object' ? (v as Record<string, unknown>) : {}
}

function inputType(t: string): string {
  switch (t) {
    case 'integer':
    case 'number':
      return 'number'
    case 'boolean':
      return 'checkbox'
    default:
      return 'text'
  }
}

function coerce(t: string, raw: string | boolean): unknown {
  if (t === 'integer') return raw === '' ? '' : parseInt(raw as string, 10)
  if (t === 'number') return raw === '' ? '' : parseFloat(raw as string)
  if (t === 'boolean') return Boolean(raw)
  return raw
}

function encodePathSegment(segment: string): string {
  // A plain replace turns `a-b` and `a b` into the same DOM id. Encode each
  // segment and include its encoded length so both sanitization and path
  // boundaries remain unambiguous (`a--b` cannot collide with `a`, `-b`).
  const encoded = encodeURIComponent(segment)
  return `${encoded.length}_${encoded}`
}

function fieldID(name: string): string {
  const path = [...props.pathPrefix, name]
  return `infrastructure-field-${path.map(encodePathSegment).join('__')}`
}

function descriptionID(name: string): string {
  return `${fieldID(name)}-description`
}
</script>

<template>
  <div class="dynform">
    <template v-for="f in fields" :key="f.name">
      <!-- Nested object: labeled group rendered by a nested DynamicForm. -->
      <fieldset v-if="f.nested" class="dynform-group">
        <legend>{{ f.name }}<span v-if="f.required" class="required">*</span></legend>
        <span v-if="f.description" class="dynform-desc">{{ f.description }}</span>
        <DynamicForm
          :schema="f.nested"
          :values="nestedValues(f.name)"
          :path-prefix="[...pathPrefix, f.name]"
          @update:values="v => update(f.name, v)"
        />
      </fieldset>

      <!-- Leaf field. -->
      <div v-else class="dynform-row">
        <label :for="fieldID(f.name)">
          <span class="dynform-label">{{ f.name }}<span v-if="f.required" class="required">*</span></span>
          <span v-if="f.description" :id="descriptionID(f.name)" class="dynform-desc">{{ f.description }}</span>
        </label>
        <select
          v-if="f.enum"
          :id="fieldID(f.name)"
          class="k-input"
          :aria-describedby="f.description ? descriptionID(f.name) : undefined"
          :value="values[f.name] ?? ''"
          @change="update(f.name, ($event.target as HTMLSelectElement).value)"
        >
          <option v-for="opt in f.enum" :key="String(opt)" :value="opt">{{ opt }}</option>
        </select>
        <input
          v-else-if="f.type === 'boolean'"
          :id="fieldID(f.name)"
          :aria-describedby="f.description ? descriptionID(f.name) : undefined"
          type="checkbox"
          :checked="!!values[f.name]"
          @change="update(f.name, ($event.target as HTMLInputElement).checked)"
        />
        <input
          v-else
          :id="fieldID(f.name)"
          class="k-input"
          :aria-describedby="f.description ? descriptionID(f.name) : undefined"
          :type="inputType(f.type)"
          :value="values[f.name] ?? ''"
          :min="f.minimum"
          :max="f.maximum"
          @input="update(f.name, coerce(f.type, ($event.target as HTMLInputElement).value))"
        />
      </div>
    </template>
  </div>
</template>
