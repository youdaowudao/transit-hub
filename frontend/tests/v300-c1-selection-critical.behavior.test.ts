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

describe('V3.0.0 critical C1 prepared selection',()=>{
 it('only submits prepared user models despite managed membership',async()=>{
  const w=await open(); await start(w);
  expect(api.startQuestionAnswerBatch.mock.calls.map(c=>c.slice(0,2))).toEqual([[targetId,['A']],[second,['A']]]);
  expect(api.getModelControlTarget).not.toHaveBeenCalled();
 });
 it('management failure and newly added managed models cannot change frozen submission',async()=>{
  const first=deferred<ReturnType<typeof batch>>(); api.startQuestionAnswerBatch.mockReturnValueOnce(first.promise);
  const w=await open(); api.getModelControlTarget.mockRejectedValue(new Error('fixture management failure'));
  await start(w); await w.setProps({preferences:preferences(['M'])}); first.resolve(batch()); await flushPromises();
  expect(api.startQuestionAnswerBatch.mock.calls.map(c=>c[1])).toEqual([['A'],['A']]);
  expect(api.getModelControlTarget).not.toHaveBeenCalled();
 });
});
