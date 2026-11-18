"use client";

import { useRef } from 'react';
import Icon from './Icon';

import { claudeDates, claudeFableMythosDates, gptDates } from '../lib/knowledge-dates';

const groups = [
  { label: 'GPT', dates: gptDates },
  { label: 'Claude · Opus / Sonnet / Haiku', dates: claudeDates },
  { label: 'Claude · Fable / Mythos', dates: claudeFableMythosDates },
];

export default function KnowledgeDates() {
  const dialog = useRef<HTMLDialogElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);

  return (
    <>
      <button
        ref={trigger}
        type="button"
        className="theme-trigger knowledge-dates-trigger"
        aria-haspopup="dialog"
        title="查看各模型的知识截止日期，不会发起检测或修改主站。"
        onClick={() => dialog.current?.showModal()}
      >
        知识截止日期
      </button>
      <dialog
        ref={dialog}
        className="feedback-dialog knowledge-dates-dialog"
        aria-labelledby="knowledge-dates-title"
        onClose={() => trigger.current?.focus()}
      >
        <div className="feedback-heading">
          <h2 id="knowledge-dates-title">模型知识截止日期</h2>
          <button type="button" className="icon-button" aria-label="关闭知识截止日期" onClick={() => dialog.current?.close()}>
            <Icon name="close" />
          </button>
        </div>
        <div className="knowledge-dates-grid">
          {groups.map(({ label, dates }) => (
            <section key={label} aria-label={label}>
              <h3>{label}</h3>
              <ul className="knowledge-dates-list">
                {dates.map(([name, date]) => (
                  <li key={name}>
                    <span>{name.replace(/^Claude /, '')}</span>
                    <time dateTime={date}>{date.length === 7 ? `${date.slice(0, 4)} 年 ${Number(date.slice(5))} 月` : date}</time>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      </dialog>
    </>
  );
}
