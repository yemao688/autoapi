// Sets up the Wails v3 JS bridge (`window._wails`), context menus and
// `--wails-draggable` window dragging used by the titlebar. Must be imported
// before the app mounts.
import '@wailsio/runtime'
import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import i18n from './locales'
import './styles.css'

createApp(App).use(router).use(i18n).mount('#app')
