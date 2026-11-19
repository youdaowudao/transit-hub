"use client";
import { ReactNode } from "react";
import { Probe } from "@/lib/api";
import Icon from "@/components/Icon";
import ErrorFold from "@/components/ErrorFold";
import ElapsedTime, { DetectionTiming } from "@/components/ElapsedTime";
import { useI18n } from "@/lib/i18n";
export default function ProbeTimeline({
  probes,
  status,
  children,
  timing,
  progress,
}: {
  probes: Probe[];
  status: string;
  children?: ReactNode;
  timing?: DetectionTiming;
  progress?: { total: number | null; finished: number; statusTexts: Record<string, string>; unfinished: number; hasRequestFailure?: boolean; unconfirmedSelfReport?: boolean };
}) {
  const { t: tr } = useI18n();
  const questionProbes = probes.filter((p) => /^Q\d+$/.test(p.id));
  const finished = progress?.finished ?? probes.filter(
    (p) => !["queued", "running"].includes(p.status),
  ).length;
  const active = ["queued", "running"].includes(status);
  const total = progress ? progress.total : probes.length;
  const ratio = total ? Math.min(1, finished / total) : 0;
  const partialTitle = progress?.hasRequestFailure || probes.some(probe => probe.status === "error")
    ? tr("报告已生成，部分请求失败")
    : progress?.unconfirmedSelfReport
      ? tr("检测已结束，模型自报题尚未判分")
      : tr("检测已结束，部分项目未确认");
  const title =
    status === "queued"
      ? tr("任务已排队")
      : status === "running"
        ? tr("正在收集证据")
        : status === "cancelled"
          ? tr("检测已停止")
          : status === "partial"
            ? partialTitle
            : status === "interrupted"
              ? tr("检测已中断")
              : tr("检测报告已生成");
  return (
    <section
      className={
        "timeline " + (active ? "timeline-active" : "timeline-settled")
      }
      aria-label={tr("检测进度")}
    >
      <div className="section-heading">
        <div>
          <span className={"timeline-symbol " + (active ? "is-working" : "")}>
            <Icon
              name={active ? "spinner" : status === "done" ? "check" : "chart"}
            />
          </span>
          <div>
            <h2>{title}</h2>
            <p className="timeline-caption">
              {progress && <>已处理数包括无需提问的项目；实际结果请看逐题状态。{progress.unfinished > 0 && `另有${progress.unfinished}项未完成。`}</>}
              {timing ? <ElapsedTime timing={timing} active={active} description={progress ? '本报告从实际开始执行到保存的耗时；等待时间不计入，也不累加并发题目耗时。' : undefined} /> : active ? progress ? status === 'queued' ? '等待执行，尚未开始计时。' : '正在读取本次任务的时间记录。' : tr("Job 已创建，可以分享。") : ""}
            </p>
          </div>
        </div>
        <span className="progress-count">
          {total === null ? <span>正在读取项目总数</span> : <><b>{finished}</b><span> / {total}{progress ? "项已处理" : ""}</span></>}
        </span>
      </div>
      <div
        className="progress-track"
        role="progressbar"
        aria-label={tr("已完成探针")}
        aria-valuemin={0}
        aria-valuemax={total ?? undefined}
        aria-valuenow={total === null ? undefined : finished}
      >
        <div style={{ transform: "scaleX(" + ratio + ")" }} />
      </div>
      <div className="probe-grid">
        {probes.map((p, i) => (
          <div key={p.id} className={"probe probe-" + p.status}>
            <span className="probe-index">
              {String(i + 1).padStart(2, "0")}
            </span>
            <span className="probe-name">
              {/^Q\d+$/.test(p.id)
                ? "Q" +
                  (questionProbes.findIndex((q) => q.id === p.id) + 1) +
                  " · "
                : ""}
              {tr(p.name)}
            </span>
            <small className="probe-state">
              <span className="probe-dot" />
              {(progress?.statusTexts[p.id] ? tr(progress.statusTexts[p.id]) : undefined) ?? (p.status === "queued"
                ? tr("等待中")
                : p.status === "running"
                  ? tr("检测中")
                  : p.status === "done"
                    ? (p.ms / 1000).toFixed(1) + "s"
                    : p.status === "cancelled"
                      ? tr("已停止")
                      : tr("请求失败"))}
            </small>
            {p.error && <ErrorFold text={p.error} compact />}
          </div>
        ))}
      </div>
      <span className="sr-only" role="status">
        {progress ? total === null ? "正在读取项目总数" : `${title}，已处理${finished}项，共${total}项` : tr("{title}，已完成 {done} 项，共 {total} 项", { title, done: finished, total: probes.length })}
      </span>
      {children}
    </section>
  );
}
