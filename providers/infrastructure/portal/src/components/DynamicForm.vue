<script setup lang="ts">
// Create-form renderer: server-owned/read-only fields are omitted. Scalar
// arrays use one value per line, additionalProperties maps use key/value rows,
// and only complex arrays retain a JSON fallback.

import { computed, nextTick, ref, watch } from 'vue'
import { Plus, X } from 'lucide-vue-next'
import type { JSONSchema } from '../types'

const props = withDefaults(defineProps<{
  schema: JSONSchema
  values: Record<string, unknown>
  pathPrefix?: string[]
}>(), { pathPrefix: () => [] })
const emit = defineEmits<{ (e: 'update:values', v: Record<string, unknown>): void }>()

const COMPUTED_PREFIX = 'Computed by the platform'

interface Field {
  name: string
  type: string
  required: boolean
  description?: string
  enum?: unknown[]
  schemaDefault?: unknown
  minimum?: number
  maximum?: number
  minLength?: number
  maxLength?: number
  pattern?: string
  minItems?: number
  maxItems?: number
  items?: JSONSchema
  additionalProperties?: boolean | JSONSchema
  nested?: JSONSchema
}

interface MapRow { id: number, key: string, rawValue: string }

const rootElement = ref<HTMLElement | null>(null)
const arrayDrafts = ref<Record<string, string>>({})
const mapDrafts = ref<Record<string, MapRow[]>>({})
const fieldErrors = ref<Record<string, string>>({})
let mapRowSerial = 0

const fields = computed<Field[]>(() => {
  const out: Field[] = []
  const required = new Set(props.schema?.required || [])
  for (const [name, spec] of Object.entries(props.schema?.properties || {})) {
    if (spec.readOnly || (spec.description || '').startsWith(COMPUTED_PREFIX)) continue
    if (spec.type === 'object' && !spec.properties && spec.additionalProperties === false) continue
    out.push({
      name,
      type: spec.type || 'string',
      required: required.has(name),
      description: spec.description,
      enum: spec.enum,
      schemaDefault: spec.default,
      minimum: spec.minimum,
      maximum: spec.maximum,
      minLength: spec.minLength,
      maxLength: spec.maxLength,
      pattern: spec.pattern,
      minItems: spec.minItems,
      maxItems: spec.maxItems,
      items: spec.items,
      additionalProperties: spec.additionalProperties,
      nested: spec.type === 'object' && !!spec.properties ? spec : undefined,
    })
  }
  out.sort((a, b) => a.required !== b.required ? (a.required ? -1 : 1) : a.name.localeCompare(b.name))
  return out
})

function update(name: string, value: unknown) {
  const next = { ...props.values }
  if (value === undefined) delete next[name]
  else next[name] = value
  emit('update:values', next)
}

