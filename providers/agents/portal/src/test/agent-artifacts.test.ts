import { afterEach, describe, expect, it } from 'vitest'
import { parseAgentArtifactSources, parseSavedCaptureCitation } from '../agentArtifacts'
import { sanitizedMarkdownWithToolCitations } from '../vue/chat'

afterEach(() => document.body.replaceChildren())

describe('source and saved-result citations', () => {
  it('accepts only bounded server-attested citation envelopes', () => {
    const citation = { artifactID: 'capture-1', digest: 'a'.repeat(64), selection: { rowIndices: [0, 2], fields: ['name'] } }
    expect(parseSavedCaptureCitation(citation)).toEqual(citation)
    expect(parseSavedCaptureCitation({ ...citation, digest: 'not-a-digest' })).toBeNull()
    expect(parseSavedCaptureCitation({ ...citation, selection: { rowIndices: [0, 0], fields: ['name'] } })).toBeNull()
    expect(parseSavedCaptureCitation({ ...citation, unexpected: 'untrusted' })).toBeNull()
  })

  it('requires exact source UIDs and bounded recorded actions before offering a source citation', () => {
    const source = {
      label: 'Model',
      object: { group: 'models.test', version: 'v1', resource: 'models', name: 'sales', uid: 'model-uid' },
      access: [{ group: 'models.test', version: 'v1', resource: 'models', name: 'sales', verb: 'get' }],
    }
    expect(parseAgentArtifactSources([source])).toEqual([source])
    expect(parseAgentArtifactSources([{ ...source, object: { ...source.object, uid: '' } }])).toBeNull()
    expect(parseAgentArtifactSources([{ ...source, access: [{ ...source.access[0], verb: 'delete' }] }])).toEqual([
      { ...source, access: [{ ...source.access[0], verb: 'delete' }] },
    ])
  })

  it('keeps ordinary sources, hides app-backed duplicate markers, and disables unmatched tokens', () => {
    const html = sanitizedMarkdownWithToolCitations(
      'Ordinary [[tool:ordinary]] hidden [[tool:app]] saved [[tool:saved]] missing [[tool:unknown]]',
      [
        { toolCallID: 'ordinary', label: 'Lookup' },
        { toolCallID: 'app', label: 'Compile', hidden: true },
        { toolCallID: 'saved', label: 'Saved capture', kind: 'saved-capture' },
      ],
    )
    const content = document.createElement('div')
    content.innerHTML = html
    expect(content.querySelectorAll('[data-agent-tool-citation]')).toHaveLength(1)
    expect(content.querySelector('[data-agent-tool-citation]')?.getAttribute('data-agent-tool-citation')).toBe('ordinary')
    expect(content.querySelectorAll('[data-agent-saved-capture-citation]')).toHaveLength(1)
    expect(content.querySelector('[data-agent-saved-capture-citation]')?.getAttribute('data-agent-saved-capture-citation')).toBe('saved')
    expect(content.querySelector('.agents-tool-citation-unavailable')?.textContent).toBe('Source unavailable')
    expect(content.textContent).not.toContain('[[tool:')
    expect(content.textContent).toContain('hidden')
  })

  it('removes model-forged citation controls before inserting receipt-backed controls', () => {
    const content = document.createElement('div')
    content.innerHTML = sanitizedMarkdownWithToolCitations(
      '<button class="agents-tool-citation" data-agent-tool-citation="forged">Forged</button> [[tool:trusted]]',
      [{ toolCallID: 'trusted', label: 'Trusted source' }],
    )
    const buttons = [...content.querySelectorAll<HTMLButtonElement>('[data-agent-tool-citation]')]
    expect(buttons).toHaveLength(1)
    expect(buttons[0]?.dataset.agentToolCitation).toBe('trusted')
    expect(buttons[0]?.textContent).toContain('Trusted source')
  })
})
