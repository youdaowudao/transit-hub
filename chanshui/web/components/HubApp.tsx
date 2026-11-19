"use client";
import { useCallback, useEffect, useRef, useState } from 'react';
import Select from './Select';
import HubSettings from './HubSettings';
import ReportShare from './ReportShare';
import ThemeMenu from './ThemeMenu';
import KnowledgeDates from './KnowledgeDates';
import GitHubLink from './GitHubLink';
import UpstreamApiDocs from './UpstreamApiDocs';
import AuditHero from './AuditHero';
import ModelPicker from './ModelPicker';
import Hint from './Hint';
import SingleAskPanel, {type SingleAskPayload} from './SingleAskPanel';
import type {Metrics, BankStats} from '../lib/api';
import { readableReportError } from './ProbeEvidence';
import HubReportContent from './HubReportContent';
import Icon from './Icon';
import { canRetryHubItem, isVerifiedHubReport, newItemRead, readReportItems, reportError, reportVersion, object, type ItemReadState } from '../lib/hub-report-view';
import { createHubBridge, HubRequestError, type HubBoot } from '../lib/hub-bridge';
import { catalogAccount, catalogGroup, catalogModels, checkingContext, emptyDraft, missingSelection, pendingWarning, readSections, sectionsStorageKey, selectDraft, storageWarning, verifiedContext, type DraftAccount } from '../lib/hub-draft';
import { HUB_KINDS, isHubTerminal, newHubID, reportPollDecision, reportStatus, sidebarDayLabel } from '../lib/hub-policy';
import type { HubCall, HubCatalog, HubContext, HubItem, HubPage, HubReport, HubRuntime, HubSettings as Settings, HubStartApproval, HubTarget, HubTargetInput } from '../lib/hub-types';

type View = 'new' | 'report' | 'settings' | 'archive' | 'docs';
type Submission = 'report' | 'single';
type Confirmation = 'lookup' | 'replay';
type PendingStart = { reportId: string; payload: Record<string, unknown>; accepted?: boolean };
type StartAction = { scope: HubBoot; target?: DraftAccount };
const problem = (error: unknown) => error instanceof HubRequestError ? readableReportError(error.details) || error.message : error instanceof Error ? error.message : '读取检测结果时中断，原因尚未确认；请重试读取，不要重复新建检测。';
const contextFailure = (reason: string) => `所选账号或协议未通过核对。${reason} 请到分组健康核对配置后再试；本次没有发送问题。`;
const acceptedReport = (result: HubReport, id: string, scope: HubBoot) => {
  const value = object(result), workspace = value.workspaceId ?? object(object(value.request).expectedContext).workspaceId;
  // Older reports do not include a workspace field. The bridge request is scoped to the authenticated workspace.
  if (value.reportId !== id || (workspace !== undefined && workspace !== scope.workspaceId)) {
    throw new Error('返回的报告编号或工作区不一致，请按原报告编号重新查询。');
  }
  return result;
};
const displayTime = (value: number, full = false) => new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Singapore', ...(full ? { year: 'numeric', month: '2-digit', day: '2-digit' } as const : {}), hour: '2-digit', minute: '2-digit', hour12: false }).format(value);
function CheckedMemory({ report }: { report?: HubReport | null }) {
  return report?.taskMemoryMaxBytes ? <p className="muted">本次启动已核验：任务最多 {(report.taskMemoryMaxBytes / 1048576).toFixed(0)}MiB；整个检测子项目最多 {((report.sliceMemoryMaxBytes ?? 500000000) / 1000000).toFixed(0)}MB；{report.swapMaxBytes === 0 ? '交换内存已禁用' : '交换内存上限待核查'}。{report.memoryPeakBytes ? `已记录峰值 ${(report.memoryPeakBytes / 1048576).toFixed(1)}MiB。` : ''}</p> : null;
}


export { default as HubItemCard } from './HubQuestionDetails';

