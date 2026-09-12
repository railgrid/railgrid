export type Collection = 'connections' | 'issues' | 'operations' | 'events';
export type Route = { page: Collection; name?: string; connection?: string; create?: 'connection' | 'issue'; invalid?: boolean };
const collections: Collection[] = ['connections', 'issues', 'operations', 'events'];
const invalidRoute = (): Route => ({ page: 'connections', invalid: true });
const encode = encodeURIComponent;

/** Routes describe resources in the active Faros workspace, without a namespace dimension. */
export function parseRoute(value = ''): Route {
  const parts = value.replace(/^\/+|\/+$/g, '').split('/');
  if (!parts[0]) return { page: 'connections' };
  const page = parts[0] as Collection;
  if (!collections.includes(page)) return invalidRoute();
  if (parts.length === 1) return { page };
  if (parts.length === 2 && parts[1] === 'create' && (page === 'connections' || page === 'issues')) return { page, create: page === 'connections' ? 'connection' : 'issue' };
  try {
    if (parts[1] !== 'detail' || parts.slice(2).some(part => !part)) return invalidRoute();
    if (page === 'issues' && parts.length === 4) return { page, connection: decodeURIComponent(parts[2]), name: decodeURIComponent(parts[3]) };
    if (page !== 'issues' && parts.length === 3) return { page, name: decodeURIComponent(parts[2]) };
  } catch { /* Malformed escapes render the not-found state. */ }
  return invalidRoute();
}
export function routePath(route: Route): string {
  if (route.create) return `${route.page}/create`;
  if (!route.name) return route.page;
  if (route.page === 'issues') return issuePath(route.connection || '', route.name);
  return resourcePath(route.page, route.name);
}
export function canonicalPath(value: string): string { return routePath(parseRoute(value)); }
export const resourcePath = (page: Collection, name: string) => `${page}/detail/${encode(name)}`;
export const issuePath = (connection: string, id: string) => `issues/detail/${encode(connection)}/${encode(id)}`;
export const authorityKey = (ctx: import('./api').FarosContext | null) => JSON.stringify([ctx?.basePath, ctx?.tenant, ctx?.orgUUID, ctx?.workspaceUUID, ctx?.user?.sub || ctx?.user?.email]);
