import { object } from './hub-report-view';
import type { HubCrossCheck, HubCrossCheckGrade, HubItem, HubPromptMatch, HubVisionStatus } from './hub-types';

export type CrossView = {
  phase: 'wait' | 'loading' | 'ok' | 'error' | 'stale';
  wait?: 'H3' | 'H3b';
  data?: HubCrossCheck;
  error?: string;
};

const defaultTemplate = '{{sample}}';
const placeholderID = 'sub2api-placeholder';
const placeholderNote = 'Sub2API 2026-05-04 前的版本用它，并会把不认识的模型名改成 gpt-5.4 发出。';
const dash = '—';
const noPrefix = '未加内容';
export const CROSS_UNKNOWN_ERROR = '未收到具体的失败原因。';
const number = (value: number) => value.toLocaleString('zh-CN');
const diff = (match: HubPromptMatch) => number(Math.abs(match.diff));
const wrappedTemplate = (template: unknown): string => typeof template === 'string' && template !== '' ? template : defaultTemplate;

export const displayTime = (value: number, full = false) => new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Singapore', ...(full ? { year: 'numeric', month: '2-digit', day: '2-digit' } as const : {}), hour: '2-digit', minute: '2-digit', hour12: false }).format(value);

export const INJECTION_NOTE = '注入大小：只发一个字母时，上游算了多少输入 token。多出来的部分是上游加在你消息前面的内容，程序看不到原文，只能按长度推断。分数沿用原分档。';
export const INJECTION_TEXT = {
  impactContext: '加内容在订阅反代中很常见，本身不说明模型是假的；模型是否可信请看报告顶部的“判读小结”。',
  promptTitle: '查看这份提示词全文',
  promptLoading: '正在读取提示词全文，不会重新检测。',
  requestsTitle: '两次请求明细与算式',
  requestsColumns: ['轮次', '样本字符', '发送内容', '输入', '其中缓存读写', '合计'],
  scoring: '<50：10分 · 50～1000：8分 · 1001～3000：6分 · 3001～6000：4分 · 6001～10000：2分 · >10000：0分',
};

export const CROSS_CHECK_TEXT = {
  title: '判读小结',
  introduction: '把注入、模型自报、知识和识图放在一起看，并与同一模型在其他账号的报告对照。只读已保存结果，不重新检测，不修改主站。',
  H3: '检测结束后生成判读小结。',
  H3b: '题目读完后生成判读小结。',
  loading: '正在读取同一模型的其他报告进行对照……',
  stale: '报告在读取对照期间有更新，判读对照暂未生成；请点击下方按钮重新读取报告与对照。',
  retry: '重新读取对照',
  refresh: '重新读取报告与对照',
  evidenceTitle: '依据',
  explanation: '判读只用固定规则，不使用 AI；“同一模型”指前后 14 天内创建、其他账号、模型名相同的报告，最多 20 份。',
  routeColumns: ['固定开销', '上游加的内容', '题数', '题目与各题固定开销', '按长度识别'],
  peersColumns: ['创建时间', '账号', '上游加的内容', '答对的最晚事件', '识图'],
  noPeers: '前后 14 天内没有同一模型其他账号的报告。',
  rulesTitle: '判读规则',
  rules: 'A：没有任何异常。B：只有“线路不稳定”。C：有“提示词不对应”“自报 GPT-5”“知识更旧”“识图不符”中的任意一项。D：有“知识更旧”，并且至少还有另外一项。E：报告未结束，或注入、逐题核对、知识、模型自报都没有可用结果。',
};

export function crossError(error?: string): string {
  return `${error || CROSS_UNKNOWN_ERROR} 判读对照读取失败，不影响下面的检测结果。`;
}

export function conclusionTitle(grade: HubCrossCheckGrade): string {
  return `结论：${gradeTitle(grade)}`;
}

export function peersTitle(count: number): string {
  return `同一模型其他账号的报告（${number(count)} 份）`;
}

export function promptFullNote(match: HubPromptMatch, source: string): string {
  return match.exact
    ? `这是程序附带的已知提示词全文，按长度推断上游加的就是它，不是截获的原文。来源：${source}。`
    : `这是程序附带的已知提示词全文。按长度判断，上游加的内容与它接近（相差 ${diff(match)} token），可能是它的改动版本；这不是截获的原文。来源：${source}。`;
}

