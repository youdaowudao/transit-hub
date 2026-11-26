"use client";
import { useCallback, useEffect, useRef, useState } from 'react';
import Select from './Select';
import HubSettings from './HubSettings';
import HubBatchCreate, { batchAccountKey, type BatchModelRead } from './HubBatchCreate';
import HubBatchView, { retryRound, stopAction, type HubStopIntent } from './HubBatchView';
import { batchStateLabel, emptyBatchFilter, phaseLabel } from '../lib/hub-batch-view';
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
import { displayTime } from '../lib/hub-crosscheck';
import { createHubBridge, HubRequestError, type HubBoot } from '../lib/hub-bridge';
import { catalogAccount, catalogGroup, equivalentCatalog, checkingContext, emptyDraft, missingSelection, modelKey, modelReadReason, pendingWarning, readSections, sectionsStorageKey, selectDraft, storageWarning, upstreamModelIds, verifiedContext, waitingForModels, type DraftAccount } from '../lib/hub-draft';
import { HUB_KINDS, isHubTerminal, newHubID, reportFinishConfirmed, reportPollDecision, reportStatus, selectHubDefaultModel, sidebarDayLabel } from '../lib/hub-policy';
import type { HubBatchDetail, HubBatchFilter, HubBatchPage, HubBatchRow, HubCall, HubCallOptions, HubCatalog, HubContext, HubItem, HubPage, HubReport, HubRuntime, HubSettings as Settings, HubStartApproval, HubTarget, HubTargetInput } from '../lib/hub-types';

type View = 'new' | 'report' | 'settings' | 'archive' | 'docs' | 'batch';
type StatusSelection = { op: 'runtime' | 'report' | 'batch' | 'batches' | 'reports'; params: Record<string, unknown> };
type BatchReturn = { id: string; reportId: string; filter: HubBatchFilter; scroll: number };
type StateLoop = { key: string; controller: AbortController; timer?: ReturnType<typeof setTimeout>; promise?: Promise<void> };
const runtimeSelection: StatusSelection = { op: 'runtime', params: {} };
type Submission = 'report' | 'single';
type Confirmation = 'lookup' | 'replay';
type PendingStart = { reportId: string; payload: Record<string, unknown>; accepted?: boolean };
type StartAction = { scope: HubBoot; target?: DraftAccount };
// 选中账号后向该账号上游读到的模型列表。key 由刷新计数、分组和账号组成（modelKey）。
type UpstreamModels = { key: string; status: 'loading' | 'done' | 'failed'; models: string[]; error: string };
const problem = (error: unknown) => error instanceof HubRequestError ? readableReportError(error.details) || error.message : error instanceof Error ? error.message : '读取检测结果时中断，原因尚未确认；请重试读取，不要重复新建检测。';
const contextFailure = (reason: string) => `所选账号或协议未通过核对。${reason} 请先点击窗口顶部「更新分组和账号」后重新选择；若仍失败，请按上面的具体原因检查账号或测试协议配置。本次没有发送问题。`;
const acceptedReport = (result: HubReport, id: string, scope: HubBoot) => {
  const value = object(result), workspace = value.workspaceId ?? object(object(value.request).expectedContext).workspaceId;
  // Older reports do not include a workspace field. The bridge request is scoped to the authenticated workspace.
  if (value.reportId !== id || (workspace !== undefined && workspace !== scope.workspaceId)) {
    throw new Error('返回的报告编号或工作区不一致，请按原报告编号重新查询。');
  }
  return result;
};
function CheckedMemory({ report }: { report?: HubReport | null }) {
  return report?.taskMemoryMaxBytes ? <p className="muted">本次启动已核验：任务最多 {(report.taskMemoryMaxBytes / 1048576).toFixed(0)}MiB；整个检测子项目最多 {((report.sliceMemoryMaxBytes ?? 500000000) / 1000000).toFixed(0)}MB；{report.swapMaxBytes === 0 ? '交换内存已禁用' : '交换内存上限待核查'}。{report.memoryPeakBytes ? `已记录峰值 ${(report.memoryPeakBytes / 1048576).toFixed(1)}MiB。` : ''}</p> : null;
}


export { default as HubItemCard } from './HubQuestionDetails';

