<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Minimize2, RotateCw, X } from 'lucide-vue-next'
import { subscribeAuthSessionChange, getAccessToken } from '@/modules/auth/api/auth'
import { chanshuiOpenEvent } from '../../composables/useChanshuiLauncher'
import { requestChanshui, ChanshuiError, chanshuiRequestSpec, type ChanshuiTargetInput } from '../../api/chanshui'

const props = defineProps<{ workspaceId: string }>()
const open = ref(false), minimized = ref(false), ready = ref(false), loadingText = ref('正在打开检测页…'), error = ref('')
const iframe = ref<HTMLIFrameElement | null>(null), dirty = ref(false)
const target = ref<ChanshuiTargetInput>({})
const targetSnapshot = (): ChanshuiTargetInput => ({
  targetId: typeof target.value.targetId === 'string' ? target.value.targetId : undefined,
  groupId: typeof target.value.groupId === 'string' ? target.value.groupId : undefined,
  accountName: typeof target.value.accountName === 'string' ? target.value.accountName : undefined,
  groupName: typeof target.value.groupName === 'string' ? target.value.groupName : undefined,
})
let generation = 0, timeout: ReturnType<typeof setTimeout> | undefined, loadingTimer: ReturnType<typeof setTimeout> | undefined
const requests = new Map<string, AbortController>()
let serial = Promise.resolve()
const send = (kind: string, data: Record<string, unknown> = {}) => iframe.value?.contentWindow?.postMessage({ kind, v: 1, generation, ...data }, window.location.origin)
const clearTimers = () => { if (timeout) clearTimeout(timeout); if (loadingTimer) clearTimeout(loadingTimer) }
const invalidate = () => { generation++; for (const c of requests.values()) c.abort(); requests.clear(); serial = Promise.resolve() }
const close = () => { dirty.value = false; invalidate(); clearTimers(); open.value = false; minimized.value = false; ready.value = false; error.value = ''; iframe.value = null }
const requestClose = () => { if (!dirty.value || window.confirm('检测设置有未保存的修改，确定关闭并放弃吗？已经开始的任务不会停止。')) close() }
const arm = () => {
  clearTimers(); ready.value = false; error.value = ''; loadingText.value = '正在打开检测页…'
  loadingTimer = setTimeout(() => { loadingText.value = '页面仍在加载，请稍候；此时没有开始检测。' }, 3000)
  timeout = setTimeout(() => { if (!ready.value) error.value = '10秒内没有收到检测页响应。请检查检测静态页面是否已安装，再点击重新载入；此时没有开始检测。' }, 10000)
}
const launch = async (event: Event) => {
  if (!props.workspaceId) return
  const payload = (event as CustomEvent<ChanshuiTargetInput>).detail ?? {}
  target.value = { targetId: payload.targetId, groupId: payload.groupId, accountName: payload.accountName, groupName: payload.groupName }
  if (!open.value) { invalidate(); open.value = true; minimized.value = false; arm(); await nextTick() }
  else { minimized.value = false; send('chanshui.VISIBILITY', { visible: true }); send('chanshui.OPEN_TARGET', { target: targetSnapshot() }) }
}
const minimize = () => { minimized.value = true; send('chanshui.VISIBILITY', { visible: false }) }
const restore = () => { minimized.value = false; send('chanshui.VISIBILITY', { visible: true }) }
const reload = () => { if (dirty.value && !window.confirm('检测设置有未保存的修改，确定重新载入并放弃吗？')) return; dirty.value = false; invalidate(); arm(); if (iframe.value) iframe.value.src = '/chanshui/' }
const onMessage = (event: MessageEvent) => {
  if (!open.value || event.origin !== window.location.origin || event.source !== iframe.value?.contentWindow) return
  const message = event.data
  if (!message || message.v !== 1 || typeof message.kind !== 'string') return
  if (message.kind === 'chanshui.READY') {
    invalidate(); ready.value = true; error.value = ''; clearTimers()
    send('chanshui.BOOT', { workspaceId: props.workspaceId, target: targetSnapshot(), visible: !minimized.value })
    return
  }
  if (message.kind === 'chanshui.DIRTY' && message.generation === generation && typeof message.dirty === 'boolean') { dirty.value = message.dirty; return }
  if (message.kind !== 'chanshui.request' || message.generation !== generation || typeof message.id !== 'string' || !/^[a-zA-Z0-9-]{1,80}$/.test(message.id) || typeof message.op !== 'string') return
  const scope = generation, requestId = message.id, workspaceId = props.workspaceId
  if (requests.has(requestId) || requests.size >= 12) return
  let params: Record<string, unknown>
  try {
    params = message.params && typeof message.params === 'object' && !Array.isArray(message.params) ? message.params : {}
    if (new TextEncoder().encode(JSON.stringify(params)).byteLength > 128 * 1024) throw new Error('请求内容超过允许大小。')
    chanshuiRequestSpec(message.op, params)
  } catch (e) { send('chanshui.response', { id: requestId, ok: false, error: { message: e instanceof Error ? e.message : '此操作无法识别。' } }); return }
  const controller = new AbortController(); requests.set(requestId, controller)
  const run = async () => {
    if (scope !== generation) { requests.delete(requestId); return }
    try {
      let warning: Record<string, unknown> | undefined
      const value = await requestChanshui(message.op, params, workspaceId, controller.signal, value => { warning = value })
      if (scope === generation) send('chanshui.response', { id: requestId, ok: true, value, ...(warning ? { warning } : {}) })
    } catch (e) {
      if (scope === generation) send('chanshui.response', { id: requestId, ok: false, error: { ...(e instanceof ChanshuiError ? e.details : {}), message: e instanceof Error ? e.message : '读取结果时中断，请重试读取。', status: e instanceof ChanshuiError ? e.status : 0 } })
    } finally { requests.delete(requestId) }
  }
  serial = serial.then(run, run)
}
watch(() => props.workspaceId, close)
let unsubscribe: (() => void) | undefined
onMounted(() => { window.addEventListener(chanshuiOpenEvent, launch); window.addEventListener('message', onMessage); unsubscribe = subscribeAuthSessionChange(() => { if (!getAccessToken()) close() }) })
onBeforeUnmount(() => { close(); unsubscribe?.(); window.removeEventListener(chanshuiOpenEvent, launch); window.removeEventListener('message', onMessage) })
</script>

