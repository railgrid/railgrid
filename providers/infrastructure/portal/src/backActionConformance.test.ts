import { describe, expect, it } from 'vitest'

const sources = import.meta.glob('./views/{ProvisionPage,MissingCredentialsPage,InstanceDetailPage}.vue', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

describe('Infrastructure back-navigation conformance', () => {
  it('keeps page-level back actions intrinsic-width and on the shared recipe', () => {
    expect(Object.keys(sources)).toHaveLength(3)
    for (const source of Object.values(sources)) {
      expect(source).toContain('k-btn k-btn--ghost k-back-action')
    }
  })
})
