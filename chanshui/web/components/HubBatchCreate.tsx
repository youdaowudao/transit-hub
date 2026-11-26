'use client';
import { useEffect, useRef, useState, type MutableRefObject, type ReactNode } from 'react';
import Select from './Select';
import { catalogAccount, catalogGroup, upstreamModelIds, verifiedContext, modelReadReason } from '../lib/hub-draft';
import { HUB_KINDS, newHubID } from '../lib/hub-policy';
import type { HubBatchAccepted, HubBatchDetail, HubBatchRequest, HubCall, HubCatalog, HubContext, HubPage, HubProtocol, HubQuestionSummary, HubSettings } from '../lib/hub-types';
import { HubRequestError } from '../lib/hub-bridge';

export type BatchModelRead = { protocol?: HubProtocol; catalog: HubCatalog; context?: Record<string, string>; models: string[]; status: 'loading' | 'done' | 'failed'; reason?: string; promise?: Promise<void> };
export const batchAccountKey = (targetId: string, groupId: string) => JSON.stringify([targetId, groupId]);
type Chosen = { key: string; targetId: string; groupId: string; models: string[]; manual: string };
const reason = (e: unknown) => e instanceof Error ? e.message : '原因尚未确认，请核查运行状态。';
const newRow = (targetId: string, groupId: string): Chosen => ({ key: batchAccountKey(targetId, groupId), targetId, groupId, models: [], manual: '' });
const copyNumber = (id: string) => navigator.clipboard ? navigator.clipboard.writeText(id) : Promise.reject(new Error('浏览器未开放剪贴板'));

