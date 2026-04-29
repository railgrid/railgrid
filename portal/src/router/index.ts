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
  {
    path: '/edges',
    name: 'edges',
    component: () => import('@/pages/EdgesPage.vue'),
  },
  {
    path: '/edges/:name',
    name: 'edge-detail',
    component: () => import('@/pages/EdgeDetailPage.vue'),
    props: true,
  },
  {
    path: '/workloads',
    name: 'workloads',
    component: () => import('@/pages/WorkloadsPage.vue'),
  },
  {
    path: '/workloads/:namespace/:name',
    name: 'workload-detail',
    component: () => import('@/pages/WorkloadDetailPage.vue'),
    props: true,
  },
  {
    path: '/mcp',
    name: 'mcp',
    component: () => import('@/pages/MCPPage.vue'),
  },
  {
    path: '/mcp/:name',
    name: 'mcp-detail',
    component: () => import('@/pages/MCPDetailPage.vue'),
    props: true,
  },
  {
    path: '/edges/:name/terminal',
    name: 'edge-terminal',
    component: () => import('@/pages/TerminalPage.vue'),
    props: true,
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
