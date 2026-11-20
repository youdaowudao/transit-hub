<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Minimize2, RotateCw, X } from 'lucide-vue-next'
import { authTokenStorageKey, subscribeAuthSessionChange, getAccessToken } from '@/modules/auth/api/auth'
import { chanshuiOpenEvent } from '../../composables/useChanshuiLauncher'
import { chanshuiCatalog, clearChanshuiCatalogForSession, clearChanshuiCatalogForWorkspace } from '../../composables/chanshuiCatalog'
import { requestChanshui, ChanshuiError, chanshuiRequestSpec, type ChanshuiTargetInput } from '../../api/chanshui'

const props = defineProps<{ workspaceId: string }>()
const open = ref(false), minimized = ref(false), ready = ref(false), loadingText = ref('正在打开检测页…'), error = ref('')
const iframe = ref<HTMLIFrameElement | null>(null), dirty = ref(false)
const target = ref<ChanshuiTargetInput>({})
const catalog = computed(() => chanshuiCatalog.value?.workspaceId === props.workspaceId ? chanshuiCatalog.value : null)
const targetSnapshot = (): ChanshuiTargetInput => ({
  reportId: typeof target.value.reportId === 'string' ? target.value.reportId : undefined,
  targetId: typeof target.value.targetId === 'string' ? target.value.targetId : undefined,
  groupId: typeof target.value.groupId === 'string' ? target.value.groupId : undefined,
  accountName: typeof target.value.accountName === 'string' ? target.value.accountName : undefined,
  groupName: typeof target.value.groupName === 'string' ? target.value.groupName : undefined,
})
let generation = 0, timeout: ReturnType<typeof setTimeout> | undefined, loadingTimer: ReturnType<typeof setTimeout> | undefined
const requests = new Map<string, AbortController>()
let serial = Promise.resolve()
let stopGeneration: number | undefined
const send = (kind: string, data: Record<string, unknown> = {}) => iframe.value?.contentWindow?.postMessage({ kind, v: 1, generation, ...data }, window.location.origin)
const discardDraftChecks = (nextTarget?: ChanshuiTargetInput) => {
  if (!ready.value) return
  try {
    iframe.value?.contentWindow?.dispatchEvent(new CustomEvent('chanshui.discard-checks', { detail: { generation, target: nextTarget } }))
  } catch {
    // A frame the host cannot reach also cannot approve a first POST.
  }
}
const clearTimers = () => { if (timeout) clearTimeout(timeout); if (loadingTimer) clearTimeout(loadingTimer) }
const invalidate = () => { generation++; for (const c of requests.values()) c.abort(); requests.clear(); serial = Promise.resolve(); stopGeneration = undefined }
const close = () => { dirty.value = false; invalidate(); clearTimers(); open.value = false; minimized.value = false; ready.value = false; error.value = ''; iframe.value = null }
const requestClose = () => { if (!dirty.value || window.confirm('检测设置有未保存的修改，确定关闭并放弃吗？已经开始的任务不会停止。')) close() }
const arm = () => {
  clearTimers(); ready.value = false; error.value = ''; loadingText.value = '正在打开检测页…'
  loadingTimer = setTimeout(() => { loadingText.value = '页面仍在加载，请稍候；此时没有开始检测。' }, 3000)
  timeout = setTimeout(() => { if (!ready.value) error.value = '10秒内没有收到检测页响应。请重新执行整站升级，并查看升级日志中的检测页面构建步骤；此时没有开始检测。' }, 10000)
}
const launch = async (event: Event) => {
  if (!props.workspaceId) return
  const payload = (event as CustomEvent<ChanshuiTargetInput>).detail ?? {}
  target.value = { reportId: payload.reportId, targetId: payload.targetId, groupId: payload.groupId, accountName: payload.accountName, groupName: payload.groupName }
  if (!open.value) { invalidate(); open.value = true; minimized.value = false; arm(); await nextTick() }
  else {
    const nextTarget = targetSnapshot()
    discardDraftChecks(nextTarget)
    restore()
    send('chanshui.OPEN_TARGET', { target: nextTarget })
  }
}
const minimize = () => { minimized.value = true; discardDraftChecks(); send('chanshui.VISIBILITY', { visible: false }) }
const restore = () => {
  if (minimized.value && ready.value) send('chanshui.CATALOG', { catalog: catalog.value })
  minimized.value = false
  send('chanshui.VISIBILITY', { visible: true })
}
const reload = () => { if (dirty.value && !window.confirm('检测设置有未保存的修改，确定重新载入并放弃吗？')) return; dirty.value = false; invalidate(); arm(); if (iframe.value) iframe.value.src = '/chanshui/' }
const checkFirstStart = (scope: number, id: string, workspaceId: string, params: Record<string, unknown>) => {
  if (!open.value || !ready.value || minimized.value || scope !== generation || workspaceId !== props.workspaceId) return '检测页已关闭、收起或切换工作区。'
  try {
    if (session !== getAccessToken()) return '登录状态已切换。'
    const group = catalog.value?.groups.find(group => group.id === params.groupId)
    const account = group?.accounts.find(account => account.targetId === params.targetId)
    if (!account || group?.accountsError) return '分组健康的最新名单已没有这个分组或账号。'
    const guard = new CustomEvent('chanshui.before-start', { cancelable: true, detail: { generation: scope, id, protocol: account.protocol } })
    if (iframe.value?.contentWindow?.dispatchEvent(guard) === false) return ''
  } catch {
    return '无法连接当前检测页。'
  }
  return '发送前无法确认原来的检测选项仍有效。'
}
const onMessage = (event: MessageEvent) => {
  if (!open.value || event.origin !== window.location.origin || event.source !== iframe.value?.contentWindow) return
  const message = event.data
  if (!message || message.v !== 1 || typeof message.kind !== 'string') return
  if (message.kind === 'chanshui.READY') {
    invalidate(); ready.value = true; error.value = ''; clearTimers()
    send('chanshui.BOOT', { workspaceId: props.workspaceId, target: targetSnapshot(), visible: !minimized.value, catalog: catalog.value })
    return
  }
  if (message.kind === 'chanshui.DIRTY' && message.generation === generation && typeof message.dirty === 'boolean') { dirty.value = message.dirty; return }
  if (message.kind !== 'chanshui.request' || message.generation !== generation || typeof message.id !== 'string' || !/^[a-zA-Z0-9-]{1,80}$/.test(message.id) || typeof message.op !== 'string') return
  const scope = generation, requestId = message.id, workspaceId = props.workspaceId
  if (requests.has(requestId)) return
  const priorityStop = message.op === 'cancel'
  const guardStart = message.op === 'start' && message.guardStart === true
  if ((!priorityStop && requests.size >= 12) || (priorityStop && stopGeneration === generation)) {
    send('chanshui.response', { id: requestId, ok: false, error: { message: priorityStop ? '停止请求正在处理，已保存回答仍可查看；请稍候核查运行状态。' : '正在处理其他页面操作，已显示内容仍可查看；请稍后重试读取。', status: 429 } })
    return
  }
  let params: Record<string, unknown>
  try {
    params = message.params && typeof message.params === 'object' && !Array.isArray(message.params) ? message.params : {}
    if (new TextEncoder().encode(JSON.stringify(params)).byteLength > 128 * 1024) throw new Error('请求内容超过允许大小。')
    chanshuiRequestSpec(message.op, params)
  } catch (e) { send('chanshui.response', { id: requestId, ok: false, error: { message: e instanceof Error ? e.message : '此操作无法识别。' } }); return }
  const controller = new AbortController(); requests.set(requestId, controller)
  if (priorityStop) stopGeneration = scope
  const run = async () => {
    if (scope !== generation) return
    try {
      const blockedStart = guardStart ? checkFirstStart(scope, requestId, workspaceId, params) : ''
      if (blockedStart) {
        send('chanshui.response', { id: requestId, ok: false, error: { message: `${blockedStart} 请回到新建检测后重新操作。本次没有发送问题。`, status: 409, operationResult: 'not_started' } })
        return
      }
      let warning: Record<string, unknown> | undefined
      const value = await requestChanshui(message.op, params, workspaceId, controller.signal, value => { warning = value })
      if (scope === generation) send('chanshui.response', { id: requestId, ok: true, value, ...(warning ? { warning } : {}) })
    } catch (e) {
      if (scope === generation) send('chanshui.response', { id: requestId, ok: false, error: { ...(e instanceof ChanshuiError ? e.details : {}), message: e instanceof Error ? e.message : '读取结果时中断，请重试读取。', status: e instanceof ChanshuiError ? e.status : 0 } })
    } finally { if (scope === generation) requests.delete(requestId); if (priorityStop && stopGeneration === scope) stopGeneration = undefined }
  }
  if (priorityStop) void run()
  else serial = serial.then(run, run)
}
const openSharedReport=()=>{const reportId=new URLSearchParams(window.location.search).get('detectorReport');if(props.workspaceId && reportId && /^[0-9a-f]{32}$/.test(reportId)) void launch(new CustomEvent(chanshuiOpenEvent,{detail:{reportId}}))}
watch(() => props.workspaceId, workspaceId => { close(); clearChanshuiCatalogForWorkspace(workspaceId); openSharedReport() })
watch(catalog, value => { if (open.value && ready.value && !minimized.value) send('chanshui.CATALOG', { catalog: value }) })
let unsubscribe: (() => void) | undefined
let session: string | null = null
const onAuthChange = () => {
  const next = getAccessToken()
  if (next && session === next) return
  session = next
  close()
  clearChanshuiCatalogForSession()
}
const onStorage = (event: StorageEvent) => { if (event.key === authTokenStorageKey || event.key === null) onAuthChange() }
onMounted(() => {
  session = getAccessToken()
  clearChanshuiCatalogForWorkspace(props.workspaceId)
  openSharedReport()
  window.addEventListener(chanshuiOpenEvent, launch)
  window.addEventListener('message', onMessage)
  window.addEventListener('storage', onStorage)
  unsubscribe = subscribeAuthSessionChange(onAuthChange)
})
onBeforeUnmount(() => {
  close()
  unsubscribe?.()
  window.removeEventListener(chanshuiOpenEvent, launch)
  window.removeEventListener('message', onMessage)
  window.removeEventListener('storage', onStorage)
})
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
