import type { TopLevelSpec } from 'vega-lite'
import type { ChatMessage, ToolCall } from './types'

export type ChartCell = string | number | boolean | null
export interface VisualizationChart {
  title: string
  description?: string
  kind: 'bar' | 'line' | 'area' | 'scatter' | 'pie'
  data: Record<string, ChartCell>[]
  x: string
  y: string
  series?: string
  xType: 'nominal' | 'temporal' | 'quantitative'
  source?: string
}
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const boundedText = (value: unknown, max: number): value is string => typeof value === 'string' && Array.from(value).length <= max
const field = (value: unknown): value is string => boundedText(value, 128) && !!value.trim() && !/[.\[\]\\\p{Cc}]/u.test(value) && !['__proto__', 'constructor', 'prototype'].includes(value)
const numeric = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value) && Math.abs(value) <= Number.MAX_SAFE_INTEGER
const scalar = (value: unknown): value is ChartCell => value === null || typeof value === 'boolean' || numeric(value) || boundedText(value, 4096)
function validTime(value: unknown): boolean {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))?$/.test(value)) return false
  const day = new Date(`${value.slice(0, 10)}T00:00:00Z`)
  return Number.isFinite(day.getTime()) && day.toISOString().slice(0, 10) === value.slice(0, 10) && Number.isFinite(Date.parse(value))
}

/** Only this provider's successful chart tool can create an inline chart. */
export function parseVisualization(tool: ToolCall): VisualizationChart | null {
  if (tool.name !== 'visualize_data' || tool.pending || tool.error || !tool.result || tool.result.length > 525312) return null
  if (new TextEncoder().encode(tool.result).byteLength > 525312) return null
  try {
    const envelope: unknown = JSON.parse(tool.result)
    if (!record(envelope) || envelope.type !== 'railgrid.visualization' || envelope.version !== 1 || !record(envelope.chart)) return null
    const c = envelope.chart
    if (Object.keys(c).some(k => !['title', 'description', 'kind', 'data', 'x', 'y', 'series', 'xType', 'source'].includes(k))) return null
    if (!boundedText(c.title, 160) || !c.title.trim() || (typeof c.kind !== 'string' || !['bar', 'line', 'area', 'scatter', 'pie'].includes(c.kind))) return null
    if (c.description !== undefined && !boundedText(c.description, 1000)) return null
    if (c.source !== undefined && !boundedText(c.source, 512)) return null
    if (!field(c.x) || !field(c.y) || (c.series !== undefined && !field(c.series))) return null
    if ((typeof c.xType !== 'string' || !['nominal', 'temporal', 'quantitative'].includes(c.xType))) return null
    if (!Array.isArray(c.data) || !c.data.length || c.data.length > 1000) return null
    const fields = new Set<string>()
    let xKind: string | undefined, seriesKind: string | undefined
    let positive = false
    for (const row of c.data) {
      if (!record(row) || !Object.keys(row).length || Object.keys(row).length > 16) return null
      for (const [key, value] of Object.entries(row)) {
        if (!field(key) || !scalar(value)) return null
        fields.add(key)
      }
      if (fields.size > 16 || !Object.hasOwn(row, c.x) || row[c.x] === null || !numeric(row[c.y])) return null
      const kind = typeof row[c.x]
      if (xKind && kind !== xKind) return null
      xKind = kind
      if (c.xType === 'quantitative' && !numeric(row[c.x])) return null
      if (c.xType === 'temporal' && !validTime(row[c.x])) return null
      if (c.series !== undefined) {
        const value = row[c.series as string]
        if (value === undefined || value === null || (seriesKind && typeof value !== seriesKind)) return null
        seriesKind = typeof value
      }
      if (c.kind === 'pie' && (row[c.y] as number) < 0) return null
      positive ||= (row[c.y] as number) > 0
    }
    if (c.kind === 'scatter' && c.xType !== 'quantitative') return null
    if (c.kind === 'pie' && (c.series !== undefined || c.xType !== 'nominal' || !positive)) return null
    return c as unknown as VisualizationChart
  } catch { return null }
}

export function messageVisualizations(message: ChatMessage): Array<{ id: string; chart: VisualizationChart }> {
  if (message.role !== 'assistant') return []
  const tools = [...message.tools, ...(message.progress?.trace.flatMap(block => block.kind === 'tool' ? [block.tool] : []) || [])]
  const results = new Map<string, VisualizationChart>()
  for (const tool of tools) {
    const chart = parseVisualization(tool)
    if (chart) results.set(tool.id, chart)
  }
  return [...results].map(([id, chart]) => ({ id, chart }))
}

export interface ChartColors { text: string; grid: string; accent: string; palette: string[] }
/** Compile a closed chart contract, never arbitrary model-authored Vega. */
export function visualizationSpec(chart: VisualizationChart, width: number, colors: ChartColors): TopLevelSpec {
  const values = chart.data.map(row => ({ x: row[chart.x], y: row[chart.y], series: chart.series ? row[chart.series] : undefined }))
  const color = chart.series ? { field: 'series', type: 'nominal' as const, title: chart.series } : { value: colors.accent }
  const dateOnlyTemporal = chart.xType === 'temporal' && chart.data.every(row => typeof row[chart.x] === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(row[chart.x] as string))
  const x = { field: 'x', type: chart.xType, title: chart.x, ...(chart.xType === 'nominal' ? { sort: null } : {}), ...(chart.xType === 'temporal' ? { scale: { type: 'utc' as const }, ...(dateOnlyTemporal ? { axis: { format: '%Y-%m-%d' } } : {}) } : {}) }
  return {
    $schema: 'https://vega.github.io/schema/vega-lite/v6.json',
    description: chart.description || chart.title,
    width: Math.max(120, Math.floor(width)), height: 250,
    autosize: { type: 'fit', contains: 'padding', resize: true },
    data: { values },
    mark: chart.kind === 'pie' ? { type: 'arc' } : chart.kind === 'scatter' ? { type: 'point', filled: true } : chart.kind === 'line' ? { type: 'line', point: true } : { type: chart.kind },
    encoding: chart.kind === 'pie'
      ? { theta: { field: 'y', type: 'quantitative', title: chart.y }, color: { field: 'x', type: 'nominal', title: chart.x } }
      : { x, y: { field: 'y', type: 'quantitative', title: chart.y }, color, ...(chart.kind === 'bar' && chart.series ? { xOffset: { field: 'series' } } : {}) },
    config: {
      background: 'transparent', view: { stroke: null },
      font: 'sans-serif',
      axis: { labelColor: colors.text, titleColor: colors.text, domainColor: colors.grid, tickColor: colors.grid, gridColor: colors.grid, labelLimit: 120 },
      legend: { labelColor: colors.text, titleColor: colors.text, orient: 'bottom' },
      range: { category: colors.palette },
    },
  } as TopLevelSpec
}
