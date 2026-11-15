// Human-readable context for Railgrid's built-in meters. Unknown provider
// meters remain editable by their stable ID without guessing their units.
export const meteringMeters = [
  { id: 'workspaces', label: 'Workspaces', unit: 'workspaces', description: 'Current number of workspaces.' },
  { id: 'request_cost_rpm', label: 'API request cost', unit: 'cost units / min', description: 'Weighted API request cost per minute.' },
  { id: 'organizations', label: 'Organizations', unit: 'organizations', description: 'Current number of organizations.' },
  { id: 'agents', label: 'Agents', unit: 'agents', description: 'Current number of agents.' },
  { id: 'edges', label: 'Edges', unit: 'edges', description: 'Current number of edges.' },
  { id: 'projects', label: 'Projects', unit: 'projects', description: 'Current number of projects.' },
  { id: 'instances', label: 'Instances', unit: 'instances', description: 'Current number of instances.' },
  { id: 'repositories', label: 'Repositories', unit: 'repositories', description: 'Current number of repositories.' },
  { id: 'services', label: 'Services', unit: 'services', description: 'Current number of services.' },
  { id: 'greetings', label: 'Greetings', unit: 'greetings', description: 'Current number of greetings.' },
] as const

export function meterDefinition(id: string) { return meteringMeters.find(meter => meter.id === id) }
export function meterLabel(id: string): string { return meterDefinition(id)?.label || id }
