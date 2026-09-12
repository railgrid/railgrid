export type Collection = 'connections' | 'issues' | 'operations' | 'events';
export type Route = { page: Collection; name?: string; connection?: string; create?: 'connection' | 'issue'; invalid?: boolean; namespace?: string };
export function parseRoute(value = ''): Route {
  const path = value.replace(/^\/+|\/+$/g, '');
  if (!path) return { page: 'connections' };
  const parts = path.split('/');
  if (parts[0] === 'namespaces' && parts.length >= 3) {
    try {
      const namespace = decodeURIComponent(parts[1]);
      if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(namespace) || namespace.length > 63) return { page: 'connections', invalid: true };
      return { ...parseRoute(parts.slice(2).join('/')), namespace };
    } catch { return { page: 'connections', invalid: true }; }
  }
  try {
    if (parts[0] === 'create' && parts.length === 2 && (parts[1] === 'connection' || parts[1] === 'issue')) return { page: parts[1] === 'connection' ? 'connections' : 'issues', create: parts[1] };
    if (['connections', 'issues', 'operations', 'events'].includes(parts[0])) {
      const page = parts[0] as Collection;
      if (parts.length === 1) return { page };
      if (page === 'issues' && parts.length === 3) return { page, connection: decodeURIComponent(parts[1]), name: decodeURIComponent(parts[2]) };
      if (page !== 'issues' && parts.length === 2) return { page, name: decodeURIComponent(parts[1]) };
    }
  } catch { /* Invalid escapes render the not-found state. */ }
  return { page: 'connections', invalid: true };
}
export const resourcePath = (page: Collection, name: string) => `${page}/${encodeURIComponent(name)}`;
export const issuePath = (connection: string, id: string) => `issues/${encodeURIComponent(connection)}/${encodeURIComponent(id)}`;
export const authorityKey = (ctx: import('./api').FarosContext | null) => JSON.stringify([ctx?.basePath, ctx?.tenant, ctx?.orgUUID, ctx?.workspaceUUID, ctx?.user?.sub || ctx?.user?.email]);
