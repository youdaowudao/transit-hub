import { HUB_KINDS } from './hub-policy';
import type { HubCatalog, HubContext, HubProtocol } from './hub-types';

const allSections = HUB_KINDS.map(section => section.value);
export const sectionsStorageKey = (workspaceId: string) => 'transithub.chanshui.sections.v1:' + workspaceId;
export const storageWarning = '当前选择可以使用，浏览器没能记住这次勾选。重新打开后请核对勾选项。';
export const missingSelection = '这个分组或账号已不在当前名单。请重新选择；此时没有开始检测。';
export const checkingContext = '正在核对所选账号和协议，尚未发送问题。';
export const pendingWarning = '本次受理尚未确认。可用原报告编号查询，不会再次发送。用原编号重试受理时，如果首次未受理，可能执行检测并产生费用。关闭前请保留原编号。';

export function readSections(workspaceId: string): { sections: string[]; warning: string } {
  try {
    const raw = localStorage.getItem(sectionsStorageKey(workspaceId));
    if (raw !== null) {
      let parsed: unknown;
      try { parsed = JSON.parse(raw); } catch { return { sections: [...allSections], warning: '' }; }
      if (Array.isArray(parsed) && parsed.every(value => typeof value === 'string')) {
        return { sections: allSections.filter(value => parsed.includes(value)), warning: '' };
      }
    }
    return { sections: [...allSections], warning: '' };
  } catch {
    return { sections: [...allSections], warning: storageWarning };
  }
}

export interface HubDraft {
  groupId: string;
  targetId: string;
  model: string;
  modelNotice: string;
  selectionError: string;
}
export const emptyDraft: HubDraft = { groupId: '', targetId: '', model: '', modelNotice: '', selectionError: '' };
// upstreamModelIds 整理检测宿主 models 读取的回包：兼容字符串和 { id } 两种写法，按原值去重并保持上游顺序。
// 和 V3.1.29 相同：排除空字符串，其余模型 ID 原样保留。
export const upstreamModelIds = (value: { models?: unknown } | null | undefined): string[] => {
  const list = Array.isArray(value?.models) ? value.models : [];
  const ids = list.map(item => typeof item === 'string' ? item : item && typeof item === 'object' && typeof (item as { id?: unknown }).id === 'string' ? (item as { id: string }).id : '');
  return Array.from(new Set(ids.filter(Boolean)));
};
// 使用本次计数及分组、账号标记读取结果，避免新读取启动前显示上次的列表或失败原因。
export const modelKey = (reload: number, groupId: string, targetId: string) => groupId && targetId ? `${reload}\n${groupId}\n${targetId}` : '';
// waitingForModels：已选中账号、但本次读取还没有结果（包括刷新或重新打开后、读取刚要开始的那次渲染）都算读取中，
// 此时模型框、开始检测和发送问题都不可用。
export const waitingForModels = (hasAccount: boolean, status?: 'loading' | 'done' | 'failed') => hasAccount && status !== 'done' && status !== 'failed';
// 桥接超时和宿主连不上调度站时的通用提示原文，含只适用于报告的指引；模型读取时整句换成读取模型的说法。
const reportOnlyReasons: Record<string, string> = {
  '等待调度站结果超过45秒，请重试读取；若刚开始检测，请按原报告编号查询，部分请求可能已计费。': '等待调度站结果超过45秒',
  '无法连接调度站。请检查网络后重试读取；若刚点击开始，请按原报告编号查询，部分请求可能已经计费。': '无法连接调度站，请检查网络',
};
// modelReadReason 给模型读取失败用的原因：调度站登录失效只按 401 判断；上面两句通用提示整句替换；其他原因（含上游原文）原样保留。
export const modelReadReason = (reason: string, status = 0): string => {
  if (status === 401) return '登录已失效，请重新登录调度站后再选择账号';
  const text = reason.trim();
  // 宿主读不懂调度站回包时的提示带 HTTP 状态和「不要重复新建检测」；读取模型时只保留原因和状态。
  const unreadable = /^调度站返回的内容无法读取（HTTP (\d+)）。请查看运行诊断后重试读取；不要重复新建检测。$/.exec(text);
  if (unreadable) return `调度站返回的内容无法读取（HTTP ${unreadable[1]}），请查看运行诊断`;
  return reportOnlyReasons[text] ?? (text.replace(/[。.]+$/, '') || '原因没有返回，请到分组健康核对此账号的地址和密钥');
};
export const catalogGroup = (catalog: HubCatalog | null, groupId: string) => catalog?.groups.find(group => group.id === groupId);
export const catalogAccount = (catalog: HubCatalog | null, groupId: string, targetId: string) => {
  const group = catalogGroup(catalog, groupId);
  return group?.accountsError ? undefined : group?.accounts.find(account => account.targetId === targetId);
};

export function selectDraft(catalog: HubCatalog | null, groupId = '', targetId = ''): HubDraft {
  const group = catalogGroup(catalog, groupId);
  const account = catalogAccount(catalog, groupId, targetId);
  if ((groupId && !group) || (targetId && !account)) return { ...emptyDraft, selectionError: missingSelection };
  // 模型不从名单取：选中账号后由页面向上游读取，读到后再按分组默认规则预选。
  return { ...emptyDraft, groupId, targetId };
}

export interface DraftAccount { groupId: string; targetId: string; protocol?: HubProtocol }

export function verifiedContext(context: HubContext, workspaceId: string, draft: DraftAccount) {
  if (!context?.canRun) throw new Error(context?.reason || '此账号当前不可检测，服务器没有返回具体原因。');
  if (context.workspaceId !== workspaceId || context.targetId !== draft.targetId || context.groupId !== draft.groupId) {
    throw new Error('服务器返回的工作区、分组或账号与所选内容不一致。');
  }
  if (!['chat', 'openai', 'anthropic'].includes(context.protocol) || draft.protocol && context.protocol !== draft.protocol) {
    throw new Error('服务器返回的检测协议与所选账号的协议不同，或无法识别此协议。');
  }
  const expected = context.expectedContext;
  if (!expected || !context.baseURL || expected.workspaceId !== context.workspaceId || expected.targetId !== context.targetId || expected.groupId !== context.groupId || expected.baseURL !== context.baseURL || expected.protocol !== context.protocol) {
    throw new Error('服务器没有返回完整且一致的账号地址或检测配置。');
  }
  return { ...expected };
}
