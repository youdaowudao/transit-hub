<script setup lang="ts">
import { Bell } from 'lucide-vue-next'
import type { NotificationController } from '../../composables/useNotifications'
const props = defineProps<{ controller: NotificationController }>()
const { unreadCount, pendingCriticalCount } = props.controller
</script>

<template>
  <div class="flex min-w-0 items-center gap-1 sm:gap-2">
    <button type="button" class="relative flex h-9 w-9 shrink-0 items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-surface-elevated hover:text-foreground" aria-label="通知：查看当前工作区的提醒，不会修改主站。" title="通知：查看当前工作区的提醒，不会修改主站。" @click="controller.openDrawer()">
      <Bell class="h-4 w-4" />
      <span v-if="unreadCount" class="absolute -right-1 -top-1 rounded-full bg-primary px-1 text-[10px] font-medium text-primary-foreground" aria-live="polite">{{ unreadCount > 99 ? '99+' : unreadCount }}<span class="sr-only">条未读</span></span>
    </button>
    <button v-if="pendingCriticalCount" type="button" class="max-w-[100px] break-words rounded-md border border-destructive/40 bg-destructive/10 px-2 py-1 text-xs font-medium text-destructive sm:max-w-none" aria-live="assertive" @click="controller.openDrawer()">待确认{{ pendingCriticalCount }}条</button>
  </div>
</template>
