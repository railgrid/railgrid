import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(process.cwd(), 'src', 'WorkloadCreate.vue'), 'utf8')

describe('Marketplace workload target regression', () => {
  it('offers and validates only KubernetesCluster edges for the Kubernetes Service follow-up', () => {
    expect(source).toMatch(/const kubernetesEdges = computed\(\(\) => edges\.value\.filter\(\(edge\) => edge\.type === 'kubernetes'\)\)/)
    expect(source).toMatch(/kubernetesEdges\.value\.some\(\(edge\) => edge\.name === deployEdge\.value\)/)
    expect(source).toMatch(/const selectedEdge = kubernetesEdges\.value\.find\(\(edge\) => edge\.name === deployEdge\.value\)/)
    expect(source).toMatch(/v-for="edge in kubernetesEdges"/)
    expect(source).not.toMatch(/v-for="edge in edges"/)
    expect(source).toMatch(/Connect a Kubernetes edge first/)
    expect(source).toMatch(/@primary="emit\('connectEdge'\)"/)
  })

  it('fences route teardown and disables cancellation while deployment is in flight', () => {
    expect(source).toMatch(/import \{ computed, onMounted, onUnmounted, ref \} from 'vue'/)
    expect(source).toMatch(/let active = true\s+let lifecycleGeneration = 0/)
    expect(source).toMatch(/function cancel\(\): void \{\s+if \(busy\.value\) return[\s\S]*active = false[\s\S]*emit\('cancel'\)/)
    expect(source).toMatch(/const latestEdges = await listEdges\(\)\s+if \(!isCurrent\(generation\)\) return/)
    expect(source).toMatch(/await deployMarketplaceApp\(\{[\s\S]*\}\)\s+if \(!isCurrent\(generation\)\) return\s+emit\('completed'/)
    expect(source).toMatch(/onUnmounted\(\(\) => \{[\s\S]*active = false[\s\S]*lifecycleGeneration \+= 1/)
    expect(source).toMatch(/k-back-action" :disabled="busy" @click="cancel"/)
    expect(source).toMatch(/>Cancel<\/button>/)
    expect(source).not.toMatch(/@click(?:\.prevent)?="emit\('cancel'\)"/)
  })
})
