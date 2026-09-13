import { computed, onActivated, onBeforeUnmount, onDeactivated, ref, watch } from 'vue';
import { useSession, useTask } from './state';
import { linkedIssue, readFactory, type FactoryScope, type FactorySnapshot } from './factory';

/** One batch per visible issue surface, cancelled by the same workspace/view fence as Linear reads. */
export function useFactory(scope: () => FactoryScope, revision: () => number = () => 0) {
  const session = useSession(); const read = useTask(); const snapshot = ref<FactorySnapshot>();
  const supported = computed(() => !!(session.context().orgUUID && session.context().workspaceUUID && session.context().tenant && scope().connection));
  function load() {
    if (!supported.value) return;
    const context = session.context(); const current = { ...scope() };
    void read.run(api => readFactory(api, context, current), value => { snapshot.value = value; });
  }
  watch(() => [session.context().orgUUID, session.context().workspaceUUID, session.context().tenant, scope().connection, scope().team, scope().issue], () => { read.reset(); snapshot.value = undefined; load(); }, { immediate: true, flush: 'sync' });
  watch(revision, load);
  let timer: ReturnType<typeof setInterval> | undefined;
  function start() { if (!timer) timer = setInterval(() => { if (!document.hidden) load(); }, 30000); }
  function stop() { clearInterval(timer); timer = undefined; }
  start(); onActivated(() => { load(); start(); }); onDeactivated(stop); onBeforeUnmount(stop);
  // A same-scope refresh retains its snapshot with a refresh notice; failures hide old badges.
  const tasks = computed(() => !read.state.error && snapshot.value?.enabled ? snapshot.value.tasks : []);
  const byIssue = computed(() => {
    const result: Record<string, typeof tasks.value> = {};
    for (const task of tasks.value) {
      const auth = task.spec?.execution?.approvedInput?.authorization;
      const uid = auth?.connectionUID || task.status?.linear?.connectionUID || '';
      const id = linkedIssue(task, scope().connection, uid, scope().team);
      if (id) (result[id] ||= []).push(task);
    }
    return result;
  });
  return { supported, read, snapshot, tasks, byIssue, load };
}