function nestedValues(name: string): Record<string, unknown> {
  const value = props.values[name]
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function inputType(type: string | undefined): string {
  return type === 'integer' || type === 'number' ? 'number' : 'text'
}

function coerce(type: string, raw: string | boolean): unknown {
  if (type === 'integer') return raw === '' ? '' : parseInt(raw as string, 10)
  if (type === 'number') return raw === '' ? '' : parseFloat(raw as string)
  if (type === 'boolean') return Boolean(raw)
  return raw
}

function encodePathSegment(segment: string): string {
  const encoded = encodeURIComponent(segment)
  return `${encoded.length}_${encoded}`
}

function fieldID(name: string): string {
  return `infrastructure-field-${[...props.pathPrefix, name].map(encodePathSegment).join('__')}`
}
function descriptionID(name: string): string { return `${fieldID(name)}-description` }
function errorID(name: string): string { return `${fieldID(name)}-error` }
function arrayHintID(name: string): string { return `${fieldID(name)}-line-hint` }
function mapLabelID(name: string): string { return `${fieldID(name)}-label` }
function mapKeyID(field: Field, row: MapRow): string { return `${fieldID(field.name)}-map-${row.id}-key` }
function mapValueID(field: Field, row: MapRow): string { return `${fieldID(field.name)}-map-${row.id}-value` }

function describedBy(field: Field, includeArrayHint = false): string | undefined {
  const ids: string[] = []
  if (field.description) ids.push(descriptionID(field.name))
  if (includeArrayHint) ids.push(arrayHintID(field.name))
  if (fieldErrors.value[field.name]) ids.push(errorID(field.name))
  return ids.length ? ids.join(' ') : undefined
}

function isScalarSchema(schema: JSONSchema | undefined): boolean {
  return !!schema && (!!schema.enum || ['string', 'integer', 'number', 'boolean'].includes(schema.type || ''))
}
function isScalarArray(field: Field): boolean { return field.type === 'array' && isScalarSchema(field.items) }
function isComplexArray(field: Field): boolean { return field.type === 'array' && !isScalarArray(field) }
function isMapField(field: Field): boolean {
  return field.type === 'object' && !field.nested && field.additionalProperties !== false
}
function mapValueSchema(field: Field): JSONSchema | undefined {
  return typeof field.additionalProperties === 'object' ? field.additionalProperties : undefined
}

function arrayText(field: Field): string {
  if (Object.prototype.hasOwnProperty.call(arrayDrafts.value, field.name)) return arrayDrafts.value[field.name]
  const value = props.values[field.name] ?? field.schemaDefault ?? []
  return Array.isArray(value) ? value.map(item => String(item)).join('\n') : ''
}

function complexArrayJSON(field: Field): string {
  if (Object.prototype.hasOwnProperty.call(arrayDrafts.value, field.name)) return arrayDrafts.value[field.name]
  return JSON.stringify(props.values[field.name] ?? field.schemaDefault ?? [], null, 2)
}

function valueType(value: unknown): string {
  if (Array.isArray(value)) return 'array'
  if (value === null) return 'null'
  return typeof value
}

function validateSchemaValue(schema: JSONSchema | undefined, value: unknown, path = 'value'): string | null {
  if (!schema) return null
  const type = schema.type
  if (type === 'array') {
    if (!Array.isArray(value)) return `${path} must be an array.`
    if (schema.minItems !== undefined && value.length < schema.minItems) return `${path} needs at least ${schema.minItems} item(s).`
    if (schema.maxItems !== undefined && value.length > schema.maxItems) return `${path} allows at most ${schema.maxItems} item(s).`
    for (let index = 0; index < value.length; index += 1) {
      const error = validateSchemaValue(schema.items, value[index], `${path} line ${index + 1}`)
      if (error) return error
    }
    return null
  }
  if (type === 'object') {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return `${path} must be an object.`
    const record = value as Record<string, unknown>
    for (const required of schema.required ?? []) {
      if (record[required] === undefined || record[required] === '') return `${path}.${required} is required.`
    }
    for (const [name, child] of Object.entries(schema.properties ?? {})) {
      if (record[name] === undefined) continue
      const error = validateSchemaValue(child, record[name], `${path}.${name}`)
      if (error) return error
    }
    return null
  }
  if (type === 'integer' && !Number.isInteger(value)) return `${path} must be an integer.`
  if (type === 'number' && typeof value !== 'number') return `${path} must be a number.`
  if (type === 'boolean' && typeof value !== 'boolean') return `${path} must be true or false.`
  if (type === 'string' && typeof value !== 'string') return `${path} must be a string.`
  if (schema.enum && !schema.enum.some(option => JSON.stringify(option) === JSON.stringify(value))) return `${path} must be one of the allowed values.`
  if (typeof value === 'number') {
    if (schema.minimum !== undefined && value < schema.minimum) return `${path} must be at least ${schema.minimum}.`
    if (schema.maximum !== undefined && value > schema.maximum) return `${path} must be at most ${schema.maximum}.`
  }
  if (typeof value === 'string') {
    if (schema.minLength !== undefined && value.length < schema.minLength) return `${path} needs at least ${schema.minLength} character(s).`
    if (schema.maxLength !== undefined && value.length > schema.maxLength) return `${path} allows at most ${schema.maxLength} character(s).`
    if (schema.pattern) {
      try {
        if (!new RegExp(schema.pattern).test(value)) return `${path} does not match the required format.`
      } catch {
        return `${path} has an invalid schema pattern.`
      }
    }
  }
  if (type && type !== valueType(value) && !(type === 'integer' && typeof value === 'number')) return `${path} must be ${type}.`
  return null
}

function parseScalar(schema: JSONSchema | undefined, raw: string, path: string): { value?: unknown, error?: string } {
  let value: unknown = raw
  if (schema?.type === 'integer' || schema?.type === 'number') {
    const expected = schema.type === 'integer' ? 'an integer' : 'a number'
    if (!raw.trim()) return { error: `${path} must be ${expected}.` }
    value = Number(raw)
    if (!Number.isFinite(value)) return { error: `${path} must be ${expected}.` }
  } else if (schema?.type === 'boolean') {
    if (raw === 'true') value = true
    else if (raw === 'false') value = false
    else return { error: `${path} must be true or false.` }
  } else if (schema?.enum && !schema.type) {
    value = schema.enum.find(option => String(option) === raw) ?? raw
  }
  const error = validateSchemaValue(schema, value, path)
  return error ? { error } : { value }
}

function setFieldError(field: Field, error: string, target?: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement) {
  target?.setCustomValidity(error)
  const next = { ...fieldErrors.value }
  if (error) next[field.name] = error
  else delete next[field.name]
  fieldErrors.value = next
}

function updateScalarArray(field: Field, target: HTMLTextAreaElement) {
  const raw = target.value
  arrayDrafts.value = { ...arrayDrafts.value, [field.name]: raw }
  const lines = raw.split(/\r?\n/).map(line => line.trim()).filter(Boolean)
  const values: unknown[] = []
  let error = ''
  for (let index = 0; index < lines.length; index += 1) {
    const parsed = parseScalar(field.items, lines[index], `${field.name} line ${index + 1}`)
    if (parsed.error) { error = parsed.error; break }
    values.push(parsed.value)
  }
  if (!error) error = validateSchemaValue({ type: 'array', items: field.items, minItems: field.minItems, maxItems: field.maxItems }, values, field.name) ?? ''
  setFieldError(field, error, target)
  if (!error) update(field.name, values)
}

function updateComplexArray(field: Field, target: HTMLTextAreaElement) {
  const raw = target.value
  arrayDrafts.value = { ...arrayDrafts.value, [field.name]: raw }
  if (!raw.trim() && !field.required) {
    setFieldError(field, '', target)
    update(field.name, undefined)
    return
  }
  let error = ''
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
    error = validateSchemaValue({ type: 'array', items: field.items, minItems: field.minItems, maxItems: field.maxItems }, parsed, field.name) ?? ''
  } catch { error = `${field.name} must contain valid JSON.` }
  setFieldError(field, error, target)
  if (!error) update(field.name, parsed)
}

