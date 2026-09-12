import { inject, onActivated, onBeforeUnmount, onDeactivated, reactive, type InjectionKey } from 'vue';
import { API, OperationError, type FarosContext } from './api';
export type Session = {
  context: () => FarosContext;
  namespace: string;
  signal: AbortSignal;
  selection: { connection: string; team: string; query: string };
  navigate: (path: string, replace?: boolean) => void;
  href: (path: string) => string;
};
export const sessionKey: InjectionKey<Session> = Symbol('linear-session');
export function useSession() { const session = inject(sessionKey); if (!session) throw new Error('Missing Linear session'); return session; }
/** Each view owns cancellation; the session also fences authority changes synchronously. */
export function useTask() {
  const session = useSession();
  const state = reactive({ loading: false, loaded: false, error: '', operationName: '', message: '' });
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
    state.loading = true; state.error = ''; state.operationName = ''; state.message = '';
    try {
      const result = await work(new API(session.context(), session.namespace, signal));
      signal.throwIfAborted();
      commit(result); state.loaded = true;
    } catch (error) {
      if (!signal.aborted) {
        state.error = error instanceof Error ? error.message : 'Request failed.';
        if (error instanceof OperationError) state.operationName = error.operationName;
      }
    } finally { if (controller === active) state.loading = false; }
  }
  function reset() { cancel(); state.loaded = false; state.error = ''; state.operationName = ''; state.message = ''; }
  return { state, run, cancel, reset };
}
export function updateFields(title: string, description: string, stateID: string): Record<string, string> {
  return { ...(title ? { title } : {}), ...(description ? { description } : {}), ...(stateID ? { stateID } : {}) };
}
