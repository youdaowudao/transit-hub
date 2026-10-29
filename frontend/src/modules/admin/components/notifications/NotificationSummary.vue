<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import type { NotificationController } from '../../composables/useNotifications'
const props = defineProps<{ controller: NotificationController }>()
const { attentionItems, importantItems, summary, summaryLoading, managementRefreshPending, busyIds, progressError } = props.controller
const signupRiskItems = props.controller.signupRiskItems
const visibleImportantItems = computed(() => {
  const riskIds = new Set(signupRiskItems.value.map(item => item.id))
  return importantItems.value.filter(item => !riskIds.has(item.id))
})
const pointerHovered = ref(false)
const focusWithin = ref(false)
let timer: ReturnType<typeof setTimeout> | null = null
let startedAt = 0
let remaining = 8000
const clearTimer = () => { if (timer !== null) clearTimeout(timer); timer = null }
const pause = () => {
  if (timer !== null) remaining = Math.max(0, remaining - (Date.now() - startedAt))
  clearTimer()
}
const resume = () => {
  if (!attentionItems.value.length || pointerHovered.value || focusWithin.value || document.visibilityState === 'hidden' || timer !== null) return
  startedAt = Date.now()
  timer = setTimeout(() => { timer = null; props.controller.clearAttention() }, remaining)
}
const hover = (value: boolean) => { pointerHovered.value = value; if (value) pause(); else resume() }
const focus = (event: FocusEvent, value: boolean) => {
  focusWithin.value = value || event.currentTarget instanceof HTMLElement && event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)
  if (focusWithin.value) pause()
  else resume()
}
const visibility = () => { if (document.visibilityState === 'hidden') pause(); else resume() }
watch(() => attentionItems.value.map(item => item.id).join('\u0000'), async ids => {
  clearTimer()
  remaining = 8000
  if (!ids) {
    pointerHovered.value = focusWithin.value = false
    return
  }
  await nextTick()
  if (ids !== attentionItems.value.map(item => item.id).join('\u0000') || document.visibilityState === 'hidden') return
  resume()
  void props.controller.presentAttention(attentionItems.value.map(item => item.id))
}, { immediate: true, flush: 'post' })
onMounted(() => document.addEventListener('visibilitychange', visibility))
onBeforeUnmount(() => { clearTimer(); pointerHovered.value = focusWithin.value = false; document.removeEventListener('visibilitychange', visibility) })
</script>

