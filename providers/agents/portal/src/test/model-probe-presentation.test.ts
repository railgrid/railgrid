import { describe, expect, it } from 'vitest'
import { presentModelProbeResult } from '../model-probe-result'
import TechnicalDetails from '../components/TechnicalDetails.vue'
import { mountVue } from './vue-helper'

describe('model verification failure presentation', () => {
  it('keeps backend diagnostics out of the headline and in a collapsed, bounded disclosure', async () => {
    const key = 'sk-proj-abcdefghijklmnopqrstuvwxyz'
    const result = presentModelProbeResult({ ok: false, latencyMS: 0, error: `HTTP 400 invalid request api_key=${key} ${'x'.repeat(5000)}` })
    expect(result.error).toContain('Review the endpoint, credential, and selected model')
    expect(result.error).not.toContain('HTTP 400')
    expect(result.technicalDiagnostic).toContain('[redacted]')
    expect(result.technicalDiagnostic).not.toContain(key)
    expect(result.technicalDiagnostic!.length).toBeLessThan(4050)
    const view = await mountVue(TechnicalDetails, { diagnostic: result.technicalDiagnostic })
    const details = view.element.querySelector('details')!
    expect(details.open).toBe(false)
    expect(details.querySelector('summary')?.textContent).toContain('Technical details')
    expect(details.querySelector('pre')?.textContent).not.toContain(key)
  })
})
