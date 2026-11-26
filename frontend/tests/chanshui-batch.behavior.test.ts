// @vitest-environment jsdom
import { createRequire } from 'node:module'
import path from 'node:path'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import HubApp from '../../chanshui/web/components/HubApp'
import { reportPollDecision } from '../../chanshui/web/lib/hub-policy'
import { restorationReport, restorationItems } from './fixtures/chanshui-report-restoration'
import { resultCell, memberTimes, validBatchCounts } from '../../chanshui/web/lib/hub-batch-view'
import type { HubCatalog, HubBatchDetail, HubBatchMember } from '../../chanshui/web/lib/hub-types'

const require = createRequire(path.resolve(process.cwd(), '../chanshui/web/package.json'))
const React = require('react'), { createRoot } = require('react-dom/client')
let root: ReturnType<typeof createRoot>, container: HTMLDivElement, dispose: (() => void) | undefined
const act = async (fn: () => unknown = () => undefined) => { await React.act(async () => { await fn(); await new Promise(r => setTimeout(r, 0)) }) }
const settle = async () => { for (let i = 0; i < 6; i++) await act() }
const button = (text: string) => [...container.querySelectorAll('button')].find(b => b.textContent?.trim() === text)!
const click = async (text: string) => { const b = button(text); expect(b, text).toBeTruthy(); expect(b.disabled, text).toBe(false); await act(() => b.click()); await settle() }
const choose = async (label: string, value: string) => {
  const field = [...container.querySelectorAll('label')].find(l => l.firstChild?.textContent?.trim() === label)!;
  const trigger = field?.querySelector<HTMLButtonElement>('[role=combobox]'); expect(trigger, label).toBeTruthy();
  await act(() => trigger!.click()); await settle();
  const group = catalog.groups.find(g => g.id === value), account = catalog.groups.flatMap(g => g.accounts).find(a=>a.targetId===value);
  const name = label.includes('分组') ? group?.name : `${account?.accountName} #${account?.accountId}`;
  const option = [...field.querySelectorAll<HTMLButtonElement>('[role=option]')].find(o => o.querySelector('span')?.textContent===name);
  expect(option, `下拉项应精确显示${name}，不能带逗号`).toBeTruthy(); await act(()=> option!.click()); await settle();
}
const check = async (label: string, scope: ParentNode = container) => { const input = [...scope.querySelectorAll('label')].find(l => l.textContent?.trim() === label)?.querySelector('input[type=checkbox]') as HTMLInputElement; expect(input, label).toBeTruthy(); await act(() => input.click()); await settle() }
const catalog: HubCatalog = { workspaceId: 'batch-fixture', groups: [
  { id: 'g', name: '分组甲', accounts: [{ targetId: 'a', accountId: 'a', accountName: '账号甲', protocol: 'chat' }, { targetId: 'b', accountId: 'b', accountName: '账号乙', protocol: 'chat' }] },
  { id: 'h', name: '分组乙', accounts: [{ targetId: 'a', accountId: 'a', accountName: '账号甲', protocol: 'chat' }] },
] }
const reply = (m: unknown) => window.dispatchEvent(new MessageEvent('message', { data: m, source: window, origin: location.origin }))
let calls: { op: string; params: any; guardStart?: boolean; afterStop?: boolean }[]
let sent: any[], generation = 1
let hideBeforeGuard = false
let success = false, created: HubBatchDetail | null = null
let queryPages = false, historyPaged = false
let reportSaved: any = null
let oldReport: ReturnType<typeof restorationReport> | null = null
let runtimeOverride: any = null
let holdCancel = false, releaseCancel: (()=>void) | undefined
beforeEach(() => {
  vi.stubGlobal('React', React); vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('requestAnimationFrame', () => 0); vi.stubGlobal('cancelAnimationFrame', () => undefined)
  container = document.createElement('div'); document.body.append(container); root = createRoot(container)
  localStorage.removeItem('transithub.chanshui.sections.v1:batch-fixture'); calls = []; sent = []; generation = 1; hideBeforeGuard = false; success = false; created = null; reportSaved = null; oldReport=null; queryPages = false; historyPaged=false; runtimeOverride = null; holdCancel = false; releaseCancel = undefined
})
afterEach(async () => { await act(() => root.unmount()); dispose?.(); dispose = undefined; container.remove(); localStorage.removeItem('transithub.chanshui.sections.v1:batch-fixture'); vi.restoreAllMocks(); vi.unstubAllGlobals() })
async function boot() {
  const listener = (event: MessageEvent) => {
    const m = event.data
    if (m?.kind === 'chanshui.READY') { reply({ kind: 'chanshui.BOOT', v: 1, generation, workspaceId: catalog.workspaceId, target: {}, visible: true, catalog }); return }
    if (m?.kind !== 'chanshui.request') return
    calls.push(m)
    const respond = (value: unknown) => reply({ kind: 'chanshui.response', v: 1, generation, id: m.id, ok: true, value })
    const reject = (error: unknown) => reply({ kind: 'chanshui.response', v: 1, generation, id: m.id, ok: false, error })
    if (m.op === 'start-batch') {
      if (hideBeforeGuard) reply({ kind: 'chanshui.VISIBILITY', v: 1, generation, visible: false })
      const protocols = m.params.tasks.map((t: any) => ({ targetId: t.targetId, groupId: t.groupId, protocol: 'chat' }))
      const guard = new CustomEvent('chanshui.before-start', { cancelable: true, detail: { generation, id: m.id, protocols } })
      if (!m.guardStart || window.dispatchEvent(guard)) { reject({ status: 409, operationResult: 'not_started', message: '检测清单或登录状态已变化，本次没有发送问题。' }); return }
      sent.push(structuredClone(m.params))
      if (success) {
        const now = Date.now(), [a,b] = m.params.tasks;
        const common = {createdAtMs: now - 10000, status: 'done', phase: 'ended', finishConfirmed: true, accountName: '账号甲', groupName: '分组甲', summary: {total: {complete: true, parts:[{key:'fingerprint',applicable:true,known:true,points:1,max_points:2,contribution:5}]}, totalScore: 55, knowledge:{label:'待核查',status:'unknown',correct:1,total:3}}};
        const items = [{...common,...a,accountId:'a'}, {...common,...b,accountId:'b',accountName:'账号乙',status:'running',phase:'running',finishConfirmed:false,summary:{}}];
        const active = {...items[1],executionPhase:'running'};
        created = {batchId:m.params.batchId,createdAtMs:now-10000,firstFinishedAtMs:0,serverNow:now,observedAt:now,state:'running',counts:{total:2,ended:1,active:1,waiting:0,unconfirmed:0,complete:1,partial:0,abnormal:0,cancelled:0,retrying:0},sections:[{id:'iq',name:'推理题',count:2}],items,memberStates:items.map(row=>({reportId:row.reportId,status:row.status,phase:row.phase,finishConfirmed:row.finishConfirmed})),eta:null,etaReason:'可比较样本不足',runtime:{enabled:true,available:true,active}} as unknown as HubBatchDetail;
        reportSaved = {...restorationReport(), ...items[0], batchId:m.params.batchId,batchFirstFinishedAtMs:0,executionPhase:'ended',processExited:true,finishConfirmed:true};
        respond({batchId:m.params.batchId, reportIds:m.params.tasks.map((t:any)=>t.reportId),accepted:true,status:'queued'}); return;
      }
      reject({ status: 503, operationResult: 'unknown', message: '保存结果尚未确认' }); return
    }
    if (m.op === 'batch') {
      if (!created || m.params.batchId !== created.batchId) { reject({ status: 404, message: '暂未找到此批次' }); return }
      let items = created.items.filter(row => (!m.params.targetId || row.targetId===m.params.targetId) && (!m.params.model || row.model===m.params.model));
      respond({...created,serverNow:Date.now(),items,nextOffset:null,runtime:runtimeOverride ?? created.runtime}); return;
    }
    if (m.op === 'cancel') {
      const done = () => respond({reportId:m.params.reportId,status:'stopping',executionPhase:'stopping',processExited:false});
      if (holdCancel) releaseCancel = done; else done(); return;
    }
    if (m.op === 'report' && oldReport?.reportId===m.params.reportId) {respond(oldReport);return}
    if (m.op === 'report' && reportSaved) {respond(reportSaved);return}
    if (m.op === 'items') { respond({items:restorationItems(1).map(i=>({...i,status:'error',result:{...i.result,error:'隔离失败'}}))}); return }
    if (m.op === 'questions' && queryPages) { respond(m.params.cursor ? {items:[{id:'q2',title:'后页题目',kind:'iq',enabled:true,revision:1,order:2}]} : {items:[{id:'q1',title:'首页题目',kind:'iq',enabled:true,revision:1,order:1}],nextCursor:'q2'}); return }
    const context = { workspaceId: catalog.workspaceId, targetId: m.params.targetId, groupId: m.params.groupId, baseURL: 'https://fixture.invalid', protocol: 'chat' }
    const values: Record<string, unknown> = { runtime: runtimeOverride ?? created?.runtime ?? { enabled: true, available: true, active: null }, settings: { revision: 3, timeoutSeconds: 120, concurrency: 32, memoryLimitMiB: 256, totalTimeoutSeconds: 3600, retries: 0, maxOutputTokens: 4096, maxAttempts: 128 }, reports: { items: oldReport ? [oldReport] : [], ...(historyPaged && m.params.scope==='archive' && {nextCursor:'next'}) }, batches: { items: created ? [created] : [], ...(historyPaged && m.params.scope==='archive' && {nextCursor:'next'}) }, context: { ...context, canRun: true, expectedContext: context }, models: { models: ['Model-X', 'model-y'] }, questions: { items: [] }, 'reference-info': {} }
    respond(values[m.op] ?? {})
  }
  window.addEventListener('message', listener); dispose = () => window.removeEventListener('message', listener)
  await act(() => root.render(React.createElement(HubApp))); await settle()
}
async function selectBatch() {
  await click('批量检测'); await choose('批量分组', 'g')
  await check('账号甲 #a'); await check('账号乙 #b')
  const accounts = [...container.querySelectorAll('[data-batch-account]')]
  expect(accounts).toHaveLength(2)
  await check('Model-X', accounts[0]); await check('model-y', accounts[1])
  await click('核对检测清单')
  expect(container.textContent).toContain('本批将创建2份报告')
  expect(sent).toHaveLength(0)
}
it('批量组合确认前只读取选中账号，单问仍可进入；未知受理查询404不换号且重试为冻结原清单', async () => {
  await boot(); await selectBatch()
  expect(calls.filter(c => c.op === 'context')).toHaveLength(2)
  expect(calls.filter(c => c.op === 'models')).toHaveLength(2)
  expect(container.textContent).toContain('单问单答')
  await click('开始本批检测'); expect(sent).toHaveLength(1)
  const frozen = sent[0]
  expect(frozen).toMatchObject({ settingsRevision: 3, tasks: [{ targetId: 'a', groupId: 'g', model: 'Model-X' }, { targetId: 'b', groupId: 'g', model: 'model-y' }] })
  expect(frozen.batchId).toMatch(/^[0-9a-f]{32}$/)
  expect(new Set(frozen.tasks.map((t: any) => t.reportId)).size).toBe(2)
  await click('查询本批受理结果'); expect(sent).toHaveLength(1)
  expect(container.textContent).toContain(frozen.batchId)
  await click('用原编号重试受理'); expect(sent).toEqual([frozen, frozen])
  expect(calls.filter(c => c.op === 'context')).toHaveLength(2)
})
it('确认后变为不可见时同源同步保护拒绝首次发送与原号重试', async () => {
  await boot(); await selectBatch(); hideBeforeGuard = true
  await click('开始本批检测'); expect(sent).toHaveLength(0)
  expect(container.textContent).not.toContain('任务已保存并排队')
})
it('带执行阶段的旧终态不得提前结束等待，确认截止到期不会继续读取', () => {
  expect(reportPollDecision({ status: 'done', processExited: true, executionPhase: 'saving_end', finishConfirmed: false, recoveryDeadlineMs: 40000 } as any, null, 200).poll).toBe(true)
  expect(reportPollDecision({ status: 'done', processExited: true, executionPhase: 'paused', finishConfirmed: false, recoveryReason: '写入失败' } as any, null, 200)).toMatchObject({ poll: false, message: expect.stringContaining('写入失败') })
  expect(reportPollDecision({ status: 'running', processExited: false, executionPhase: 'stopping', stopConfirmDeadlineMs: 15000 } as any, null, 15000)).toMatchObject({ poll: false, message: expect.stringContaining('尚未确认') })
})