export default function HubApp({ initialView = 'new' }: { initialView?: View }) {
  const bridge = useRef<ReturnType<typeof createHubBridge> | null>(null);
  const [boot, setBoot] = useState<HubBoot | null>(null), [visible, setVisible] = useState(true), [view, setView] = useState<View>(initialView);
  const scopeRef = useRef<HubBoot | null>(null);
  const [batchOpened, setBatchOpened] = useState(false);
  const [mode, setMode] = useState<'single' | 'batch'>('single'), modeRef = useRef<'single' | 'batch'>('single');
  const batchApprovalEpoch = useRef(0);
  const batchModelCache = useRef(new Map<string, BatchModelRead>());
  const [settingsOpen, setSettingsOpen] = useState(initialView === 'settings'), settingsOpenRef = useRef(settingsOpen);
  const [batch, setBatch] = useState<HubBatchDetail | null>(null), [batchId, setBatchId] = useState(''), [batchFilter, setBatchFilter] = useState<HubBatchFilter>({ ...emptyBatchFilter });
  const batchNav = useRef<BatchReturn | null>(null);
  const [batchHistory, setBatchHistory] = useState<HubBatchPage>({items: []}), [archiveBatches, setArchiveBatches] = useState<HubBatchPage>({items: []});
  const [archiveType, setArchiveType] = useState<'single' | 'batch'>('single');
  const [batchLookup, setBatchLookup] = useState(''), [batchMessage, setBatchMessage] = useState(''), [statusLoading, setStatusLoading] = useState(false);
  const [reportTarget, setReportTarget] = useState('');
  const desiredStatus = useRef<StatusSelection>(runtimeSelection), statusLoop = useRef<StateLoop | null>(null);
  const [archiveParams, setArchiveParams] = useState<Record<string, unknown>>({scope: 'archive', reportType: 'single', limit: 20});
  const stopIntentBatch = useRef('');
  const [stopIntent, setStopIntent] = useState<HubStopIntent | null>(null), stopIntentRef = useRef<HubStopIntent | null>(null), stopTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [recoverBusy, setRecoverBusy] = useState(false), recoverBusyRef = useRef(false), singleWaiter = useRef('');
  const serverDelta = useRef(0), lastStatusConfirmed = useRef(0);
  const setIntent = (intent: HubStopIntent | null) => { stopIntentRef.current = intent; setStopIntent(intent); };
  const [bankStats, setBankStats] = useState<BankStats | null>(null);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [catalog, setCatalog] = useState<HubCatalog | null>(null), catalogRef = useRef<HubCatalog | null>(null);
  const [draft, setDraft] = useState(emptyDraft), draftRef = useRef(emptyDraft);
  const [upstreamModels, setUpstreamModels] = useState<UpstreamModels | null>(null), [modelReload, setModelReload] = useState(0), modelRequest = useRef(0);
  const awaitingPrefill = useRef<HubTargetInput | null>(null);
  const [runtime, setRuntime] = useState<HubRuntime | null>(null), [targets, setTargets] = useState<HubTarget[]>([]);
  const [contextError, setContextError] = useState(''), [error, setError] = useState('');
  const [sections, setSections] = useState<string[]>(HUB_KINDS.map(k => k.value)), [settings, setSettings] = useState<Settings | null>(null);
  const sectionsRef = useRef(sections);
  const [sectionWarning, setSectionWarning] = useState('');
  const [history, setHistory] = useState<HubPage<HubReport>>({ items: [] }), [historyError, setHistoryError] = useState('');
  const [report, setReport] = useState<HubReport | null>(null), [active, setActive] = useState<HubReport | null>(null), [reportMessage, setReportMessage] = useState('');
  const [retryBusy, setRetryBusy] = useState(false), [retryPending, setRetryPending] = useState<{ reportId: string; retryId: string; expectedRetryId: string; itemIds: string[] } | null>(null);
  const [items, setItems] = useState<HubItem[]>([]), [itemBusy, setItemBusy] = useState(false), [itemsComplete, setItemsComplete] = useState(false), [itemError, setItemError] = useState('');
  const [itemsReadVersion, setItemsReadVersion] = useState('');
  const runtimeChecks = useRef(0), [runtimeChecking, setRuntimeChecking] = useState(false);
  const itemReader = useRef<{ id: string; request: number; state: ItemReadState; promise?: Promise<void>; version: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [actions, setActions] = useState<Partial<Record<Submission, StartAction>>>({}), actionsRef = useRef(actions);
  const [pending, setPending] = useState<Partial<Record<Submission, PendingStart>>>({}), pendingRef = useRef(pending);
  const [archive, setArchive] = useState<HubPage<HubReport>>({ items: [] }), [from, setFrom] = useState(''), [to, setTo] = useState(''), [archiveTarget, setArchiveTarget] = useState(''), [reportLookup, setReportLookup] = useState('');
  const dirty = useRef(false);
  const onDirtyChange = useCallback((value: boolean) => { dirty.current = value; bridge.current?.setDirty(value); }, []);
  const reportRequest = useRef(0);
  const lastReport = useRef<HubReport | null>(null), latestView = useRef(view), visibleRef = useRef(visible), alive = useRef(true), terminalSince = useRef<number | null>(null);
  const call: HubCall = useCallback(<T,>(op: string, params: Record<string, unknown> = {}, approveStart?: HubStartApproval, options?: HubCallOptions): Promise<T> => bridge.current ? bridge.current.call<T>(op, params, approveStart, options) : Promise.reject(new HubRequestError('请从调度站的模型检测按钮打开此页面。')), []);
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
    if (next !== 'new') { discardChecks(); batchApprovalEpoch.current++; }
    if (latestView.current !== next) { statusLoop.current?.controller.abort(); if (statusLoop.current?.timer) clearTimeout(statusLoop.current.timer); statusLoop.current = null; }
    latestView.current = next; setView(next);
  }, [discardChecks]);
  const chooseDraft = (next: HubTargetInput, prefill = false) => {
    modelRequest.current++; // 任何重新选择（含 BOOT、重新打开同一账号）都让进行中的模型读取立即作废。
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
    // 每次明确选择或重新打开账号都重新读取上游模型（同一账号再次打开也会读取并重新预选）。
    setModelReload(n => n + 1);
  };
  const applyCatalog = (value: HubCatalog | null) => {
    if (value && value.workspaceId !== scopeRef.current?.workspaceId) return;
    const next = equivalentCatalog(catalogRef.current, value) ? catalogRef.current : value;
    if (catalogRef.current !== next) { batchApprovalEpoch.current++; batchModelCache.current.clear(); }
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
  const applyRuntime = useCallback((result: HubRuntime) => {
    if (typeof result.serverNow === 'number') serverDelta.current = result.serverNow - Date.now();
    setRuntime(result); setActive(result.active ?? null);
  }, []);
  const loadRuntime = useCallback(async (options?: HubCallOptions) => {
    const scope = scopeRef.current, result = await call<HubRuntime>('runtime', {}, undefined, options);
    if (scopeRef.current === scope && alive.current && !options?.signal?.aborted) applyRuntime(result);
    return result;
  }, [call, applyRuntime]);
  const applyReportState = useCallback((value: HubReport, selected: boolean) => {
    if (selected) { setReport(value); lastReport.current = value; }
    const decision = reportPollDecision(value, terminalSince.current, Date.now() + serverDelta.current);
    terminalSince.current = decision.terminalSince;
    if (selected) setReportMessage(decision.message);
    if (reportFinishConfirmed(value)) setActive(current => current?.reportId === value.reportId ? null : current);
    else if (['preparing','running','stopping','saving_end'].includes(value.executionPhase ?? '') || (!value.executionPhase && value.status === 'running')) setActive(value);
    return decision;
  }, []);
  const loadHistory = useCallback(async (cursor = '', batchCursor = '') => {
    const scope = scopeRef.current, current = () => scopeRef.current === scope && alive.current;
    try {
      if (!batchCursor) {
        const page = await call<HubPage<HubReport>>('reports', { scope: 'sidebar', reportType: 'single', limit: 20, cursor });
        if (!current()) return;
        setHistory(old => ({ ...page, items: cursor ? [...old.items, ...(page.items ?? [])] : page.items ?? [] }));
      }
      if (!cursor) {
        const page = await call<HubBatchPage>('batches', { scope: 'sidebar', limit: 20, cursor: batchCursor });
        if (!current()) return;
        setBatchHistory(old => ({ ...page, items: batchCursor ? [...old.items, ...(page.items ?? [])] : page.items ?? [] }));
      }
      if (current()) setHistoryError('');
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
    const signal = statusLoop.current?.controller.signal;
    const itemCall: HubCall = (op, params, approve, options) => call(op, params, approve, {...options, signal});
    if (reader.promise) { await reader.promise; return; }
    if (!force && !resume && reader.version === version) return;
    if (!resume) reader.state = newItemRead();
    const selectedReader = reader;
    // An interrupted hidden read must remain eligible when this view returns.
    selectedReader.version = '';
    setItemsReadVersion('');
    setItemBusy(true); setItemError('');
    const run = async () => {
      for (let round = 0; round < 2 && current(); round++) {
        const startedVersion = reportVersion(lastReport.current!);
        try {
          await readReportItems(itemCall, id, selectedReader.state, current, rows => {
            if (!current()) return;
            setItems(previous => { const merged = new Map(previous.map(item => [item.itemId, item])); rows.forEach(item => merged.set(item.itemId, item)); return Array.from(merged.values()); });
          });
          if (!current()) return;
          selectedReader.version = startedVersion; setItemsComplete(selectedReader.state.complete);
          setItemsReadVersion(selectedReader.state.complete ? startedVersion : '');
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
    if (!boot || view !== 'archive' || archiveType !== 'single' || !visible) return;
    let live = true;
    void call<{ items: HubTarget[] }>('targets').then(value => {
      if (live && scopeRef.current === boot) setTargets(value.items ?? []);
    }, e => { if (live && scopeRef.current === boot) setError(problem(e)); });
    return () => { live = false; };
  }, [boot, view, archiveType, visible, call]);

  // 选中账号后，和分组健康「账号问答」一样实时向该账号的上游读取模型列表（先核对账号，再读模型）。
  // 只读取，不修改主站、不开始检测。重新选择（含 BOOT、重新打开）、换账号、检测页收到名单移除账号后，旧请求不再发出第二步，也不写入结果；
  // 卸载时桥接让未完成的调用失败，不会再发第二步（失败分支对已卸载页面的状态更新由 React 忽略）。
  const draftAccountReady = Boolean(catalogAccount(catalog, draft.groupId, draft.targetId));
  useEffect(() => {
    const request = ++modelRequest.current;
    const groupId = draft.groupId, targetId = draft.targetId;
    const key = modelKey(modelReload, groupId, targetId);
    if (!boot || !key || !draftAccountReady) { setUpstreamModels(null); return; }
    // 改分组或账号只有两条路：chooseDraft（BOOT、重新打开、手动选择都走它）会让请求编号失效；
    // 名单移除账号时 applyCatalog 会同步清空草稿里的账号。两项都核对即可丢弃旧读取。
    const stillCurrent = () => request === modelRequest.current
      && draftRef.current.groupId === groupId && draftRef.current.targetId === targetId;
    setUpstreamModels({ key, status: 'loading', models: [], error: '' });
    const source = catalogRef.current;
    const account = catalogAccount(source, groupId, targetId);
    const cacheKey = batchAccountKey(targetId, groupId);
    const cached: BatchModelRead | undefined = source ? {catalog: source, protocol: account?.protocol, models: [], status: 'loading'} : undefined;
    if (cached) batchModelCache.current.set(cacheKey, cached);
    const cacheCurrent = () => cached && catalogRef.current === source && batchModelCache.current.get(cacheKey) === cached;
    const read = (async () => {
      let step = '核对账号';
      try {
        const value = await call<HubContext>('context', { targetId, groupId });
        if (!stillCurrent()) return;
        const expectedContext = verifiedContext(value, boot.workspaceId, { groupId, targetId }); if (cached && cacheCurrent()) cached.context = expectedContext;
        step = '读取上游模型';
        const list = await call<{ models?: unknown }>('models', { targetId, groupId, expectedContext });
        if (!stillCurrent()) return;
        const ids = upstreamModelIds(list);
        if (cached && cacheCurrent()) { cached.models = ids; cached.status = 'done'; }
        setUpstreamModels({ key, status: 'done', models: ids, error: '' });
        if (!ids.length) return;
        // 和 V3.1.29 一样优先用本次核对返回的分组名判断默认模型（分组刚改名、名单还没更新时也按新名）；页面上的分组和账号仍按名单显示。
        const selection = selectHubDefaultModel(ids, value.groupName || catalogGroup(catalogRef.current, groupId)?.name || '');
        storeDraft({ ...draftRef.current, model: selection.model, modelNotice: selection.notice });
      } catch (e) {
        if (stillCurrent() && cached && cacheCurrent()) { cached.status = 'failed'; cached.reason = problem(e); }
        if (stillCurrent()) setUpstreamModels({ key, status: 'failed', models: [], error: `${step}时${modelReadReason(e instanceof HubRequestError ? problem(e) : e instanceof Error ? e.message : '', e instanceof HubRequestError ? e.status : 0)}` });
      } finally { if (cached && cacheCurrent()) { cached.promise = undefined; if (cached.status === 'loading') batchModelCache.current.delete(cacheKey); } }
    })();
    if (cached) cached.promise = read;
    // 不需要清理函数：effect 每次重新执行都会先让请求编号加一。
  }, [boot, draft.groupId, draft.targetId, draftAccountReady, modelReload, call]);

  const releaseStop = () => { if (stopTimer.current) { clearTimeout(stopTimer.current); stopTimer.current = null; } setIntent(null); };
  const stopUnknown = '停止结果尚未确认：确认时间已过，检测可能仍在继续。自动核查已暂停，请检查运行环境后核查原报告；本次不会新建、补测或重复发送停止。';
  const observeStop = (row: {reportId: string; phase?: string; executionPhase?: string; retryId?: string; retry?: {id:string}; finishConfirmed?: boolean; stopConfirmDeadlineMs?: number}) => {
    const intent = stopIntentRef.current;
    if (!intent || intent.reportId !== row.reportId) return;
    if (intent.expectedRetryId !== retryRound(row)) { releaseStop(); return; }
    const phase = row.executionPhase ?? row.phase;
    if (phase === 'ended' && row.finishConfirmed === true || phase === 'saving_end' || phase === 'saving_cancel' || phase === 'paused') { releaseStop(); return; }
    if (intent.ack !== 'pending' && row.stopConfirmDeadlineMs && row.stopConfirmDeadlineMs - serverDelta.current < intent.confirmationDeadlineMs) {
      const deadline = row.stopConfirmDeadlineMs - serverDelta.current;
      setIntent({ ...intent, confirmationDeadlineMs: deadline });
    }
  };
  const observeRuntime = (value: HubRuntime, projectSelected = false) => {
    const row = value.active;
    if (!row) return;
    observeStop(row);
    const selected = lastReport.current;
    // A report response already reconciles both snapshots. Its nested runtime is
    // the read-after view for the top line, not authority to promote that report
    // to a healthier phase. A priority runtime-only read can enrich a same-round
    // report but cannot turn an unresolved report into a healthy one.
    if (projectSelected && selected?.reportId === row.reportId && retryRound(selected) === retryRound(row) && !(selected.executionPhase === 'unknown' && ['queued','preparing','running'].includes(row.executionPhase ?? ''))) {
      const next = {...selected, executionPhase: row.executionPhase, finishConfirmed: row.finishConfirmed, processExited: row.processExited, recoveryDeadlineMs: row.recoveryDeadlineMs, recoveryReason: row.recoveryReason, stopConfirmDeadlineMs: row.stopConfirmDeadlineMs, stopCallPending: row.stopCallPending, executionConfirmDeadlineMs: row.executionConfirmDeadlineMs};
      if (row.executionPhase) applyReportState(next, true);
    }
  };
  const beginStatus = useCallback((selection: StatusSelection, force = false, known?: HubReport | HubBatchDetail, runtimeFirst = false): Promise<void> => {
    const scope = scopeRef.current, key = JSON.stringify(selection);
    if (!scope || !visibleRef.current || !alive.current) return Promise.resolve();
    if (!force && statusLoop.current?.key === key) return statusLoop.current.promise ?? Promise.resolve();
    if (statusLoop.current?.timer) clearTimeout(statusLoop.current.timer);
    statusLoop.current?.controller.abort();
    const loop: StateLoop = {key, controller: new AbortController()}; statusLoop.current = loop;
    const current = () => statusLoop.current === loop && scopeRef.current === scope && alive.current && visibleRef.current && !loop.controller.signal.aborted;
    let failures = 0, busyDeadline = 0, readRuntimeFirst = runtimeFirst, readSelection = selection, forceItems = force;
    let initial = known, manualFirst = runtimeFirst;
    const waitRead = '正在读取或保存其他检测状态，稍后自动核查；此时不会重发检测题目。超过等待时间仍无法确认时，请按提示检查。';
    const setMessage = (message: string) => { if (selection.op === 'batch' || selection.op === 'batches') setBatchMessage(message); else setReportMessage(message); };
    const failureMessage = (e: unknown) => `读取最新状态未完成：${problem(e)}。${lastStatusConfirmed.current ? `以下保留${displayTime(lastStatusConfirmed.current, true)}最后确认的结果，` : ''}请重试读取；这不表示检测已失败。`;
    const fixedBusy = (deadline?: unknown) => {
      const fallback = Date.now() + 40000;
      const known = typeof deadline === 'number' && deadline > 0 ? deadline - serverDelta.current : fallback;
      busyDeadline = Math.min(busyDeadline || fallback, known);
    };
    const stopExpired = () => {
      const intent = stopIntentRef.current;
      const inSelection = intent && (selection.op === 'report' && selection.params.reportId === intent.reportId || selection.op === 'batch' && stopIntentBatch.current === selection.params.batchId);
      if (!intent || !inSelection || Date.now() < intent.confirmationDeadlineMs) return false;
      if (intent.ack === 'accepted') setIntent({...intent, ack: 'unknown'});
      setMessage(stopUnknown); setError(stopUnknown); return true;
    };
    const schedule = () => { if (current()) loop.timer = setTimeout(() => { if (current()) loop.promise = poll(); }, 2000); };
    const poll = async () => {
      if (!current()) return;
      if (readSelection.op === 'report' && singleWaiter.current === readSelection.params.reportId) { schedule(); return; }
      const id = readSelection.params.reportId as string | undefined;
      const reportSaving = id && ['saving_end','saving_cancel','paused','unknown','stopping'].includes(lastReport.current?.executionPhase ?? '');
      const priorityEligible = ['report','batch','runtime'].includes(readSelection.op);
      const afterStop = priorityEligible && Boolean(stopIntentRef.current || reportSaving || busyDeadline > 0);
      const options: HubCallOptions = { signal: loop.controller.signal, afterStop };
      setStatusLoading(true);
      try {
        if ((afterStop || readRuntimeFirst) && readSelection.op !== 'runtime') {
          const observed = await loadRuntime({ ...options, afterStop: afterStop || busyDeadline > 0 });
          if (!current()) return;
          observeRuntime(observed, readSelection.op === 'report');
          if (observed.queryState === 'retained') { setMessage(observed.reason || '查询程序退出状态无法确认，请核查运行环境。'); setError(observed.reason || '环境已暂停，请检查本实例systemd与原进程。'); return; }
          if (!manualFirst && stopExpired()) return;
          if (observed.queryState === 'busy') {
            fixedBusy(observed.recoveryDeadlineMs);
            if (Date.now() >= busyDeadline) { setMessage(`状态待确认：${observed.reason || '等待时间已到'}。请检查运行环境后重试读取。`); return; }
            if (stopExpired()) return; manualFirst = false; setMessage(waitRead); readRuntimeFirst = true; schedule(); return;
          }
        }
        if (!manualFirst && stopExpired()) return;
        manualFirst = false;
        if (readSelection.op === 'runtime') {
          const result = await loadRuntime(options); if (!current()) return; observeRuntime(result);
          if (result.queryState === 'retained') { setError(result.reason || '无法确认查询程序退出，请检查运行环境。'); return; }
          if (result.queryState === 'busy') { fixedBusy(result.recoveryDeadlineMs); if (Date.now() >= busyDeadline) { setError(`状态待确认：${result.reason || '等待时间已到'}。请检查运行环境后重试读取。`); return; } }
          else busyDeadline = 0;
        } else if (readSelection.op === 'report') {
          const value = initial as HubReport | undefined ?? await call<HubReport>('report', readSelection.params, undefined, options); initial = undefined;
          if (!current()) return;
          acceptedReport(value, String(id), scope); observeStop(value);
          const decision = applyReportState(value, true);
          if (value.runtime) { applyRuntime(value.runtime); observeRuntime(value.runtime); }
          lastStatusConfirmed.current = Date.now();
          if (stopExpired()) return;
          if (!stopIntentRef.current && (forceItems || !['saving_end','saving_cancel','paused','unknown','stopping'].includes(value.executionPhase ?? ''))) { await loadItems(value.reportId, false, forceItems); forceItems = false; }
          if (!current()) return;
          if (!decision.poll) {
            if (!reportFinishConfirmed(value)) { if (!value.runtime && value.executionPhase) { const observed = await loadRuntime({ ...options, afterStop: true }); if (current()) observeRuntime(observed); } return; }
            void loadHistory(); readSelection = runtimeSelection;
          }
        } else if (readSelection.op === 'batch') {
          const value = initial as HubBatchDetail | undefined ?? await call<HubBatchDetail>('batch', readSelection.params, undefined, options); initial = undefined;
          if (!current()) return;
          if (value.batchId !== readSelection.params.batchId) throw new Error('批次编号不一致，请按原编号查询。');
          if (value.runtime) { applyRuntime(value.runtime); observeRuntime(value.runtime); }
          // The visible page is filtered; the stop target may only remain in the
          // always-present all-member projection. Row data adds its deadline.
          value.memberStates?.forEach(observeStop);
          value.items?.forEach(observeStop);
          setBatch(value); lastStatusConfirmed.current = Date.now(); if (stopExpired()) return; setBatchHistory(old => ({...old, items: old.items.map(row => row.batchId === value.batchId ? {...row, counts: value.counts, state: value.state} : row)}));
          if (value.state === 'unknown' || value.state === 'paused') { setBatchMessage(value.runtime?.reason || '状态尚未确认，请核查运行状态。'); return; }
          if (value.state === 'ended') { void loadHistory(); readSelection = runtimeSelection; }
        } else if (readSelection.op === 'batches') {
          const page = await call<HubBatchPage>('batches', readSelection.params, undefined, options);
          if (!current()) return; if (page.runtime) applyRuntime(page.runtime);
          setArchiveBatches({...page, items: page.items ?? []});
        } else {
          const page = await call<HubPage<HubReport>>('reports', readSelection.params, undefined, options);
          if (!current()) return; setArchive({...page, items: page.items ?? []}); readSelection = runtimeSelection;
        }
        if (!current()) return;
        failures = 0; busyDeadline = 0; readRuntimeFirst = false; lastStatusConfirmed.current = Date.now();
        setError(old => old.startsWith('读取最新状态未完成：') ? '' : old);
        if (selection.op === 'batch' || selection.op === 'batches') setBatchMessage('');
        schedule();
      } catch (e) {
        if (!current()) return;
        if (stopExpired()) return;
        if (e instanceof HubRequestError && e.details.queryState === 'retained') { setMessage(failureMessage(e)); setError(problem(e)); return; }
        if (e instanceof HubRequestError && e.details.queryState === 'busy' && (afterStop || e.status === 429)) {
          fixedBusy(e.details.recoveryDeadlineMs); readRuntimeFirst = true;
          if (Date.now() >= busyDeadline) { setMessage(failureMessage(e)); setError(problem(e)); return; }
          setMessage(waitRead); schedule(); return;
        }
        const message = failureMessage(e); setMessage(message); setError(message);
        if (++failures < 3) schedule(); else setMessage(`${message} 连续三次无法读取运行状态，已停止自动读取；请点击核查运行状态，不会自动重复检测。`);
      } finally { if (current()) setStatusLoading(false); }
    };
    loop.promise = poll(); return loop.promise;
  }, [call, loadRuntime, loadItems, loadHistory, applyRuntime, applyReportState]);
  const openReport = useCallback(async (id: string, known?: HubReport, fromBatch?: BatchReturn) => {
    if (!/^[0-9a-f]{32}$/.test(id)) { setError('请输入完整32位报告编号后查看已保存结果。'); return; }
    if (settingsOpenRef.current) { if (dirty.current && !confirm('检测设置有未保存的修改，确定关闭并放弃吗？')) return; settingsOpenRef.current = false; setSettingsOpen(false); }
    const same = lastReport.current?.reportId === id;
    if (fromBatch?.reportId === id) batchNav.current = fromBatch;
    else if (batchNav.current?.reportId !== id) batchNav.current = null;
    reportRequest.current++; itemReader.current = null; setItemBusy(false); setItemError(''); setReportLookup('');
    if (!same) { setRetryPending(null); setReport(null); lastReport.current = null; setItemsComplete(false); setItemsReadVersion(''); setItems([]); terminalSince.current = null; }
    setReportTarget(id); setError(''); if (!same) setReportMessage(''); changeView('report');
    const selection: StatusSelection = { op: 'report', params: { reportId: id } }; desiredStatus.current = selection;
    return beginStatus(selection, true, known);
  }, [beginStatus, changeView]);
  const openBatch = useCallback((id: string, known?: HubBatchDetail, filter: HubBatchFilter = {...emptyBatchFilter}) => {
    if (!/^[0-9a-f]{32}$/.test(id)) { setError('请输入完整32位批次编号后查看已保存任务。'); return; }
    if (settingsOpenRef.current) { if (dirty.current && !confirm('检测设置有未保存的修改，确定关闭并放弃吗？')) return; settingsOpenRef.current = false; setSettingsOpen(false); }
    reportRequest.current++; itemReader.current = null;
    setBatch(old => old?.batchId === id ? old : null); setBatchId(id); setBatchFilter(filter); setBatchMessage(''); setError(''); changeView('batch');
    const selection: StatusSelection = { op: 'batch', params: { batchId: id, ...filter, limit: 20 } }; desiredStatus.current = selection;
    void beginStatus(selection, true, known);
  }, [beginStatus, changeView]);
  const changeBatchFilter = (filter: HubBatchFilter) => { setBatchFilter(filter); setBatchMessage(''); setError(''); const selection: StatusSelection = { op: 'batch', params: { batchId, ...filter, limit: 20 }}; desiredStatus.current = selection; void beginStatus(selection, true); };
  const openBatchReport = (id: string) => { void openReport(id, undefined, {id: batchId, reportId: id, filter: batchFilter, scroll: window.scrollY}); };
  const returnBatch = () => {
    if (!report?.batchId) return;
    const nav = batchNav.current; batchNav.current = null;
    if (nav?.reportId === report.reportId && nav.id === report.batchId) { openBatch(nav.id, undefined, nav.filter); requestAnimationFrame(() => window.scrollTo(0, nav.scroll)); }
    else openBatch(report.batchId);
  };

  useEffect(() => {
    alive.current = true;
    const receiveVisibility = (next: boolean) => {
      visibleRef.current = next; setVisible(next);
      if (!next) { discardChecks(); batchApprovalEpoch.current++; statusLoop.current?.controller.abort(); if (statusLoop.current?.timer) clearTimeout(statusLoop.current.timer); statusLoop.current = null; }
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
      setReport(null); setItems([]); setItemBusy(false); setItemsComplete(false); setItemsReadVersion(''); setItemError(''); setReportMessage('');
      statusLoop.current?.controller.abort(); if (statusLoop.current?.timer) clearTimeout(statusLoop.current.timer); statusLoop.current = null; releaseStop();
      batchApprovalEpoch.current++; batchModelCache.current.clear(); modeRef.current = 'single'; setMode('single'); setBatchOpened(false); setBatch(null); setBatchId(''); setBatchFilter({...emptyBatchFilter}); batchNav.current = null; setBatchHistory({items: []}); setArchiveBatches({items: []}); setBatchMessage(''); setReportTarget(''); setBatchLookup(''); setSettingsOpen(initialView === 'settings'); settingsOpenRef.current = initialView === 'settings';
      setRuntime(null); setActive(null); setArchive({ items: [] }); setTargets([]); setArchiveTarget(''); setReportLookup('');
      setSettings(null); setBankStats(null); setHistory({ items: [] }); setHistoryError('');
      setRetryBusy(false); setRetryPending(null); setBusy(false);
      actionsRef.current = {}; setActions(actionsRef.current); pendingRef.current = {}; setPending(pendingRef.current);
      if (previous) changeView(initialView);
      setError('');
      awaitingPrefill.current = null; storeDraft(emptyDraft);
      applyCatalog(value.catalog?.workspaceId === value.workspaceId ? value.catalog : null);
      chooseDraft(value.target ?? {}, true);
    }, value => {
      if (dirty.current && !confirm('检测设置有未保存的修改，确定切换到新账号并放弃吗？')) return;
      settingsOpenRef.current = false; setSettingsOpen(false); batchApprovalEpoch.current++; modeRef.current = 'single'; setMode('single');
      if (value.reportId && /^[0-9a-f]{32}$/.test(value.reportId)) { void openReport(value.reportId); return; }
      chooseDraft(value, true); changeView('new'); setError('');
    }, receiveVisibility, warning => setError(readableReportError(warning)), applyCatalog, nextTarget => {
      batchApprovalEpoch.current++;
      if (!nextTarget || nextTarget.reportId) discardChecks();
      else discardChecks(target => target.groupId === nextTarget.groupId && target.targetId === nextTarget.targetId);
    });
    bridge.current = instance;
    const standalone = setTimeout(() => {
      if (!scopeRef.current) setError(old => old || '请从调度站中的“模型检测”按钮打开此页面。页面本身不会启动检测。');
    }, 10000);
    return () => { alive.current = false; reportRequest.current++; clearTimeout(standalone); statusLoop.current?.controller.abort(); if (statusLoop.current?.timer) clearTimeout(statusLoop.current.timer); statusLoop.current = null; if (stopTimer.current) clearTimeout(stopTimer.current); instance.dispose(); bridge.current = null; scopeRef.current = null; };
  }, [changeView, discardChecks, initialView, openReport]);

  useEffect(()=>{if(boot?.target.reportId && /^[0-9a-f]{32}$/.test(boot.target.reportId)) void openReport(boot.target.reportId);},[boot,openReport]);
  useEffect(() => { if (boot && visible) void loadHistory(); }, [boot, visible, loadHistory]);
  const selection: StatusSelection = view === 'report' && reportTarget ? {op: 'report', params: {reportId: reportTarget}} : view === 'batch' && batchId ? {op: 'batch', params: {batchId, ...batchFilter, limit: 20}} : view === 'archive' ? {op: archiveType === 'batch' ? 'batches' : 'reports', params: archiveParams} : runtimeSelection;
  const selectionKey = JSON.stringify(selection);
  desiredStatus.current = selection;
  useEffect(() => { if (boot && visible) void beginStatus(selection); }, [boot, visible, selectionKey, beginStatus]);

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
    let knownReport = known, busyDeadline = 0, preferRuntime = false, failures = 0;
    const paused = (reason: string) => new Error(`问答记录${request.reportId}已受理，但${reason}，本次等待已结束。请核查原记录；不会自动重复提问或补测。`);
    const wait = () => new Promise(resolve => setTimeout(resolve, 800));
    const fixedBusy = (deadline?: unknown) => { const fallback = Date.now() + 40000; busyDeadline = Math.min(busyDeadline || fallback, typeof deadline === 'number' && deadline > 0 ? deadline - serverDelta.current : fallback); };
    singleWaiter.current = request.reportId;
    try {
      while (current()) {
        if (!visibleRef.current) { await wait(); continue; }
        try {
          if (preferRuntime || stopIntentRef.current?.reportId === request.reportId) {
            const observed = await loadRuntime({afterStop: true, signal}); if (!current()) return null;
            observeRuntime(observed, latestView.current === 'report' && desiredStatus.current.params.reportId === request.reportId && observed.active?.reportId === request.reportId);
            if (observed.queryState === 'retained') throw paused(observed.reason || '查询程序是否结束尚未确认');
            if (observed.active?.reportId === request.reportId) {
              const decision = reportPollDecision(observed.active, null, Date.now() + serverDelta.current);
              if (!decision.poll && !reportFinishConfirmed(observed.active)) throw paused(decision.message);
            }
            if (observed.queryState === 'busy') { fixedBusy(observed.recoveryDeadlineMs); if (Date.now() >= busyDeadline) throw paused(observed.reason || '读取状态等待时间已到'); await wait(); continue; }
          }
          const row = knownReport ?? acceptedReport(await call<HubReport>('report', { reportId: request.reportId }, undefined, {afterStop: preferRuntime, signal}), request.reportId, action.scope);
          knownReport = undefined;
          if (!current()) return null;
          observeStop(row); const selected = latestView.current === 'report' && desiredStatus.current.params.reportId === row.reportId;
          const decision = applyReportState(row, selected);
          if (row.runtime) { applyRuntime(row.runtime); observeRuntime(row.runtime); }
          if (reportFinishConfirmed(row)) {
            const page = await call<HubPage<HubItem>>('items', { reportId: request.reportId, limit: 5 }, undefined, {signal});
            if (!current()) return null;
            const item = page.items?.[0], metrics = object(object(item?.result?.evidence).single_ask);
            if (item?.result?.error || metrics.Err || ['cancelled', 'interrupted'].includes(row.status) || !Object.keys(metrics).length) throw paused(String(item?.result?.error || metrics.Err || '问答已停止或未完整返回'));
            storePending('single'); void loadHistory(); return metrics as Metrics;
          }
          if (!decision.poll) throw paused(decision.message || row.recoveryReason || '执行状态待确认');
          if (stopIntentRef.current?.reportId === row.reportId && Date.now() >= stopIntentRef.current.confirmationDeadlineMs) throw paused(stopUnknown);
          preferRuntime = ['saving_end','saving_cancel'].includes(row.executionPhase ?? ''); busyDeadline = 0; failures = 0;
        } catch (e) {
          if (!current()) return null;
          if (e instanceof HubRequestError && e.details.queryState === 'busy') {
            fixedBusy(e.details.recoveryDeadlineMs); preferRuntime = true;
            if (Date.now() >= busyDeadline) throw paused(problem(e));
          } else if (e instanceof HubRequestError && e.details.queryState === 'retained') throw paused(problem(e));
          else if (!(e instanceof HubRequestError) || ++failures >= 3) throw e;
        }
        await wait();
      }
      return null;
    } finally {
      if (singleWaiter.current === request.reportId) {
        singleWaiter.current = '';
        if (latestView.current === 'report' && desiredStatus.current.params.reportId === request.reportId && scopeRef.current === action.scope) void beginStatus(desiredStatus.current, true);
      }
    }
  };

  const resolveSubmission = async (kind: Submission, action: StartAction, request: PendingStart, method: 'first' | Confirmation, signal?: AbortSignal, submittedDraft?: typeof emptyDraft): Promise<Metrics | null> => {
    const current = () => currentPending(kind, action, request);
    const approveStart: HubStartApproval | undefined = method === 'first' ? protocol => {
      if (!currentAction(kind, action) || !action.target || !visibleRef.current || latestView.current !== 'new' || signal?.aborted) return false;
      if (action.target.protocol !== protocol) {
        setContextError(contextFailure('当前名单中的检测协议已变化。请点击窗口顶部「更新分组和账号」后重新选择；本次没有发送问题。'));
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
        else { void loadHistory(); }
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

  const stop = async (target?: HubBatchRow) => {
    const row: HubBatchRow | undefined = target ?? (report ? {...report, phase: report.executionPhase ?? (report.status === 'queued' ? 'queued' : reportFinishConfirmed(report) ? 'ended' : 'running')} : undefined);
    if (!row || stopIntentRef.current && (stopIntentRef.current.ack === 'pending' || Date.now() < stopIntentRef.current.confirmationDeadlineMs && stopIntentRef.current.reportId !== row.reportId)) return;
    const possible = stopAction(row, Date.now() + serverDelta.current, stopIntentRef.current);
    if (!possible || possible.disabled) return;
    const scope = scopeRef.current, id = row.reportId, expectedRetryId = retryRound(row), intent: HubStopIntent = {reportId: id, expectedRetryId, ack: 'pending', confirmationDeadlineMs: Date.now() + 15000};
    stopIntentBatch.current = target ? batchId : report?.batchId ?? ''; setIntent(intent); setBusy(true); setError('');
    reportRequest.current++; itemReader.current = null; setItemBusy(false); setItemsReadVersion('');
    if (stopTimer.current) clearTimeout(stopTimer.current);
    stopTimer.current = setTimeout(() => {
      const current = stopIntentRef.current;
      if (scopeRef.current !== scope || current?.reportId !== id || current.expectedRetryId !== expectedRetryId) return;
      // Preserve pending until the actual POST returns; a timeout cannot permit another Stop call.
      const selected = desiredStatus.current;
      if (selected.op === 'report' && selected.params.reportId === id || selected.op === 'batch' && selected.params.batchId === stopIntentBatch.current) { setError(stopUnknown); if (selected.op === 'report') setReportMessage(stopUnknown); else setBatchMessage(stopUnknown); statusLoop.current?.controller.abort(); if (statusLoop.current?.timer) clearTimeout(statusLoop.current.timer); statusLoop.current = null; setStatusLoading(false); }
    }, 15000);
    const same = () => scopeRef.current === scope && stopIntentRef.current?.reportId === id && stopIntentRef.current?.expectedRetryId === expectedRetryId;
    const control = call<HubReport>('cancel', { reportId: id, expectedRetryId });
    // Priority status can run while the independent cancel POST remains in flight.
    void beginStatus(desiredStatus.current, true);
    try {
      const result = await control;
      if (!same()) return;
      const current = stopIntentRef.current!;
      setIntent({...current, ack: Date.now() >= current.confirmationDeadlineMs ? 'unknown' : 'accepted'});
      if (result.reportId === id && (result.executionPhase || result.accountName)) {
        observeStop(result);
        if (lastReport.current?.reportId === id && retryRound(lastReport.current) === expectedRetryId && result.accountName) applyReportState(result, true);
      }
    } catch (e) {
      if (!same()) return;
      if (e instanceof HubRequestError && e.status === 409) { releaseStop(); setError(`停止未受理：${problem(e)}。请核查当前报告轮次，未停止其他任务。`); }
      else { setIntent({...stopIntentRef.current!, ack: 'unknown'}); setError(`停止结果尚未确认：${problem(e)}。已显示的回答仍可查看，请核查运行状态。`); }
    } finally { if (scopeRef.current === scope) setBusy(false); }
  };
  const checkRuntime = async () => {
    runtimeChecks.current++; setRuntimeChecking(true); setError('');
    // Explicit refresh restarts the same controller, never calls openReport twice.
    reportRequest.current++; itemReader.current = null; setItemBusy(false); setItemsReadVersion('');
    try { await beginStatus(desiredStatus.current, true, undefined, true); } finally { if (--runtimeChecks.current === 0) setRuntimeChecking(false); }
  };
  const recoverRuntime = async () => {
    if (recoverBusyRef.current || runtime?.recoverable !== true) return;
    const scope = scopeRef.current; recoverBusyRef.current = true; setRecoverBusy(true);
    try { await call('recover-runtime'); if (scopeRef.current === scope) { setError(''); void beginStatus(desiredStatus.current, true); } }
    catch (e) { if (scopeRef.current === scope) setError(`核查并重试保存未完成：${problem(e)}。已保存回答保留，不会重新发送检测题目。`); }
    finally { if (scopeRef.current === scope) { recoverBusyRef.current = false; setRecoverBusy(false); } }
  };
  const retryItems = async (ids: string[]) => {
    if (!report || retryBusy) return;
    const confirming = retryPending?.reportId === report.reportId;
    if (!confirming && (!itemsComplete || !reportFinishConfirmed(report) || report.batchId && !report.batchFirstFinishedAtMs)) return;
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
  const findArchive = (cursor = '', type = archiveType) => {
    // Pagination continues the last applied filters, not unsubmitted edits in the date/account controls.
    const params = cursor ? {...archiveParams, cursor} : {scope: 'archive', from, to, ...(type === 'single' && {targetId: archiveTarget, reportType: 'single'}), cursor: '', limit: 20};
    setError(''); setArchiveParams(params);
    const selection: StatusSelection = {op: type === 'batch' ? 'batches' : 'reports', params}; desiredStatus.current = selection; void beginStatus(selection, true);
  };
  const dismissSettings = () => { if (dirty.current && !confirm('检测设置有未保存的修改，确定关闭并放弃吗？')) return false; settingsOpenRef.current = false; setSettingsOpen(false); return true; };
  const showSettings = () => { batchApprovalEpoch.current++; discardChecks(); settingsOpenRef.current = true; setSettingsOpen(true); };
  useEffect(() => {
    if (!settingsOpen) return;
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.preventDefault(); dismissSettings(); } };
    window.addEventListener('keydown', key); return () => window.removeEventListener('keydown', key);
  }, [settingsOpen]);
  const navigate = (next: View) => { if (next === 'settings') { showSettings(); return; } if (settingsOpenRef.current && !dismissSettings()) return; changeView(next); setError(''); if (next === 'archive') { setArchiveType('single'); findArchive('', 'single'); } };
  const tasksPage = () => { if (settingsOpenRef.current && !dismissSettings()) return; setArchiveType('batch'); changeView('archive'); findArchive('', 'batch'); };
  const switchMode = (next: 'single' | 'batch') => { batchApprovalEpoch.current++; modeRef.current = next; setMode(next); if (next === 'batch') setBatchOpened(true); };
  const canBatchApprove = (token: number) => token === batchApprovalEpoch.current && visibleRef.current && latestView.current === 'new' && modeRef.current === 'batch' && !settingsOpenRef.current && Boolean(scopeRef.current);
  const handleBatchAccepted = (id: string, known?: HubBatchDetail) => { void loadHistory(); if (latestView.current === 'new' && modeRef.current === 'batch' && visibleRef.current) openBatch(id, known); };

  const groups = catalog?.groups ?? [];
  const group = catalogGroup(catalog, draft.groupId);
  const account = catalogAccount(catalog, draft.groupId, draft.targetId);
  const groupAccounts = group?.accountsError ? [] : group?.accounts ?? [];
  const draftKey = account ? modelKey(modelReload, draft.groupId, draft.targetId) : '';
  const upstream = upstreamModels?.key === draftKey ? upstreamModels : null;
  const modelLoading = waitingForModels(Boolean(account), upstream?.status);
  // 上游读到模型就只列这些；读取失败或上游没返回模型时，暂列检测题库的模型，也可以直接输入。
  const libraryFallback = Boolean(upstream && upstream.status !== 'loading' && upstream.models.length === 0);
  const models = upstream?.status === 'done' && upstream.models.length ? upstream.models : libraryFallback ? bankStats?.supported_models ?? [] : [];
  const isChecking = Boolean(actions.report?.target || actions.single?.target);
  const hasDraft = Boolean(account && draft.model.trim());
  const reportStop = report && stopAction({...report, phase: report.executionPhase ?? (report.status === 'queued' ? 'queued' : reportFinishConfirmed(report) ? 'ended' : 'running')}, Date.now() + serverDelta.current, stopIntent);
  const retryAllowed = Boolean(report && reportFinishConfirmed(report) && (!report.batchId || report.batchFirstFinishedAtMs));
  const sectionPicker = <details className="fold hub-section-picker" open>
          <summary>本次检测内容</summary>
          <p className="muted">本浏览器会记住这个工作区上次勾选的项目。再次新建时可以继续调整，勾选不会修改主站。</p>
          <p className="muted">选择分类后使用该分类中全部已启用题目；请在设置里增删或禁用单题。工具、缓存与指纹可能发送多次请求。缓存默认勾选，执行原版六轮检测，会增加请求；可取消。在设置中手动禁用或删除的题目仍跳过。</p>
          <p className="muted">模型自报检查位于推理题中，默认5分，只对GPT-6系列执行；可在设置里禁用或删除，不修改主站。</p>
          <div className="hub-sections">{HUB_KINDS.map(section => <label key={section.value} className="hub-checkbox"><input type="checkbox" checked={sections.includes(section.value)} onChange={e => toggleSection(section.value, e.target.checked)} />{section.label}</label>)}</div>
          {sectionWarning && <p role="status" className="muted">{sectionWarning}</p>}
        </details>;
  return <div className="hub-app">
    <aside className={'sidebar' + (mobileOpen ? ' mobile-open' : '')}>
      <button type="button" className="sidebar-logo hub-nav-button" onClick={() => navigate('new')}><span className="logo-symbol">?</span><span>掺水<span className="logo-punct">？</span></span></button>
      <div className="sidebar-sub">模型检测台</div>
      <button type="button" className={'nav-home hub-nav-button' + (view === 'new' ? ' active' : '')} onClick={() => navigate('new')}><Icon name="scan" />新建检测</button>
      <div className="hub-extra-nav">
        <button type="button" className={'nav-home hub-nav-button' + (view === 'batch' || view === 'archive' && archiveType === 'batch' ? ' active' : '')} onClick={tasksPage}><Icon name="layers" />任务与结果</button>
        <button type="button" className={'nav-home hub-nav-button' + (view === 'archive' ? ' active' : '')} onClick={() => navigate('archive')}><Icon name="clock" />查找更早记录</button>
      </div>
      <button type="button" className="mobile-history" aria-expanded={mobileOpen} onClick={() => setMobileOpen(!mobileOpen)}><Icon name="clock" /><span>{mobileOpen ? '收起历史' : '历史记录'}</span></button>
      <div className="sidebar-title"><span>本站检测记录</span><button type="button" className="hub-nav-button" onClick={() => void loadHistory()}>刷新</button></div>
      <div className="sidebar-list">
        {(['今天', '昨天'] as const).map(day => {
          const rows = history.items.filter(row => !row.batchId && sidebarDayLabel(row.createdAtMs) === day);
          const batchRows = batchHistory.items.filter(row => sidebarDayLabel(row.createdAtMs) === day);
          const groups = new Map<string, HubReport[]>();
          rows.forEach(row => { const key = row.groupId ? 'id:' + row.groupId : 'name:' + (row.groupName || '未记录分组'); groups.set(key, [...(groups.get(key) ?? []), row]); });
          return (rows.length > 0 || batchRows.length > 0) && <section key={day}><div className="sidebar-title">{day}</div>{batchRows.map(row => <button type="button" key={row.batchId} data-batch-id={row.batchId} className={'hist-item hub-nav-button' + (batchId === row.batchId && view === 'batch' ? ' active' : '')} onClick={() => openBatch(row.batchId)}><div className="history-model">批量检测 · {row.counts?.ended ?? 0}/{row.counts?.total ?? 0}份已结束</div><div className="history-meta"><b className="hist-badge">{batchStateLabel(row.state)}</b><time className="history-time">{displayTime(row.createdAtMs)}</time></div></button>)}{Array.from(groups).map(([key, reports]) => <section key={key}><div className="sidebar-title">{reports[0].groupName || '未记录分组'}{reports[0].groupId && rows.some(r => r.groupName === reports[0].groupName && r.groupId !== reports[0].groupId) ? ` #${reports[0].groupId}` : ''}</div>{reports.map(row => {
            const saved = object(row.summary), totalScore = saved.totalScore ?? object(saved.total).score, score = totalScore ?? saved.iqScore;
            return <button type="button" key={row.reportId} data-report-id={row.reportId} className={'hist-item hub-nav-button' + (report?.reportId === row.reportId && view === 'report' ? ' active' : '')} onClick={() => void openReport(row.reportId)}><div className="history-model">{row.accountName}{row.accountId ? ` #${row.accountId}` : ''}</div><div className="history-model">{row.model}</div><div className="history-meta"><b className="hist-badge" title={typeof totalScore === 'number' ? '总分' : '推理分'}>{typeof score === 'number' ? `${isVerifiedHubReport(row) && typeof totalScore !== 'number' ? '推理 ' : ''}${score} 分` : reportStatus(row.status)}</b><time className="history-time">{displayTime(row.createdAtMs)}</time></div>{typeof score === 'number' && row.status !== 'done' && <div className="history-model">{reportStatus(row.status)}</div>}</button>;
          })}</section>)}</section>;
        })}
        {history.items.length === 0 && batchHistory.items.length === 0 && !historyError && <p className="sidebar-empty">今天和昨天暂无记录。</p>}
        {history.nextCursor && <button type="button" className="nav-home hub-nav-button" onClick={() => void loadHistory(history.nextCursor)}>显示更多最近记录</button>}
        {batchHistory.nextCursor && <button type="button" className="nav-home hub-nav-button" onClick={() => void loadHistory('', batchHistory.nextCursor)}>显示更多最近批次</button>}
        {historyError && <p role="alert" className="sidebar-empty">{historyError}</p>}
      </div><div className="sidebar-bottom"><p className="sidebar-retention">只显示当前工作区今天和昨天的记录；更早报告仍可查找。</p></div>
    </aside>
    <main className="main hub-main" id="main-content"><div className="top-tools"><KnowledgeDates /><button type="button" className="theme-trigger docs-entry" onClick={()=>navigate('docs')}><Icon name="layers" /><span className="tool-label">接口文档</span></button><button type="button" className="theme-trigger" onClick={showSettings}><Icon name="layers"/><span className="tool-label">设置与全部题目</span></button><ThemeMenu /><GitHubLink /></div><div className={"page-shell" + (view === 'report' ? ' history-page' : '')}><header hidden={view === 'report'} className="hub-page-heading"><p className="muted">此页只做检测和保存结果，不会修改主站账号或调度设置。</p><p className="muted">收起或关闭页面不会停止已开始的检测；需要中止请点击停止检测。</p></header>
      {!boot && <section className="hero-card hub-card"><h1>正在连接调度站…</h1><p>请从调度站中的模型检测入口打开此页。静态页面不会启动检测程序。</p></section>}
      {runtime && (!runtime.available || runtime.queueBlocked || runtime.queryState === 'retained') && <section className="hero-card hub-card"><p className="hub-error" role="alert">{runtime.reason || (runtime.busy ? '当前已有检测正在执行，请等待后点击核查运行状态；仍可查看自己的历史和设置。' : '检测环境或结束状态需要核查。请检查运行诊断后重试。')}</p><div className="actions hub-wrap"><button type="button" className="btn-secondary" onClick={() => void checkRuntime()}>核查运行状态</button>{runtime.recoverable && <button type="button" className="btn-secondary" disabled={recoverBusy} onClick={() => void recoverRuntime()}>核查并重试保存</button>}</div>{runtime.recoverable && <p className="muted">程序已确认结束时，只重试保存，不重新检测或修改主站；退出状态无法确认时须先检查运行环境，此按钮不能解除暂停。</p>}</section>}
      {runtime?.queryState === 'retained' && <p className="hub-error">无法确认查询程序已退出，已保留位置并暂停后续检测。请检查本实例systemd与原进程后按审核重新启动本调度站后端；已保存回答保留，原等待任务不自动重跑。</p>}
      {runtime?.otherActive ? <div className="hub-note" role="status">当前有其他检测正在执行。<button type="button" className="hub-text-button" onClick={() => void checkRuntime()}>核查运行状态</button></div> : active ? <div className="hub-note" role="status">当前任务：{active.batchId ? `批次 ${active.batchId} · ` : ''}{active.accountName} · {active.model} · {active.executionPhase ? phaseLabel(active.executionPhase) : reportStatus(active.status)}。切换账号不会改变此任务。{(view !== 'report' || report?.reportId !== active.reportId) && <button type="button" className="hub-text-button" onClick={() => void openReport(active.reportId)}>查看该任务</button>}</div> : runtime?.busy ? <div className="hub-note" role="status">当前已有检测正在执行，新报告可加入等待队列，不会与它同时执行。<button type="button" className="hub-text-button" onClick={() => void checkRuntime()}>核查运行状态</button></div> : null}
      {view !== 'report' && <CheckedMemory report={runtime?.active} />}
      {error && <p role="alert" className="hub-error hub-alert">{error}</p>}
      {settingsOpen && boot && <div className="hub-settings-overlay" onClick={e => {if (e.target === e.currentTarget) dismissSettings();}}><section className="hub-settings-dialog" role="dialog" aria-modal="true" aria-label="检测设置"><div className="hub-settings-dialog-head"><strong>检测设置与全部题目</strong><button type="button" className="btn-secondary" onClick={dismissSettings}>关闭设置</button></div><HubSettings call={call} onDirtyChange={onDirtyChange} onSettingsSaved={setSettings} /></section></div>}
      {view === 'new' && boot && <div className="actions hub-wrap hub-create-mode"><button type="button" className={mode === 'single' ? 'btn-primary' : 'btn-secondary'} onClick={() => switchMode('single')}>单次检测</button><button type="button" className={mode === 'batch' ? 'btn-primary' : 'btn-secondary'} onClick={() => switchMode('batch')}>批量检测</button></div>}
      {view === 'batch' && boot && <HubBatchView batch={batch} filter={batchFilter} loading={statusLoading} message={batchMessage} stopIntent={stopIntent} onFilter={changeBatchFilter} onReport={openBatchReport} onStop={row => void stop(row)} onRefresh={() => void checkRuntime()} />}
      {boot && batchOpened && <div hidden={view !== 'new' || mode !== 'batch'}><HubBatchCreate key={boot.generation} call={call} catalog={catalog} catalogNow={() => catalogRef.current} workspaceId={boot.workspaceId} active={view === 'new' && mode === 'batch' && visible && !settingsOpen} sections={sections} settings={settings} available={runtime?.available === true && !runtime.queueBlocked} sectionPicker={sectionPicker} modelsCache={batchModelCache} approvalToken={() => batchApprovalEpoch.current} canApprove={canBatchApprove} onAccepted={handleBatchAccepted} /></div>}
      {view === 'new' && boot && <div className={"opening" + (mode === 'batch' ? " hub-batch-single-controls" : "")}>{mode === 'single' && <AuditHero />}<section className="hero-card connection-card hub-card">
        <div className="section-heading"><h1>{mode === 'single' ? '新建账号检测' : '单问单答账号'}</h1><span className="muted">只预填，不自动检测</span></div>
        <p className="muted">分组和账号优先使用上次名单。选中账号后会向该账号的上游读取模型列表，不会修改主站，也不会开始检测。</p>
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
        {!catalog && <p className="muted">尚未读到分组和账号。请查看窗口顶部的读取结果，或点击「更新分组和账号」重试；此时没有开始检测。</p>}
        {catalog && groups.length === 0 && <p className="muted">当前没有可选分组。请核对当前工作区及分组隐藏设置，需要时点击窗口顶部「更新分组和账号」；此时没有开始检测。</p>}
        {draft.selectionError && <p role="status" className="muted">{draft.selectionError}</p>}
        {group?.accountsError && <p className="hub-error" role="alert">上次没有读到这个分组的账号。请选择其他分组，或点击窗口顶部「更新分组和账号」重试；本次没有发送问题。</p>}
        {group && !group.accountsError && groupAccounts.length === 0 && <p className="muted">此分组没有可选账号。请选择其他分组；此时没有开始检测。</p>}
        {isChecking && <p role="status">{checkingContext}</p>}
        {contextError && <p role="alert" className="hub-error">{contextError}</p>}
        {account && <>
          <p className="hub-target-line"><strong>{account.accountName}</strong> · {group?.name}{account.protocol && ` · ${account.protocol}`}</p>
          <div className="connection-grid">
            <div className="field"><label>API 密钥<input readOnly value="使用此账号已配置的密钥" /></label><p className="muted">页面不显示或保存密钥；执行时由服务器读取。</p></div>
            <div className="field">
              <label htmlFor="model">目标模型</label>
              <ModelPicker id="model" value={draft.model} options={models} loading={modelLoading} disabled={modelLoading || Boolean(actions.report)} onRefresh={() => {
                storeDraft({ ...draftRef.current, model: '', modelNotice: '' });
                setModelReload(n => n + 1);
              }} onChange={value => storeDraft({ ...draftRef.current, model: value, modelNotice: '' })} placeholder="可选择或输入模型名称" describedBy="model-help" />
              <small id="model-help">可选择或输入自定义名称，请求时原样发送；没有对应基线时不代表已验证型号。</small>
              {modelLoading && <p role="status" className="muted">正在向上游读取此账号的模型列表，读完后才能编辑模型、开始检测或发送问题；本次只读取模型列表，不发送检测题目。</p>}
              {upstream?.status === 'failed' && <p role="alert" className="hub-error">读取此账号的模型列表未完成：{upstream.error}。可点模型框右侧的刷新按钮重新读取，也可以直接输入模型名称；本次读取不会开始检测。</p>}
              {upstream?.status === 'done' && upstream.models.length === 0 && <p className="muted">此账号的上游没有返回可选择的模型，请检查上游模型列表后点刷新按钮重新读取。</p>}
              {libraryFallback && <p className="muted">{models.length ? '下拉里暂列的是检测题库的模型，不代表此账号支持。' : '下拉里没有可选模型，可以直接在模型框输入模型名称。'}</p>}
              {draft.modelNotice && upstream?.status === 'done' && <p className="muted">{draft.modelNotice}</p>}
            </div>
          </div>
        </>}
        {mode === 'single' && sectionPicker}
        <p className="hub-note">点击开始后会向所选上游发送请求，可能产生费用；请先核对账号、模型和题目。检测任务默认{settings?.memoryLimitMiB ?? 256}MiB，整个检测子项目最多约0.5GB；报告保存后程序退出。</p>
        {mode === 'single' && <div className="actions hub-wrap">
          <button type="button" className="btn-primary" disabled={Boolean(actions.report || pending.report) || !hasDraft || modelLoading || !sections.length || runtime?.available !== true || runtime?.queueBlocked === true} onClick={() => void start()}>{actions.report?.target ? '正在核对账号…' : actions.report ? '正在确认受理…' : active || runtime?.busy || runtime?.otherActive || (runtime?.queuedCount ?? 0) > 0 ? '加入等待队列' : '开始检测'}</button>
        </div>}
        {pending.report && <section className="hub-note" aria-label="普通检测受理">
          <p>{pendingWarning}</p><p>原报告编号 <strong>{pending.report.reportId}</strong></p>
          <div className="actions hub-wrap">
            <button type="button" className="btn-secondary" disabled={Boolean(actions.report)} onClick={() => void confirmSubmission('report', 'lookup')}>查询本次受理结果</button>
            <button type="button" className="btn-secondary" disabled={Boolean(actions.report)} onClick={() => void confirmSubmission('report', 'replay')}>用原编号重试受理</button>
          </div>
        </section>}
        <div className="bank-strip"><span><Icon name="layers"/><b>{bankStats?.fingerprint_count ?? '—'}</b> 个可测模型</span><Hint label="模型库说明" text="服务端在检测任务中检查模型基线更新。数字指纹仅对库内候选比较，行为参考不等于型号证明。"/></div>
      </section></div>}
      {boot && <div hidden={view !== 'new'}><SingleAskPanel
        key={boot.generation}
        model={draft.model}
        canRun={hasDraft && !modelLoading && !actions.single && (!pending.single || pending.single.accepted === true)}
        onAsk={askOne}
        pending={pending.single && {
          reportId: pending.single.reportId,
          accepted: pending.single.accepted,
          busy: Boolean(actions.single),
          confirm: (method, signal) => confirmSubmission('single', method, signal),
          open: () => void openReport(pending.single!.reportId),
        }}
      /></div>}
      {view === 'report' && report && <>
        <section className="hero-card">
          <div className="section-heading"><div><span className="step">报告</span><h2>本站检测报告</h2></div>{report.batchId && <button type="button" className="btn-secondary" onClick={returnBatch}>返回批次</button>}</div>
          {report.batchId && <p className="muted">{batchNav.current?.reportId === report.reportId && batchNav.current.id === report.batchId ? '返回原批次及刚才的筛选位置，后台检测继续执行。' : '查看本报告所属批次，后台检测继续执行。'}</p>}
          <details className="fold"><summary>按编号查找其他报告</summary><p className="muted">输入报告编号后查看已保存结果，不会开始检测或修改主站。</p><form className="report-lookup" onSubmit={e => { e.preventDefault(); if (reportLookup.trim()) void openReport(reportLookup.trim()); }}><label className="sr-only" htmlFor="hub-report-id">检测 ID</label><input id="hub-report-id" value={reportLookup} placeholder="粘贴检测 ID" onChange={e => setReportLookup(e.target.value)} /><button type="submit" className="btn-primary">查看报告</button></form></details>
          <h1 className="hero-title">{report.model}</h1>
          <p className="report-meta">{new Date(report.createdAtMs).toLocaleString('zh-CN')} · {report.executionPhase ? phaseLabel(report.executionPhase) : reportStatus(report.status)} · 账号：{report.accountName} · 分组：{report.groupName}</p>
          <ReportShare id={report.reportId} identityLabel="报告编号" reportHref={(()=>{if(typeof window==='undefined')return '';const url=new URL(window.parent.location.href);url.searchParams.set('detectorReport',report.reportId);return url.href;})()} actions={<>            <button type="button" className="btn-secondary" onClick={() => void checkRuntime()}>核查运行状态</button>
            {reportStop && <button type="button" className="btn-secondary" disabled={busy || reportStop.disabled} onClick={() => void stop()}>{reportStop.label}</button>}
            <button type="button" className="btn-secondary" disabled={retryBusy || (!retryPending && (!itemsComplete || !retryAllowed || !items.some(canRetryHubItem)))} onClick={() => void retryItems(items.filter(canRetryHubItem).map(item => item.itemId))}>{retryBusy ? '正在确认补测…' : retryPending ? '用原操作确认补测受理' : `补测失败项（${items.filter(canRetryHubItem).length}）`}</button>
            <button type="button" className="btn-secondary" onClick={() => { chooseDraft({ targetId: report.targetId, groupId: report.groupId }, true); switchMode('single'); changeView('new'); }}>重新检测（新任务）</button>
</>} />
          <p className="muted">报告链接通过本站登录和当前工作区权限校验；复制链接不会公开报告。报告长期保存在磁盘，可在原报告补测失败项。</p>
          <p className="muted">补测只重新提问失败或未完成项，成功结果保留，结果更新到本报告；可能产生请求费用，不修改主站。{!itemsComplete ? '题目还未读完，请先等待读取完成。' : !isHubTerminal(report.status) ? '本轮正在运行或等待，结束后可再补测。' : !reportFinishConfirmed(report) ? '上一轮程序退出或结束状态尚未确认，请先核查运行状态。' : !items.some(canRetryHubItem) ? '当前没有需要补测的失败项目。' : ''}</p>
          {report.batchId && !report.batchFirstFinishedAtMs && <p className="muted">请先等本批首次执行结束，再补测这份报告的失败或未完成项；已保存回答保留。</p>}
          {reportStop && <p className="muted">{reportStop.label === '取消等待' ? '只取消这一份尚未开始的任务，不发送检测题目；其他任务继续保留。' : reportStop.label === '再次停止' ? '只有上次停止调用已返回且确认时间已过，才可再次停止这一份；会重新等待15秒确认，不影响其他任务。上次调用未返回时请先检查环境。' : '只停止这一份检测，已保存回答保留，不修改主站；其他等待任务在结束处理完成后继续。'}</p>}
          {report.status === 'queued' && (!report.executionPhase || report.executionPhase === 'queued') && typeof report.queuePosition === 'number' && <p role="status" className="muted">已加入等待，前面还有{Math.max(0, report.queuePosition)}份报告；轮到后自动开始，此时尚未发送检测题目。</p>}
          {report.status === 'cancelled' && reportFinishConfirmed(report) && <p className="muted">已停止，已保存回答保留；未执行题目不作答错。</p>}
          {reportMessage && <p role="status" className="muted">{reportMessage}</p>}
          {report.diagnostic && <p className="error-box" role="alert">{report.diagnostic}</p>}
          {reportError(report.error) && <p className="error-box" role="alert">{reportError(report.error)}</p>}
          <div className="hub-read-status" role={itemError ? 'alert' : 'status'}>{itemError || (itemBusy && !items.length ? '正在读取本次报告的题目与回答…' : `已读取${items.length}项回答${itemsComplete ? '；本次回答已读完。' : '；继续读取已保存的结果。'}`)}</div>
          {itemError && <button type="button" className="btn-secondary" disabled={itemBusy} title="只读取已保存的回答，不重新检测。" onClick={() => void loadItems(report.reportId, true, true)}>继续读取回答</button>}
          <details className="fold"><summary>运行与资源详情</summary><CheckedMemory report={report} /><p className="muted">结果只供人工检阅，不会自动降权、停用或恢复账号。配置不同的报告不宜只比较总分。</p></details>
        </section>
        <HubReportContent key={report.reportId} report={report} items={items} complete={itemsComplete} itemsReadVersion={itemsReadVersion}
          retryHold={retryBusy || retryPending?.reportId === report.reportId} readHold={runtimeChecking || Boolean(stopIntent?.reportId === report.reportId) || !reportFinishConfirmed(report) && isHubTerminal(report.status)}
          onRefresh={async expectedReportId => {
            if (expectedReportId && (!alive.current || latestView.current !== 'report' || lastReport.current?.reportId !== expectedReportId)) return;
            setItemsReadVersion('');
            return openReport(report.reportId);
          }}
          readFailed={Boolean(itemError) || itemBusy} call={call} onRetry={ids => void retryItems(ids)} retryDisabled={retryBusy || !itemsComplete || !retryAllowed || Boolean(retryPending)} />
      </>}
      {view === 'docs' && <><p className="hub-note">以下保留原版独立检测服务的接口说明，本站未开放这些匿名公共接口；本站检测请使用当前页面的账号入口。查看说明不会发起检测或修改主站。</p><UpstreamApiDocs referenceOnly /></>}
      {view === 'report' && !report && boot && <section className="hero-card hub-card"><h1>查看已保存报告</h1><p>{statusLoading ? '正在读取报告…' : '尚未读取报告，请核查运行状态。'}</p><button type="button" className="btn-secondary" onClick={() => void checkRuntime()}>核查运行状态</button><form className="report-lookup" onSubmit={e => {e.preventDefault(); if(reportLookup.trim()) void openReport(reportLookup.trim());}}><label className="sr-only">报告编号</label><input value={reportLookup} placeholder="粘贴报告编号" onChange={e => setReportLookup(e.target.value)} /><button type="submit" className="btn-primary">查看报告</button></form></section>}
      {view === 'archive' && boot && <section className="hero-card hub-card"><h1>{archiveType === 'batch' ? '任务与结果' : '查找更早记录'}</h1><p className="muted">历史记录长期保存在磁盘，按新加坡日期筛选；默认查询最近30天。只查看保存的记录，不会重新调用模型或修改主站。</p>
        <div className="actions hub-wrap"><button type="button" className={archiveType === 'single' ? 'btn-primary' : 'btn-secondary'} onClick={() => {setArchiveType('single'); findArchive('', 'single');}}>单次记录</button><button type="button" className={archiveType === 'batch' ? 'btn-primary' : 'btn-secondary'} onClick={() => {setArchiveType('batch'); findArchive('', 'batch');}}>批次记录</button></div>
        <div className="hub-form-grid"><div className="field"><label>开始日期<input type="date" value={from} onChange={e => setFrom(e.target.value)} /></label></div><div className="field"><label>结束日期<input type="date" value={to} onChange={e => setTo(e.target.value)} /></label></div>{archiveType === 'single' && <div className="field"><label>账号<Select value={archiveTarget} onChange={e => setArchiveTarget(e.target.value)}><option value="">全部账号</option>{targets.map(t => <option key={t.targetId} value={t.targetId}>{t.accountName}</option>)}</Select></label></div>}</div>
        <button type="button" className="btn-primary" onClick={() => void findArchive()}>查找{archiveType === 'batch' ? '批次' : '报告'}</button>
        <div className="hub-search field"><label>按批次编号查找<input value={batchLookup} onChange={e => setBatchLookup(e.target.value)} /></label><button type="button" className="btn-secondary" onClick={() => openBatch(batchLookup.trim())}>打开此批次</button></div><p className="muted">按批次编号查看已保存任务，不重新提交或修改主站；暂未找到请稍后再查原编号。</p>
        <div className="hub-search field"><label>按报告编号查找<input value={reportLookup} onChange={e => setReportLookup(e.target.value)} /></label><button type="button" className="btn-secondary" onClick={() => void openReport(reportLookup.trim())}>打开此报告</button></div><p className="muted">输入报告编号后查看已保存结果，不会开始检测或修改主站。</p>
        {archiveType === 'single' ? <>{archive.items.filter(row => !row.batchId).map(row => <div className="hub-question-row" key={row.reportId}><div><strong>{row.accountName} · {row.model}</strong><p className="muted">{displayTime(row.createdAtMs, true)} · {row.groupName} · {reportStatus(row.status)}</p></div><button type="button" className="btn-secondary" onClick={() => void openReport(row.reportId)}>查看报告</button></div>)}{archive.items.length === 0 && <p className="muted">此范围没有报告，可调整日期或直接输入报告编号。</p>}{archive.nextCursor && <button type="button" className="btn-secondary" onClick={() => void findArchive(archive.nextCursor)}>下一页报告</button>}</> : <>{archiveBatches.items.map(row => <div className="hub-question-row" key={row.batchId}><div><strong>批量检测 · {batchStateLabel(row.state)} · {row.counts?.ended}/{row.counts?.total}份已结束</strong><p className="muted">{displayTime(row.createdAtMs, true)}</p></div><button type="button" className="btn-secondary" onClick={() => openBatch(row.batchId)}>查看批次</button></div>)}{archiveBatches.items.length === 0 && <p className="muted">此范围没有批次，可调整日期或直接输入批次编号。</p>}{archiveBatches.nextCursor && <button type="button" className="btn-secondary" onClick={() => void findArchive(archiveBatches.nextCursor)}>下一页批次</button>}{batchMessage && <p role="status" className="hub-error">{batchMessage}</p>}</>}
      </section>}

    </div></main>
  </div>;
}
