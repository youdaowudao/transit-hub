"use client";
import { useState } from 'react';
import { useI18n } from '../lib/i18n';
import { HubProbeEvidence } from './ProbeEvidence';
import type { HubCall, HubItem } from '../lib/hub-types';
import { canRetryHubItem, itemJudgment, itemLabel, itemPoints } from '../lib/hub-report-view';

export default function HubQuestionDetails({ item, call, reportId, reportStatus = '', number, onRetry, retryDisabled, previous }: { item: HubItem; call: HubCall; reportId: string; reportStatus?: string; number?: number; onRetry?: (ids: string[]) => void; retryDisabled?: boolean; previous?: boolean }) {
  const { t: tr } = useI18n();
  const [image, setImage] = useState(''), [error, setError] = useState('');
  const label = itemLabel(item, reportStatus), points = label === '完成' ? itemPoints(item) : '', result = item.result ?? {}, judgment = itemJudgment(item);
  const earned = result.earned ?? judgment.earned, max = result.max ?? judgment.max ?? item.definition.maxScore;
  const full = Boolean(points) && judgment.status === 'known' && typeof earned === 'number' && earned === max && !result.half && !judgment.half;
  return <details className="question" data-item-id={item.itemId}>
    <summary><span>{number === undefined ? '' : `Q${number} · `}{tr(item.definition.title)}</span><b className={label === '请求失败' ? 'bad' : full ? 'good' : 'warn'}>{points || label}</b></summary>
    {previous && <p className="muted">正在补测；下面保留的是上次结果，当前结果尚未确认。</p>}
    {onRetry && canRetryHubItem(item) && <><button type="button" className="btn-secondary" disabled={retryDisabled} onClick={() => onRetry([item.itemId])}>补测此项</button><p className="muted">只重新提问此失败或未完成项，成功结果保留；可能产生请求费用，结果更新到本报告，不修改主站。</p></>}
    <h4>本次实际题目</h4><pre>{item.prompt || (item.imageAvailable ? '题面在图片中' : '使用本项固定检测流程')}</pre>
    {item.imageAvailable && <><button type="button" className="btn-secondary" onClick={async () => { try { const data = await call<{ dataURL: string }>('image', { reportId, itemId: item.itemId }); setImage(data.dataURL); setError(''); } catch (e) { setError(e instanceof Error ? e.message : '读取已保存图片时中断，请重试读取；不会重新检测。'); } }}>查看题目图片</button><p className="muted">只读取本次已保存图片，不重新检测。</p>{image && <img className="question-image" src={image} alt="本次检测实际使用的题目图片" />}</>}
    {typeof result.answer === 'string' && <><h4>模型回答</h4><pre>{result.answer || '尚未返回正文'}</pre></>}
    {item.expected && <><h4>检测端参考答案</h4><p className="muted">仅用于本地判题或人工核对，不发送给被测模型自评。</p><pre>{item.expected}</pre></>}
    {label === '待人工查看' && <p className="muted">此题只供人工查看，不参与自动得分。</p>}
    {(result.half || judgment.half) && <p className="muted">本题半分只展示卷面，不进入推理通过分子。</p>}
    {judgment.reason && <p className="muted">{judgment.reason}</p>}
    {(result.error || error) && <p className="error-box" role="alert">{result.error || error}</p>}
    {label === '未完成' ? <p className="muted">{item.status === 'queued' ? '本次检测已结束，此项未执行。' : '本次检测已结束，此项未完成；已发出的请求可能已计费。'}</p> : <HubProbeEvidence item={item} />}
    {typeof (result.metrics as { Signature?: unknown } | undefined)?.Signature === 'string' && <details className="fold"><summary>已返回的签名</summary><pre>{(result.metrics as { Signature: string }).Signature}</pre></details>}
    {typeof result.thinkingText === 'string' && result.thinkingText && <details className="fold"><summary>实际返回的可见推导</summary><pre>{result.thinkingText}</pre></details>}
    {result.evidence !== undefined && <details className="fold"><summary>查看原始检测证据</summary><pre>{JSON.stringify(result.evidence, null, 2)}</pre></details>}
    {result.metrics !== undefined && <details className="fold"><summary>用量和响应记录</summary><pre>{JSON.stringify(result.metrics, null, 2)}</pre></details>}
    {item.tags && item.tags.length > 0 && <p>{item.tags.join(' · ')}</p>}
    {Object.keys(result).some(k => !['answer', 'earned', 'max', 'half', 'judgment', 'error', 'metrics', 'evidence', 'thinkingText'].includes(k)) && <details className="fold"><summary>其他已保存结果</summary><pre>{JSON.stringify(Object.fromEntries(Object.entries(result).filter(([k]) => !['answer', 'earned', 'max', 'half', 'judgment', 'error', 'metrics', 'evidence', 'thinkingText'].includes(k))), null, 2)}</pre></details>}
  </details>;
}
