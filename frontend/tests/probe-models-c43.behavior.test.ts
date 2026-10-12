// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import PolicyConfigDrawer from '@/modules/admin/components/dashboard/PolicyConfigDrawer.vue'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import { Tooltip } from '@/components/ui/tooltip'
import type { ConnectionHealthPolicy, PolicyInput, ModelHealth, AdminGroupUnprobedModel } from '@/modules/admin/types/connectionHealth'
import { c32Groups, c32Model } from './fixtures/c32ModelControl'

const label = '按顺序只测一个模型'
const help = '开启后，每个账号只测列表里启用的模型中的一个：从上往下，第一个主站确认这个账号有、并且没有被改名的；一个都确认不了时测最后一个启用的。OpenAI、Anthropic 的 API Key 账号，白名单为空时，或者是开了透传的 OpenAI 账号时，主站认为它什么模型都有，所以测第一个。不开启时，列表里启用的模型每个账号都测。个别账号如果主站单独给出了模型列表，开不开启都仍按原来的规则，只测两份列表里都有的模型。保存策略后生效。保存这一步不会改主站，之后的探活仍按现有规则处理账号。'
const upHelp = '把这个模型往上排一位，排在上面的优先测。只改这里的顺序，保存策略后生效；点它不会改主站。'
const downHelp = '把这个模型往下排一位，排在上面的优先测。只改这里的顺序，保存策略后生效；点它不会改主站。'
const fallbackHelp = '这个账号的主站白名单里没有排在前面的 A、B，或者被改了名，所以改测这个模型。想让它测更新的模型，请到主站确认排在前面的模型已经加进这个账号的白名单，并且没有被改名。'
const lastHelp = '没法从主站白名单确认这个账号有策略列表里的哪个模型，所以按列表里最后一个启用的模型测。可能是白名单里一个都没有或都被改了名、白名单读取失败，或账号不是 OpenAI、Anthropic 的 API Key 账号。请到主站检查这个账号的模型白名单。'
const wrappers: VueWrapper[] = []
const track = <T extends VueWrapper>(w:T):T => { wrappers.push(w); return w }
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); document.body.innerHTML = '' })
const policy = (probeMode = 'real_model'): ConnectionHealthPolicy => ({
 id:'p', name:'C43策略', enabled:true, ownGroupId:'', ownGroupName:'', modelPattern:'*', probeMode,
 probeIntervalSeconds:60, continueProbeWhenUnschedulable:true, unschedulableProbeIntervalMinutes:60,
 failureThreshold:3, successThreshold:2, cooldownSeconds:300, observationSeconds:300, recoveryStepPercent:25,
 autoDegradeEnabled:true, autoRemoteActionEnabled:false, priorityMode:'none', strategyMode:'health_probe',
 dailyProbeBudget:1000, createdAt:'', updatedAt:'',
 modelTargets:['A','B','C'].map((modelName, i) => ({id:modelName,policyId:'p',modelName,providerFamily:'openai',enabled:i!==1,probePrompt:'prompt-'+modelName,maxProbeTokens:1+i,createdAt:'',updatedAt:''})),
})
const drawer = async (platform='sub2api', mode='real_model', multiplier=false) => {
 const p=policy(mode);if(multiplier) p.strategyMode='multiplier_only'
 const w=track(mount(PolicyConfigDrawer,{props:{open:false,policy:p,ownGroupOptions:[],workspacePlatform:platform},global:{stubs:{Teleport:true,Transition:false}}}))
 await w.setProps({open:true});await flushPromises();return w
}
const save = async(w:VueWrapper):Promise<PolicyInput> => {await w.get('[data-testid="save-health-policy"]').trigger('click');return w.emitted('save')!.at(-1)![0] as PolicyInput}
const hoverText = async(w:VueWrapper, text:string) => {
 const tip = w.findAllComponents(Tooltip).find(x=>x.props('text')===text)
 if(!tip) throw new Error('missing tooltip '+text)
 await tip.trigger('mouseenter');await new Promise(resolve=>setTimeout(resolve,180));await flushPromises()
 // Teleport is stubbed for drawers and real for detail.
 expect(tip.text() + document.body.textContent).toContain(text)
 await tip.trigger('mouseleave');await flushPromises()
}

