export type HubKind = 'iq' | 'vision' | 'knowledge' | 'tools' | 'cache' | 'injection' | 'web' | 'thinking' | 'fingerprint_numeric' | 'fingerprint_behavior';
export const HUB_MAX_TOTAL_SECONDS = 9223371976;
export const HUB_KINDS: { value: HubKind; label: string }[] = [
  { value: 'iq', label: '推理题' }, { value: 'vision', label: '识图' }, { value: 'knowledge', label: '知识事件' },
  { value: 'tools', label: '工具调用' }, { value: 'cache', label: '缓存读写' }, { value: 'injection', label: '注入大小' },
  { value: 'web', label: '联网' }, { value: 'thinking', label: '思维摘要' },
  { value: 'fingerprint_numeric', label: '数字指纹' }, { value: 'fingerprint_behavior', label: '行为指纹' },
];
export function selectHubDefaultModel(models: string[], group: string): { model: string; notice: string } {
  if (!models.length) return { model: '', notice: '此账号没有返回可选择的模型，请检查上游模型列表后重新读取。' };
  const normalized = (value: string) => value.toLowerCase().replace(/[^a-z0-9\u4e00-\u9fff]/g, '');
  const family = normalized(group);
  const preferred = family.includes('gpt') ? '61sol' : (family.includes('claude') || family.includes('克劳德')) ? 'opus55' : '';
  if (!preferred) return { model: models[0], notice: '' };
  const match = models.find(model => normalized(model).includes(preferred));
  return match ? { model: match, notice: '' } : { model: models[0], notice: `本分组默认模型 ${preferred === '61sol' ? '6.1 sol' : 'opus 5.5'} 未在此账号列表中，已选择第一项；开始前可更换。` };
}
export function singaporeDay(value: number | Date): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Singapore', year: 'numeric', month: '2-digit', day: '2-digit' }).format(value);
}
export function sidebarDayLabel(createdAtMs: number, now = Date.now()): string | null {
  const day = singaporeDay(createdAtMs);
  if (day === singaporeDay(now)) return '今天';
  if (day === singaporeDay(now - 86400000)) return '昨天';
  return null;
}
export function reportStatus(status: string): string {
  return ({ queued: '等待执行', running: '检测中', done: '完成', partial: '已结束，部分项目未确认', cancelled: '已停止', interrupted: '异常中断', error: '失败' } as Record<string, string>)[status] ?? status;
}
export function isHubTerminal(status: string): boolean { return ['done', 'partial', 'cancelled', 'interrupted'].includes(status); }
export function reportFinishConfirmed(report: {status: string; processExited?: boolean; finishConfirmed?: boolean; executionPhase?: string}) {
  return report.executionPhase !== undefined || report.finishConfirmed !== undefined ? report.executionPhase === 'ended' && report.finishConfirmed === true : isHubTerminal(report.status) && report.processExited === true;
}
export function reportPollDecision(report: { status: string; processExited?: boolean; executionPhase?: string; finishConfirmed?: boolean; recoveryReason?: string; recoveryDeadlineMs?: number; stopConfirmDeadlineMs?: number; executionConfirmDeadlineMs?: number; serverNow?: number }, terminalSince: number | null, now: number): { poll: boolean; message: string; terminalSince: number | null } {
  if (report.executionPhase) {
    const phase = report.executionPhase, current = report.serverNow ?? now;
    const result = (poll: boolean, message: string) => ({ poll, message, terminalSince: null });
    const check = '请检查本实例systemd与原进程后按审核重新启动本调度站后端；已保存回答保留，原等待任务不自动重跑。';
    if (phase === 'paused') return result(false, `后续检测已暂停：${report.recoveryReason || '保存结束状态未完成'}。已保存结果保留，请核查并重试保存；不会自动重跑模型。`);
    if (phase === 'unknown' || phase === 'stopping' && report.stopConfirmDeadlineMs && current >= report.stopConfirmDeadlineMs || ['preparing', 'running'].includes(phase) && report.executionConfirmDeadlineMs && current >= report.executionConfirmDeadlineMs) return result(false, `停止或运行结果尚未确认：${report.recoveryReason || '程序退出状态尚未核实'}，检测可能仍在继续。自动核查已暂停。${check}`);
    if (phase === 'saving_end' || phase === 'saving_cancel') {
      if (report.recoveryDeadlineMs && current >= report.recoveryDeadlineMs) return result(false, `保存状态尚未确认：${report.recoveryReason || '等待保存时间已到'}。请核查并重试保存。`);
      return result(true, phase === 'saving_end' ? '这一份检测程序已停止，正在保存结束状态；只重试保存，不会再次发送检测题目。' : '正在保存这一份等待任务的取消状态，其他检测继续。');
    }
    if (phase === 'ended') return result(false, reportFinishConfirmed(report) ? '报告已保存；本次检测程序已结束，服务器不再保留这次检测进程。' : `报告结束状态尚未确认。${check}`);
    if (phase === 'queued') return result(true, '等待执行，轮到后自动开始；尚未发送检测题目，等待时间不计入任务总时长。');
    if (phase === 'preparing') return result(true, '任务已保存并排队，轮到后开始准备；准备或启动异常会显示在这份报告中，请勿重复提交。');
    if (phase === 'stopping') return result(true, '正在确认程序结束；尚不能确认已停止。');
    return result(true, '');
  }
  if (report.status === 'queued') return { poll: true, message: '等待执行，轮到后自动开始；尚未发送检测题目，等待时间不计入任务总时长。', terminalSince: null };
  if (report.processExited && !isHubTerminal(report.status)) return { poll: false, message: '检测程序已退出，但报告状态尚未保存；请处理提示的问题，再点击停止检测以重试保存状态，不会再次调用模型。', terminalSince };
  if (!isHubTerminal(report.status)) return { poll: true, message: '', terminalSince: null };
  if (report.processExited) return { poll: false, message: '报告已保存；本次检测程序已结束，服务器不再保留这次检测进程。', terminalSince };
  const since = terminalSince ?? now;
  return now - since < 15000 ? { poll: true, message: '报告已保存，正在确认检测程序退出。', terminalSince: since } : { poll: false, message: '报告已保存，但无法确认检测程序已退出；新检测已暂停，请点击核查运行状态。', terminalSince: since };
}
export function newHubID(prefix = ''): string { return prefix + Array.from(crypto.getRandomValues(new Uint8Array(16)), x => x.toString(16).padStart(2, '0')).join(''); }
