"use client";
import { useState, type ReactNode } from 'react';
import ResultCards from './ResultCards';
import {SingleAnswer} from './SingleAskPanel';
import {object} from '../lib/hub-report-view';
import type {Metrics} from '../lib/api';
import ProbeTimeline from './ProbeTimeline';
import HubQuestionDetails from './HubQuestionDetails';
import { hubReportView, itemJudgment, canRetryHubItem, hasOriginalItemDTO } from '../lib/hub-report-view';
import { isHubTerminal } from '../lib/hub-policy';
import type { HubCall, HubItem, HubReport } from '../lib/hub-types';

export default function HubReportContent({ report, items, complete, call, onRetry, retryDisabled }: { report: HubReport; items: HubItem[]; complete: boolean; call: HubCall; onRetry?: (ids: string[]) => void; retryDisabled?: boolean }) {
  const view = hubReportView(report, items, complete);

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
  const knowledgeDetail=(id:string)=>{const item=items.find(x=>x.itemId===id || x.itemId==='knowledge:'+id);return item ? <HubQuestionDetails bodyOnly item={item} call={call} reportId={report.reportId} reportStatus={report.status} onRetry={onRetry} retryDisabled={retryDisabled} previous={Boolean(report.retry?.itemIds.includes(item.itemId)&&['queued','running'].includes(item.status))}/> : <p className="muted">正在读取本题已保存的详细结果，请稍候；不会重新检测。</p>};
  const questions: Record<string, ReactNode> = {};
 let customIQ:ReactNode;
  for (const [kind, rows] of Object.entries(view.groups)) {
    if(kind==='knowledge') {
      const skipped=rows.filter(item=>['not_asked','not_applicable'].includes(itemJudgment(item).status??''));
      const represented=new Set((view.verdict.knowledge?.rows??[]).map(row=>row.id.replace(/^knowledge:/,'')));
      const extra=rows.filter(item=>!skipped.includes(item)&&!represented.has(item.itemId.replace(/^knowledge:/,'')));
      questions.knowledge=<><p className="muted">点击上方事件行可查看本题已保存回答，默认收起，不重新检测或修改主站。</p>{extra.map(item=><HubQuestionDetails key={item.itemId} item={item} call={call} reportId={report.reportId} reportStatus={report.status} onRetry={onRetry} retryDisabled={retryDisabled} />)}{skipped.length>0 && <details className="fold"><summary>本轮未提问的题目与原因（{skipped.length}）</summary><p className="muted">完整原题库沿用原程序的串行二分，最多10问；未选到的题不当作答错。自定义或部分选择时按设置执行。</p><div className="question-list">{skipped.map(item=><HubQuestionDetails key={item.itemId} item={item} call={call} reportId={report.reportId} reportStatus={report.status}/>)}</div></details>}</>;
      continue;
    }

    if(view.native && !['iq','vision','knowledge'].includes(kind)) {
      const compatible=rows.filter(item=>!hasOriginalItemDTO(item)&&!['not_asked','not_applicable'].includes(itemJudgment(item).status??''));
      const failed=rows.filter(item=>hasOriginalItemDTO(item)&&canRetryHubItem(item));
      questions[kind]=compatible.length > 0 || (onRetry && failed.length > 0) ? <div className="question-list">
        {compatible.map(item=><HubQuestionDetails key={item.itemId} item={item} call={call} reportId={report.reportId} reportStatus={report.status} onRetry={onRetry} retryDisabled={retryDisabled} previous={Boolean(report.retry?.itemIds.includes(item.itemId)&&['queued','running'].includes(item.status))}/>)}
        {onRetry && failed.map(item=><div key={item.itemId}><button type="button" className="btn-secondary" disabled={retryDisabled} onClick={()=>onRetry([item.itemId])}>补测此项</button><p className="muted">只补测此失败项，成功结果保留；可能产生费用，结果更新到本报告，不修改主站。</p></div>)}
      </div> : null;
      continue;
    }

    const folds = rows.map((item, index) => <HubQuestionDetails key={report.reportId + ':' + item.itemId} item={item} reportId={report.reportId} reportStatus={report.status} call={call} number={kind === 'iq' ? index + 1 : undefined} onRetry={onRetry} retryDisabled={retryDisabled} previous={Boolean(report.retry?.itemIds.includes(item.itemId) && ['queued', 'running'].includes(item.status))} />);
    const originalQuestionItems = view.native && ['iq','vision'].includes(kind) && rows.every(item => typeof (item.result?.evidence as Record<string,unknown> | undefined)?.[kind==='vision'?'question':'id'] !== 'undefined');
    if(originalQuestionItems) continue;
    if(view.native && kind==='iq' && view.verdict.iq) {customIQ=<>{rows.filter(item=>!('id' in ((item.result?.evidence as Record<string,unknown>)||{}))).map(item=><HubQuestionDetails key={item.itemId} item={item} reportId={report.reportId} reportStatus={report.status} call={call} onRetry={onRetry} retryDisabled={retryDisabled} />)}</>;continue;}
    questions[kind] = kind === 'iq'  ? <><p className="muted">点击题目查看本次题面、回答和判定；默认收起，不会重新检测或修改主站。</p>{folds}</> : <div className="question-list">{folds}</div>;
  }
  const sectionContent: Record<string, ReactNode> = Object.fromEntries(Object.entries(view.messages).map(([key, text]) => [key, <p key={key} className="muted">{text}</p>]));
  if (view.native && !view.verdict.iq && questions.iq) sectionContent.iq = questions.iq;
  // Preserve the upstream omission of an unselected vision section.
  if (complete && !view.groups.vision?.length) sectionContent.vision = null;
  const ended = isHubTerminal(report.status);
  const startedAt = typeof report.startedAtMs === 'number' ? report.startedAtMs : report.status === 'queued' ? 0 : report.createdAtMs;
  const timing = startedAt && (!ended || report.updatedAtMs) ? { started_at: new Date(startedAt).toISOString(), ...(ended ? { finished_at: new Date(report.updatedAtMs!).toISOString() } : {}), total_ms: ended ? Math.max(0, report.updatedAtMs! - startedAt) : 0, last_run_ms: 0, runs: 1 } : undefined;
  if(object(report.summary).singleAsk===true){const item=items[0],raw=object(object(item?.result?.evidence).single_ask);return <>
    <section className="result-card"><h3>单问单答</h3><p className="muted">这是当前账号的一次独立问答，不计推理或总分；展开查看原问题、完整回答和用量，失败可在本记录补测。</p>
    {item && <details className="question" data-item-id={item.itemId}><summary><span>本次问题与回答</span><b>{item.status==='error'?'请求失败':ended?'已结束':'等待回答'}</b></summary><pre>{item.prompt}</pre>{item.result?.error && <p className="error-box" role="alert">{item.result.error}</p>}<SingleAnswer model={report.model} answer={Object.keys(raw).length?raw as Metrics:null} />{extras(item.itemId)}</details>}
    </section><ProbeTimeline probes={view.probes} status={report.status} timing={view.verdict.timing||timing} progress={view.progress}/></>;
  }
  return <>
    {view.native && items.some(item=>item.status==='done'&&!hasOriginalItemDTO(item)&&!['not_asked','not_applicable'].includes(itemJudgment(item).status??'')) && <p className="muted">本报告含自定义或旧格式结果，已保存回答可逐项展开；旧版未保存的原诊断不能补回，成功结果不会重新提问。</p>}
    {!view.native && ended && <p className="muted">这是旧版接入保存的报告；已有回答保留，但当时未保存的原版诊断不能补回。新检测使用原程序；失败项可在原报告补测。</p>}
    <ResultCards verdict={view.native ? originalVerdict : view.verdict} model={report.model} loading={!ended} showCache={Boolean(view.groups.cache)} savedScore={!view.native} totalLabel={view.totalLabel} questions={questions} sectionContent={sectionContent} questionExtra={view.native ? extras : undefined} onQuestionOpen={view.native ? id=>void openQuestion(id) : undefined} collapseVision={view.native} questionTail={customIQ} knowledgeExtra={knowledgeDetail} />
    <ProbeTimeline probes={view.probes} status={report.status} timing={view.native ? view.verdict.timing || timing : timing} progress={view.progress}>
      {!report.model.toLowerCase().startsWith('claude-') && questions.thinking}
    </ProbeTimeline>
  </>;
}
