// Query playground support: a lazily-loaded CodeMirror editor with
// schema-driven autocomplete, plus canned examples. CodeMirror (~CM5 UMD) is
// vendored and injected on first use — same reason as cytoscape: the portal is
// an IIFE library bundle with no runtime module loader, so a bundler dynamic
// import() would get inlined. `import type` only here pulls in zero bytes.

// The vendored UMD assigns this global.
declare global {
  interface Window {
    // CM5 has no first-class ESM types here; treat as opaque.
    CodeMirror?: CMFactory
  }
}

// Minimal shape of the CodeMirror 5 factory + instance we use, so the rest of
// the file stays typed without pulling @types/codemirror.
type CMPos = { line: number; ch: number }
interface CMInstance {
  getValue(): string
  setValue(v: string): void
  getCursor(): CMPos
  getLine(n: number): string
  getWrapperElement(): HTMLElement
  on(ev: string, fn: (...a: unknown[]) => void): void
  showHint(opts: unknown): void
  refresh(): void
}
interface CMFactory {
  (host: HTMLElement, opts: Record<string, unknown>): CMInstance
  Pos(line: number, ch: number): CMPos
}

let _cmPromise: Promise<CMFactory> | null = null

// loadCodeMirror injects the vendored bundle (CSS + JS) once and resolves to
// the window.CodeMirror factory. Concurrent callers share one in-flight load.
export function loadCodeMirror(jsUrl: string, cssUrl: string): Promise<CMFactory> {
  if (window.CodeMirror) return Promise.resolve(window.CodeMirror)
  if (_cmPromise) return _cmPromise
  _cmPromise = new Promise<CMFactory>((resolve, reject) => {
    if (!document.querySelector('link[data-kuery-cm]')) {
      const link = document.createElement('link')
      link.rel = 'stylesheet'
      link.href = cssUrl
      link.setAttribute('data-kuery-cm', '')
      document.head.appendChild(link)
    }
    const s = document.createElement('script')
    s.src = jsUrl
    s.async = true
    s.onload = () => (window.CodeMirror ? resolve(window.CodeMirror) : reject(new Error('CodeMirror global missing after load')))
    s.onerror = () => {
      _cmPromise = null
      reject(new Error(`failed to load ${jsUrl}`))
    }
    document.head.appendChild(s)
  })
  return _cmPromise
}

// collectSchemaWords walks a JSON Schema and gathers every property name and
// string enum value, plus a few common kinds — the vocabulary the editor's
// autocomplete offers. Not context-aware (no path tracking), but with the
// QuerySpec's distinctive keys (relations, groupKind, maxDepth…) a prefix match
// is enough to be genuinely useful.
export function collectSchemaWords(schema: unknown): string[] {
  const words = new Set<string>()
  const walk = (node: unknown): void => {
    if (!node || typeof node !== 'object') return
    const n = node as Record<string, unknown>
    if (n.properties && typeof n.properties === 'object') {
      for (const k of Object.keys(n.properties as object)) {
        words.add(k)
        walk((n.properties as Record<string, unknown>)[k])
      }
    }
    if (Array.isArray(n.enum)) for (const v of n.enum) if (typeof v === 'string') words.add(v)
    if (n.items) walk(n.items)
    if (n.definitions && typeof n.definitions === 'object') {
      for (const k of Object.keys(n.definitions as object)) walk((n.definitions as Record<string, unknown>)[k])
    }
    if (n.additionalProperties && typeof n.additionalProperties === 'object') walk(n.additionalProperties)
  }
  walk(schema)
  for (const k of ['Deployment', 'Service', 'Pod', 'ConfigMap', 'Secret', 'Namespace', 'StatefulSet', 'DaemonSet', 'Ingress', 'ServiceAccount', 'Node', 'PersistentVolumeClaim', 'Job', 'CronJob']) {
    words.add(k)
  }
  return [...words].sort()
}

export interface EditorHandle {
  getValue(): string
  setValue(v: string): void
  destroy(): void
  refresh(): void
}

// createEditor builds a JSON CodeMirror instance with prefix autocomplete over
// the schema vocabulary. Hint fires as you type a word and on Ctrl/Cmd-Space.
export function createEditor(CM: CMFactory, host: HTMLElement, doc: string, words: string[]): EditorHandle {
  const cm = CM(host, {
    value: doc,
    mode: { name: 'javascript', json: true },
    lineNumbers: true,
    autoCloseBrackets: true,
    matchBrackets: true,
    tabSize: 2,
    lineWrapping: true,
    extraKeys: { 'Ctrl-Space': 'autocomplete', 'Cmd-Space': 'autocomplete' },
  })

  const hint = (editor: CMInstance) => {
    const cur = editor.getCursor()
    const line = editor.getLine(cur.line)
    let start = cur.ch
    while (start > 0 && /[\w-]/.test(line.charAt(start - 1))) start--
    const token = line.slice(start, cur.ch).toLowerCase()
    const list = token ? words.filter((w) => w.toLowerCase().includes(token)) : words
    return { list, from: CM.Pos(cur.line, start), to: CM.Pos(cur.line, cur.ch) }
  }
  cm.on('inputRead', (...args: unknown[]) => {
    const change = args[1] as { text?: string[] }
    const first = change.text?.[0] ?? ''
    if (/[A-Za-z]/.test(first)) cm.showHint({ hint, completeSingle: false })
  })

  return {
    getValue: () => cm.getValue(),
    setValue: (v: string) => cm.setValue(v),
    refresh: () => cm.refresh(),
    destroy: () => {
      const w = cm.getWrapperElement()
      w.parentNode?.removeChild(w)
    },
  }
}

export interface Example {
  label: string
  spec: unknown
}

// EXAMPLES seed the editor — one per common shape, ordered simplest-first.
export const EXAMPLES: Example[] = [
  {
    label: 'List deployments (whole fleet)',
    spec: {
      filter: { objects: [{ groupKind: { apiGroup: 'apps', kind: 'Deployment' } }] },
      objects: { cluster: true, object: { metadata: { name: true, namespace: true }, spec: { replicas: true } } },
      limit: 50,
    },
  },
  {
    label: 'Everything in a namespace',
    spec: {
      filter: { objects: [{ namespace: 'default' }] },
      objects: { cluster: true, object: { kind: true, metadata: { name: true, namespace: true } } },
      limit: 100,
    },
  },
  {
    label: 'Restrict to one edge',
    spec: {
      cluster: { name: 'dev-edge-kube-1' },
      objects: { cluster: true, object: { kind: true, metadata: { name: true, namespace: true } } },
      limit: 100,
    },
  },
  {
    label: 'Impact of a ConfigMap (upstream + downstream)',
    spec: {
      filter: { objects: [{ groupKind: { kind: 'ConfigMap' }, namespace: 'default', name: 'app-config' }] },
      objects: {
        cluster: true,
        relations: { references: {}, 'selected-by': {}, namespace: {}, 'descendants+': {} },
      },
    },
  },
  {
    label: 'Per-cluster tree (root=clusters)',
    spec: {
      root: 'clusters',
      objects: {
        cluster: true,
        relations: { members: { limit: 50, objects: { object: { kind: true, metadata: { name: true, namespace: true } } } } },
      },
    },
  },
]
