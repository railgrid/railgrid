export type Collection = 'connections' | 'issues' | 'operations' | 'events';
export type Route = { page: Collection; name?: string; connection?: string; create?: 'connection' | 'issue'; invalid?: boolean; namespace?: string };

const collections: Collection[] = ['connections', 'issues', 'operations', 'events'];
const namespacePattern = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

function invalidRoute(): Route { return { page: 'connections', invalid: true }; }

function decode(value: string): string {
  return decodeURIComponent(value);
}

function encode(value: string): string {
  return encodeURIComponent(value);
}

function validNamespace(value: string): boolean {
  return value.length <= 63 && namespacePattern.test(value);
}

function parseBareCollection(page: Collection, parts: string[]): Route {
  if (!parts.length) return { page };
  if (page === 'issues' && parts[0] === 'detail' && parts.length === 3) {
    return { page, connection: decode(parts[1]), name: decode(parts[2]) };
  }
  if (page === 'issues' && parts.length === 2) {
    return { page, connection: decode(parts[0]), name: decode(parts[1]) };
  }
  if (page !== 'issues' && parts.length === 1) return { page, name: decode(parts[0]) };
  return invalidRoute();
}

function parseCanonicalCollection(page: Collection, namespace: string, parts: string[]): Route {
  if (!parts.length) return { page, namespace };
  if (parts.length === 1 && parts[0] === 'create' && (page === 'connections' || page === 'issues')) {
    return { page, create: page === 'connections' ? 'connection' : 'issue', namespace };
  }
  if (parts[0] !== 'detail') return invalidRoute();
  if (page === 'issues' && parts.length === 3) {
    return { page, connection: decode(parts[1]), name: decode(parts[2]), namespace };
  }
  if (page !== 'issues' && parts.length === 2) {
    return { page, name: decode(parts[1]), namespace };
  }
  return invalidRoute();
}

function parseCollection(parts: string[]): Route {
  if (!collections.includes(parts[0] as Collection)) return invalidRoute();
  const page = parts[0] as Collection;
  if (parts[1] !== 'namespaces' || parts.length < 3) return parseBareCollection(page, parts.slice(1));
  const namespace = decode(parts[2]);
  if (!validNamespace(namespace)) return invalidRoute();
  return parseCanonicalCollection(page, namespace, parts.slice(3));
}

export function parseRoute(value = ''): Route {
  const path = value.replace(/^\/+|\/+$/g, '');
  if (!path) return { page: 'connections' };
  const parts = path.split('/');
  if (parts[0] === 'namespaces' && parts.length >= 3) {
    try {
      const namespace = decode(parts[1]);
      if (!validNamespace(namespace)) return invalidRoute();
      const inner = parts.slice(2);
      if (inner[0] === 'create' && inner.length === 2 && (inner[1] === 'connection' || inner[1] === 'issue')) {
        return { page: inner[1] === 'connection' ? 'connections' : 'issues', create: inner[1], namespace };
      }
      if (!collections.includes(inner[0] as Collection)) return { ...invalidRoute(), namespace };
      return { ...parseBareCollection(inner[0] as Collection, inner.slice(1)), namespace };
    } catch { return { page: 'connections', invalid: true }; }
  }
  try {
    if (parts[0] === 'create' && parts.length === 2 && (parts[1] === 'connection' || parts[1] === 'issue')) return { page: parts[1] === 'connection' ? 'connections' : 'issues', create: parts[1] };
    return parseCollection(parts);
  } catch { /* Invalid escapes render the not-found state. */ }
  return invalidRoute();
}

/** Serialize a parsed route using the collection-first, namespace-scoped URL contract. */
export function routePath(route: Route, defaultNamespace = 'default'): string {
  const namespace = route.namespace || defaultNamespace || 'default';
  const prefix = `${route.page}/namespaces/${encode(namespace)}`;
  if (route.create) return `${prefix}/create`;
  if (!route.name) return prefix;
  if (route.page === 'issues') return `${prefix}/detail/${encode(route.connection || '')}/${encode(route.name)}`;
  return `${prefix}/detail/${encode(route.name)}`;
}

/** Convert a provider-relative route, including legacy aliases, to its canonical URL. */
export function canonicalPath(namespace: string, value: string): string {
  const route = parseRoute(value);
  return routePath(route.invalid ? { page: 'connections' } : route, route.namespace || namespace);
}

export const resourcePath = (page: Collection, name: string) => `${page}/${encode(name)}`;
// The explicit marker keeps a connection named "namespaces" unambiguous from
// the canonical issues/namespaces/<namespace> collection route.
export const issuePath = (connection: string, id: string) => `issues/detail/${encode(connection)}/${encode(id)}`;
export const authorityKey = (ctx: import('./api').FarosContext | null) => JSON.stringify([ctx?.basePath, ctx?.tenant, ctx?.orgUUID, ctx?.workspaceUUID, ctx?.user?.sub || ctx?.user?.email]);
