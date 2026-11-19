import { createRouter, createWebHistory } from 'vue-router'
import { routes } from './routes'
import { installContextGuard } from './contextGuard'
import { installTelemetry } from '@/telemetry/install'

export const router = createRouter({ history: createWebHistory(import.meta.env.BASE_URL), routes })
installContextGuard(router)
// Inert unless the hub operator enabled product telemetry (docs/telemetry.md).
installTelemetry(router)
