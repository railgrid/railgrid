<script setup>
import { ref } from 'vue'
import AgentRichComposer from '../../providers/agents/portal/src/AgentRichComposer.vue'

const message = ref('')
const parts = ref([])
const outcome = ref('success')
const result = ref('')
const fixture = ref(null)
function attachSample() {
  const input = fixture.value?.querySelector('input[type="file"]')
  if (!input) return
  const transfer = new DataTransfer()
  transfer.items.add(new File(['Example notes for an agent.'], 'review-notes.txt', { type: 'text/plain' }))
  input.files = transfer.files
  input.dispatchEvent(new Event('change', { bubbles: true }))
}
const api = {
  context: () => null,
  async uploadAgentAttachment(_agent, _session, file, _signal, id) {
    if (outcome.value === 'pending') await new Promise((_, reject) => {
      const abort = () => reject(new DOMException('Upload canceled', 'AbortError'))
      if (_signal.aborted) abort()
      else _signal.addEventListener('abort', abort, { once: true })
    })
    if (outcome.value === 'failure') throw new Error('The fixture could not upload this file. Retry or remove it.')
    return { id, filename: file.name, contentType: file.type || 'application/octet-stream', sizeBytes: file.size, sha256: '0'.repeat(64), createdAt: new Date().toISOString() }
  },
  async deleteAgentAttachment() {},
}
</script>

<template>
  <section ref="fixture" class="spec-section spec-stack" aria-labelledby="product-attachment-title">
    <div>
      <h3 id="product-attachment-title">Agents composer in context</h3>
      <p class="spec-note">The composer keeps its file entry, selected-file states, and recovery in the conversation. Choose Add → Files, drop a file, or attach the sample; no file leaves your browser. Switch the simulated service to failure before selecting a file, then switch back and retry. Pending lasts until you remove the file.</p>
    </div>
    <label class="spec-field spec-fit">Simulated upload service
      <select v-model="outcome" class="k-input"><option value="success">Available</option><option value="failure">Unavailable</option><option value="pending">Pending</option></select>
    </label>
    <div><button type="button" class="k-btn k-btn--ghost" @click="attachSample">Attach sample text file</button></div>
    <AgentRichComposer v-model="message" v-model:content-parts="parts" :api="api" agent="research-assistant" owner-key="design-review" draft-storage-key="" draft-scope-key="attachment-review" :skills="[]" placeholder="Ask the agent about your files" @submit="result = 'Message submitted in the fixture; nothing was sent.'" />
    <p class="spec-note" role="status">{{ result }}</p>
  </section>
</template>
