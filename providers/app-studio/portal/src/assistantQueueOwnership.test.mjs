import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'
import { effectScope, ref, computed } from 'vue'

async function assistantQueueHarness(t, initialQueue, initialRun = null) {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
  const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const functions = new Set([
    'assistantMessageQueueScope',
    'invalidateAssistantQueueOperations',
    'beginAssistantQueueOperation',
    'assistantQueueOperationIsCurrent',
    'canRemoveAcceptedQueuedMessage',
    'removeQueuedAssistantMessage',
    'steerQueuedAssistantMessage',
    'deliverNextQueuedAssistantMessage',
  ])
  const statements = ast.statements.filter(node => ts.isFunctionDeclaration(node) && functions.has(node.name?.text))
  assert.equal(statements.length, functions.size)
  const { outputText } = ts.transpileModule(statements.map(node => node.getText(ast)).join('\n'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  })
  const scope = effectScope()
  t.after(() => scope.stop())
  const sendCalls = []
  return scope.run(() => new Function('ref', 'computed', 'outputText', 'sendCalls', 'initialQueue', 'initialRun', `
    const props = { ctx: { tenant: 'tenant-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a', user: { sub: 'alice' }, subPath: '/project-a' } };
    const selected = ref({ name: 'project-a', uid: 'uid-a' });
    const activeAssistantThreadID = ref('thread-a');
    let assistantThreadRequestSerial = 1;
    let assistantQueueOperationGeneration = 0;
    const queuedAssistantMessages = ref(initialQueue.slice());
    const queuedAssistantSteeringID = ref('');
    const queuedAssistantDeliveryBusy = ref(false);
    const prompt = ref('');
    const assistantComposerParts = ref([]);
    const selectedTurnSkills = ref([]);
    const selectedTurnResources = ref([]);
    const messageStreaming = ref(false);
    const busy = ref(false);
    const conversationInteractionBusy = ref(false);
    const assistantResumeBusy = ref(false);
    let activeAssistantRun = initialRun;
    const key = (scope) => JSON.stringify([scope.tenant, scope.orgUUID, scope.workspaceUUID, scope.user, scope.project, scope.thread]);
    const storage = new Map();
    const assistantMessageQueueStorageKey = key;
    const readAssistantMessageQueue = (scope) => storage.get(key(scope))?.slice() ?? [];
    const writeAssistantMessageQueue = (scope, messages) => {
      if (messages.length) storage.set(key(scope), messages.slice());
      else storage.delete(key(scope));
      return true;
    };
    const projectContextFingerprint = (ctx) => JSON.stringify([ctx.tenant, ctx.orgUUID, ctx.workspaceUUID, ctx.user?.sub, ctx.subPath]);
    const activeAssistantMessageQueueScopeKey = computed(() => assistantMessageQueueStorageKey(assistantMessageQueueScope()));
    const assistantRunTerminal = () => false;
    const clearSelectedTurnAttachments = () => {
      assistantComposerParts.value = [];
      selectedTurnSkills.value = [];
      selectedTurnResources.value = [];
    };
    const sendMessage = (intent) => new Promise((resolve, reject) => {
      prompt.value = '';
      assistantComposerParts.value = [];
      selectedTurnSkills.value = [];
      selectedTurnResources.value = [];
      sendCalls.push({ intent, resolve, reject });
    });
    ${outputText}
    const currentScope = () => assistantMessageQueueScope();
    writeAssistantMessageQueue(currentScope(), initialQueue);
    return {
      prompt, selected, activeAssistantThreadID, queuedAssistantMessages,
      queuedAssistantSteeringID, queuedAssistantDeliveryBusy, assistantComposerParts,
      selectedTurnSkills, selectedTurnResources, messageStreaming, sendCalls, storage,
      scopeKey: (scope = currentScope()) => assistantMessageQueueStorageKey(scope),
      scope: currentScope,
      readQueue: (scope) => readAssistantMessageQueue(scope),
      steerQueuedAssistantMessage,
      deliverNextQueuedAssistantMessage,
      navigate({ context, project, thread, queue, run = null, streaming = false }) {
        invalidateAssistantQueueOperations();
        assistantThreadRequestSerial += 1;
        props.ctx = context;
        selected.value = project;
        activeAssistantThreadID.value = thread;
        activeAssistantRun = run;
        messageStreaming.value = streaming;
        queuedAssistantMessages.value = queue.slice();
        writeAssistantMessageQueue(currentScope(), queue);
        prompt.value = '';
        assistantComposerParts.value = [];
      },
    };
  `)(ref, computed, outputText, sendCalls, initialQueue, initialRun))
}

function queued(id, content) {
  return { id, content, createdAt: '2026-01-01T00:00:00.000Z' }
}

