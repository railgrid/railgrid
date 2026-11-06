import { isMap, isScalar, isSeq, parseDocument, type Node, type Scalar } from 'yaml'

const MAX_DOCUMENT_BYTES = 32 * 1024
const MAX_DESCRIPTION_BYTES = 1024
const MAX_NAME_BYTES = 64
const MAX_RESOURCE_COUNT = 64
const MAX_RESOURCE_BYTES = 64 * 1024
const MAX_PACKAGE_BYTES = 512 * 1024

export interface ParsedSkillDocument {
  name: string
  description: string
  body: string
}

function bytes(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

function splitFrontmatter(document: string): { frontmatter: string; body: string } {
  const source = document.replace(/^\uFEFF/u, '')
  const firstEnd = source.indexOf('\n')
  if (firstEnd < 0 || source.slice(0, firstEnd).replace(/\r$/u, '').trim() !== '---') {
    throw new Error('SKILL.md must start with a YAML frontmatter delimiter (---).')
  }

  const frontmatterStart = firstEnd + 1
  for (let lineStart = frontmatterStart; lineStart <= source.length;) {
    let lineEnd = source.indexOf('\n', lineStart)
    if (lineEnd < 0) lineEnd = source.length
    const line = source.slice(lineStart, lineEnd).replace(/\r$/u, '').trim()
    if (line === '---') {
      const bodyStart = lineEnd < source.length ? lineEnd + 1 : lineEnd
      return { frontmatter: source.slice(frontmatterStart, lineStart), body: source.slice(bodyStart) }
    }
    if (lineEnd >= source.length) break
    lineStart = lineEnd + 1
  }

  throw new Error('SKILL.md frontmatter needs a closing --- delimiter.')
}

function isYamlStringScalar(node: Node | null): node is Scalar<string> {
  return isScalar(node)
    && typeof node.value === 'string'
    && (node.tag === undefined || node.tag === 'tag:yaml.org,2002:str')
}

function scalarString(node: Node | null): string {
  if (!isScalar(node) || node.value === null || node.tag === 'tag:yaml.org,2002:null') return ''
  if (typeof node.value === 'string') return node.value
  // yaml.v3 keeps the scalar's decoded source text for non-string scalar tags.
  return node.source ?? String(node.value)
}

function trimYamlSpace(value: string): string {
  // Match Go's strings.TrimSpace / unicode.IsSpace used by the backend.
  return value.replace(/^[\u0009-\u000d \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+|[\u0009-\u000d \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+$/gu, '')
}

function yamlNodeNonEmpty(node: Node | null): boolean {
  if (!node) return false
  if (isScalar(node)) return node.value !== null && trimYamlSpace(scalarString(node)) !== ''
  if (isMap(node) || isSeq(node)) return node.items.length > 0
  // yaml.v3 considers alias nodes empty because their Content is empty.
  return false
}

/** Parse the constrained SKILL.md frontmatter fields shared with the SDK. */
export function parseSkillDocument(document: string): ParsedSkillDocument {
  if (bytes(document) > MAX_DOCUMENT_BYTES) throw new Error(`SKILL.md must be ${MAX_DOCUMENT_BYTES / 1024} KiB or smaller.`)
  const { frontmatter, body } = splitFrontmatter(document)
  const parsed = parseDocument(frontmatter, { version: '1.2', uniqueKeys: true, merge: true })
  if (parsed.errors.length) {
    const duplicateKey = parsed.errors.some((error) => error.code === 'DUPLICATE_KEY')
    throw new Error(duplicateKey
      ? 'SKILL.md frontmatter contains duplicate YAML mapping keys.'
      : `SKILL.md frontmatter contains invalid YAML: ${parsed.errors[0].message}`)
  }

  if (!isMap(parsed.contents)) throw new Error('SKILL.md YAML frontmatter must be a mapping.')
  const fields = new Map<string, Node | null>()
  for (const pair of parsed.contents.items) {
    if (!isYamlStringScalar(pair.key)) throw new Error('SKILL.md frontmatter keys must be strings.')
    const key = pair.key.value
    if (fields.has(key)) throw new Error(`SKILL.md frontmatter repeats “${key}”.`)
    fields.set(key, pair.value)
  }

  for (const field of ['context', 'agent', 'model']) {
    if (yamlNodeNonEmpty(fields.get(field) ?? null)) throw new Error(`SKILL.md cannot define “${field}”.`)
  }

  const name = trimYamlSpace(scalarString(fields.get('name') ?? null))
  const rawDescription = scalarString(fields.get('description') ?? null)
  const description = trimYamlSpace(rawDescription)
  if (!name) throw new Error('SKILL.md needs a non-empty name field.')
  if (!description) throw new Error('SKILL.md needs a non-empty description field.')
  if (/[\\/\p{Cc}]/u.test(name)) throw new Error('Skill name cannot contain path separators or control characters.')
  if (bytes(name) > MAX_NAME_BYTES) throw new Error(`Skill name must be ${MAX_NAME_BYTES} bytes or shorter.`)
  if (bytes(description) > MAX_DESCRIPTION_BYTES) throw new Error(`Skill description must be ${MAX_DESCRIPTION_BYTES} bytes or shorter.`)
  if (/[\p{Cc}]/u.test(rawDescription)) throw new Error('Skill description must be a single line without control characters.')
  return { name, description, body }
}

export function createSkillDocument(name: string, description: string, body: string): string {
  return `---\nname: ${JSON.stringify(name)}\ndescription: ${JSON.stringify(description)}\n---\n${body}`
}

export interface SkillResourceDocument { name: string; content: string }

/** Validate client-side bounds before writing a workspace Skill CR. */
export function validateSkillResources(document: string, resources: SkillResourceDocument[]): SkillResourceDocument[] {
  if (resources.length > MAX_RESOURCE_COUNT) throw new Error(`A skill can have at most ${MAX_RESOURCE_COUNT} supporting resources.`)
  let total = bytes(document)
  const names = new Set<string>()
  const clean = resources.map((resource) => ({ name: resource.name.trim(), content: resource.content }))
    .filter((resource) => resource.name || resource.content)
  for (const resource of clean) {
    const path = resource.name
    const parts = path.split('/')
    if (!path || path.startsWith('/') || path.includes('\\') || path.includes('\0') || /^[A-Za-z]:/u.test(path) || parts.some((part) => !part || part === '.' || part === '..')) {
      throw new Error('Supporting resource paths must be clean relative paths.')
    }
    if (path.length > 1024) throw new Error('Supporting resource paths must be 1024 characters or shorter.')
    if (path === 'SKILL.md' || path.endsWith('/SKILL.md')) throw new Error('SKILL.md is the instructions file; supporting resources need another path.')
    if (names.has(path)) throw new Error(`Supporting resource “${path}” appears twice.`)
    names.add(path)
    const size = bytes(resource.content)
    if (size > MAX_RESOURCE_BYTES) throw new Error(`Supporting resource “${path}” must be 64 KiB or smaller.`)
    if (resource.content.includes('\0')) throw new Error(`Supporting resource “${path}” contains a null character.`)
    total += size
  }
  if (total > MAX_PACKAGE_BYTES) throw new Error('The skill package must be 512 KiB or smaller.')
  return clean
}

export function skillPackageName(name: string): string {
  const normalized = name.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 63).replace(/-+$/g, '')
  return normalized || 'skill'
}