describe('C4.3 ordered probe policy drawer',()=>{
 it('loads saved mode, switches hints, and submits explicit enabled and disabled modes',async()=>{
  const w=await drawer('sub2api','first_available')
  expect((w.get('[data-testid="first-available-probe"]').element as HTMLInputElement).checked).toBe(true)
  expect(w.text()).toContain(label);expect(w.text()).toContain(help)
  expect(w.find('[data-testid="model-targets-help"]').exists()).toBe(false)
  expect((await save(w)).probeMode).toBe('first_available')
  await w.get('[data-testid="first-available-probe"]').setValue(false)
  expect(w.find('[data-testid="model-targets-help"]').exists()).toBe(true)
  expect(w.find('[data-testid="probe-model-up"]').exists()).toBe(false)
  expect(w.find('[data-testid="probe-model-down"]').exists()).toBe(false)
  expect((await save(w)).probeMode).toBe('real_model')
  await w.setProps({open:false,policy:policy('real_model')});await w.setProps({open:true})
  expect((w.get('[data-testid="first-available-probe"]').element as HTMLInputElement).checked).toBe(false)
  await w.get('[data-testid="first-available-probe"]').setValue(true)
  expect((await save(w)).probeMode).toBe('first_available')
 })
 it('moves complete model rows only in the draft and preserves disabled state, prompt, tokens and original policy',async()=>{
  const w=await drawer('sub2api','first_available'), original=JSON.parse(JSON.stringify(w.props('policy')))
  const ups=w.findAll('[data-testid="probe-model-up"]'),downs=w.findAll('[data-testid="probe-model-down"]')
  expect(ups[0]!.attributes()).toHaveProperty('disabled');expect(downs[2]!.attributes()).toHaveProperty('disabled')
  expect(ups[1]!.attributes()).not.toHaveProperty('disabled');expect(downs[0]!.attributes()).not.toHaveProperty('disabled')
  await hoverText(w,upHelp);await hoverText(w,downHelp)
  await downs[0]!.trigger('click');expect(w.emitted('save')).toBeUndefined()
  expect(w.props('policy')).toEqual(original)
  const submitted=await save(w)
  expect(submitted.modelTargets.map(x=>x.modelName)).toEqual(['B','A','C'])
  expect(submitted.modelTargets[0]).toMatchObject({enabled:false,probePrompt:'prompt-B',maxProbeTokens:2})
  await w.findAll('[data-testid="probe-model-up"]')[1]!.trigger('click')
  expect((await save(w)).modelTargets.map(x=>x.modelName)).toEqual(['A','B','C'])
 })
 it.each(['new-api','newapi',''])('hides the option and order buttons and omits mode for workspace %s',async platform=>{
  const w=await drawer(platform,'first_available')
  expect(w.text()).not.toContain(label);expect(w.text()).not.toContain(help)
  expect(w.find('[data-testid="first-available-probe"]').exists()).toBe(false)
  expect(w.find('[data-testid="probe-model-up"]').exists()).toBe(false)
  expect(w.find('[data-testid="probe-model-down"]').exists()).toBe(false)
  expect(w.find('[data-testid="model-targets-help"]').exists()).toBe(true)
  expect(await save(w)).not.toHaveProperty('probeMode')
 })
 it('hides probing options for multiplier-only and submits real_model with no targets',async()=>{
  const w=await drawer('sub2api','first_available',true)
  expect(w.find('[data-testid="first-available-probe"]').exists()).toBe(false)
  expect(w.find('[data-testid="probe-model-up"]').exists()).toBe(false)
  expect(await save(w)).toMatchObject({probeMode:'real_model',modelTargets:[]})
 })
})
const detail=async(kind:ModelHealth['modelSelection'], unprobed:boolean,workspacePlatform='sub2api',accountPlatform='openai')=>{
 const g=c32Groups()[0]!,a=g.accounts[0]!
 g.platform=accountPlatform;a.platform=accountPlatform
 const hint={modelSelection:kind,skippedModels:kind==='fallback'?['A','B']:undefined}
 a.modelHealth=unprobed?[]:[c32Model('C',hint)]
 a.unprobedModels=unprobed?[{modelName:'C',providerFamily:accountPlatform,...hint} as AdminGroupUnprobedModel]:[]
 const w=track(mount(AdminGroupHealthDetail,{props:{group:g,workspacePlatform,hideUnmonitoredAccounts:false,questionAnswerUnreadTargetIds:[],actionLoading:false}}))
 await w.get('tbody tr').findAll('button')[0]!.trigger('click');await flushPromises();return w
}
describe('C4.3 selected model explanation on existing health cards',()=>{
 it.each([false,true])('renders exact fallback tooltip and no last-unconfirmed tag; unprobed=%s',async unprobed=>{
  const w=await detail('fallback',unprobed)
  expect(w.get('[data-testid="probe-model-fallback"]').text()).toBe('顺延')
  expect(w.find('[data-testid="probe-model-last-unconfirmed"]').exists()).toBe(false)
  await hoverText(w,fallbackHelp)
 })
 it.each([false,true])('renders exact last-unconfirmed tooltip and no fallback tag; unprobed=%s',async unprobed=>{
  const w=await detail('last_unconfirmed',unprobed,'sub2api','anthropic')
  expect(w.get('[data-testid="probe-model-last-unconfirmed"]').text()).toBe('按最后一个测')
  expect(w.find('[data-testid="probe-model-fallback"]').exists()).toBe(false)
  await hoverText(w,lastHelp)
 })
 it.each([false,true])('renders no new tags for missing selection metadata; unprobed=%s',async unprobed=>{
  const w=await detail(undefined,unprobed)
  expect(w.find('[data-testid="probe-model-fallback"]').exists()).toBe(false)
  expect(w.find('[data-testid="probe-model-last-unconfirmed"]').exists()).toBe(false)
 })
 it.each(['fallback','last_unconfirmed'] as const)('suppresses %s in a non-Sub2API workspace even if data carries metadata',async kind=>{
  for(const unprobed of [false,true]) {
   const w=await detail(kind,unprobed,'new-api','openai')
   expect(w.find('[data-testid="probe-model-fallback"]').exists()).toBe(false)
   expect(w.find('[data-testid="probe-model-last-unconfirmed"]').exists()).toBe(false)
  }
 })
 it('keeps every new page sentence free from prohibited development terms',()=>{
  for(const sentence of [label,help,upHelp,downHelp,'顺延',fallbackHelp,'按最后一个测',lastHelp]) {
   expect(sentence).not.toMatch(/租约|回执|收口|对账|权威|矩阵|source=|schedulable=/)
  }
 })
})
