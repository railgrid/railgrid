import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useAdminStore } from '@/stores/admin'

const routes = [
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
    path: '/',
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
  // /mcp + /mcp/:name removed: the mcp provider now ships its own custom-
  // element micro-frontend under providers/mcp/portal/ and renders via
  // the dynamic /providers/:name/:rest(.*)* route handled by ProviderFrame.
  {
    path: '/providers',
    name: 'providers',
    component: () => import('@/pages/ProvidersPage.vue'),
  },
  {
    path: '/tenant',
    name: 'tenant',
    component: () => import('@/pages/TenantSettingsPage.vue'),
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

export const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!to.meta.public && !auth.isAuthenticated) {
    return { name: 'login' }
  }
  // Admin-only routes: confirm access before loading. Non-admins are bounced to
  // the dashboard so the page never mounts and never fires admin data fetches.
  if (to.meta.admin) {
    const admin = useAdminStore()
    const ok = admin.isAdmin === null ? await admin.checkAccess() : admin.isAdmin
    if (!ok) return { name: 'dashboard' }
  }
})
