import { computed, inject, onActivated, onBeforeUnmount, onDeactivated, reactive, type InjectionKey } from 'vue';
import { API, ConnectionError, OperationError, type FarosContext } from './api';
export type Session = {
  context: () => FarosContext;
  signal: AbortSignal;
  writes: Record<string, import('./api').WriteIntent>;
  draft: { title: string; description: string; stateID: string; returnToIssue: boolean };
  selection: { connection: string; team: string; query: string; teamResource?: string };
  navigate: (path: string, replace?: boolean) => void;
  href: (path: string) => string;
};
export const sessionKey: InjectionKey<Session> = Symbol('linear-session');
export function useSession() { const session = inject(sessionKey); if (!session) throw new Error('Missing Linear session'); return session; }
/** Each view owns cancellation; the session also fences authority changes synchronously. */
export function useTask() {
  const session = useSession();
  const state = reactive({ loading: false, loaded: false, error: '', operationName: '', connectionName: '', connectionPartial: false, message: '' });
  let controller = new AbortController();
  let activeView = true;
  function cancel() { controller.abort(); controller = new AbortController(); state.loading = false; }
  onBeforeUnmount(() => { activeView = false; cancel(); });
  onDeactivated(() => { activeView = false; cancel(); });
  onActivated(() => { activeView = true; });
  async function run<T>(work: (api: API) => Promise<T>, commit: (value: T) => void = () => {}) {
    if (!activeView || state.loading) return;
    const active = controller;
    const signal = AbortSignal.any([active.signal, session.signal]);
    state.loading = true; state.error = ''; state.operationName = ''; state.connectionName = ''; state.connectionPartial = false; state.message = '';
    try {
      const api = new API(session.context(), signal, session.writes);
      const result = await work(api);
      signal.throwIfAborted();
      commit(result); api.acknowledgeWrites(); state.loaded = true;
    } catch (error) {
      if (!signal.aborted) {
        state.error = error instanceof Error ? error.message : 'Request failed.';
        if (error instanceof ConnectionError) { state.connectionName = error.connectionName; state.connectionPartial = error.partial; }
        if (error instanceof OperationError) state.operationName = error.operationName;
      }
    } finally { if (controller === active) state.loading = false; }
  }
  function reset() { cancel(); state.loaded = false; state.error = ''; state.operationName = ''; state.connectionName = ''; state.connectionPartial = false; state.message = ''; }
  return { state, run, cancel, reset };
}
export function updateFields(title: string, description: string, stateID: string): Record<string, string> {
  return { ...(title ? { title } : {}), ...(description ? { description } : {}), ...(stateID ? { stateID } : {}) };
}

/** Pending write identities belong to the workspace, while polling belongs to the view. */
export function useWriteTask(key: () => string) {
  const session = useSession(); const task = useTask();
  const pending = computed(() => session.writes[key()]?.name || '');
  const connection = computed(() => session.writes[key()]?.connection || '');
  function resume(commit: (value: import('./api').Result) => void) {
    const name = pending.value;
    if (name) void task.run(api => api.resumeOperation(name), commit);
  }
  function separate() {
    if (task.state.loading) return;
    delete session.writes[key()]; task.reset();
  }
  return { ...task, pending, connection, resume, separate };
}
