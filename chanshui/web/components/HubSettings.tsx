"use client";
import { useCallback, useEffect, useState } from 'react';
import Select from './Select';
import { HUB_KINDS, HUB_MAX_TOTAL_SECONDS, newHubID, type HubKind } from '../lib/hub-policy';
import type { HubCall, HubPage, HubQuestion, HubQuestionSummary, HubSettings as Settings } from '../lib/hub-types';

const labels: Record<string, string> = {
  pairs: '随机数字对', father: '父亲读数', daughter: '女儿读数', remark: '此组说明',
  ca: '圆形苹果数', ct: '圆形桃子数', cm: '圆形西瓜数', sa: '星形苹果数', st: '星形桃子数', sm: '星形西瓜数',
  min: '随机最小库存', max: '随机最大库存', colorsMin: '最少颜色数', colorsMax: '最多颜色数', stockMin: '每色最少袜数', stockMax: '每色最多袜数', pairsMin: '最少配对数', pairsMax: '最多配对数', colors: '可用颜色名',
  lines: '图内六行说明', date: '事件日期', event: '事件名称', choices: '答案选项', text: '选项文字', kind: '此答案代表', knowAliases: '正确答案别名', wrongAliases: '错误答案别名', unknownAliases: '不知道答案别名',
  lhs: '左侧整数', rhs: '右侧整数', prefixHeading: '固定前缀说明', prefixLineTemplate: '前缀行模板', turnTemplate: '每轮提示模板', deepSeekQuery: 'DeepSeek 搜索文字',
  openings: '开头片段', actions: '动作片段', endings: '结尾片段', separatorHints: '分隔符说明', generator: '题目生成方式', slot: '探针位置', probeKind: '行为判定方式',
};
const fixedInfo: Record<HubKind, string> = {
  iq: '参数题只支持列出的范围和占位符。原题使用原判分规则时满分固定；改变题意后请选通用判题或人工查看。',
  vision: '固定40根K线、1360×1000图片，每次1张。只编辑图内六行；随包字体缺字、换行或文字放不下时不能保存。数字和图片使用同一随机种子。',
  knowledge: '日期1900～2100年，选项2～8个，恰好一个正确且至少一个不知道。完整原题库沿用原串行二分、最多10问；改题、禁用或只选部分题时逐题检测已选集合。原事件判定不参与总分。',
  tools: '整数1～1000；工具名称、参数格式、随机校验值和调用结果往返检查固定。普通文字声称调用工具不算通过。',
  cache: '原版缓存默认不选，首页勾选可临时执行未修改的原题；在这里明确禁用或删除后仍跳过。固定170行前缀、6轮串行，前缀在各轮保持不变。改文字可能不满足上游缓存门槛；未确认不会冒充命中。',
  injection: '固定两次独立样本：1份、800份 x 加空格。同模板用于两次采样，结果含协议包装开销，不表示恶意注入。',
  web: '搜索工具、接口路径及来源校验固定；不会只因模型自称联网就判通过。通用模板必须保留 {{today}}。',
  thinking: '仅对适用的Claude模型检查返回的可见思维摘要，不声称得到全部内部推理；不做答案自评分。',
  fingerprint_numeric: '固定数字1～355、数量292～332，至少3个有效样本、最多10个。改提示或片段后仍可采证，但原参考库归因不再适用，恢复默认后才恢复比较。',
  fingerprint_behavior: '原3个探针及特征算法固定。可逐题修改或删除；改提示或启用组合后不作原库身份归因。自报能力分仅作风格参考，不计推理分。',
};
const generatorLabels: Record<string, string> = { static: '普通文字题', color_blind_v1: '红绿色盲', candy_classic_v1: '经典糖果', candy_variant_v1: '糖果变体', socks_v1: '袜子配对', chart_v1: 'K线图片', event_v1: '知识事件', tools_v1: '工具往返', cache_v1: '缓存六轮', injection_v1: '两次样本', web_v1: '联网证据', thinking_v1: '思维摘要', fingerprint_numeric_v1: '数字采样', fingerprint_behavior_v1: '行为参考' };
const message = (e: unknown) => e instanceof Error ? e.message : '操作未完成，原因尚未确认；请重试读取并核查运行诊断。';
const copy = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

