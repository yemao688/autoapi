<script setup lang="ts">
import { useAppVisibility } from '@/composables/useAppVisibility'
import AppWindow from '@/components/AppWindow.vue'
import AppToast from '@/components/AppToast.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

// Initialize the app visibility coordinator in the shell so the Wails
// 'app:visibility' event is subscribed once for the application lifetime.
useAppVisibility()
</script>

<template>
  <AppWindow />
  <AppToast />
  <ConfirmDialog />
</template>