export function promptFullError(error: unknown): string {
  const reason = error instanceof Error ? error.message : typeof error === 'string' ? error : '';
  return `${reason || CROSS_UNKNOWN_ERROR} 提示词全文读取失败，可收起后再展开重试；不会重新检测。`;
}

type UsagePoint = { n: number; input: number; total: number };

function validPoint(value: unknown, length: number): UsagePoint | undefined {
  const { n, input, total } = object(value);
  if (n === length && Number.isSafeInteger(n) && Number.isSafeInteger(input) && Number.isSafeInteger(total) && input >= 0 && total >= input) {
    return { n, input, total };
  }
}

function pointsInOrder(points: unknown, template: unknown, usableCount: 0 | 1 | 2 = 2): { a?: UsagePoint; b?: UsagePoint } {
  if (usableCount === 0 || !Array.isArray(points)) return {};
  const encoder = new TextEncoder();
  const rendered = wrappedTemplate(template);
  const a = validPoint(points[0], encoder.encode(rendered.replaceAll(defaultTemplate, 'x')).length);
  if (!a) return {};
  const b = usableCount === 2 ? validPoint(points[1], encoder.encode(rendered.replaceAll(defaultTemplate, `${'x '.repeat(799)}x`)).length) : undefined;
  return { a, b };
}

export function usablePoints(item: HubItem): { a?: UsagePoint; b?: UsagePoint } {
  // JSON decoding can collapse a stored decimal like 5.0 into the integer 5.
  return pointsInOrder(object(item.result?.evidence).points, item.definition.template, item.usableInjectionPoints ?? 0);
}

export function injectionFactLine(points: unknown, template: unknown): string {
  const { a } = pointsInOrder(points, template);
  if (!a) return '';
  return wrappedTemplate(template) === defaultTemplate
    ? `你只发了 1 个字母 x，上游算了 ${number(a.total)} 个输入 token；不加内容的线路只算 5～7 个。`
    : `你发的是修改过的注入模板（其中填入 1 个字母 x），上游算了 ${number(a.total)} 个输入 token；不加内容的线路只比模板文字本身多 4～6 个。`;
}

export function prefixLine(cross: CrossView): string {
  switch (cross.phase) {
    case 'wait':
    case 'loading':
      return '正在等待判读结果，读到后显示上游加了多少内容。';
    case 'error':
    case 'stale':
      return '判读结果读取失败，暂不能说明上游加了多少内容；可在报告顶部重新读取。';
    case 'ok': {
      const { reference, referenceUnavailableReason } = cross.data!;
      if (!reference) return `无法计算上游加了多少内容：${referenceUnavailableReason}。`;
      if (reference.prefixTokens === 0) return '上游没有在请求 A 前加内容。其他题是否走同一线路，见报告顶部“判读小结”的线路一项。';
      return `多出的约 ${number(reference.prefixTokens)} 个 token 是上游加在请求 A 前面的内容。`;
    }
  }
}

export function impactLine(cross: HubCrossCheck): string | null {
  const ref = cross.reference;
  if (!ref || ref.prefixTokens <= 0) return null;
  const cache = ref.aCache === 0 ? '本次请求 A 没有缓存读写记录。' : `本次请求 A 的合计里有 ${number(ref.aCache)} 个 token 记为缓存读写。`;
  return `影响：走这条线路的请求，每次都要多算约 ${number(ref.prefixTokens)} 个输入 token，这段内容也会占用上下文；其他题是否走同一线路，见报告顶部“判读小结”的线路一项。${cache}`;
}

