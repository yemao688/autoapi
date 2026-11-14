<script setup lang="ts">
import { onUnmounted } from 'vue'
import { disposeAppVisibility, useAppVisibility } from '@/composables/useAppVisibility'
import { disposeWebViewReset, useWebViewReset } from '@/composables/useWebViewReset'
import AppWindow from '@/components/AppWindow.vue'
import AppToast from '@/components/AppToast.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

// Initialize the app visibility coordinator in the shell so the Wails
// 'app:visibility' event is subscribed once for the application lifetime.
useAppVisibility()

// Periodic WebView reload: the macOS WKWebView render process accumulates
// layer-backing IOSurface buffers WebKit never reclaims (WebKit bugs 296891 /
// 277392). Reloading when idle resets the render process and its surface
// pools, bounding the process memory footprint over long sessions.
useWebViewReset()

onUnmounted(() => {
  disposeAppVisibility()
  disposeWebViewReset()
})
</script>

<template>
  <AppWindow />
  <AppToast />
  <ConfirmDialog />
</template>
