import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'
import wails from '@wailsio/runtime/plugins/vite'
import {fileURLToPath, URL} from 'node:url'

// https://vitejs.dev/config/
export default defineConfig({
  // Wails embeds the built assets — relative base is mandatory
  base: './',
  plugins: [vue(), wails('./bindings')],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  server: {
    // Wails dev bridges to this port; bind all so 0.0.0.0:8344 proxy + LAN work
    host: '0.0.0.0',
    // wails3 dev injects WAILS_VITE_PORT
    port: Number(process.env.WAILS_VITE_PORT) || 5173,
    strictPort: true
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true
  }
})
