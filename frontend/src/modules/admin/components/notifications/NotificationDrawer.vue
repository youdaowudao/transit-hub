<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { getNotificationCheckRoute, type NotificationController } from '../../composables/useNotifications'
import type { NotificationHistoryFilter } from '../../types/notifications'
const props = defineProps<{ controller: NotificationController }>()
const { drawerOpen, historyFilter, historyItems, historyNextCursor, historyLoading, historyError, detail, detailLoading, detailError, busyIds, progressError, summaryError, lastCheckedAt, summary, displayedPreferenceError: preferenceError } = props.controller
const filters: { value: NotificationHistoryFilter; label: string }[] = [{ value: 'all', label: '全部消息' }, { value: 'unread', label: '未读消息' }, { value: 'pending', label: '待确认消息' }]
const levels = ['', '普通', '关注', '重要', '紧急']
const dialogRef = ref<HTMLElement | null>(null)
const detailRef = ref<HTMLElement | null>(null)
let previousFocus: HTMLElement | null = null
let previousOverflow = ''
const restore = () => {
  document.body.style.overflow = previousOverflow
  const returnFocus = previousFocus?.isConnected && previousFocus !== document.body
    ? previousFocus
    : document.querySelector<HTMLElement>('button[aria-label="通知：查看当前工作区的提醒，不会修改主站。"]')
  returnFocus?.focus()
  previousFocus = null
}
watch(drawerOpen, async open => {
  if (!open) { restore(); return }
  previousFocus = document.activeElement as HTMLElement | null
  previousOverflow = document.body.style.overflow
  document.body.style.overflow = 'hidden'
  await nextTick()
  if (drawerOpen.value) dialogRef.value?.focus()
}, { flush: 'post' })
const readRenderedDetail = async (focusDetail = false) => {
  const id = detail.value?.item.id
  const workspace = props.controller.workspaceId.value
  if (!id) return
  await nextTick()
  if (drawerOpen.value && workspace === props.controller.workspaceId.value && detail.value?.item.id === id && detailRef.value?.isConnected && document.visibilityState !== 'hidden') {
    if (focusDetail) detailRef.value.focus()
    void props.controller.markDetailRead(id)
  }
}
watch(() => detail.value?.item.id, id => {
  if (id) void readRenderedDetail(true)
}, { flush: 'post' })
const readVisibleDetail = () => { if (document.visibilityState !== 'hidden') void readRenderedDetail() }
onMounted(() => document.addEventListener('visibilitychange', readVisibleDetail))
watch([detailLoading, detailError], async ([loading, error]) => {
  if (!loading && !error) return
  await nextTick()
  if (drawerOpen.value && !detail.value && (detailLoading.value || detailError.value) && document.visibilityState !== 'hidden') dialogRef.value?.focus()
}, { flush: 'post' })
const keydown = (event: KeyboardEvent) => {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); props.controller.closeDrawer(); return }
  if (event.key !== 'Tab' || !dialogRef.value) return
  const elements = Array.from(dialogRef.value.querySelectorAll<HTMLElement>('button:not([disabled]),a[href],input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'))
  const first = elements[0], last = elements[elements.length - 1]
  if (!first || !last) { event.preventDefault(); dialogRef.value.focus(); return }
  if (event.shiftKey && (document.activeElement === first || document.activeElement === dialogRef.value)) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}
const focusBeforeButtonAction = (event: MouseEvent) => {
  const button = event.target instanceof Element ? event.target.closest('button') : null
  if (button && event.currentTarget instanceof Element && event.currentTarget.contains(button)) dialogRef.value?.focus()
}
const showHistory = async () => {
  const workspace = props.controller.workspaceId.value
  detail.value = null
  detailError.value = ''
  void props.controller.loadHistory()
  await nextTick()
  if (drawerOpen.value && workspace === props.controller.workspaceId.value && !detail.value && !detailLoading.value) dialogRef.value?.focus()
}
onBeforeUnmount(() => { document.removeEventListener('visibilitychange', readVisibleDetail); if (drawerOpen.value || previousFocus) restore() })
</script>