test('late accepted queue delivery removes only its captured queue and cannot release the next delivery latch', async t => {
  const first = queued('queued-a', 'send from A')
  const second = queued('queued-b', 'send from B')
  const h = await assistantQueueHarness(t, [first])
  h.prompt.value = ''
  const sendA = h.deliverNextQueuedAssistantMessage()
  assert.equal(h.queuedAssistantDeliveryBusy.value, true)
  assert.equal(h.sendCalls.length, 1)

  h.navigate({
    context: { tenant: 'tenant-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a', user: { sub: 'alice' }, subPath: '/project-b' },
    project: { name: 'project-b', uid: 'uid-b' },
    thread: 'thread-b',
    queue: [second],
  })
  h.prompt.value = ''
  const sendB = h.deliverNextQueuedAssistantMessage()
  assert.equal(h.sendCalls.length, 2)
  assert.equal(h.queuedAssistantDeliveryBusy.value, true)

  h.sendCalls[0].resolve(true)
  await sendA
  assert.deepEqual(h.readQueue({ tenant: 'tenant-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a', user: 'alice', project: 'project-a', thread: 'thread-a' }), [])
  assert.deepEqual(h.queuedAssistantMessages.value, [second], 'old cleanup must not replace the active queue')
  assert.equal(h.prompt.value, '', 'old cleanup must not restore its prompt over the new delivery')
  assert.equal(h.queuedAssistantDeliveryBusy.value, true, 'old finally must not release the new delivery latch')

  h.sendCalls[1].resolve(false)
  await sendB
  assert.equal(h.prompt.value, second.content)
  assert.deepEqual(h.queuedAssistantMessages.value, [])
  assert.equal(h.queuedAssistantDeliveryBusy.value, false)
})

test('late rejected queue delivery leaves the original queue and preserves new conversation draft and latch', async t => {
  const first = queued('queued-a', 'send from A')
  const second = queued('queued-b', 'send from B')
  const h = await assistantQueueHarness(t, [first])
  h.prompt.value = ''
  const sendA = h.deliverNextQueuedAssistantMessage()

  h.navigate({
    context: { tenant: 'tenant-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a', user: { sub: 'alice' }, subPath: '/project-b' },
    project: { name: 'project-b', uid: 'uid-b' },
    thread: 'thread-b',
    queue: [second],
  })
  h.prompt.value = ''
  const sendB = h.deliverNextQueuedAssistantMessage()
  h.prompt.value = 'draft typed in B'

  h.sendCalls[0].resolve(false)
  await sendA
  assert.equal(h.prompt.value, 'draft typed in B')
  assert.deepEqual(h.queuedAssistantMessages.value, [second])
  assert.equal(h.queuedAssistantDeliveryBusy.value, true)

  h.sendCalls[1].resolve(false)
  await sendB
  assert.equal(h.prompt.value, 'draft typed in B')
})

test('stale queued steering cannot restore its draft or clear a newer steering owner', async t => {
  const first = queued('steer-a', 'steer A')
  const second = queued('steer-b', 'steer B')
  const h = await assistantQueueHarness(t, [first], { id: 'run-a', status: 'running' })
  h.messageStreaming.value = true
  h.prompt.value = 'draft from A'
  h.assistantComposerParts.value = [{ type: 'text', text: 'draft from A' }]
  const steerA = h.steerQueuedAssistantMessage(first)
  assert.equal(h.queuedAssistantSteeringID.value, first.id)

  h.navigate({
    context: { tenant: 'tenant-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a', user: { sub: 'alice' }, subPath: '/project-b' },
    project: { name: 'project-b', uid: 'uid-b' },
    thread: 'thread-b',
    queue: [second],
    run: { id: 'run-b', status: 'running' },
    streaming: true,
  })
  h.prompt.value = 'draft from B'
  h.assistantComposerParts.value = [{ type: 'text', text: 'draft from B' }]
  const steerB = h.steerQueuedAssistantMessage(second)
  assert.equal(h.queuedAssistantSteeringID.value, second.id)
  h.prompt.value = 'newer draft from B'
  h.assistantComposerParts.value = [{ type: 'text', text: 'newer draft from B' }]

  h.sendCalls[0].resolve(true)
  await steerA
  assert.equal(h.prompt.value, 'newer draft from B')
  assert.equal(h.queuedAssistantSteeringID.value, second.id, 'old finally must not clear the new steering latch')
  assert.deepEqual(h.queuedAssistantMessages.value, [second])

  h.sendCalls[1].resolve(true)
  await steerB
  assert.equal(h.prompt.value, 'newer draft from B', 'a draft authored during steering wins over the captured draft')
  assert.equal(h.queuedAssistantSteeringID.value, '')
  assert.deepEqual(h.queuedAssistantMessages.value, [])
})

test('same-name Project recreation fences old queue completion from the reused storage scope', async t => {
  const first = queued('queued-old', 'old project queue')
  const replacement = queued('queued-new', 'new project queue')
  const h = await assistantQueueHarness(t, [first])
  h.prompt.value = ''
  const oldDelivery = h.deliverNextQueuedAssistantMessage()
  h.navigate({
    context: { tenant: 'tenant-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a', user: { sub: 'alice' }, subPath: '/project-a' },
    project: { name: 'project-a', uid: 'uid-a-recreated' },
    thread: 'thread-a',
    queue: [replacement],
  })
  h.prompt.value = ''
  const newDelivery = h.deliverNextQueuedAssistantMessage()
  h.sendCalls[0].resolve(true)
  await oldDelivery
  assert.deepEqual(h.queuedAssistantMessages.value, [replacement])
  assert.equal(h.queuedAssistantDeliveryBusy.value, true)
  h.sendCalls[1].resolve(false)
  await newDelivery
})
