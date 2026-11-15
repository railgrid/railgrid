<script setup>
import { ref } from 'vue'
import ReviewRequest from '../../providers/app-studio/portal/src/ReviewRequest.vue'
import { parseAssistantInterrupt } from '../../providers/app-studio/portal/src/assistantInterrupt'

const request = parseAssistantInterrupt({
  interruptId: 'sample-question',
  kind: 'follow_up',
  status: 'pending',
  action: { runId: 'sample-run', requestId: 'sample-question' },
})
const questions = [
  { id: 'environment', question: 'Which environment should be reviewed?', options: [{ label: 'Staging' }, { label: 'Production' }] },
  { id: 'format', question: 'How should the findings be presented?', options: [{ label: 'Summary' }, { label: 'Details' }] },
]
const answers = ref({})
const busy = ref(false)
const error = ref('')
const sent = ref(false)
let attempts = 0

function answer(_request, id, value) {
  answers.value = { ...answers.value, [id]: value }
  sent.value = false
}

async function submit() {
  busy.value = true
  error.value = ''
  await new Promise(resolve => setTimeout(resolve, 750))
  busy.value = false
  attempts += 1
  if (attempts === 1) error.value = 'The simulated answer could not be sent. Try again.'
  else sent.value = true
}
</script>

<template>
  <section class="spec-stack spec-section" aria-labelledby="clarification-sample">
    <h3 id="clarification-sample">App Studio clarification</h3>
    <p class="spec-note">The first submission fails. Retry to record the answers in this example.</p>
    <ReviewRequest :interrupt="request" :questions="questions" :answers="answers" :busy="busy" :error="error" @answer="answer" @submit="submit" />
    <p v-if="sent" class="spec-note" role="status">Answers recorded in this example.</p>
  </section>
</template>
