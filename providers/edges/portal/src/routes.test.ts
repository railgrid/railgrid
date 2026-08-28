import { describe, expect, it } from 'vitest'
import {
  edgeDetailPath,
  navigationDetail,
  parseSubPath,
  serviceCreatePath,
  serviceDetailPath,
  workloadDeployPath,
} from './routes'

describe('Edges provider routes', () => {
  it('parses provider-relative collection, action, and detail paths', () => {
    expect(parseSubPath(undefined)).toEqual({ page: 'edges' })
    expect(parseSubPath('connect/edge')).toEqual({ page: 'edges', connect: { resource: 'edge' } })
    expect(parseSubPath('/create/service/')).toEqual({ page: 'services', create: { resource: 'service' } })
    expect(parseSubPath('create/service/server/edge%2Fa')).toEqual({
      page: 'services',
      create: { resource: 'service', edgeType: 'server', edgeName: 'edge/a' },
    })
    expect(parseSubPath('create/service/kubernetes/prod/us')).toEqual({
      page: 'services',
      create: { resource: 'service', edgeType: 'kubernetes', edgeName: 'prod/us' },
    })
    expect(parseSubPath('deploy/workload/manual')).toEqual({
      page: 'workloads',
      deploy: { resource: 'workload', mode: 'manual' },
    })
    expect(parseSubPath('deploy/workload/marketplace/grafana')).toEqual({
      page: 'workloads',
      deploy: { resource: 'workload', mode: 'marketplace', app: 'grafana' },
    })
    expect(parseSubPath('edges/kubernetes/prod%2Fus')).toEqual({
      page: 'edges', edge: { type: 'kubernetes', name: 'prod/us' },
    })
    expect(parseSubPath('services/home%2Dassistant')).toEqual({ page: 'services', service: 'home-assistant' })
  })

  it('normalizes a shell-prefixed debug path without changing provider output paths', () => {
    expect(parseSubPath('/providers/edges/edges/server/edge-a')).toEqual({
      page: 'edges', edge: { type: 'server', name: 'edge-a' },
    })
    expect(navigationDetail('/create/service')).toEqual({ path: 'create/service' })
    expect(navigationDetail('services/svc', true)).toEqual({ path: 'services/svc', replace: true })
  })

  it('builds collision-safe paths and preserves encoded names', () => {
    expect(edgeDetailPath('kubernetes', 'prod/us')).toBe('edges/kubernetes/prod%2Fus')
    expect(serviceDetailPath('home assistant')).toBe('services/home%20assistant')
    expect(serviceCreatePath('server', 'edge-a')).toBe('create/service/server/edge-a')
    expect(serviceCreatePath()).toBe('create/service')
    expect(workloadDeployPath('manual')).toBe('deploy/workload/manual')
    expect(workloadDeployPath('marketplace', 'grafana')).toBe('deploy/workload/marketplace/grafana')
  })
})
