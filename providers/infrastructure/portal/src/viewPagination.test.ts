import { describe, expect, it } from 'vitest'

import instanceListSource from './views/InstanceListPage.vue?raw'

function inactiveChangeBranch(source: string): string {
  const conditionOffset = source.indexOf('if (!activeQuery)')
  expect(conditionOffset).toBeGreaterThanOrEqual(0)
  const openBrace = source.indexOf('{', conditionOffset)
  expect(openBrace).toBeGreaterThanOrEqual(0)
  let depth = 0
  for (let offset = openBrace; offset < source.length; offset += 1) {
    if (source[offset] === '{') depth += 1
    if (source[offset] !== '}') continue
    depth -= 1
    if (depth === 0) return source.slice(conditionOffset, offset + 1)
  }
  throw new Error('unterminated inactive query branch')
}

describe('instance server pagination transitions', () => {
  it('preserves the incoming page and opaque cursor for unfiltered page changes', () => {
    const branch = inactiveChangeBranch(instanceListSource)
    expect(branch).toMatch(/void load\(\)/)
    expect(branch).not.toMatch(/page\.value = 1/)
    expect(branch).not.toMatch(/cursor\.value = null/)
    expect(branch).toContain('wasClientMode || change.reason === \'query\' || change.reason === \'filter\'')
    expect(branch).not.toContain("change.reason === 'page'")
  })

  it('resets a client-side clear to the first server page', () => {
    const helperOffset = instanceListSource.indexOf('function resetToFirstServerPage()')
    expect(helperOffset).toBeGreaterThanOrEqual(0)
    const helper = instanceListSource.slice(helperOffset, instanceListSource.indexOf('\n}', helperOffset) + 2)
    expect(helper).toContain('page.value = 1')
    expect(helper).toContain('cursor.value = null')

    const branch = inactiveChangeBranch(instanceListSource)
    expect(branch).toContain('if (wasClientMode || change.reason === \'query\' || change.reason === \'filter\') resetToFirstServerPage()')
  })

  it('keeps query and filter edits local while a complete client walk is in flight', () => {
    const clientBranchOffset = instanceListSource.indexOf("if (paginationMode.value === 'client')")
    expect(clientBranchOffset).toBeGreaterThanOrEqual(0)
    const clientBranch = instanceListSource.slice(clientBranchOffset, instanceListSource.indexOf('\n  // Entering a query/filter', clientBranchOffset))
    expect(clientBranch).toContain('query-independent')
    expect(clientBranch).not.toContain("change.reason === 'query' || change.reason === 'filter'")
    expect(clientBranch).toContain("change.reason === 'page-size'")
    expect(clientBranch).toContain('void load()')
    expect(clientBranch).toContain('return')
  })

  it('re-enters client mode after a clear read without relying on polling', () => {
    const clientBranchOffset = instanceListSource.indexOf("if (paginationMode.value === 'client')")
    expect(clientBranchOffset).toBeGreaterThanOrEqual(0)
    const clientBranch = instanceListSource.slice(clientBranchOffset, instanceListSource.indexOf('\n  // Entering a query/filter', clientBranchOffset))
    expect(clientBranch).toContain("pendingReadMode === 'server'")
    expect(clientBranch).toContain('void load()')
    expect(instanceListSource).toContain('let pendingReadMode: InstanceListRequest[\'mode\'] | null = null')
  })

  it('reuses a complete first page and marks the client source ready', () => {
    expect(instanceListSource).toContain('isCompleteFirstCursorPage')
    expect(instanceListSource).toContain('if (canReuseCurrentServerPage) {')
    expect(instanceListSource).toContain('clientAuthorityReady.value = true')
    expect(instanceListSource).toContain('clientAuthorityReady.value = false')
  })
})
