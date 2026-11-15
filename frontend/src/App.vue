<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { Events } from '@wailsio/runtime'
import { disposeAppVisibility, useAppVisibility } from '@/composables/useAppVisibility'
import { disposeWebViewReset, useWebViewReset } from '@/composables/useWebViewReset'
import { UPDATE_CHECK_EVENT, disposeUpdateFlow, useUpdateFlow } from '@/composables/useUpdateFlow'
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

const router = useRouter()
const { check: checkForUpdate } = useUpdateFlow()

// The menu / tray 检查更新 entries emit this app event. Views are not kept
// alive, so the shell owns the subscription: go to Settings (where the About
// section renders the flow) and start the check. The flow itself is a module
// singleton, so it survives the view being unmounted meanwhile.
let updateCheckOff: (() => void) | null = null

onMounted(() => {
  updateCheckOff = Events.On(UPDATE_CHECK_EVENT, () => {
    // Navigate first so the About section is on screen; a dismissed navigation
    // (already on Settings) must not skip the check.
    void router
      .push('/settings')
      .catch(() => undefined)
      .then(() => {
        void checkForUpdate()
      })
  })
})

onUnmounted(() => {
  updateCheckOff?.()
  updateCheckOff = null
  disposeAppVisibility()
  disposeWebViewReset()
  disposeUpdateFlow()
})
</script>

<template>
  <AppWindow />
  <AppToast />
  <ConfirmDialog />
</template>
