<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Minimize2, RotateCw, X } from 'lucide-vue-next'
import { authTokenStorageKey, subscribeAuthSessionChange, getAccessToken } from '@/modules/auth/api/auth'
import { chanshuiOpenEvent } from '../../composables/useChanshuiLauncher'
import { acceptChanshuiCatalog, chanshuiCatalog, chanshuiCatalogSession, clearChanshuiCatalogForSession, clearChanshuiCatalogForWorkspace, restoreChanshuiCatalog } from '../../composables/chanshuiCatalog'
import { requestChanshui, ChanshuiError, chanshuiRequestSpec, type ChanshuiTargetInput } from '../../api/chanshui'

const props = defineProps<{ workspaceId: string; isWorkspaceConfirmed: (workspaceId: string) => boolean }>()
const open = ref(false), minimized = ref(false), ready = ref(false), loadingText = ref('正在打开检测页…'), error = ref('')
const iframe = ref<HTMLIFrameElement | null>(null), dirty = ref(false)
const target = ref<ChanshuiTargetInput>({})
const catalogLoading = ref(false), catalogError = ref(''), catalogWarning = ref('')
const workspaceReady = () => Boolean(props.workspaceId && props.isWorkspaceConfirmed(props.workspaceId))
const catalog = computed(() => chanshuiCatalog.value?.workspaceId === props.workspaceId ? chanshuiCatalog.value : null)
const targetSnapshot = (): ChanshuiTargetInput => {
  const id = target.value.targetId, prefix = `sub2api:${props.workspaceId}:`
  const input = workspaceReady() && typeof id === 'string' && id.startsWith(prefix) && id.slice(prefix.length).trim() ? target.value : {}
  return {
    reportId: typeof target.value.reportId === 'string' ? target.value.reportId : undefined,
    targetId: typeof input.targetId === 'string' ? input.targetId : undefined,
    groupId: typeof input.groupId === 'string' ? input.groupId : undefined,
    accountName: typeof input.accountName === 'string' ? input.accountName : undefined,
    groupName: typeof input.groupName === 'string' ? input.groupName : undefined,
  }
}