export default function HubBatchCreate({ call, catalog, catalogNow, workspaceId, active, sections, settings, available, sectionPicker, modelsCache, approvalToken, canApprove, onAccepted }: {
  call: HubCall; catalog: HubCatalog | null; catalogNow: () => HubCatalog | null; workspaceId: string; active: boolean; sections: string[];
  settings: HubSettings | null; available: boolean; sectionPicker: ReactNode; modelsCache: MutableRefObject<Map<string, BatchModelRead>>;
  approvalToken: () => number; canApprove: (token: number) => boolean; onAccepted: (id: string, known?: HubBatchDetail) => void;
}) {
  const [groupId, setGroupId] = useState(''), [chosen, setChosen] = useState<Chosen[]>([]), [notice, setNotice] = useState(''), [epoch, setEpoch] = useState(0);
  const [frozen, setFrozen] = useState<HubBatchRequest | null>(null), frozenRef = useRef<HubBatchRequest | null>(null);
  const [unknown, setUnknown] = useState(false), [busy, setBusy] = useState(false), busyRef = useRef(false), alive = useRef(true);
  const [partial, setPartial] = useState(false), [selectedQuestions, setSelectedQuestions] = useState<Record<string, HubQuestionSummary>>({});
  const selectedQuestionsRef = useRef(selectedQuestions); selectedQuestionsRef.current = selectedQuestions;
  const [invalidQuestions, setInvalidQuestions] = useState<string[]>([]);
  const scanQuestions = useRef<Record<string, Set<string>>>({});
  const [pages, setPages] = useState<Record<string, HubPage<HubQuestionSummary>>>({}), [questionError, setQuestionError] = useState('');
  const questionBusy = useRef(new Set<string>());
  const currentRows = useRef(chosen); currentRows.current = chosen;
  const rerender = () => { if (alive.current) setEpoch(n => n + 1); };
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const replaceFrozen = (value: HubBatchRequest | null) => { frozenRef.current = value; setFrozen(value); };
  const cached = (row: Chosen) => {
    const read = modelsCache.current.get(row.key), account = catalogAccount(catalog, row.groupId, row.targetId);
    return read && read.catalog === catalog && account && read.protocol === account.protocol ? read : undefined;
  };
  const readModels = async (row: Chosen, force = false) => {
    const source = catalogNow(), account = catalogAccount(source, row.groupId, row.targetId);
    if (!source || !account || !alive.current) return;
    let old = modelsCache.current.get(row.key);
    if (old?.catalog !== source || old.protocol !== account.protocol) old = undefined;
    if (!force && old) { if (old.promise) await old.promise; if (!modelsCache.current.has(row.key) && catalogNow() === source && currentRows.current.some(r => r.key === row.key)) return readModels(row); rerender(); return; }
    const read: BatchModelRead = { catalog: source, protocol: account.protocol, context: old?.context, status: 'loading', models: [] };
    modelsCache.current.set(row.key, read); rerender();
    const valid = () => alive.current && catalogNow() === source && modelsCache.current.get(row.key) === read;
    read.promise = (async () => {
      try {
        if (!read.context) read.context = verifiedContext(await call<HubContext>('context', { targetId: row.targetId, groupId: row.groupId }), workspaceId, {...row, protocol: account.protocol});
        if (!valid()) return;
        const list = await call<{models?: unknown}>('models', { targetId: row.targetId, groupId: row.groupId, expectedContext: read.context });
        if (valid()) { read.models = upstreamModelIds(list).map(m => m.trim()).filter(m => m && new TextEncoder().encode(m).length <= 256); read.status = 'done'; }
      } catch (e) { if (valid()) { read.status = 'failed'; read.reason = modelReadReason(reason(e), e instanceof HubRequestError ? e.status : 0); } }
      finally { read.promise = undefined; rerender(); }
    })();
    await read.promise;
  };
  const toggleAccount = (targetId: string, checked: boolean) => {
    const row = newRow(targetId, groupId);
    setChosen(old => checked ? [...old.filter(v => v.key !== row.key), row] : old.filter(v => v.key !== row.key));
    if (checked) void readModels(row);
  };
  const update = (key: string, fn: (row: Chosen) => Chosen) => setChosen(old => old.map(row => row.key === key ? fn(row) : row));
  const toggleModel = (key: string, model: string, checked: boolean) => update(key, row => ({ ...row, models: checked ? Array.from(new Set([...row.models, model])) : row.models.filter(m => m !== model) }));
  const applyModels = (row: Chosen) => {
    setChosen(old => old.map(other => {
      if (other.key === row.key || other.groupId !== row.groupId) return other;
      const list = cached(other)?.models ?? [];
      return { ...other, models: Array.from(new Set([...other.models, ...row.models.filter(m => list.includes(m))])) };
    }));
    setNotice('已将本账号勾选的模型应用到同组其他已选账号的已读取列表；未列出的模型仍需明确手动填写。');
  };
  const tasks = chosen.flatMap(row => row.models.map(model => ({ row, model })));
  const conflicts = tasks.filter((task, i) => tasks.findIndex(t => t.row.targetId === task.row.targetId && t.model === task.model) !== i);
  const actualQuestions = Object.values(selectedQuestions).filter(q => sections.includes(q.kind));
  const actualQuestionIds = actualQuestions.map(q => q.id);
  const invalidActualIds = invalidQuestions.filter(id => actualQuestionIds.includes(id));
  const labels = HUB_KINDS.filter(k => sections.includes(k.value)).map(k => k.label);
  const loadQuestions = async (kind: string, cursor = '') => {
    if (questionBusy.current.has(kind)) return;
    questionBusy.current.add(kind); setQuestionError('');
    try {
      const page = await call<HubPage<HubQuestionSummary>>('questions', { kind, cursor, limit: 20 });
      if (alive.current) {
        const seen = cursor ? scanQuestions.current[kind] ?? new Set<string>() : new Set<string>();
        page.items.forEach(q => seen.add(q.id)); scanQuestions.current[kind] = seen;
        const invalid = (q: HubQuestionSummary) => q.kind === kind && (page.items.some(next => next.id === q.id && !next.enabled && !(next.id === 'cache' && next.revision === 1)) || !page.nextCursor && !seen.has(q.id));
        setInvalidQuestions(old => Array.from(new Set([...old.filter(id => !page.items.some(q => q.id === id && (q.enabled || q.id === 'cache' && q.revision === 1))), ...Object.values(selectedQuestionsRef.current).filter(invalid).map(q => q.id)])));
        setPages(old => ({ ...old, [kind]: { ...page, items: cursor ? Array.from(new Map([...(old[kind]?.items ?? []), ...page.items].map(q => [q.id, q])).values()) : page.items } }));
      }
    } catch (e) { if (alive.current) setQuestionError(`读取题库摘要未完成：${reason(e)}。请重试读取。`); }
    finally { questionBusy.current.delete(kind); rerender(); }
  };
  const prepare = () => {
    if (busyRef.current || frozenRef.current || !active || !available || !settings || !Number.isSafeInteger(settings.revision) || !sections.length || !tasks.length || tasks.length > 32 || conflicts.length || partial && (!actualQuestionIds.length || invalidActualIds.length > 0)) return;
    const requestTasks: HubBatchRequest['tasks'] = [];
    for (const { row, model } of tasks) {
      const data = cached(row);
      if (!data?.context || data.status === 'loading' || data.context.protocol !== catalogAccount(catalog, row.groupId, row.targetId)?.protocol) { setNotice('所选账号或协议尚未核对，请重新读取该账号后再核对清单；本次未发送检测题目。'); return; }
      requestTasks.push({ reportId: newHubID(), targetId: row.targetId, groupId: row.groupId, model, expectedContext: { ...data.context } });
    }
    setNotice(''); setUnknown(false);
    replaceFrozen({ batchId: newHubID(), settingsRevision: settings.revision, selectedSectionIds: [...sections], selectedQuestionIds: partial ? Array.from(new Set(actualQuestionIds)) : [], tasks: requestTasks });
  };
  const submit = async (lookup = false) => {
    const request = frozenRef.current, token = approvalToken();
    if (!request || busyRef.current || !active) return;
    busyRef.current = true; setBusy(true); setNotice('');
    let approved = false;
    const live = () => alive.current && frozenRef.current === request;
    const accepted = (known?: HubBatchDetail) => {
      replaceFrozen(null); setUnknown(false); setChosen([]); setSelectedQuestions({}); setInvalidQuestions([]); setPartial(false); setNotice(''); onAccepted(request.batchId, known);
    };
    try {
      if (lookup) {
        const result = await call<HubBatchDetail>('batch', { batchId: request.batchId, offset: 0, limit: 20, view: 'progress' });
        if (!live()) return;
        if (result.batchId !== request.batchId) throw new Error('返回的批次编号不一致，请按原编号查询。');
        accepted(result); return;
      }
      const result = await call<HubBatchAccepted>('start-batch', { ...request }, undefined, { approveBatch: protocols => {
        if (!live() || !canApprove(token) || protocols.length !== request.tasks.length) return false;
        const current = catalogNow();
        approved = request.tasks.every((task, index) => {
          const entry = protocols[index], account = catalogAccount(current, task.groupId, task.targetId);
          return entry?.targetId === task.targetId && entry.groupId === task.groupId && entry.protocol === task.expectedContext.protocol && account?.protocol === entry.protocol;
        });
        return approved;
      } });
      if (!live()) return;
      if (!result.accepted || result.batchId !== request.batchId || !Array.isArray(result.reportIds) || result.reportIds.length !== request.tasks.length || !result.reportIds.every((id, i) => id === request.tasks[i].reportId)) throw new Error('返回的批次清单不一致，请按原编号查询。');
      accepted();
    } catch (e) {
      if (!live()) return;
      if (!lookup && !approved && !unknown) { replaceFrozen(null); setNotice(`清单核对未完成：${reason(e)}。本次没有发送问题，请重新核对。`); }
      else if (!lookup && !unknown && e instanceof HubRequestError && e.details.operationResult === 'not_started' && !e.details.batchId) { replaceFrozen(null); setNotice(`本次整批未创建：${reason(e)} 请检查后重新核对。`); }
      else { setUnknown(true); setNotice(`本批受理结果尚未确认：${reason(e)}。请查询原批次${request.batchId}，不要另建一批；已发请求是否发生以查询结果为准。`); }
    } finally { if (alive.current) { busyRef.current = false; setBusy(false); } }
  };
  const group = catalogGroup(catalog, groupId);
  const validTasks = tasks.every(({ row }) => {const read = cached(row);return read?.context?.protocol === catalogAccount(catalog, row.groupId, row.targetId)?.protocol && Boolean(read?.context) && read?.status !== 'loading';});
  const canPrepare = available && Boolean(settings) && sections.length > 0 && tasks.length > 0 && tasks.length <= 32 && !conflicts.length && validTasks && (!partial || actualQuestionIds.length > 0 && !invalidActualIds.length);
  return <section className="hero-card hub-card" aria-label="批量检测清单">
    <h2>批量检测</h2><p className="muted">选择多个账号和模型，共用下面勾选的检测项目。每个账号与模型组合生成一份报告，按顺序执行，不会修改主站。</p>
    <fieldset disabled={Boolean(frozen) || busy} className="hub-batch-fieldset">
      <div className="field"><label>批量分组<Select value={groupId} onChange={e => setGroupId(e.target.value)}><option value="">请选择分组</option>{catalog?.groups.map(g => <option key={g.id} value={g.id}>{`${g.name}${catalog.groups.some(other => other.id !== g.id && other.name === g.name) ? ` #${g.id}` : ''}`}</option>)}</Select></label></div>
      {!group && <p className="muted">请选择分组；未读到名单时请在窗口顶部更新。</p>}
      {group?.accountsError && <p className="hub-error">未读到该组账号，请点击窗口顶部「更新分组和账号」后重试。</p>}
      {group && !group.accountsError && <div className="hub-sections">{group.accounts.map(a => <label className="hub-checkbox" key={a.targetId}><input type="checkbox" checked={chosen.some(row => row.targetId === a.targetId && row.groupId === groupId)} onChange={e => toggleAccount(a.targetId, e.target.checked)} />{a.accountName}{a.accountId ? ` #${a.accountId}` : ''}</label>)}</div>}
      <p className="muted">只向所选账号读取模型列表，此时没有开始检测。未读到时可重试，或确认后手动填写模型名。</p>
      {chosen.map(row => { const read = cached(row), account = catalogAccount(catalog, row.groupId, row.targetId), models = Array.from(new Set([...(read?.models ?? []), ...row.models])); return <div className="hub-batch-account" key={row.key} data-batch-account={row.key}>
        <div className="section-heading"><strong>{account?.accountName || row.targetId} · {catalogGroup(catalog, row.groupId)?.name || row.groupId}</strong><button type="button" className="btn-secondary hub-small" onClick={() => setChosen(old => old.filter(other => other.key !== row.key))}>移除账号</button></div>
        {!account ? <p className="hub-error">此账号已不在当前名单，请移除或更新分组和账号。</p> : read?.status === 'loading' ? <p role="status">正在读取此账号的模型列表…</p> : !read || read.status === 'failed' ? <p className="hub-error">模型名单尚未确认{read?.reason ? `：${read.reason}` : ''}。<button type="button" className="hub-text-button" onClick={() => void readModels(row, true)}>重试读取模型</button></p> : <p className="muted">已读取 {read.models.length} 个模型；未列出的模型不会默认选择。</p>}
        <div className="hub-sections">{models.map(model => <label className="hub-checkbox" key={model}><input type="checkbox" checked={row.models.includes(model)} onChange={e => toggleModel(row.key, model, e.target.checked)} />{model}</label>)}</div>
        <div className="hub-search field"><label>手动填写模型名<input value={row.manual} onChange={e => update(row.key, v => ({ ...v, manual: e.target.value }))} /></label><button type="button" className="btn-secondary" disabled={!row.manual.trim()} onClick={() => { const model = row.manual.trim(); if (new TextEncoder().encode(model).length > 256) { setNotice('模型名最多256字节，请修改后再添加。'); return; } update(row.key, v => ({ ...v, models: Array.from(new Set([...v.models, model])), manual: '' })); }}>添加模型</button></div>
        <button type="button" className="btn-secondary hub-small" disabled={!row.models.length} onClick={() => applyModels(row)}>将所选模型应用到同组其他账号</button>
      </div>; })}
      {conflicts.map(({row, model}) => <p key={`${row.key}:${model}`} className="hub-error">同一账号的模型 {model} 在不同分组重复选择，请指定使用哪个组：{tasks.filter(t => t.row.targetId === row.targetId && t.model === model).map(t => <button key={t.row.key} type="button" className="btn-secondary hub-small" onClick={() => setChosen(old => old.map(other => other.targetId === row.targetId && other.key !== t.row.key ? { ...other, models: other.models.filter(m => m !== model) } : other))}>只使用 {catalogGroup(catalog, t.row.groupId)?.name || t.row.groupId}</button>)}</p>)}
      {sectionPicker}
      <details className="fold"><summary>选择单题（可选）</summary><p className="muted">默认全部已启用题目；选择部分题目时仅检测勾选分类内实际选中的题目，空选择不能提交。</p><label className="hub-checkbox"><input type="checkbox" checked={partial} onChange={e => setPartial(e.target.checked)} />选择部分题目</label>{partial && HUB_KINDS.filter(k => sections.includes(k.value)).map(k => <div key={k.value}><button type="button" className="btn-secondary hub-small" disabled={questionBusy.current.has(k.value)} onClick={() => void loadQuestions(k.value)}>{k.label} · 读取题目</button>{pages[k.value]?.items.filter(q => q.enabled || q.id === 'cache' && q.revision === 1).map(q => <label key={q.id} className="hub-checkbox"><input type="checkbox" checked={Boolean(selectedQuestions[q.id])} onChange={e => { const checked = e.target.checked; setSelectedQuestions(old => {const next = {...old}; if (checked) next[q.id] = q; else delete next[q.id]; return next;}); if (!checked) setInvalidQuestions(old => old.filter(id => id !== q.id)); }} />{q.title}</label>)}{pages[k.value]?.nextCursor && <button type="button" className="btn-secondary hub-small" onClick={() => void loadQuestions(k.value, pages[k.value].nextCursor)}>下一页题目</button>}</div>)}{partial && actualQuestions.length > 0 && <div className="hub-note"><strong>已勾选题目（包含其他页）</strong>{actualQuestions.map(q => <p key={q.id}>{q.title}{invalidActualIds.includes(q.id) && '（已禁用或删除，请移除）'} <button type="button" className="hub-text-button" onClick={() => {setSelectedQuestions(old => {const next = {...old}; delete next[q.id]; return next;}); setInvalidQuestions(old => old.filter(id=>id!==q.id));}}>移除此题</button></p>)}</div>}{invalidActualIds.length > 0 && <p role="alert" className="hub-error">所选题目已变化，请移除无效题目后核对清单；不会静默减少题目。</p>}{questionError && <p role="alert" className="hub-error">{questionError}</p>}</details>
      <p className="hub-note">已选择 {tasks.length} 份报告（账号 × 模型），共同项目：{labels.join('、') || '未选择'}。{partial ? `已选${actualQuestionIds.length}题` : '全部已启用题目'}。</p>
      {tasks.length > 32 && <p role="alert" className="hub-error">本批最多32份，请减少组合或分开提交；本次没有创建报告或发送检测请求。</p>}
      <button type="button" className="btn-primary" disabled={!canPrepare} onClick={prepare}>核对检测清单</button>
    </fieldset>
    {frozen && <section className="hub-note" aria-label="本批冻结清单"><h3>确认检测清单</h3><p>本批将创建{frozen.tasks.length}份报告，检测{HUB_KINDS.filter(k => frozen.selectedSectionIds.includes(k.value)).map(k => k.label).join('、')}。多轮检测和失败重试可能增加请求费用；确认后才开始，不修改主站。</p><p>创建时采用当前已保存题库。{frozen.selectedQuestionIds.length ? `已选择${frozen.selectedQuestionIds.length}题：${frozen.selectedQuestionIds.map(id => selectedQuestions[id]?.title || id).join('、')}。` : '全部已启用题目。'}</p><ol>{frozen.tasks.map(t => <li key={t.reportId}>{catalogAccount(catalog, t.groupId, t.targetId)?.accountName || t.targetId} · {catalogGroup(catalog, t.groupId)?.name || t.groupId} · {t.model}</li>)}</ol>
      <p>批次编号：<b>{frozen.batchId}</b> <button type="button" className="hub-text-button" onClick={() => { void copyNumber(frozen.batchId).then(() => setNotice('已复制批次编号。'), () => setNotice('复制未完成，请手动选择编号复制。')); }}>复制批次编号</button></p>
      {unknown ? <><p>原批次确实未创建时，重试会按这份清单开始并可能产生费用；已经受理时只返回原批，不重复检测或修改主站。</p><p>关闭窗口会丢弃本次未保存的清单，不自动重发；请保留原批次编号。</p><div className="actions hub-wrap"><button type="button" className="btn-secondary" disabled={busy} onClick={() => void submit(true)}>查询本批受理结果</button><button type="button" className="btn-primary" disabled={busy} onClick={() => void submit()}>用原编号重试受理</button></div></> : <><p>确认以上清单后开始；收起或关闭窗口不会停止已受理的任务。</p><div className="actions"><button type="button" className="btn-primary" disabled={busy} onClick={() => void submit()}>开始本批检测</button><button type="button" className="btn-secondary" disabled={busy} onClick={() => replaceFrozen(null)}>返回修改清单</button></div></>}
    </section>}
    {notice && <p role="status" className={unknown ? 'hub-error' : 'hub-note'}>{notice}</p>}
  </section>;
}
