import { ref } from 'vue'
import { getQuestionAnswerRecentSummaries } from '../api/connectionHealth'
import type { QuestionAnswerRecentSummaryItem } from '../types/connectionHealth'

export const useQuestionAnswerRecentSummaries = (options: {
  workspace: () => string
  visible: () => boolean
  apply: (item: QuestionAnswerRecentSummaryItem) => void
}) => {
  const failures = ref<string[]>([])
  const pending = new Map<string, number>()
  const settled = new Map<string, number>()
  const inFlight = new Map<string, AbortController>()
  let lifecycle = 0
  let flushScheduled = false

  const suspend = () => {
    lifecycle++
    for (const controller of new Set(inFlight.values())) controller.abort()
    inFlight.clear()
  }
  const reset = () => {
    suspend()
    pending.clear()
    settled.clear()
    failures.value = []
  }
  const flush = async () => {
    if (!options.visible()) return
    const workspace = options.workspace(), generation = lifecycle
    const ids = [...pending].filter(([id, version]) => id.startsWith(`sub2api:${workspace}:`) && version > (settled.get(id) ?? 0) && !inFlight.has(id)).map(([id]) => id)
    const requests: Promise<void>[] = []
    for (let offset = 0; offset < ids.length; offset += 50) {
      const chunk = ids.slice(offset, offset + 50), controller = new AbortController()
      const versions = new Map(chunk.map(id => [id, pending.get(id)!]))
      for (const id of chunk) inFlight.set(id, controller)
      const current = () => generation === lifecycle && workspace === options.workspace() && options.visible() && !controller.signal.aborted
      requests.push((async () => {
        let changed = false
        try {
          const items = await getQuestionAnswerRecentSummaries(chunk, controller.signal)
          if (!current()) return
          for (const item of items) {
            if (pending.get(item.targetId) !== versions.get(item.targetId)) { changed = true; continue }
            options.apply(item)
            settled.set(item.targetId, versions.get(item.targetId)!)
            failures.value = failures.value.filter(id => id !== item.targetId)
          }
        } catch {
          if (!current()) return
          failures.value = [...new Set([...failures.value, ...chunk])]
          changed = chunk.some(id => pending.get(id) !== versions.get(id))
        } finally {
          for (const id of chunk) if (inFlight.get(id) === controller) inFlight.delete(id)
          if (changed && current()) scheduleFlush()
        }
      })())
    }
    await Promise.all(requests)
  }
  const scheduleFlush = () => {
    if (flushScheduled) return
    flushScheduled = true
    queueMicrotask(() => { flushScheduled = false; void flush() })
  }
  const refreshTargets = (ids: string[]) => {
    for (const id of new Set(ids)) if (id.startsWith(`sub2api:${options.workspace()}:`)) pending.set(id, (pending.get(id) ?? 0) + 1)
    scheduleFlush()
  }
  return { failures, refreshTargets, retry: (id: string) => refreshTargets([id]), resume: scheduleFlush, suspend, reset }
}