export function promptLines(cross: HubCrossCheck, model: string): string[] {
  const ref = cross.reference;
  if (!ref || ref.prefixTokens <= 0) return [];
  const match = ref.match;
  if (!match) return ['这段内容的长度不在已知提示词库里，无法判断是什么。'];
  if (match.id === placeholderID) {
    return [match.exact
      ? `按长度判断，这段内容与 Sub2API 2026-05-04 之前版本的占位提示词吻合（相差 ${diff(match)} token）。那些旧版本会把不认识的模型名悄悄改成 gpt-5.4 再发出。`
      : `按长度判断，这段内容接近 Sub2API 2026-05-04 之前版本的占位提示词（相差 ${diff(match)} token），可能是它的改动版本。那些旧版本会把不认识的模型名悄悄改成 gpt-5.4 再发出。`];
  }
  const main = match.exact
    ? `按长度判断，这段内容与「${match.name}」吻合（相差 ${diff(match)} token）。`
    : `按长度判断，这段内容接近「${match.name}」（相差 ${diff(match)} token），可能是它的改动版本。`;
  const identity = match.identity ? `这份提示词写明模型“基于 ${match.identity}”。` : '';
  const expected = cross.expectedPromptIds;
  if (expected.length === 0) return [main + identity, '这个模型不在本程序整理的提示词对照表里，无法判断这份提示词是否对应。'];
  if (expected.includes(match.id)) return [main + identity, `按本程序参照 Sub2API 配置整理的对照表，${model} 正是配这份提示词。`];
  return [main + identity, `按本程序参照 Sub2API 配置整理的对照表，${model} 应配「${cross.expectedPromptNames[0]}」，本次配的却是这一份：可能是上游软件版本较旧，也可能实际发出的模型不是 ${model}。上游若用别的软件或别的配置，结论可能不同。`];
}

function requestNote(evidence: Record<string, unknown>, b: UsagePoint | undefined, itemError: unknown): string {
  if (!b) {
    const error = typeof itemError === 'string' ? itemError : typeof evidence.error === 'string' ? evidence.error : '';
    return `只取得了请求 A，无法连线计算斜率，上面的说明只按请求 A 计算。${error}`;
  }
  const slope = typeof evidence.slope === 'number' ? evidence.slope.toFixed(3) : dash;
  if (evidence.reliable !== true) return `两次请求的结果连不成合理的直线（斜率 ${slope}），可能被分到了不同线路，这里的“注入大小”不可靠，上面的说明只按请求 A 计算。`;
  const injection = typeof evidence.inj === 'number' ? number(evidence.inj) : dash;
  return `两次合计连成一条直线：每多 1 个字符多算 ${slope} 个 token，截距约 ${injection}，即“注入大小”。斜率约 0.5 说明上游对 x 的计数正常（每个“x 加空格”约 1 个 token）。`;
}

export function requestRoundOverhead(evidence: unknown): string | null {
  const value = object(evidence).round_overhead;
  return typeof value === 'number' ? `每轮新增包装修正 ${value.toFixed(2)} token` : null;
}

export function requestCell(value: unknown): string | number | undefined {
  if (typeof value === 'number' && Number.isInteger(value)) return number(value);
  if (typeof value === 'number' || typeof value === 'string') return value;
  return value === undefined || value === null || typeof value === 'boolean' ? undefined : JSON.stringify(value);
}

export function requestTable(value: unknown, template: unknown, itemError?: unknown, usableCount?: 0 | 1 | 2): { rows: unknown[][]; note: string } {
  const evidence: Record<string, unknown> = object(value);
  const points = Array.isArray(evidence.points) ? evidence.points : [];
  const { a, b } = pointsInOrder(points, template, usableCount);
  const sent = wrappedTemplate(template) === defaultTemplate
    ? ['1 个字母 x', '800 个 x（1,599 个字符）']
    : ['修改后的模板，填入 1 个 x', '修改后的模板，填入 800 个 x'];
  const rows = points.map((value, index) => {
    const p = object(value);
    const point = [a, b][index];
    return [p.round || index + 1, p.n, sent[index] ?? dash, p.input, point ? point.total - point.input : dash, p.total];
  });
  if (points.length === 1) {
    const missing = '未取得';
    rows.push([2, missing, sent[1], missing, missing, missing]);
  }
  return { rows, note: requestNote(evidence, b, itemError) };
}

export type CheckLine = { code: string; group: number | null; choice: '是' | '不是' | null };
type Group = NonNullable<HubCrossCheck['groups']>[number];

const routeGroups = (cross: HubCrossCheck): Group[] => [...(cross.groups ?? [])].sort((a, b) => a.overhead - b.overhead);

function mainPrompt(cross: HubCrossCheck) {
  if (cross.reference) return cross.reference;
  let main: Group | undefined;
  for (const group of cross.groups ?? []) {
    if (!main || group.count > main.count || (group.count === main.count && group.overhead < main.overhead)) main = group;
  }
  return main;
}

