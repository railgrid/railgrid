import { resolve } from 'node:path'

/** Audit exports can exceed 400,000 lines. Only write them to a chosen destination. */
export function auditDirectory(directory = process.env.AUDIT_DIR) {
  if (!directory?.trim()) {
    throw new Error('Set AUDIT_DIR to a local audit bundle directory outside the checkout.')
  }
  return resolve(directory)
}
