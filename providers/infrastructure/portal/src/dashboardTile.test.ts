import { describe, expect, it } from 'vitest'

const source = import.meta.glob('./DashboardTile.vue', { query: '?raw', import: 'default', eager: true })['./DashboardTile.vue'] as string

describe('Infrastructure dashboard tile navigation affordances', () => {
  it('keeps visible labels and renders Lucide arrows instead of glyph icons', () => {
    expect(source).toMatch(/import \{[^}]*ArrowRight[^}]*\} from 'lucide-vue-next'/)
    expect(source).toMatch(/Browse templates\s*<ArrowRight[^>]*aria-hidden="true"/)
    expect(source).toMatch(/Open Instances\s*<ArrowRight[^>]*aria-hidden="true"/)
    expect(source).not.toContain('Browse templates →')
    expect(source).not.toContain('Open Instances →')
    expect(source.match(/k-dashboard-action/g)).toHaveLength(4)
  })

  it('uses the shared serial poller and navigation dispatcher', () => {
    expect(source).toContain('createTilePoller(load)')
    expect(source).toContain('poller?.stop()')
    expect(source).toContain('navigateFromTile(rootRef,')
    expect(source).not.toContain('window.setInterval')
    expect(source).not.toContain('createLatestRefreshController')
  })
})
