"use client";
import {useState,useEffect,useRef} from 'react';
import Select from './Select';
import ErrorFold from './ErrorFold';
import {useI18n} from '../lib/i18n';
import {modelFamily} from '../lib/report-view';
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
export default function SingleAskPanel({model,canRun,onAsk}:{model:string;canRun:boolean;onAsk:(input:SingleAskPayload,signal:AbortSignal)=>Promise<Metrics>}) {
 const {t:tr}=useI18n();
 const [prompt,setPrompt]=useState(''),[mode,setMode]=useState('summary'),[budget,setBudget]=useState(2048),[effort,setEffort]=useState('');
 const [answer,setAnswer]=useState<Metrics|null>(null),[asking,setAsking]=useState(false),[askError,setAskError]=useState('');
 const active=useRef<AbortController|null>(null);
 useEffect(()=>()=>active.current?.abort(),[]);
 const askOnce=async()=>{
  const controller=new AbortController();active.current=controller;
  setAsking(true);setAskError('');setAnswer(null);
    try {
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
      const m = await onAsk({prompt, thinking, effort}, controller.signal);
      if(!controller.signal.aborted){setAnswer(m);if(m.Err)setAskError(m.Err);}
  }catch(error){if(!controller.signal.aborted)setAskError(error instanceof Error?error.message:'单问单答未完成，请按记录编号查询。');}
  finally{if(!controller.signal.aborted)setAsking(false);}
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
        {askError && <ErrorFold text={askError} />}
        <SingleAnswer answer={answer} model={model} />
      </details></>;
}