function mainPromptCode(cross: HubCrossCheck, main: ReturnType<typeof mainPrompt>): string {
  if (!main) return 'prompt.unavailable';
  if (main.prefixTokens === 0) return 'prompt.none';
  if (main.match?.id === placeholderID) return 'prompt.placeholder';
  if (!main.match) return 'prompt.unknown';
  if (!cross.expectedPromptIds.length) return 'prompt.norule';
  return cross.expectedPromptIds.includes(main.match.id) ? 'prompt.ok' : 'prompt.mismatch';
}

export function checkLines(cross: HubCrossCheck): CheckLine[] {
  const groups = routeGroups(cross);
  const lines: CheckLine[] = [];
  const add = (code: string, group: number | null = null, choice: CheckLine['choice'] = null) => lines.push({ code, group, choice });
  if (cross.perItemChecked) {
    add(groups.length > 1 ? 'route.mixed' : groups.length === 1 ? 'route.single' : 'route.nodata');
  } else {
    add(cross.injectionReliable === false && cross.injectionPoints >= 2 ? 'route.unreliable' : 'route.unchecked');
  }

  const main = mainPrompt(cross);
  add(mainPromptCode(cross, main));
  if (main) {
    for (const group of groups) {
      if (group === main || (Math.abs(group.overhead - main.overhead) <= 6 && group.match?.id === main.match?.id)) continue;
      const key = group.overhead;
      if (group.prefixTokens === 0) add('prompt.other.none', key);
      else if (!group.match) add('prompt.other.unknown', key);
      else if (group.match.id === placeholderID) add('prompt.other.placeholder', key);
      else if (cross.expectedPromptIds.length === 0) add('prompt.other.match.norule', key);
      else add('prompt.other.match', key, cross.expectedPromptIds.includes(group.match.id) ? '是' : '不是');
    }
  }

  const { present, status, earned, byPrompt } = cross.selfReport;
  if (present && status === 'not_applicable') add('self.na');
  else if (present && status === 'known' && earned !== null && Number.isSafeInteger(earned) && earned >= 0) {
    add(earned === 0 ? 'self.gpt5' : 'self.ok');
    if (earned === 0 && byPrompt) add(cross.perItemChecked ? 'self.gpt5.byPrompt' : 'self.gpt5.byInjection');
  } else add(present ? 'self.unconfirmed' : 'self.none');

  const { rows, contradictions, peerReports } = cross.knowledge;
  if (rows === 0) add('knowledge.none');
  else if (contradictions.length) add('knowledge.older');
  else add(peerReports === 0 ? 'knowledge.nopeer' : 'knowledge.ok');

  if (cross.codes.includes('vision_mismatch')) add('vision.mismatch');
  else add(cross.vision.status === 'none' || cross.vision.peerFull === 0 ? 'vision.none' : 'vision.ok');
  return lines;
}

function matchLabel(match: HubPromptMatch | null | undefined): string {
  if (!match) return '';
  return match.exact ? `「${match.name}」` : `接近「${match.name}」的内容（相差 ${diff(match)} token）`;
}

