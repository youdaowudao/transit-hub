import { HUB_KINDS, selectHubDefaultModel } from './hub-policy';
import type { HubCatalog, HubCatalogAccount, HubContext, HubProtocol } from './hub-types';

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
export const catalogModels = (account?: HubCatalogAccount) => {
  const names = (account?.models ?? '').split(',').map(model => model.trim()).filter(Boolean);
  if (names.length) return Array.from(new Set(names));
  return account?.probeModels ?? [];
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
  const models = catalogModels(account);
  const selected = models.length ? selectHubDefaultModel(models, group?.name ?? '') : { model: '', notice: '' };
  return { ...emptyDraft, groupId, targetId, model: selected.model, modelNotice: selected.notice };
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
