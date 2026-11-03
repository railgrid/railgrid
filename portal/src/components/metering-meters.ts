// Human-readable context for Railgrid's built-in meters. Unknown provider
// meters remain editable by their stable ID without guessing their units.
export const meteringMeters = [
  { id: 'workspaces', label: 'Workspaces', unit: 'workspaces', description: 'Current number of workspaces.' },
  { id: 'request_cost_rpm', label: 'API request cost', unit: 'cost units / min', description: 'Weighted API request cost per minute.' },
  { id: 'organizations', label: 'Organizations', unit: 'organizations', description: 'Current number of organizations.' },
] as const

export function meterDefinition(id: string) { return meteringMeters.find(meter => meter.id === id) }
export function meterLabel(id: string): string { return meterDefinition(id)?.label || id }