export default function HubApp({ initialView = 'new' }: { initialView?: View }) {
  const bridge = useRef<ReturnType<typeof createHubBridge> | null>(null);
  const [boot, setBoot] = useState<HubBoot | null>(null), [visible, setVisible] = useState(true), [view, setView] = useState<View>(initialView);
  const scopeRef = useRef<HubBoot | null>(null);
  const [bankStats, setBankStats] = useState<BankStats | null>(null);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [catalog, setCatalog] = useState<HubCatalog | null>(null), catalogRef = useRef<HubCatalog | null>(null);
  const [draft, setDraft] = useState(emptyDraft), draftRef = useRef(emptyDraft);
  const awaitingPrefill = useRef<HubTargetInput | null>(null);
  const [runtime, setRuntime] = useState<HubRuntime | null>(null), [targets, setTargets] = useState<HubTarget[]>([]);
  const [contextError, setContextError] = useState(''), [error, setError] = useState('');
  const [sections, setSections] = useState<string[]>(HUB_KINDS.map(k => k.value)), [settings, setSettings] = useState<Settings | null>(null);
  const sectionsRef = useRef(sections);
  const [sectionWarning, setSectionWarning] = useState('');
  const [history, setHistory] = useState<HubPage<HubReport>>({ items: [] }), [historyError, setHistoryError] = useState('');
  const [report, setReport] = useState<HubReport | null>(null), [active, setActive] = useState<HubReport | null>(null), [reportMessage, setReportMessage] = useState('');
  const [stopRequested, setStopRequested] = useState('');
  const [retryBusy, setRetryBusy] = useState(false), [retryPending, setRetryPending] = useState<{ reportId: string; retryId: string; expectedRetryId: string; itemIds: string[] } | null>(null);
  const [items, setItems] = useState<HubItem[]>([]), [itemBusy, setItemBusy] = useState(false), [itemsComplete, setItemsComplete] = useState(false), [itemError, setItemError] = useState(''), [pollEpoch, setPollEpoch] = useState(0);
  const itemReader = useRef<{ id: string; request: number; state: ItemReadState; promise?: Promise<void>; version: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [actions, setActions] = useState<Partial<Record<Submission, StartAction>>>({}), actionsRef = useRef(actions);
  const [pending, setPending] = useState<Partial<Record<Submission, PendingStart>>>({}), pendingRef = useRef(pending);
  const [archive, setArchive] = useState<HubPage<HubReport>>({ items: [] }), [from, setFrom] = useState(''), [to, setTo] = useState(''), [archiveTarget, setArchiveTarget] = useState(''), [reportLookup, setReportLookup] = useState('');
  const dirty = useRef(false);
  const onDirtyChange = useCallback((value: boolean) => { dirty.current = value; bridge.current?.setDirty(value); }, []);
  const reportRequest = useRef(0);
  const lastReport = useRef<HubReport | null>(null), latestView = useRef(view), visibleRef = useRef(visible), alive = useRef(true), terminalSince = useRef<number | null>(null);
  const call: HubCall = useCallback(<T,>(op: string, params: Record<string, unknown> = {}, approveStart?: HubStartApproval): Promise<T> => bridge.current ? bridge.current.call<T>(op, params, approveStart) : Promise.reject(new HubRequestError('请从调度站的模型检测按钮打开此页面。')), []);
  const storeDraft = (value: typeof emptyDraft) => { draftRef.current = value; setDraft(value); };
  const storePending = (kind: Submission, value?: PendingStart) => { pendingRef.current = { ...pendingRef.current, [kind]: value }; setPending(pendingRef.current); };
  const storeAction = useCallback((kind: Submission, value?: StartAction) => { actionsRef.current = { ...actionsRef.current, [kind]: value }; setActions(actionsRef.current); }, []);
  const discardChecks = useCallback((keep?: (target: DraftAccount) => boolean) => {
    let discarded = false;
    for (const kind of ['report', 'single'] as const) {
      const target = actionsRef.current[kind]?.target;
      if (target && !keep?.(target)) { storeAction(kind); discarded = true; }
    }
    return discarded;
  }, [storeAction]);
  const changeView = useCallback((next: View) => {
    if (next !== 'new') discardChecks();
    latestView.current = next; setView(next);
  }, [discardChecks]);
  const chooseDraft = (next: HubTargetInput, prefill = false) => {
    const keep = (target: DraftAccount) => target.groupId === next.groupId && target.targetId === next.targetId;
    const discarded = discardChecks(keep);
    setContextError(discarded ? contextFailure('核对期间更换了分组或账号。') : '');
    const requested = Boolean(next.targetId || next.groupId);
    awaitingPrefill.current = null;
    if (prefill && requested && (!next.groupId || !next.targetId)) {
      storeDraft({ ...emptyDraft, selectionError: missingSelection });
      return;
    }
    if (requested && !catalogRef.current) awaitingPrefill.current = next;
    storeDraft(awaitingPrefill.current ? emptyDraft : selectDraft(catalogRef.current, next.groupId, next.targetId));
  };
  const applyCatalog = (value: HubCatalog | null) => {
    if (value && value.workspaceId !== scopeRef.current?.workspaceId) return;
    const next = value;
    catalogRef.current = next; setCatalog(next);
    if (next && awaitingPrefill.current) { chooseDraft(awaitingPrefill.current); return; }
    const previous = draftRef.current;
    const group = catalogGroup(next, previous.groupId), account = catalogAccount(next, previous.groupId, previous.targetId);
    const discarded = discardChecks(target => Boolean(account) && target.groupId === previous.groupId && target.targetId === previous.targetId && account?.protocol === target.protocol);
    if (discarded) setContextError(contextFailure('核对期间，所选分组、账号或协议已发生变化。'));
    if (previous.groupId && !group) storeDraft({ ...emptyDraft, selectionError: missingSelection });
    else if (previous.targetId && !account) storeDraft({ ...emptyDraft, groupId: previous.groupId, selectionError: missingSelection });
  };
  const toggleSection = (id: string, checked: boolean) => {
    const workspace = scopeRef.current?.workspaceId;
    if (!workspace) return;
    const next = checked ? Array.from(new Set([...sectionsRef.current, id])) : sectionsRef.current.filter(value => value !== id);
    sectionsRef.current = next; setSections(next);
    try { localStorage.setItem(sectionsStorageKey(workspace), JSON.stringify(next)); setSectionWarning(''); }
    catch { setSectionWarning(storageWarning); }
  };
  const loadRuntime = useCallback(async () => {
    const scope = scopeRef.current, result = await call<HubRuntime>('runtime');
    if (scopeRef.current === scope && alive.current) { setRuntime(result); setActive(result.active ?? null); }
    return result;
  }, [call]);
  const applyReportState = useCallback((value: HubReport, selected: boolean) => {
    if (selected) { setReport(value); lastReport.current = value; }
    const decision = reportPollDecision(value, terminalSince.current, Date.now());
    if (isHubTerminal(value.status) || value.processExited) setStopRequested(current => current === value.reportId ? '' : current);
    else if (value.stopRequested) setStopRequested(value.reportId);
    terminalSince.current = decision.terminalSince;
    setReportMessage(decision.message);
    setActive(current => value.status === 'queued' ? current : isHubTerminal(value.status) && value.processExited ? (current?.reportId === value.reportId ? null : current) : value);
    return decision;
  }, []);
  const loadHistory = useCallback(async (cursor = '') => {
    const scope = scopeRef.current, current = () => scopeRef.current === scope && alive.current;
    try {
      const page = await call<HubPage<HubReport>>('reports', { scope: 'sidebar', limit: 20, cursor });
      if (!current()) return;
      setHistory(old => ({ ...page, items: cursor ? [...old.items, ...page.items] : page.items })); setHistoryError('');
    } catch (e) { if (current()) setHistoryError(problem(e)); }
  }, [call]);
  const loadItems = useCallback(async (id: string, resume = false, force = false) => {
    const request = reportRequest.current;
    const current = () => alive.current && visibleRef.current && lastReport.current?.reportId === id && reportRequest.current === request;
    if (!current()) return;
    let reader = itemReader.current;
    if (!reader || reader.id !== id || reader.request !== request) {
      reader = { id, request, state: newItemRead(), version: '' }; itemReader.current = reader;
    }
    const version = reportVersion(lastReport.current!);
    if (reader.promise) { await reader.promise; return; }
    if (!force && !resume && reader.version === version) return;
    if (!resume) reader.state = newItemRead();
    const selectedReader = reader;
    // An interrupted hidden read must remain eligible when this view returns.
    selectedReader.version = '';
    setItemBusy(true); setItemError('');
    const run = async () => {
      for (let round = 0; round < 2 && current(); round++) {
        const startedVersion = reportVersion(lastReport.current!);
        try {
          await readReportItems(call, id, selectedReader.state, current, rows => {
            if (!current()) return;
            setItems(previous => { const merged = new Map(previous.map(item => [item.itemId, item])); rows.forEach(item => merged.set(item.itemId, item)); return Array.from(merged.values()); });
          });
          if (!current()) return;
          selectedReader.version = startedVersion; setItemsComplete(selectedReader.state.complete);
          if (selectedReader.state.complete && startedVersion !== reportVersion(lastReport.current!) && round === 0) { selectedReader.state = newItemRead(); continue; }
          break;
        } catch (e) {
          if (current()) {
            selectedReader.version = startedVersion;
            setItemError(`读取第${selectedReader.state.batches + 1}批回答时中断：${problem(e)}。已读取${selectedReader.state.ids.size}项仍可查看；点击继续读取回答，不会再次调用模型。`);
          }
          break;
        }
      }
    };
    selectedReader.promise = run();
    try { await selectedReader.promise; } finally { selectedReader.promise = undefined; if (current()) setItemBusy(false); }
  }, [call]);

  useEffect(() => {
    if (!boot) return;
    let live = true;
    const current = () => live && scopeRef.current === boot;
    void call<Settings>('settings').then(value => { if (current()) setSettings(value); }, e => { if (current()) setError(problem(e)); });
    void call<BankStats>('reference-info').then(value => { if (current()) setBankStats(value); }, e => { if (current()) setError(problem(e)); });
    return () => { live = false; };
  }, [boot, call]);

  useEffect(() => {
    if (!boot || view !== 'archive' || !visible) return;
    let live = true;
    void call<{ items: HubTarget[] }>('targets').then(value => {
      if (live && scopeRef.current === boot) setTargets(value.items ?? []);
    }, e => { if (live && scopeRef.current === boot) setError(problem(e)); });
    return () => { live = false; };
  }, [boot, view, visible, call]);

  const openReport = useCallback(async (id: string, known?: HubReport) => {
    const same = lastReport.current?.reportId === id;
    const request = ++reportRequest.current; itemReader.current = null; setItemBusy(false); setItemError(''); setReportLookup(id);
    if (!same) { setRetryPending(null); setReport(null); lastReport.current = null; setItemsComplete(false); setItems([]); setStopRequested(''); terminalSince.current = null; }
    setError(''); if (!same) setReportMessage(''); changeView('report');
    try { const result = known ?? await call<HubReport>('report', { reportId: id }); if (request !== reportRequest.current) return; applyReportState(result, true); await loadItems(id); }
    catch (e) { if (request === reportRequest.current) { setError(`读取最新报告未完成：${problem(e)}${same ? '。已显示的回答仍可查看，请点击核查运行状态；不会重新检测。' : `。请按报告编号 ${id} 重试读取，不要重复新建。`}`); } }
    finally { if (same && request === reportRequest.current) setPollEpoch(n => n + 1); }
  }, [call, loadItems, applyReportState, changeView]);

  useEffect(() => {
    alive.current = true;
    const receiveVisibility = (next: boolean) => {
      visibleRef.current = next; setVisible(next);
      if (!next) discardChecks();
    };
    const instance = createHubBridge(value => {
      const previous = scopeRef.current;
      scopeRef.current = value; setBoot(value);
      receiveVisibility(value.visible);
      if (previous?.workspaceId !== value.workspaceId) {
        const saved = readSections(value.workspaceId);
        sectionsRef.current = saved.sections; setSections(saved.sections); setSectionWarning(saved.warning);
      }
      reportRequest.current++; lastReport.current = null; itemReader.current = null; terminalSince.current = null;
      setReport(null); setItems([]); setItemBusy(false); setItemsComplete(false); setItemError(''); setReportMessage('');
      setRuntime(null); setActive(null); setArchive({ items: [] }); setTargets([]); setArchiveTarget(''); setReportLookup('');
      setSettings(null); setBankStats(null); setHistory({ items: [] }); setHistoryError('');
      setRetryBusy(false); setRetryPending(null); setStopRequested(''); setBusy(false);
      actionsRef.current = {}; setActions(actionsRef.current); pendingRef.current = {}; setPending(pendingRef.current);
      if (previous) changeView(initialView);
      setError('');
      awaitingPrefill.current = null; storeDraft(emptyDraft);
      applyCatalog(value.catalog?.workspaceId === value.workspaceId ? value.catalog : null);
      chooseDraft(value.target ?? {}, true);
    }, value => {
      if (dirty.current && !confirm('检测设置有未保存的修改，确定切换到新账号并放弃吗？')) return;
      if (value.reportId && /^[0-9a-f]{32}$/.test(value.reportId)) { void openReport(value.reportId); return; }
      chooseDraft(value, true); changeView('new'); setError('');
    }, receiveVisibility, warning => setError(readableReportError(warning)), applyCatalog, nextTarget => {
      if (!nextTarget || nextTarget.reportId) discardChecks();
      else discardChecks(target => target.groupId === nextTarget.groupId && target.targetId === nextTarget.targetId);
    });
    bridge.current = instance;
    const standalone = setTimeout(() => {
      if (!scopeRef.current) setError(old => old || '请从调度站中的“模型检测”按钮打开此页面。页面本身不会启动检测。');
    }, 10000);
    return () => { alive.current = false; reportRequest.current++; clearTimeout(standalone); instance.dispose(); bridge.current = null; scopeRef.current = null; };
  }, [changeView, discardChecks, initialView, openReport]);

  useEffect(()=>{if(boot?.target.reportId && /^[0-9a-f]{32}$/.test(boot.target.reportId)) void openReport(boot.target.reportId);},[boot,openReport]);
  useEffect(() => {
    if (!boot || !visible) return;
    void loadHistory();
    void loadRuntime().catch(e => { if (scopeRef.current === boot) setError(problem(e)); });
  }, [visible, boot, loadHistory, loadRuntime]);

  const pollId = view === 'report' ? report?.reportId : active?.reportId;
  useEffect(() => {
    if (!boot || !visible || !pollId) return;
    let live = true, timer: ReturnType<typeof setTimeout> | undefined, failures = 0;
    terminalSince.current = null;
    const poll = async () => {
      const request = reportRequest.current;
      try {
        const value = await call<HubReport>('report', { reportId: pollId }); if (!live || request !== reportRequest.current) return;
        failures = 0;
        const selected = lastReport.current?.reportId === pollId;
        const decision = applyReportState(value, selected);
        if (selected && latestView.current === 'report') await loadItems(pollId);
        if (!live || request !== reportRequest.current) return;
        if (decision.poll) timer = setTimeout(() => { void poll(); }, 2000);
        else { if (isHubTerminal(value.status) && value.processExited) void loadHistory(); }
      } catch (e) {
        if (!live || request !== reportRequest.current) return;
        if (++failures < 3) timer = setTimeout(() => { void poll(); }, 2000);
        else { setError(problem(e)); setReportMessage('状态读取失败，上次显示不代表仍在执行。连续三次无法读取运行状态，已停止自动读取；请点击核查运行状态，不会自动重复检测。'); }
      }
    };
    void poll();
    return () => { live = false; if (timer) clearTimeout(timer); };
  }, [boot, visible, pollId, pollEpoch, call, loadItems, loadHistory, applyReportState]);

  const lockStart = (kind: Submission, target?: DraftAccount) => {
    const scope = scopeRef.current;
    if (!scope || !visibleRef.current || latestView.current !== 'new' || actionsRef.current[kind]) return;
    const action: StartAction = { scope, target };
    storeAction(kind, action);
    return action;
  };
  const currentAction = (kind: Submission, action: StartAction) =>
    alive.current && scopeRef.current === action.scope && actionsRef.current[kind] === action;
  const finishAction = (kind: Submission, action: StartAction) => { if (currentAction(kind, action)) storeAction(kind); };
  const currentPending = (kind: Submission, action: StartAction, request: PendingStart) =>
    currentAction(kind, action) && pendingRef.current[kind] === request;

  const waitForSingle = async (action: StartAction, request: PendingStart, signal?: AbortSignal, known?: HubReport): Promise<Metrics | null> => {
    const current = () => currentPending('single', action, request) && !signal?.aborted;
    let knownReport = known;
    while (current()) {
      if (!visibleRef.current) { await new Promise(resolve => setTimeout(resolve, 800)); continue; }
      const row = knownReport ?? acceptedReport(await call<HubReport>('report', { reportId: request.reportId }), request.reportId, action.scope);
      knownReport = undefined;
      if (!current()) return null;
      applyReportState(row, false);
      if (isHubTerminal(row.status)) {
        const page = await call<HubPage<HubItem>>('items', { reportId: request.reportId, limit: 5 });
        if (!current()) return null;
        const item = page.items?.[0], metrics = object(object(item?.result?.evidence).single_ask);
        if (item?.result?.error || metrics.Err || ['cancelled', 'interrupted'].includes(row.status) || !Object.keys(metrics).length) {
          throw new Error(`${item?.result?.error || metrics.Err || '问答已停止或未完整返回'}。记录 ${request.reportId} 已保留，可以打开原问答记录补测失败项。`);
        }
        storePending('single'); void loadHistory();
        return metrics as Metrics;
      }
      await new Promise(resolve => setTimeout(resolve, 800));
    }
    return null;
  };

  const resolveSubmission = async (kind: Submission, action: StartAction, request: PendingStart, method: 'first' | Confirmation, signal?: AbortSignal, submittedDraft?: typeof emptyDraft): Promise<Metrics | null> => {
    const current = () => currentPending(kind, action, request);
    const approveStart: HubStartApproval | undefined = method === 'first' ? protocol => {
      if (!currentAction(kind, action) || !action.target || !visibleRef.current || latestView.current !== 'new' || signal?.aborted) return false;
      if (action.target.protocol !== protocol) {
        setContextError(contextFailure('分组健康名单中的检测协议已变化。'));
        finishAction(kind, action);
        return false;
      }
      action.target = undefined; storeAction(kind, action); storePending(kind, request);
      return true;
    } : undefined;
    try {
      const result = method === 'lookup'
        ? await call<HubReport>('report', { reportId: request.reportId })
        : await call<HubReport>('start', request.payload, approveStart);
      if (!current()) return null;
      const row = acceptedReport(result, request.reportId, action.scope);
      if (kind === 'report') {
        storePending(kind);
        const sameDraft = latestView.current === 'new' && visibleRef.current && draftRef.current === submittedDraft;
        if (method !== 'first' || sameDraft) void openReport(request.reportId, method === 'lookup' ? row : undefined);
        else { void loadHistory(); void loadRuntime().catch(() => undefined); }
        return null;
      }
      request.accepted = true; storePending(kind, request); void loadHistory();
      return await waitForSingle(action, request, signal, method === 'lookup' ? row : undefined);
    } catch (e) {
      if (method === 'first' && action.target && currentAction(kind, action)) {
        setContextError(contextFailure(problem(e)));
        return null;
      }
      if (!current()) return null;
      if (!request.accepted && method !== 'lookup' && e instanceof HubRequestError && e.details.operationResult === 'not_started') {
        if (e.details.reportId === request.reportId) {
          setError(`服务器确认检测没有开始。${problem(e)} 点击查询本次受理结果可查看原编号 ${request.reportId} 的记录。`);
          return null;
        }
        const firstRejected = method === 'first';
        const replayRejected = method === 'replay' && ['account', 'context', 'queue'].includes(String(e.details.step));
        if (!e.details.reportId && (firstRejected || replayRejected)) {
          storePending(kind);
          setError(`服务器拒绝了本次受理，检测没有开始。${problem(e)} 请检查提示原因后重新操作。`);
          return null;
        }
      }
      const description = request.accepted ? '读取原问答未完成' : method === 'lookup' ? '原编号查询未确认受理' : '本次受理尚未确认';
      setError(`${description}：${problem(e)} 原报告编号 ${request.reportId}。`);
      return null;
    }
  };

  const submitDraft = async (kind: Submission, payload: Record<string, unknown>, signal?: AbortSignal): Promise<Metrics | null> => {
    const currentDraft = draftRef.current;
    const account = catalogAccount(catalogRef.current, currentDraft.groupId, currentDraft.targetId);
    if (!account || !currentDraft.model.trim() || signal?.aborted) return null;
    const previous = pendingRef.current[kind];
    if (previous && !previous.accepted) return null;
    const selected = { groupId: currentDraft.groupId, targetId: account.targetId, protocol: account.protocol };
    const action = lockStart(kind, selected);
    if (!action) return null;
    const confirmedDraft = { groupId: selected.groupId, targetId: selected.targetId, model: currentDraft.model, ...payload };
    const abortBeforeStart = () => { if (action.target) finishAction(kind, action); };
    signal?.addEventListener('abort', abortBeforeStart);
    setContextError(''); setError('');
    try {
      let context: Record<string, string>;
      try {
        const value = await call<HubContext>('context', { targetId: selected.targetId, groupId: selected.groupId });
        if (!currentAction(kind, action) || signal?.aborted) return null;
        context = verifiedContext(value, action.scope.workspaceId, selected);
      } catch (e) {
        if (currentAction(kind, action) && !signal?.aborted) setContextError(contextFailure(problem(e)));
        return null;
      }
      const id = newHubID();
      const request: PendingStart = { reportId: id, payload: { ...confirmedDraft, reportId: id, expectedContext: context } };
      return await resolveSubmission(kind, action, request, 'first', signal, currentDraft);
    } finally {
      signal?.removeEventListener('abort', abortBeforeStart);
      finishAction(kind, action);
    }
  };

  const confirmSubmission = async (kind: Submission, method: Confirmation, signal?: AbortSignal) => {
    const request = pendingRef.current[kind];
    if (!request || (request.accepted && method === 'replay') || signal?.aborted) return null;
    const action = lockStart(kind);
    if (!action) return null;
    setError('');
    try { return await resolveSubmission(kind, action, request, method, signal); }
    finally { finishAction(kind, action); }
  };
  const start = () => {
    if (!sectionsRef.current.length || runtime?.available !== true || runtime.queueBlocked) return;
    return submitDraft('report', { selectedSectionIds: [...sectionsRef.current], selectedQuestionIds: [] });
  };
  const askOne = (input: SingleAskPayload, signal: AbortSignal) => {
    const previous = pendingRef.current.single, current = draftRef.current;
    if (previous?.accepted && previous.payload.targetId === current.targetId && previous.payload.groupId === current.groupId &&
      previous.payload.model === current.model && JSON.stringify(previous.payload.singleAsk) === JSON.stringify(input)) {
      return confirmSubmission('single', 'lookup', signal);
    }
    return submitDraft('single', { singleAsk: input }, signal);
  };

  const stop = async () => {
    if (!report) return;
    const id = report.reportId; setBusy(true); setStopRequested(id); setError('');
    try {
      const result = await call<HubReport>('cancel', { reportId: id, expectedRetryId: report.retry?.id ?? '' });
      if (lastReport.current?.reportId !== id) return;
      if (isHubTerminal(result.status) && result.accountName) applyReportState(result, true);
      await openReport(id);
    } catch (e) { if (lastReport.current?.reportId === id) { setStopRequested(''); setError(`停止结果尚未确认：${problem(e)}。已显示的回答仍可查看，请核查运行状态。`); } }
    finally { setBusy(false); }
  };
  const checkRuntime = async () => {
    const request = ++reportRequest.current, id = lastReport.current?.reportId;
    setError(''); setItemBusy(false);
    try {
      await loadRuntime();
      if (request !== reportRequest.current || !id) return;
      const value = await call<HubReport>('report', { reportId: id });
      if (request !== reportRequest.current) return;
      const decision = applyReportState(value, true);
      await loadItems(id, false, true);
      if (decision.poll) setPollEpoch(n => n + 1);
    } catch (e) { if (request === reportRequest.current) setError(problem(e)); }
  };
  const retryItems = async (ids: string[]) => {
    if (!report || retryBusy) return;
    const confirming = retryPending?.reportId === report.reportId;
    if (!confirming && (!itemsComplete || !isHubTerminal(report.status) || report.processExited !== true)) return;
    const id = report.reportId;
    const payload = retryPending?.reportId === id ? retryPending : { reportId: id, retryId: newHubID(), expectedRetryId: report.retry?.id ?? '', itemIds: ids };
    setRetryPending(payload); setRetryBusy(true); setError('');
    try {
      await call('retry', payload);
      if (!alive.current) return;
      setRetryPending(null);
      if (lastReport.current?.reportId === id && latestView.current === 'report') await openReport(id);
      await loadHistory();
    } catch (e) {
      if (alive.current && lastReport.current?.reportId === id) {
        const rejected = e instanceof HubRequestError && [400, 401, 403, 404, 409, 422, 429].includes(e.status);
        if (rejected) setRetryPending(null);
        setError(rejected ? `补测未受理：${problem(e)}。成功结果保留，请核查并重新读取报告后再操作。` : `补测受理尚未确认：${problem(e)}。请点击“用原操作确认补测受理”，不会创建新报告或重复已受理的补测。`);
      }
    } finally { if (alive.current) setRetryBusy(false); }
  };
  const findArchive = async (cursor = '') => {
    const scope = scopeRef.current;
    setError('');
    try {
      const page = await call<HubPage<HubReport>>('reports', { scope: 'archive', from, to, targetId: archiveTarget, cursor, limit: 20 });
      if (scopeRef.current === scope) setArchive(page);
    } catch (e) { if (scopeRef.current === scope) setError(problem(e)); }
  };
  const navigate = (next: View) => { if (view === 'settings' && next !== 'settings' && dirty.current && !confirm('检测设置有未保存的修改，确定离开并放弃吗？')) return; changeView(next); setError(''); if (next === 'archive') void findArchive(); };

  const groups = catalog?.groups ?? [];
  const group = catalogGroup(catalog, draft.groupId);
  const account = catalogAccount(catalog, draft.groupId, draft.targetId);
  const groupAccounts = group?.accountsError ? [] : group?.accounts ?? [];
  const models = catalogModels(account);
  const isChecking = Boolean(actions.report?.target || actions.single?.target);
  const hasDraft = Boolean(account && draft.model.trim());
  return <div className="hub-app">
    <aside className={'sidebar' + (mobileOpen ? ' mobile-open' : '')}>
      <button type="button" className="sidebar-logo hub-nav-button" onClick={() => navigate('new')}><span className="logo-symbol">?</span><span>掺水<span className="logo-punct">？</span></span></button>
      <div className="sidebar-sub">模型检测台</div>
      <button type="button" className={'nav-home hub-nav-button' + (view === 'new' ? ' active' : '')} onClick={() => navigate('new')}><Icon name="scan" />新建检测</button>
      <div className="hub-extra-nav">
        <button type="button" className={'nav-home hub-nav-button' + (view === 'settings' ? ' active' : '')} onClick={() => navigate('settings')}><Icon name="layers" />设置与全部题目</button>
        <button type="button" className={'nav-home hub-nav-button' + (view === 'archive' ? ' active' : '')} onClick={() => navigate('archive')}><Icon name="clock" />查找更早记录</button>
      </div>
      <button type="button" className="mobile-history" aria-expanded={mobileOpen} onClick={() => setMobileOpen(!mobileOpen)}><Icon name="clock" /><span>{mobileOpen ? '收起历史' : '历史记录'}</span></button>
      <div className="sidebar-title"><span>本站检测记录</span><button type="button" className="hub-nav-button" onClick={() => void loadHistory()}>刷新</button></div>
      <div className="sidebar-list">
        {(['今天', '昨天'] as const).map(day => {
          const rows = history.items.filter(row => sidebarDayLabel(row.createdAtMs) === day);
          const groups = new Map<string, HubReport[]>();
          rows.forEach(row => { const key = row.groupId ? 'id:' + row.groupId : 'name:' + (row.groupName || '未记录分组'); groups.set(key, [...(groups.get(key) ?? []), row]); });
          return rows.length > 0 && <section key={day}><div className="sidebar-title">{day}</div>{Array.from(groups).map(([key, reports]) => <section key={key}><div className="sidebar-title">{reports[0].groupName || '未记录分组'}{reports[0].groupId && rows.some(r => r.groupName === reports[0].groupName && r.groupId !== reports[0].groupId) ? ` #${reports[0].groupId}` : ''}</div>{reports.map(row => {
            const saved = object(row.summary), totalScore = saved.totalScore ?? object(saved.total).score, score = totalScore ?? saved.iqScore;
            return <button type="button" key={row.reportId} data-report-id={row.reportId} className={'hist-item hub-nav-button' + (report?.reportId === row.reportId && view === 'report' ? ' active' : '')} onClick={() => void openReport(row.reportId)}><div className="history-model">{row.accountName}{row.accountId ? ` #${row.accountId}` : ''}</div><div className="history-model">{row.model}</div><div className="history-meta"><b className="hist-badge" title={typeof totalScore === 'number' ? '总分' : '推理分'}>{typeof score === 'number' ? `${isVerifiedHubReport(row) && typeof totalScore !== 'number' ? '推理 ' : ''}${score} 分` : reportStatus(row.status)}</b><time className="history-time">{displayTime(row.createdAtMs)}</time></div>{typeof score === 'number' && row.status !== 'done' && <div className="history-model">{reportStatus(row.status)}</div>}</button>;
          })}</section>)}</section>;
        })}
        {history.items.length === 0 && !historyError && <p className="sidebar-empty">今天和昨天暂无记录。</p>}
        {history.nextCursor && <button type="button" className="nav-home hub-nav-button" onClick={() => void loadHistory(history.nextCursor)}>显示更多最近记录</button>}
        {historyError && <p role="alert" className="sidebar-empty">{historyError}</p>}
      </div><div className="sidebar-bottom"><p className="sidebar-retention">只显示当前工作区今天和昨天的记录；更早报告仍可查找。</p></div>
    </aside>
    <main className="main hub-main" id="main-content"><div className="top-tools"><KnowledgeDates /><button type="button" className="theme-trigger docs-entry" onClick={()=>navigate('docs')}><Icon name="layers" /><span className="tool-label">接口文档</span></button><ThemeMenu /><GitHubLink /></div><div className={"page-shell" + (view === 'report' ? ' history-page' : '')}><header hidden={view === 'report'} className="hub-page-heading"><p className="muted">此页只做检测和保存结果，不会修改主站账号或调度设置。</p><p className="muted">收起或关闭页面不会停止已开始的检测；需要中止请点击停止检测。</p></header>
      {!boot && <section className="hero-card hub-card"><h1>正在连接调度站…</h1><p>请从调度站中的模型检测入口打开此页。静态页面不会启动检测程序。</p></section>}
      {runtime && (!runtime.available || runtime.queueBlocked) && <section className="hero-card hub-card"><p className="hub-error" role="alert">{runtime.reason || (runtime.busy ? '当前已有检测正在执行，请等待后点击核查运行状态；仍可查看自己的历史和设置。' : '当前环境未启用硬内存限制，不能开始检测。请按部署说明配置后重试。')}</p><button type="button" className="btn-secondary" onClick={() => void checkRuntime()}>核查运行状态</button></section>}
      {runtime?.available && runtime.busy && <div className="hub-note" role="status">当前已有检测正在执行，新报告可加入等待队列，不会与它同时执行。<button type="button" className="hub-text-button" onClick={() => void checkRuntime()}>核查运行状态</button></div>}
      {view !== 'report' && <CheckedMemory report={runtime?.active} />}
      {active && (view !== 'report' || report?.reportId !== active.reportId) && (!isHubTerminal(active.status) || !active.processExited) && <div className="hub-note" role="status">当前任务：{active.accountName} · {active.model} · {reportStatus(active.status)}。切换账号不会改变此任务。<button type="button" className="hub-text-button" onClick={() => void openReport(active.reportId)}>查看该任务</button></div>}
      {error && <p role="alert" className="hub-error hub-alert">{error}</p>}
      {view === 'settings' && boot && <HubSettings call={call} onDirtyChange={onDirtyChange} />}
      {view === 'new' && boot && <div className="opening"><AuditHero /><section className="hero-card connection-card hub-card">
        <div className="section-heading"><h1>新建账号检测</h1><span className="muted">只预填，不自动检测</span></div>
        <p className="muted">分组和账号来自本次已加载的分组健康名单。切换选项不会查询上游或修改主站。</p>
        <div className="connection-grid">
          <div className="field"><label>分组<Select value={draft.groupId} onChange={e => chooseDraft({ groupId: e.target.value })}>
            <option value="">请选择分组</option>
            {groups.map(group => <option key={group.id} value={group.id}>{`${group.name}${groups.some(other => other.id !== group.id && other.name === group.name) ? ` #${group.id}` : ''}`}</option>)}
          </Select></label></div>
          <div className="field"><label>账号<Select value={draft.targetId} disabled={!group || groupAccounts.length === 0} onChange={e => chooseDraft({ groupId: draft.groupId, targetId: e.target.value })}>
            <option value="">{group ? '请选择账号' : '请先选择分组'}</option>
            {groupAccounts.map(account => <option key={account.targetId} value={account.targetId}>{`${account.accountName}${account.accountId ? ` #${account.accountId}` : ''}`}</option>)}
          </Select></label></div>
        </div>
        {!catalog && <p className="muted">请先打开「分组健康」加载当前工作区的名单，再回到这里。此时没有开始检测。</p>}
        {catalog && groups.length === 0 && <p className="muted">分组健康当前没有显示可选分组。请检查左侧筛选或隐藏设置；此时没有开始检测。</p>}
        {draft.selectionError && <p role="status" className="muted">{draft.selectionError}</p>}
        {group?.accountsError && <p className="hub-error" role="alert">分组健康没有读到这个分组的账号。请选择其他分组，或回到分组健康查看原因并重新加载；本次没有发送问题。</p>}
        {group && !group.accountsError && groupAccounts.length === 0 && <p className="muted">此分组没有可选账号。请选择其他分组；此时没有开始检测。</p>}
        {isChecking && <p role="status">{checkingContext}</p>}
        {contextError && <p role="alert" className="hub-error">{contextError}</p>}
        {account && <>
          <p className="hub-target-line"><strong>{account.accountName}</strong> · {group?.name}{account.protocol && ` · ${account.protocol}`}</p>
          <div className="connection-grid">
            <div className="field"><label>API 密钥<input readOnly value="使用此账号已配置的密钥" /></label><p className="muted">页面不显示或保存密钥；执行时由服务器读取。</p></div>
            <div className="field">
              <label htmlFor="model">目标模型</label>
              <ModelPicker id="model" value={draft.model} options={models} onChange={value => storeDraft({ ...draftRef.current, model: value })} placeholder="可选择或输入模型名称" describedBy="model-help" />
              <small id="model-help">可选择或输入自定义名称，请求时原样发送；没有对应基线时不代表已验证型号。</small>
              {models.length === 0 && <p className="muted">分组健康暂无该账号的模型名单。可以手动输入模型名称，发送时会使用你填写的名称。</p>}
              {draft.modelNotice && <p className="muted">{draft.modelNotice}</p>}
            </div>
          </div>
        </>}
        <details className="fold hub-section-picker" open>
          <summary>本次检测内容</summary>
          <p className="muted">本浏览器会记住这个工作区上次勾选的项目。再次新建时可以继续调整，勾选不会修改主站。</p>
          <p className="muted">选择分类后使用该分类中全部已启用题目；请在设置里增删或禁用单题。工具、缓存与指纹可能发送多次请求。缓存默认勾选，执行原版六轮检测，会增加请求；可取消。在设置中手动禁用或删除的题目仍跳过。</p>
          <p className="muted">模型自报检查位于推理题中，默认5分，只对GPT-6系列执行；可在设置里禁用或删除，不修改主站。</p>
          <div className="hub-sections">{HUB_KINDS.map(section => <label key={section.value} className="hub-checkbox"><input type="checkbox" checked={sections.includes(section.value)} onChange={e => toggleSection(section.value, e.target.checked)} />{section.label}</label>)}</div>
          {sectionWarning && <p role="status" className="muted">{sectionWarning}</p>}
        </details>
        <p className="hub-note">点击开始后会向所选上游发送请求，可能产生费用；请先核对账号、模型和题目。检测任务默认{settings?.memoryLimitMiB ?? 256}MiB，整个检测子项目最多约0.5GB；报告保存后程序退出。</p>
        <div className="actions hub-wrap">
          <button type="button" className="btn-primary" disabled={Boolean(actions.report || pending.report) || !hasDraft || !sections.length || runtime?.available !== true || runtime?.queueBlocked === true} onClick={() => void start()}>{actions.report?.target ? '正在核对账号…' : actions.report ? '正在确认受理…' : active || runtime?.busy || (runtime?.queuedCount ?? 0) > 0 ? '加入等待队列' : '开始检测'}</button>
          <button type="button" className="btn-secondary" onClick={() => navigate('settings')}>设置与全部题目</button>
        </div>
        {pending.report && <section className="hub-note" aria-label="普通检测受理">
          <p>{pendingWarning}</p><p>原报告编号 <strong>{pending.report.reportId}</strong></p>
          <div className="actions hub-wrap">
            <button type="button" className="btn-secondary" disabled={Boolean(actions.report)} onClick={() => void confirmSubmission('report', 'lookup')}>查询本次受理结果</button>
            <button type="button" className="btn-secondary" disabled={Boolean(actions.report)} onClick={() => void confirmSubmission('report', 'replay')}>用原编号重试受理</button>
          </div>
        </section>}
        <div className="bank-strip"><span><Icon name="layers"/><b>{bankStats?.fingerprint_count ?? '—'}</b> 个可测模型</span><Hint label="模型库说明" text="服务端在检测任务中检查模型基线更新。数字指纹仅对库内候选比较，行为参考不等于型号证明。"/></div>
      </section></div>}
      {view === 'new' && boot && <SingleAskPanel
        key={boot.generation}
        model={draft.model}
        canRun={hasDraft && !actions.single && (!pending.single || pending.single.accepted === true)}
        onAsk={askOne}
        pending={pending.single && {
          reportId: pending.single.reportId,
          accepted: pending.single.accepted,
          busy: Boolean(actions.single),
          confirm: (method, signal) => confirmSubmission('single', method, signal),
          open: () => void openReport(pending.single!.reportId),
        }}
      />}
      {view === 'report' && report && <>
        <section className="hero-card">
          <div className="section-heading"><div><span className="step">报告</span><h2>按检测 ID 查看</h2></div><span className="muted">本站检测报告</span></div>
          <form className="report-lookup" onSubmit={e => { e.preventDefault(); if (reportLookup.trim()) void openReport(reportLookup.trim()); }}><label className="sr-only" htmlFor="hub-report-id">检测 ID</label><input id="hub-report-id" value={reportLookup} placeholder="粘贴检测 ID" onChange={e => setReportLookup(e.target.value)} /><button type="submit" className="btn-primary">查看报告</button></form>
          <h1 className="hero-title">{report.model}</h1>
          <p className="report-meta">{new Date(report.createdAtMs).toLocaleString('zh-CN')} · {reportStatus(report.status)} · 账号：{report.accountName} · 分组：{report.groupName}</p>
          <ReportShare id={report.reportId} identityLabel="报告编号" reportHref={(()=>{if(typeof window==='undefined')return '';const url=new URL(window.parent.location.href);url.searchParams.set('detectorReport',report.reportId);return url.href;})()} actions={<>            <button type="button" className="btn-secondary" onClick={() => void checkRuntime()}>核查运行状态</button>
            {(!isHubTerminal(report.status) || !report.processExited) && <button type="button" className="btn-secondary" disabled={busy} onClick={() => void stop()}>{report.diagnostic ? '停止检测并重试保存状态' : stopRequested === report.reportId ? '正在停止…' : report.status === 'queued' ? '取消等待' : report.processExited ? '停止检测并重试保存状态' : '停止检测'}</button>}
            <button type="button" className="btn-secondary" disabled={retryBusy || (!retryPending && (!itemsComplete || !isHubTerminal(report.status) || report.processExited !== true || !items.some(canRetryHubItem)))} onClick={() => void retryItems(items.filter(canRetryHubItem).map(item => item.itemId))}>{retryBusy ? '正在确认补测…' : retryPending ? '用原操作确认补测受理' : `补测失败项（${items.filter(canRetryHubItem).length}）`}</button>
            <button type="button" className="btn-secondary" onClick={() => { chooseDraft({ targetId: report.targetId, groupId: report.groupId }, true); changeView('new'); }}>重新检测（新任务）</button>
</>} />
          <p className="muted">报告链接通过本站登录和当前工作区权限校验；复制链接不会公开报告。报告长期保存在磁盘，可在原报告补测失败项。</p>
          <p className="muted">补测只重新提问失败或未完成项，成功结果保留，结果更新到本报告；可能产生请求费用，不修改主站。{!itemsComplete ? '题目还未读完，请先等待读取完成。' : !isHubTerminal(report.status) ? '本轮正在运行或等待，结束后可再补测。' : report.processExited !== true ? '上一轮程序退出尚未确认，请先核查运行状态。' : !items.some(canRetryHubItem) ? '当前没有需要补测的失败项目。' : ''}</p>
          {stopRequested === report.reportId && <p role="status" className="muted">正在停止，已完成回答仍可查看。</p>}
          {report.status === 'queued' && typeof report.queuePosition === 'number' && <p role="status" className="muted">已加入等待，前面还有{Math.max(0, report.queuePosition)}份报告；轮到后自动开始，此时尚未发送检测题目。</p>}
          {report.status === 'cancelled' && <p className="muted">已停止，已保存回答保留；未执行题目不作答错。</p>}
          {reportMessage && <p role="status" className="muted">{reportMessage}</p>}
          {report.diagnostic && <p className="error-box" role="alert">{report.diagnostic}</p>}
          {reportError(report.error) && <p className="error-box" role="alert">{reportError(report.error)}</p>}
          <div className="hub-read-status" role={itemError ? 'alert' : 'status'}>{itemError || (itemBusy && !items.length ? '正在读取本次报告的题目与回答…' : `已读取${items.length}项回答${itemsComplete ? '；本次回答已读完。' : '；继续读取已保存的结果。'}`)}</div>
          {itemError && <button type="button" className="btn-secondary" disabled={itemBusy} title="只读取已保存的回答，不重新检测。" onClick={() => void loadItems(report.reportId, true, true)}>继续读取回答</button>}
          <details className="fold"><summary>运行与资源详情</summary><CheckedMemory report={report} /><p className="muted">结果只供人工检阅，不会自动降权、停用或恢复账号。配置不同的报告不宜只比较总分。</p></details>
        </section>
        <HubReportContent key={report.reportId} report={report} items={items} complete={itemsComplete} readFailed={Boolean(itemError) || itemBusy} call={call} onRetry={ids => void retryItems(ids)} retryDisabled={retryBusy || !itemsComplete || !isHubTerminal(report.status) || report.processExited !== true || Boolean(retryPending)} />
      </>}
      {view === 'docs' && <><p className="hub-note">以下保留原版独立检测服务的接口说明，本站未开放这些匿名公共接口；本站检测请使用当前页面的账号入口。查看说明不会发起检测或修改主站。</p><UpstreamApiDocs referenceOnly /></>}
      {view === 'archive' && boot && <section className="hero-card hub-card"><h1>查找更早记录</h1><p className="muted">历史报告长期保存在磁盘，按新加坡日期筛选；默认查询最近30天。不会重新调用模型。</p><div className="hub-form-grid"><div className="field"><label>开始日期<input type="date" value={from} onChange={e => setFrom(e.target.value)} /></label></div><div className="field"><label>结束日期<input type="date" value={to} onChange={e => setTo(e.target.value)} /></label></div><div className="field"><label>账号<Select value={archiveTarget} onChange={e => setArchiveTarget(e.target.value)}><option value="">全部账号</option>{targets.map(t => <option key={t.targetId} value={t.targetId}>{t.accountName}</option>)}</Select></label></div></div><button type="button" className="btn-primary" onClick={() => void findArchive()}>查找报告</button><div className="hub-search field"><label>按报告编号查找<input value={reportLookup} onChange={e => setReportLookup(e.target.value)} /></label><button type="button" className="btn-secondary" onClick={() => void openReport(reportLookup.trim())}>打开此报告</button></div>{archive.items.map(row => <div className="hub-question-row" key={row.reportId}><div><strong>{row.accountName} · {row.model}</strong><p className="muted">{displayTime(row.createdAtMs, true)} · {row.groupName} · {reportStatus(row.status)}</p></div><button type="button" className="btn-secondary" onClick={() => void openReport(row.reportId)}>查看报告</button></div>)}{archive.items.length === 0 && <p className="muted">此范围没有报告，可调整日期或直接输入报告编号。</p>}{archive.nextCursor && <button type="button" className="btn-secondary" onClick={() => void findArchive(archive.nextCursor)}>下一页报告</button>}</section>}
    </div></main>
  </div>;
}