function rawMapValue(schema: JSONSchema | undefined, value: unknown): string {
  if (schema && !isScalarSchema(schema)) return JSON.stringify(value)
  return String(value ?? '')
}

function initialMapRows(field: Field): MapRow[] {
  const source = props.values[field.name] ?? field.schemaDefault ?? {}
  if (!source || typeof source !== 'object' || Array.isArray(source)) return []
  const schema = mapValueSchema(field)
  return Object.entries(source as Record<string, unknown>).map(([key, value]) => ({ id: ++mapRowSerial, key, rawValue: rawMapValue(schema, value) }))
}

watch(fields, current => {
  const next = { ...mapDrafts.value }
  let changed = false
  for (const field of current) {
    if (!isMapField(field) || Object.prototype.hasOwnProperty.call(next, field.name)) continue
    next[field.name] = initialMapRows(field)
    changed = true
  }
  if (changed) mapDrafts.value = next
}, { immediate: true })

function mapRows(field: Field): MapRow[] { return mapDrafts.value[field.name] ?? [] }

function syncMapValidity(field: Field, error: string) {
  void nextTick(() => rootElement.value
    ?.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(`[data-map-field="${fieldID(field.name)}"]`)
    .forEach(control => control.setCustomValidity(error)))
}

function commitMap(field: Field) {
  const output: Record<string, unknown> = {}
  const seen = new Set<string>()
  const schema = mapValueSchema(field)
  let error = ''
  for (const row of mapRows(field)) {
    const key = row.key.trim()
    if (!key) { error = `Enter a key for every ${field.name} entry.`; break }
    if (seen.has(key)) { error = `${field.name} contains the duplicate key “${key}”.`; break }
    seen.add(key)
    let parsed: { value?: unknown, error?: string }
    if (schema && !isScalarSchema(schema)) {
      try {
        const value = JSON.parse(row.rawValue)
        const nestedError = validateSchemaValue(schema, value, `${field.name}.${key}`)
        parsed = nestedError ? { error: nestedError } : { value }
      } catch { parsed = { error: `${field.name}.${key} must contain valid JSON.` } }
    } else parsed = parseScalar(schema, row.rawValue, `${field.name}.${key}`)
    if (parsed.error) { error = parsed.error; break }
    output[key] = parsed.value
  }
  setFieldError(field, error)
  syncMapValidity(field, error)
  if (!error) update(field.name, output)
}

