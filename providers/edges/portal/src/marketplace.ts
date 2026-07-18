// The marketplace catalog: one-click deployable self-hosted apps. Each entry
// ships a Helm chart reference (the provider renders it hub-side; the edge only
// applies the rendered manifests) plus the join to the MCP service catalog
// (`type` matches Services.vue PRESETS) so a deployed app auto-wires an edges
// Service and its `<service>_*` MCP tools light up once a token is set.
//
// Keep values minimal: only what makes the app usable on an edge (persistence
// size, a Service type/port). fullnameOverride is forced provider-side to the
// workload name, so the chart's Service name is deterministic for targetRef.

export interface HelmChartRef {
  repoURL: string
  chart: string
  version: string
}

export interface MarketplaceApp {
  type: string // edges Service spec.type — join key to Services PRESETS
  label: string
  category: string
  description: string
  chart: HelmChartRef
  // Container/Service port the app listens on; used for the edges Service
  // targetRef.port. Should match the chart's Service port.
  port: number
  // Minimal preset chart values (merged over chart defaults, under the forced
  // fullnameOverride). Persistence sizes, service type, etc.
  values?: Record<string, unknown>
  // How the operator authenticates once deployed (surfaced as a next-step hint;
  // token itself is minted in the app's own UI and pasted on the Services tab).
  credential: 'api-key' | 'user-pass' | 'password' | 'optional'
}

// Chart versions pinned for reproducible renders. Verify/bump against the
// upstream repos when refreshing the catalog.
export const MARKETPLACE: MarketplaceApp[] = [
  {
    type: 'grafana',
    label: 'Grafana',
    category: 'Observability',
    description: 'Dashboards and visualization for your metrics and logs.',
    chart: { repoURL: 'https://grafana.github.io/helm-charts', chart: 'grafana', version: '8.5.1' },
    port: 80,
    values: { persistence: { enabled: true, size: '2Gi' }, service: { type: 'ClusterIP', port: 80 } },
    credential: 'api-key',
  },
  {
    type: 'prometheus',
    label: 'Prometheus',
    category: 'Observability',
    description: 'Time-series metrics collection and querying.',
    chart: { repoURL: 'https://prometheus-community.github.io/helm-charts', chart: 'prometheus', version: '27.5.1' },
    port: 9090,
    values: { server: { service: { type: 'ClusterIP', servicePort: 9090 }, persistentVolume: { size: '4Gi' } } },
    credential: 'optional',
  },
  {
    type: 'qbittorrent',
    label: 'qBittorrent',
    category: 'Media',
    description: 'BitTorrent client with a web UI.',
    chart: { repoURL: 'https://charts.gabe565.com', chart: 'qbittorrent', version: '0.9.2' },
    port: 8080,
    values: { persistence: { config: { enabled: true, size: '1Gi' } }, service: { main: { ports: { http: { port: 8080 } } } } },
    credential: 'user-pass',
  },
  {
    type: 'prowlarr',
    label: 'Prowlarr',
    category: 'Media',
    description: 'Indexer manager/proxy for the *arr apps.',
    chart: { repoURL: 'https://charts.gabe565.com', chart: 'prowlarr', version: '0.7.2' },
    port: 9696,
    values: { persistence: { config: { enabled: true, size: '1Gi' } } },
    credential: 'api-key',
  },
  {
    type: 'sonarr',
    label: 'Sonarr',
    category: 'Media',
    description: 'TV series collection manager.',
    chart: { repoURL: 'https://charts.gabe565.com', chart: 'sonarr', version: '0.8.2' },
    port: 8989,
    values: { persistence: { config: { enabled: true, size: '1Gi' } } },
    credential: 'api-key',
  },
  {
    type: 'radarr',
    label: 'Radarr',
    category: 'Media',
    description: 'Movie collection manager.',
    chart: { repoURL: 'https://charts.gabe565.com', chart: 'radarr', version: '0.8.2' },
    port: 7878,
    values: { persistence: { config: { enabled: true, size: '1Gi' } } },
    credential: 'api-key',
  },
  {
    type: 'jellyfin',
    label: 'Jellyfin',
    category: 'Media',
    description: 'Media server for movies, shows and music.',
    chart: { repoURL: 'https://charts.gabe565.com', chart: 'jellyfin', version: '2.3.2' },
    port: 8096,
    values: { persistence: { config: { enabled: true, size: '2Gi' } } },
    credential: 'api-key',
  },
]

export const MARKETPLACE_CATEGORIES = MARKETPLACE.reduce<{ category: string; apps: MarketplaceApp[] }[]>(
  (groups, a) => {
    let g = groups.find((x) => x.category === a.category)
    if (!g) {
      g = { category: a.category, apps: [] }
      groups.push(g)
    }
    g.apps.push(a)
    return groups
  },
  [],
)
