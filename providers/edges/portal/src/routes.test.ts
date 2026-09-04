import { describe, expect, it } from 'vitest'
import {
  edgeConnectPath,
  edgeConnectionCancelPath,
  edgeConnectionSuccessPath,
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

  it.each([
    ['service collection', serviceCreatePath(), { cancelPath: 'services' }, 'services'],
    ['manual workload collection', workloadDeployPath('manual'), { cancelPath: 'workloads', requiredType: 'kubernetes' }, 'workloads'],
    ['marketplace workload form', workloadDeployPath('marketplace', 'grafana'), { requiredType: 'kubernetes' }, workloadDeployPath('marketplace', 'grafana')],
  ] as const)('preserves distinct %s success and cancel destinations without duplicate history', (_label, origin, options, cancelPath) => {
    const connectPath = edgeConnectPath(origin, options)
    expect(parseSubPath(connectPath)).toEqual({
      page: 'edges',
      connect: {
        resource: 'edge',
        successPath: origin,
        cancelPath,
        ...('requiredType' in options ? { requiredType: options.requiredType } : {}),
      },
    })
    // Entry and exit both replace the current action route. Restoring the form
    // therefore leaves one form entry, not a duplicate that Back can reopen.
    expect(navigationDetail(connectPath, true)).toEqual({ path: connectPath, replace: true })
    expect(navigationDetail(edgeConnectionCancelPath(cancelPath), true)).toEqual({ path: cancelPath, replace: true })
    expect(navigationDetail(edgeConnectionSuccessPath(origin, 'kubernetes', 'new-edge'), true)).toEqual({ path: origin, replace: true })
  })

  it('infers the Kubernetes requirement from a workload return route that omitted its type', () => {
    const origin = workloadDeployPath('manual')
    expect(parseSubPath(edgeConnectPath(origin, { cancelPath: 'workloads' }))).toEqual({
      page: 'edges',
      connect: { resource: 'edge', successPath: origin, cancelPath: 'workloads', requiredType: 'kubernetes' },
    })
    const legacy = `connect/edge/return/${encodeURIComponent(origin)}`
    expect(parseSubPath(legacy)).toEqual({
      page: 'edges',
      connect: { resource: 'edge', successPath: origin, cancelPath: origin, requiredType: 'kubernetes' },
    })
  })

  it('rejects a server requirement for a workload return route', () => {
    const invalid = `connect/edge/server/return/${encodeURIComponent(workloadDeployPath('manual'))}`
    expect(parseSubPath(invalid)).toEqual({ page: 'edges', connect: { resource: 'edge' } })
  })

  it('rejects a prerequisite cancel destination outside its originating journey', () => {
    const invalid = `connect/edge/success/${encodeURIComponent(serviceCreatePath())}/cancel/${encodeURIComponent('../../settings')}`
    expect(parseSubPath(invalid)).toEqual({ page: 'edges', connect: { resource: 'edge' } })
  })

  it('canonicalizes accepted shell-prefixed prerequisite destinations before navigation', () => {
    const success = `providers/edges/${serviceCreatePath()}`
    const cancel = 'providers/edges/services'
    const path = `connect/edge/success/${encodeURIComponent(success)}/cancel/${encodeURIComponent(cancel)}`
    expect(parseSubPath(path)).toEqual({
      page: 'edges',
      connect: { resource: 'edge', successPath: 'create/service', cancelPath: 'services' },
    })
  })

  it.each([
    'create/service/kubernetes/../../../../../settings',
    'create/service/kubernetes/..\\..\\..\\..\\..\\settings',
    'create/service/kubernetes/%252E%252E%252F%252E%252E%252Fsettings',
    'create/service/kubernetes/%2e%2e/%zz/%2e%2e/%2e%2e/%2e%2e/%2e%2e/%2e%2e/settings',
  ])('rejects dot-segment prerequisite callbacks before shell navigation: %s', (success) => {
    const path = `connect/edge/success/${encodeURIComponent(success)}/cancel/${encodeURIComponent('services')}`
    expect(parseSubPath(path)).toEqual({ page: 'edges', connect: { resource: 'edge' } })
  })

  it('falls back to the new edge detail when no create journey is active', () => {
    expect(edgeConnectPath()).toBe('connect/edge')
    expect(edgeConnectionCancelPath()).toBe('')
    expect(edgeConnectionSuccessPath(undefined, 'server', 'edge/a')).toBe('edges/server/edge%2Fa')
  })
})
