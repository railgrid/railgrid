import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const readSource = file => readFileSync(resolve(process.cwd(), 'src', file), 'utf8')

describe('Edges create guidance', () => {
  it('uses shared first-run journeys for every user-created resource surface', () => {
    const edgeCollection = readSource('EdgeCollection.vue')
    const services = readSource('Services.vue')
    const workloads = readSource('Workloads.vue')

    for (const source of [edgeCollection, services, workloads]) {
      expect(source).toMatch(/import FirstRunGuide from '\.\/portalkit\/FirstRunGuide\.vue'/)
      expect(source).toMatch(/<FirstRunGuide/)
      expect(source).toMatch(/journey-label=/)
    }
    expect(services).toMatch(/hasEdges \? 'Create service' : 'Connect edge'/)
    expect(services).toMatch(/if \(hasEdges\.value\) emit\('create'\)[\s\S]*else emit\('connectEdge'\)/)
    expect(services).toMatch(/services\.value\.length === 0 && isCompleteFirstCursorPage\(/)
    expect(services).toMatch(/<ResourceTable\s+v-else/)
    expect(workloads).toMatch(/hasKubernetesEdges \? 'Create workload' : 'Connect edge'/)
    expect(workloads).toMatch(/secondary-label="hasKubernetesEdges \? 'Browse marketplace' : ''"/)
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
    expect(wizard).toMatch(/Edge name[\s\S]*Resource type[\s\S]*Scheduling labels/)
    expect(service).toMatch(/Service name[\s\S]*Endpoint[\s\S]*Credentials[\s\S]*MCP tools/)
    expect(workload).toMatch(/Namespace[\s\S]*Edge selector[\s\S]*Placements/)
    expect(service).toMatch(/await createKubeEdgeService\(\{/)
    expect(workload).toMatch(/await createWorkload\(workload\)/)
  })

  it('makes edge connection progress semantic while retaining masked token display', () => {
    const wizard = readSource('Wizard.vue')
    expect(wizard).toMatch(/<ol class="wiz-steps" aria-label="Edge connection progress">/)
    expect(wizard).toMatch(/:aria-current="step === i \+ 1 \? 'step' : undefined"/)
    expect(wizard).toMatch(/role="status" aria-live="polite" aria-atomic="true">\{\{ connectionAnnouncement \}\}/)
    expect(wizard).toMatch(/Generating join token for \$\{trimmed\.value\}/)
    expect(wizard).toMatch(/Waiting for \$\{trimmed\.value\} to connect/)
    expect(wizard).toMatch(/class="banner error" role="alert" aria-live="assertive"/)
    expect(wizard).toMatch(/class="banner warn" role="alert" aria-live="assertive"/)
    expect(wizard).toMatch(/const masked = '••••••••••••••••'/)
    expect(wizard).toMatch(/navigator\.clipboard\.writeText\(build\(joinToken\.value\)\)/)
    expect(wizard).not.toMatch(/revealedCommand|revealForManualCopy|Reveal command for manual copy/)
    expect(wizard).toMatch(/onUnmounted\(\(\) => \{[\s\S]*clearSetupSecret\(\)/)
  })
})
