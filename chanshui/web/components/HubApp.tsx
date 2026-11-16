"use client";
import { useCallback, useEffect, useRef, useState } from 'react';
import Select from './Select';
import HubSettings from './HubSettings';
import { HubProbeEvidence, HubReportSummary, readableReportError } from './ProbeEvidence';
import { createHubBridge, HubRequestError, type HubBoot } from '../lib/hub-bridge';
import { HUB_KINDS, isHubTerminal, newHubID, reportPollDecision, reportStatus, selectHubDefaultModel, sidebarDayLabel } from '../lib/hub-policy';
import type { HubCall, HubContext, HubItem, HubPage, HubReport, HubRuntime, HubSettings as Settings, HubTarget, HubTargetInput } from '../lib/hub-types';

type View = 'new' | 'report' | 'settings' | 'archive';
const problem = (error: unknown) => error instanceof Error ? error.message : '读取检测结果时中断，原因尚未确认；请重试读取，不要重复新建检测。';
const displayTime = (value: number, full = false) => new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Singapore', ...(full ? { year: 'numeric', month: '2-digit', day: '2-digit' } as const : {}), hour: '2-digit', minute: '2-digit', hour12: false }).format(value);
function CheckedMemory({ report }: { report?: HubReport | null }) {
  return report?.taskMemoryMaxBytes ? <p className="muted">本次启动已核验：任务最多 {(report.taskMemoryMaxBytes / 1048576).toFixed(0)}MiB；整个检测子项目最多 {((report.sliceMemoryMaxBytes ?? 500000000) / 1000000).toFixed(0)}MB；{report.swapMaxBytes === 0 ? '交换内存已禁用' : '交换内存上限待核查'}。{report.memoryPeakBytes ? `已记录峰值 ${(report.memoryPeakBytes / 1048576).toFixed(1)}MiB。` : ''}</p> : null;
}


export function HubItemCard({ item, call, reportId, reportStatus: parentStatus }: { item: HubItem; call: HubCall; reportId: string; reportStatus?: string }) {
  const [image, setImage] = useState(''), [error, setError] = useState('');
  const result = item.result ?? {};
  const judgment = typeof result.judgment === 'object' ? result.judgment : undefined;
  const earned = result.earned ?? judgment?.earned;
  const max = result.max ?? judgment?.max ?? item.definition.maxScore;
  const judgmentStatus = typeof result.judgment === 'string' ? result.judgment : judgment?.status;
  const incomplete = isHubTerminal(parentStatus ?? '') && ['queued', 'running'].includes(item.status) && !['not_asked', 'not_applicable'].includes(judgmentStatus ?? '');
  const itemState = judgmentStatus === 'not_asked' ? '本次未提问' : judgmentStatus === 'not_applicable' ? '不适用' : incomplete ? '未完成' : judgmentStatus === 'unreviewed' ? '待人工查看' : judgmentStatus === 'unknown' ? '尚未确认' : reportStatus(item.status);
  return <section className="result-card hub-result"><div className="rc-head"><h3>{item.definition.title}</h3><span>{itemState}{!incomplete && earned !== null && earned !== undefined ? ` · ${earned}/${max}` : ''}</span></div>
    <details className="fold" open><summary>本次实际题目</summary><pre>{item.prompt || (item.imageAvailable ? '题面在图片中' : '使用本项固定检测流程')}</pre></details>
    {item.imageAvailable && <div><button type="button" className="btn-secondary" onClick={async () => { try { const data = await call<{ dataURL: string }>('image', { reportId, itemId: item.itemId }); setImage(data.dataURL); setError(''); } catch (e) { setError(problem(e)); } }}>查看本次题目图片</button>{image && <img src={image} className="hub-image" alt="本次检测实际使用的题目图片" />}</div>}
    {typeof result.answer === 'string' && <details className="fold" open><summary>模型回答</summary><pre>{result.answer || '尚未返回正文'}</pre></details>}
    {item.expected && <details className="fold"><summary>检测端参考答案</summary><p className="muted">仅用于本地判题或人工核对，不发送给被测模型自评。</p><pre>{item.expected}</pre></details>}
    {(result.judgment === 'unreviewed' || judgment?.status === 'unreviewed' || item.definition.judge.type === 'manual' && ['iq', 'vision'].includes(item.definition.kind)) && <p className="muted">此题只供人工查看，不参与自动得分。</p>}
    {(result.half === true || judgment?.half === true) && <p className="muted">本题半分只展示卷面，不进入推理通过分子。</p>}
    {typeof result.thinkingText === 'string' && result.thinkingText && <details className="fold"><summary>实际返回的可见推导</summary><pre>{result.thinkingText}</pre></details>}
    {judgment?.reason && <p className="hub-note">{judgment.reason}</p>}
    {(result.error || error) && <p className="hub-error" role="alert">{String(result.error || error)}</p>}
    {incomplete ? <p className="muted">{item.status === 'queued' ? '本次检测已结束，此项未执行。' : '本次检测已结束，此项未完成；已发出的请求可能已计费。'}</p> : <HubProbeEvidence item={item} />}
    {result.evidence !== undefined && <details className="fold"><summary>查看原始检测证据</summary><pre>{JSON.stringify(result.evidence, null, 2)}</pre></details>}
    {result.metrics !== undefined && <details className="fold"><summary>用量和响应记录</summary><pre>{JSON.stringify(result.metrics, null, 2)}</pre></details>}
    {Object.keys(result).filter(key => !['answer', 'earned', 'max', 'half', 'judgment', 'error', 'metrics', 'evidence'].includes(key)).length > 0 && <details className="fold"><summary>其他已保存结果</summary><pre>{JSON.stringify(Object.fromEntries(Object.entries(result).filter(([key]) => !['answer', 'earned', 'max', 'half', 'judgment', 'error', 'metrics', 'evidence'].includes(key))), null, 2)}</pre></details>}
  </section>;
}