function promptLineText(line: CheckLine, cross: HubCrossCheck): string {
  if (line.code === 'prompt.unavailable') return `上游加的内容：无法计算，${cross.referenceUnavailableReason}。`;
  if (line.group !== null) {
    const group = cross.groups?.find(group => group.overhead === line.group);
    if (!group) return '';
    const subject = `另有 ${number(group.count)} 道题（固定开销约 ${number(group.overhead)} token）`;
    const name = matchLabel(group.match);
    switch (line.code) {
      case 'prompt.other.none': return `${subject}没有加内容。`;
      case 'prompt.other.unknown': return `${subject}加了约 ${number(group.prefixTokens)} token 的未识别内容。`;
      case 'prompt.other.match': return `${subject}配的是${name}，${line.choice} ${cross.model} 应配的提示词。`;
      case 'prompt.other.match.norule': return `${subject}配的是${name}。`;
      case 'prompt.other.placeholder': return `${subject}配的是${name}。${placeholderNote}`;
    }
  }
  const main = mainPrompt(cross);
  if (!main) return '';
  const subject = 'count' in main ? `${number(main.count)} 道题（固定开销约 ${number(main.overhead)} token）` : '注入检测请求 A ';
  const start = `上游加的内容：${subject}`;
  const name = matchLabel(main.match);
  switch (line.code) {
    case 'prompt.none': return `${start}没有加内容。`;
    case 'prompt.placeholder': return `${start}配的是${name}。${placeholderNote}`;
    case 'prompt.unknown': return `${start}加了约 ${number(main.prefixTokens)} token，不在已知提示词库里，无法判断是什么。`;
    case 'prompt.norule': return `${start}配的是${name}；这个模型不在本程序整理的对照表里，无法判断是否对应。`;
    case 'prompt.ok': return `${start}配的是${name}，与 ${cross.model} 应配的提示词一致。`;
    case 'prompt.mismatch': return `${start}配的是${name}，按本程序参照 Sub2API 配置整理的对照表，这不是 ${cross.model} 应配的提示词。`;
    default: return '';
  }
}

export function lineText(line: CheckLine, cross: HubCrossCheck): string {
  if (line.code.startsWith('prompt.')) return promptLineText(line, cross);
  switch (line.code) {
    case 'route.mixed': {
      const groups = routeGroups(cross);
      return `线路：核对的题目走了 ${number(groups.length)} 条不同线路（固定开销分别约 ${groups.map(group => number(group.overhead)).join('、')} token）。同一模型名下的请求被分到了不同来源。`;
    }
    case 'route.single': return `线路：本次核对的 ${number(cross.checked)} 道题都走同一条线路。`;
    case 'route.nodata': return '线路：本次没有可核对用量的题目，无法判断。';
    case 'route.unreliable': return '线路：注入检测的两次请求结果连不成合理的直线，可能被分到了不同线路。';
    case 'route.unchecked': return '线路：这份报告没有逐题核对，无法判断。';
    case 'self.gpt5': return '模型自报：回答自称 GPT-5 系列，与所选的 GPT-6 系列不符。';
    case 'self.gpt5.byPrompt': return '按长度判断，这道题的请求里上游加的提示词写着“基于 GPT-5”，自报很可能来自提示词，单凭这一项不能断定型号。';
    case 'self.gpt5.byInjection': return '按长度判断，注入检测的请求里上游加的提示词写着“基于 GPT-5”。这份报告没有逐题核对，无法确认这道题的请求是否也带着它；单凭自报不能断定型号。';
    case 'self.ok': return '模型自报：没有发现与所选模型矛盾的说法（自报不证明实际型号）。';
    case 'self.unconfirmed': return '模型自报：回答无法按规则确认，请展开模型自报题查看原话。';
    case 'self.none': return '模型自报：本次没有这道题或不适用。';
    case 'self.na': return `模型自报：这道题只检查选测 GPT-6 系列时的自报矛盾，${cross.model} 不在适用范围，本次未发题。`;
    case 'knowledge.older': {
      const events = cross.knowledge.contradictions.map(row => `${row.event}（${row.date}，${number(row.knownBy)} 份答对）`).join('；');
      return `知识对照：以下事件本报告答“不知道”或答错，而同一模型其他账号有至少 2 份报告答对了它、并且还答对了至少晚 60 天的事件，问过它的其他报告也没有答“不知道”或答错：${events}。说明这里的模型知识更旧。`;
    }
    case 'knowledge.ok': return `知识对照：与同一模型其他账号的 ${number(cross.knowledge.peerReports)} 份报告没有矛盾。`;
    case 'knowledge.nopeer': return '知识对照：前后 14 天内没有同一模型其他账号的报告，无法对照。';
    case 'knowledge.none': return '知识对照：本次没有知识题结果。';
    case 'vision.mismatch': return `识图对照：本报告识图 0 分，而同一模型其他账号有 ${number(cross.vision.peerFull)} 份报告满分。`;
    case 'vision.ok': return '识图对照：没有发现与同一模型其他报告矛盾的地方。';
    case 'vision.none': return '识图对照：本次没有识图结果，或没有可对照的报告。';
    default: return '';
  }
}

const prefixText = (tokens: number | null): string => tokens === null ? dash : tokens === 0 ? noPrefix : `约 ${number(tokens)} token`;

