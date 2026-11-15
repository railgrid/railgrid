// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies.
// Unique within a document, including independently loaded provider bundles.
const sequenceKey = Symbol.for('railgrid.portalkit.document-id.sequence')
const shared = globalThis as typeof globalThis & { [key: symbol]: unknown }

export function nextDocumentID(): string {
  const next = Number(shared[sequenceKey] ?? 0) + 1
  shared[sequenceKey] = next
  return `railgrid-${next}`
}

// Only for client-rendered apps. SSR must provide the same unique prefix to
// server render and hydration rather than allocate one independently here.
export function configureClientAppIDs(app: { config: { idPrefix?: string } }): void {
  app.config.idPrefix = nextDocumentID()
}
