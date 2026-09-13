import { onBeforeUnmount, onDeactivated, ref } from 'vue';
import type { Resource } from './api';
import { useSession, useTask } from './state';
import { confirmDialog, resolveConfirm } from './portalkit/confirm';

function useDeletion(kind: 'connection' | 'team', deleted: (resource: Resource) => void) {
  const session = useSession(); const task = useTask(); const pending = ref('');
  let confirming = false;
  function cancelConfirmation() { if (confirming) { confirming = false; resolveConfirm(false); } }
  onBeforeUnmount(cancelConfirmation); onDeactivated(cancelConfirmation);
  async function remove(resource: Resource) {
    if (confirming || task.state.loading || resource.metadata.deletionTimestamp) return;
    confirming = true;
    const ok = await confirmDialog({ title: kind === 'team' ? `Remove team "${resource.status?.name || resource.metadata.name}" from Faros?` : `Delete connection "${resource.metadata.name}"?`, message: kind === 'team' ? 'The team and its issues remain in Linear. This registration will no longer grant access for new Faros operations. In-flight operations may still finish.' : 'Faros will stop using this connection. Its owned API-key Secret and Team registrations will also be removed. Issues in Linear are not deleted. In-flight operations may still finish.', confirmLabel: kind === 'team' ? 'Remove' : 'Delete', danger: true });
    confirming = false;
    if (!ok || session.signal.aborted) return;
    pending.value = resource.metadata.name;
    await task.run(api => kind === 'team' ? api.removeTeam(resource.metadata.name, resource.metadata.uid || '') : api.deleteConnection(resource.metadata.name, resource.metadata.uid || ''), () => {
      if ((kind === 'connection' && session.selection.connection === resource.metadata.name) || (kind === 'team' && session.selection.teamResource === resource.metadata.name)) { session.selection.connection = ''; session.selection.team = ''; session.selection.teamResource = ''; }
      deleted(resource);
    });
    pending.value = '';
  }
  return { ...task, pending, remove };
}
export const useConnectionDeletion = (deleted: (resource: Resource) => void) => useDeletion('connection', deleted);
export const useTeamDeletion = (deleted: (resource: Resource) => void) => useDeletion('team', deleted);
