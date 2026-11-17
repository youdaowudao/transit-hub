import { ref } from 'vue'
import type { ChanshuiTargetInput } from '../api/chanshui'

export const chanshuiOpenEvent = 'transithub:open-chanshui'
const enabled = ref(false)
export function openChanshui(target: ChanshuiTargetInput = {}): void {
  window.dispatchEvent(new CustomEvent(chanshuiOpenEvent, { detail: target }))
}
export function useChanshuiLauncher() {
  const load = (workspaceId: string): Promise<void> => {
    enabled.value = Boolean(workspaceId)
    return Promise.resolve()
  }
  const clear = () => { enabled.value = false }
  return { enabled, load, clear, open: openChanshui }
}
