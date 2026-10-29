<script setup lang="ts">
import { computed } from 'vue'
import { Bell } from 'lucide-vue-next'
import type { NotificationController } from '../../composables/useNotifications'
const props = defineProps<{ controller: NotificationController }>()
const { unreadCount, pendingCriticalCount, importantCountReliable, managementRefreshPending, summaryError, summary } = props.controller
const marker = computed(() => {
  if (managementRefreshPending.value) return null
  if (pendingCriticalCount.value > 0) return { kind: 'critical', count: pendingCriticalCount.value }
  const important = importantCountReliable.value ? summary.value?.active.important.totalCount ?? 0 : 0
  if (important > 0) return { kind: 'important', count: important }
  if (unreadCount.value > 0) return { kind: 'unread', count: unreadCount.value }
  return null
})
</script>

<template>
  <div class="flex min-w-0 items-center gap-1 sm:gap-2">
    <button type="button" class="relative flex h-9 w-9 shrink-0 items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-surface-elevated hover:text-foreground" aria-label="通知：查看当前工作区的提醒，不会修改主站。" title="通知：查看当前工作区的提醒，不会修改主站。" @click="controller.openDrawer()">
      <Bell class="h-4 w-4" />
      <span v-if="marker" class="absolute -right-1 -top-1 rounded-full" :class="marker.kind === 'unread' ? 'h-2 w-2 bg-primary' : ['px-1 text-[10px] font-medium', marker.kind === 'critical' ? 'bg-destructive text-destructive-foreground' : 'bg-warning text-warning-foreground']" aria-live="polite"><template v-if="marker.kind !== 'unread'">{{ marker.count > 99 ? '99+' : marker.count }}</template><span class="sr-only">{{ marker.kind === 'critical' ? `${marker.count}条紧急消息待确认` : marker.kind === 'important' ? `${marker.count}条重要提醒待处理` : '有未读消息' }}</span></span>
    </button>
    <button v-if="managementRefreshPending" type="button" class="max-w-[14rem] break-words text-xs text-muted-foreground" @click="controller.openDrawer()">{{ summaryError ? '通知数量暂时无法确认，点击查看原因并重试读取。' : '站点设置已变化，正在重新确认通知数量；这里不会修改主站。' }}</button>
  </div>
</template>