function ParameterField({ name, value, change, path = '' }: { name: string; value: unknown; change: (value: unknown) => void; path?: string }) {
  const label = labels[name] ?? (path ? name : '参数');
  if (['generator', 'slot', 'probeKind'].includes(name)) return <p className="muted">{label}：{generatorLabels[String(value)] ?? (name === 'slot' ? String(Number(value) + 1) : '使用原特征规则，不可改动')}</p>;
  if (Array.isArray(value)) return <div className="hub-parameter-array"><strong>{label}</strong>{value.map((item, index) => <div className="hub-array-row" key={index}><span className="muted">{name === 'choices' ? String.fromCharCode(65 + index) : `${index + 1}`}</span><div className="hub-array-content"><ParameterField name={name === 'lines' ? `第${index + 1}行` : '内容'} value={item} change={next => change(value.map((old, i) => i === index ? next : old))} path={`${path}/${name}`} /></div>{name !== 'lines' && <button type="button" className="btn-secondary hub-small" onClick={() => change(value.filter((_, i) => i !== index))}>移除此项</button>}</div>)}{name !== 'lines' && <button type="button" className="btn-secondary hub-small" onClick={() => change([...value, name === 'pairs' ? { father: '', daughter: '', remark: '' } : name === 'choices' ? { text: '', kind: 'unknown' } : ''])}>增加一项</button>}</div>;
  if (value && typeof value === 'object') return <div className="hub-parameter-object">{Object.entries(value as Record<string, unknown>).map(([key, item]) => <ParameterField key={key} name={key} path={`${path}/${name}`} value={item} change={next => change({ ...(value as Record<string, unknown>), [key]: next })} />)}</div>;
  if (typeof value === 'boolean') return <label className="hub-checkbox"><input type="checkbox" checked={value} onChange={e => change(e.target.checked)} />{label}</label>;
  if (typeof value === 'number') return <div className="field"><label>{label}<input type="number" step="1" value={value} onChange={e => change(Number(e.target.value))} /></label></div>;
  if (name === 'kind') return <div className="field"><label>{label}<Select value={String(value)} onChange={e => change(e.target.value)}><option value="know">正确</option><option value="wrong">错误</option><option value="unknown">不知道</option></Select></label></div>;
  return <div className="field"><label>{label}{name === 'date' ? <input type="date" value={String(value ?? '')} min="1900-01-01" max="2100-12-31" onChange={e => change(e.target.value)} /> : <textarea rows={String(value ?? '').length > 100 ? 4 : 2} value={String(value ?? '')} onChange={e => change(e.target.value)} />}</label></div>;
}

function blankQuestion(kind: 'iq' | 'knowledge'): HubQuestion {
  return { id: newHubID('custom:'), title: '', kind, enabled: true, order: 1000, maxScore: 0, template: '', parameters: kind === 'knowledge' ? { generator: 'event_v1', date: new Date().toISOString().slice(0, 10), event: '', choices: [{ text: '', kind: 'know' }, { text: '不知道', kind: 'unknown' }], knowAliases: [], wrongAliases: [], unknownAliases: [] } : { generator: 'static' }, judge: { type: 'manual', referenceAnswer: '' } };
}

