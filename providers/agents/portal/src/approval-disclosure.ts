export const APPROVAL_DISCLOSURE_ERROR = 'Approval details are unavailable or malformed. Deny this request or inspect the run.'

export interface ApprovalDisclosure {
  tool: string
  formattedArgs: string
  argsAvailable: boolean
  valid: boolean
}

// Approval cards may only enable approval when the server-provided, redacted
// disclosure names a tool and contains a JSON object. An empty object is a
// complete disclosure for a no-argument tool; arrays, scalars, null, missing,
// and malformed JSON are not.
export function approvalDisclosure(tool: unknown, args: unknown): ApprovalDisclosure {
  const normalizedTool = typeof tool === 'string' ? tool.trim() : ''
  if (typeof args !== 'string' || !args.trim()) {
    return { tool: normalizedTool, formattedArgs: '', argsAvailable: false, valid: false }
  }
  try {
    const parsed: unknown = JSON.parse(args)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      return { tool: normalizedTool, formattedArgs: '', argsAvailable: false, valid: false }
    }
    return {
      tool: normalizedTool,
      formattedArgs: JSON.stringify(parsed, null, 2),
      argsAvailable: true,
      valid: normalizedTool !== '',
    }
  } catch {
    return { tool: normalizedTool, formattedArgs: '', argsAvailable: false, valid: false }
  }
}

export function approvalDisclosureAvailable(tool: unknown, args: unknown): boolean {
  return approvalDisclosure(tool, args).valid
}
