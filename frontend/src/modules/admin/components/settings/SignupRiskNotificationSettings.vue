<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { getSignupRiskPreferences, saveSignupRiskPreferences } from '../../api/notifications'
import { safeScopedErrorText } from '../../utils/scopedErrorText'
import { defaultSignupRiskSchedule, sameSignupRiskSettings, signupRiskTimeZones, validSignupRiskSchedule } from '../../utils/signupRiskSettings'
import type { SignupRiskPreferences } from '../../types/notifications'

const props = defineProps<{ workspaceId: string; platform: string; switching?: boolean }>()
const emit = defineEmits<{ saved: []; 'settings-changing': [value: boolean] }>()
const draft = ref({ enabled: false, ...defaultSignupRiskSchedule() })
const saved = shallowRef<SignupRiskPreferences | null>(null)
const confirmed = ref(false)
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const success = ref('')
const tick = ref(Date.now())
let receivedAt = Date.now()
let savedRequestStartedAt = receivedAt
let boundaryTimer: ReturnType<typeof setTimeout> | null = null
let epoch = 0
let request: AbortController | null = null
const dirty = computed(() => saved.value !== null && !sameSignupRiskSettings(saved.value, draft.value))
const valid = computed(() => validSignupRiskSchedule(draft.value))
const disabled = computed(() => !confirmed.value || loading.value || saving.value || props.switching || props.platform !== 'sub2api')
const cause = (failure: unknown) => failure instanceof Error
  ? failure.message === 'auth.errors.unauthorized' ? '登录状态已失效，请重新登录后读取提醒设置' : safeScopedErrorText(failure.message)
  : '具体原因尚无法确认，请维护人员核查 Hub 后端通知设置接口'
const savedClock = () => {
  if (!saved.value) return null
  const value = saved.value
  const offset = signupRiskTimeZones.find(zone => zone.value === value.timeZone)?.offset ?? 0
  const now = Date.parse(value.serverTime) + tick.value - receivedAt
  // The response may have spent time in transit. Start from the latest possible
  // receipt of serverTime, but end from the earliest possible receipt.
  const latestNow = Date.parse(value.serverTime) + tick.value - savedRequestStartedAt
  const local = new Date(now + offset * 3_600_000)
  const day = Date.UTC(local.getUTCFullYear(), local.getUTCMonth(), local.getUTCDate()) - offset * 3_600_000
  const minute = (clock: string) => Number(clock.slice(0, 2)) * 60 + Number(clock.slice(3))
  const start = day + minute(value.workStart) * 60_000
  const end = day + minute(value.workEnd) * 60_000
  return { now, latestNow, start, end }
}
const currentState = computed(() => {
  if (!confirmed.value || !saved.value) return '设置未能读取，当前是否开启尚无法确认'
  if (!saved.value.enabled) return '已关闭'
  const clock = savedClock()!
  return clock.now >= clock.start && clock.latestNow < clock.end ? '已开启，当前在工作时间内' : '已开启，当前不在工作时间内'
})
const clearBoundary = () => { if (boundaryTimer !== null) clearTimeout(boundaryTimer); boundaryTimer = null }
const scheduleBoundary = () => {
  clearBoundary()
  if (!confirmed.value || !saved.value?.enabled) return
  const clock = savedClock()!
  const delay = clock.now < clock.start ? clock.start - clock.now : clock.latestNow < clock.end ? clock.end - clock.latestNow : clock.start + 86_400_000 - clock.now
  boundaryTimer = setTimeout(() => { tick.value = Date.now(); scheduleBoundary() }, Math.max(1, delay))
}
const apply = (value: SignupRiskPreferences, startedAt: number, replaceDraft = true) => {
  saved.value = value; confirmed.value = true; savedRequestStartedAt = startedAt; receivedAt = Date.now(); tick.value = receivedAt
  if (replaceDraft) draft.value = { enabled: value.enabled, workStart: value.workStart, workEnd: value.workEnd, timeZone: value.timeZone }
  scheduleBoundary()
}
const load = async () => {
  if (!props.workspaceId || props.platform !== 'sub2api' || saving.value || props.switching) return
  request?.abort()
  const abort = new AbortController(); request = abort
  const version = epoch; const workspace = props.workspaceId
  loading.value = true; confirmed.value = false; error.value = success.value = ''; clearBoundary()
  try {
    const startedAt = Date.now()
    const result = await getSignupRiskPreferences(workspace, abort.signal)
    if (!abort.signal.aborted && version === epoch && workspace === props.workspaceId) apply(result, startedAt)
  } catch (failure) {
    if (!abort.signal.aborted && version === epoch) error.value = `读取高风险注册提醒设置失败：${cause(failure)}。原设置未被当作关闭，请重试读取设置后再保存。`
  } finally {
    if (!abort.signal.aborted && version === epoch) loading.value = false
  }
}
const buttonText = computed(() => saving.value ? '正在保存注册提醒设置' : !confirmed.value ? '保存注册提醒设置' : !dirty.value ? '设置已保存' : draft.value.enabled !== saved.value?.enabled ? draft.value.enabled ? '保存并开启提醒' : '保存并关闭提醒' : '保存工作时间')
const save = async () => {
  if (disabled.value || !dirty.value || !valid.value) return
  request?.abort()
  const abort = new AbortController(); request = abort
  const version = epoch; const workspace = props.workspaceId; const desired = { ...draft.value }
  const current = () => !abort.signal.aborted && version === epoch && workspace === props.workspaceId
  saving.value = true; success.value = error.value = ''; emit('settings-changing', true)
  try {
    let result: SignupRiskPreferences
    let startedAt = Date.now()
    try {
      result = await saveSignupRiskPreferences({ workspaceAdminAccountId: workspace, ...desired }, abort.signal)
    } catch (failure) {
      if (!current()) return
      try {
        startedAt = Date.now()
        result = await getSignupRiskPreferences(workspace, abort.signal)
        if (!current()) return
        if (!sameSignupRiskSettings(result, desired) || result.updatedAt === null) {
          apply(result, startedAt, false)
          error.value = `保存结果与本次修改不一致：${cause(failure)}。已重新读取实际设置，本页修改已保留；请核对后重新保存。`
          return
        }
      } catch (checkFailure) {
        if (current()) {
          confirmed.value = false; clearBoundary()
          error.value = `保存结果尚无法确认：${cause(failure)}。核对已保存设置也未成功：${cause(checkFailure)}。本页修改已保留；请重试读取设置，确认后再保存。`
        }
        return
      }
    }
    if (!current()) return
    apply(result, startedAt)
    success.value = '注册提醒设置已保存，仅影响当前工作区，不会修改主站。'
    emit('saved')
  } finally {
    if (current()) { saving.value = false; emit('settings-changing', false) }
  }
}
watch(draft, () => { success.value = '' }, { deep: true, flush: 'sync' })
const release = () => {
  epoch++; request?.abort(); request = null; clearBoundary()
  if (saving.value) { saving.value = false; emit('settings-changing', false) }
}
watch([() => props.workspaceId, () => props.platform, () => props.switching], () => {
  release()
  draft.value = { enabled: false, ...defaultSignupRiskSchedule() }; saved.value = null
  confirmed.value = loading.value = false; error.value = success.value = ''
  void load()
}, { immediate: true, flush: 'sync' })
onBeforeUnmount(release)
</script>

