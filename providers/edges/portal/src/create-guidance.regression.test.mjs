import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { MACOS_MASKED_JOIN_TOKEN, macosJoinSnippet } from './macos.ts'

const readSource = file => readFileSync(resolve(process.cwd(), 'src', file), 'utf8')

describe('Edges create guidance', () => {
  it('uses shared first-run guidance for every user-created resource surface', () => {
    const edgeCollection = readSource('EdgeCollection.vue')
    const services = readSource('Services.vue')
    const workloads = readSource('Workloads.vue')

    for (const source of [edgeCollection, services, workloads]) {
      expect(source).toMatch(/import FirstRunGuide from '\.\/portalkit\/FirstRunGuide\.vue'/)
      expect(source).toMatch(/<FirstRunGuide/)
      expect(source).toMatch(/:steps="\[\]"/)
    }
    expect(services).toMatch(/hasEdges \? 'Create service' : 'Connect edge'/)
    expect(services).toMatch(/if \(hasEdges\.value\) emit\('create'\)[\s\S]*else emit\('connectEdge'\)/)
    expect(services).toMatch(/services\.value\.length === 0 && isCompleteFirstCursorPage\(/)
    expect(services).toMatch(/<div v-else class="services-table">[\s\S]*?<ResourceTable/)
    expect(workloads).toMatch(/hasKubernetesEdges \? 'Create workload' : 'Connect edge'/)
    expect(workloads).not.toMatch(/Browse marketplace|MARKETPLACE_CATEGORIES|showMarket/)
    expect(workloads).toMatch(/workloads\.value\.length === 0 && isCompleteFirstCursorPage\(/)
    expect(workloads).toMatch(/<ResourceTable\s+v-if="!showFirstRun"/)
  })

  it('adds live shared guidance rails without changing create mutations', () => {
    const wizard = readSource('Wizard.vue')
    const service = readSource('ServiceCreate.vue')
    const workload = readSource('WorkloadCreate.vue')

    for (const source of [wizard, service, workload]) {
      expect(source).toMatch(/import CreateGuidance/)
      expect(source).toMatch(/<CreateGuidance/)
      expect(source).toMatch(/k-create-body--guided/)
      expect(source).toMatch(/k-create-fields/)
    }
    expect(wizard).toMatch(/parseEdgeLabelPairs[\s\S]*edge-labels-error/)
    expect(service).toMatch(/parseServiceHost[\s\S]*Before you create/)
    expect(workload).toMatch(/targetNamespaceError[\s\S]*pullSecretsError/)
    expect(service).toMatch(/await createKubeEdgeService\(\{/)
    expect(workload).toMatch(/await createWorkload\(workload\)/)
  })

  it('makes edge connection progress semantic while retaining masked token display', () => {
    const wizard = readSource('Wizard.vue')
    expect(wizard).toMatch(/<ol class="wiz-steps k-wizard-steps" aria-label="Edge connection progress">/)
    expect(wizard).toMatch(/:aria-current="step === i \+ 1 \? 'step' : undefined"/)
    expect(wizard).toMatch(/role="status" aria-live="polite" aria-atomic="true">\{\{ connectionAnnouncement \}\}/)
    expect(wizard).toMatch(/Generating join token for \$\{trimmed\.value\}/)
    expect(wizard).toMatch(/Waiting for \$\{trimmed\.value\} to connect/)
    expect(wizard).toMatch(/<InlineNotification v-if="error" tone="error"/)
    expect(wizard).toMatch(/<InlineNotification v-if="tokenError" tone="warning" announce="assertive"/)
    expect(wizard).toMatch(/MACOS_MASKED_JOIN_TOKEN/)
    expect(wizard).toMatch(/navigator\.clipboard\.writeText\(build\(joinToken\.value\)\)/)
    expect(wizard).not.toMatch(/revealedCommand|revealForManualCopy|Reveal command for manual copy/)
    expect(wizard).toMatch(/onUnmounted\(\(\) => \{[\s\S]*clearSetupSecret\(\)/)

    expect(MACOS_MASKED_JOIN_TOKEN).toBe('••••••••••••••••')
    const masked = macosJoinSnippet('mac-mini', 'tenant-macos', MACOS_MASKED_JOIN_TOKEN, 'https://console.dev.kyrosos.com')
    expect(masked).toContain('--hub-url https://console.dev.kyrosos.com/clusters/tenant-macos')
    expect(masked).toContain('--worker-user "$(id -un)"')
    expect(masked).toContain('--cluster tenant-macos')
    expect(masked).toContain('--token ••••••••••••••••')
    expect(masked).not.toContain('join-secret')

    const copied = macosJoinSnippet('mac-mini', 'tenant-macos', 'join-secret', 'https://console.dev.kyrosos.com')
    expect(copied).toContain('--token join-secret')
    expect(copied).not.toContain(MACOS_MASKED_JOIN_TOKEN)
  })
})
