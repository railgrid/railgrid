// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { expect, it, vi } from 'vitest'
import ProjectGitReview from '../src/ProjectGitReview.vue'

it('requires explicit acceptance of a replacement and retains recovery after skipping', async () => {
  const withGit = ref(true)
  const reviewed = ref('old-connection')
  const current = ref('new-connection')
  const error = ref('The selected Git connection is no longer ready.')
  const retry = vi.fn()
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(ProjectGitReview, {
    withGit: withGit.value, reviewedConnection: reviewed.value, currentConnection: current.value,
    checking: false, readinessError: null, error: error.value, pending: false, connectionsUrl: '/connections',
    'onUpdate:withGit': (value: boolean) => { withGit.value = value },
    onRetry: retry,
    onUseCurrent: () => { reviewed.value = current.value; error.value = '' },
    onSkip: () => { withGit.value = false; reviewed.value = ''; error.value = '' },
  }) })
  const click = (label: string) => [...host.querySelectorAll('button')].find(button => button.textContent?.trim() === label)!.click()
  try {
    app.mount(host)
    expect(host.textContent).toContain('old-connection to new-connection')
    expect(host.textContent).toContain('Git connection: old-connection')
    click('Check again'); expect(retry).toHaveBeenCalledOnce()
    expect(reviewed.value).toBe('old-connection')
    click('Use updated Git connection'); await nextTick()
    expect(reviewed.value).toBe('new-connection')
    expect(withGit.value).toBe(true)
    expect(host.textContent).not.toContain('Use updated Git connection')
    error.value = 'The selected Git connection is no longer ready.'; await nextTick()
    click('Continue without Git'); await nextTick()
    expect(withGit.value).toBe(false)
    expect(reviewed.value).toBe('')
    expect(host.textContent).toContain('Check again')
    expect(host.textContent).toContain('Start without Git')
  } finally { app.unmount(); host.remove() }
})

it('offers an explicit choice when readiness arrives without changing the draft', async () => {
  const withGit = ref(false)
  const reviewed = ref('')
  const current = ref('')
  const checking = ref(true)
  const draft = ref('Build a pantry tracker')
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h('div', [
    h('textarea', { value: draft.value, onInput: (event: Event) => { draft.value = (event.target as HTMLTextAreaElement).value } }),
    h(ProjectGitReview, {
      withGit: withGit.value, reviewedConnection: reviewed.value, currentConnection: current.value,
      checking: checking.value, readinessError: null, error: '', pending: false, connectionsUrl: '/connections',
      'onUpdate:withGit': (value: boolean) => { withGit.value = value },
      onUseCurrent: () => { reviewed.value = current.value; withGit.value = true },
    }),
  ]) })
  try {
    app.mount(host)
    expect(host.querySelector<HTMLInputElement>('input')!.disabled).toBe(true)
    checking.value = false
    current.value = 'github-ready'
    await nextTick()
    expect(withGit.value).toBe(false)
    expect(reviewed.value).toBe('')
    expect(host.textContent).toContain('Git connection available: github-ready')
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Use Git connection')!.click()
    await nextTick()
    expect(withGit.value).toBe(true)
    expect(reviewed.value).toBe('github-ready')
    expect(host.textContent).toContain('Git connection: github-ready')
    expect(host.querySelector<HTMLTextAreaElement>('textarea')!.value).toBe('Build a pantry tracker')
  } finally { app.unmount(); host.remove() }
})