let generation = 0, timeout: ReturnType<typeof setTimeout> | undefined, loadingTimer: ReturnType<typeof setTimeout> | undefined
let catalogSequence = 0, catalogController: AbortController | undefined
type HostRequest = { controller: AbortController; timer: ReturnType<typeof setTimeout>; started: boolean; op: string }
const requests = new Map<string, HostRequest>()
let serial = Promise.resolve()
let stopRequest: HostRequest | undefined, stateRequest: HostRequest | undefined
const send = (kind: string, data: Record<string, unknown> = {}) => {
  if (['chanshui.BOOT', 'chanshui.CATALOG', 'chanshui.OPEN_TARGET'].includes(kind) && !workspaceReady()) return
  iframe.value?.contentWindow?.postMessage({ kind, v: 1, generation, ...data }, window.location.origin)
}
const discardDraftChecks = (nextTarget?: ChanshuiTargetInput) => {
  if (!ready.value) return
  try {
    iframe.value?.contentWindow?.dispatchEvent(new CustomEvent('chanshui.discard-checks', { detail: { generation, target: nextTarget } }))
  } catch {
    // A frame the host cannot reach also cannot approve a first POST.
  }
}
const clearTimers = () => { if (timeout) clearTimeout(timeout); if (loadingTimer) clearTimeout(loadingTimer) }
const invalidate = () => {
  generation++
  for (const request of requests.values()) { clearTimeout(request.timer); request.controller.abort() }
  requests.clear(); serial = Promise.resolve(); stopRequest = stateRequest = undefined
}
const releaseRequest = (id: string, request: HostRequest) => {
  clearTimeout(request.timer)
  if (requests.get(id) === request) requests.delete(id)
  if (stopRequest === request) stopRequest = undefined
  if (stateRequest === request) stateRequest = undefined
}
const abortRequest = (id: string, request: HostRequest) => {
  if (requests.get(id) !== request) return
  releaseRequest(id, request)
  request.controller.abort()
  send('chanshui.response', { id, ok: false, error: {
    message: !request.started ? '等待发送超过45秒或页面已取消，本次没有发送请求；请核查原记录后再操作。' : request.op === 'start-batch' ? '等待批次受理结果超过45秒，尚不能确认是否已创建。请查询原批次编号，不要另建一批。' : '等待调度站结果已超时或取消，不能确认后台操作结果。请核查原编号；不要重复检测或停止。',
    status: 0, operationResult: request.started ? 'unknown' : 'not_started',
  } })
}
const cancelCatalog = () => { catalogSequence++; catalogController?.abort(); catalogController = undefined; catalogLoading.value = false }
const close = () => { dirty.value = false; invalidate(); cancelCatalog(); clearTimers(); open.value = false; minimized.value = false; ready.value = false; error.value = ''; catalogError.value = ''; catalogWarning.value = ''; iframe.value = null }
const waitForWorkspace = () => {
  invalidate(); cancelCatalog(); clearTimers(); ready.value = false; minimized.value = false; iframe.value = null
  target.value = { reportId: target.value.reportId }; error.value = ''; catalogError.value = ''; catalogWarning.value = ''
}
const loadCatalog = async () => {
  if (!open.value || !workspaceReady() || catalogLoading.value) return
  const sequence = ++catalogSequence, workspaceId = props.workspaceId, sourceSession = chanshuiCatalogSession(), token = getAccessToken(), previous = chanshuiCatalog.value
  const controller = new AbortController(); catalogController = controller
  const current = () => sequence === catalogSequence && !controller.signal.aborted && open.value && workspaceId === props.workspaceId && workspaceReady() && token === getAccessToken() && sourceSession === chanshuiCatalogSession()
  catalogLoading.value = true; catalogError.value = ''
  try {
    const value = await requestChanshui<{ catalog: unknown }>('catalog', {}, workspaceId, controller.signal)
    if (!current() || previous !== chanshuiCatalog.value) return
    const accepted = acceptChanshuiCatalog(workspaceId, value?.catalog, sourceSession, current)
    if (accepted) catalogWarning.value = accepted.warning
  } catch (e) {
    if (current() && previous === chanshuiCatalog.value) catalogError.value = `${e instanceof Error ? e.message : '读取分组和账号时中断，原因尚未返回，请检查后端日志'}。${catalog.value ? '已保留上次名单' : '当前仍没有名单'}；没有修改主站或开始检测。处理后请点击“更新分组和账号”重试。`
  } finally {
    if (sequence === catalogSequence) { catalogLoading.value = false; catalogController = undefined }
  }
}
const preparePage = async () => {
  if (!open.value || !workspaceReady() || iframe.value) return
  target.value = targetSnapshot()
  restoreChanshuiCatalog(props.workspaceId)
  arm()
  if (!catalog.value) void loadCatalog()
  await nextTick()
}
const requestClose = () => { if (!dirty.value || window.confirm('检测设置有未保存的修改，确定关闭并放弃吗？已经开始的任务不会停止。')) close() }
const arm = () => {
  clearTimers(); ready.value = false; error.value = ''; loadingText.value = '正在打开检测页…'
  loadingTimer = setTimeout(() => { loadingText.value = '页面仍在加载，请稍候；此时没有开始检测。' }, 3000)
  timeout = setTimeout(() => { if (!ready.value) error.value = '10秒内没有收到检测页响应。请重新执行整站升级，并查看升级日志中的检测页面构建步骤；此时没有开始检测。' }, 10000)
}
const launch = async (event: Event) => {
  const payload = (event as CustomEvent<ChanshuiTargetInput>).detail ?? {}
  target.value = { reportId: payload.reportId, targetId: payload.targetId, groupId: payload.groupId, accountName: payload.accountName, groupName: payload.groupName }
  if (!workspaceReady()) { open.value = true; waitForWorkspace(); return }
  target.value = targetSnapshot()
  if (!open.value || !iframe.value) { invalidate(); open.value = true; minimized.value = false; await preparePage() }
  else {
    const nextTarget = targetSnapshot()
    discardDraftChecks(nextTarget)
    restore()
    send('chanshui.OPEN_TARGET', { target: nextTarget })
  }
}
const minimize = () => { minimized.value = true; discardDraftChecks(); send('chanshui.VISIBILITY', { visible: false }) }
const restore = () => {
  if (!workspaceReady()) { waitForWorkspace(); return }
  if (minimized.value && ready.value) send('chanshui.CATALOG', { catalog: catalog.value })
  minimized.value = false
  send('chanshui.VISIBILITY', { visible: true })
}
const reload = () => { if (dirty.value && !window.confirm('检测设置有未保存的修改，确定重新载入并放弃吗？')) return; if (!workspaceReady()) { waitForWorkspace(); return }; dirty.value = false; invalidate(); arm(); if (iframe.value) iframe.value.src = '/chanshui/'; restoreChanshuiCatalog(props.workspaceId); if (!catalog.value) void loadCatalog() }
const checkFirstStart = (scope: number, id: string, workspaceId: string, params: Record<string, unknown>, batch: boolean) => {
  if (!open.value || !ready.value || minimized.value || scope !== generation || workspaceId !== props.workspaceId || !workspaceReady()) return '检测页已关闭、收起或切换工作区。'
  try {
    if (session !== getAccessToken()) return '登录状态已切换。'
    const tasks = batch ? params.tasks : [params]
    if (!Array.isArray(tasks) || tasks.length < 1 || tasks.length > 32) return '检测组合必须包含1～32份报告。'
    const protocols: { targetId: string; groupId: string; protocol: unknown }[] = []
    for (const task of tasks) {
      if (!task || typeof task !== 'object' || typeof task.groupId !== 'string' || typeof task.targetId !== 'string') return '检测组合中的分组或账号无效。'
      const group = catalog.value?.groups.find(group => group.id === task.groupId)
      const account = group?.accounts.find(account => account.targetId === task.targetId)
      if (!account || group?.accountsError) return '当前名单已没有这个分组或账号。请点击窗口顶部“更新分组和账号”后重新选择。'
      protocols.push({ targetId: task.targetId, groupId: task.groupId, protocol: account.protocol })
    }
    const guard = new CustomEvent('chanshui.before-start', { cancelable: true, detail: { generation: scope, id, ...(batch ? { protocols } : { protocol: protocols[0].protocol }) } })
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
    if (!workspaceReady()) { waitForWorkspace(); return }
    invalidate(); ready.value = true; error.value = ''; clearTimers()
    send('chanshui.BOOT', { workspaceId: props.workspaceId, target: targetSnapshot(), visible: !minimized.value, catalog: catalog.value })
    return
  }
  if (message.kind === 'chanshui.DIRTY' && message.generation === generation && typeof message.dirty === 'boolean') { dirty.value = message.dirty; return }
  if (message.kind === 'chanshui.abort' && message.generation === generation && typeof message.id === 'string') {
    const existing = requests.get(message.id)
    if (existing) abortRequest(message.id, existing)
    return
  }
  if (message.kind !== 'chanshui.request' || message.generation !== generation || typeof message.id !== 'string' || !/^[a-zA-Z0-9-]{1,80}$/.test(message.id) || typeof message.op !== 'string') return
  const scope = generation, requestId = message.id, workspaceId = props.workspaceId, op = message.op
  if (requests.has(requestId)) return
  const priorityStop = op === 'cancel'
  const priorityState = message.afterStop === true && ['report', 'batch', 'runtime'].includes(op)
  const guardStart = op === 'start-batch' || (op === 'start' && message.guardStart === true)
  const ordinaryCount = requests.size - (stopRequest ? 1 : 0) - (stateRequest ? 1 : 0)
  if ((!priorityStop && !priorityState && ordinaryCount >= 12) || (priorityStop && stopRequest) || (priorityState && stateRequest)) {
    send('chanshui.response', { id: requestId, ok: false, error: { message: priorityStop ? '停止请求正在处理，已保存回答仍可查看；请稍候核查运行状态。' : '正在处理其他页面操作，已显示内容仍可查看；请稍后重试读取。', status: 429 } })
    return
  }
  let params: Record<string, unknown>
  try {
    params = message.params && typeof message.params === 'object' && !Array.isArray(message.params) ? message.params : {}
    if (new TextEncoder().encode(JSON.stringify(params)).byteLength > 128 * 1024) throw new Error('请求内容超过允许大小。')
    chanshuiRequestSpec(op, params)
  } catch (e) { send('chanshui.response', { id: requestId, ok: false, error: { message: e instanceof Error ? e.message : '此操作无法识别。', operationResult: 'not_started' } }); return }
  const controller = new AbortController()
  const request: HostRequest = { controller, started: false, op, timer: setTimeout(() => abortRequest(requestId, request), 45000) }
  requests.set(requestId, request)
  if (priorityStop) stopRequest = request
  if (priorityState) stateRequest = request
  const current = () => scope === generation && requests.get(requestId) === request && !controller.signal.aborted
  const run = async () => {
    if (!current()) return
    try {
      const blockedStart = guardStart ? checkFirstStart(scope, requestId, workspaceId, params, op === 'start-batch') : ''
      if (blockedStart) {
        send('chanshui.response', { id: requestId, ok: false, error: { message: `${blockedStart} 请回到新建检测后重新操作。本次没有发送问题。`, status: 409, operationResult: 'not_started' } })
        return
      }
      let warning: Record<string, unknown> | undefined
      request.started = true
      const value = await requestChanshui(op, params, workspaceId, controller.signal, value => { warning = value })
      if (current()) send('chanshui.response', { id: requestId, ok: true, value, ...(warning ? { warning } : {}) })
    } catch (e) {
      if (current()) send('chanshui.response', { id: requestId, ok: false, error: { ...(e instanceof ChanshuiError ? e.details : {}), message: e instanceof Error ? e.message : '读取结果时中断，请重试读取。', status: e instanceof ChanshuiError ? e.status : 0 } })
    } finally { releaseRequest(requestId, request) }
  }
  if (priorityStop || priorityState) void run()
  else serial = serial.then(run, run)
}
const openSharedReport=()=>{const reportId=new URLSearchParams(window.location.search).get('detectorReport');if(props.workspaceId && reportId && /^[0-9a-f]{32}$/.test(reportId)) void launch(new CustomEvent(chanshuiOpenEvent,{detail:{reportId}}))}
watch([() => props.workspaceId, workspaceReady], ([workspaceId, confirmed], [previousWorkspace]) => {
  const waiting = open.value && !iframe.value
  if (workspaceId !== previousWorkspace) {
    if (!waiting) close()
    clearChanshuiCatalogForWorkspace(workspaceId)
  }
  if (!confirmed) { if (open.value) waitForWorkspace(); return }
  if (open.value && !iframe.value) void preparePage()
  else if (!open.value) openSharedReport()
})
watch(catalog, value => { if (open.value && ready.value && !minimized.value && workspaceReady()) send('chanshui.CATALOG', { catalog: value }) })
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
        <div v-if="!workspaceReady()" role="status" class="shrink-0 border-b border-border px-4 py-3 text-sm"><p>尚未确认当前工作区。确认后会继续打开；若一直停在这里，请刷新页面或重新选择工作区，再打开模型检测。也可点击「重试打开」检查已有结果，此时没有读取账号或开始检测。</p><button type="button" class="mt-2 rounded-md border border-border px-3 py-1.5" @click="preparePage">重试打开</button></div>
        <template v-else>
          <div class="shrink-0 border-b border-border px-4 py-2 text-xs"><button type="button" aria-label="更新分组和账号" class="rounded-md border border-border px-3 py-1.5 disabled:opacity-50" :disabled="catalogLoading" @click="loadCatalog">更新分组和账号</button><p class="mt-1 text-muted-foreground">优先使用本机上次保存的名单。更新只读取当前分组和账号，不会刷新分组健康、修改主站或开始检测。</p><p v-if="catalogLoading" role="status" class="mt-1">正在读取分组和账号；已有名单仍可查看，此时没有开始检测。</p><p v-if="catalogError" role="alert" class="mt-1 text-destructive">{{ catalogError }}</p><p v-if="catalogWarning" role="status" class="mt-1 text-amber-600">{{ catalogWarning }}</p></div>
          <div v-if="error || !ready" class="shrink-0 border-b border-border px-4 py-3 text-sm" role="status"><p :class="error ? 'text-destructive' : 'text-muted-foreground'">{{ error || loadingText }}</p><button v-if="error" type="button" class="mt-2 inline-flex items-center gap-2 rounded-md border border-border px-3 py-1.5" @click="reload"><RotateCw class="h-4 w-4" />重新载入页面</button></div>
          <iframe ref="iframe" src="/chanshui/" title="账号模型检测" class="min-h-0 w-full flex-1 border-0" referrerpolicy="same-origin" />
        </template>
      </section>
    </div>
    <div v-if="open && minimized" class="fixed bottom-4 right-4 z-[81] flex max-w-[calc(100vw-2rem)] items-center rounded-lg border border-border bg-background shadow-lg" data-testid="chanshui-minimized"><button type="button" class="min-w-0 px-4 py-3 text-left text-sm" @click="restore"><span class="block truncate font-medium">模型检测<span v-if="target.accountName"> · {{ target.accountName }}</span></span><span class="block text-xs text-muted-foreground">已收起，恢复后查看最新结果。</span></button><button type="button" class="mr-1 rounded-md p-2 text-muted-foreground hover:bg-surface" aria-label="关闭收起的检测页" @click="requestClose"><X class="h-4 w-4" /></button></div>
  </Teleport>
</template>