<template>
  <div class="space-y-3 border-t border-border/50 pt-4" aria-label="通知等级与开关">
    <h4 class="text-sm font-semibold">通知等级与开关</h4>
    <p class="text-sm font-medium">Sub2API 高风险注册提醒 <span class="text-xs text-muted-foreground">重要（弹窗提醒）</span></p>
    <label class="inline-flex cursor-pointer items-center gap-3 text-sm font-medium">
      <span class="relative inline-flex shrink-0 items-center">
        <input v-model="draft.enabled" type="checkbox" class="peer sr-only" aria-label="Sub2API 高风险注册提醒" :disabled="disabled">
        <span class="h-6 w-11 rounded-full bg-surface-elevated after:absolute after:left-[2px] after:top-[2px] after:h-5 after:w-5 after:rounded-full after:border after:border-border after:bg-white after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-full peer-checked:after:border-white peer-focus-visible:ring-2 peer-focus-visible:ring-primary peer-disabled:opacity-50" />
      </span>
      启用注册提醒
    </label>
    <div class="flex flex-wrap items-end gap-3">
      <div class="space-y-1"><label for="signup-risk-start" class="block text-sm">工作开始时间</label><Input id="signup-risk-start" v-model="draft.workStart" type="time" class="w-36" :disabled="disabled" /></div>
      <div class="space-y-1"><label for="signup-risk-end" class="block text-sm">工作结束时间</label><Input id="signup-risk-end" v-model="draft.workEnd" type="time" class="w-36" :disabled="disabled" /></div>
      <div class="space-y-1"><label for="signup-risk-zone" class="block text-sm">时区</label><select id="signup-risk-zone" v-model="draft.timeZone" class="h-10 rounded-lg border border-border/50 bg-surface-elevated px-3 text-sm disabled:opacity-50" :disabled="disabled"><option v-for="zone in signupRiskTimeZones" :key="zone.value" :value="zone.value">{{ zone.label }}</option></select></div>
    </div>
    <p class="text-xs text-muted-foreground">工作时间内，后台每1分钟检查主站，网页每1分钟读取新提醒。两个检查时间不同，正常时可能接近2分钟后显示；不会修改主站。</p>
    <p class="text-xs text-muted-foreground">每天按这里保存的时间执行，与主站的注册赠送时间无关。非工作时间不检查、不弹窗；下一工作时段补查夜间新增用户。</p>
    <p class="text-xs text-muted-foreground">首次开启或手动关闭后重开，会先登记已有用户，不补报关闭期间的注册；首次完整读取后开始提醒。</p>
    <p v-if="platform !== 'sub2api'" class="text-sm text-muted-foreground">这个工作区未连接 Sub2API，无法开启此提醒；请切换到 Sub2API 工作区。</p>
    <p v-else-if="loading" role="status" class="text-sm text-muted-foreground">正在读取注册提醒设置；读取完成后再编辑，不会修改主站。</p>
    <p v-else role="status" class="text-sm">{{ currentState }}</p>
    <p v-if="!confirmed && saved" class="text-xs text-muted-foreground">上次确认：{{ saved.enabled ? '已开启' : '已关闭' }}；当前保存结果请重新读取确认。</p>
    <p v-if="dirty" class="text-sm text-warning">有修改尚未保存</p>
    <p v-if="confirmed && !valid" role="alert" class="text-sm text-destructive">工作开始时间必须早于结束时间，不支持跨午夜；请填写00:00至23:59的时间并选择时区后保存。</p>
    <p v-if="error" role="alert" class="break-words text-sm text-destructive">{{ error }}</p>
    <p v-if="success" role="status" class="text-sm text-green-500">{{ success }}</p>
    <div class="flex flex-wrap gap-3"><Button :disabled="disabled || !dirty || !valid" @click="save">{{ buttonText }}</Button><Button variant="secondary" :disabled="loading || saving || switching || platform !== 'sub2api'" @click="load">{{ error ? '重试读取设置' : '重新读取设置' }}</Button></div>
    <p class="text-xs text-muted-foreground">重新读取会放弃本页尚未保存的修改，恢复已保存设置。</p>
  </div>
</template>
