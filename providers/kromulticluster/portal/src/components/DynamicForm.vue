<script setup lang="ts">
// DynamicForm renders a JSON-schema-shaped object into a flat list of
// labeled inputs. Supported field types: string, integer, number,
// boolean, plus enum (rendered as a <select>). Out of scope v1:
// nested objects, arrays, oneOf/anyOf, $ref. Server-side validation
// is authoritative; this form only catches the obvious cases so the
// submit roundtrip doesn't fail for simple typos.
//
// The form uses two-way binding through a single `values` prop that
// the parent owns. v-model:values keeps the parent the source of
// truth so navigating away and back re-hydrates the form.

import { computed } from 'vue'
import type { JSONSchema } from '../types'

const props = defineProps<{ schema: JSONSchema; values: Record<string, unknown> }>()
const emit = defineEmits<{ (e: 'update:values', v: Record<string, unknown>): void }>()

interface Field {
  name: string
  type: string
  required: boolean
  description?: string
  enum?: unknown[]
  minimum?: number
  maximum?: number
}

const fields = computed<Field[]>(() => {
  const out: Field[] = []
  const props2 = props.schema?.properties || {}
  const required = new Set(props.schema?.required || [])
  for (const [name, spec] of Object.entries(props2)) {
    out.push({
      name,
      type: spec.type || 'string',
      required: required.has(name),
      description: spec.description,
      enum: spec.enum,
      minimum: spec.minimum,
      maximum: spec.maximum,
    })
  }
  // Stable order: required first, then alphabetic. Keeps the form
  // visually predictable across renders even if the schema's key
  // order shifts (it can — Go maps don't guarantee order).
  out.sort((a, b) => {
    if (a.required !== b.required) return a.required ? -1 : 1
    return a.name.localeCompare(b.name)
  })
  return out
})

function update(name: string, value: unknown) {
  emit('update:values', { ...props.values, [name]: value })
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
</script>

<template>
  <div class="dynform">
    <div v-for="f in fields" :key="f.name" class="dynform-row">
      <label>
        <span class="dynform-label">{{ f.name }}<span v-if="f.required" class="required">*</span></span>
        <span v-if="f.description" class="dynform-desc">{{ f.description }}</span>
      </label>
      <select
        v-if="f.enum"
        :value="values[f.name] ?? ''"
        @change="update(f.name, ($event.target as HTMLSelectElement).value)"
      >
        <option v-for="opt in f.enum" :key="String(opt)" :value="opt">{{ opt }}</option>
      </select>
      <input
        v-else-if="f.type === 'boolean'"
        type="checkbox"
        :checked="!!values[f.name]"
        @change="update(f.name, ($event.target as HTMLInputElement).checked)"
      />
      <input
        v-else
        :type="inputType(f.type)"
        :value="values[f.name] ?? ''"
        :min="f.minimum"
        :max="f.maximum"
        @input="update(f.name, coerce(f.type, ($event.target as HTMLInputElement).value))"
      />
    </div>
  </div>
</template>
