import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

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

router.beforeEach((to) => {
  const auth = useAuthStore()
  if (!to.meta.public && !auth.isAuthenticated) {
    return { name: 'login' }
  }
})
