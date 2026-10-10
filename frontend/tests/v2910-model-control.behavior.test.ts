// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Row from '@/modules/admin/components/dashboard/QuestionAnswerModelControlRow.vue'
import Panel from '@/modules/admin/components/dashboard/QuestionAnswerModelControlPanel.vue'
import Dialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import Drawer from '@/modules/admin/components/dashboard/QuestionAnswerModelControlDrawer.vue'
import { ConnectionHealthApiError } from '@/modules/admin/api/connectionHealth'
import { modelControlBusyTargets } from '@/modules/admin/utils/questionAnswerModelControl'
import { questionAnswerFixtureStats } from './fixtures/c1QuestionAnswerHistory'
import type { ModelControlItem, QuestionAnswerBatch } from '@/modules/admin/types/connectionHealth'

const api = vi.hoisted(() => Object.fromEntries(['getModelControlTarget','verifyModelControl','getModelControlSettings','discoverTargetModels','listTestQuestions','getQuestionAnswerHistory','getLatestQuestionAnswerBatch','getQuestionAnswerBatch','startQuestionAnswerBatch','addModelControlManaged','listModelControlItems','listModelControlEvents','getModelControlVerifyTargets','previewModelControl','executeModelControl'].map(name => [name,vi.fn()])))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
import { targetId, now, target, managed, batch, emptyBatch } from './fixtures/v2911ModelControl'
const wrappers:VueWrapper[]=[]
const track=<T extends VueWrapper>(value:T):T=>{wrappers.push(value);return value}
const button=(w:VueWrapper,label:string)=>{const result=w.findAll('button').find(b=>b.text()===label);if(!result)throw new Error(`missing ${label}: ${w.text()}`);return result}
const hasButton=(w:VueWrapper,label:string)=>w.findAll('button').some(b=>b.text()===label)
const deferred=<T,>()=>{let resolve!:(v:T)=>void;const promise=new Promise<T>(done=>{resolve=done});return {resolve,promise}}
const dialog=async()=>{const w=track(mount(Dialog,{props:{open:false,target,questionAnswerPreferences:{modelIds:['A'],questionIds:['q'],reasoningEffort:'medium',repeatCount:1}},global:{stubs:{Teleport:true,Transition:false}}}));await w.setProps({open:true});await flushPromises();return w}
beforeEach(()=>{
 vi.resetAllMocks();modelControlBusyTargets.value=new Set();Object.defineProperty(document,'visibilityState',{configurable:true,value:'visible'})
 api.getModelControlTarget.mockResolvedValue({targetId,items:[managed()],candidates:[]});api.verifyModelControl.mockResolvedValue({items:[managed()],errors:[]});api.getModelControlSettings.mockResolvedValue({minAccuracyPercent:50,minJudgedAnswers:3,version:1})
 api.discoverTargetModels.mockResolvedValue([{id:'A',name:'A',isDefault:true},{id:'M',name:'M'}]);api.listTestQuestions.mockResolvedValue([{id:'q',name:'隔离题',body:'答案',keywords:['答案'],enabled:true,isDefault:true,createdAt:now,updatedAt:now}]);api.getLatestQuestionAnswerBatch.mockResolvedValue(emptyBatch())
 api.getQuestionAnswerHistory.mockResolvedValue({batches:[],page:1,pageSize:20,totalBatches:0,totalPages:0,todayStats:questionAnswerFixtureStats([])})
 api.startQuestionAnswerBatch.mockResolvedValue(batch());api.getQuestionAnswerBatch.mockResolvedValue(batch());api.addModelControlManaged.mockResolvedValue(managed('A'))
 api.listModelControlItems.mockResolvedValue({items:[managed()],page:1,totalPages:1,counts:{total:1,open:1,closed:0,attention:1,untested:0}});api.listModelControlEvents.mockResolvedValue({items:[],page:1,totalPages:1});api.getModelControlVerifyTargets.mockResolvedValue({targetIds:[targetId]})
 api.previewModelControl.mockResolvedValue({item:managed(),entries:[{key:'M',value:'M',state:'to_close'}],groups:[],blockReasonKey:'',requestHealth:{checked:false,allowed:null,reasonKey:''},accountStatus:'active',accountSchedulable:true,planFingerprint:'fixture-fingerprint'})
})

