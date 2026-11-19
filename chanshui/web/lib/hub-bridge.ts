import type { HubCatalog, HubProtocol, HubStartApproval, HubTargetInput } from './hub-types';
export class HubRequestError extends Error {
  constructor(message: string, readonly status = 0, readonly details: Record<string, unknown> = {}) { super(message); }
}
export interface HubBoot { workspaceId: string; generation: number; visible: boolean; target: HubTargetInput; catalog: HubCatalog | null }
type Pending = { resolve: (value: unknown) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout>; approveStart?: HubStartApproval };
export function createHubBridge(onBoot: (boot: HubBoot) => void, onTarget: (target: HubTargetInput) => void, onVisibility: (visible: boolean) => void, onWarning: (warning: Record<string, unknown>) => void, onCatalog: (catalog: HubCatalog | null) => void, onDiscardChecks: (nextTarget?: HubTargetInput) => void) {
  let generation: number | null = null, disposed = false, sequence = 0;
  const pending = new Map<string, Pending>();
  const clearPending = () => { pending.forEach(item => { clearTimeout(item.timer); item.reject(new HubRequestError('检测页已重新加载或已切换工作区，请重新读取。')); }); pending.clear(); };
  const handler = (event: MessageEvent) => {
    if (event.origin !== location.origin || event.source !== parent || !event.data || event.data.v !== 1) return;
    const m = event.data;
    if (m.kind === 'chanshui.BOOT') {
      if (!Number.isSafeInteger(m.generation) || (generation !== null && m.generation <= generation)) return;
      clearPending(); generation = m.generation; onBoot(m as HubBoot); return;
    }
    if (m.generation !== generation) return;
    if (m.kind === 'chanshui.OPEN_TARGET') { onTarget(m.target ?? {}); return; }
    if (m.kind === 'chanshui.VISIBILITY') { onVisibility(m.visible === true); return; }
    if (m.kind === 'chanshui.CATALOG') { onCatalog(m.catalog ?? null); return; }
    if (m.kind !== 'chanshui.response' || typeof m.id !== 'string') return;
    const item = pending.get(m.id); if (!item) return;
    pending.delete(m.id); clearTimeout(item.timer);
    if (m.ok) { if (m.warning && typeof m.warning.message === 'string') onWarning(m.warning); item.resolve(m.value); } else item.reject(new HubRequestError(typeof m.error?.message === 'string' ? m.error.message : '检测操作未完成，请查看运行诊断后重试读取。', Number(m.error?.status ?? 0), m.error ?? {}));
  };
  const beforeStart = (event: Event) => {
    const detail = (event as CustomEvent<{ generation: number; id: string; protocol?: HubProtocol }>).detail;
    if (!event.cancelable || event.bubbles || !detail || detail.generation !== generation) return;
    const item = pending.get(detail.id);
    const approve = item?.approveStart;
    if (!item || !approve) return;
    item.approveStart = undefined;
    // The host sends the first POST only if this same-origin, synchronous exchange succeeds.
    if (approve(detail.protocol)) event.preventDefault();
  };
  const discardChecks = (event: Event) => {
    const detail = (event as CustomEvent<{ generation: number; target?: HubTargetInput }>).detail;
    if (generation !== null && !event.bubbles && detail?.generation === generation) onDiscardChecks(detail.target);
  };
  window.addEventListener('message', handler);
  window.addEventListener('chanshui.before-start', beforeStart);
  window.addEventListener('chanshui.discard-checks', discardChecks);
  parent.postMessage({ kind: 'chanshui.READY', v: 1 }, location.origin);
  const call = <T = unknown>(op: string, params: Record<string, unknown> = {}, approveStart?: HubStartApproval): Promise<T> => new Promise((resolve, reject) => {
    if (disposed || generation === null) { reject(new HubRequestError('请从调度站的模型检测按钮打开此页面。')); return; }
    const id = `hub-${Date.now()}-${++sequence}`;
    const timer = setTimeout(() => { pending.delete(id); reject(new HubRequestError('等待调度站结果超过45秒，请重试读取；若刚开始检测，请按原报告编号查询，部分请求可能已计费。')); }, 45000);
    pending.set(id, { resolve: value => resolve(value as T), reject, timer, approveStart });
    parent.postMessage({ kind: 'chanshui.request', v: 1, generation, id, op, params, ...(approveStart && { guardStart: true }) }, location.origin);
  });
  return { call, setDirty(dirty: boolean) { if (!disposed && generation !== null) parent.postMessage({ kind: 'chanshui.DIRTY', v: 1, generation, dirty }, location.origin); }, dispose() { disposed = true; clearPending(); window.removeEventListener('message', handler); window.removeEventListener('chanshui.before-start', beforeStart); window.removeEventListener('chanshui.discard-checks', discardChecks); } };
}
