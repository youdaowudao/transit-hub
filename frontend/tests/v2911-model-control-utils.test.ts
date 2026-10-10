import { unref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as control from '@/modules/admin/utils/questionAnswerModelControl'
import { managed, targetId, now } from './fixtures/v2911ModelControl'

const deferred=<T,>()=>{let resolve!:(value:T)=>void;const promise=new Promise<T>(done=>{resolve=done});return{promise,resolve}}
const clean=()=>({items:[managed()],errors:[]})
afterEach(()=>{vi.useRealTimers();vi.restoreAllMocks()})

describe('manual model verification preserves sequencing and account freezes',()=>{
 it('has no timers or automatic retries for every exceptional verify result',async()=>{
  vi.useFakeTimers();const verify=vi.fn();const state=control.createModelControlVerifyState({verify})
  for(const reason of ['Busy','Processing','AccountReadFailed','Storage']){
   verify.mockResolvedValueOnce({items:[],errors:[{targetId,reasonKey:`admin.connectionHealth.errors.modelControl${reason}`}]})
   await state.verifyNow([targetId]);expect(unref(state.issues).get(targetId)?.reasonKey).toContain(reason)
   await vi.advanceTimersByTimeAsync(180000);expect(vi.getTimerCount()).toBe(0)
  }
  verify.mockRejectedValueOnce(new Error('fixture transport failure'));await state.verifyNow([targetId])
  expect(unref(state.issues).get(targetId)?.reasonKey).toBe('request_failed');expect(verify).toHaveBeenCalledTimes(5)
  verify.mockResolvedValueOnce(clean());await state.verifyNow([targetId]);expect(unref(state.issues).has(targetId)).toBe(false);state.dispose()
 })
 it('discards a clean older response after a newer Busy result',async()=>{
  const first=deferred<ReturnType<typeof clean>>(), second=deferred<{items:never[];errors:{targetId:string;reasonKey:string}[]}>()
  const verify=vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise), state=control.createModelControlVerifyState({verify})
  const a=state.verifyNow([targetId]),b=state.verifyNow([targetId]);second.resolve({items:[],errors:[{targetId,reasonKey:'admin.connectionHealth.errors.modelControlBusy'}]});await b
  first.resolve(clean());await a;expect(unref(state.issues).get(targetId)?.reasonKey).toContain('Busy');state.dispose()
 })
 it('only a clean verification issued after uncertainty clears its model and account freeze',async()=>{
  vi.useFakeTimers({now:new Date(now)});const late=deferred<ReturnType<typeof clean>>();const verify=vi.fn().mockReturnValueOnce(late.promise),state=control.createModelControlVerifyState({verify})
  const inFlight=state.verifyNow([targetId]);await vi.advanceTimersByTimeAsync(1);state.markUnconfirmed(targetId,'M');expect(state.accountUnconfirmed(targetId)).toBe(true)
  late.resolve(clean());await inFlight;expect(state.isUnconfirmed(targetId,'M')).toBe(true)
  await vi.advanceTimersByTimeAsync(1);verify.mockResolvedValueOnce({items:[],errors:[{targetId,reasonKey:'admin.connectionHealth.errors.modelControlBusy'}]});await state.verifyNow([targetId]);expect(state.accountUnconfirmed(targetId)).toBe(true)
  const pending=managed();pending.control.accountPending={modelName:'M',operation:'close',state:'unknown'};pending.control.pending={operation:'close',phase:'sending',receipt:'',state:'unknown',startedAt:now,sendStartedAt:now}
  verify.mockResolvedValueOnce({items:[pending],errors:[]});await state.verifyNow([targetId]);expect(state.accountUnconfirmed(targetId)).toBe(true)
  verify.mockResolvedValueOnce(clean());await state.verifyNow([targetId]);expect(state.accountUnconfirmed(targetId)).toBe(false);expect(state.isUnconfirmed(targetId,'M')).toBe(false);state.dispose()
 })
 it('does not infer completion when a successful response omits the uncertain model',async()=>{
  vi.useFakeTimers({now:new Date(now)});const verify=vi.fn().mockResolvedValue({items:[],errors:[]}),state=control.createModelControlVerifyState({verify})
  state.markUnconfirmed(targetId,'M');await vi.advanceTimersByTimeAsync(1);await state.verifyNow([targetId]);expect(state.accountUnconfirmed(targetId)).toBe(true);state.dispose()
 })
 it('makes late results inert after the containing view is disposed',async()=>{
  const late=deferred<ReturnType<typeof clean>>();const verify=vi.fn().mockReturnValueOnce(late.promise),state=control.createModelControlVerifyState({verify})
  const pending=state.verifyNow([targetId]);state.markUnconfirmed(targetId,'M');state.dispose();late.resolve(clean());await pending
  expect(state.accountUnconfirmed(targetId)).toBe(false);expect(unref(state.issues).size).toBe(0)
 })
})

describe('model cards and operation events follow the confirmed page contract',()=>{
 it.each([['unverified',''],['not_isolatable','AccountTypeUnsupported'],['not_isolatable','AccountReadFailed']] as const)('keeps %s/%s uncertain even with historical closed entries', (state,reason)=>{
  const item=managed();item.decision='usable';item.control.closedEntries={alias:'M'};item.control.observation.state=state;item.control.observation.reasonKey=reason ? `admin.connectionHealth.errors.modelControl${reason}` : ''
  const view=control.modelControlCardView(item,{availableModels:['M']});expect(view.label).toBe('主站状态无法确认');expect(view.tone).toBe('neutral');expect(view.stateSentence).not.toContain('开放中');expect(view.primaryAction).toBe('restore')
 })
 it.each([
  ['serving','close_recommended','',false,'close'],['serving','usable','',false,null],['partially_closed','close_recommended','',true,'close'],['closed','usable','',true,'restore'],['not_provided','usable','',false,'add'],['not_provided','no_evidence','',false,null],['closed','testing','',true,null],['account_missing','usable','',true,null],['closed','usable','conflict',true,null],['unverified','usable','',false,null],
 ] as const)('%s/%s shows at most its intended primary action', (state,decision,conflict,closed,action)=>{
  const item=managed();item.decision=decision;item.control.observation.state=state;item.control.conflictReason=conflict;if(closed)item.control.closedEntries={alias:'M'}
  const view=control.modelControlCardView(item,{availableModels:['M']});expect(view.primaryAction).toBe(action)
  if(state==='unverified')expect(view.showReread).toBe(true)
 })
 it('explains both entries and the reason in a real unknown-event structure',()=>{
  const result=control.modelControlEventLines({id:'e',targetId,modelName:'M',eventType:'add_unknown',actorUserId:'fixture',createdAt:now,basis:{},detail:{entries:[{key:'M',value:'M',state:'not_added'}],reasonKey:'admin.connectionHealth.errors.modelControlReadbackFailed'}})
  expect(result.sentence).not.toContain('ReadbackFailed');expect(result.sentence).not.toContain('{');expect(result.entries.join(' ')).toContain('M → M')
 })
 it('handles abandoned closed and unconfirmed mappings and caps entries at five',()=>{
  const entries=Object.fromEntries(Array.from({length:6},(_,i)=>[`alias-${i}`,'M']))
  const result=control.modelControlEventLines({id:'e',targetId,modelName:'M',eventType:'managed_abandoned',actorUserId:'fixture',createdAt:now,basis:{},detail:{closedEntries:entries,unconfirmedClose:{entries:{late:'M'}}}})
  expect(result.entries).toHaveLength(5);expect(result.more).toBe(2);expect(result.entries.every(line=>line.includes('仍关闭'))).toBe(true)
 })
})
