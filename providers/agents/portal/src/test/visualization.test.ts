import { describe, expect, it } from 'vitest'
import { parse as parseVega, View } from 'vega'
import { compile } from 'vega-lite'
import { messageVisualizations, parseVisualization, visualizationSpec, type VisualizationChart } from '../visualization'
import type { ChatMessage, ToolCall } from '../types'
const chart: VisualizationChart = { title: 'Units', kind: 'bar', x: 'Product', y: 'Units', xType: 'nominal', data: [{ Product: '<b>Bread</b>', Units: 12 }, { Product: 'Cake', Units: 8 }] }
const tool = (value: unknown = chart): ToolCall => ({ id: 'chart-1', name: 'visualize_data', args: '{}', pending: false, result: JSON.stringify({ type: 'railgrid.visualization', version: 1, chart: value }) })
const colors = { text: '#111', grid: '#ddd', accent: '#6141c9', palette: ['#6141c9'] }

async function renderChart(chart: VisualizationChart): Promise<string> {
  const spec = compile(visualizationSpec(chart, 300, colors)).spec
  const view = new View(parseVega(spec), { renderer: 'none' })
  try {
    await view.runAsync()
    return await view.toSVG()
  } finally {
    view.finalize()
  }
}

function svgText(svg: string): string[] {
  return [...svg.matchAll(/<text\b[^>]*>([\s\S]*?)<\/text>/g)].map(([, value]) => value.replace(/<[^>]*>/g, '').trim()).filter(Boolean)
}
describe('inline visualization contract', () => {
  it('preserves supplied data, titles, and text without treating them as markup', () => { expect(parseVisualization(tool())).toEqual(chart) })
  it('requires a completed successful builtin tool with a known envelope', () => {
    for (const change of [{ name: 'mcp_chart' }, { pending: true }, { error: 'failed' }, { result: '{}' }, { result: '{' }, { result: JSON.stringify({ type: 'railgrid.visualization', version: 2, chart }) }]) expect(parseVisualization({ ...tool(), ...change })).toBeNull()
  })
  it.each([
    { kind: ['bar'] }, { xType: ['nominal'] }, { url: 'https://example.com/data' },
    { data: [] }, { data: Array(1001).fill(chart.data[0]) },
    { data: [{ Product: 'Bread', Units: Number.MAX_SAFE_INTEGER + 1 }] },
    { data: [{ Product: 'Bread', Units: 12, 'a.b': 1 }] },
    { data: [{ Product: 'Bread', Units: [1] }] },
    { kind: 'pie', data: [{ Product: 'Bread', Units: -1 }] },
    { kind: 'pie', data: [{ Product: 'Bread', Units: 0 }] },
    { kind: 'scatter' },
    { xType: 'temporal', data: [{ Product: '2026-02-30', Units: 1 }] },
  ])('rejects invalid or unsupported chart parameters %j', change => { expect(parseVisualization(tool({ ...chart, ...change }))).toBeNull() })
  it('deduplicates live tools and persisted progress trace', () => {
    const message: ChatMessage = { id: 'turn', content: '', role: 'assistant', tools: [tool()], progress: { status: 'completed', trace: [{ kind: 'tool', id: 'trace', tool: tool() }] } }
    expect(messageVisualizations(message)).toHaveLength(1)
    expect(messageVisualizations({ ...message, tools: [] })).toEqual(messageVisualizations(message))
    expect(messageVisualizations({ ...message, role: 'user' })).toEqual([])
  })
  it('builds closed inline specs and uses UTC for dates without aggregation', () => {
    const spec = visualizationSpec(chart, 500, colors)
    expect(spec.data).toEqual({ values: [{ x: '<b>Bread</b>', y: 12, series: undefined }, { x: 'Cake', y: 8, series: undefined }] })
    expect(spec).toHaveProperty('encoding.x.sort', null)
    expect(spec).not.toHaveProperty('transform')
    expect(spec.data).not.toHaveProperty('url')
    const temporal = visualizationSpec({ ...chart, xType: 'temporal' }, 300, colors)
    expect(temporal).toHaveProperty('encoding.x.scale.type', 'utc')
  })

  it('uses UTC adaptive tick labels for timestamps and keeps date-only labels', async () => {
    const timestamps: VisualizationChart = {
      title: 'Hourly units', kind: 'line', x: 'Time', y: 'Units', xType: 'temporal',
      data: Array.from({ length: 8 }, (_, hour) => ({ Time: `2026-09-20T${String(hour).padStart(2, '0')}:00:00Z`, Units: hour + 1 })),
    }
    const timestampSpec = visualizationSpec(timestamps, 300, colors)
    expect(timestampSpec).toHaveProperty('encoding.x.scale.type', 'utc')
    expect(timestampSpec).not.toHaveProperty('encoding.x.axis.format')

    const previousTZ = process.env.TZ
    process.env.TZ = 'Pacific/Honolulu'
    try {
      const timestampLabels = svgText(await renderChart(timestamps))
      expect(timestampLabels).toContain('Sep 20')
      expect(timestampLabels).toContain('01 AM')
      expect(timestampLabels).toContain('02 AM')
      expect(timestampLabels).not.toContain('Sep 19')

      const dateOnly: VisualizationChart = {
        title: 'Daily units', kind: 'line', x: 'Day', y: 'Units', xType: 'temporal',
        data: [18, 19, 20].map(day => ({ Day: `2026-09-${day}`, Units: day })),
      }
      const dateSpec = visualizationSpec(dateOnly, 300, colors)
      expect(dateSpec).toHaveProperty('encoding.x.axis.format', '%Y-%m-%d')
      const dateLabels = svgText(await renderChart(dateOnly))
      expect(dateLabels).toContain('2026-09-18')
      expect(dateLabels).toContain('2026-09-19')
      expect(dateLabels).toContain('2026-09-20')
    } finally {
      if (previousTZ === undefined) delete process.env.TZ
      else process.env.TZ = previousTZ
    }
  })
})
