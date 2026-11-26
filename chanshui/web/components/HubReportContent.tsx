"use client";
import { useEffect, useRef, useState, type ReactNode } from 'react';
import ResultCards from './ResultCards';
import HubCrossCheckCard from './HubCrossCheckCard';
import HubInjectionDetail from './HubInjectionDetail';
import {SingleAnswer} from './SingleAskPanel';
import {object} from '../lib/hub-report-view';
import type {Metrics} from '../lib/api';
import ProbeTimeline from './ProbeTimeline';
import HubQuestionDetails from './HubQuestionDetails';
import { HubProbeEvidence } from './ProbeEvidence';
import { hubReportView, itemJudgment, canRetryHubItem, hasOriginalItemDTO, isVerifiedHubReport, reportVersion } from '../lib/hub-report-view';
import { isHubTerminal, reportFinishConfirmed } from '../lib/hub-policy';
import { CROSS_UNKNOWN_ERROR, INJECTION_NOTE, usablePoints, type CrossView } from '../lib/hub-crosscheck';
import { fingerprintState } from '../lib/report-view';
import { boundaryReviewText, knowledgeDateComparison } from '../lib/knowledge-dates';
import type { HubCall, HubCrossCheck, HubItem, HubReport } from '../lib/hub-types';

type CrossState = {
  version: string; op: number; state: 'idle' | 'loading' | 'ok' | 'error' | 'stale';
  attempts: number; data?: HubCrossCheck; error?: string;
};
const crossBackoff = [1000, 2000, 4000] as const;