export default function HubApp({ initialView = 'new' }: { initialView?: View }) {
  const bridge = useRef<ReturnType<typeof createHubBridge> | null>(null);
  const [boot, setBoot] = useState<HubBoot | null>(null), [visible, setVisible] = useState(true), [view, setView] = useState<View>(initialView);
  const [input, setInput] = useState<HubTargetInput>({}), [targetChange, setTargetChange] = useState(0);
  const [runtime, setRuntime] = useState<HubRuntime | null>(null), [targets, setTargets] = useState<HubTarget[]>([]), [context, setContext] = useState<HubContext | null>(null);
  const [models, setModels] = useState<string[]>([]), [model, setModel] = useState(''), [modelNotice, setModelNotice] = useState(''), [targetBusy, setTargetBusy] = useState(false);
  const [contextError, setContextError] = useState(''), [error, setError] = useState(''), [notice, setNotice] = useState('');
  const [sections, setSections] = useState<string[]>(HUB_KINDS.filter(k => k.value !== 'cache').map(k => k.value)), [settings, setSettings] = useState<Settings | null>(null);
  const [history, setHistory] = useState<HubPage<HubReport>>({ items: [] }), [historyError, setHistoryError] = useState('');
  const [report, setReport] = useState<HubReport | null>(null), [active, setActive] = useState<HubReport | null>(null), [reportMessage, setReportMessage] = useState('');
  const [items, setItems] = useState<HubPage<HubItem>>({ items: [] }), [itemCursor, setItemCursor] = useState(''), [previousCursors, setPreviousCursors] = useState<string[]>([]), [itemBusy, setItemBusy] = useState(false);
  const [busy, setBusy] = useState(false), [pending, setPending] = useState<{ reportId: string; payload: Record<string, unknown> } | null>(null);
  const [archive, setArchive] = useState<HubPage<HubReport>>({ items: [] }), [from, setFrom] = useState(''), [to, setTo] = useState(''), [archiveTarget, setArchiveTarget] = useState(''), [reportLookup, setReportLookup] = useState('');
  const dirty = useRef(false);
  const onDirtyChange = useCallback((value: boolean) => { dirty.current = value; bridge.current?.setDirty(value); }, []);
  const bootReceived = useRef(false), reportRequest = useRef(0);
  const currentTargetRequest = useRef(0), lastReport = useRef<HubReport | null>(null), latestView = useRef(view), latestCursor = useRef(itemCursor), terminalSince = useRef<number | null>(null);
  lastReport.current = report; latestView.current = view; latestCursor.current = itemCursor;
  const call: HubCall = useCallback(<T,>(op: string, params: Record<string, unknown> = {}): Promise<T> => bridge.current ? bridge.current.call<T>(op, params) : Promise.reject(new HubRequestError('请从调度站的模型检测按钮打开此页面。')), []);

  useEffect(() => {
    const instance = createHubBridge(value => { bootReceived.current = true; setBoot(value); setVisible(value.visible); setInput(value.target); setTargetChange(n => n + 1); }, value => { setInput(value); setTargetChange(n => n + 1); if (!dirty.current || confirm('检测设置有未保存的修改，确定切换到新账号并放弃吗？')) setView('new'); setPending(null); setContext(null); setModels([]); setModel(''); setError(''); setNotice(''); }, setVisible, warning => setError(readableReportError(warning)));
    bridge.current = instance;
    const standalone = setTimeout(() => { if (bootReceived.current) return; setError(old => old || '请从调度站中的“模型检测”按钮打开此页面。页面本身不会启动检测。'); }, 10000);
    return () => { clearTimeout(standalone); instance.dispose(); bridge.current = null; };
  }, []);
  const loadRuntime = useCallback(async () => { const result = await call<HubRuntime>('runtime'); setRuntime(result); setActive(result.active ?? null); return result; }, [call]);
  const applyReportState = useCallback((value: HubReport, selected: boolean) => {
    if (selected) { setReport(value); lastReport.current = value; }
    const decision = reportPollDecision(value, terminalSince.current, Date.now());
    terminalSince.current = decision.terminalSince;
    setReportMessage(decision.message);
    setActive(current => isHubTerminal(value.status) && value.processExited ? (current?.reportId === value.reportId ? null : current) : value);
    return decision;
  }, []);
  const loadHistory = useCallback(async (cursor = '') => { try { const page = await call<HubPage<HubReport>>('reports', { scope: 'sidebar', limit: 20, cursor }); setHistory(old => ({ ...page, items: cursor ? [...old.items, ...page.items] : page.items })); setHistoryError(''); } catch (e) { setHistoryError(problem(e)); } }, [call]);
  const loadItems = useCallback(async (id: string, cursor = '') => {
    const request = reportRequest.current;
    const current = () => lastReport.current?.reportId === id && latestCursor.current === cursor && reportRequest.current === request;
    if (!current()) return;
    setItemBusy(true); setError('');
    try {
      const page = await call<HubPage<HubItem>>('items', { reportId: id, cursor, limit: 5 });
      if (current()) setItems(page);
    } catch (e) { if (current()) setError(problem(e)); }
    finally { if (current()) setItemBusy(false); }
  }, [call]);
  const changeItemsPage = (id: string, cursor: string) => { latestCursor.current = cursor; setItemCursor(cursor); void loadItems(id, cursor); };

  useEffect(() => {
    if (!boot) return;
    let live = true;
    setError('');
    void (async () => {
      try { const list = await call<{ items: HubTarget[] }>('targets'); if (!live) return; setTargets(list.items ?? []); const s = await call<Settings>('settings'); if (live) setSettings(s); }
      catch (e) { if (live) setError(problem(e)); }
    })();
    return () => { live = false; };
  }, [boot, call, loadRuntime, loadHistory]);

  useEffect(() => {
    if (!boot || !input.targetId) { setContext(null); setModels([]); setModel(''); return; }
    const request = ++currentTargetRequest.current;
    setTargetBusy(true); setContext(null); setModels([]); setModel(''); setModelNotice(''); setContextError('');
    void (async () => {
      try {
        const ctx = await call<HubContext>('context', { targetId: input.targetId, groupId: input.groupId });
        if (request !== currentTargetRequest.current) return;
        setContext(ctx);
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

  const openReport = useCallback(async (id: string) => {
    const request = ++reportRequest.current; setReport(null); lastReport.current = null; latestCursor.current = ''; setItemBusy(false);
    setError(''); setNotice(''); setReportMessage(''); setView('report'); setItems({ items: [] }); setItemCursor(''); setPreviousCursors([]); terminalSince.current = null;
    try { const result = await call<HubReport>('report', { reportId: id }); if (request !== reportRequest.current) return; applyReportState(result, true); await loadItems(id); }
    catch (e) { if (request === reportRequest.current) { setReport(null); lastReport.current = null; setError(problem(e)); } }
  }, [call, loadItems, applyReportState]);
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
        if (selected && latestView.current === 'report') await loadItems(pollId, latestCursor.current);
        if (!live || request !== reportRequest.current) return;
        if (decision.poll) timer = setTimeout(() => { void poll(); }, 2000);
        else { if (isHubTerminal(value.status) && value.processExited) void loadHistory(); }
      } catch (e) {
        if (!live || request !== reportRequest.current) return;
        if (++failures < 3) timer = setTimeout(() => { void poll(); }, 2000);
        else { setError(problem(e)); setReportMessage('连续三次无法读取运行状态，已停止自动读取。请点击核查运行状态；不会自动重复检测。'); }
      }
    };
    void poll();
    return () => { live = false; if (timer) clearTimeout(timer); };
  }, [boot, visible, pollId, call, loadItems, loadHistory, applyReportState]);

  const start = async () => {
    if (!context || !model || !sections.length || targetBusy) return;
    setError(''); setNotice(''); setBusy(true);
    const request = pending ?? { reportId: newHubID(), payload: { targetId: context.targetId, groupId: context.groupId, model, selectedSectionIds: sections, selectedQuestionIds: [], expectedContext: context.expectedContext } };
    setPending(request); const targetRequest = currentTargetRequest.current;
    try { await call('start', { ...request.payload, reportId: request.reportId }); setPending(null); if (targetRequest === currentTargetRequest.current) await openReport(request.reportId); else applyReportState(await call<HubReport>('report', { reportId: request.reportId }), false); }
    catch (e) { setError(problem(e)); setNotice(`本次报告编号：${request.reportId}。请先查询受理结果，不要重复新建；可能已有请求计费。`); }
    finally { setBusy(false); }
  };
  const stop = async () => { if (!report) return; setBusy(true); setError(''); try { await call('cancel', { reportId: report.reportId }); await openReport(report.reportId); } catch (e) { setError(problem(e)); } finally { setBusy(false); } };
  const checkRuntime = async () => {
    const request = ++reportRequest.current, id = lastReport.current?.reportId;
    setError(''); setItemBusy(false);
    try {
      await loadRuntime();
      if (request !== reportRequest.current || !id) return;
      const value = await call<HubReport>('report', { reportId: id });
      if (request !== reportRequest.current) return;
      applyReportState(value, true);
      await loadItems(id, latestCursor.current);
    } catch (e) { if (request === reportRequest.current) setError(problem(e)); }
  };
  const findArchive = async (cursor = '') => { setError(''); try { const page = await call<HubPage<HubReport>>('reports', { scope: 'archive', from, to, targetId: archiveTarget, cursor, limit: 20 }); setArchive(page); } catch (e) { setError(problem(e)); } };
  const navigate = (next: View) => { if (view === 'settings' && next !== 'settings' && dirty.current && !confirm('检测设置有未保存的修改，确定离开并放弃吗？')) return; setView(next); setError(''); setNotice(''); if (next === 'archive') void findArchive(); };

  return <div className="hub-app">
    <aside className="sidebar hub-sidebar"><div className="sidebar-logo"><span className="logo-symbol">掺</span><span>模型检测</span></div><p className="muted hub-sidebar-caption">调度站账号检测 · 人工检阅</p><div className="hub-sidebar-nav"><button type="button" onClick={() => navigate('new')} className={view === 'new' ? 'active' : ''}>新建检测</button><button type="button" onClick={() => navigate('settings')} className={view === 'settings' ? 'active' : ''}>设置与全部题目</button><button type="button" onClick={() => navigate('archive')} className={view === 'archive' ? 'active' : ''}>查找更早记录</button></div>
      <div className="hub-history-heading"><strong>最近记录</strong><button type="button" onClick={() => void loadHistory()}>刷新</button></div><p className="muted">只显示今天和昨天；更早报告仍在磁盘，可按日期查找。</p><div className="hub-history-list">{(['今天', '昨天'] as const).map(day => { const rows = history.items.filter(row => sidebarDayLabel(row.createdAtMs) === day); return rows.length > 0 && <section key={day}><h3>{day}</h3>{rows.map(row => <button type="button" key={row.reportId} className={report?.reportId === row.reportId && view === 'report' ? 'active' : ''} onClick={() => void openReport(row.reportId)}><strong>{row.accountName}{row.accountId ? ` #${row.accountId}` : ''}</strong><span>{row.model}</span><small>{displayTime(row.createdAtMs)} · {reportStatus(row.status)}{row.groupName ? ` · ${row.groupName}` : ''}</small></button>)}</section>; })}{history.items.length === 0 && !historyError && <p className="muted">今天和昨天暂无记录。</p>}{history.nextCursor && <button type="button" className="hub-sidebar-more" onClick={() => void loadHistory(history.nextCursor)}>显示更多最近记录</button>}{historyError && <p role="alert" className="hub-error">{historyError}</p>}</div>
    </aside>
    <main className="main hub-main" id="main-content"><div className="page-shell"><header className="hub-page-heading"><p className="muted">此页只做检测和保存结果，不会修改主站账号或调度设置。</p><p className="muted">收起或关闭页面不会停止已开始的检测；需要中止请点击停止检测。</p></header>
      {!boot && <section className="hero-card hub-card"><h1>正在连接调度站…</h1><p>请从调度站中的模型检测入口打开此页。静态页面不会启动检测程序。</p></section>}
      {runtime && (!runtime.available || runtime.busy) && <section className="hero-card hub-card"><p className="hub-error" role="alert">{runtime.reason || (runtime.busy ? '当前已有检测正在执行，请等待后点击核查运行状态；仍可查看自己的历史和设置。' : '当前环境未启用硬内存限制，不能开始检测。请按部署说明配置后重试。')}</p><button type="button" className="btn-secondary" onClick={() => void checkRuntime()}>核查运行状态</button></section>}
      <CheckedMemory report={runtime?.active} />
      {active && (!isHubTerminal(active.status) || !active.processExited) && <div className="hub-note" role="status">当前任务：{active.accountName} · {active.model} · {reportStatus(active.status)}。切换账号不会改变此任务。<button type="button" className="hub-text-button" onClick={() => void openReport(active.reportId)}>查看该任务</button></div>}
      {error && <p role="alert" className="hub-error hub-alert">{error}</p>}{notice && <p role="status" className="hub-note">{notice}</p>}
      {view === 'settings' && boot && <HubSettings call={call} onDirtyChange={onDirtyChange} />}
      {view === 'new' && boot && <section className="hero-card connection-card hub-card"><div className="section-heading"><h1>新建账号检测</h1><span className="muted">只预填，不自动检测</span></div><div className="connection-grid"><div className="field"><label>账号<Select value={input.targetId ?? ''} onChange={e => { setInput({ targetId: e.target.value }); setTargetChange(n => n + 1); setPending(null); }}><option value="">请选择账号</option>{targets.map(t => <option key={t.targetId} value={t.targetId}>{t.accountName}{t.accountId ? ` #${t.accountId}` : ''}</option>)}</Select></label></div><div className="field"><label>分组<Select value={input.groupId ?? context?.groupId ?? ''} onChange={e => { setInput(old => ({ ...old, groupId: e.target.value })); setTargetChange(n => n + 1); setPending(null); }}><option value="">使用账号默认分组</option>{targets.find(t => t.targetId === input.targetId)?.groups?.map(group => <option key={group.id} value={group.id}>{group.name}</option>)}</Select></label></div></div>
        {targetBusy && <p role="status">正在核对账号并读取可用模型；没有发送检测题目。</p>}{contextError && <p role="alert" className="hub-error">{contextError}</p>}
        {context && <><p className="hub-target-line"><strong>{context.accountName}</strong> · {context.groupName} · {context.baseURL} · {context.protocol}</p><div className="connection-grid"><div className="field"><label>API 密钥<input readOnly value="使用此账号已配置的密钥" /></label><p className="muted">页面不显示或保存密钥；执行时由服务器读取。</p></div><div className="field"><label>检测模型<Select value={model} disabled={targetBusy || Boolean(pending) || models.length === 0} loading={targetBusy} onChange={e => { setModel(e.target.value); setPending(null); }}><option value="">请选择模型</option>{models.map(id => <option key={id} value={id}>{id}</option>)}</Select></label>{modelNotice && <p className="muted">{modelNotice}</p>}</div></div><button type="button" className="btn-secondary" disabled={targetBusy} onClick={() => setTargetChange(n => n + 1)}>重新读取账号与模型列表</button></>}
        <details className="fold" open><summary>本次检测内容</summary><p className="muted">选择分类后使用该分类中全部已启用题目；请在设置里增删或禁用单题。工具、缓存与指纹可能发送多次请求。</p><div className="hub-sections">{HUB_KINDS.map(section => <label key={section.value} className="hub-checkbox"><input type="checkbox" checked={sections.includes(section.value)} disabled={Boolean(pending)} onChange={e => { setSections(old => e.target.checked ? [...old, section.value] : old.filter(s => s !== section.value)); setPending(null); }} />{section.label}</label>)}</div></details>
        <p className="hub-note">点击开始后会向所选上游发送请求，可能产生费用；请先核对账号、模型和题目。检测任务默认{settings?.memoryLimitMiB ?? 256}MiB，整个检测子项目最多约0.5GB；报告保存后程序退出。</p>
        <div className="actions hub-wrap"><button type="button" className="btn-primary" disabled={busy || targetBusy || !context?.canRun || !model || !sections.length || runtime?.available !== true || runtime?.busy === true || Boolean(active && (!isHubTerminal(active.status) || !active.processExited))} onClick={() => void start()}>{busy ? '正在确认受理…' : pending ? '用原编号重试受理' : '开始检测'}</button>{pending && <button type="button" className="btn-secondary" onClick={() => void openReport(pending.reportId)}>查询本次受理结果</button>}<button type="button" className="btn-secondary" onClick={() => navigate('settings')}>设置与全部题目</button></div>
      </section>}
      {view === 'report' && report && <><section className="hero-card hub-card"><h1>{report.accountName} · {report.model}</h1><p className="muted">{report.groupName} · {displayTime(report.createdAtMs, true)} · {reportStatus(report.status)}</p><p className="muted">报告编号：{report.reportId}</p><CheckedMemory report={report} />{reportMessage && <p role="status" className="hub-note">{reportMessage}</p>}{report.diagnostic && <p className="hub-error">{report.diagnostic}</p>}{Boolean(report.error) && <p className="hub-error">{readableReportError(report.error) || '报告已记录异常，但原因尚未确认；请核查运行状态和已保存的逐项结果。'}</p>}<div className="actions hub-wrap"><button type="button" className="btn-secondary" onClick={() => void checkRuntime()}>核查运行状态</button>{(!isHubTerminal(report.status) || !report.processExited) && <button type="button" className="btn-secondary" disabled={busy} onClick={() => void stop()}>{report.processExited ? '停止检测并重试保存状态' : '停止检测'}</button>}<button type="button" className="btn-secondary" onClick={() => { setPending(null); setView('new'); setInput({ targetId: report.targetId, groupId: report.groupId }); setTargetChange(n => n + 1); }}>重新检测（新任务）</button></div>{report.summary && <><HubReportSummary summary={report.summary} /><details className="fold"><summary>查看原始结果摘要</summary><pre>{JSON.stringify(report.summary, null, 2)}</pre></details></>}<p className="muted">结果只供人工检阅，不会自动降权、停用或恢复账号。配置不同的报告不宜只比较总分。</p></section>
        <section className="hub-item-heading"><h2>本次题目与回答</h2><p className="muted">每页最多5项，图片按需读取；所有题目都保留在磁盘。失败、0分和未完成同样显示。</p><button type="button" className="btn-secondary" disabled={itemBusy} onClick={() => void loadItems(report.reportId, itemCursor)}>刷新本页回答</button></section>{itemBusy && <p role="status">正在读取本页回答…</p>}{items.items.map(item => <HubItemCard key={report.reportId + ':' + item.itemId} item={item} call={call} reportId={report.reportId} reportStatus={report.status} />)}{items.items.length === 0 && !itemBusy && <p className="muted">本页尚无已保存题目，请核查报告状态。</p>}<div className="actions hub-wrap"><button type="button" className="btn-secondary" disabled={itemBusy || !previousCursors.length} onClick={() => { const prev = previousCursors[previousCursors.length - 1]; setPreviousCursors(old => old.slice(0, -1)); changeItemsPage(report.reportId, prev); }}>上一页回答</button><span className="muted">第{previousCursors.length + 1}页</span><button type="button" className="btn-secondary" disabled={itemBusy || !items.nextCursor} onClick={() => { const next = items.nextCursor!; setPreviousCursors(old => [...old, itemCursor]); changeItemsPage(report.reportId, next); }}>下一页回答</button></div>
      </>}
      {view === 'archive' && boot && <section className="hero-card hub-card"><h1>查找更早记录</h1><p className="muted">历史报告长期保存在磁盘，按新加坡日期筛选；默认查询最近30天。不会重新调用模型。</p><div className="hub-form-grid"><div className="field"><label>开始日期<input type="date" value={from} onChange={e => setFrom(e.target.value)} /></label></div><div className="field"><label>结束日期<input type="date" value={to} onChange={e => setTo(e.target.value)} /></label></div><div className="field"><label>账号<Select value={archiveTarget} onChange={e => setArchiveTarget(e.target.value)}><option value="">全部账号</option>{targets.map(t => <option key={t.targetId} value={t.targetId}>{t.accountName}</option>)}</Select></label></div></div><button type="button" className="btn-primary" onClick={() => void findArchive()}>查找报告</button><div className="hub-search field"><label>按报告编号查找<input value={reportLookup} onChange={e => setReportLookup(e.target.value)} /></label><button type="button" className="btn-secondary" onClick={() => void openReport(reportLookup.trim())}>打开此报告</button></div>{archive.items.map(row => <div className="hub-question-row" key={row.reportId}><div><strong>{row.accountName} · {row.model}</strong><p className="muted">{displayTime(row.createdAtMs, true)} · {row.groupName} · {reportStatus(row.status)}</p></div><button type="button" className="btn-secondary" onClick={() => void openReport(row.reportId)}>查看报告</button></div>)}{archive.items.length === 0 && <p className="muted">此范围没有报告，可调整日期或直接输入报告编号。</p>}{archive.nextCursor && <button type="button" className="btn-secondary" onClick={() => void findArchive(archive.nextCursor)}>下一页报告</button>}</section>}
    </div></main>
  </div>;
}
