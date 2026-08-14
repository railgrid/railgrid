const RESOURCE_NAME = /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/

/** Kubernetes resource names are user-facing identifiers, not display text. */
export function resourceNameError(value: string, label = 'name'): string | null {
  if (!value) return `${label} is required.`
  if (value.trim() !== value) return `${label} cannot start or end with whitespace.`
  if (value.length > 253) return `${label} must be 253 characters or fewer.`
  if (!RESOURCE_NAME.test(value)) {
    return `${label} must use lowercase letters, numbers, and hyphens, and start and end with a letter or number.`
  }
  return null
}
