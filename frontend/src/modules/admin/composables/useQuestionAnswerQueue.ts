import { onBeforeUnmount, ref, shallowRef, watch, type Ref, type ShallowRef } from 'vue'
import { useDocumentVisibility } from '@vueuse/core'
import { getAccessToken, subscribeAuthSessionChange } from '@/modules/auth/api/auth'
import { getQuestionAnswerQueue } from '../api/connectionHealth'
import type { QuestionAnswerQueue } from '../types/connectionHealth'

type Observer = { visible: () => boolean; active: () => boolean }
type QueueState = {
  snapshot: ShallowRef<QuestionAnswerQueue | null>; error: Ref<string>; stale: Ref<boolean>; loading: Ref<boolean>
  observers: Set<Observer>; timer: ReturnType<typeof setTimeout> | null; controller: AbortController | null
  generation: number; refreshRequested: boolean
}
const scopes = new Map<string, QueueState>()
// This identity stays private in memory and is never returned, serialized or
// used as a cache key. Only the existing auth store/clear can notify a change.
let sessionIdentity: string | null = null
let unsubscribeAuth: (() => void) | null = null

const clearTimer = (state: QueueState) => { if (state.timer !== null) clearTimeout(state.timer); state.timer = null }
const suspend = (state: QueueState) => {
  state.generation++; clearTimer(state); state.controller?.abort(); state.controller = null; state.loading.value = false; state.refreshRequested = false
}
const invalidateSession = () => {
  sessionIdentity = getAccessToken()
  for (const entry of scopes.values()) {
    suspend(entry); entry.snapshot.value = null; entry.error.value = ''; entry.stale.value = false
  }
}
const sessionMatches = () => {
  if (sessionIdentity === getAccessToken()) return true
  invalidateSession()
  return false
}
const visible = (state: QueueState) => [...state.observers].some(observer => observer.visible())
const shouldPoll = (state: QueueState) => visible(state) && ([...state.observers].some(observer => observer.visible() && observer.active())
  || Boolean(state.snapshot.value && (state.snapshot.value.heldCount > 0 || Object.values(state.snapshot.value.counts).some(count => count > 0))))

const load = async (workspace: string, state: QueueState, explicit = false) => {
  if (!sessionMatches() || !visible(state) || scopes.get(workspace) !== state) return
  if (state.controller) { if (explicit) state.refreshRequested = true; return }
  clearTimer(state)
  const identity = sessionIdentity, own = ++state.generation, controller = new AbortController()
  state.controller = controller; state.loading.value = true
  const current = () => sessionMatches() && scopes.get(workspace) === state && own === state.generation && identity === sessionIdentity && !controller.signal.aborted && visible(state)
  try {
    const result = await getQuestionAnswerQueue(workspace, controller.signal)
    if (!current()) return
    if (!result.counts || ![result.queued, result.processing, result.stopping, result.preparingTargets, result.finalizingBatches].every(Array.isArray)) throw new Error('后台返回的排队信息不完整，请重新读取；这次只查询状态，没有发题')
    state.snapshot.value = result; state.error.value = ''; state.stale.value = false
  } catch (cause) {
    if (!current()) return
    state.error.value = cause instanceof Error ? cause.message : '读取排队信息时中断，接口没有提供更具体原因，请重新读取'
    state.stale.value = true
  } finally {
    if (own === state.generation && state.controller === controller) {
      state.controller = null; state.loading.value = false
      const requested = state.refreshRequested; state.refreshRequested = false
      if (requested && current()) void load(workspace, state)
      else if (current() && shouldPoll(state)) state.timer = setTimeout(() => void load(workspace, state), 2000)
    }
  }
}

export const refreshQuestionAnswerQueue = (workspace: string) => {
  const state = scopes.get(workspace)
  if (state) void load(workspace, state, true)
}

export const useQuestionAnswerQueue = (options: { workspace: () => string; visible: () => boolean; active: () => boolean }) => {
  const snapshot = shallowRef<QuestionAnswerQueue | null>(null), error = ref(''), stale = ref(false), loading = ref(false)
  const visibility = useDocumentVisibility()
  const observer: Observer = { visible: () => options.visible() && visibility.value === 'visible', active: options.active }
  let workspace = '', state: QueueState | undefined, stopMirror: (() => void) | undefined
  const detach = () => {
    stopMirror?.(); stopMirror = undefined
    if (state) {
      state.observers.delete(observer)
      if (state.observers.size === 0) { suspend(state); scopes.delete(workspace) }
      else if (!visible(state)) suspend(state)
    }
    state = undefined; snapshot.value = null; error.value = ''; stale.value = false; loading.value = false
    if (scopes.size === 0) { unsubscribeAuth?.(); unsubscribeAuth = null; sessionIdentity = null }
  }
  watch(() => [options.workspace(), observer.visible(), options.active()] as const, ([next, shown], previous) => {
    if (next !== workspace || !state) {
      detach(); workspace = next
      if (!workspace || workspace === 'anonymous' || !shown) return
      if (!unsubscribeAuth) {
        sessionIdentity = getAccessToken()
        unsubscribeAuth = subscribeAuthSessionChange(() => {
          invalidateSession()
          for (const [scope, entry] of scopes) {
            if (sessionIdentity !== null && visible(entry)) void load(scope, entry)
          }
        })
      }
      state = scopes.get(workspace)
      if (!state) {
        state = { snapshot: shallowRef(null), error: ref(''), stale: ref(false), loading: ref(false), observers: new Set(), timer: null, controller: null, generation: 0, refreshRequested: false }
        scopes.set(workspace, state)
      }
      state.observers.add(observer)
      const entry = state
      stopMirror = watch(() => [entry.snapshot.value, entry.error.value, entry.stale.value, entry.loading.value] as const, ([value, reason, old, busy]) => {
        snapshot.value = value; error.value = reason; stale.value = old; loading.value = busy
      }, { immediate: true, flush: 'sync' })
      // Joining another panel shares its pending read rather than requesting a
      // second one. Opening an idle view performs one read, never discovery polling.
      if (!entry.controller && entry.timer === null) void load(workspace, entry)
    } else if (!shown) {
      if (!visible(state)) { suspend(state); state.stale.value = true }
    } else if (!previous?.[1] || options.active() !== previous?.[2]) void load(workspace, state)
  }, { immediate: true, flush: 'sync' })
  onBeforeUnmount(detach)
  return { snapshot, error, stale, loading, refresh: () => { if (state) void load(workspace, state, true) } }
}
