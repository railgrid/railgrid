import { describe, expect, it } from 'vitest'
import { approvalDisclosure } from '../approval-disclosure'

describe('approval disclosure', () => {
  it('accepts an empty object for a no-argument tool', () => {
    expect(approvalDisclosure('notify', '{}')).toEqual({
      tool: 'notify',
      formattedArgs: '{}',
      argsAvailable: true,
      valid: true,
    })
  })

  it.each([
    ['missing tool', '', '{}'],
    ['missing arguments', 'notify', ''],
    ['malformed arguments', 'notify', '{'],
    ['null arguments', 'notify', 'null'],
    ['array arguments', 'notify', '[]'],
    ['scalar arguments', 'notify', 'true'],
  ])('rejects %s', (_name, tool, args) => {
    expect(approvalDisclosure(tool, args).valid).toBe(false)
  })

  it('formats a disclosed object without changing its values', () => {
    const disclosure = approvalDisclosure(' edges__pods_delete ', '{"namespace":"prod","name":"api"}')
    expect(disclosure.tool).toBe('edges__pods_delete')
    expect(disclosure.formattedArgs).toBe('{\n  "namespace": "prod",\n  "name": "api"\n}')
    expect(disclosure.valid).toBe(true)
  })
})