export function HubQuestionEditor({ original, call, saved, cancelled, onDirtyChange }: { original: HubQuestion; call: HubCall; saved: (kind: HubKind) => void; cancelled: () => void; onDirtyChange: (dirty: boolean) => void }) {
  const [draft, setDraft] = useState(() => copy(original));
  const [error, setError] = useState(''), [busy, setBusy] = useState(false), [preview, setPreview] = useState<{ prompt?: string; expected?: string; seed?: number; image?: string } | null>(null);
  const set = <K extends keyof HubQuestion>(name: K, value: HubQuestion[K]) => { setDraft(old => ({ ...old, [name]: value })); setPreview(null); };
  const changeContent = (name: 'template' | 'parameters', value: HubQuestion[typeof name]) => {
    setDraft(old => ({ ...old, [name]: value, ...((old.judge.type === 'legacy' && (name === 'template' || old.id === 'Q6' || old.kind === 'vision')) ? { maxScore: 0, judge: { type: 'manual', referenceAnswer: old.judge.expectedText ?? '' } as HubQuestion['judge'] } : {}) })); setPreview(null);
  };
  const dirty = JSON.stringify(original) !== JSON.stringify(draft);
  useEffect(() => { onDirtyChange(dirty); }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);
  useEffect(() => { const before = (e: BeforeUnloadEvent) => { if (dirty) { e.preventDefault(); e.returnValue = ''; } }; window.addEventListener('beforeunload', before); return () => window.removeEventListener('beforeunload', before); }, [dirty]);
  const cancel = () => { if (!dirty || confirm('有未保存的题目修改，确定放弃吗？')) cancelled(); };
  const run = async (op: 'save' | 'preview' | 'restore') => {
    setError(''); setBusy(true);
    try {
      if (op === 'preview') { setPreview(await call('preview-question', { definition: draft, ...(preview?.seed === undefined ? {} : { seed: preview.seed }) })); }
      else { await call(original.revision ? 'save-question' : 'create-question', { definition: draft, revision: original.revision, ...(op === 'restore' ? { restoreDefault: true } : {}) }); saved(draft.kind); }
    } catch (e) { setError(message(e)); } finally { setBusy(false); }
  };
  const remove = async () => {
    if (!confirm(`删除“${draft.title}”？只影响后续检测，旧报告仍保留；重启或升级不会自动加回此题。`)) return;
    setError(''); setBusy(true); try { await call('delete-question', { id: original.id, revision: original.revision }); saved(draft.kind); } catch (e) { setError(message(e)); } finally { setBusy(false); }
  };
  const scoring = draft.kind === 'iq' || draft.kind === 'vision';
  return <section className="hero-card hub-card hub-editor" data-testid="hub-question-editor"><div className="section-heading"><h2>{original.revision ? '编辑题目' : '新增题目'}</h2><button type="button" className="btn-secondary" onClick={cancel}>取消</button></div>
    <p className="muted">保存只影响新检测。进行中的任务和旧报告使用原题目快照；原版推理题先按本地规则判分，零分时可能将参考答案发给被测模型自评；识图和自定义题按各自规则处理。</p>
    <div className="connection-grid"><div className="field"><label>题目名称<input value={draft.title} maxLength={100} onChange={e => set('title', e.target.value)} /></label></div><div className="field"><label>排序（数字小的在前）<input type="number" min="0" max="999999" value={draft.order} onChange={e => set('order', Number(e.target.value))} /></label></div></div>
    <label className="hub-checkbox"><input type="checkbox" checked={draft.enabled} onChange={e => set('enabled', e.target.checked)} />启用：后续检测可选择此题</label>
    <p className="hub-note">{fixedInfo[draft.kind]}</p>
    {draft.id === 'knowledge:olympics' && <p className="muted">原奥运会预设的A、B都代表正确答案，原选项保留兼容；修改选项后必须恰好一个正确选项。</p>}
    {draft.kind !== 'vision' && draft.kind !== 'cache' && <div className="field"><label>题干／提示模板<textarea rows={6} value={draft.template} onChange={e => changeContent('template', e.target.value)} /></label><p className="muted">参数题请保留双花括号中的字段。更改原题题干后自动转为人工查看，您可明确选择通用判题。</p></div>}
    <details className="fold" open><summary>题目参数与固定说明</summary><div className="hub-form-grid">{Object.entries(draft.parameters).map(([name, value]) => <ParameterField key={name} name={name} value={value} change={next => changeContent('parameters', { ...draft.parameters, [name]: next })} />)}</div></details>
    {scoring && <details className="fold" open><summary>检测端判题与参考答案</summary><div className="hub-form-grid"><div className="field"><label>判题方式<Select value={draft.judge.type} onChange={e => { const type = e.target.value as HubQuestion['judge']['type']; setDraft(old => ({ ...old, maxScore: type === 'manual' ? 0 : type === 'legacy' ? original.maxScore : old.maxScore || 10, judge: { ...old.judge, type, ...(type === 'keywords' ? { match: old.judge.match ?? 'all', required: old.judge.required ?? [], forbidden: old.judge.forbidden ?? [] } : {}) } })); }}><option value="manual">人工查看，不计分</option><option value="text">精确文本</option><option value="number">数值</option><option value="keywords">关键词</option>{original.judge.type === 'legacy' && <option value="legacy">原题判分规则，满分固定</option>}</Select></label></div>
      <div className="field"><label>满分<input type="number" min={draft.judge.type === 'manual' ? 0 : 1} max="100" disabled={draft.judge.type === 'manual' || draft.judge.type === 'legacy'} value={draft.maxScore} onChange={e => set('maxScore', Number(e.target.value))} /></label>{draft.judge.type === 'legacy' && <p className="muted">此题使用原判分规则，满分固定；改用通用判题后才可修改满分。</p>}</div>
      {draft.judge.type === 'manual' && <div className="field"><label>参考答案（可空）<textarea rows={3} value={draft.judge.referenceAnswer ?? ''} onChange={e => set('judge', { ...draft.judge, referenceAnswer: e.target.value })} /></label><p className="muted">此题只供人工查看，不参与自动得分。</p></div>}
      {['text', 'number'].includes(draft.judge.type) && draft.parameters.generator !== 'static' && <label className="hub-checkbox"><input type="checkbox" checked={draft.judge.expectedFrom === 'generator'} onChange={e => set('judge', { ...draft.judge, expectedFrom: e.target.checked ? 'generator' : '' })} />使用本次生成器算出的答案（改题意需同步改判题）</label>}
      {(draft.judge.type === 'text' || draft.judge.type === 'legacy') && draft.judge.expectedFrom !== 'generator' && <div className="field"><label>标准答案<textarea rows={2} disabled={draft.judge.type === 'legacy'} value={draft.judge.expectedText ?? ''} onChange={e => set('judge', { ...draft.judge, expectedText: e.target.value })} /></label></div>}
      {draft.judge.type === 'number' && draft.judge.expectedFrom !== 'generator' && <div className="field"><label>数值答案<input type="number" step="any" value={draft.judge.expectedNumber ?? 0} onChange={e => set('judge', { ...draft.judge, expectedNumber: Number(e.target.value) })} /></label><p className="muted">仅取最终答案，绝对差不超过0.000000001才得满分；解析不出则未确认。</p></div>}
      {draft.judge.type === 'keywords' && <><div className="field"><label>命中要求<Select value={draft.judge.match ?? 'all'} onChange={e => set('judge', { ...draft.judge, match: e.target.value })}><option value="all">全部必需词</option><option value="any">任一必需词</option></Select></label></div><ParameterField name="必需词（1～20条）" value={draft.judge.required ?? []} change={value => set('judge', { ...draft.judge, required: value as string[] })} /><ParameterField name="禁词（命中任意一条即0分）" value={draft.judge.forbidden ?? []} change={value => set('judge', { ...draft.judge, forbidden: value as string[] })} /><p className="muted">只是字词匹配，请人工看原文。忽略大小写，不推断语义。</p></>}
    </div></details>}
    {error && <p className="hub-error" role="alert">{error}</p>}
    <div className="actions hub-wrap"><button type="button" className="btn-primary" disabled={busy} onClick={() => void run('save')}>{busy ? '正在处理…' : '保存题目'}</button><button type="button" className="btn-secondary" disabled={busy} onClick={() => void run('preview')}>生成预览</button>{original.revision && !original.id.startsWith('custom:') && <button type="button" className="btn-secondary" disabled={busy} onClick={() => { if (confirm('恢复此题的预设正文、参数和判题？只影响后续检测。')) void run('restore'); }}>恢复该题默认</button>}{original.revision && <button type="button" className="btn-secondary" disabled={busy} onClick={() => void remove()}>删除题目</button>}</div>
    {preview && <div className="hub-preview"><h3>本次预览</h3><p className="muted">这里只在检测端生成，不调用模型。种子：{preview.seed}</p>{preview.image?.startsWith('data:image/png;base64,') && <img src={preview.image} className="hub-image" alt="生成后的识图题面" />}<pre>{preview.prompt || '题面在图片中'}</pre><strong>检测端参考答案</strong><pre>{preview.expected || '此项按固定证据规则或人工查看'}</pre></div>}
  </section>;
}

