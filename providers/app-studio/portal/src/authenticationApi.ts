import { request, projectURL } from './api'
import type { RailgridContext, ListResponse, ProjectPublishingGrant } from './types'

export interface ProjectAuthentication {
  enabled: boolean
  supported: boolean
  supportReason?: string
  supportMessage?: string
  applicationVerified: boolean
  message: string
  environments: { environment: string; instance: string; runtimeExists?: boolean; configured: boolean; runtimeReady: boolean; reason?: string }[]
}

export const authenticationApi = {
  getProjectAuthentication(ctx: RailgridContext | null, name: string) {
    return request<ProjectAuthentication>(ctx, 'GET', projectURL(ctx, name, 'authentication'))
  },
  configureProjectAuthentication(ctx: RailgridContext | null, name: string, enabled: boolean) {
    return request<ProjectAuthentication>(ctx, 'POST', projectURL(ctx, name, 'authentication'), { enabled })
  },
  getProjectAuthenticationGrants(ctx: RailgridContext | null, name: string) {
    return request<ListResponse<ProjectPublishingGrant>>(ctx, 'GET', projectURL(ctx, name, 'authentication-grants'))
  },
  addProjectAuthenticationGrant(ctx: RailgridContext | null, name: string, user: string, appId: string) {
    return request<ListResponse<ProjectPublishingGrant>>(ctx, 'POST', projectURL(ctx, name, 'authentication-grants'), { user, appId })
  },
  removeProjectAuthenticationGrant(ctx: RailgridContext | null, name: string, grant: string) {
    return request<ListResponse<ProjectPublishingGrant>>(ctx, 'DELETE', projectURL(ctx, name, 'authentication-grants', grant))
  },
}
