<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { consumePortalNext, rememberPortalNext } from '@/auth/portalNext'
import { consumeAppAccessNext, peekAppAccessNext, rememberAppAccessNext } from '@/auth/appAccessNext'
import { API_PATHS } from '@/lib/constants'
import { AlertCircle, Hexagon, Loader2, Monitor, Moon, Sun } from 'lucide-vue-next'

const auth = useAuthStore()
const theme = useThemeStore()
const themeIcon = computed(() => {
  if (theme.mode === 'light') return Sun
  if (theme.mode === 'dark') return Moon
  return Monitor
})
const router = useRouter()
const tokenInput = ref('')
const loginError = ref<string | null>(null)
const tokenField = ref<HTMLInputElement | null>(null)
const showTokenForm = ref(false)

const oidcAvailable = computed(
  () => auth.authMode === 'oidc' || auth.authMode === 'both',
)
// Interactive token login exists only when the hub offers it; OIDC-only hubs
// (authMode 'oidc') ask for a username and password at the IdP instead.
const tokenAvailable = computed(
  () => auth.authMode === 'token' || auth.authMode === 'both',
)
const tokenFormVisible = computed(
  () => tokenAvailable.value && (!oidcAvailable.value || showTokenForm.value),
)

onMounted(async () => {
  const search = new URLSearchParams(window.location.search)
  const switching = search.get('switch') === '1'
  // A private published app sent the browser here to establish the shared
  // hub session; remember the hub-relative continuation across the flow.
  rememberAppAccessNext(search.get('next'))
  rememberPortalNext(search.get('returnTo'))
  // Login is public, so the protected-route guard cannot serve as the
  // browser-session barrier for a cached portal bearer. App.vue shares this
  // initialization promise when it starts its own mount-time check.
  await auth.detectAuthMode()
  if (auth.token && !switching) {
    if (peekAppAccessNext() && !auth.browserSessionReady) {
      // Preserve the app destination, but do not retry a rejected cached login.
      auth.logout()
      loginError.value = 'Could not restore the browser session. Sign in again to continue.'
      return
    }
    const next = consumeAppAccessNext()
    if (next) {
      window.location.assign(next)
      return
    }
    router.replace(consumePortalNext() ?? '/')
    return
  }
  if (switching && oidcAvailable.value) handleOIDCLogin(true)
})

function resumeAfterLogin() {
  const next = consumeAppAccessNext()
  if (next) {
    window.location.assign(next)
    return true
  }
  return false
}

async function handleTokenLogin() {
  if (!tokenInput.value || auth.loading) return
  loginError.value = null
  try {
    await auth.loginStatic(tokenInput.value)
    if (resumeAfterLogin()) return
    router.replace(consumePortalNext() ?? '/')
  } catch (e) {
    loginError.value = e instanceof Error ? e.message : 'Login failed'
  }
}

function handleOIDCLogin(force = false) {
  const array = new Uint8Array(32)
  crypto.getRandomValues(array)
  const codeVerifier = btoa(String.fromCharCode(...array))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')

  const sessionId = crypto.randomUUID()

  sessionStorage.setItem('oidc_verifier', codeVerifier)
  sessionStorage.setItem('oidc_session', sessionId)

  const callbackUrl = `${window.location.origin}${import.meta.env.BASE_URL}auth/callback`
  const params = new URLSearchParams({
    redirect_uri: callbackUrl,
    s: sessionId,
    v: codeVerifier,
  })
  if (force) params.set('force', '1')

  window.location.href = `${API_PATHS.authorize}?${params.toString()}`
}

function startOIDCLogin() {
  handleOIDCLogin(false)
}

async function revealTokenForm() {
  showTokenForm.value = true
  await nextTick()
  tokenField.value?.focus()
}
</script>

<template>
  <main class="relative grid min-h-dvh place-items-center bg-surface px-6 py-20">
    <div class="absolute right-4 top-4">
      <button
        type="button"
        class="k-icon-action"
        :aria-label="`Toggle theme (current: ${theme.mode})`"
        :title="`Toggle theme (current: ${theme.mode})`"
        @click="theme.toggle()"
      >
        <component :is="themeIcon" :stroke-width="1.75" aria-hidden="true" />
      </button>
    </div>

    <div class="w-full max-w-sm">
      <div class="mb-9 text-center">
        <div class="inline-flex items-center gap-2.5 text-text-primary">
          <Hexagon class="size-7 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
          <span class="type-display text-2xl/none font-bold">RAILGRID</span>
        </div>
        <div class="mx-auto mt-4 h-2 w-16 border-y border-border-default" aria-hidden="true" />
      </div>

      <div class="text-center">
        <h1 class="text-2xl font-semibold tracking-tight text-text-primary">Sign in to Railgrid</h1>
        <p class="mt-2 text-sm/5 text-balance text-text-secondary">
          Build apps, run AI agents, and work with connected data and infrastructure.
        </p>
      </div>

      <div class="mt-6 space-y-3">
        <div
          v-if="loginError"
          id="login-error"
          class="flex items-start gap-2 rounded-md bg-danger-subtle p-3 text-sm text-danger"
          role="alert"
        >
          <AlertCircle class="mt-0.5 size-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
          {{ loginError }}
        </div>

        <button
          v-if="oidcAvailable"
          type="button"
          class="k-btn k-btn--primary min-h-11 w-full"
          @click="startOIDCLogin"
        >
          Sign in with SSO
        </button>

        <button
          v-if="oidcAvailable && tokenAvailable && !showTokenForm"
          type="button"
          class="k-btn k-btn--text w-full"
          @click="revealTokenForm"
        >
          Use bearer token instead
        </button>

        <div v-if="oidcAvailable && tokenFormVisible" class="flex items-center gap-3 text-xs text-text-secondary">
          <span class="h-px flex-1 bg-border-default" aria-hidden="true" />
          or
          <span class="h-px flex-1 bg-border-default" aria-hidden="true" />
        </div>

        <form v-if="tokenFormVisible" class="space-y-3" @submit.prevent="handleTokenLogin">
          <div>
            <label for="token" class="mb-1 block text-sm font-medium text-text-primary">Bearer token</label>
            <input
              id="token"
              ref="tokenField"
              v-model="tokenInput"
              type="password"
              placeholder="Paste token here"
              class="k-input w-full font-mono"
              :aria-describedby="loginError ? 'login-error' : undefined"
              :aria-invalid="loginError ? 'true' : undefined"
            />
          </div>
          <button
            type="submit"
            class="k-btn min-h-11 w-full"
            :class="oidcAvailable ? 'k-btn--ghost' : 'k-btn--primary'"
            :disabled="!tokenInput"
            :aria-disabled="auth.loading || undefined"
            :aria-busy="auth.loading || undefined"
          >
            <Loader2 v-if="auth.loading" class="k-spin size-4" :stroke-width="1.75" aria-hidden="true" />
            {{ auth.loading ? 'Signing in…' : 'Sign in with token' }}
          </button>
        </form>
      </div>
    </div>
  </main>
</template>
