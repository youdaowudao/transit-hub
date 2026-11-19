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
import { canRetryHubItem, newItemRead, readReportItems, reportError, reportVersion, object, type ItemReadState } from '../lib/hub-report-view';
import { createHubBridge, HubRequestError, type HubBoot } from '../lib/hub-bridge';
import { HUB_KINDS, isHubTerminal, newHubID, reportPollDecision, reportStatus, selectHubDefaultModel, sidebarDayLabel } from '../lib/hub-policy';
import type { HubCall, HubContext, HubItem, HubPage, HubReport, HubRuntime, HubSettings as Settings, HubTarget, HubTargetInput } from '../lib/hub-types';

type View = 'new' | 'report' | 'settings' | 'archive' | 'docs';
const problem = (error: unknown) => error instanceof Error ? error.message : '读取检测结果时中断，原因尚未确认；请重试读取，不要重复新建检测。';
const displayTime = (value: number, full = false) => new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Singapore', ...(full ? { year: 'numeric', month: '2-digit', day: '2-digit' } as const : {}), hour: '2-digit', minute: '2-digit', hour12: false }).format(value);
function CheckedMemory({ report }: { report?: HubReport | null }) {
  return report?.taskMemoryMaxBytes ? <p className="muted">本次启动已核验：任务最多 {(report.taskMemoryMaxBytes / 1048576).toFixed(0)}MiB；整个检测子项目最多 {((report.sliceMemoryMaxBytes ?? 500000000) / 1000000).toFixed(0)}MB；{report.swapMaxBytes === 0 ? '交换内存已禁用' : '交换内存上限待核查'}。{report.memoryPeakBytes ? `已记录峰值 ${(report.memoryPeakBytes / 1048576).toFixed(1)}MiB。` : ''}</p> : null;
}


export { default as HubItemCard } from './HubQuestionDetails';

