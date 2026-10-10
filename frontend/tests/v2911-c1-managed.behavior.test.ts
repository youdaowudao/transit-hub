// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Drawer from '@/modules/admin/components/dashboard/QuestionAnswerBatchDrawer.vue'
import { c2Groups } from './fixtures/c2QuestionAnswerSchedules'
import { batch, managed, targetId, now } from './fixtures/v2911ModelControl'

const api=vi.hoisted(()=>({discoverTargetModels:vi.fn(),listTestQuestions:vi.fn(),getModelControlTarget:vi.fn(),startQuestionAnswerBatch:vi.fn()}))
vi.mock('@/modules/admin/api/connectionHealth',async original=>({...await original<typeof import('@/modules/admin/api/connectionHealth')>(),...api}))
const second='sub2api:ws1:b', wrappers:VueWrapper[]=[]
const deferred=<T,>()=>{let resolve!:(value:T)=>void;const promise=new Promise<T>(done=>{resolve=done});return{promise,resolve}}
const groups=()=>{const result=c2Groups();const a=result[0]!.accounts[0]!;a.targetId=targetId;a.status='active';a.schedulable=true;result[0]!.accounts.push({...a,id:'b',name:'隔离账号B',targetId:second});return result}
const preferences=(models=['A'],repeatCount=1)=>({modelIds:models,questionIds:['q'],reasoningEffort:'medium' as const,repeatCount,batchTargetIds:[targetId,second]})
const open=async(models=['A'],repeatCount=1)=>{const w=mount(Drawer,{props:{groups:groups(),preferenceScope:'ws1',preferences:preferences(models,repeatCount)},global:{stubs:{Teleport:true}}});wrappers.push(w);await w.get('[data-testid="question-answer-batch-open"]').trigger('click');await flushPromises();expect(w.get('[data-testid="question-answer-batch-start"]').attributes('disabled')).toBeUndefined();return w}
const start=async(w:VueWrapper)=>{await w.get('[data-testid="question-answer-batch-start"]').trigger('click');await flushPromises()}
beforeEach(()=>{vi.resetAllMocks();api.discoverTargetModels.mockResolvedValue([{id:'A',name:'A'},{id:'M',name:'M'}]);api.listTestQuestions.mockResolvedValue([{id:'q',name:'隔离题',body:'答案',keywords:['答案'],enabled:true,isDefault:true,createdAt:now,updatedAt:now}]);api.getModelControlTarget.mockImplementation(async(id:string)=>({targetId:id,items:[{...managed(),targetId:id}],candidates:[]}));api.startQuestionAnswerBatch.mockResolvedValue(batch())})
afterEach(()=>{for(const w of wrappers.splice(0))w.unmount();vi.restoreAllMocks()})

describe('V2.9.11 C1 uses frozen base models plus current managed models',()=>{
 it('includes an unselected managed model in each account request and preview',async()=>{
  const w=await open();expect(w.text()).toContain('含受管 1 个');await start(w)
  expect(api.startQuestionAnswerBatch.mock.calls.map(c=>c.slice(0,2))).toEqual([[targetId,['A','M']],[second,['A','M']]])
  const saved=w.emitted('preferences-changed')??[];expect(saved.every(e=>!(e[0] as {modelIds:string[]}).modelIds.includes('M'))).toBe(true)
 })
 it.each(['read_failed','missing','over_limit'] as const)('isolates a %s managed account and still submits its peer',async problem=>{
  if(problem==='over_limit'){api.discoverTargetModels.mockResolvedValue(Array.from({length:11},(_,i)=>({id:i===0?'A':`M${i}`,name:`模型${i}`})))}
  api.getModelControlTarget.mockImplementation(async(id:string)=>{
   if(id===second)return{targetId:id,items:[],candidates:[]}
   if(problem==='read_failed')throw new Error('fixture local read failed')
   return{targetId:id,items:problem==='missing'?[managed('missing')]:Array.from({length:10},(_,i)=>managed(`M${i+1}`)),candidates:[]}
  })
  const w=await open(['A'],problem==='over_limit'?5:1);await start(w)
  expect(api.startQuestionAnswerBatch.mock.calls.map(c=>c[0])).toEqual([second])
  const outcome=w.get(`[data-testid="question-answer-batch-outcome-${targetId}"]`).text()
  expect(outcome).toContain(problem==='read_failed'?'受管模型读取失败':problem==='missing'?'missing':'单批 50 条')
 })
 it('recomputes preview problems and permits newly added compatible managed models at submission',async()=>{
  api.discoverTargetModels.mockImplementation(async(id:string)=>id===targetId?[{id:'A',name:'A'}]:[{id:'M',name:'M'}])
  let submitting=false;api.getModelControlTarget.mockImplementation(async(id:string)=>({targetId:id,items:id===second&&submitting?[{...managed(),targetId:id}]:[],candidates:[]}))
  const w=await open();submitting=true;await start(w)
  expect(api.startQuestionAnswerBatch.mock.calls.map(c=>c.slice(0,2))).toEqual([[targetId,['A']],[second,['M']]])
 })
 it('uses resolved default models frozen in the run snapshot when preferences change during submission',async()=>{
  const first=deferred<ReturnType<typeof batch>>();api.startQuestionAnswerBatch.mockReturnValueOnce(first.promise)
  api.getModelControlTarget.mockResolvedValue({targetId,items:[],candidates:[]})
  const w=await open([]);await start(w);expect(api.startQuestionAnswerBatch.mock.calls[0]![1]).toEqual(['A'])
  await w.setProps({preferences:preferences(['M'])});first.resolve(batch());await flushPromises()
  expect(api.startQuestionAnswerBatch.mock.calls.map(c=>c[1])).toEqual([['A'],['A']])
 })
})
