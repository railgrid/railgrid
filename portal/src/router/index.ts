import { createRouter, createWebHistory } from 'vue-router'
import { routes } from './routes'
import { installContextGuard } from './contextGuard'

export const router = createRouter({ history: createWebHistory(import.meta.env.BASE_URL), routes })
installContextGuard(router)