const setInput = async (input: HTMLInputElement, value: string) => { await act(() => {Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value')!.set!.call(input,value); input.dispatchEvent(new Event('input',{bubbles:true}));}); await settle() };
it('成功受理进入同主区批次，结果总览不读逐题；筛选返回原报告后恢复，首次结束前补测禁用；切旧单次无返回批次', async () => {
  oldReport={...restorationReport(),reportId:'f'.repeat(32),accountName:'隔离旧单次报告',createdAtMs:Date.now()};
  await boot(); success = true; await selectBatch(); await click('开始本批检测');
  expect(container.textContent).toContain('已结束1/2，尚余1份'); expect(calls.filter(c=>c.op==='items')).toHaveLength(0);
  expect(container.querySelectorAll('[data-batch-id]')).toHaveLength(1);
  expect(calls.filter(c=>c.op==='reports').every(c=>c.params.reportType==='single')).toBe(true);
  await click('检测结果'); expect(container.textContent).toContain('数字/行为指纹'); expect(container.textContent).toContain('1/2（计入总分 5）');
  expect(calls.filter(c=>c.op==='items')).toHaveLength(0);
  await setInput(container.querySelector<HTMLInputElement>('input[name=model]')!, 'Model-X'); await click('应用筛选');
  expect(container.querySelectorAll('[data-batch-report-id]')).toHaveLength(1); expect(container.textContent).toContain('已结束1/2，尚余1份');
  const scroll=vi.spyOn(window,'scrollTo').mockImplementation(()=>{}); vi.stubGlobal('scrollY',220); await click('查看报告');
  expect(container.querySelector<HTMLInputElement>('#hub-report-id')?.value).toBe(''); expect(container.querySelector<HTMLInputElement>('#hub-report-id')?.closest('details')?.open).toBe(false);
  expect(container.textContent).toContain('请先等本批首次执行结束'); expect([...container.querySelectorAll('button')].find(b=>b.textContent?.startsWith('补测失败项'))?.disabled).toBe(true);
  vi.stubGlobal('requestAnimationFrame', (fn: FrameRequestCallback)=>{fn(0); return 0}); await click('返回批次'); expect(container.querySelector<HTMLInputElement>('input[name=model]')?.value).toBe('Model-X'); expect(scroll).toHaveBeenCalledWith(0,220);
  const last = calls.filter(c=>c.op==='batch').at(-1); expect(last?.params).toMatchObject({view:'results',model:'Model-X',offset:0}); expect(sent).toHaveLength(1);
  // 直接从返回后的批次页点击一个非成员旧报告，绝不能继承旧成员返回上下文。
  const old=container.querySelector<HTMLButtonElement>(`[data-report-id="${oldReport.reportId}"]`)!; expect(old).toBeTruthy();
  await act(()=>old.click()); await settle(); expect(container.querySelector('.report-meta')?.textContent).toContain('隔离旧单次报告');
  expect(button('返回批次')).toBeUndefined(); expect(container.textContent).not.toContain('返回原批次及刚才的筛选位置');
});
it('复用单次已读模型，跨组同账号同模型先选组，后页已选题在刷新首页后仍保留', async () => {
  await boot(); queryPages=true; await choose('分组','g'); await choose('账号','a');
  expect(calls.filter(c=>c.op==='context')).toHaveLength(1);
  await click('批量检测'); await choose('批量分组','g'); await check('账号甲 #a');
  expect(calls.filter(c=>c.op==='context')).toHaveLength(1); expect(calls.filter(c=>c.op==='models')).toHaveLength(1);
  await check('Model-X', container.querySelector('[data-batch-account]')!);
  await choose('批量分组','h'); await check('账号甲 #a');
  await check('Model-X', container.querySelectorAll('[data-batch-account]')[1]);
  expect(button('核对检测清单').disabled).toBe(true); expect(container.textContent).toContain('请指定使用哪个组');
  await click('只使用 分组甲');
  await check('选择部分题目'); await click('推理题 · 读取题目'); await click('下一页题目'); await check('后页题目');
  await click('推理题 · 读取题目'); expect(container.textContent).toContain('已勾选题目（包含其他页）');
  await click('核对检测清单'); expect(container.textContent).toContain('已选择1题：后页题目');
  await click('开始本批检测'); expect(sent[0].selectedQuestionIds).toEqual(['q2']); expect(sent[0].tasks).toHaveLength(1); expect(sent[0].tasks[0].groupId).toBe('g');
});
it('显示未知、暂计0和原分项，不以0分判错；补测旧timing不造本轮倒计时', () => {
  expect(resultCell({totalScore:null,total:{complete:false,parts:[{key:'cache',applicable:true,known:false,points:0,max_points:1}]}},'cache')).toContain('暂计0分');
  expect(resultCell({totalScore:null,total:{complete:false}},'total')).not.toContain('0 分');
  expect(resultCell({total:{parts:[{key:'iq',applicable:true,known:true,points:0,max_points:5,contribution:0}]}},'iq')).toBe('0/5（计入总分 0）');
  const member: HubBatchMember = {reportId:'a',status:'running',phase:'running',finishConfirmed:false,retryId:'r',retryAcceptedAtMs:10000,totalTimeoutSeconds:60,timing:{started_at:new Date(1000).toISOString(),total_ms:5000,last_run_ms:5000,runs:1}};
  expect(memberTimes(member,0,20000)).toMatchObject({elapsed:expect.stringContaining('以前累计'),budget:'本轮尚未开始或起点待确认'});
  expect(validBatchCounts({total:15,complete:5,partial:1,abnormal:1,cancelled:0,ended:7,active:1,waiting:7,unconfirmed:0,retrying:0})).toBe(true);
})
it('单项停止POST在途仍可读优先状态；15秒不明结束自动等待且不重复停止', async () => {
  const real = globalThis.setTimeout.bind(globalThis); let expire: (()=>void) | undefined;
  vi.spyOn(globalThis,'setTimeout').mockImplementation((fn,ms,...args) => {
    if(ms===15000 && typeof fn==='function') {expire=()=>fn(...args); return real(()=>{}, 45000)}
    return real(fn,ms,...args)
  });
  let now=Date.now(); vi.spyOn(Date,'now').mockImplementation(()=>now);
  await boot(); success=true; await selectBatch(); await click('开始本批检测'); holdCancel=true;
  await click('停止检测');
  expect(calls.filter(c=>c.op==='cancel')).toHaveLength(1); expect(releaseCancel).toBeTypeOf('function');
  expect(calls.some(c=>c.op==='runtime'&&c.afterStop)).toBe(true); expect(calls.some(c=>c.op==='batch'&&c.afterStop)).toBe(true);
  now+=15000; expect(expire).toBeTypeOf('function'); await act(()=>expire?.()); await settle();
  expect(container.textContent).toContain('自动核查已暂停'); expect(calls.filter(c=>c.op==='cancel')).toHaveLength(1);
  expect(button('再次停止')?.disabled || button('正在停止…')?.disabled).toBe(true);
  await act(()=>releaseCancel?.()); await settle(); expect(calls.filter(c=>c.op==='cancel')).toHaveLength(1); expect(sent).toHaveLength(1);
})
it('原报告保存中即使数据库旧状态done也不宣称已生成、已停止，不放开补测', async () => {
  await boot(); success=true; await selectBatch(); await click('开始本批检测');
  Object.assign(reportSaved, {status:'done',processExited:true,executionPhase:'saving_end',finishConfirmed:false,recoveryDeadlineMs:Date.now()+40000,batchFirstFinishedAtMs:Date.now()});
  await act(()=>container.querySelector<HTMLButtonElement>('[data-batch-report-id] button')!.click()); await settle();
  expect(container.textContent).toContain('正在保存结束状态');
  expect(container.querySelector('.timeline h2')?.textContent).toBe('正在保存结束状态');
  expect(container.querySelector('.timeline h2')?.textContent).not.toContain('检测报告已生成');
  expect([...container.querySelectorAll('button')].find(b=>b.textContent?.startsWith('补测失败项'))?.disabled).toBe(true);
  expect(calls.some(c=>c.op==='retry'||c.op==='cancel')).toBe(false);
})
it('唯一右上设置对话框dirty关闭和Esc先确认，不丢批量草稿', async () => {
  await boot(); await click('批量检测'); await choose('批量分组','g'); await check('账号甲 #a'); await check('Model-X',container.querySelector('[data-batch-account]')!);
  await click('设置与全部题目'); const dialog=container.querySelector('[role=dialog]')!; expect(dialog).toBeTruthy(); expect(container.querySelectorAll('[role=dialog]')).toHaveLength(1);
  const concurrency=[...dialog.querySelectorAll('label')].find(l=>l.firstChild?.textContent?.includes('上游并发'))!.querySelector('input')!; await setInput(concurrency,'16');
  const confirm=vi.spyOn(window,'confirm').mockReturnValue(false);
  await act(()=>window.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))); expect(confirm).toHaveBeenCalledTimes(1); expect(container.querySelector('[role=dialog]')).toBeTruthy();
  confirm.mockReturnValue(true); await click('关闭设置'); expect(container.querySelector('[role=dialog]')).toBeNull(); expect(container.querySelector('[data-batch-account] input[type=checkbox]:checked')).toBeTruthy();
  expect(calls.some(c=>c.op==='save-settings'||c.op==='start-batch')).toBe(false);
})

it.each([{entry:'任务与结果',button:'查找批次',next:'下一页批次',op:'batches'},{entry:'查找更早记录',button:'查找报告',next:'下一页报告',op:'reports'}])('历史 $op 下一页继承已应用日期，新的查找才从首页开始', async ({entry,button:search,next,op}) => {
  historyPaged=true; await boot(); await click(entry);
  const start=[...container.querySelectorAll('label')].find(l=>l.firstChild?.textContent?.trim()==='开始日期')!.querySelector('input')!;
  await setInput(start,'2026-11-01'); await click(search);
  expect(calls.filter(c=>c.op===op&&c.params.scope==='archive').at(-1)?.params).toMatchObject({from:'2026-11-01',cursor:''});
  await setInput(start,'2026-11-15'); await click(next);
  expect(calls.filter(c=>c.op===op&&c.params.scope==='archive').at(-1)?.params).toMatchObject({from:'2026-11-01',cursor:'next'});
  await click(search); expect(calls.filter(c=>c.op===op&&c.params.scope==='archive').at(-1)?.params).toMatchObject({from:'2026-11-15',cursor:''});
})

// Review regressions: the status loop and the local stop window use actual timer
// callbacks, while unrelated React/bridge timers retain their real scheduling.
const statusTimers = () => {
  const realSet = globalThis.setTimeout.bind(globalThis), realClear = globalThis.clearTimeout.bind(globalThis);
  const pending = new Map<number,{delay:number,run:()=>void}>(); let sequence=0;
  vi.spyOn(globalThis,'setTimeout').mockImplementation((fn,delay,...args)=>{
    if ((delay===2000 || delay===15000) && typeof fn==='function') {const id=--sequence; pending.set(id,{delay,run:()=>fn(...args)});return id as unknown as ReturnType<typeof setTimeout>}
    return realSet(fn,delay,...args);
  });
  vi.spyOn(globalThis,'clearTimeout').mockImplementation(id=>{if (!pending.delete(id as unknown as number)) realClear(id)});
  return {count:(delay:number)=>Array.from(pending.values()).filter(t=>t.delay===delay).length,run:async(delay:number)=>{const entry=Array.from(pending.entries()).find(([,t])=>t.delay===delay);if(entry){pending.delete(entry[0]);await act(entry[1].run);await settle();}}};
};
it('R2 报告跨快照等待变准备时仍显示核查未确认，并不继续读；手动确认准备后恢复唯一循环', async () => {
  const timers=statusTimers(), id='e'.repeat(32);
  const active={...restorationReport(),reportId:id,createdAtMs:Date.now(),accountName:'跨快照报告',status:'queued',executionPhase:'preparing' as const,finishConfirmed:false,processExited:false};
  runtimeOverride={enabled:true,available:true,active};
  oldReport={...active,executionPhase:'unknown',runtime:runtimeOverride,recoveryReason:'本次读取期间执行状态变化，尚待核查'};
  await boot(); await act(()=>container.querySelector<HTMLButtonElement>(`[data-report-id="${id}"]`)!.click()); await settle();
  expect(container.querySelector('.report-meta')?.textContent).toContain('状态待确认');
  expect(container.querySelector('.report-meta')?.textContent).not.toContain('准备中');
  expect(timers.count(2000)).toBe(0);
  const before=calls.filter(c=>c.op==='report').length; await timers.run(2000); expect(calls.filter(c=>c.op==='report')).toHaveLength(before);
  oldReport={...active,runtime:runtimeOverride}; await click('核查运行状态');
  expect(container.querySelector('.report-meta')?.textContent).toContain('准备中'); expect(timers.count(2000)).toBe(1);
  await timers.run(2000); expect(calls.filter(c=>c.op==='report')).toHaveLength(before+2);
});
it('R3 取消等待项后筛选隐藏该行，全批memberStates仍确认其结束，另一份持续检测而不限时误暂停', async () => {
  const timers=statusTimers(); let now=Date.now(); vi.spyOn(Date,'now').mockImplementation(()=>now);
  await boot(); success=true; await selectBatch(); await click('开始本批检测');
  const detail=created!, [a,b]=detail.items;
  Object.assign(a,{status:'running',phase:'running',finishConfirmed:false}); Object.assign(b,{status:'queued',phase:'queued',finishConfirmed:false});
  detail.counts={total:2,active:1,waiting:1,ended:0,unconfirmed:0,complete:0,partial:0,abnormal:0,cancelled:0,retrying:0};
  detail.memberStates=detail.items.map(row=>({reportId:row.reportId,status:row.status,phase:row.phase,finishConfirmed:Boolean(row.finishConfirmed)}));
  runtimeOverride={enabled:true,available:true,active:{...a,batchId:detail.batchId,executionPhase:'running'}};
  await click('核查运行状态'); holdCancel=true; await click('取消等待'); expect(timers.count(15000)).toBe(1);
  Object.assign(b,{status:'cancelled',phase:'ended',finishConfirmed:true}); detail.counts={...detail.counts,ended:1,waiting:0,cancelled:1};
  detail.memberStates=detail.items.map(row=>({reportId:row.reportId,status:row.status,phase:row.phase,finishConfirmed:Boolean(row.finishConfirmed)}));
  await setInput(container.querySelector<HTMLInputElement>('input[name=model]')!,'Model-X'); await click('应用筛选');
  expect(container.querySelector(`[data-batch-report-id="${b.reportId}"]`)).toBeNull(); expect(container.querySelector(`[data-batch-report-id="${a.reportId}"]`)?.textContent).toContain('检测中');
  expect(timers.count(15000)).toBe(0); now+=15000; await timers.run(15000);
  expect(container.textContent).not.toContain('停止结果尚未确认'); expect(timers.count(2000)).toBe(1);
  const before=calls.filter(c=>c.op==='batch').length; await timers.run(2000); expect(calls.filter(c=>c.op==='batch')).toHaveLength(before+1);
  await act(()=>releaseCancel?.()); await settle(); expect(calls.filter(c=>c.op==='cancel')).toHaveLength(1);
});
it('R6 同代次收起恢复收到等价新引用名单，已读模型继续可选；真实协议变动不能冻结发送', async () => {
  await boot(); await click('批量检测'); await choose('批量分组','g'); await check('账号甲 #a'); await check('Model-X',container.querySelector('[data-batch-account]')!);
  const reads=()=>calls.filter(c=>c.op==='context'||c.op==='models').length; expect(reads()).toBe(2);
  await act(()=>reply({kind:'chanshui.VISIBILITY',v:1,generation,visible:false}));
  const clone=structuredClone(catalog); clone.groups[0].accounts[0]={protocol:'chat',accountName:'账号甲',accountId:'a',targetId:'a'};
  await act(()=>reply({kind:'chanshui.CATALOG',v:1,generation,catalog:clone})); await act(()=>reply({kind:'chanshui.VISIBILITY',v:1,generation,visible:true})); await settle();
  expect(reads()).toBe(2); expect(button('核对检测清单').disabled).toBe(false); expect(container.textContent).not.toContain('重试读取模型');
  await click('核对检测清单'); clone.groups[0].accounts[0].protocol='openai';
  await act(()=>reply({kind:'chanshui.CATALOG',v:1,generation,catalog:structuredClone(clone)})); await settle();
  await click('开始本批检测'); expect(sent).toHaveLength(0); expect(reads()).toBe(2);
});
it('R1 未确认首轮结束的服务投影不宣称首次完成；已确认首轮后补测待核查保留原首次时间', async () => {
  await boot(); success=true; await selectBatch(); await click('开始本批检测'); const detail=created!;
  detail.state='unknown'; detail.firstFinishedAtMs=0; detail.counts={...detail.counts,active:0,waiting:0,unconfirmed:2,ended:0,complete:0};
  await click('核查运行状态'); expect(container.textContent).toContain('首轮结束尚未确认'); expect(container.textContent).not.toContain('首轮已结束');
  detail.firstFinishedAtMs=detail.createdAtMs+1000; detail.counts={...detail.counts,retrying:1};
  await click('核查运行状态'); expect(container.textContent).toContain('首轮已结束，首次历时'); expect(container.textContent).toContain('当前补测1份');
});