const container=async(kind:'panel'|'drawer')=>{
 if(kind==='panel'){const w=track(mount(Panel,{props:{targetId,refreshKey:0,availableModels:['A','M']}}));await flushPromises();return w}
 const w=track(mount(Drawer,{props:{groups:[],workspace:'ws1',platform:'sub2api'}}));await w.get('[data-testid="model-control-open"]').trigger('click');await flushPromises();return w
}
describe.each(['panel','drawer'] as const)('V2.9.11 %s manual reread and account freeze',kind=>{
 it('does not automatically retry an entire failed verification request',async()=>{
  vi.useFakeTimers();api.verifyModelControl.mockRejectedValue(new Error('fixture transport failure'))
  const w=await container(kind);expect(w.text()).toContain('这次没读到');expect(w.text()).toContain('各模型上次确认的结果');expect(hasButton(w,'重新读取主站结果')).toBe(true)
  await vi.advanceTimersByTimeAsync(180000);expect(api.verifyModelControl).toHaveBeenCalledTimes(1)
  api.verifyModelControl.mockResolvedValue({items:[managed()],errors:[]});await button(w,'重新读取主站结果').trigger('click');await flushPromises()
  expect(api.verifyModelControl.mock.calls.at(-1)![0]).toEqual([targetId]);expect(w.text()).not.toContain('这次没读到');expect(w.text()).not.toContain('各模型上次确认的结果')
 })
 it.each(['unknown','storage400','network','server500'] as const)('freezes every same-account card after %s without another write or verify',async problem=>{
  vi.useFakeTimers();const other=managed('A');api.getModelControlTarget.mockResolvedValue({targetId,items:[managed(),other],candidates:[]});api.listModelControlItems.mockResolvedValue({items:[managed(),other],page:1,totalPages:1,counts:{total:2,open:2,closed:0,attention:2,untested:0}})
  api.verifyModelControl.mockResolvedValue({items:[managed(),other],errors:[]});const w=await container(kind);expect(api.verifyModelControl).toHaveBeenCalledTimes(1)
  if(problem==='unknown')api.executeModelControl.mockResolvedValue({item:managed(),outcome:'unknown',reasonKey:'admin.connectionHealth.errors.modelControlReadbackFailed',entries:[],groups:[],hintKeys:[]})
  else api.executeModelControl.mockRejectedValue(problem==='network'?new Error('fixture connection lost'):new ConnectionHealthApiError('admin.connectionHealth.errors.modelControlStorageUnknown',problem==='storage400'?400:500))
  // A failed local reread must not release other cards on this account.
  api.getModelControlTarget.mockRejectedValue(new Error('fixture local read failed'));api.listModelControlItems.mockRejectedValue(new Error('fixture local read failed'))
  await w.findAllComponents(Row)[0]!.findAll('button').find(b=>b.text()==='关闭此模型')!.trigger('click');await flushPromises();await button(w,'确认').trigger('click');await flushPromises()
  expect(w.text()).toContain('主站没有明确答复');for(const row of w.findAllComponents(Row)){
   expect(button(row as unknown as VueWrapper,'不再管理').attributes('disabled')).toBeDefined()
   const close=row.findAll('button').find(b=>b.text()==='关闭此模型');if(close)expect(close.attributes('disabled')).toBeDefined()
  }
  await vi.advanceTimersByTimeAsync(180000);expect(api.executeModelControl).toHaveBeenCalledTimes(1);expect(api.verifyModelControl).toHaveBeenCalledTimes(1)
  const reread=w.findAll('button').find(b=>b.text()==='重新读取主站结果')!;await reread.trigger('click');await flushPromises()
  expect(w.text()).not.toContain('主站没有明确答复');expect(api.executeModelControl).toHaveBeenCalledTimes(1)
 })
 it('rejects an opening verification that arrives clean after an uncertain execution',async()=>{
  vi.useFakeTimers({now:new Date(now)});const late=deferred<{items:ModelControlItem[];errors:never[]}>();api.verifyModelControl.mockReturnValueOnce(late.promise)
  const w=await container(kind);await vi.advanceTimersByTimeAsync(1)
  api.executeModelControl.mockResolvedValue({item:managed(),outcome:'unknown',entries:[],groups:[],hintKeys:[]});await button(w,'关闭此模型').trigger('click');await flushPromises();await button(w,'确认').trigger('click');await flushPromises()
  late.resolve({items:[managed()],errors:[]});await flushPromises();expect(button(w,'不再管理').attributes('disabled')).toBeDefined();expect(w.text()).toContain('主站没有明确答复')
  await vi.advanceTimersByTimeAsync(1);api.verifyModelControl.mockResolvedValue({items:[managed()],errors:[]});await button(w,'重新读取主站结果').trigger('click');await flushPromises();expect(button(w,'不再管理').attributes('disabled')).toBeUndefined()
 })
})
afterEach(async()=>{for(const w of wrappers.splice(0))w.unmount();await flushPromises();modelControlBusyTargets.value=new Set();vi.useRealTimers();vi.restoreAllMocks()})