<template>
  <div v-if="attentionItems.length || importantItems.length || signupRiskItems.length" class="fixed right-3 top-16 z-40 flex max-h-[calc(100dvh-5rem)] w-[calc(100%-1.5rem)] max-w-sm flex-col gap-3 overflow-y-auto sm:right-6">
    <p v-if="managementRefreshPending" role="status" class="break-words bg-card p-3 text-xs text-muted-foreground">{{ controller.summaryError.value || '站点设置已变化，正在重新确认通知数量；这里不会修改主站。' }}</p>
    <section v-if="signupRiskItems.length" class="max-h-[calc((100dvh-5.75rem)/2)] shrink-0 overflow-y-auto overscroll-contain rounded-xl border border-warning/40 bg-card p-4 shadow-lg" aria-label="高风险注册提醒" aria-live="polite">
      <div class="flex items-center justify-between gap-3"><h2 class="text-sm font-semibold">重要：注册提醒</h2><Button variant="ghost" size="sm" :disabled="signupRiskItems.some(item => busyIds.includes(item.id))" @click="controller.dismissSignupRisk()">收起这组注册提醒</Button></div>
      <p class="mb-3 text-xs text-muted-foreground">注册提醒：这里最多显示20条；收起只停止这组提醒，消息仍可查看，不会修改主站。</p>
      <div v-for="item in signupRiskItems" :key="item.id" class="mb-3 min-w-0 break-words text-sm"><p class="font-medium">{{ item.content.title }}</p><p class="whitespace-pre-wrap text-muted-foreground">{{ item.content.summary }}</p><button type="button" class="mt-1 text-primary underline" @click="controller.openDetail(item.id)">查看详情</button></div>
      <p v-if="progressError" role="alert" class="break-words text-sm text-destructive">{{ progressError }}</p>
    </section>
    <section v-if="visibleImportantItems.length || importantItems.length && summary?.active.important.nextCursor" class="shrink-0 overflow-y-auto overscroll-contain rounded-xl border border-warning/40 bg-card p-4 shadow-lg" :class="attentionItems.length ? 'max-h-[calc((100dvh-5.75rem)/2)]' : 'max-h-[calc(100dvh-5rem)]'" aria-label="重要通知" aria-live="polite">
      <div class="flex items-center justify-between gap-3">
        <h2 class="text-sm font-semibold">重要：尽快检查</h2>
        <Button variant="ghost" size="sm" :disabled="importantItems.some(item => busyIds.includes(item.id))" @click="controller.dismissImportant()">收起</Button>
      </div>
      <p class="mb-3 text-xs text-muted-foreground"><template v-if="controller.importantCountReliable.value">共有{{ summary?.active.important.totalCount ?? importantItems.length }}条待提醒，</template>本摘要{{ importantItems.length }}条。收起只停止这些提醒，消息仍可查看，不会修改主站。<span v-if="visibleImportantItems.length !== importantItems.length">相同的注册消息已在上方单列显示。</span></p>
      <div v-for="item in visibleImportantItems" :key="item.id" class="mb-3 min-w-0 break-words text-sm">
        <p class="font-medium">{{ item.content.title }}</p><p class="whitespace-pre-wrap text-muted-foreground">{{ item.content.summary }}</p>
        <button type="button" class="mt-1 text-primary underline" @click="controller.openDetail(item.id)">查看详情</button>
      </div>
      <p v-if="progressError" role="alert" class="break-words text-sm text-destructive">{{ progressError }}</p>
      <Button v-if="summary?.active.important.nextCursor" variant="secondary" size="sm" :disabled="summaryLoading" @click="controller.nextSummaryPage(3)">查看下一页重要提醒</Button>
    </section>
    <section v-if="attentionItems.length" class="shrink-0 overflow-y-auto overscroll-contain rounded-xl border border-border/60 bg-card p-4 shadow-lg" :class="importantItems.length ? 'max-h-[calc((100dvh-5.75rem)/2)]' : 'max-h-[calc(100dvh-5rem)]'" aria-label="关注通知" aria-live="polite" @mouseenter="hover(true)" @mouseleave="hover(false)" @focusin="focus($event, true)" @focusout="focus($event, false)">
      <div class="flex items-center justify-between gap-3"><h2 class="text-sm font-semibold">关注：建议查看</h2><button type="button" class="shrink-0 text-muted-foreground hover:text-foreground" aria-label="收起关注提醒" @click="controller.clearAttention()"><X class="h-4 w-4" /></button></div>
      <p class="mb-3 text-xs text-muted-foreground"><template v-if="!managementRefreshPending">共有{{ summary?.active.attention.totalCount ?? attentionItems.length }}条待提醒，</template>本摘要{{ attentionItems.length }}条。显示8秒后收起，悬停或操作时暂停；消息仍保留，不会修改主站。</p>
      <div v-for="item in attentionItems" :key="item.id" class="mb-3 min-w-0 break-words text-sm"><p class="font-medium">{{ item.content.title }}</p><p class="whitespace-pre-wrap text-muted-foreground">{{ item.content.summary }}</p><button type="button" class="mt-1 text-primary underline" @click="controller.openDetail(item.id)">查看详情</button></div>
      <p v-if="progressError" role="alert" class="break-words text-sm text-destructive">{{ progressError }}</p>
      <Button v-if="summary?.active.attention.nextCursor" variant="secondary" size="sm" :disabled="summaryLoading" @click="controller.nextSummaryPage(2)">查看下一页关注提醒</Button>
    </section>
  </div>
</template>
