import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { initTheme } from '@/stores/theme'
import '@fontsource-variable/instrument-sans'
import '@fontsource-variable/archivo/wdth.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import '@/assets/main.css'
import './review.css'
import App from './Review.vue'

initTheme()
createApp(App).use(createPinia()).mount('#app')