function addMapRow(field: Field) {
  const row: MapRow = { id: ++mapRowSerial, key: '', rawValue: '' }
  mapDrafts.value = { ...mapDrafts.value, [field.name]: [...mapRows(field), row] }
  commitMap(field)
  void nextTick(() => rootElement.value?.querySelector<HTMLInputElement>(`[id="${mapKeyID(field, row)}"]`)?.focus())
}

function updateMapRow(field: Field, row: MapRow, part: 'key' | 'value', value: string) {
  mapDrafts.value = {
    ...mapDrafts.value,
    [field.name]: mapRows(field).map(candidate => candidate.id === row.id ? { ...candidate, [part === 'key' ? 'key' : 'rawValue']: value } : candidate),
  }
  commitMap(field)
}

function removeMapRow(field: Field, row: MapRow) {
	const current = mapRows(field)
	const removedIndex = current.findIndex(candidate => candidate.id === row.id)
	const remaining = current.filter(candidate => candidate.id !== row.id)
	mapDrafts.value = { ...mapDrafts.value, [field.name]: remaining }
	commitMap(field)
	void nextTick(() => {
		const successor = remaining[Math.min(Math.max(removedIndex, 0), remaining.length - 1)]
		if (successor) {
			rootElement.value?.querySelector<HTMLInputElement>(`[id="${mapKeyID(field, successor)}"]`)?.focus()
			return
		}
		rootElement.value?.querySelector<HTMLButtonElement>(`[data-map-add="${fieldID(field.name)}"]`)?.focus()
	})
}
</script>

