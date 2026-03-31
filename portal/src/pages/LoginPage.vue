<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { API_PATHS } from '@/lib/constants'
import { Hexagon, KeyRound, ShieldCheck, Loader2, AlertCircle, Sun, Moon, Monitor } from 'lucide-vue-next'

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
const inputFocused = ref(false)

onMounted(async () => {
  if (auth.isAuthenticated) {
    router.push('/')
    return
  }
  await auth.detectAuthMode()
})

async function handleTokenLogin() {
  loginError.value = null
  try {
    await auth.loginStatic(tokenInput.value)
    router.push('/')
  } catch (e) {
    loginError.value = e instanceof Error ? e.message : 'Login failed'
  }
}

function handleOIDCLogin() {
  const array = new Uint8Array(32)
  crypto.getRandomValues(array)
  const codeVerifier = btoa(String.fromCharCode(...array))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')

  const sessionId = crypto.randomUUID()

  sessionStorage.setItem('oidc_verifier', codeVerifier)
  sessionStorage.setItem('oidc_session', sessionId)

  const callbackUrl = `${window.location.origin}/auth/callback`
  const params = new URLSearchParams({
    redirect_uri: callbackUrl,
    s: sessionId,
    v: codeVerifier,
  })

  window.location.href = `${API_PATHS.authorize}?${params.toString()}`
}
</script>

<template>
  <div class="cross-grid relative flex min-h-screen bg-surface">
    <!-- Theme toggle -->
    <button
      class="fixed right-4 top-4 z-50 flex h-8 w-8 items-center justify-center rounded-lg border border-border-subtle bg-surface-raised/80 text-text-muted backdrop-blur transition-all hover:border-accent/30 hover:text-text-secondary"
      title="Toggle theme"
      @click="theme.toggle()"
    >
      <component :is="themeIcon" class="h-3.5 w-3.5" :stroke-width="1.75" />
    </button>

    <!-- Ambient -->
    <div class="pointer-events-none fixed inset-0 overflow-hidden">
      <div class="absolute -top-40 left-1/2 h-96 w-[600px] -translate-x-1/2 rounded-full bg-accent/5 blur-[180px]" />
      <div class="absolute bottom-0 right-1/3 h-72 w-72 rounded-full bg-success/3 blur-[140px]" />
    </div>

    <!-- Left decorative panel (hidden on small) -->
    <div class="hidden flex-1 items-center justify-center lg:flex">
      <div class="relative">
        <div class="absolute inset-0 rounded-3xl bg-accent/8 blur-2xl" />
        <div class="dot-grid relative flex h-72 w-72 flex-col items-center justify-center rounded-3xl border border-border-subtle bg-surface-raised/50 backdrop-blur">
          <div class="relative flex h-20 w-20 items-center justify-center">
            <div class="absolute inset-0 rounded-2xl bg-accent/20 blur-lg" />
            <div class="relative flex h-20 w-20 items-center justify-center rounded-2xl border border-accent/25 bg-surface-overlay">
              <Hexagon class="h-10 w-10 text-accent" :stroke-width="1.5" />
            </div>
          </div>
          <span class="text-gradient mt-6 text-2xl font-bold tracking-tight">KEDGE</span>
          <span class="mt-1 text-[10px] font-semibold uppercase tracking-[0.25em] text-text-muted">Command Center</span>
          <div class="energy-line mt-6 h-px w-24" />
        </div>
      </div>
    </div>

    <!-- Right: login form -->
    <div class="relative flex flex-1 items-center justify-center px-6">
      <div class="w-full max-w-sm">
        <!-- Mobile logo -->
        <div class="mb-8 text-center lg:hidden">
          <div class="relative mx-auto flex h-14 w-14 items-center justify-center">
            <div class="absolute inset-0 rounded-xl bg-accent/20 blur-md" />
            <div class="relative flex h-14 w-14 items-center justify-center rounded-xl border border-accent/20 bg-surface-overlay">
              <Hexagon class="h-7 w-7 text-accent" :stroke-width="2" />
            </div>
          </div>
          <h1 class="text-gradient mt-4 text-xl font-bold">KEDGE</h1>
        </div>

        <div class="border-beam rounded-2xl">
          <div class="space-y-5 rounded-2xl border border-border-subtle bg-surface-raised/80 p-7 backdrop-blur">
            <div>
              <h2 class="text-[15px] font-semibold text-text-primary">Sign in</h2>
              <p class="mt-0.5 text-[12px] text-text-muted">Authenticate to access your cluster</p>
            </div>

            <!-- Error -->
            <div v-if="loginError" class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger">
              <AlertCircle class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
              {{ loginError }}
            </div>

            <!-- OIDC -->
            <button
              v-if="auth.authMode === 'both' || auth.authMode === 'oidc'"
              class="group flex w-full items-center justify-center gap-2 rounded-xl bg-accent px-4 py-2.5 text-[13px] font-semibold text-white transition-all duration-200 hover:bg-accent-hover hover:shadow-lg hover:shadow-accent/20 active:scale-[0.98]"
              @click="handleOIDCLogin"
            >
              <ShieldCheck class="h-4 w-4 transition-transform duration-200 group-hover:scale-110" :stroke-width="2" />
              Sign in with SSO
            </button>

            <!-- Divider -->
            <div v-if="auth.authMode === 'both'" class="relative flex items-center">
              <div class="energy-line h-px flex-grow" />
              <span class="mx-3 text-[9px] font-semibold uppercase tracking-[0.2em] text-text-muted">or</span>
              <div class="energy-line h-px flex-grow" />
            </div>

            <!-- Token -->
            <form @submit.prevent="handleTokenLogin" class="space-y-3">
              <div>
                <label for="token" class="mb-1 block text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">Bearer Token</label>
                <div
                  class="glow-ring flex items-center gap-2 rounded-xl border bg-surface-overlay/60 px-3 py-2.5 backdrop-blur transition-all duration-200"
                  :class="inputFocused ? 'active border-accent/40' : 'border-border-default'"
                >
                  <KeyRound class="h-3.5 w-3.5 shrink-0 text-text-muted transition-colors" :class="{ 'text-accent': inputFocused }" :stroke-width="1.75" />
                  <input
                    id="token"
                    v-model="tokenInput"
                    type="password"
                    placeholder="Paste token here"
                    class="w-full bg-transparent font-mono text-[12px] text-text-primary placeholder-text-muted outline-none"
                    @focus="inputFocused = true"
                    @blur="inputFocused = false"
                  />
                </div>
              </div>
              <button
                type="submit"
                :disabled="!tokenInput || auth.loading"
                class="group flex w-full items-center justify-center gap-2 rounded-xl border border-border-default bg-surface-overlay/60 px-4 py-2.5 text-[12px] font-semibold text-text-primary backdrop-blur transition-all duration-200 hover:border-accent/30 hover:bg-surface-hover active:scale-[0.98] disabled:pointer-events-none disabled:opacity-30"
              >
                <Loader2
                  v-if="auth.loading"
                  class="h-3.5 w-3.5 animate-spin text-accent"
                  :stroke-width="2"
                />
                <KeyRound
                  v-else
                  class="h-3.5 w-3.5 text-text-muted group-hover:text-accent"
                  :stroke-width="1.75"
                />
                {{ auth.loading ? 'Signing in...' : 'Sign in with Token' }}
              </button>
            </form>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
