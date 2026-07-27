export interface AssistantTraceSummaryItem {
  label: string
  tool?: string
}

const collapsedTraceItemLimit = 3
const collapsedTraceLabelLimit = 80

export function parseAssistantTraceHeader(value: string, explicitTool?: string): AssistantTraceSummaryItem {
  const label = value.trim()
  const tool = explicitTool?.trim()
  if (tool) return { label, tool }

  const separator = label.lastIndexOf(' · ')
  if (separator === -1) return { label }
  const candidate = label.slice(separator + 3).trim()
  if (!/^[A-Za-z0-9_.:-]+$/.test(candidate)) return { label }
  return {
    label: label.slice(0, separator).trim(),
    tool: candidate,
  }
}

export function summarizeAssistantTrace(items: AssistantTraceSummaryItem[]): string {
  const labels = items
    .map((item) => boundedTraceLabel(item.label))
    .filter(Boolean)
  if (labels.length === 0) return ''

  const visible = labels.slice(0, collapsedTraceItemLimit).join(' · ')
  const remaining = labels.length - collapsedTraceItemLimit
  return remaining > 0 ? `${visible} · ${remaining} more` : visible
}

function boundedTraceLabel(value: string): string {
  const normalized = value.trim().replace(/\s+/g, ' ')
  if (normalized.length <= collapsedTraceLabelLimit) return normalized
  return `${normalized.slice(0, collapsedTraceLabelLimit - 3).trimEnd()}...`
}
