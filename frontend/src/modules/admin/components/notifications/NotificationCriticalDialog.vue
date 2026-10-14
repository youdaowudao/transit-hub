<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import type { NotificationController } from '../../composables/useNotifications'
const props = defineProps<{ controller: NotificationController }>()
const { criticalOpen, criticalBatch, criticalNextCursor, criticalTotalCount, busyIds, progressError, summaryLoading } = props.controller
const dialogRef = ref<HTMLElement | null>(null)
let previousFocus: HTMLElement | null = null
let previousOverflow = ''
const restore = () => {
  document.body.style.overflow = previousOverflow
  if (previousFocus?.isConnected) previousFocus.focus()
  previousFocus = null
}
watch(criticalOpen, async open => {
  if (!open) { restore(); return }
  previousFocus = document.activeElement as HTMLElement | null
  previousOverflow = document.body.style.overflow
  document.body.style.overflow = 'hidden'
  await nextTick()
  if (criticalOpen.value) dialogRef.value?.focus()
}, { flush: 'post' })
const keydown = (event: KeyboardEvent) => {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); props.controller.laterCritical(); return }
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
onBeforeUnmount(() => { if (criticalOpen.value || previousFocus) restore() })
</script>

<template>
  <Teleport to="body">
    <div v-if="criticalOpen" class="fixed inset-0 z-[100] flex items-center justify-center bg-black/50 p-3 sm:p-4" @keydown="keydown">
      <section ref="dialogRef" role="alertdialog" aria-modal="true" aria-labelledby="notification-critical-title" aria-describedby="notification-critical-description" tabindex="-1" class="flex max-h-[calc(100dvh-2rem)] w-full max-w-2xl flex-col overflow-hidden rounded-xl border border-destructive/40 bg-card shadow-2xl" @click.capture="focusBeforeButtonAction">
        <div class="flex shrink-0 items-start justify-between gap-3 border-b border-border/50 p-4 sm:p-5"><div><h2 id="notification-critical-title" class="text-lg font-semibold text-destructive">紧急：优先确认</h2><p class="mt-1 text-sm text-muted-foreground">本次读取待确认{{ criticalTotalCount }}条，本摘要{{ criticalBatch.length }}条。</p></div><button type="button" class="shrink-0 text-muted-foreground hover:text-foreground" aria-label="收起紧急通知，待确认消息仍会保留" @click="controller.laterCritical()"><X class="h-5 w-5" /></button></div>
        <div class="min-h-0 flex-1 space-y-4 overflow-y-auto overscroll-contain p-4 sm:p-5">
          <p id="notification-critical-description" class="text-sm text-muted-foreground">查看或确认不会修改主站。只确认当前摘要中列出的消息，后到和未显示的消息仍需单独确认。</p>
          <article v-for="item in criticalBatch" :key="item.id" class="min-w-0 break-words border-b border-border/50 pb-4"><h3 class="font-semibold">{{ item.content.title }}</h3><p class="mt-1 whitespace-pre-wrap text-sm">{{ item.content.summary }}</p><button type="button" class="mt-2 text-sm text-primary underline" @click="controller.openDetail(item.id)">查看详情</button><p class="mt-1 text-xs text-muted-foreground">查看详情只标记看过，仍需点击“我已知悉”。</p></article>
          <p v-if="progressError" role="alert" class="break-words text-sm text-destructive">{{ progressError }}</p>
          <Button v-if="criticalNextCursor" variant="secondary" size="sm" :disabled="summaryLoading || criticalBatch.some(item => busyIds.includes(item.id))" @click="controller.nextSummaryPage(4)">查看下一页紧急提醒</Button>
          <p class="text-xs text-muted-foreground">下一页只读取下一批消息，不会确认本批或其他消息。</p>
        </div>
        <div class="shrink-0 space-y-2 border-t border-border/50 p-4 sm:p-5"><div class="flex flex-wrap gap-2"><Button :disabled="!criticalBatch.length || criticalBatch.some(item => busyIds.includes(item.id))" @click="controller.acknowledgeCritical()">我已知悉这{{ criticalBatch.length }}条</Button><Button variant="secondary" @click="controller.laterCritical()">稍后查看</Button></div><p class="break-words text-xs text-muted-foreground">只停止这{{ criticalBatch.length }}条提醒，不表示定价或主站已调整。</p><p class="text-xs text-muted-foreground">收起弹窗，待确认消息仍会保留。</p></div>
      </section>
    </div>
  </Teleport>
</template>