<template>
  <Teleport to="body">
    <div v-if="drawerOpen" class="fixed inset-0 z-[100] flex justify-end bg-black/50" @click.self="controller.closeDrawer()" @keydown="keydown">
      <section ref="dialogRef" role="dialog" aria-modal="true" aria-labelledby="notification-drawer-title" tabindex="-1" class="flex h-full w-full max-w-xl flex-col overflow-hidden border-l border-border/50 bg-card shadow-2xl" @click.capture="focusBeforeButtonAction">
        <div class="flex shrink-0 items-start justify-between gap-3 border-b border-border/50 p-4 sm:p-5"><div><h2 id="notification-drawer-title" class="text-lg font-semibold">站内通知</h2><p class="mt-1 break-words text-sm text-muted-foreground">这里保留当前工作区的消息。已读表示看过；紧急消息还需要点击‘我已知悉’。</p><p class="mt-2 text-xs text-muted-foreground">普通：知道即可；关注：建议查看；重要：尽快检查；紧急：优先确认。</p></div><button type="button" class="shrink-0 text-muted-foreground hover:text-foreground" aria-label="关闭消息抽屉" @click="controller.closeDrawer()"><X class="h-5 w-5" /></button></div>
        <div class="min-h-0 flex-1 space-y-4 overflow-y-auto overscroll-contain p-4 sm:p-5">
          <p class="text-xs text-muted-foreground">上次成功检查：{{ lastCheckedAt ? new Date(lastCheckedAt).toLocaleString() : '尚无' }}。这里只读取本地消息，不会同步上游或修改主站。</p>
          <p v-if="summary?.samplingState.autoSyncEnabled === false" class="break-words text-sm text-warning">当前未开启自动同步，网页只读取已保存的提醒；新的倍率变化需开启自动同步或手动同步后才能发现。</p>
          <p v-if="summary?.samplingState.warning" role="alert" class="break-words text-sm text-warning">{{ summary.samplingState.warning.message }}；{{ summary.samplingState.warning.detail }}；{{ summary.samplingState.warning.next }}</p>
          <div v-if="summary?.samplingState.unconfirmedSiteCount" class="space-y-1 text-sm text-warning"><p>有{{ summary.samplingState.unconfirmedSiteCount }}个网站的倍率尚未确认，不能据此判断没有变化；请检查下面原因，再到上游管理重试同步。</p><p v-for="site in summary.samplingState.sites" :key="site.siteName" class="break-words">{{ site.siteName }}：{{ site.reason }}；最近读取时间：{{ site.observedAt ? new Date(site.observedAt).toLocaleString() : '尚无' }}</p></div>
          <p class="text-xs text-muted-foreground">新功能首次取得可信倍率时开始比较，不补发无法确认的旧变化。</p><p class="text-xs text-muted-foreground">手工接入且未在本系统关联的账号，可能无法按主站接入分类。</p>
          <p v-for="error in [summaryError, preferenceError, historyError, detailError, progressError].filter(Boolean)" :key="error" role="alert" class="break-words text-sm text-destructive">{{ error }}</p>
          <div class="flex flex-wrap gap-2"><Button variant="secondary" size="sm" :disabled="controller.summaryLoading.value" @click="controller.refreshSummary()">重试读取</Button><Button v-if="preferenceError" variant="secondary" size="sm" :disabled="controller.preferenceLoading.value" @click="controller.loadPreferences()">重试读取通知设置</Button><Button v-if="historyError" variant="secondary" size="sm" :disabled="historyLoading" @click="controller.loadHistory()">重试读取消息页</Button></div>
          <div v-if="detailLoading" role="status" aria-live="polite" class="text-sm text-muted-foreground">正在读取这条消息的详情；读取并显示后才标为已读。</div>
          <article v-if="detail" ref="detailRef" tabindex="-1" class="min-w-0 space-y-3 break-words" aria-label="通知详情">
            <Button variant="secondary" size="sm" @click="showHistory">返回消息列表</Button>
            <p class="text-xs text-muted-foreground">{{ levels[detail.item.level] }} · {{ detail.item.readAt ? '已读' : '未读' }}<span v-if="detail.item.level === 4"> · {{ detail.item.reminderCompletedAt ? '已知悉' : '待确认' }}</span></p>
            <h3 class="text-lg font-semibold">{{ detail.item.content.title }}</h3><p class="whitespace-pre-wrap text-sm">{{ detail.item.content.summary }}</p><p class="text-xs text-muted-foreground">发现时间：{{ new Date(detail.item.discoveredAt).toLocaleString() }}</p>
            <dl v-if="detail.item.content.details.length" class="space-y-3"><div v-for="(field, index) in detail.item.content.details" :key="index"><dt class="text-xs text-muted-foreground">{{ field.label }}</dt><dd class="whitespace-pre-wrap text-sm">{{ field.value }}</dd></div></dl>
            <div v-if="detail.item.content.action" class="space-y-2"><Button variant="secondary" :disabled="!getNotificationCheckRoute(detail)" @click="controller.checkDetailAction()">查看对应分组</Button><p class="text-xs text-muted-foreground">打开现有页面检查，不会自动修改主站。</p><p class="break-words text-sm text-muted-foreground">{{ detail.checkAction?.message || '当前版本暂时不能打开这个检查入口，请从菜单进入对应功能' }}</p></div>
            <Button v-if="!detail.item.readAt || detail.item.level === 3 && !detail.item.reminderCompletedAt" variant="secondary" :disabled="busyIds.includes(detail.item.id)" @click="controller.markDetailRead(detail.item.id)">重试标为已读</Button>
            <div v-if="detail.item.level === 4 && !detail.item.reminderCompletedAt" class="space-y-2"><Button :disabled="busyIds.includes(detail.item.id)" @click="controller.acknowledgeCritical([detail.item.id])">我已知悉这1条</Button><p class="text-xs text-muted-foreground">只停止这1条提醒，不表示定价或主站已调整。</p></div>
          </article>
          <template v-else-if="!detailLoading">
            <div class="flex flex-wrap gap-2" aria-label="消息筛选"><Button v-for="filter in filters" :key="filter.value" :variant="historyFilter === filter.value ? 'default' : 'secondary'" size="sm" :disabled="historyLoading" :aria-pressed="historyFilter === filter.value" @click="controller.loadHistory(filter.value)">{{ filter.label }}</Button></div>
            <p class="text-xs text-muted-foreground">筛选只改变当前显示的消息；待确认只列出还没有知悉的紧急消息。</p>
            <p v-if="historyLoading" role="status" aria-live="polite" class="text-sm text-muted-foreground">正在读取当前工作区的消息页，原消息保留。</p>
            <p v-if="!historyLoading && !historyError && !historyItems.length" class="text-sm text-muted-foreground">{{ historyNextCursor ? '本页没有符合当前筛选的消息。请点击“下一页消息”继续查看。' : historyFilter === 'all' ? '当前没有已保存的消息。倍率是否已检查，请看上游同步状态。' : historyFilter === 'unread' ? '当前筛选没有未读消息；已读消息可从“全部消息”查看。' : '当前没有待确认的紧急消息；其他消息可从“全部消息”查看。' }}</p>
            <button v-for="item in historyItems" :key="item.id" type="button" class="block w-full min-w-0 space-y-1 break-words border-b border-border/50 py-3 text-left hover:bg-surface-line/30" @click="controller.openDetail(item.id)"><span class="block text-xs text-muted-foreground">{{ levels[item.level] }} · {{ item.readAt ? '已读' : '未读' }}<span v-if="item.level === 4"> · {{ item.reminderCompletedAt ? '已知悉' : '待确认' }}</span></span><span class="block font-medium">{{ item.content.title }}</span><span class="block whitespace-pre-wrap text-sm text-muted-foreground">{{ item.content.summary }}</span><span class="block text-xs text-primary">打开详情；显示后仅标记这一条已读。</span></button>
            <div class="flex flex-wrap gap-2"><Button v-if="historyNextCursor" variant="secondary" :disabled="historyLoading" @click="controller.loadHistory(historyFilter, historyNextCursor)">下一页消息</Button><Button v-if="historyItems.length" variant="secondary" :disabled="historyLoading || historyItems.some(item => busyIds.includes(item.id))" @click="controller.markPageRead()">本页标为已读</Button></div><p class="text-xs text-muted-foreground">只标记本页消息，并停止本页重要提醒；紧急消息仍需单独知悉。</p><p v-if="historyNextCursor" class="text-xs text-muted-foreground">下一页只读取下一批消息，不会把未显示的消息标为已读。</p>
          </template>
        </div>
      </section>
    </div>
  </Teleport>
</template>
