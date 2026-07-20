import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { registerProviderRoutes } from './router/providers'
import { initTheme } from './stores/theme'
// Self-hosted webfonts. Imported before main.css so @font-face rules are
// registered ahead of the stylesheet that references them.
import '@fontsource-variable/instrument-sans' // UI / body
import '@fontsource-variable/archivo/wdth.css' // display / KPI numerals (width axis)
import '@fontsource/ibm-plex-mono/400.css' // data / mono
import '@fontsource/ibm-plex-mono/500.css'
import './assets/main.css'

// Apply theme before mount to prevent flash
initTheme()

// Register the dynamic /providers/:name/:rest(.*)* matcher BEFORE the
// router resolves the initial URL. If it lands in App.vue's setup instead
// (the previous arrangement), a hard refresh of /ui/providers/{name} can
// race the initial navigation: Vue Router falls through to the
// /:pathMatch(.*)* catch-all and renders NotFoundPage. Doing it here, before
// app.use(router), removes the race entirely — addRoute is idempotent via
// an internal guard so the App.vue call (still present) is a no-op.
registerProviderRoutes(router)

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')
