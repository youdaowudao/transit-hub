<script setup lang="ts">
import { t } from './locales'
import { routeErrorKey } from './lib/routeError'
</script>

<template>
  <router-view />
  <div
    v-if="routeErrorKey"
    class="fixed inset-x-3 top-3 z-[100] mx-auto max-w-xl rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive shadow-lg"
    role="alert"
    aria-live="assertive"
  >
    {{ t(routeErrorKey) }}
  </div>
</template>