export default function HubSettings({ call, onDirtyChange }: { call: HubCall; onDirtyChange: (dirty: boolean) => void }) {
  const [bank, setBank] = useState<{ upstream_sha?: string; generated_at?: string; numeric_count?: number; behavioral_count?: number; preset_count?: number; preset_summaries?: { id: string; title: string; kind: HubKind }[]; preset_definitions?: HubQuestion[] } | null>(null);
  const [bankError, setBankError] = useState(''), [presetSource, setPresetSource] = useState('');
  const [editorDirty, setEditorDirty] = useState(false);
  const [settings, setSettings] = useState<Settings | null>(null), [settingDraft, setSettingDraft] = useState<Settings | null>(null);
  const [pages, setPages] = useState<Partial<Record<HubKind, HubPage<HubQuestionSummary>>>>({});
  const [loading, setLoading] = useState<Partial<Record<HubKind, boolean>>>({}), [errors, setErrors] = useState<Partial<Record<HubKind, string>>>({});
  const [editor, setEditor] = useState<HubQuestion | null>(null), [error, setError] = useState(''), [saving, setSaving] = useState(false), [notice, setNotice] = useState('');
  const [search, setSearch] = useState(''), [filter, setFilter] = useState('');
  const settingsDirty = JSON.stringify(settings) !== JSON.stringify(settingDraft);
  useEffect(() => { onDirtyChange(editorDirty || settingsDirty); }, [editorDirty, settingsDirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);
  const loadKind = useCallback(async (kind: HubKind, cursor = '') => {
    setLoading(old => ({ ...old, [kind]: true })); setErrors(old => ({ ...old, [kind]: '' }));
    try { const page = await call<HubPage<HubQuestionSummary>>('questions', { kind, cursor, limit: 20, q: filter }); setPages(old => ({ ...old, [kind]: { ...page, items: cursor ? [...(old[kind]?.items ?? []), ...page.items] : page.items } })); }
    catch (e) { setErrors(old => ({ ...old, [kind]: message(e) })); } finally { setLoading(old => ({ ...old, [kind]: false })); }
  }, [call, filter]);
  useEffect(() => { let live = true; void call<typeof bank>('bank-info').then(value => { if (live) setBank(value); }).catch(e => { if (live) setBankError(message(e)); }); return () => { live = false; }; }, [call]);
  useEffect(() => { let live = true; void call<Settings>('settings').then(s => { if (live) { setSettings(s); setSettingDraft(s); } }).catch(e => { if (live) setError(message(e)); }); return () => { live = false; }; }, [call]);
  useEffect(() => { setPages({}); setErrors({}); void loadKind('iq'); }, [loadKind]);
  const edit = async (q: HubQuestionSummary, duplicate = false) => {
    if (editor && !confirm('放弃当前尚未保存的编辑，打开另一题吗？')) return;
    setError(''); try { const definition = await call<HubQuestion>('question', { id: q.id }); setEditor(duplicate ? { ...copy(definition), id: newHubID('custom:'), title: `${definition.title}（副本）`, revision: undefined, maxScore: 0, judge: { type: 'manual', referenceAnswer: definition.judge.expectedText ?? '' } } : definition); setNotice(''); } catch (e) { setError(message(e)); }
  };
  const addPreset = () => {
    const source = bank?.preset_definitions?.find(q => q.id === presetSource);
    if (!source) { setError('请先选择安装包中的预设；若列表为空，请检查静态参考信息是否随安装包提供。'); return; }
    if (editor && !confirm('放弃当前未保存的编辑，新增所选预设的副本吗？')) return;
    const definition = copy(source);
    setEditor({ ...definition, id: newHubID('custom:'), revision: undefined, title: `${definition.title}（副本）`, ...(definition.judge.type === 'legacy' ? { maxScore: 0, judge: { type: 'manual', referenceAnswer: definition.judge.expectedText ?? '' } as HubQuestion['judge'] } : {}) });
    setNotice('已填入安装包预设副本，请核对后保存。使用新的题目编号，不会恢复已删除的旧编号；原专用判题副本默认为人工查看，您可明确改为通用判题。');
  };
  const saveSettings = async () => {
    if (!settingDraft) return;
    if (!Number.isSafeInteger(settingDraft.concurrency) || settingDraft.concurrency < 1 || settingDraft.concurrency > 64) { setError('上游并发必须为1～64的整数；设置尚未保存，请修改后重试。'); return; }
    if (!Number.isSafeInteger(settingDraft.totalTimeoutSeconds) || settingDraft.totalTimeoutSeconds < 60 || settingDraft.totalTimeoutSeconds > HUB_MAX_TOTAL_SECONDS || settingDraft.totalTimeoutSeconds < settingDraft.timeoutSeconds) { setError('任务总时长必须为60～9223371976秒的整数，且不少于单请求超时；设置尚未保存，请修改后重试。'); return; }
    setSaving(true); setError(''); try { const result = await call<Settings>('save-settings', settingDraft as unknown as Record<string, unknown>); setSettings(result); setSettingDraft(result); setNotice('运行设置已保存，仅供新任务使用。'); } catch (e) { setError(message(e)); } finally { setSaving(false); } };
  const numberField = (key: keyof Settings, label: string, min: number, max: number) => <div className="field" key={key}><label>{label}<input type="number" min={min} max={max} value={settingDraft?.[key] ?? ''} onChange={e => setSettingDraft(old => old && ({ ...old, [key]: Number(e.target.value) }))} /></label></div>;
  return <div className="hub-settings"><section className="hero-card hub-card"><h1>检测设置</h1><p className="muted">这里列出全部预设和自定义题目。展开分类可逐题编辑、复制或删除；删除只影响后续任务，旧报告保留。页面只读写检测配置，不修改主站账号或调度设置。</p><details className="fold"><summary>当前安装包的预设与指纹参考版本</summary><p className="muted">任务启动时复用原程序的参考库缓存和更新检查；任务结束后检查程序退出，不另开常驻服务。</p>{bank && <p className="muted">安装包预设 {bank.preset_count ?? '—'} 项；数字参考 {bank.numeric_count ?? '—'}，行为参考 {bank.behavioral_count ?? '—'}。题库删除不改变安装包数量，也不会自动把删除项加回。</p>}{bank?.upstream_sha && <p className="muted">上游版本：{bank.upstream_sha.slice(0, 12)} · 构建时间：{bank.generated_at}</p>}{bankError && <p className="hub-error">{bankError}</p>}</details><details className="fold" open><summary>运行与资源</summary><p className="hub-note">限制只作用于检测任务；整个检测子项目最多使用约0.5GB。更改供新任务使用。空闲时限额在下一次启动时核验。</p>{settingDraft && <><div className="hub-form-grid"><div className="field"><label>检测接口模式<Select value={settingDraft.protocol || 'auto'} onChange={e => setSettingDraft(old => old && ({ ...old, protocol: e.target.value }))}><option value="auto">自动（原版兼容）</option><option value="chat">Chat Completions</option><option value="anthropic">Messages</option><option value="openai">Responses</option></Select></label><p className="muted">只决定新检测的请求格式，自动兼容限定同一上游地址，不修改主站账号；原报告补测保留创建时的模式。</p></div>{numberField('memoryLimitMiB' , '检测任务内存上限（MiB，128～384）', 128, 384)}{numberField('timeoutSeconds', '单请求超时（秒，10～600）', 10, 600)}{numberField('totalTimeoutSeconds', '任务总时长（秒，默认3600）', 60, HUB_MAX_TOTAL_SECONDS)}{numberField('concurrency', '上游并发（1～64，默认32）', 1, 64)}{numberField('maxOutputTokens', '普通推理输出预算（128～16384）', 128, 16384)}{numberField('retries', '失败重试次数（0～3，会增加费用）', 0, 3)}{numberField('maxAttempts', '总请求尝试上限（0不限，16～256）', 0, 256)}</div><p className="muted">完成一个请求后立即补下一个，超过数量的题目等待。</p><p className="muted">任务总时长从本报告实际开始执行计时，等待时间不计入；调高只影响新任务。</p><button type="button" className="btn-primary" disabled={saving || JSON.stringify(settings) === JSON.stringify(settingDraft)} onClick={() => void saveSettings()}>保存运行设置</button></>}</details>
    <div className="actions hub-wrap"><button type="button" className="btn-secondary" onClick={() => { if (!editor || confirm('放弃当前编辑并新增普通题吗？')) setEditor(blankQuestion('iq')); }}>新增普通题</button><button type="button" className="btn-secondary" onClick={() => { if (!editor || confirm('放弃当前编辑并新增知识事件吗？')) setEditor(blankQuestion('knowledge')); }}>新增知识事件</button></div>
    <p className="muted">其他检测类型可打开现有题“复制新增”，或从下方安装包预设新增副本；即使已删光此分类，仍可新增同类。原算法的固定部分会说明原因。</p>
    <div className="hub-search field"><label>安装包预设<Select value={presetSource} onChange={e => setPresetSource(e.target.value)}><option value="">选择要新增副本的预设</option>{(bank?.preset_summaries ?? bank?.preset_definitions ?? []).map(q => <option key={q.id} value={q.id}>{HUB_KINDS.find(k => k.value === q.kind)?.label} · {q.title}</option>)}</Select></label><button type="button" className="btn-secondary" disabled={!presetSource} onClick={addPreset}>新增所选预设副本</button></div>
    <p className="muted">这里只填入新副本，点击保存后才新增。原题判分规则不复制到新编号，推理／识图副本默认人工查看，可改通用判题；不会重建已删除的旧编号。</p><div className="hub-search field"><label>按题名查找<input value={search} maxLength={80} onChange={e => setSearch(e.target.value)} /></label><button type="button" className="btn-secondary" onClick={() => setFilter(search)}>查找题目</button></div>{notice && <p role="status" className="good">{notice}</p>}{error && <p role="alert" className="hub-error">{error}</p>}</section>
    {editor && <HubQuestionEditor key={editor.id + ':' + (editor.revision ?? 'new')} original={editor} call={call} onDirtyChange={setEditorDirty} cancelled={() => setEditor(null)} saved={kind => { setEditor(null); setNotice('题库已保存。新检测会采用新内容，旧报告不会改变。'); void loadKind(kind); }} />}
    {HUB_KINDS.map(kind => <details className="hero-card hub-card hub-category" key={`${kind.value}:${filter}`} open={kind.value === 'iq'} onToggle={e => { if (e.currentTarget.open && !pages[kind.value] && !loading[kind.value]) void loadKind(kind.value); }}><summary>{kind.label}<span className="muted"> · 展开查看、增删和编辑</span></summary><p className="muted">{fixedInfo[kind.value]}</p>{errors[kind.value] && <p role="alert" className="hub-error">{errors[kind.value]}</p>}{pages[kind.value]?.items.map(q => <div className="hub-question-row" key={q.id} data-question-id={q.id}><div><strong>{q.title}</strong><span className="muted"> · {q.enabled ? '已启用' : q.id==='cache' && q.revision===1 ? '默认不执行（首页可选）' : '已禁用'} · 排序 {q.order}</span></div><div className="actions"><button type="button" className="btn-secondary hub-small" onClick={() => void edit(q)}>编辑</button><button type="button" className="btn-secondary hub-small" onClick={() => void edit(q, true)}>复制新增</button><button type="button" className="btn-secondary hub-small" onClick={async () => { if (!confirm(`删除“${q.title}”？旧报告仍保留，重启或升级不会自动加回。`)) return; try { await call('delete-question', { id: q.id, revision: q.revision }); setNotice(`已删除“${q.title}”，旧报告不变。`); void loadKind(kind.value); } catch (e) { setErrors(old => ({ ...old, [kind.value]: message(e) })); } }}>删除</button></div></div>)}{loading[kind.value] && <p role="status">正在读取本页题目…</p>}{pages[kind.value]?.items.length === 0 && <p className="muted">此分类没有符合条件的题目。删除预设后不会自动重建。</p>}{pages[kind.value]?.nextCursor && <button type="button" className="btn-secondary" disabled={loading[kind.value]} onClick={() => void loadKind(kind.value, pages[kind.value]!.nextCursor)}>下一页题目</button>}{errors[kind.value] && <button type="button" className="btn-secondary" onClick={() => void loadKind(kind.value)}>重新读取此分类</button>}</details>)}
  </div>;
}
