import { ORGANIZATION_ROUTE, WORKSPACE_ROUTE } from '@/portalkit/navigation'

export const routes = [
  { path: '/', name: 'landing', component: () => import('@/pages/NotFoundPage.vue') },
  { path: ORGANIZATION_ROUTE + '/providers', name: 'org-providers', component: () => import('@/pages/ProvidersPage.vue') },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/pages/LoginPage.vue'),
    meta: { public: true },
  },
  {
    path: '/auth/callback',
    name: 'auth-callback',
    component: () => import('@/pages/AuthCallback.vue'),
    meta: { public: true },
  },
  {
    path: WORKSPACE_ROUTE,
    name: 'dashboard',
    component: () => import('@/pages/DashboardPage.vue'),
  },
  // /edges, /servers, /edges/:name, /workloads, /workloads/:ns/:name,
  // /edges/:name/terminal removed: the kubernetes-edges and server-edges
  // providers now ship their own custom-element micro-frontends under
  // providers/{name}/portal/. Their internal memory-history routers
  // handle the in-provider navigation; the URLs land on the portal SPA
  // at /providers/kubernetes-edges/* and /providers/server-edges/* via
  // ProviderFrame.
  {
    path: WORKSPACE_ROUTE + '/providers',
    name: 'providers',
    component: () => import('@/pages/ProvidersPage.vue'),
  },
  {
    path: ORGANIZATION_ROUTE + '/settings',
    name: 'settings',
    redirect: (to: { params: Record<string, unknown> }) => `/${to.params.orgID}/settings/workspaces`,
  },
  {
    path: ORGANIZATION_ROUTE + '/settings/workspaces',
    name: 'settings-workspaces',
    component: () => import('@/pages/TenantSettingsPage.vue'),
  },
  {
    path: ORGANIZATION_ROUTE + '/settings/workspaces/:workspaceUUID',
    name: 'settings-workspace-overview',
    component: () => import('@/pages/TenantSettingsPage.vue'),
  },
  {
    path: ORGANIZATION_ROUTE + '/settings/organizations',
    name: 'settings-organizations',
    component: () => import('@/pages/TenantSettingsPage.vue'),
  },
  {
    path: '/organizations',
    name: 'organizations',
    component: () => import('@/pages/OrganizationsPage.vue'),
  },
  {
    path: '/organizations/new',
    name: 'organization-create',
    component: () => import('@/pages/OrganizationCreatePage.vue'),
  },
  {
    path: WORKSPACE_ROUTE + '/mcp',
    name: 'mcp',
    component: () => import('@/pages/MCPPage.vue'),
  },
  {
    path: WORKSPACE_ROUTE + '/create/mcp-server',
    name: 'mcp-create',
    component: () => import('@/pages/MCPPage.vue'),
  },
  {
    path: WORKSPACE_ROUTE + '/mcp/:name',
    name: 'mcp-detail',
    component: () => import('@/pages/MCPPage.vue'),
  },
  {
    // Platform-admin area. Gated by an admin-only meta flag: the guard below
    // probes /api/admin/access and redirects non-admins to the dashboard, so
    // the shell (and its admin data fetches) never loads for them. The shell
    // renders an admin sub-nav + a nested <router-view> for each section.
    path: '/bonkers',
    component: () => import('@/pages/BonkersPage.vue'),
    meta: { admin: true },
    children: [
      { path: '', redirect: '/bonkers/providers' },
      { path: 'providers', name: 'bonkers-providers', component: () => import('@/pages/bonkers/ProvidersSection.vue') },
      { path: 'identities', name: 'bonkers-identities', component: () => import('@/pages/bonkers/IdentitiesSection.vue') },
      { path: 'organizations', name: 'bonkers-organizations', component: () => import('@/pages/bonkers/OrgsSection.vue') },
      { path: 'users', name: 'bonkers-users', component: () => import('@/pages/bonkers/UsersSection.vue') },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/pages/NotFoundPage.vue'),
    meta: { public: true },
  },
]
