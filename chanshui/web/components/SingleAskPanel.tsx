"use client";
import {useState,useEffect,useRef} from 'react';
import Select from './Select';
import ErrorFold from './ErrorFold';
import {useI18n} from '../lib/i18n';
import {modelFamily} from '../lib/report-view';
import {pendingWarning} from '../lib/hub-draft';
import type {Metrics} from '../lib/api';
export interface SingleAskPayload {prompt:string;thinking:{type:string;display?:string;budget_tokens?:number}|null;effort:string}
export function SingleAnswer({answer,model}:{answer:Metrics|null;model:string}) {const {t:tr}=useI18n();return <>{answer && (
          <div className="answer-panel">
            <pre>{answer.Answer || tr("未返回正文")}</pre>
            <p className="muted">
              {tr("输入 {a} · 输出 {b} · 思考 {c} · 缓存写入 {d} · 缓存读取 {e}", {
                a: answer.InputTokens,
                b: answer.OutputTokens,
                c: answer.ThinkingTokens,
                d: answer.CacheCreation5m + answer.CacheCreation1h,
                e: answer.CacheRead,
              })}
            </p>
            {modelFamily(model) === "claude" && (
              <details>
                <summary>{tr("思维链全文与签名")}</summary>
                <pre>{answer.ThinkingText || tr("未回传摘要")}</pre>
                <pre>{answer.Signature || tr("无签名")}</pre>
              </details>
            )}
          </div>
        )}</>;} 
type PendingAsk = {
  reportId: string;
  accepted?: boolean;
  busy: boolean;
  confirm: (method: 'lookup' | 'replay', signal: AbortSignal) => Promise<Metrics | null>;
  open: () => void;
};
export default function SingleAskPanel({model,canRun,onAsk,pending}:{model:string;canRun:boolean;onAsk:(input:SingleAskPayload,signal:AbortSignal)=>Promise<Metrics | null>;pending?:PendingAsk}) {
 const {t:tr}=useI18n();
 const [prompt,setPrompt]=useState(''),[mode,setMode]=useState('summary'),[budget,setBudget]=useState(2048),[effort,setEffort]=useState('');
 const [answer,setAnswer]=useState<Metrics|null>(null),[asking,setAsking]=useState(false),[askError,setAskError]=useState('');
 const active=useRef<AbortController|null>(null);
 useEffect(()=>()=>active.current?.abort(),[]);
 const run=async(work:(signal:AbortSignal)=>Promise<Metrics | null>)=>{
  if(active.current)return;
  const controller=new AbortController();active.current=controller;
  setAsking(true);setAskError('');setAnswer(null);
  try {
    const m = await work(controller.signal);
    if(m&&!controller.signal.aborted){setAnswer(m);if(m.Err)setAskError(m.Err);}
  }catch(error){if(!controller.signal.aborted)setAskError(error instanceof Error?error.message:'单问单答未完成，请按记录编号查询。');}
  finally{if(active.current===controller){active.current=null;if(!controller.signal.aborted)setAsking(false);}}
 };
 const cancel=()=>{active.current?.abort();active.current=null;setAsking(false);};
 const askOnce=()=>{
    if (!canRun || !prompt.trim() || asking) return;
      const thinking = modelFamily(model) !== "claude"
        ? null
        : mode === "omit"
          ? null
          : mode === "disabled"
            ? { type: "disabled" }
            : mode === "highlights"
              ? { type: "adaptive", display: "highlights" }
              : mode === "updates"
                ? { type: "adaptive", display: "updates" }
              : mode === "budget"
              ? {
                  type: "enabled",
                  budget_tokens: budget,
                  display: "summarized",
                }
              : mode === "summary"
                ? { type: "adaptive", display: "summarized" }
                : { type: "adaptive" };
      void run(signal => onAsk({prompt, thinking, effort}, signal));
 };
 return <>      <details className="ask-panel">
        <summary>{tr("单问单答")}</summary><p className="muted">只向当前账号模型发送这一个问题，沿同一队列等待；可能产生费用，结果保存在一份问答记录，失败补测写回该记录，不修改主站。问题最多16KiB。</p>
        <div className="advanced-grid">
          {modelFamily(model) === "claude" && (
            <div className="field">
              <label htmlFor="thinking-mode">{tr("思考模式")}</label>
              <Select
                id="thinking-mode"
                value={mode}
                onChange={(e) => setMode(e.target.value)}
              >
                <option value="summary">{tr("自适应 + 摘要")}</option>
                <option value="adaptive">{tr("自适应（不传 display）")}</option>
                <option value="highlights">{tr("自适应 + 要点")}</option>
                <option value="updates">{tr("自适应 + 进度更新（beta）")}</option>
                <option value="budget">{tr("指定预算")}</option>
                <option value="omit">{tr("不传 thinking")}</option>
                <option value="disabled">{tr("明确关闭（部分模型不支持）")}</option>
              </Select>
            </div>
          )}
          {modelFamily(model) === "claude" && mode === "budget" && (
            <div className="field">
              <label htmlFor="budget">{tr("思考预算 token")}</label>
              <input
                id="budget"
                type="number"
                min="1024"
                max="8192"
                value={budget}
                onChange={(e) => setBudget(Number(e.target.value))}
              />
            </div>
          )}
          <div className="field">
            <label htmlFor="effort">{tr("推理努力（按上游支持）")}</label>
            <Select
              id="effort"
              value={effort}
              onChange={(e) => setEffort(e.target.value)}
            >
              {[
                ["", tr("不传")],
                ["low", tr("低")],
                ["medium", tr("中")],
                ["high", tr("高")],
                ["max", tr("最大")],
              ].map(([v, t]) => (
                <option key={v} value={v}>
                  {t}
                </option>
              ))}
            </Select>
          </div>
        </div>
        <div className="field">
          <label htmlFor="question">{tr("向当前选中模型提问")}</label>
          <textarea
            id="question"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            placeholder={tr("输入问题，Ctrl + Enter 发送")}
            onKeyDown={(e) => {
              if (
                e.ctrlKey &&
                e.key === "Enter" &&
                !asking &&
                canRun &&
                prompt.trim()
              )
                void askOnce();
            }}
          />
        </div>
        <button
          className="btn-primary"
          disabled={!canRun || !prompt.trim() || asking}
          onClick={askOnce}
        >
          {asking ? tr("正在回答…") : tr("发送问题")}
        </button>
        {asking && <button type="button" className="btn-secondary" onClick={cancel}>取消等待问答</button>}
        {askError && <ErrorFold text={askError} />}
        <SingleAnswer answer={answer} model={model} />
      </details>
      {pending && <section className="hub-note" aria-label="单问单答受理">
        {pending.accepted ? <p>原问答记录已保存，可打开查看结果或补测失败项。</p> : <p>{pendingWarning}</p>}
        <p>原报告编号 <strong>{pending.reportId}</strong></p>
        {pending.accepted ? <button type="button" className="btn-secondary" onClick={pending.open}>查看原问答记录</button> : <div className="actions hub-wrap">
          <button type="button" className="btn-secondary" disabled={pending.busy || asking} onClick={() => void run(signal => pending.confirm('lookup', signal))}>查询本次受理结果</button>
          <button type="button" className="btn-secondary" disabled={pending.busy || asking} onClick={() => void run(signal => pending.confirm('replay', signal))}>用原编号重试受理</button>
        </div>}
      </section>}
      </>;
}