export default function HubReportContent({
  report, items, complete, readFailed, call, onRetry, retryDisabled,
  itemsReadVersion, retryHold, readHold, onRefresh,
}: {
  report: HubReport; items: HubItem[]; complete: boolean; readFailed?: boolean; call: HubCall;
  onRetry?: (ids: string[]) => void; retryDisabled?: boolean;
  itemsReadVersion: string; retryHold: boolean; readHold: boolean; onRefresh: (expectedReportId?: string) => Promise<void>;
}) {
  // An old persisted terminal status is not confirmation that this round exited or saved.
  const confirmed = report.executionPhase ? reportFinishConfirmed(report) : isHubTerminal(report.status);
  const presentationStatus = !confirmed && isHubTerminal(report.status) ? 'running' : report.status;
  const view = hubReportView({...report, status: presentationStatus}, items, complete, readFailed);
  const verified = isVerifiedHubReport(report);
  const currentVersion = reportVersion(report);
  const singleAsk = object(report.summary).singleAsk === true;
  const retryActive = Boolean(report.retry) && ['queued', 'running'].includes(report.status);
  const itemsReady = complete && itemsReadVersion === currentVersion;
  // The saved total can arrive before the item judgments for this report version.
  const preserveTotalScore = Boolean(view.cacheView?.part?.applicable &&
    (!itemsReady || view.groups.cache?.some(item => itemJudgment(item).source === 'manual')));
  const crossReady = !singleAsk && reportFinishConfirmed(report) && !retryActive && !retryHold && !readHold && itemsReady;

  const [cross, setCross] = useState<CrossState>({ version: '', op: 0, state: 'idle', attempts: 0 });
  const opSeq = useRef(0);
  const busyOp = useRef(0);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);
  const live = useRef({ version: currentVersion, crossReady });
  live.current = { version: currentVersion, crossReady };
  const [wake, setWake] = useState(0);
  const [manualRefresh, setManualRefresh] = useState(false);
  const effective = cross.version === currentVersion ? cross : { state: 'idle' as const, attempts: 0 };

  const own = (op: number, next: Omit<CrossState, 'op'>) => {
    setCross(existing => existing.op === op ? { ...next, op } : existing);
  };
  const finish = (op: number) => {
    if (busyOp.current === op) busyOp.current = 0;
    setWake(n => n + 1);
  };
  const loadCross = async (used: number) => {
    const op = ++opSeq.current;
    busyOp.current = op;
    const version = currentVersion;
    const updated = report.updatedAtMs;
    const status = report.status;
    const attempts = used + 1;
    const base = { version, attempts };
    setCross({ ...base, op, state: 'loading' });
    const canRetry = () => alive.current && live.current.version === version && live.current.crossReady;
    const shouldPause = () => live.current.version !== version || (attempts === 1 && !live.current.crossReady);
    try {
      let data: HubCrossCheck;
      for (let request = 0; ; request++) {
        try {
          data = await call<HubCrossCheck>('cross-check', { reportId: report.reportId });
          break;
        } catch (error) {
          if (object(error).status !== 429 || request >= crossBackoff.length || !canRetry()) throw error;
          await new Promise(resolve => setTimeout(resolve, crossBackoff[request]));
          if (!canRetry()) throw error;
        }
      }
      if (!alive.current) return;
      if (live.current.version !== version) own(op, { ...base, state: 'idle' });
      else if (data.reportUpdatedAtMs === updated && data.reportStatus === status) own(op, { ...base, state: 'ok', data });
      else if (shouldPause()) own(op, { ...base, state: 'idle' });
      else own(op, { ...base, state: 'stale' });
    } catch (error) {
      if (!alive.current) return;
      if (shouldPause()) own(op, { ...base, state: 'idle' });
      else own(op, { ...base, state: 'error', error: (error instanceof Error && error.message) || CROSS_UNKNOWN_ERROR });
    } finally {
      finish(op);
    }
  };
  useEffect(() => {
    if (crossReady && effective.state === 'idle' && busyOp.current === 0) void loadCross(effective.attempts);
  }, [crossReady, currentVersion, cross, wake]);

  const retryCross = () => setCross({ version: currentVersion, op: 0, state: 'idle', attempts: 0 });
  const refreshCross = async () => {
    const pressed = currentVersion;
    setManualRefresh(true);
    try {
      await onRefresh();
    } catch {
      // The existing report flow displays its own refresh error.
    } finally {
      if (alive.current) {
        setManualRefresh(false);
        setCross(existing => existing.state === 'stale' && existing.version === pressed
          ? { version: live.current.version, op: 0, state: 'idle', attempts: 0 }
          : existing);
      }
    }
  };
  const refreshingStale = manualRefresh && effective.state === 'stale' && !readHold && !retryHold;
  const crossView: CrossView =
    !crossReady && !refreshingStale ? { phase: 'wait', wait: (!confirmed || retryActive || retryHold) ? 'H3' : 'H3b' }
    : effective.state === 'ok' ? { phase: 'ok', data: effective.data }
    : effective.state === 'error' ? { phase: 'error', error: effective.error }
    : effective.state === 'stale' ? { phase: 'stale' }
    : { phase: 'loading' };
  const injections = itemsReady ? items.filter(item => item.definition.kind === 'injection') : [];
  const injection = injections.length === 1 && usablePoints(injections[0]).a ? injections[0] : undefined;

  const [images,setImages]=useState<Record<string,string>>({}), [imageErrors,setImageErrors]=useState<Record<string,string>>({});
  const [loadingImages,setLoadingImages]=useState<Record<string,boolean>>({});
  const openQuestion=async (id:string) => {
    const item=items.find(x=>x.itemId===id);
    if(!item?.imageAvailable || images[id] || loadingImages[id]) return;
    setLoadingImages(old=>({...old,[id]:true}));
    try { const result=await call<{dataURL:string}>('image',{reportId:report.reportId,itemId:id});setImages(old=>({...old,[id]:result.dataURL}));setImageErrors(old=>({...old,[id]:''})); }
    catch(e) {setImageErrors(old=>({...old,[id]:e instanceof Error?e.message:'读取本次图片中断，请重新展开题目重试；不会重新检测。'}));}
    finally {setLoadingImages(old=>({...old,[id]:false}));}
  };
  const originalVerdict={...view.verdict};
  if(view.native && originalVerdict.iq) originalVerdict.iq={...originalVerdict.iq,per_question:originalVerdict.iq.per_question.map(q=>({...q,image:images[q.id]}))};
  if(view.native && originalVerdict.vision?.question) originalVerdict.vision={...originalVerdict.vision,question:{...originalVerdict.vision.question,image:images.vision}};
  const extras=(id:string) => {const item=items.find(x=>x.itemId===id);return <>
    {item && report.retry?.itemIds.includes(id) && ['queued','running'].includes(item.status) && <p className="muted">正在补测，下面保留的是上次回答。</p>}
    {onRetry && item && canRetryHubItem(item) && <><button type="button" className="btn-secondary" disabled={retryDisabled} onClick={()=>onRetry([id])}>补测此项</button><p className="muted">只重新提问此失败项，成功回答保留；可能产生费用，结果更新到本报告，不修改主站。</p></>}
    {loadingImages[id] && <p className="muted">正在读取本次已保存图片，不重新检测。</p>}
    {imageErrors[id] && <p className="error-box" role="alert">{imageErrors[id]}</p>}
  </>};
  const knowledgeDetail=(id:string)=>{const item=items.find(x=>x.itemId===id || x.itemId==='knowledge:'+id);return item ? <HubQuestionDetails bodyOnly modelGroup={view.verdict.knowledge?.boundary_review?.method === 'model_group'} item={item} call={call} reportId={report.reportId} reportStatus={presentationStatus} onRetry={onRetry} retryDisabled={retryDisabled} previous={Boolean(report.retry?.itemIds.includes(item.itemId)&&['queued','running'].includes(item.status))}/> : complete ? undefined : <p className="muted">正在读取本题已保存的详细结果，请稍候；不会重新检测。</p>};
  const questions: Record<string, ReactNode> = {};
  const retryRunning = ['queued','running'].includes(report.status);
  const activeKnowledgeRetry = retryRunning &&
    Boolean(view.groups.knowledge?.some(item => report.retry?.itemIds.includes(item.itemId)));
  const knowledgeReview = view.verdict.knowledge?.boundary_review;
  const crossYear = knowledgeReview?.method === 'cross_year';
  const modelGroup = knowledgeReview?.method === 'model_group';
  const monthlyKnowledge = crossYear && items.some(item => item.itemId === 'knowledge:minnesota_2026_pretti');
  const knowledgeAskLimit = monthlyKnowledge ? 16 : complete ? 10 : undefined;
  const pendingKnowledge = knowledgeReview?.reason === 'retry_pending';
  if (pendingKnowledge && originalVerdict.knowledge) {
    let reason = '补测未完成或未能保存复核结论，请查看最新逐题结果和运行状态。';
    if (activeKnowledgeRetry) {
      reason = modelGroup ? '正在补测知识题，型号核对暂未确认。已保存的回答仍可查看。' : '正在补测知识题，日期边界暂未确认。已保存的回答仍可查看。';
    } else if (!complete && retryRunning) {
      reason = '正在读取本轮补测状态，已保存回答仍可查看。';
    }
    originalVerdict.knowledge = {
      ...originalVerdict.knowledge, rows: [], guessed: [], label: modelGroup ? '型号核对未确认' : crossYear ? '知识截止日期未确认' : '日期边界未确认', reason,
    };
    view.verdict.knowledge = originalVerdict.knowledge;
  } else if (crossYear && knowledgeReview.status === 'not_needed' && originalVerdict.knowledge &&
      (report.status === 'cancelled' || report.status === 'interrupted')) {
    originalVerdict.knowledge = {
      ...originalVerdict.knowledge, reason: boundaryReviewText('interrupted', knowledgeReview.method),
    };
    view.verdict.knowledge = originalVerdict.knowledge;
  }
  const knowledge = view.verdict.knowledge;
  const fp = originalVerdict.fingerprint;
  const overlap = fp && !fp.error && fp.basis === 'numeric' && Number.isInteger(fp.used_outputs) && fp.used_outputs > 0 &&
    fingerprintState(report.model,fp.family || '',fp.top_model || '') === 'unconfirmed';
  const sharedDate = !modelGroup && overlap && knowledge && !['not_selected','not_applicable'].includes(knowledge.status)
    ? 'GPT-6 Astra 和 GPT-6.1 Sol 的公开知识截止日期均为 2026-04-30，知识日期无法区分两款。' : '';
  const knowledgeComparison = complete && (report.status === 'done' || report.status === 'partial') && knowledge
    ? knowledgeDateComparison(report.model, knowledge)
    : undefined;
 let customIQ:ReactNode;
  for (const [kind, rows] of Object.entries(view.groups)) {
    if (kind === 'cache' && view.cacheView) {
      const inlineItem = view.cacheView.inlineItem;
      questions.cache = <>
        {inlineItem && <HubProbeEvidence item={inlineItem} verifiedCache inlineCache />}
        <div className="question-list">{rows.map(item => {
          const samples = object(item.result?.evidence).samples;
          const previous = report.retry?.itemIds.includes(item.itemId) && ['queued', 'running'].includes(item.status) && Array.isArray(samples) && samples.length > 0;
          return <HubQuestionDetails key={item.itemId} item={item} reportId={report.reportId} reportStatus={presentationStatus}
            call={call} onRetry={onRetry} onJudged={() => onRefresh(report.reportId)} retryDisabled={retryDisabled} previous={previous} verifiedScoring cacheEvidenceInline={item === inlineItem} />;
        })}</div>
      </>;
      continue;
    }
    if(kind==='knowledge') {
      const chronological = [...rows].sort((a, b) =>
        String(a.definition.parameters?.date || '\uffff').localeCompare(String(b.definition.parameters?.date || '\uffff')));
      const skipped=chronological.filter(item=>['not_asked','not_applicable'].includes(itemJudgment(item).status??''));
      const represented=new Set((view.verdict.knowledge?.rows??[]).map(row=>row.id.replace(/^knowledge:/,'')));
      const unrepresented=chronological.filter(item=>!skipped.includes(item)&&!represented.has(item.itemId.replace(/^knowledge:/,'')));
      // While detection runs, untouched queued events are only candidates for the search; listing them all stretches the page.
      const waiting=retryRunning ? unrepresented.filter(item=>item.status==='queued'&&Object.keys(object(item.result)).length===0&&!report.retry?.itemIds.includes(item.itemId)) : [];
      const extra=unrepresented.filter(item=>!waiting.includes(item));
      const skippedExplanation = modelGroup
        ? '本轮只核对该型号的题组，其余题目没有提问，不算答错。'
        : knowledgeAskLimit === 16
        ? '完整默认题库先问两道 2026 年事件，再按月补问并核实答对的题，每次最多 16 道。展开可查看本轮未选到的题目，未选到的题不算答错。自选题按所选范围检测。'
        : !crossYear
          ? '完整默认题库沿用原程序的串行二分，每次最多提问10道；列表会写明本轮未选到的题目，未选到的题不算答错。自定义或部分选择时按设置执行。'
          : knowledgeAskLimit === 10
            ? '完整默认题库先问两道 2026 年事件，再根据回答选题，每次最多 10 道。展开可查看本轮未选到的题目，未选到的题不算答错。自选题按所选范围检测。'
            : '';
      questions.knowledge = <>
        <p className="muted">点击上方事件行可查看本题已保存回答，默认收起，不重新检测或修改主站。</p>
        {pendingKnowledge && complete && !activeKnowledgeRetry && extra.some(item => ['queued', 'running'].includes(item.status)) &&
          <p className="muted">下面保留的是停止前保存的结果，未完成题目可按状态补测。</p>}
        {extra.map(item => <HubQuestionDetails key={item.itemId} item={item} modelGroup={modelGroup} call={call} reportId={report.reportId}
          reportStatus={presentationStatus} onRetry={onRetry} retryDisabled={retryDisabled}
          previous={activeKnowledgeRetry && Boolean(report.retry?.itemIds.includes(item.itemId) && ['queued', 'running'].includes(item.status))} />)}
        {waiting.length > 0 && <p className="muted">{modelGroup ? `另有 ${waiting.length} 道事件题等待处理，本轮只提问该型号的固定题组。` : `另有 ${waiting.length} 道候选事件题，检测会按回答决定是否提问，答完后显示在这里。`}</p>}
        {skipped.length > 0 && <details className="fold">
          <summary>本轮未提问的题目与原因（{skipped.length}）</summary>
          {skippedExplanation && <p className="muted">{skippedExplanation}</p>}
          <div className="question-list">
            {skipped.map(item => <HubQuestionDetails key={item.itemId} item={item} modelGroup={modelGroup} call={call}
              reportId={report.reportId} reportStatus={presentationStatus} />)}
          </div>
        </details>}
      </>;
      continue;
    }

    if(view.native && !['iq','vision','knowledge'].includes(kind)) {
      const compatible=rows.filter(item=>!hasOriginalItemDTO(item)&&!['not_asked','not_applicable'].includes(itemJudgment(item).status??''));
      const failed=rows.filter(item=>hasOriginalItemDTO(item)&&canRetryHubItem(item));
      questions[kind]=compatible.length > 0 || (onRetry && failed.length > 0) ? <div className="question-list">
        {compatible.map(item=><HubQuestionDetails key={item.itemId} item={item} call={call} reportId={report.reportId} reportStatus={presentationStatus} onRetry={onRetry} retryDisabled={retryDisabled} previous={Boolean(report.retry?.itemIds.includes(item.itemId)&&['queued','running'].includes(item.status))}/>)}
        {onRetry && failed.map(item=><div key={item.itemId}><button type="button" className="btn-secondary" disabled={retryDisabled} onClick={()=>onRetry([item.itemId])}>补测此项</button><p className="muted">只补测此失败项，成功结果保留；可能产生费用，结果更新到本报告，不修改主站。</p></div>)}
      </div> : null;
      continue;
    }

    const folds = rows.map((item, index) => <HubQuestionDetails key={report.reportId + ':' + item.itemId} item={item} reportId={report.reportId} reportStatus={presentationStatus} call={call} number={kind === 'iq' ? index + 1 : undefined} onRetry={onRetry} retryDisabled={retryDisabled} onJudged={onRefresh} verifiedScoring={verified} previous={Boolean(report.retry?.itemIds.includes(item.itemId) && ['queued', 'running'].includes(item.status))} />);
    const originalQuestionItems = view.native && ['iq','vision'].includes(kind) && rows.every(item => typeof (item.result?.evidence as Record<string,unknown> | undefined)?.[kind==='vision'?'question':'id'] !== 'undefined');
    if(originalQuestionItems) continue;
    if(view.native && kind==='iq' && view.verdict.iq) {customIQ=<>{rows.filter(item=>!('id' in ((item.result?.evidence as Record<string,unknown>)||{}))).map(item=><HubQuestionDetails key={item.itemId} item={item} reportId={report.reportId} reportStatus={presentationStatus} call={call} onRetry={onRetry} retryDisabled={retryDisabled} onJudged={onRefresh} verifiedScoring={verified} />)}</>;continue;}
    questions[kind] = kind === 'iq'  ? <><p className="muted">点击题目查看本次题面、回答和判定；默认收起，不会重新检测或修改主站。</p>{folds}</> : <div className="question-list">{folds}</div>;
  }
  if (knowledgeComparison || sharedDate) {
    questions.knowledge = <><p className="muted" data-testid="knowledge-date-comparison">{[sharedDate,knowledgeComparison].filter(Boolean).join(" ")}</p>{questions.knowledge}</>;
  }
  const sectionContent: Record<string, ReactNode> = Object.fromEntries(Object.entries(view.messages).map(([key, text]) => [key, <p key={key} className="muted">{text}</p>]));
  const ended = confirmed;
  if (view.cacheView) {
    const part = view.cacheView.part;
    const totalVisible = typeof view.verdict.total?.score === 'number' && !view.totalLabel;
    let cacheUnconfirmed = '缓存尚未确认。可以展开本项查看缺少的回包字段或未完成的轮次，本次报告的总分也暂未确认。';
    if (totalVisible) {
      cacheUnconfirmed = ended
        ? '缓存尚未确认，总分里这一项暂按 0 分计算。请展开下面的缓存项查看原因：六轮都已完成的，可以人工判为通过或不通过；请求失败或未完成的，可以补测。'
        : '检测还在进行，缓存这一项暂按 0 分计入总分；检测结束后这里会显示缓存结果和可用的处理办法。';
    }
    sectionContent.cache = <>
      <div className="metric-line"><strong className={part?.known && part.points === 1 ? 'good' : undefined}>
        {!part ? '等待结果' : !part.known ? '尚未确认' : part.points === 1 ? '通过' : '未通过'}
      </strong></div>
      {!part ? <p className="muted">本项尚未记录分类结果，请查看本次检测状态。</p> : !part.known &&
        <p className="muted">{cacheUnconfirmed}</p>}
    </>;
  }
  if (view.native && !view.verdict.iq && questions.iq) sectionContent.iq = questions.iq;
  // Preserve the upstream omission of an unselected vision section.
  if (complete && !view.groups.vision?.length) sectionContent.vision = null;
  const startedAt = typeof report.startedAtMs === 'number' ? report.startedAtMs : report.status === 'queued' ? 0 : report.createdAtMs;
  const timing = startedAt && (!ended || report.updatedAtMs) ? { started_at: new Date(startedAt).toISOString(), ...(ended ? { finished_at: new Date(report.updatedAtMs!).toISOString() } : {}), total_ms: ended ? Math.max(0, report.updatedAtMs! - startedAt) : 0, last_run_ms: 0, runs: 1 } : undefined;
  if(singleAsk){const item=items[0],raw=object(object(item?.result?.evidence).single_ask);return <>
    <section className="result-card"><h3>单问单答</h3><p className="muted">这是当前账号的一次独立问答，不计推理或总分；展开查看原问题、完整回答和用量，失败可在本记录补测。</p>
    {item && <details className="question" data-item-id={item.itemId}><summary><span>本次问题与回答</span><b>{item.status==='error'?'请求失败':ended?'已结束':'等待回答'}</b></summary><pre>{item.prompt}</pre>{item.result?.error && <p className="error-box" role="alert">{item.result.error}</p>}<SingleAnswer model={report.model} answer={Object.keys(raw).length?raw as Metrics:null} />{extras(item.itemId)}</details>}
    </section><ProbeTimeline probes={view.probes} status={presentationStatus} executionPhase={report.executionPhase} timing={view.verdict.timing||timing} progress={view.progress}/></>;
  }
  return <>
    {view.native && items.some(item=>item.status==='done'&&item.definition.judge.type!=='model_identity'&&!hasOriginalItemDTO(item)&&!['not_asked','not_applicable'].includes(itemJudgment(item).status??'')) && <p className="muted">本报告含自定义或旧格式结果，已保存回答可逐项展开；旧版未保存的原诊断不能补回，成功结果不会重新提问。</p>}
    {!view.native && ended && <p className="muted">这是旧版接入保存的报告；已有回答保留，但当时未保存的原版诊断不能补回。新检测使用原程序；失败项可在原报告补测。</p>}
    <HubCrossCheckCard cross={crossView} items={items} onRetry={retryCross} onRefresh={() => void refreshCross()} refreshing={manualRefresh} />
    <ResultCards verdict={view.native ? originalVerdict : view.verdict} model={report.model} loading={!ended} showCache={view.cacheView?.visible ?? Boolean(view.groups.cache)} savedScore={!view.native} preserveTotalScore={preserveTotalScore} totalLabel={view.totalLabel} questions={questions} sectionContent={sectionContent} questionExtra={view.native ? extras : undefined} onQuestionOpen={view.native ? id=>void openQuestion(id) : undefined} collapseVision={view.native} questionTail={customIQ} knowledgeExtra={knowledgeDetail} knowledgeAskLimit={knowledgeAskLimit}
      injectionNote={injection ? INJECTION_NOTE : undefined} injectionDetail={injection ? <HubInjectionDetail item={injection} cross={crossView} model={report.model} call={call} /> : undefined} />
    <ProbeTimeline probes={view.probes} status={presentationStatus} executionPhase={report.executionPhase} timing={view.native ? view.verdict.timing || timing : timing} progress={view.progress}>
      {!report.model.toLowerCase().startsWith('claude-') && questions.thinking}
    </ProbeTimeline>
  </>;
}
