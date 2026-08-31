<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { BookOpen, CircleHelp, ExternalLink, Github, MessageCircle, X } from 'lucide-vue-next'
import { useEscapeKey } from '@/composables/useEscapeKey'

const emit = defineEmits<{ close: [] }>()

const docsURL = 'https://faros.sh/docs/'
const discordURL = 'https://discord.gg/VjUA7zyhC'
const issuesURL = 'https://github.com/faroshq/faros/issues'

useEscapeKey(() => emit('close'))

const dialogRef = ref<HTMLElement | null>(null)
const closeButton = ref<HTMLButtonElement | null>(null)
let previousFocus: HTMLElement | null = null

function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Tab') return
  const focusable = Array.from(dialogRef.value?.querySelectorAll<HTMLElement>(
    'button:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
  ) ?? [])
  if (!focusable.length) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

onMounted(() => {
  previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  window.addEventListener('keydown', onKeydown)
  nextTick(() => closeButton.value?.focus())
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  const target = previousFocus
  previousFocus = null
  nextTick(() => target?.isConnected && target.focus())
})
</script>

<template>
  <Teleport to="body">
    <div class="k-modal-overlay" @click.self="$emit('close')">
      <section
        id="help-support-dialog"
        ref="dialogRef"
        class="k-modal w-full max-w-xl max-h-[90vh] overflow-y-auto p-0"
        role="dialog"
        aria-modal="true"
        aria-labelledby="help-support-title"
        aria-describedby="help-support-description"
      >
        <header class="flex items-center gap-3 border-b border-border-subtle bg-surface-overlay/60 px-4 py-3.5 sm:px-5">
          <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-accent/30 bg-accent-subtle text-accent" aria-hidden="true">
            <CircleHelp class="h-[18px] w-[18px]" :stroke-width="1.75" />
          </span>
          <div class="min-w-0 flex-1">
            <h2 id="help-support-title" class="text-[15px] font-bold text-text-primary">Help &amp; community</h2>
            <p id="help-support-description" class="mt-0.5 text-[11px] leading-relaxed text-text-secondary">
              Find an answer, ask the community, or report a problem.
            </p>
          </div>
          <button
            ref="closeButton"
            type="button"
            class="k-btn k-btn--ghost flex h-8 w-8 shrink-0 items-center justify-center p-0 text-text-muted transition-colors hover:text-text-primary"
            aria-label="Close help and community dialog"
            @click="$emit('close')"
          >
            <X class="h-4 w-4" :stroke-width="2" aria-hidden="true" />
          </button>
        </header>

        <div class="p-4 sm:p-5">
          <a
            :href="docsURL"
            target="_blank"
            rel="noreferrer noopener"
            class="group grid min-h-[92px] grid-cols-[auto_minmax(0,1fr)] items-center gap-3 rounded-lg border border-accent/30 bg-accent-subtle/60 p-3.5 text-left transition-colors hover:border-accent/50 hover:bg-accent-subtle focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30 sm:grid-cols-[auto_minmax(0,1fr)_auto]"
          >
            <span class="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-accent text-on-accent" aria-hidden="true">
              <BookOpen class="h-[18px] w-[18px]" :stroke-width="1.75" />
            </span>
            <span class="min-w-0">
              <span class="block text-[12px] font-semibold text-text-primary">Documentation</span>
              <span class="mt-0.5 block text-[11px] leading-relaxed text-text-secondary">
                Guides, setup, CLI reference, providers, and architecture.
              </span>
            </span>
            <span class="col-start-2 flex items-center gap-1.5 text-[10px] font-semibold text-accent-hover sm:col-start-auto">
              Explore docs
              <ExternalLink class="h-3 w-3" :stroke-width="2" aria-hidden="true" />
            </span>
          </a>

          <div class="my-4 flex items-center gap-2 font-mono text-[9px] font-medium uppercase tracking-[0.15em] text-text-muted after:h-px after:flex-1 after:bg-border-subtle">
            More ways to get help
          </div>

          <div class="overflow-hidden rounded-lg border border-border-subtle bg-surface-overlay/40">
            <a
              :href="discordURL"
              target="_blank"
              rel="noreferrer noopener"
              class="group grid min-h-[78px] grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 p-3.5 transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/30"
            >
              <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay text-text-secondary" aria-hidden="true">
                <MessageCircle class="h-[18px] w-[18px]" :stroke-width="1.75" />
              </span>
              <span class="min-w-0">
                <span class="block text-[12px] font-semibold text-text-primary">Discord community</span>
                <span class="mt-0.5 block text-[11px] leading-relaxed text-text-secondary">
                  Ask questions and compare approaches with people building Faros.
                </span>
              </span>
              <span class="flex items-center gap-1.5 text-[10px] font-semibold text-text-secondary transition-colors group-hover:text-text-primary">
                <span class="hidden sm:inline">Join Discord</span>
                <ExternalLink class="h-3 w-3" :stroke-width="2" aria-hidden="true" />
              </span>
            </a>

            <a
              :href="issuesURL"
              target="_blank"
              rel="noreferrer noopener"
              class="group grid min-h-[78px] grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 border-t border-border-subtle p-3.5 transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/30"
            >
              <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay text-text-secondary" aria-hidden="true">
                <Github class="h-[18px] w-[18px]" :stroke-width="1.75" />
              </span>
              <span class="min-w-0">
                <span class="block text-[12px] font-semibold text-text-primary">GitHub issues</span>
                <span class="mt-0.5 block text-[11px] leading-relaxed text-text-secondary">
                  Search known problems or file a reproducible issue.
                </span>
              </span>
              <span class="flex items-center gap-1.5 text-[10px] font-semibold text-text-secondary transition-colors group-hover:text-text-primary">
                <span class="hidden sm:inline">Browse issues</span>
                <ExternalLink class="h-3 w-3" :stroke-width="2" aria-hidden="true" />
              </span>
            </a>
          </div>
        </div>

      </section>
    </div>
  </Teleport>
</template>
