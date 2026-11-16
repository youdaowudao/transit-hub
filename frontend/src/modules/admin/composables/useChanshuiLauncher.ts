import { ref } from 'vue'
import { requestChanshui, type ChanshuiRuntime, type ChanshuiTargetInput } from '../api/chanshui'

export const chanshuiOpenEvent = 'transithub:open-chanshui'
const enabled = ref(false)
let currentScope = '', version = 0, pending: Promise<void> | undefined
export function openChanshui(target: ChanshuiTargetInput = {}): void {
  window.dispatchEvent(new CustomEvent(chanshuiOpenEvent, { detail: target }))
}
export function useChanshuiLauncher() {
  const load = (workspaceId: string): Promise<void> => {
    if (workspaceId === currentScope && pending) return pending
    currentScope = workspaceId
    enabled.value = false
    const generation = ++version
    if (!workspaceId) return Promise.resolve()
    pending = requestChanshui<ChanshuiRuntime>('runtime', {}, workspaceId).then(value => { if (version === generation) enabled.value = value.enabled === true }).catch(() => { if (version === generation) enabled.value = false })
    return pending
  }
  const clear = () => { ++version; currentScope = ''; pending = undefined; enabled.value = false }
  return { enabled, load, clear, open: openChanshui }
}
