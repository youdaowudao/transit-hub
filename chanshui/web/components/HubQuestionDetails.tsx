"use client";
import { useState } from 'react';
import { useI18n } from '../lib/i18n';
import { HubProbeEvidence } from './ProbeEvidence';
import { HubRequestError } from '../lib/hub-bridge';
import type { HubCall, HubItem } from '../lib/hub-types';
import { canRetryHubItem, itemJudgment, itemLabel, itemPoints, object } from '../lib/hub-report-view';

export default function HubQuestionDetails({ item, call, reportId, reportStatus = '', number, onRetry, retryDisabled, onJudged, previous, bodyOnly = false, verifiedScoring = false, cacheEvidenceInline = false, modelGroup = false }: { item: HubItem; call: HubCall; reportId: string; reportStatus?: string; number?: number; onRetry?: (ids: string[]) => void; retryDisabled?: boolean; onJudged?: () => Promise<void>; previous?: boolean; bodyOnly?: boolean; verifiedScoring?: boolean; cacheEvidenceInline?: boolean; modelGroup?: boolean }) {
  const { t: tr } = useI18n();
  const [image, setImage] = useState(''), [error, setError] = useState('');
  const label = itemLabel(item, reportStatus), points = label === '完成' ? itemPoints(item) : '', result = item.result ?? {}, judgment = itemJudgment(item);
  const earned = result.earned ?? judgment.earned, max = result.max ?? judgment.max ?? item.definition.maxScore;
  const full = Boolean(points) && judgment.status === 'known' && typeof earned === 'number' && earned === max && !result.half && !judgment.half;
  const knowledge = item.definition.kind === 'knowledge';
  const verifiedCache = verifiedScoring && item.definition.kind === 'cache';
  const observation = object(object(result.evidence).row);
  const extract = typeof observation.answer === 'string' ? observation.answer : '';
  const hasAnswer = typeof result.answer === 'string' && Boolean(result.answer.trim());
  const [judging, setJudging] = useState(false), [judgeError, setJudgeError] = useState('');
  const manual = judgment.source === 'manual';
  const detectionActive = reportStatus === 'queued' || reportStatus === 'running';
  const identityJudge = item.definition.judge.type === 'model_identity' && hasAnswer;
  const canJudge = Boolean(onJudged) && (identityJudge || verifiedCache) && !result.error &&
    (item.status === 'done' || detectionActive && ['queued', 'running'].includes(item.status)) && (judgment.status === 'unknown' || manual);
  const currentVerdict = manual ? judgment.earned === 0 ? 'fail' : 'pass' : '';
  const judgeItem = async (verdict: 'pass' | 'fail') => {
    if (!onJudged || judging || detectionActive) return;
    setJudging(true);
    setJudgeError('');
    try {
      await call('judge-item', { reportId, itemId: item.itemId, verdict });
    } catch (e) {
      const reason = e instanceof Error ? e.message.replace(/[。.]$/, '') : '暂时无法确认保存失败的原因，请核查运行状态';
      const rejected = e instanceof HubRequestError && e.details.operationResult === 'not_started' &&
        ['judge-item', 'report', 'workspace', 'input', 'storage'].some(step => step === e.details.step);
      setJudgeError(rejected
        ? `人工判定没有保存：${reason}。本题分数和报告总分保持不变，可刷新报告后重试。`
        : `人工判定的保存结果尚未确认：${reason}。请刷新报告核对判定和分数后再决定是否重试。`);
      setJudging(false);
      return;
    }
    try {
      await onJudged();
    } finally {
      setJudging(false);
    }
  };
  const retry = onRetry && canRetryHubItem(item) && <><button type="button" className="btn-secondary" disabled={retryDisabled || judging} onClick={() => onRetry([item.itemId])}>补测此项</button><p className="muted">只重新提问此失败或未完成项，成功结果保留；可能产生请求费用，结果更新到本报告，不修改主站。</p></>;
  const showKnowledgeAnswer = knowledge && (hasAnswer || extract || item.status === 'done' && judgment.status === 'reference');
  const eventDate = knowledge ? String(item.definition.parameters?.date ?? '') : '';
  const body=<>
    {previous && <p className="muted">正在补测；下面保留的是上次结果，当前结果尚未确认。</p>}
    {!canJudge && retry}
    <h4>本次实际题目</h4><pre>{(result.evidence && typeof result.evidence==='object' && 'prompt' in result.evidence ? String((result.evidence as {prompt:string}).prompt) : item.prompt) || (item.imageAvailable ? '题面在图片中' : '使用本项固定检测流程')}</pre>
    {item.imageAvailable && <><button type="button" className="btn-secondary" onClick={async () => { try { const data = await call<{ dataURL: string }>('image', { reportId, itemId: item.itemId }); setImage(data.dataURL); setError(''); } catch (e) { setError(e instanceof Error ? e.message : '读取已保存图片时中断，请重试读取；不会重新检测。'); } }}>查看题目图片</button><p className="muted">只读取本次已保存图片，不重新检测。</p>{image && <img className="question-image" src={image} alt="本次检测实际使用的题目图片" />}</>}
    {showKnowledgeAnswer && <>
      <h4>模型原始回答</h4>
      {hasAnswer ? <pre>{result.answer}</pre> : <p className="muted">这份报告没有保存模型原话。</p>}
      {!hasAnswer && extract && <><h4>已保存的判定摘录</h4><pre>{extract}</pre></>}
    </>}
    {!knowledge && typeof result.answer === 'string' && <><h4>模型回答</h4><pre>{result.answer || '尚未返回正文'}</pre></>}
    {item.expected && <><h4>检测端参考答案</h4><p className="muted">{item.definition.judge.type === 'model_identity' ? '本题按检测端规则计分，不调用模型自评。' : knowledge ? modelGroup ? '这道事件题用于核对型号表现，不计入总分。' : '这道知识事件题用于判断日期范围，不计入总分。' : '原版推理题先按本地规则判分，零分时可能调用被测模型自评；识图和自定义题按各自规则处理。'}</p><pre>{item.expected}</pre></>}
    {label === '待人工查看' && <p className="muted">此题只供人工查看，不参与自动得分。</p>}
    {(result.half || judgment.half) && <p className="muted">{verifiedScoring && item.definition.kind === 'iq' ? '本题实际获得的分数计入推理百分制。' : '本题半分只展示卷面，不进入推理通过分子。'}</p>}
    {judgment.reason && <p className="muted">{judgment.reason}</p>}
    {canJudge && <>
      <button type="button" className="btn-secondary" disabled={detectionActive || judging || currentVerdict === 'pass'} onClick={() => void judgeItem('pass')}>人工判为通过</button>
      <button type="button" className="btn-secondary" disabled={detectionActive || judging || currentVerdict === 'fail'} onClick={() => void judgeItem('fail')}>人工判为不通过</button>
      {retry}
      <p className="muted">{verifiedCache
        ? '看过下面各轮的缓存读取和写入后由你判定：通过计入缓存这一项的分数，不通过得 0 分。只改本报告这一项并重算总分，不重新请求、不产生费用、不修改主站；之后可以改判。'
        : '看过上面的模型回答后由你判定：通过得本题满分，不通过得 0 分。只改本报告这一题并重算推理分和总分，不重新提问、不产生请求费用、不修改主站；之后可以改判。'}</p>
      {detectionActive && <p className="muted">检测或补测进行中，结束后才能人工判定。</p>}
      {judgeError && <p className="error-box" role="alert">{judgeError}</p>}
    </>}
    {(result.error || error) && <p className="error-box" role="alert">{result.error || error}</p>}
    {label === '未完成' && <p className="muted">{item.status === 'queued' ? '本次检测已结束，此项未执行。' : '本次检测已结束，此项未完成；已发出的请求可能已计费。'}</p>}
    {!cacheEvidenceInline && (label !== '未完成' || verifiedCache && previous) && <HubProbeEvidence item={item} verifiedCache={verifiedCache} previous={previous} />}
    {typeof (result.metrics as { Signature?: unknown } | undefined)?.Signature === 'string' && <details className="fold"><summary>已返回的签名</summary><pre>{(result.metrics as { Signature: string }).Signature}</pre></details>}
    {typeof result.thinkingText === 'string' && result.thinkingText && <details className="fold"><summary>实际返回的可见推导</summary><pre>{result.thinkingText}</pre></details>}
    {result.evidence !== undefined && <details className="fold"><summary>查看原始检测证据</summary><pre>{JSON.stringify(result.evidence, null, 2)}</pre></details>}
    {result.metrics !== undefined && <details className="fold"><summary>用量和响应记录</summary><pre>{JSON.stringify(result.metrics, null, 2)}</pre></details>}
    {item.tags && item.tags.length > 0 && <p>{item.tags.join(' · ')}</p>}
    {Object.keys(result).some(k => !['answer', 'earned', 'max', 'half', 'judgment', 'error', 'metrics', 'evidence', 'thinkingText'].includes(k)) && <details className="fold"><summary>其他已保存结果</summary><pre>{JSON.stringify(Object.fromEntries(Object.entries(result).filter(([k]) => !['answer', 'earned', 'max', 'half', 'judgment', 'error', 'metrics', 'evidence', 'thinkingText'].includes(k))), null, 2)}</pre></details>}
  </>;
  if(bodyOnly)return body;
  return <details className="question" data-item-id={item.itemId}>
    <summary><span>{number === undefined ? '' : `Q${number} · `}{eventDate && eventDate + ' · '}{tr(item.definition.title)}</span><b className={label === '请求失败' ? 'bad' : full ? 'good' : 'warn'}>{manual ? '人工判定' : points || label}</b></summary>
    {body}
  </details>;
}
