<script setup lang="ts">
import { computed } from 'vue'
import type { KedgeContext } from './types'

// PR A ships a placeholder shell that routes on subPath. The real Connections
// (connect GitHub, paste PAT, validation status) and Repositories
// (list/create/delete + deploy keys + collaborators) views land in PR C.
//
//   ''  | 'connections'  → Connections
//   'repositories'       → Repositories
const props = defineProps<{ ctx: KedgeContext | null }>()

type Page = 'connections' | 'repositories'

const page = computed<Page>(() => {
  const s = (props.ctx?.subPath ?? '').replace(/^\/+|\/+$/g, '').split('/')[0]
  return s === 'repositories' ? 'repositories' : 'connections'
})

function navigate(sub: Page) {
  // Mirror the infra provider: dispatch a bubbling kedge-navigate CustomEvent
  // so the shell's ProviderFrame router keeps the browser URL in sync.
  document.dispatchEvent(new CustomEvent('kedge-navigate', { bubbles: true, detail: { subPath: sub } }))
}
</script>

<template>
  <div class="code-shell">
    <nav class="code-tabs">
      <button :class="{ active: page === 'connections' }" @click="navigate('connections')">Connections</button>
      <button :class="{ active: page === 'repositories' }" @click="navigate('repositories')">Repositories</button>
    </nav>

    <section v-if="page === 'connections'" class="code-panel">
      <h2>Connections</h2>
      <p class="code-muted">
        Connect a git account (GitHub) by pasting a personal access token. The
        provider validates it and shows the authenticated login here.
      </p>
      <p class="code-todo">Connection management UI arrives in PR C.</p>
    </section>

    <section v-else class="code-panel">
      <h2>Repositories</h2>
      <p class="code-muted">
        Create and manage repositories under a connected account, plus deploy
        keys and collaborators.
      </p>
      <p class="code-todo">Repository management UI arrives in PR C.</p>
    </section>
  </div>
</template>