<template>
  <div ref="rootElement" class="dynform">
    <template v-for="field in fields" :key="field.name">
      <fieldset v-if="field.nested" class="dynform-group">
        <legend>{{ field.name }}<span v-if="field.required" class="required">*</span></legend>
        <span v-if="field.description" class="dynform-desc">{{ field.description }}</span>
        <DynamicForm :schema="field.nested" :values="nestedValues(field.name)" :path-prefix="[...pathPrefix, field.name]" @update:values="value => update(field.name, value)" />
      </fieldset>

      <div v-else-if="isMapField(field)" class="dynform-row">
        <div :id="mapLabelID(field.name)" class="dynform-label">{{ field.name }}<span v-if="field.required" class="required">*</span></div>
        <span v-if="field.description" :id="descriptionID(field.name)" class="dynform-desc">{{ field.description }}</span>
        <div class="dynform-map" role="group" :aria-labelledby="mapLabelID(field.name)" :aria-describedby="describedBy(field)">
          <div v-if="mapRows(field).length" class="dynform-map-head" aria-hidden="true"><span>Key</span><span>Value</span><span /></div>
          <div v-for="row in mapRows(field)" :key="row.id" class="dynform-map-row">
            <label class="dynform-sr-only" :for="mapKeyID(field, row)">Key for {{ field.name }}</label>
            <input :id="mapKeyID(field, row)" :data-map-field="fieldID(field.name)" class="k-input" :value="row.key" placeholder="Key" required :aria-invalid="fieldErrors[field.name] ? 'true' : undefined" @input="updateMapRow(field, row, 'key', ($event.target as HTMLInputElement).value)" />
            <label class="dynform-sr-only" :for="mapValueID(field, row)">Value for {{ field.name }} key {{ row.key || 'new entry' }}</label>
            <select v-if="mapValueSchema(field)?.enum" :id="mapValueID(field, row)" :data-map-field="fieldID(field.name)" class="k-input" :value="row.rawValue" :aria-invalid="fieldErrors[field.name] ? 'true' : undefined" @change="updateMapRow(field, row, 'value', ($event.target as HTMLSelectElement).value)">
              <option v-for="option in mapValueSchema(field)?.enum" :key="String(option)" :value="String(option)">{{ option }}</option>
            </select>
            <select v-else-if="mapValueSchema(field)?.type === 'boolean'" :id="mapValueID(field, row)" :data-map-field="fieldID(field.name)" class="k-input" :value="row.rawValue" :aria-invalid="fieldErrors[field.name] ? 'true' : undefined" @change="updateMapRow(field, row, 'value', ($event.target as HTMLSelectElement).value)">
              <option value="true">true</option><option value="false">false</option>
            </select>
            <textarea v-else-if="mapValueSchema(field) && !isScalarSchema(mapValueSchema(field))" :id="mapValueID(field, row)" :data-map-field="fieldID(field.name)" class="k-input dynform-map-json" :value="row.rawValue" rows="2" spellcheck="false" :aria-invalid="fieldErrors[field.name] ? 'true' : undefined" @input="updateMapRow(field, row, 'value', ($event.target as HTMLTextAreaElement).value)" />
            <input v-else :id="mapValueID(field, row)" :data-map-field="fieldID(field.name)" class="k-input" :type="inputType(mapValueSchema(field)?.type)" :value="row.rawValue" placeholder="Value" :min="mapValueSchema(field)?.minimum" :max="mapValueSchema(field)?.maximum" :minlength="mapValueSchema(field)?.minLength" :maxlength="mapValueSchema(field)?.maxLength" :pattern="mapValueSchema(field)?.pattern" :aria-invalid="fieldErrors[field.name] ? 'true' : undefined" @input="updateMapRow(field, row, 'value', ($event.target as HTMLInputElement).value)" />
            <button class="k-icon-action dynform-map-remove" type="button" :aria-label="`Remove ${field.name} key ${row.key || 'new entry'}`" :data-k-tip="`Remove ${field.name} key ${row.key || 'new entry'}`" @click="removeMapRow(field, row)"><X :size="14" :stroke-width="1.75" aria-hidden="true" /></button>
          </div>
		  <button class="k-btn k-btn--ghost dynform-map-add" type="button" :data-map-add="fieldID(field.name)" @click="addMapRow(field)"><Plus :size="14" :stroke-width="1.75" aria-hidden="true" /> Add entry</button>
        </div>
        <span v-if="fieldErrors[field.name]" :id="errorID(field.name)" class="dynform-error" role="alert">{{ fieldErrors[field.name] }}</span>
      </div>

      <div v-else class="dynform-row">
        <label :for="fieldID(field.name)">
          <span class="dynform-label">{{ field.name }}<span v-if="field.required" class="required">*</span></span>
          <span v-if="field.description" :id="descriptionID(field.name)" class="dynform-desc">{{ field.description }}</span>
          <span v-if="isScalarArray(field)" :id="arrayHintID(field.name)" class="dynform-desc">Enter one item per line.</span>
        </label>
        <textarea v-if="isScalarArray(field)" :id="fieldID(field.name)" class="k-input dynform-lines" :aria-describedby="describedBy(field, true)" :aria-invalid="fieldErrors[field.name] ? 'true' : undefined" :required="field.required" :value="arrayText(field)" rows="4" spellcheck="false" @input="updateScalarArray(field, $event.target as HTMLTextAreaElement)" />
        <textarea v-else-if="isComplexArray(field)" :id="fieldID(field.name)" class="k-input dynform-json" :aria-describedby="describedBy(field)" :aria-invalid="fieldErrors[field.name] ? 'true' : undefined" :required="field.required" :value="complexArrayJSON(field)" rows="5" spellcheck="false" @input="updateComplexArray(field, $event.target as HTMLTextAreaElement)" />
        <select v-else-if="field.enum" :id="fieldID(field.name)" class="k-input" :aria-describedby="field.description ? descriptionID(field.name) : undefined" :required="field.required" :value="values[field.name] ?? ''" @change="update(field.name, ($event.target as HTMLSelectElement).value)">
          <option v-for="option in field.enum" :key="String(option)" :value="option">{{ option }}</option>
        </select>
        <input v-else-if="field.type === 'boolean'" :id="fieldID(field.name)" class="k-checkbox" :aria-describedby="field.description ? descriptionID(field.name) : undefined" type="checkbox" :checked="!!values[field.name]" @change="update(field.name, ($event.target as HTMLInputElement).checked)" />
        <input v-else :id="fieldID(field.name)" class="k-input" :aria-describedby="field.description ? descriptionID(field.name) : undefined" :type="inputType(field.type)" :required="field.required" :value="values[field.name] ?? ''" :min="field.minimum" :max="field.maximum" :minlength="field.minLength" :maxlength="field.maxLength" :pattern="field.pattern" @input="update(field.name, coerce(field.type, ($event.target as HTMLInputElement).value))" />
        <span v-if="fieldErrors[field.name]" :id="errorID(field.name)" class="dynform-error" role="alert">{{ fieldErrors[field.name] }}</span>
      </div>
    </template>
  </div>
</template>