export default function HubApp({ initialView = 'new' }: { initialView?: View }) {
  const singlePending=useRef<{key:string;input:Record<string,unknown>;id:string;accepted:boolean;failed:boolean;retry?:{id:string;expected:string}}|null>(null);
 const bridge = useRef<ReturnType<typeof createHubBridge> | null>(null);
  const [boot, setBoot] = useState<HubBoot | null>(null), [visible, setVisible] = useState(true), [view, setView] = useState<View>(initialView);
  const [bankStats,setBankStats]=useState<BankStats|null>(null);
 const [mobileOpen, setMobileOpen] = useState(false);
  const [input, setInput] = useState<HubTargetInput>({}), [targetChange, setTargetChange] = useState(0);
  const [runtime, setRuntime] = useState<HubRuntime | null>(null), [targets, setTargets] = useState<HubTarget[]>([]), [context, setContext] = useState<HubContext | null>(null);
  const [models, setModels] = useState<string[]>([]), [model, setModel] = useState(''), [modelNotice, setModelNotice] = useState(''), [targetBusy, setTargetBusy] = useState(false);
  const [contextError, setContextError] = useState(''), [error, setError] = useState(''), [notice, setNotice] = useState('');
  const [sections, setSections] = useState<string[]>(HUB_KINDS.map(k => k.value)), [settings, setSettings] = useState<Settings | null>(null);
  const [history, setHistory] = useState<HubPage<HubReport>>({ items: [] }), [historyError, setHistoryError] = useState('');
  const [report, setReport] = useState<HubReport | null>(null), [active, setActive] = useState<HubReport | null>(null), [reportMessage, setReportMessage] = useState('');
  const [stopRequested, setStopRequested] = useState('');
  const [retryBusy, setRetryBusy] = useState(false), [retryPending, setRetryPending] = useState<{ reportId: string; retryId: string; expectedRetryId: string; itemIds: string[] } | null>(null);
  const [items, setItems] = useState<HubItem[]>([]), [itemBusy, setItemBusy] = useState(false), [itemsComplete, setItemsComplete] = useState(false), [itemError, setItemError] = useState(''), [pollEpoch, setPollEpoch] = useState(0);
  const itemReader = useRef<{ id: string; request: number; state: ItemReadState; promise?: Promise<void>; version: string } | null>(null);
  const [busy, setBusy] = useState(false), [pending, setPending] = useState<{ reportId: string; payload: Record<string, unknown> } | null>(null);
  const [archive, setArchive] = useState<HubPage<HubReport>>({ items: [] }), [from, setFrom] = useState(''), [to, setTo] = useState(''), [archiveTarget, setArchiveTarget] = useState(''), [reportLookup, setReportLookup] = useState('');
  const dirty = useRef(false);
  const onDirtyChange = useCallback((value: boolean) => { dirty.current = value; bridge.current?.setDirty(value); }, []);
  const bootReceived = useRef(false), reportRequest = useRef(0);
  const currentTargetRequest = useRef(0), lastReport = useRef<HubReport | null>(null), latestView = useRef(view), visibleRef = useRef(visible), alive = useRef(true), terminalSince = useRef<number | null>(null);
  lastReport.current = report; latestView.current = view; visibleRef.current = visible;
  const call: HubCall = useCallback(<T,>(op: string, params: Record<string, unknown> = {}): Promise<T> => bridge.current ? bridge.current.call<T>(op, params) : Promise.reject(new HubRequestError('请从调度站的模型检测按钮打开此页面。')), []);
  const resetTarget = useCallback((next: HubTargetInput) => {
    currentTargetRequest.current++;
    setInput(next); setTargetChange(n => n + 1); setPending(null); setContext(null); setModels([]); setModel(''); setModelNotice(''); setContextError(''); setTargetBusy(false);
  }, []);

  useEffect(() => {
    alive.current = true;
    const instance = createHubBridge(value => { bootReceived.current = true; setBoot(value); setVisible(value.visible); resetTarget(value.target); }, value => { if (dirty.current && !confirm('检测设置有未保存的修改，确定切换到新账号并放弃吗？')) return; resetTarget(value); setView('new'); setError(''); setNotice(''); }, setVisible, warning => setError(readableReportError(warning)));
    bridge.current = instance;
    const standalone = setTimeout(() => { if (bootReceived.current) return; setError(old => old || '请从调度站中的“模型检测”按钮打开此页面。页面本身不会启动检测。'); }, 10000);
    return () => { alive.current = false; reportRequest.current++; clearTimeout(standalone); instance.dispose(); bridge.current = null; };
  }, []);
  const loadRuntime = useCallback(async () => { const result = await call<HubRuntime>('runtime'); setRuntime(result); setActive(result.active ?? null); return result; }, [call]);
  const applyReportState = useCallback((value: HubReport, selected: boolean) => {
 if(singlePending.current?.id===value.reportId)singlePending.current.accepted=true;
    if (selected) { setReport(value); lastReport.current = value; }
    const decision = reportPollDecision(value, terminalSince.current, Date.now());
    if (isHubTerminal(value.status) || value.processExited) setStopRequested(current => current === value.reportId ? '' : current);
    else if (value.stopRequested) setStopRequested(value.reportId);
    terminalSince.current = decision.terminalSince;
    setReportMessage(decision.message);
    setActive(current => value.status === 'queued' ? current : isHubTerminal(value.status) && value.processExited ? (current?.reportId === value.reportId ? null : current) : value);
    return decision;
  }, []);
  const loadHistory = useCallback(async (cursor = '') => { try { const page = await call<HubPage<HubReport>>('reports', { scope: 'sidebar', limit: 20, cursor }); setHistory(old => ({ ...page, items: cursor ? [...old.items, ...page.items] : page.items })); setHistoryError(''); } catch (e) { setHistoryError(problem(e)); } }, [call]);
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
    setError('');
    void (async () => {
      try { const list = await call<{ items: HubTarget[] }>('targets'); if (!live) return; setTargets(list.items ?? []); const s = await call<Settings>('settings'); if (live) setSettings(s);
 const stats=await call<BankStats>('reference-info');if(live)setBankStats(stats); }
      catch (e) { if (live) setError(problem(e)); }
    })();
    return () => { live = false; };
  }, [boot, call, loadRuntime, loadHistory]);

  useEffect(() => {
    const request = ++currentTargetRequest.current;
    if (!boot || !input.targetId) { setContext(null); setModels([]); setModel(''); setTargetBusy(false); return; }
    setTargetBusy(true); setContext(null); setModels([]); setModel(''); setModelNotice(''); setContextError('');
    void (async () => {
      try {
        const ctx = await call<HubContext>('context', { targetId: input.targetId, groupId: input.groupId });
        if (request !== currentTargetRequest.current) return;
        setContext(ctx);
        if (!input.groupId && ctx.groupId) setInput(old => old.targetId === ctx.targetId && !old.groupId ? { ...old, groupId: ctx.groupId } : old);
        if (!ctx.canRun) { setContextError(ctx.reason || '此账号当前不能检测，请检查账号和分组配置。'); return; }
        const list = await call<{ models: (string | { id: string })[] }>('models', { targetId: ctx.targetId, groupId: ctx.groupId, expectedContext: ctx.expectedContext });
        if (request !== currentTargetRequest.current) return;
        const ids = Array.from(new Set((list.models ?? []).map(m => typeof m === 'string' ? m : m.id).filter(Boolean)));
        const selection = selectHubDefaultModel(ids, ctx.groupName || input.groupName || '');
        setModels(ids); setModel(selection.model); setModelNotice(selection.notice);
      } catch (e) { if (request === currentTargetRequest.current) setContextError(problem(e)); }
      finally { if (request === currentTargetRequest.current) setTargetBusy(false); }
    })();
    return () => { currentTargetRequest.current++; };
  }, [boot, input.targetId, input.groupId, targetChange, call]);

  const askOne=async (payload:SingleAskPayload,signal:AbortSignal):Promise<Metrics>=>{
    if(!context?.canRun || !model || targetBusy)throw new Error('请先选择可用分组、账号和模型，再发送问题。');
    const key=JSON.stringify({targetId:context.targetId,groupId:context.groupId,model,payload});
    let request=singlePending.current;
    if(request && request.key!==key) {if(!request.accepted)throw new Error(`上次单问单答的受理尚未确认，请恢复原问题后重试确认，或从左侧查看记录 ${request.id}；本次没有另发问题。`);request=null;}
    if(!request){const id=newHubID();request={key,id,input:{reportId:id,targetId:context.targetId,groupId:context.groupId,model,expectedContext:context.expectedContext,singleAsk:payload},accepted:false,failed:false};singlePending.current=request;}
    try {
      if(request.failed){
        const old=await call<HubReport>('report',{reportId:request.id});
        if(isHubTerminal(old.status)) {
          const page=await call<HubPage<HubItem>>('items',{reportId:request.id,limit:5});const item=page.items?.[0],metrics=object(object(item?.result?.evidence).single_ask);
          if(old.status==='done' && !item?.result?.error && !metrics.Err && Object.keys(metrics).length) {if(singlePending.current===request)singlePending.current=null;return metrics as Metrics;}
          request.retry??={id:newHubID(),expected:old.retry?.id??''};
          await call('retry',{reportId:request.id,retryId:request.retry.id,expectedRetryId:request.retry.expected,itemIds:['custom:'+request.id]});request.retry=undefined;
        }
        request.failed=false;
      }else await call('start',request.input);
    }catch(error){
      if(error instanceof HubRequestError && error.status>=400 && error.status<500){request.retry=undefined;if(!request.accepted && error.details.operationResult==='not_started' && singlePending.current===request)singlePending.current=null;}
      throw error;
    }
    request.accepted=true;void loadHistory();
    while(alive.current&&!signal.aborted){
      if(!visibleRef.current){await new Promise(resolve=>setTimeout(resolve,800));continue;}
      const report=await call<HubReport>('report',{reportId:request.id});
      if(signal.aborted)break;
      applyReportState(report,false);
      if(isHubTerminal(report.status)){
        const page=await call<HubPage<HubItem>>('items',{reportId:request.id,limit:5});const item=page.items?.[0],metrics=object(object(item?.result?.evidence).single_ask);
        if(item?.result?.error || metrics.Err || ['cancelled','interrupted'].includes(report.status) || !Object.keys(metrics).length){request.failed=true;throw new Error(`${item?.result?.error||metrics.Err||'问答已停止或未完整返回'}；本次记录 ${request.id} 已保留，再次发送同一问题只补测本记录，不新建报告。`);}
        if(singlePending.current===request)singlePending.current=null;void loadHistory();return metrics as Metrics;
      }
      await new Promise(resolve=>setTimeout(resolve,800));
    }
    throw new Error(`页面已收起或离开，问答记录 ${request.id} 仍可从左侧查看；不会自动重发。`);
  };
  const openReport = useCallback(async (id: string) => {
    const same = lastReport.current?.reportId === id;
    const request = ++reportRequest.current; itemReader.current = null; setItemBusy(false); setItemError(''); setReportLookup(id);
    if (!same) { setRetryPending(null); setReport(null); lastReport.current = null; setItemsComplete(false); setItems([]); setStopRequested(''); terminalSince.current = null; }
    setError(''); setNotice(''); if (!same) setReportMessage(''); setView('report');
    try { const result = await call<HubReport>('report', { reportId: id }); if (request !== reportRequest.current) return; applyReportState(result, true); await loadItems(id); }
    catch (e) { if (request === reportRequest.current) { setError(`读取最新报告未完成：${problem(e)}${same ? '。已显示的回答仍可查看，请点击核查运行状态；不会重新检测。' : `。请按报告编号 ${id} 重试读取，不要重复新建。`}`); } }
    finally { if (same && request === reportRequest.current) setPollEpoch(n => n + 1); }
  }, [call, loadItems, applyReportState]);
  useEffect(()=>{if(boot?.target.reportId && /^[0-9a-f]{32}$/.test(boot.target.reportId)) void openReport(boot.target.reportId);},[boot,openReport]);
  useEffect(() => {
    if (!boot || !visible) return;
    void loadHistory();
    void loadRuntime().catch(e => setError(problem(e)));
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

  const start = async () => {
    if (!context || !model || !sections.length || targetBusy) return;
    setError(''); setNotice(''); setBusy(true);
    const request = pending ?? { reportId: newHubID(), payload: { targetId: context.targetId, groupId: context.groupId, model, selectedSectionIds: sections, selectedQuestionIds: [], expectedContext: context.expectedContext } };
    setPending(request); const targetRequest = currentTargetRequest.current;
    try { await call('start', { ...request.payload, reportId: request.reportId }); setPending(null); if (targetRequest === currentTargetRequest.current) await openReport(request.reportId); else applyReportState(await call<HubReport>('report', { reportId: request.reportId }), false); }
    catch (e) { setError(problem(e)); setNotice(`本次报告编号：${request.reportId}。请先查询受理结果，不要重复新建；可能已有请求计费。`); }
    finally { setBusy(false); }
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
  const findArchive = async (cursor = '') => { setError(''); try { const page = await call<HubPage<HubReport>>('reports', { scope: 'archive', from, to, targetId: archiveTarget, cursor, limit: 20 }); setArchive(page); } catch (e) { setError(problem(e)); } };
  const navigate = (next: View) => { if (view === 'settings' && next !== 'settings' && dirty.current && !confirm('检测设置有未保存的修改，确定离开并放弃吗？')) return; setView(next); setError(''); setNotice(''); if (next === 'archive') void findArchive(); };

  const groups = Array.from(new Map(targets.flatMap(target => target.groups ?? []).map(group => [group.id, group])).values()).sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
  const groupAccounts = targets.filter(target => target.groups?.some(group => group.id === input.groupId));
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
            return <button type="button" key={row.reportId} data-report-id={row.reportId} className={'hist-item hub-nav-button' + (report?.reportId === row.reportId && view === 'report' ? ' active' : '')} onClick={() => void openReport(row.reportId)}><div className="history-model">{row.accountName}{row.accountId ? ` #${row.accountId}` : ''}</div><div className="history-model">{row.model}</div><div className="history-meta"><b className="hist-badge" title={typeof totalScore === 'number' ? '总分' : '推理分'}>{typeof score === 'number' ? `${score} 分` : reportStatus(row.status)}</b><time className="history-time">{displayTime(row.createdAtMs)}</time></div>{typeof score === 'number' && row.status !== 'done' && <div className="history-model">{reportStatus(row.status)}</div>}</button>;
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
      {error && <p role="alert" className="hub-error hub-alert">{error}</p>}{notice && <p role="status" className="hub-note">{notice}</p>}
      {view === 'settings' && boot && <HubSettings call={call} onDirtyChange={onDirtyChange} />}
      {view === 'new' && boot && <div className="opening"><AuditHero /><section className="hero-card connection-card hub-card"><div className="section-heading"><h1>新建账号检测</h1><span className="muted">只预填，不自动检测</span></div><p className="muted">选择分组和账号后读取模型列表，点击开始才会发送检测题目，不修改主站账号或调度设置。</p>
        <div className="connection-grid"><div className="field"><label>分组<Select value={input.groupId ?? ''} onChange={e => resetTarget({ groupId: e.target.value })}><option value="">请选择分组</option>{groups.map(group => <option key={group.id} value={group.id}>{`${group.name}${groups.some(other => other.id !== group.id && other.name === group.name) ? ` #${group.id}` : ''}`}</option>)}</Select></label></div><div className="field"><label>账号<Select value={input.targetId ?? ''} disabled={!input.groupId} onChange={e => resetTarget({ groupId: input.groupId, targetId: e.target.value })}><option value="">{input.groupId ? '请选择账号' : '请先选择分组'}</option>{groupAccounts.map(t => <option key={t.targetId} value={t.targetId}>{`${t.accountName}${t.accountId ? ` #${t.accountId}` : ''}`}</option>)}</Select></label></div></div>
        {groups.length === 0 && <p className="muted">尚无可选分组，请核对当前工作区的主站连接后重新打开检测入口；此时没有开始检测。</p>}
        {input.groupId && groupAccounts.length === 0 && <p className="muted">此分组没有可选账号，请选择其他分组或检查主站分组账号清单。</p>}
        {targetBusy && <p role="status">正在核对账号并读取可用模型；没有发送检测题目。</p>}{contextError && <p role="alert" className="hub-error">{contextError}</p>}
        {context && <><p className="hub-target-line"><strong>{context.accountName}</strong> · {context.groupName} · {context.baseURL} · {context.protocol}</p><div className="connection-grid"><div className="field"><label>API 密钥<input readOnly value="使用此账号已配置的密钥" /></label><p className="muted">页面不显示或保存密钥；执行时由服务器读取。</p></div><div className="field"><label htmlFor="model">目标模型</label><ModelPicker id="model" value={model} options={models.length?models:bankStats?.supported_models??[]} loading={targetBusy} disabled={targetBusy||Boolean(pending)} onChange={value=>{setModel(value);setPending(null);}} onRefresh={()=>setTargetChange(n=>n+1)} refreshDisabled={targetBusy||Boolean(pending)} placeholder="可选择或输入模型名称" describedBy="model-help"/><small id="model-help">可选择或输入自定义名称，请求时原样发送；没有对应基线时不代表已验证型号。</small>{modelNotice && <p className="muted">{modelNotice}</p>}</div></div><button type="button" className="btn-secondary" disabled={targetBusy} onClick={() => setTargetChange(n => n + 1)}>重新读取账号与模型列表</button></>}
        <details className="fold hub-section-picker" open><summary>本次检测内容</summary><p className="muted">选择分类后使用该分类中全部已启用题目；请在设置里增删或禁用单题。工具、缓存与指纹可能发送多次请求。缓存默认勾选，执行原版六轮检测，会增加请求；可取消。在设置中手动禁用或删除的题目仍跳过。</p><p className="muted">模型自报检查位于推理题中，默认5分，只对GPT-6系列执行；可在设置里禁用或删除，不修改主站。</p><div className="hub-sections">{HUB_KINDS.map(section => <label key={section.value} className="hub-checkbox"><input type="checkbox" checked={sections.includes(section.value)} disabled={Boolean(pending)} onChange={e => { setSections(old => e.target.checked ? [...old, section.value] : old.filter(s => s !== section.value)); setPending(null); }} />{section.label}</label>)}</div></details>
        <p className="hub-note">点击开始后会向所选上游发送请求，可能产生费用；请先核对账号、模型和题目。检测任务默认{settings?.memoryLimitMiB ?? 256}MiB，整个检测子项目最多约0.5GB；报告保存后程序退出。</p>
        <div className="actions hub-wrap"><button type="button" className="btn-primary" disabled={busy || targetBusy || !context?.canRun || !model || !sections.length || runtime?.available !== true || runtime?.queueBlocked === true} onClick={() => void start()}>{busy ? '正在确认受理…' : pending ? '用原编号重试受理' : active || runtime?.busy || (runtime?.queuedCount ?? 0) > 0 ? '加入等待队列' : '开始检测'}</button>{pending && <button type="button" className="btn-secondary" onClick={() => void openReport(pending.reportId)}>查询本次受理结果</button>}<button type="button" className="btn-secondary" onClick={() => navigate('settings')}>设置与全部题目</button></div>
        <div className="bank-strip"><span><Icon name="layers"/><b>{bankStats?.fingerprint_count??'—'}</b> 个可测模型</span><Hint label="模型库说明" text="服务端在检测任务中检查模型基线更新。数字指纹仅对库内候选比较，行为参考不等于型号证明。"/></div>
      </section></div>}
      {view === 'new' && boot && <SingleAskPanel model={model} canRun={Boolean(context?.canRun && model && !targetBusy && !busy)} onAsk={askOne} />}
      {view === 'report' && report && <>
        <section className="hero-card">
          <div className="section-heading"><div><span className="step">报告</span><h2>按检测 ID 查看</h2></div><span className="muted">本站检测报告</span></div>
          <form className="report-lookup" onSubmit={e => { e.preventDefault(); if (reportLookup.trim()) void openReport(reportLookup.trim()); }}><label className="sr-only" htmlFor="hub-report-id">检测 ID</label><input id="hub-report-id" value={reportLookup} placeholder="粘贴检测 ID" onChange={e => setReportLookup(e.target.value)} /><button type="submit" className="btn-primary">查看报告</button></form>
          <h1 className="hero-title">{report.model}</h1>
          <p className="report-meta">{new Date(report.createdAtMs).toLocaleString('zh-CN')} · {reportStatus(report.status)} · 账号：{report.accountName} · 分组：{report.groupName}</p>
          <ReportShare id={report.reportId} identityLabel="报告编号" reportHref={(()=>{if(typeof window==='undefined')return '';const url=new URL(window.parent.location.href);url.searchParams.set('detectorReport',report.reportId);return url.href;})()} actions={<>            <button type="button" className="btn-secondary" onClick={() => void checkRuntime()}>核查运行状态</button>
            {(!isHubTerminal(report.status) || !report.processExited) && <button type="button" className="btn-secondary" disabled={busy} onClick={() => void stop()}>{report.diagnostic ? '停止检测并重试保存状态' : stopRequested === report.reportId ? '正在停止…' : report.status === 'queued' ? '取消等待' : report.processExited ? '停止检测并重试保存状态' : '停止检测'}</button>}
            <button type="button" className="btn-secondary" disabled={retryBusy || (!retryPending && (!itemsComplete || !isHubTerminal(report.status) || report.processExited !== true || !items.some(canRetryHubItem)))} onClick={() => void retryItems(items.filter(canRetryHubItem).map(item => item.itemId))}>{retryBusy ? '正在确认补测…' : retryPending ? '用原操作确认补测受理' : `补测失败项（${items.filter(canRetryHubItem).length}）`}</button>
            <button type="button" className="btn-secondary" onClick={() => { setPending(null); setView('new'); setInput({ targetId: report.targetId, groupId: report.groupId }); setTargetChange(n => n + 1); }}>重新检测（新任务）</button>
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
        <HubReportContent key={report.reportId} report={report} items={items} complete={itemsComplete} call={call} onRetry={ids => void retryItems(ids)} retryDisabled={retryBusy || !itemsComplete || !isHubTerminal(report.status) || report.processExited !== true || Boolean(retryPending)} />
      </>}
      {view === 'docs' && <><p className="hub-note">以下保留原版独立检测服务的接口说明，本站未开放这些匿名公共接口；本站检测请使用当前页面的账号入口。查看说明不会发起检测或修改主站。</p><UpstreamApiDocs referenceOnly /></>}
      {view === 'archive' && boot && <section className="hero-card hub-card"><h1>查找更早记录</h1><p className="muted">历史报告长期保存在磁盘，按新加坡日期筛选；默认查询最近30天。不会重新调用模型。</p><div className="hub-form-grid"><div className="field"><label>开始日期<input type="date" value={from} onChange={e => setFrom(e.target.value)} /></label></div><div className="field"><label>结束日期<input type="date" value={to} onChange={e => setTo(e.target.value)} /></label></div><div className="field"><label>账号<Select value={archiveTarget} onChange={e => setArchiveTarget(e.target.value)}><option value="">全部账号</option>{targets.map(t => <option key={t.targetId} value={t.targetId}>{t.accountName}</option>)}</Select></label></div></div><button type="button" className="btn-primary" onClick={() => void findArchive()}>查找报告</button><div className="hub-search field"><label>按报告编号查找<input value={reportLookup} onChange={e => setReportLookup(e.target.value)} /></label><button type="button" className="btn-secondary" onClick={() => void openReport(reportLookup.trim())}>打开此报告</button></div>{archive.items.map(row => <div className="hub-question-row" key={row.reportId}><div><strong>{row.accountName} · {row.model}</strong><p className="muted">{displayTime(row.createdAtMs, true)} · {row.groupName} · {reportStatus(row.status)}</p></div><button type="button" className="btn-secondary" onClick={() => void openReport(row.reportId)}>查看报告</button></div>)}{archive.items.length === 0 && <p className="muted">此范围没有报告，可调整日期或直接输入报告编号。</p>}{archive.nextCursor && <button type="button" className="btn-secondary" onClick={() => void findArchive(archive.nextCursor)}>下一页报告</button>}</section>}
    </div></main>
  </div>;
}
