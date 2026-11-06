import { describe, expect, it, vi } from 'vitest'
import { loadSkillLibrary, loadSkillLibraryDetail } from '../skill-library'
import type { RailgridContext } from '../types'

const document = '---\nname: Review\ndescription: Review evidence\n---\nCheck the evidence.'
const workspace = { metadata: { name: 'review-package', resourceVersion: '3' }, spec: { document, resources: [{ name: 'checklist.md', content: 'Check permissions.' }] } }
function context(providerStatus = 200) {
  const fetch = vi.fn(async (url: RequestInfo | URL) => {
    const path = String(url)
    if (path === '/api/providers') return new Response(JSON.stringify({ items: [{ name: 'code', hub: { assistantSkills: [{ packageName: 'review', version: '1', skill: document }] } }] }), { status: providerStatus })
    if (path.includes('/skills/review-package')) return new Response(JSON.stringify(workspace))
    if (path.includes('/skills')) return new Response(JSON.stringify({ items: [workspace, { metadata: { name: 'broken' }, spec: { document: 'invalid' } }] }))
    throw new Error(`Unexpected request ${path}`)
  })
  return { ctx: { tenant: 'cluster-a', fetch } as unknown as RailgridContext, fetch }
}

describe('workspace skill library', () => {
  it('loads source packages without reading or requiring any Agent', async () => {
    const { ctx, fetch } = context()
    const result = await loadSkillLibrary(ctx)
    expect(result.skills.map(skill => skill.id)).toEqual(['workspace:review-package', 'system:providers/code/review'])
    expect(result.skills[0]).toMatchObject({ name: 'Review', editable: true, enabled: false })
    expect(result.skills[1]).toMatchObject({ editable: false, enabled: false })
    expect(result.warnings).toContain('Workspace skill “broken” is invalid or exceeds its bounds.')
    expect(fetch.mock.calls.some(([url]) => String(url).includes('/agents/'))).toBe(false)
  })

  it('keeps workspace management available when provider discovery fails', async () => {
    const { ctx } = context(503)
    const result = await loadSkillLibrary(ctx)
    expect(result.skills).toHaveLength(1)
    expect(result.warnings).toContain('Provider skills could not be loaded. Retry to refresh the library.')
  })

  it('reads details using the source object name rather than the display name', async () => {
    const { ctx, fetch } = context()
    const detail = await loadSkillLibraryDetail(ctx, 'workspace:review-package')
    expect(detail.instructions).toBe('Check the evidence.')
    expect(detail.resources?.[0].content).toBe('Check permissions.')
    expect(fetch).toHaveBeenCalledOnce()
    expect(String(fetch.mock.calls[0][0])).toContain('/skills/review-package')
  })
})
