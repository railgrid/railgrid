import { describe, expect, it } from 'vitest'
import { parseSkillDocument, validateSkillResources } from '../skill-document'

describe('workspace skill documents', () => {
  it('accepts standard scalar quoting, comments, and unrelated YAML fields', () => {
    const parsed = parseSkillDocument([
      '---',
      'name: "Release # review" # the heading',
      "description: 'Checks the release''s evidence' # short summary",
      'license: Apache-2.0',
      'metadata:',
      '  maintained-by: platform',
      '---',
      'Check the diff.',
    ].join('\n'))

    expect(parsed.name).toBe('Release # review')
    expect(parsed.description).toBe("Checks the release's evidence")
    expect(parsed.body).toBe('Check the diff.')
  })

  it('parses folded descriptions and quoted keys while preserving the Markdown body', () => {
    const parsed = parseSkillDocument([
      '---',
      '"name": release-review',
      '"description": >-',
      '  Check the current changes',
      '  and preserve evidence.',
      'metadata:',
      '  maintained-by: platform',
      '---',
      '',
      '# Review steps',
      '',
    ].join('\r\n'))

    expect(parsed.name).toBe('release-review')
    expect(parsed.description).toBe('Check the current changes and preserve evidence.')
    expect(parsed.body).toBe('\r\n# Review steps\r\n')
  })

  it('rejects YAML merge keys but accepts a quoted literal merge key', () => {
    for (const mergeKey of ['<<: {name: inherited}', '!!merge <<: {name: inherited}']) {
      expect(() => parseSkillDocument([
        '---',
        'name: review',
        'description: Review',
        mergeKey,
        '---',
      ].join('\n'))).toThrow('frontmatter keys must be strings')
    }

    const parsed = parseSkillDocument([
      '---',
      'name: review',
      'description: Review',
      '"<<": literal field',
      '---',
    ].join('\n'))

    expect(parsed.name).toBe('review')
    expect(parsed.description).toBe('Review')
  })

  it('matches backend authority, duplicate, null, and control validation', () => {
    expect(() => parseSkillDocument('---\nname: review\ndescription: Review\ncontext:\n  mode: trusted\n---\n')).toThrow('cannot define “context”')
    expect(() => parseSkillDocument('---\nname: review\ndescription: Review\nagent: [worker]\n---\n')).toThrow('cannot define “agent”')
    expect(() => parseSkillDocument('---\nname: review\n"name": another\ndescription: Review\n---\n')).toThrow('duplicate YAML mapping keys')
    expect(() => parseSkillDocument('---\nname: null\ndescription: Review\n---\n')).toThrow('non-empty name')
    expect(() => parseSkillDocument('---\nname: review\ndescription: null\n---\n')).toThrow('non-empty description')
    expect(parseSkillDocument('---\nname: review\ndescription: Review\ncontext: null\nagent: ~\nmodel: {}\n---\n').name).toBe('review')
    expect(() => parseSkillDocument('---\nname: "rev\\u0085iew"\ndescription: Review\n---\n')).toThrow('control characters')
    expect(() => parseSkillDocument('---\nname: review\ndescription: "line\\u0085value"\n---\n')).toThrow('single line')
  })

  it('rejects invalid supporting resource packages', () => {
    expect(() => validateSkillResources('---\nname: review\ndescription: Review\n---\n', [{ name: '../secret.txt', content: 'x' }])).toThrow('clean relative paths')
    expect(() => validateSkillResources('---\nname: review\ndescription: Review\n---\n', [{ name: 'notes.txt', content: 'a' }, { name: 'notes.txt', content: 'b' }])).toThrow('appears twice')
    expect(() => validateSkillResources('---\nname: review\ndescription: Review\n---\n', [{ name: 'notes.txt', content: 'x'.repeat(64 * 1024 + 1) }])).toThrow('64 KiB')
  })
})