<template>
  <Teleport to="body">
    <div v-if="open" v-show="!minimized" class="fixed inset-0 z-[80] flex items-center justify-center bg-background/75 p-2 backdrop-blur-sm" data-testid="chanshui-host">
      <section role="dialog" aria-modal="true" aria-label="模型检测" class="flex h-[calc(100dvh-1rem)] w-[calc(100vw-1rem)] min-w-0 flex-col overflow-hidden rounded-xl border border-border bg-background shadow-xl">
        <header class="flex shrink-0 items-center justify-between gap-3 border-b border-border px-4 py-2">
          <div class="min-w-0"><h2 class="truncate text-sm font-semibold">模型检测<span v-if="target.accountName"> · {{ target.accountName }}</span></h2><p class="text-xs text-muted-foreground">只检测并保存结果，不会修改主站。收起或关闭不会停止任务；需要中止请在页内停止检测。</p></div>
          <div class="flex shrink-0 items-center gap-1"><button type="button" class="rounded-md p-2 text-muted-foreground hover:bg-surface hover:text-foreground" aria-label="收起检测页" title="保留本机页面，服务器检测保存后仍会退出" @click="minimize"><Minimize2 class="h-4 w-4" /></button><button type="button" class="rounded-md p-2 text-muted-foreground hover:bg-surface hover:text-foreground" aria-label="关闭检测页" @click="requestClose"><X class="h-4 w-4" /></button></div>
        </header>
        <div v-if="error || !ready" class="shrink-0 border-b border-border px-4 py-3 text-sm" role="status"><p :class="error ? 'text-destructive' : 'text-muted-foreground'">{{ error || loadingText }}</p><button v-if="error" type="button" class="mt-2 inline-flex items-center gap-2 rounded-md border border-border px-3 py-1.5" @click="reload"><RotateCw class="h-4 w-4" />重新载入页面</button></div>
        <iframe ref="iframe" src="/chanshui/" title="账号模型检测" class="min-h-0 w-full flex-1 border-0" referrerpolicy="same-origin" />
      </section>
    </div>
    <div v-if="open && minimized" class="fixed bottom-4 right-4 z-[81] flex max-w-[calc(100vw-2rem)] items-center rounded-lg border border-border bg-background shadow-lg" data-testid="chanshui-minimized"><button type="button" class="min-w-0 px-4 py-3 text-left text-sm" @click="restore"><span class="block truncate font-medium">模型检测<span v-if="target.accountName"> · {{ target.accountName }}</span></span><span class="block text-xs text-muted-foreground">已收起，恢复后查看最新结果。</span></button><button type="button" class="mr-1 rounded-md p-2 text-muted-foreground hover:bg-surface" aria-label="关闭收起的检测页" @click="requestClose"><X class="h-4 w-4" /></button></div>
  </Teleport>
</template>