function routeMatch(group: Group): string {
  if (group.prefixTokens === 0) return dash;
  if (!group.match) return '未识别';
  return `${group.match.exact ? '' : '接近 '}${group.match.name}（相差 ${diff(group.match)}）`;
}

export function routeDetail(cross: HubCrossCheck, items: HubItem[]): { title: string; lines: string[]; rows: string[][] } {
  const heading = '逐题核对';
  if (!cross.perItemChecked) {
    const line = {
      stale: '本报告在逐题核对之后又有题目更新（例如补测中断），已保存的逐题核对可能过时，暂不使用。',
      unavailable: `逐题核对没有完成：${cross.prefixReason}。`,
      invalid: '已保存的逐题核对记录不完整，或与本报告的题目对不上，暂不使用。',
      none: '这份报告没有可用的逐题核对记录（本版本之前完成的报告，或补测没有正常结束）；本版本之后完成的检测或补测会显示。',
    };
    return { title: heading, lines: [cross.prefixStatus === 'ok' ? line.none : line[cross.prefixStatus]], rows: [] };
  }
  const groups = routeGroups(cross);
  if (groups.length === 0) {
    return { title: heading, lines: ['本次没有可核对用量的题目。可核对的是保存了用量记录、不带图片的推理题、知识题和模型自报题。'], rows: [] };
  }

  const range = (group: Group) => group.overhead === group.overheadMax ? number(group.overhead) : `${number(group.overhead)}～${number(group.overheadMax)}`;
  const checked = number(cross.checked);
  const first = groups[0];
  const singleRange = first.overhead === first.overheadMax ? `都是 ${range(first)} token` : `都在 ${range(first)} token 之间`;
  const lines = [
    '每道题的输入总数减去题面本身，剩下的就是上游每次额外算的固定开销。同一条线路上，各题的固定开销相同；带系统提示的推理题在不加内容的线路上会多 4 个左右。',
    groups.length === 1
      ? `所有 ${checked} 道题的固定开销${singleRange}，说明这 ${checked} 道题都走同一条线路。`
      : `核对的题目走了 ${number(groups.length)} 条不同线路，同一个模型名下的请求被分到了不同来源。`,
  ];
  const titles = new Map(items.map(item => [item.itemId, item.definition.title]));
  const rows = groups.map(group => [
    range(group), prefixText(group.prefixTokens), number(group.count),
    group.items.map(item => `${titles.get(item.itemId) || item.itemId}（${number(item.overhead)}）`).join('、'),
    routeMatch(group),
  ]);
  return { title: `${heading}（${checked} 题）`, lines, rows };
}

const gradeTitles: Record<HubCrossCheckGrade, string> = {
  A: 'A 未见异常',
  B: 'B 线路不稳定',
  C: 'C 有异常，需复核',
  D: 'D 多项证据指向不是所选模型',
  E: 'E 无法判断',
};

export function gradeTitle(grade: HubCrossCheckGrade): string {
  return gradeTitles[grade];
}

export function gradeText(grade: HubCrossCheckGrade, model: string): string {
  return {
    A: '上游加的内容、模型自报、知识和识图都没有发现与所选模型矛盾的地方。',
    B: '核对的题目走了不同线路，或注入检测的两次结果显示可能如此；其他检查项没有发现矛盾。',
    C: '发现了异常，但这些还不能断定模型不对，请结合下面的检查项复核。',
    D: `知识明显旧于同一模型在其他账号的报告，而且还有其他异常；实际提供的可能不是 ${model}。`,
    E: '关键检测缺失或失败，无法判断。',
  }[grade];
}

const visionText: Record<HubVisionStatus, string> = { full: '满分', partial: '部分得分', zero: '0 分', none: dash };

export function peerRows(cross: HubCrossCheck): Array<{ time: string; account: string; prefix: string; latest: string; vision: string }> {
  return cross.peers.map(peer => ({
    time: displayTime(peer.createdAtMs, true),
    account: peer.accountName,
    prefix: prefixText(peer.prefixTokens),
    latest: peer.latestKnown ? `${peer.latestKnown.event}（${peer.latestKnown.date}）` : dash,
    vision: visionText[peer.vision],
  }));
}
