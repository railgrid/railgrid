// Pure helpers for the admin area's System services and Workspace seeds
// sections. Kept free of imports so the node tests can run them as they are.

export type Tone = 'success' | 'warning' | 'danger' | 'muted'
export interface Status { label: string; tone: Tone }

// What the hub reports for a system service's workspace. A False condition is
// either a refusal the admin has to act on (a reserved name, a workspace the
// hub does not own) or a provisioning error it is retrying; both read as
// "not ready", with the hub's message beside it.
export function systemWorkspaceStatus(row: { ready?: unknown; reason?: unknown }): Status {
  if (row.ready === true) return { label: 'Ready', tone: 'success' }
  if (row.reason === 'NameReserved' || row.reason === 'WorkspaceNotOwned') return { label: 'Refused', tone: 'danger' }
  if (row.reason) return { label: 'Not ready', tone: 'danger' }
  return { label: 'Provisioning', tone: 'warning' }
}

// accepted is 'True', 'False', or '' while the hub has not looked at the
// current spec. Accepted means the hub applies the seed to tenant workspaces;
// it does not say how many have been reached.
export function seedStatus(seed: { accepted?: unknown }): Status {
  if (seed.accepted === 'True') return { label: 'Accepted', tone: 'success' }
  if (seed.accepted === 'False') return { label: 'Refused', tone: 'danger' }
  return { label: 'Pending', tone: 'warning' }
}

// Kubernetes object names: what the hub will accept for a system service
// (one DNS label, it becomes a workspace path segment) and for a seed.
const dnsLabel = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/
export const reservedSystemNames = ['controllers', 'providers', 'tenants']

export function systemWorkspaceNameError(name: string): string {
  if (!name) return ''
  if (!dnsLabel.test(name)) return 'Use lowercase letters, digits and hyphens, starting and ending with a letter or digit (63 characters at most).'
  if (reservedSystemNames.includes(name)) return `“${name}” is one of the hub’s own system workspaces.`
  return ''
}

export function seedNameError(name: string): string {
  if (!name) return ''
  return dnsLabel.test(name) ? '' : 'Use lowercase letters, digits and hyphens, starting and ending with a letter or digit (63 characters at most).'
}

// A starting point for a new seed. exportPath is the system service's
// workspace when the editor was opened from one, and the name is that
// workspace's last segment; both are placeholders to edit otherwise.
export function starterSeedSpec(exportPath = '', exportName = ''): string {
  const path = exportPath || 'root:railgrid:system:my-service'
  const service = path.split(':').pop() || 'my-service'
  const name = exportName || `${service}.example.com`
  return `# Applied to EVERY tenant workspace once saved and accepted.
bindings:
- name: ${service}                # the APIBinding's name in each workspace
  export:
    path: ${path}
    name: ${name}       # the APIExport's name in that workspace
  tenantAccess: ReadOnly          # ReadWrite | ReadOnly | None
  # permissionClaims must match what the export declares, exactly.
  # A claim on secrets or configmaps needs a selector.
  # permissionClaims:
  # - group: ""
  #   resource: secrets
  #   verbs: ["get", "list", "watch"]
  #   selector:
  #     matchLabels:
  #       example.com/owned: "true"
objects: []
# - resource: widgets             # plural of the manifest's kind
#   whenBound:                    # optional: only where this export is bound too
#   - path: root:railgrid:providers:agents
#     name: agents.railgrid.ai
#   manifest:
#     apiVersion: example.com/v1alpha1
#     kind: Widget
#     metadata: {name: default, namespace: default}
#     spec: {}
`
}

// The paths of the workspaces a seed binds exports from, deduplicated, in the
// order they appear.
export function seedExportPaths(seed: { bindings?: { exportPath: string }[] }): string[] {
  const out: string[] = []
  for (const b of seed.bindings ?? []) if (b.exportPath && !out.includes(b.exportPath)) out.push(b.exportPath)
  return out
}

// The system service whose workspace is at path, if the hub provisioned one.
export function systemWorkspaceAt<T extends { workspacePath: string }>(path: string, workspaces: T[]): T | undefined {
  return workspaces.find(ws => ws.workspacePath === path)
}
