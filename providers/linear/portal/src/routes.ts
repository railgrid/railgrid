export type Collection = 'connections' | 'teams' | 'issues';
export type Route = { page: Collection; name?: string; connection?: string; team?: string; create?: 'connection' | 'team' | 'issue'; invalid?: boolean };
const collections: Collection[] = ['connections', 'teams', 'issues'];
const invalidRoute = (): Route => ({ page: 'connections', invalid: true });
const encode = encodeURIComponent;

/** Routes describe resources in the active Faros workspace, without a namespace dimension. */
export function parseRoute(value = ''): Route {
  const parts = value.replace(/^\/+|\/+$/g, '').split('/');
  if (!parts[0]) return { page: 'connections' };
  const page = parts[0] as Collection;
  if (!collections.includes(page)) return invalidRoute();
  if (parts.length === 1) return { page };
  if (parts.length === 2 && parts[1] === 'create' && (page === 'connections' || page === 'teams' || page === 'issues')) return { page, create: page === 'connections' ? 'connection' : page === 'teams' ? 'team' : 'issue' };
  try {
    if (parts[1] !== 'detail' || parts.slice(2).some(part => !part)) return invalidRoute();
    if (page === 'issues' && parts.length === 5) return { page, connection: decodeURIComponent(parts[2]), name: decodeURIComponent(parts[3]), team: decodeURIComponent(parts[4]) };
    if (page !== 'issues' && parts.length === 3) return { page, name: decodeURIComponent(parts[2]) };
  } catch { /* Malformed escapes render the not-found state. */ }
  return invalidRoute();
}
export function routePath(route: Route): string {
  if (route.create) return `${route.page}/create`;
  if (!route.name) return route.page;
  if (route.page === 'issues') return issuePath(route.connection || '', route.name, route.team || '');
  return resourcePath(route.page, route.name);
}
export function canonicalPath(value: string): string { return routePath(parseRoute(value)); }
export const resourcePath = (page: Collection, name: string) => `${page}/detail/${encode(name)}`;
export const issuePath = (connection: string, id: string, team = '') => `issues/detail/${encode(connection)}/${encode(id)}/${encode(team)}`;
export const authorityKey = (ctx: import('./api').FarosContext | null) => JSON.stringify([ctx?.basePath, ctx?.tenant, ctx?.orgUUID, ctx?.workspaceUUID, ctx?.user?.sub || ctx?.user?.email]);

/** Preserve native modified-click navigation while routing ordinary clicks in Faros. */
export function followResourceLink(event: MouseEvent, path: string, navigate: (path: string) => void): void {
  if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  event.preventDefault();
  navigate(path);
}

/** Only trust canonical Linear issue URLs supplied by the provider. */
export function linearIssueURL(value: unknown): string | undefined {
  if (typeof value !== "string") return;
  try { const url = new URL(value); if (url.protocol === "https:" && url.hostname === "linear.app" && !url.username && !url.password && /^\/[^/]+\/issue\/[^/]+/.test(url.pathname)) return url.href; } catch {}
}