describe('V2.9.11 confirmed model-control business RED',()=>{
 it('replaces permanent verification with an exceptional reread action',()=>{
  const value=managed();const w=track(mount(Row,{props:{item:value,targetId,modelName:'M',workspace:'ws1'}}))
  expect(hasButton(w,'核对主站')).toBe(false);expect(hasButton(w,'重新读取主站结果')).toBe(false)
 })
 it('verifies an ordinary serving account once per panel mount',async()=>{
  vi.useFakeTimers();const w=track(mount(Panel,{props:{targetId,refreshKey:0,availableModels:['A','M']}}));await flushPromises()
  expect(api.verifyModelControl).toHaveBeenCalledTimes(1);await w.setProps({refreshKey:1});await flushPromises();await vi.advanceTimersByTimeAsync(180000);expect(api.verifyModelControl).toHaveBeenCalledTimes(1)
 })
 it('consumes the opening verification opportunity on an empty complete read',async()=>{
  vi.useFakeTimers();api.getModelControlTarget.mockResolvedValueOnce({targetId,items:[],candidates:[]})
  const w=track(mount(Panel,{props:{targetId,refreshKey:0,availableModels:['A','M']}}));await flushPromises();expect(api.verifyModelControl).not.toHaveBeenCalled()
  const unverified=managed();unverified.control.observation.state='unverified';unverified.control.observation.reasonKey='';unverified.control.observation.checkedAt=null
  api.getModelControlTarget.mockResolvedValue({targetId,items:[unverified],candidates:[]})
  await w.setProps({refreshKey:1});await flushPromises();await vi.advanceTimersByTimeAsync(180000)
  expect(api.verifyModelControl).not.toHaveBeenCalled();expect(w.text()).toContain('还没读取');expect(hasButton(w,'重新读取主站结果')).toBe(true)
  await button(w,'重新读取主站结果').trigger('click');await flushPromises();expect(api.verifyModelControl.mock.calls[0]![0]).toEqual([targetId])
 })
 it('does not consume the opening opportunity on a failed complete read',async()=>{
  api.getModelControlTarget.mockRejectedValueOnce(new Error('fixture local read failed'))
  const w=track(mount(Panel,{props:{targetId,refreshKey:0,availableModels:['A','M']}}));await flushPromises();expect(api.verifyModelControl).not.toHaveBeenCalled()
  await w.setProps({refreshKey:1});await flushPromises();expect(api.verifyModelControl).toHaveBeenCalledTimes(1)
  await w.setProps({refreshKey:2});await flushPromises();expect(api.verifyModelControl).toHaveBeenCalledTimes(1)
 })
 it('gives a partially closed model a single primary action',()=>{
  const value=managed();value.control.closedEntries={alias:'M'};value.control.observation.state='partially_closed'
  const w=track(mount(Row,{props:{item:value,targetId,modelName:'M',workspace:'ws1'}}))
  expect(hasButton(w,'关闭此模型')).toBe(true);expect(hasButton(w,'恢复此模型')).toBe(false);expect(hasButton(w,'开放此模型')).toBe(false)
 })
 it.each([['restore','account_missing'],['restore','conflict'],['add','account_missing'],['add','conflict']] as const)('blocks an already open %s preview after %s arrives',async(operation,problem)=>{
  const value=managed();value.decision='usable';value.basis.decision='usable';value.control.closedEntries=operation==='restore'?{alias:'M'}:{};value.control.observation.state=operation==='restore'?'closed':'not_provided'
  api.previewModelControl.mockResolvedValue({item:value,entries:[{key:'alias',value:'M',state:operation==='restore'?'to_restore':'to_add'}],groups:[],blockReasonKey:'',requestHealth:{checked:true,allowed:true,reasonKey:''},accountStatus:'active',accountSchedulable:true,planFingerprint:'fixture-fingerprint'})
  const w=track(mount(Row,{props:{item:value,targetId,modelName:'M',workspace:'ws1'}}));await button(w,'开放此模型').trigger('click');await flushPromises()
  const changed=JSON.parse(JSON.stringify(value)) as ModelControlItem
  if(problem==='account_missing')changed.control.observation.state='account_missing'
  else changed.control.conflictReason='admin.connectionHealth.errors.modelControlManualChanged'
  await w.setProps({item:changed});await flushPromises();expect(hasButton(w,'开放此模型')).toBe(false)
  const confirm=w.findAll('button').find(b=>b.text()==='确认')
  if(confirm){await confirm.trigger('click');await flushPromises()}
  expect(api.executeModelControl).not.toHaveBeenCalled()
  if(confirm)expect(confirm.attributes('disabled')).toBeDefined()
 })
 it('includes an unselected managed model in the actual submitted request',async()=>{
  const w=await dialog();await button(w,'开始回答').trigger('click');await flushPromises()
  expect(api.startQuestionAnswerBatch).toHaveBeenCalledTimes(1);expect(api.startQuestionAnswerBatch.mock.calls[0]![1]).toEqual(['A','M'])
 })
 it('blocks starts until a complete managed read succeeds',async()=>{
  const pending=deferred<{targetId:string;items:ModelControlItem[];candidates:string[]}>();api.getModelControlTarget.mockReturnValueOnce(pending.promise)
  const w=await dialog();expect(button(w,'开始回答').attributes('disabled')).toBeDefined();await button(w,'开始回答').trigger('click');expect(api.startQuestionAnswerBatch).not.toHaveBeenCalled()
  pending.resolve({targetId,items:[managed()],candidates:[]});await flushPromises();expect(button(w,'开始回答').attributes('disabled')).toBeUndefined()
 })
 it('blocks starts when managed reading fails',async()=>{
  api.getModelControlTarget.mockRejectedValue(new Error('fixture failure'));const w=await dialog()
  expect(button(w,'开始回答').attributes('disabled')).toBeDefined();expect(w.text()).toContain('受管模型读取失败');await button(w,'开始回答').trigger('click');expect(api.startQuestionAnswerBatch).not.toHaveBeenCalled()
 })
 it('renders judged results before manual review without an empty box',async()=>{
  api.getLatestQuestionAnswerBatch.mockResolvedValue(batch());const w=await dialog();const text=w.text()
  expect(text.indexOf('判题结果')).toBeLessThan(text.indexOf('待人工判断'));expect(text).toContain('题目没设关键词时');expect(w.findAll('.border-dashed').some(node=>node.text().includes('暂无'))).toBe(false)
 })
 it('keeps the zero-result explanation visible when its heading is clicked',async()=>{
  const w=await dialog();const processed=w.get('[data-testid="question-answer-processed"]');expect(processed.text()).toContain('这一批没有可判的答案');await processed.get('[data-testid="question-answer-processed-summary"]').trigger('click');expect(processed.text()).toContain('这一批没有可判的答案');expect(processed.findAll('.border-dashed')).toHaveLength(0)
 })
 it('does not save a managed model introduced by a completed running batch',async()=>{
  vi.useFakeTimers();const completed=batch();const m=batch('M').records[0]!;completed.records.push({...m,id:'record-M'});completed.submittedCount=2;completed.completedCount=2;completed.stats=questionAnswerFixtureStats(completed.records)
  const running={...completed,active:true,runningCount:2,completedCount:0,currentModel:'A',records:completed.records.map(r=>({...r,status:'running' as const,answerJudgment:null,completedAt:null}))}
  api.getLatestQuestionAnswerBatch.mockResolvedValue(running);api.getQuestionAnswerBatch.mockResolvedValue(completed)
  const w=await dialog();await vi.advanceTimersByTimeAsync(2000);await flushPromises()
  await w.get('[data-testid="question-answer-configuration-toggle"]').trigger('click');await flushPromises()
  const a=w.get('[data-testid="question-answer-models"] label input[type="checkbox"]')
  expect(a.attributes('disabled')).toBeUndefined();await a.setValue(false);await flushPromises()
  const saved=w.emitted('question-answer-preferences-changed')!.at(-1)![0] as {modelIds:string[]}
  expect(saved.modelIds).not.toContain('M')
 })
})
